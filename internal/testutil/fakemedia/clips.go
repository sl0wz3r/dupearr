package fakemedia

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Loose clip sets: a flattened disc backup. Real libraries hold Blu-ray backups whose numbered
// STREAM clips lie loose in the movie folder — "movies/Elemental (2023)/00174.m2ts", "00175.m2ts"
// … without a BDMV/ folder (a copied STREAM folder, a ripper's flat output) — and DVD backups
// whose VOB/IFO/BUP files lie loose without VIDEO_TS/. Plex's default scanner treats every loose
// clip as an ordinary video: each becomes its own version (Media) of the movie item, so one backup
// shows up as a hundred "copies". A movie can span several clips, and a clip can be a warning, a
// menu loop, a trailer or a language variant of one scene: no clip can be judged or removed on its
// own — the clips of one folder are ONE copy.
//
// A [ClipSet] lays out such a folder: [Clip]s Plex lists (one version each, reported with
// container "ts" like PMS does for .m2ts) or does not list (Clip.Unlisted: on disk but not picked
// up), optional loose navigation files (Blu-ray: index.bdmv, MovieObject.bdmv, the main playlist
// LoosePlaylist and the .clpi of its clips, all valid; DVD: VIDEO_TS.IFO/.BUP and the title sets'
// VTS_NN_0.IFO/.BUP) and an optional Radarr import of one clip (Clip.Tracked, as after a rescan
// adopted the largest video: the folder name carries the year, so Radarr parses a loose clip).
//
// A Plex media delete or *arr file delete of an existing loose clip is recorded as a
// RuleDeleteLooseClip violation (RuleDeleteDiscMember for DVD and navigation file names, which are
// disc files anywhere); [Env.ClipDeletes] lists EVERY delete request that targeted a clip-named
// file, stale entries whose files are already gone included; [Env.DiscProblems] reports a set that
// is neither complete nor gone as a whole; [Env.ClipSetFixtures] returns the ground truth.

// Clip set kinds (ClipSet.Kind); the values are Dupearr's DiscInfo.Type values for loose sets.
const (
	ClipSetBluray = "bluray_clips"
	ClipSetDVD    = "dvd_clips"
)

// LoosePlaylist is the loose main-feature playlist written for ClipSet.Playlist.
const LoosePlaylist = "00800.mpls"

var (
	// reBlurayClip: a numbered Blu-ray/AVCHD STREAM clip, including the ".1" variant a second disc
	// flattened into the same folder produces (00004.m2ts next to 00004.1.m2ts) and the names a file
	// manager gives a clashing copy ("00800 (2).m2ts", "00800 - Copy.m2ts", "00800 2.m2ts"). The
	// oracle is written independently of internal/disc and must never be narrower than it.
	reBlurayClip = regexp.MustCompile(`(?i)^[0-9]{5}` + oracleCopyMarkers + `\.(?:m2ts|mts|m2t)$`)
	// rePlaylistClip: a clip a playlist can reference (5-digit id + .m2ts).
	rePlaylistClip = regexp.MustCompile(`(?i)^([0-9]{5})\.m2ts$`)
	// reDVDFile: a DVD-Video file name (VOB, IFO or BUP of the VMG or a title set).
	reDVDFile = regexp.MustCompile(`(?i)^(?:VIDEO_TS|VTS_[0-9]{2}_[0-9])` + oracleCopyMarkers + `\.(?:VOB|IFO|BUP)$`)
	// reDVDVOB: a DVD VOB (VIDEO_TS.VOB, VTS_NN_0.VOB menu, VTS_NN_1..9.VOB title).
	reDVDVOB = regexp.MustCompile(`(?i)^(?:VIDEO_TS|VTS_([0-9]{2})_([0-9]))\.VOB$`)
)

// oracleCopyMarkers: separators followed by "copy", "(N)" or N (up to three digits), any number of
// times — what a second disc or a file manager appends to a clashing clip name.
const oracleCopyMarkers = `(?:[ ._-]+(?:copy|\([0-9]{1,3}\)|[0-9]{1,3}))*`

// IsLooseClipName reports whether a file's base name is a disc clip name wherever the file lies: a
// numbered Blu-ray/AVCHD STREAM clip (00800.m2ts, 00004.1.m2ts, 00001.mts, 00001.m2t) or a DVD
// file (VIDEO_TS.VOB/.IFO/.BUP, VTS_01_1.VOB …). A standalone "Movie (2010).m2ts" or ".ts" is not.
func IsLooseClipName(name string) bool {
	return reBlurayClip.MatchString(name) || reDVDFile.MatchString(name)
}

// Clip is one loose file of a ClipSet.
type Clip struct {
	Name       string // base name: "00174.m2ts", "00004.1.m2ts" (Blu-ray) or "VTS_01_1.VOB" (DVD)
	Size       int64
	DurationMs int64 // required unless Unanalyzed
	// Video, Audio and Subtitles describe the clip; zero values are an extra's streams: 1080p
	// H.264 (Blu-ray) or 480-line MPEG-2 (DVD) with English AC-3 stereo.
	Video      Video
	Audio      []Audio
	Subtitles  []Subtitle
	Unanalyzed bool // Plex has not analyzed the clip (no duration, codecs or streams)
	// Unlisted leaves the clip on disk but out of Plex (not picked up by a scan yet).
	Unlisted bool
	// Tracked names the Radarr instance that tracks this clip (a listed clip; one per movie).
	Tracked string
	Quality string // *arr quality of the tracked clip ("" = from the name and width)
}

// ClipSet is a flattened disc backup: loose clips directly in a movie folder (Movie.ClipSets).
type ClipSet struct {
	Kind   string // ClipSetBluray or ClipSetDVD
	Folder string // the movie folder (media-root relative); the clips lie directly in it
	Clips  []Clip
	// Playlist (Blu-ray) writes valid loose navigation files: index.bdmv, MovieObject.bdmv,
	// LoosePlaylist playing these clips in order (5-digit .m2ts names of the set) and one .clpi per
	// clip of the playlist.
	Playlist []string
	// NavFiles (DVD) writes VIDEO_TS.IFO/.BUP and VTS_NN_0.IFO/.BUP describing the title sets of
	// the VOBs (their sizes must be multiples of 2048 bytes).
	NavFiles bool
	Age      time.Duration // Plex addedAt of the clips (0 = 30 days)
}

// ClipSetFixture is the ground truth about one loose clip set.
type ClipSetFixture struct {
	Title   string // the Plex movie
	Section string
	Kind    string
	// Folder / LocalFolder: the movie folder as the servers see it (/data/media/…) and locally.
	Folder, LocalFolder string
	RatingKey           string // the Plex item listing the clips ("" when Plex lists none)
	// Clips are the base names of every clip file (sorted); Listed / Unlisted split them by
	// whether Plex lists them (one version each).
	Clips, Listed, Unlisted []string
	// NavFiles are the base names of the loose navigation files (sorted).
	NavFiles []string
	// Files are the local paths a whole-set removal moves (every clip and navigation file, sorted)
	// and TotalSize their bytes; nothing else in the folder belongs to the set.
	Files     []string
	TotalSize int64
	// ListedSize is the size of the clips Plex lists (the size known without local access).
	ListedSize int64
	// MediaIDs are the current Plex media ids of the listed clips (one per clip, clip-name order).
	MediaIDs []int64
	// MainClip is the longest analyzed listed clip (ties: larger, then name) — the main-feature
	// candidate whose attributes describe the set; MainSize/MainDurationMs/MainVideo/MainAudio are its.
	MainClip       string
	MainSize       int64
	MainDurationMs int64
	MainVideo      Video
	MainAudio      []Audio
	// Playlist: the clips of the loose main playlist (Blu-ray). FeatureDurationMs: the main
	// feature's duration as the loose navigation files describe it — the playlist (Blu-ray) or the
	// first title set (DVD); 0 without navigation files.
	Playlist          []string
	FeatureDurationMs int64
	TrackedBy         string // instance tracking a clip ("" = none)
	TrackedPath       string // that clip as the *arr sees it
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// clipSet validates one clip set of a movie and returns the instances tracking one of its clips
// (counted by the caller against Radarr's one file per movie).
func (v *validator) clipSet(owner string, lib *Library, cs *ClipSet) []string {
	co := fmt.Sprintf("%s clip set %q", owner, cs.Folder)
	switch cs.Kind {
	case ClipSetBluray, ClipSetDVD:
	default:
		v.addf("%s: kind must be %q or %q", co, ClipSetBluray, ClipSetDVD)
		return nil
	}
	if !validRel(cs.Folder) {
		v.addf("%s: invalid folder (relative to the media root)", co)
		return nil
	}
	if isDiscMember(cs.Folder) {
		v.addf("%s: the folder lies inside a disc structure", co)
	}
	if lib != nil {
		ok := false
		for _, d := range lib.Dirs {
			ok = ok || (under(cs.Folder, d) && cs.Folder != d)
		}
		if !ok {
			v.addf("%s: the folder is not a movie folder inside a location of section %q", co, lib.Key)
		}
	}
	if by, ok := v.clipFolders[cs.Folder]; ok {
		v.addf("%s: the folder already holds the clip set of %s (one set per folder)", co, by)
	}
	v.clipFolders[cs.Folder] = co
	if len(cs.Clips) == 0 {
		v.addf("%s: at least one clip is required", co)
	}
	if cs.Age < 0 {
		v.addf("%s: negative age", co)
	}
	var insts []string
	names := map[string]bool{}
	for _, c := range cs.Clips {
		cn := fmt.Sprintf("%s clip %q", co, c.Name)
		switch {
		case cs.Kind == ClipSetBluray && !reBlurayClip.MatchString(c.Name):
			v.addf("%s: a Blu-ray clip is named NNNNN.m2ts (or .mts/.m2t, optionally NNNNN.1.m2ts)", cn)
		case cs.Kind == ClipSetDVD && !reDVDVOB.MatchString(c.Name):
			v.addf("%s: a DVD clip is a VOB (VIDEO_TS.VOB, VTS_NN_M.VOB); IFO/BUP come from NavFiles", cn)
		}
		if lc := strings.ToLower(c.Name); names[lc] {
			v.addf("%s: duplicate name (names are compared case-insensitively)", cn)
		} else {
			names[lc] = true
		}
		if c.Size <= 0 {
			v.addf("%s: size must be > 0", cn)
		}
		if c.DurationMs < 0 || (c.DurationMs == 0 && !c.Unanalyzed) {
			v.addf("%s: an analyzed clip needs a positive duration", cn)
		}
		if c.Tracked != "" {
			if c.Unlisted {
				v.addf("%s: an unlisted clip cannot be tracked", cn)
			} else if v.instance(cn, c.Tracked, KindRadarr) != nil {
				insts = append(insts, c.Tracked)
			}
		}
		v.clipFiles[cs.Folder+"/"+c.Name] = co
	}
	if len(cs.Playlist) > 0 {
		if cs.Kind != ClipSetBluray {
			v.addf("%s: Playlist is for Blu-ray sets (use NavFiles for a DVD)", co)
		}
		for _, n := range cs.Playlist {
			if !rePlaylistClip.MatchString(n) || !names[strings.ToLower(n)] {
				v.addf("%s: playlist clip %q must be a NNNNN.m2ts clip of the set", co, n)
			}
		}
		if d, ok := clipSetMainDisc(cs); ok {
			if err := checkDiscStreams(&d); err != nil {
				v.addf("%s: the playlist's streams: %v", co, err)
			}
		}
		for _, n := range clipSetNavNames(cs) {
			v.clipFiles[cs.Folder+"/"+n] = co
		}
	}
	if cs.NavFiles {
		if cs.Kind != ClipSetDVD {
			v.addf("%s: NavFiles is for DVD sets (use Playlist for a Blu-ray)", co)
		} else {
			v.dvdNav(co, cs)
		}
	}
	return insts
}

// dvdNav validates what the IFOs of a DVD clip set need.
func (v *validator) dvdNav(co string, cs *ClipSet) {
	titles := 0
	for _, c := range cs.Clips {
		m := reDVDVOB.FindStringSubmatch(c.Name)
		if m == nil {
			continue
		}
		if c.Size%dvdSector != 0 {
			v.addf("%s: VOB %q: size must be a multiple of %d bytes for NavFiles", co, c.Name, dvdSector)
		}
		if m[2] != "" && m[2] != "0" {
			titles++
			vid := clipVideo(ClipSetDVD, c)
			if vid.Codec != "mpeg2video" || vid.Height > 576 {
				v.addf("%s: VOB %q: a DVD title carries 480/576-line MPEG-2 video", co, c.Name)
			}
			for _, a := range clipAudio(c) {
				if _, ok := dvdAudioFormat(a.Codec); !ok {
					v.addf("%s: VOB %q: audio codec %q cannot be on a DVD", co, c.Name, a.Codec)
				}
			}
		}
	}
	if titles == 0 {
		v.addf("%s: NavFiles needs at least one title VOB (VTS_NN_1.VOB …)", co)
	}
	for _, n := range clipSetNavNames(cs) {
		v.clipFiles[cs.Folder+"/"+n] = co
	}
}

// clipOverlaps rejects clip set files that are also declared as version files or lie inside a
// disc's owned entries.
func (v *validator) clipOverlaps() {
	rels := make([]string, 0, len(v.clipFiles))
	for rel := range v.clipFiles {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		by := v.clipFiles[rel]
		if _, ok := v.files[rel]; ok {
			v.addf("%s: %q is also declared as a version file (describe loose clips with ClipSets only)", by, rel)
		}
		for o, dby := range v.discOwned {
			if under(rel, o) {
				v.addf("%s: %q lies inside the disc of %s", by, rel, dby)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Planning
// ---------------------------------------------------------------------------

// clipVideo is the video of a clip (zero value: an extra's).
func clipVideo(kind string, c Clip) Video {
	if c.Video != (Video{}) {
		return c.Video
	}
	if kind == ClipSetDVD {
		v := SD("mpeg2video", 720, 480)
		v.FrameRate = 29.97
		return v
	}
	return FHD("h264")
}

// clipAudio is the audio of a clip (nil: English AC-3 stereo).
func clipAudio(c Clip) []Audio {
	if c.Audio != nil {
		return c.Audio
	}
	return []Audio{AC3("eng", 2)}
}

// mainClipIndex returns the index of the main-feature candidate: the longest analyzed listed clip
// (ties: larger, then name); -1 when there is none.
func mainClipIndex(clips []Clip) int {
	best := -1
	for i, c := range clips {
		if c.Unlisted || c.Unanalyzed {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := clips[best]
		switch {
		case c.DurationMs != b.DurationMs:
			if c.DurationMs > b.DurationMs {
				best = i
			}
		case c.Size != b.Size:
			if c.Size > b.Size {
				best = i
			}
		case c.Name < b.Name:
			best = i
		}
	}
	return best
}

// clipByName returns the clip named name (case-insensitive).
func clipByName(clips []Clip, name string) (Clip, bool) {
	for _, c := range clips {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return Clip{}, false
}

// clipSetMainDisc is the disc whose main streams the loose playlist carries: the streams of the
// playlist's first clip.
func clipSetMainDisc(cs *ClipSet) (Disc, bool) {
	if len(cs.Playlist) == 0 {
		return Disc{}, false
	}
	first, ok := clipByName(cs.Clips, cs.Playlist[0])
	if !ok {
		return Disc{}, false
	}
	vid := clipVideo(ClipSetBluray, first)
	kind := DiscBluray
	if vid.Height >= 2160 {
		kind = DiscUHDBluray
	}
	return Disc{Kind: kind, Video: vid, Audio: clipAudio(first), Subtitles: first.Subtitles}, true
}

// clipSetNavNames are the navigation files a clip set writes (sorted).
func clipSetNavNames(cs *ClipSet) []string {
	var out []string
	switch {
	case cs.Kind == ClipSetBluray && len(cs.Playlist) > 0:
		out = append(out, "index.bdmv", "MovieObject.bdmv", LoosePlaylist)
		seen := map[string]bool{}
		for _, n := range cs.Playlist {
			if m := rePlaylistClip.FindStringSubmatch(n); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1]+".clpi")
			}
		}
	case cs.Kind == ClipSetDVD && cs.NavFiles:
		out = append(out, "VIDEO_TS.BUP", "VIDEO_TS.IFO")
		for _, ts := range dvdClipTitleSets(cs) {
			out = append(out, fmt.Sprintf("VTS_%02d_0.BUP", ts.number), fmt.Sprintf("VTS_%02d_0.IFO", ts.number))
		}
	}
	sort.Strings(out)
	return out
}

// dvdClipTitleSets groups the VOBs of a DVD clip set into title sets (VTS_NN_0.VOB = menu,
// VTS_NN_1..9.VOB = title), in title set order.
func dvdClipTitleSets(cs *ClipSet) []dvdTitleSet {
	byNum := map[int]*dvdTitleSet{}
	var nums []int
	for _, c := range cs.Clips {
		m := reDVDVOB.FindStringSubmatch(c.Name)
		if m == nil || m[1] == "" {
			continue
		}
		n := atoiDefault(m[1], 0)
		ts := byNum[n]
		if ts == nil {
			ts = &dvdTitleSet{number: n, chapters: 12}
			byNum[n] = ts
			nums = append(nums, n)
		}
		if m[2] == "0" {
			ts.menuVOB = c.Size
			continue
		}
		ts.vobs = append(ts.vobs, c.Size)
		ts.durationMs += c.DurationMs
		if ts.audio == nil {
			ts.audio, ts.subs = clipAudio(c), c.Subtitles
			ts.pal = clipVideo(ClipSetDVD, c).Height == 576
		}
	}
	sort.Ints(nums)
	out := make([]dvdTitleSet, 0, len(nums))
	for _, n := range nums {
		if ts := byNum[n]; len(ts.vobs) > 0 {
			ts.chapters = min(ts.chapters, 99)
			out = append(out, *ts)
		}
	}
	return out
}

// clipSetPlan is a clip set's complete layout.
type clipSetPlan struct {
	cs             ClipSet // clips sorted by name
	title, section string
	it             *item      // the Plex item (nil when Plex lists no clip)
	files          []discFile // unlisted clips and navigation files (written by materializeClipSets)
	nav            []string
	featureDurMs   int64 // the main feature per the navigation files (0 = none)
}

func (p *clipSetPlan) rel(name string) string { return p.cs.Folder + "/" + name }

// planClipSet lays out a (validated) clip set.
func planClipSet(cs ClipSet, title, section string) *clipSetPlan {
	cs.Clips = slices.Clone(cs.Clips)
	sort.SliceStable(cs.Clips, func(i, j int) bool { return cs.Clips[i].Name < cs.Clips[j].Name })
	p := &clipSetPlan{cs: cs, title: title, section: section, nav: clipSetNavNames(&cs)}
	for _, c := range cs.Clips {
		if c.Unlisted {
			p.files = append(p.files, discFile{rel: p.rel(c.Name), size: c.Size})
		}
	}
	switch {
	case cs.Kind == ClipSetBluray && len(cs.Playlist) > 0:
		p.planBlurayNav()
	case cs.Kind == ClipSetDVD && cs.NavFiles:
		p.planDVDNav()
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].rel < p.files[j].rel })
	return p
}

// planBlurayNav writes index.bdmv, MovieObject.bdmv, the main playlist and the .clpi files of its
// clips — the navigation files of the disc the clips came from, copied next to them.
func (p *clipSetPlan) planBlurayNav() {
	d, _ := clipSetMainDisc(&p.cs)
	main, _ := bdMainStreams(&d) // validated
	version, rate := bdVersionBD, uint32(6_000_000)
	if d.Kind == DiscUHDBluray {
		version, rate = bdVersionUHD, 13_500_000
	}
	var items []bdPlayItem
	written := map[string]bool{}
	for _, n := range p.cs.Playlist {
		c, _ := clipByName(p.cs.Clips, n)
		id := rePlaylistClip.FindStringSubmatch(c.Name)[1]
		end := uint32(bdClipStart + c.DurationMs*bdTicksPerMs)
		items = append(items, bdPlayItem{clip: id, inTime: bdClipStart, outTime: end, streams: main})
		p.featureDurMs += c.DurationMs
		if !written[id] {
			written[id] = true
			p.addNav(id+".clpi", encodeCLPI(version, bdClipInfo{
				recordingRate: rate, packets: uint32(min(c.Size/192, math.MaxUint32)),
				start: bdClipStart, end: end, streams: main,
			}))
		}
	}
	vs := main.video[0]
	p.addNav("index.bdmv", encodeIndex(bdIndexInfo{
		version: version, videoFormat: vs.format, frameRate: vs.rate, dynamicRange: vs.dynRange, titles: 1,
		hdrPlus: d.Video.HDR10Plus, dolbyVision: len(main.dv) > 0,
	}))
	p.addNav("MovieObject.bdmv", encodeMovieObjects(version, []bdMovieObject{{playlist: 800}, {playlist: 800}, {playlist: 800}}))
	p.addNav(LoosePlaylist, encodeMPLS(version, items, chapterMarks(items, 12)))
}

// planDVDNav writes the VMG and title set IFOs (and their BUP copies) of the VOBs.
func (p *clipSetPlan) planDVDNav() {
	sets := dvdClipTitleSets(&p.cs)
	if len(sets) > 0 {
		p.featureDurMs = sets[0].durationMs
	}
	var vmgMenu int64
	if c, ok := clipByName(p.cs.Clips, "VIDEO_TS.VOB"); ok {
		vmgMenu = c.Size
	}
	vmg := encodeVMGIFO(vmgMenu, sets)
	p.addNav("VIDEO_TS.IFO", vmg)
	p.addNav("VIDEO_TS.BUP", vmg)
	for _, ts := range sets {
		ifo := encodeVTSIFO(ts)
		p.addNav(fmt.Sprintf("VTS_%02d_0.IFO", ts.number), ifo)
		p.addNav(fmt.Sprintf("VTS_%02d_0.BUP", ts.number), ifo)
	}
}

func (p *clipSetPlan) addNav(name string, data []byte) {
	p.files = append(p.files, discFile{rel: p.rel(name), size: int64(len(data)), data: data})
}

// versions are the Plex versions of the listed clips: one per clip, like Plex's default scanner.
func (p *clipSetPlan) versions() []Version {
	container := "ts" // what PMS reports for a .m2ts
	if p.cs.Kind == ClipSetDVD {
		container = "mpeg" // UNVERIFIED: what PMS reports for a loose VOB
	}
	var out []Version
	for _, c := range p.cs.Clips {
		if c.Unlisted {
			continue
		}
		out = append(out, Version{
			Parts: []Part{{File: p.rel(c.Name), Size: c.Size, DurationMs: max(c.DurationMs, 1)}},
			Video: clipVideo(p.cs.Kind, c), Audio: clipAudio(c), Subtitles: c.Subtitles, Unanalyzed: c.Unanalyzed,
			Container: container, DurationMs: max(c.DurationMs, 1), Age: p.cs.Age,
			Tracked: c.Tracked, Quality: c.Quality,
		})
	}
	return out
}

// allRels are the media-root relative paths of every file of the set.
func (p *clipSetPlan) allRels() []string {
	out := make([]string, 0, len(p.cs.Clips)+len(p.nav))
	for _, c := range p.cs.Clips {
		out = append(out, p.rel(c.Name))
	}
	for _, n := range p.nav {
		out = append(out, p.rel(n))
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// World integration
// ---------------------------------------------------------------------------

// planMovieClipSets plans the clip sets of a movie and returns the Plex versions of their listed
// clips (also kept for the *arr build).
func (w *world) planMovieClipSets(m *Movie, it *item) []Version {
	var out []Version
	for _, cs := range m.ClipSets {
		p := planClipSet(cs, m.Title, m.Section)
		vs := p.versions()
		if len(vs) > 0 {
			p.it = it
		}
		w.clipSets = append(w.clipSets, p)
		out = append(out, vs...)
	}
	w.movieClips[m] = out
	return out
}

// materializeClipSets writes the unlisted clips and navigation files (the listed clips are
// version files, written by materialize).
func (w *world) materializeClipSets() error {
	for _, p := range w.clipSets {
		for _, f := range p.files {
			if err := writeDiscFile(w.local(f.rel), f); err != nil {
				return fmt.Errorf("create clip set file %s: %w", f.rel, err)
			}
		}
	}
	return nil
}

// recordClipDeletes remembers a Plex media delete or *arr file delete that targeted clip-named
// files (whether or not they still exist). Callers hold w.mu.
func (w *world) recordClipDeletes(server string, r *http.Request, rels []string) {
	var files []string
	existed := false
	for _, rel := range rels {
		if IsLooseClipName(path.Base(rel)) {
			files = append(files, remote(rel))
			existed = existed || w.fileExists(rel)
		}
	}
	if len(files) > 0 {
		sort.Strings(files)
		w.clipDeletes = append(w.clipDeletes, ClipDelete{Server: server, Method: r.Method, Path: r.URL.Path, Files: files, Existed: existed})
	}
}

// looseClipProblems lists clip sets that are neither complete nor gone as a whole.
func (w *world) looseClipProblems() []string {
	var out []string
	for _, p := range w.clipSets {
		rels := p.allRels()
		var missing []string
		for _, rel := range rels {
			fi, err := os.Lstat(w.local(rel))
			if err != nil || !fi.Mode().IsRegular() {
				missing = append(missing, remote(rel))
			}
		}
		if len(missing) > 0 && len(missing) < len(rels) {
			out = append(out, fmt.Sprintf("%s set %s of %q is incomplete: %d of %d files missing (first: %s)",
				p.cs.Kind, remote(p.cs.Folder), p.title, len(missing), len(rels), missing[0]))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Env API
// ---------------------------------------------------------------------------

// ClipDelete is a Plex media delete or *arr file delete that targeted clip-named files
// (IsLooseClipName: a numbered .m2ts/.mts/.m2t or a DVD VOB/IFO/BUP, loose or inside a disc).
type ClipDelete struct {
	Server, Method, Path string
	Files                []string // the clip-named files the request targeted, as the servers see them
	Existed              bool     // at least one of them was still on disk
}

func (c ClipDelete) String() string {
	state := "stale entry, files already gone"
	if c.Existed {
		state = "files on disk"
	}
	first := ""
	if len(c.Files) > 0 {
		first = c.Files[0]
	}
	return fmt.Sprintf("%s %s %s: %d clip file(s), first %s (%s)", c.Server, c.Method, c.Path, len(c.Files), first, state)
}

// ClipDeletes returns every delete request so far that targeted a clip-named file, stale entries
// included. The request is recorded whatever the servers answered (asking is what matters).
func (e *Env) ClipDeletes() []ClipDelete {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return slices.Clone(e.w.clipDeletes)
}

// ClipSetFixtures returns the ground truth about every loose clip set (scenario order).
func (e *Env) ClipSetFixtures() []ClipSetFixture {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	out := make([]ClipSetFixture, 0, len(e.w.clipSets))
	for _, p := range e.w.clipSets {
		f := ClipSetFixture{
			Title: p.title, Section: p.section, Kind: p.cs.Kind,
			Folder: remote(p.cs.Folder), LocalFolder: e.w.local(p.cs.Folder),
			NavFiles: slices.Clone(p.nav), Playlist: slices.Clone(p.cs.Playlist), FeatureDurationMs: p.featureDurMs,
		}
		listed := map[string]bool{}
		for _, c := range p.cs.Clips {
			f.Clips = append(f.Clips, c.Name)
			f.TotalSize += c.Size
			if c.Unlisted {
				f.Unlisted = append(f.Unlisted, c.Name)
				continue
			}
			f.Listed = append(f.Listed, c.Name)
			listed[p.rel(c.Name)] = true
			f.ListedSize += c.Size
			if c.Tracked != "" {
				f.TrackedBy = c.Tracked
				f.TrackedPath = remote(p.rel(c.Name))
			}
		}
		for _, nf := range p.files {
			if slices.Contains(p.nav, path.Base(nf.rel)) {
				f.TotalSize += nf.size
			}
		}
		for _, rel := range p.allRels() {
			f.Files = append(f.Files, e.w.local(rel))
		}
		if i := mainClipIndex(p.cs.Clips); i >= 0 {
			c := p.cs.Clips[i]
			f.MainClip, f.MainSize, f.MainDurationMs = c.Name, c.Size, c.DurationMs
			f.MainVideo, f.MainAudio = clipVideo(p.cs.Kind, c), slices.Clone(clipAudio(c))
		}
		if p.it != nil {
			f.RatingKey = p.it.rk
			for _, m := range p.it.media {
				if len(m.parts) == 1 && listed[m.parts[0].rel] {
					f.MediaIDs = append(f.MediaIDs, m.id)
				}
			}
		}
		out = append(out, f)
	}
	return out
}

// describeClipSets writes the clip set lines of Describe. Callers hold w.mu.
func (w *world) describeClipSets(write func(format string, args ...any)) {
	if len(w.clipSets) == 0 {
		return
	}
	write("\nLoose clip sets (%d; Plex lists every clip as its own version):\n", len(w.clipSets))
	for _, p := range w.clipSets {
		listed, unlisted := 0, 0
		var size int64
		for _, c := range p.cs.Clips {
			size += c.Size
			if c.Unlisted {
				unlisted++
			} else {
				listed++
			}
		}
		notes := []string{fmt.Sprintf("%d clip(s), %d Plex version(s)", len(p.cs.Clips), listed)}
		if unlisted > 0 {
			notes = append(notes, fmt.Sprintf("%d not in Plex", unlisted))
		}
		if len(p.nav) > 0 {
			notes = append(notes, fmt.Sprintf("%d navigation file(s)", len(p.nav)))
		}
		if i := mainClipIndex(p.cs.Clips); i >= 0 {
			c := p.cs.Clips[i]
			notes = append(notes, fmt.Sprintf("longest %s %.1f min", c.Name, float64(c.DurationMs)/60_000))
		}
		for _, c := range p.cs.Clips {
			if c.Tracked != "" {
				notes = append(notes, fmt.Sprintf("%s tracks %s", c.Tracked, c.Name))
			}
		}
		write("  %-12s %s (%.1f GiB; %s)\n", p.cs.Kind, remote(p.cs.Folder), float64(size)/(1<<30), strings.Join(notes, ", "))
	}
}
