package fakemedia

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Full-disc backups (docs/research/disc-structures.md). A [Disc] is a Blu-ray / UHD Blu-ray BDMV
// tree, a DVD VIDEO_TS tree or a disc image placed in a movie or season folder. The fake writes
// the complete structure — valid index.bdmv, MovieObject.bdmv, playlists, clip information files,
// IFOs, BACKUP/, CERTIFICATE/, AACS/ … and sparse stream files — and emulates how each server sees
// it:
//
//   - Plex's default scanners exclude BDMV/, VIDEO_TS/ and .iso/.img before matching, so a disc
//     is invisible: a movie folder with an MKV next to BDMV/ is one Plex version (the MKV), and a
//     disc-only folder has no Plex item at all.
//   - A movie library using the legacy "Plex Movie Scanner with Disc Image Support"
//     (Library.Scanner, Options.DiscImageScanner) exposes each disc as one version: one Part per
//     BDMV/STREAM clip (menus, trailers and extras included), VIDEO_TS.IFO plus the largest VOB
//     for a DVD, the image itself (unanalyzed) for an ISO. Discs of a multi-disc set become
//     versions of the same item (the merge is UNVERIFIED in real PMS); discs in extras/bonus
//     folders stay hidden (the scanner's ignore_dirs). Show libraries never expose discs.
//   - Radarr/Sonarr have no disc concept: a rescan never adopts a clip (the file name does not
//     parse), but Disc.Tracked emulates a manual in-place import of the main clip
//     (relativePath "BDMV/STREAM/00800.m2ts", quality BR-DISK), of the first title VOB of a DVD or
//     of a whole image.
//
// Removing a file of a disc through Plex or an *arr corrupts it: such requests are recorded as
// RuleDeleteDiscMember / RuleDeleteDiscImage violations, and [Env.AssertDiscsIntact] checks that
// every disc is either complete or gone as a whole. [Env.DiscFixtures] returns the ground truth
// (main playlist, duration, clip list, sizes, owned entries) tests compare Dupearr's findings to.

// Disc kinds (Disc.Kind); the values are Dupearr's DiscInfo.Type values.
const (
	DiscBluray    = "bluray"
	DiscUHDBluray = "uhd_bluray"
	DiscDVD       = "dvd"
	DiscISO       = "iso"
)

// Plex scanner names (Library.Scanner, reported as Directory.scanner by /library/sections).
const (
	ScannerMovie           = "Plex Movie"
	ScannerSeries          = "Plex TV Series"
	ScannerMovieDiscImage  = "Plex Movie Scanner with Disc Image Support"
	ScannerSeriesDiscImage = "Plex Series Scanner with Disc Image Support"
)

// Fixed names inside every generated disc (relative to the disc root).
const (
	// DiscMainPlaylist is the main-feature playlist of every Blu-ray fixture.
	DiscMainPlaylist = "BDMV/PLAYLIST/00800.mpls"
	// DiscMainClip is the first clip of the main feature — the one Disc.Tracked registers.
	DiscMainClip = "BDMV/STREAM/00800.m2ts"
	// DiscMainTitleSet is the main-feature title set of every DVD fixture.
	DiscMainTitleSet = "VIDEO_TS/VTS_01_0.IFO"
	// DiscMainVOB is the first VOB of the DVD main feature — the one Disc.Tracked registers.
	DiscMainVOB = "VIDEO_TS/VTS_01_1.VOB"
)

// Disc is a full-disc backup inside a movie folder (Movie.Discs) or a show folder (Show.Discs).
// Zero values pick realistic defaults for the kind.
type Disc struct {
	Kind string // DiscBluray, DiscUHDBluray, DiscDVD or DiscISO
	// Root is the disc root relative to the media root: the folder that holds BDMV/ or VIDEO_TS/
	// (the movie folder itself, a "Disc N" sub-folder of it, or a season folder), or the image file
	// for DiscISO. A disc root in an extras folder ("Bonus Disc", "Extras", …) is an extras disc:
	// never exposed by Plex and never part of a multi-disc set.
	Root string
	// FeatureSize is the size of the main feature: the sum of its clips (Blu-ray), of the main
	// title set's VOBs (DVD, at most 9 VOBs of 1 GiB) or the image (DVD/Blu-ray ISO). Menus,
	// extras, navigation files, BACKUP/, CERTIFICATE/ … come on top.
	// 0 = 58 GiB (UHD), 30 GiB (Blu-ray), 5.5 GiB (DVD), 40 GiB (ISO).
	FeatureSize int64
	DurationMs  int64 // main feature duration (0 = 2 h); unknown to everyone for an ISO
	// Video, Audio and Subtitles describe the main feature (written into the playlist's STN
	// table / the IFO attributes). Zero values: HDR10 HEVC 2160p + TrueHD Atmos (UHD), H.264 1080p
	// + DTS-HD MA 5.1 (Blu-ray), MPEG-2 720x480 + AC-3 5.1 (DVD). Languages are ISO 639-2 codes.
	Video     Video
	Audio     []Audio
	Subtitles []Subtitle
	// FeatureClips is the number of clips the main playlist strings together (Blu-ray; 0 = 5).
	// They are named 00800.m2ts, 00801.m2ts, … and differ in size.
	FeatureClips int
	// ExtraClips is the number of other clips in BDMV/STREAM (Blu-ray; 0 = 6, negative = none):
	// clip 00000 is a warning, 00001 a logo (both played by playlist 00000), 00002 a menu loop
	// (playlist 00001 repeats it until it is longer than the feature: a looping playlist a
	// "longest playlist" rule must skip), 00003…00011 trailers and featurettes (one playlist
	// each) and the rest small menu fragments no playlist references.
	ExtraClips int
	Chapters   int  // entry marks of the main playlist / programs of the DVD title (0 = 24)
	AACS       bool // Blu-ray: an AACS/ folder (original-disc copy)
	MakeMKV    bool // Blu-ray: a MAKEMKV/ folder (MakeMKV backup)
	EmptySSIF  bool // Blu-ray: an empty BDMV/STREAM/SSIF/ folder (not a 3D disc: SSIF holds no file)
	// Damaged truncates the playlists (Blu-ray) or IFOs (DVD), primary and BACKUP copies, so no
	// main feature can be read — an unreadable or half-written backup.
	Damaged bool
	// Tracked names the Radarr instance that tracks one file of the disc, as after a manual
	// in-place import: DiscMainClip (Blu-ray), DiscMainVOB (DVD) or the image (ISO). Movies only.
	Tracked string
	// Quality is the *arr quality of the tracked file ("" = BR-DISK for a Blu-ray clip, DVD for a
	// VOB, from the file name for an image: BR-DISK when it names a Blu-ray disc, else DVD).
	Quality string
	Age     time.Duration // Plex addedAt (disc-image scanner) and *arr dateAdded (0 = 30 days)
}

// DiscFixture is the ground truth about one disc the fake created, for tests to compare with what
// Dupearr detects. Paths under Root/LocalRoot use forward slashes relative to the root.
type DiscFixture struct {
	Title   string // title of the Plex movie / show whose folder holds the disc
	Section string // library section key
	Show    bool   // in a show (season) folder
	Kind    string
	// Root / LocalRoot: the disc root (folder holding BDMV/ or VIDEO_TS/, or the image file) as
	// the servers see it (/data/media/…) and on the local filesystem.
	Root, LocalRoot string
	// Folder is the movie (or season) folder that contains the disc, as the servers see it.
	Folder string
	// OwnedEntries are the local paths a whole-disc removal moves (sorted): the disc-owned entries
	// of the root (BDMV, CERTIFICATE, AACS, MAKEMKV; VIDEO_TS, AUDIO_TS), the whole folder for a
	// "Disc N" / extras root, the file for an image. Nothing else in the folder belongs to the disc.
	OwnedEntries []string
	SetNumber    int  // number within a multi-disc set ("Disc 2" → 2); 0 = not in a set
	SetSize      int  // discs in the set (1 = single disc)
	Extras       bool // an extras/bonus disc (never a version of the movie)
	Files        int  // regular files under the owned entries
	TotalSize    int64
	FeatureSize  int64    // sum of the distinct main-feature clips / title VOBs / the image
	MainFeature  string   // DiscMainPlaylist / DiscMainTitleSet; "" for images and damaged discs
	MainClips    []string // Blu-ray: main-feature clips in playback order (BDMV/STREAM/…)
	Clips        int      // Blu-ray: clips in BDMV/STREAM
	DurationMs   int64    // main feature duration; 0 = unknown (images, damaged discs)
	Chapters     int
	Video        Video
	Audio        []Audio
	Subtitles    []Subtitle
	Readable     bool   // the navigation files describe the main feature (false for images and damaged discs)
	TrackedBy    string // instance tracking a file of the disc ("" = none)
	TrackedPath  string // that file as the *arr sees it
	PlexVisible  bool   // a disc-image scanner exposes the disc as a Plex version
}

var (
	// Tier-1 disc membership (docs/research/disc-structures.md §6.2), applied to forward-slash
	// paths: a path component that belongs to a disc structure, a disc marker or flat-DVD file
	// name, or a disc-only extension. Deliberately a copy of the research, not of internal/disc.
	reDiscDir  = regexp.MustCompile(`(?i)(?:^|/)(?:BDMV|BDAV|VIDEO_TS|HVDVD_TS|AVCHD|AACS|CERTIFICATE|MAKEMKV|BDSVM|SLYVM|ANYVM|ADV_OBJ)(?:/|$)`)
	reDiscFile = regexp.MustCompile(`(?i)(?:^|/)(?:VIDEO_TS\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]\.(?:IFO|BUP|VOB)|index\.bdmv|MovieObject\.bdmv|INDEX\.BDM|MOVIEOBJ\.BDM|discatt\.dat)$`)
	reDiscExt  = regexp.MustCompile(`(?i)\.(?:mpls|clpi|bdmv|bdm|mpl|cpi|ssif|ifo|bup|evo)$`)

	// Multi-disc set folders ("Disc 1", "Movie - CD2", "part3"; bare "Disc 1" accepted) and
	// extras folders (Plex legacy ignore_dirs + Radarr extras names + Kodi "Bonus Disc").
	reDiscSetFolder = regexp.MustCompile(`(?i)^(?:.*?[ ._-])?(?:cd|dvd|dis[ck]|part|pt)[ ._-]*([0-9]{1,2})$`)
	reExtrasFolder  = regexp.MustCompile(`(?i)^(?:extras?|bonus.*|.*bonus dis[ck].*|special[ ._-]?features|featurettes|behind the scenes|deleted scenes|interviews|scenes|shorts|trailers|other|samples?)$`)
	reImageExt      = regexp.MustCompile(`(?i)\.(?:iso|img)$`)
	// reBRDiskName matches image names that identify a Blu-ray disc (Radarr's BR-DISK tokens).
	reBRDiskName = regexp.MustCompile(`(?i)(?:br-?disk|full[ ._-]?blu-?ray|\b(?:bd|uhd)[ ._-]?(?:25|50|66|100)\b|blu-?ray)`)
)

// isDiscMember reports whether a (forward-slash) path lies inside a disc structure or is a disc
// navigation file: a per-file removal of such a path corrupts the disc.
func isDiscMember(p string) bool {
	return reDiscDir.MatchString(p) || reDiscFile.MatchString(p) || reDiscExt.MatchString(p)
}

// discFolderRoot reports whether a disc root is a whole sub-folder that belongs to the disc
// ("Disc 2", "Bonus Disc"): such a folder is owned (and moved) as a whole.
func discFolderRoot(root string) bool {
	base := path.Base(root)
	return reDiscSetFolder.MatchString(base) || reExtrasFolder.MatchString(base)
}

// discFolder returns the movie/season folder that contains a disc.
func discFolder(d *Disc) string {
	if d.Kind == DiscISO || discFolderRoot(d.Root) {
		return path.Dir(d.Root)
	}
	return d.Root
}

// discOwned returns the media-root relative entries a whole-disc removal moves.
func discOwned(d *Disc) []string {
	switch {
	case d.Kind == DiscISO, discFolderRoot(d.Root):
		return []string{d.Root}
	case d.Kind == DiscDVD:
		return []string{d.Root + "/AUDIO_TS", d.Root + "/VIDEO_TS"}
	}
	out := []string{d.Root + "/BDMV", d.Root + "/CERTIFICATE"}
	if d.AACS {
		out = append(out, d.Root+"/AACS")
	}
	if d.MakeMKV {
		out = append(out, d.Root+"/MAKEMKV")
	}
	sort.Strings(out)
	return out
}

func isBluray(kind string) bool { return kind == DiscBluray || kind == DiscUHDBluray }

// withDefaults fills the zero values of a disc.
func (d Disc) withDefaults() Disc {
	switch d.Kind {
	case DiscUHDBluray:
		d.FeatureSize = cmp.Or(d.FeatureSize, GiB(58))
		if d.Video == (Video{}) {
			d.Video = HDR10UHD()
		}
		if d.Audio == nil {
			d.Audio = []Audio{TrueHDAtmos("eng")}
		}
	case DiscBluray:
		d.FeatureSize = cmp.Or(d.FeatureSize, GiB(30))
		if d.Video == (Video{}) {
			d.Video = FHD("h264")
		}
		if d.Audio == nil {
			d.Audio = []Audio{DTSHDMA("eng", 6)}
		}
	case DiscDVD:
		d.FeatureSize = cmp.Or(d.FeatureSize, GiB(5.5))
		if d.Video == (Video{}) {
			d.Video = SD("mpeg2video", 720, 480)
			d.Video.FrameRate = 29.97
		}
		if d.Audio == nil {
			d.Audio = []Audio{AC3("eng", 6)}
		}
	case DiscISO:
		d.FeatureSize = cmp.Or(d.FeatureSize, GiB(40))
	}
	d.DurationMs = cmp.Or(d.DurationMs, Mins(120))
	d.FeatureClips = cmp.Or(d.FeatureClips, 5)
	switch {
	case d.ExtraClips == 0:
		d.ExtraClips = 6
	case d.ExtraClips < 0:
		d.ExtraClips = 0
	}
	d.Chapters = cmp.Or(d.Chapters, 24)
	return d
}

// ---------------------------------------------------------------------------
// Stream mapping (scenario presets → BDMV stream attributes)
// ---------------------------------------------------------------------------

func bdFrameRate(fps float64) (uint8, bool) {
	for _, r := range []struct {
		fps  float64
		code uint8
	}{{0, 1}, {23.976, 1}, {24, 2}, {25, 3}, {29.97, 4}, {50, 6}, {59.94, 7}} {
		if math.Abs(fps-r.fps) < 0.01 {
			return r.code, true
		}
	}
	return 0, false
}

// bdVideoStream maps the main-feature video to its primary video stream.
func bdVideoStream(v Video) (bdStream, error) {
	s := bdStream{pid: bdPIDVideo}
	switch v.Codec {
	case "hevc":
		s.coding = bdCodingHEVC
	case "h264":
		s.coding = bdCodingH264
	case "vc1":
		s.coding = bdCodingVC1
	case "mpeg2video":
		s.coding = bdCodingMPEG2
	default:
		return s, fmt.Errorf("video codec %q cannot be on a Blu-ray (hevc, h264, vc1, mpeg2video)", v.Codec)
	}
	switch {
	case v.Height >= 2160:
		s.format = bdVideo2160p
	case v.Height >= 1080:
		s.format = bdVideo1080p
	case v.Height >= 720:
		s.format = bdVideo720p
	case v.Height >= 576:
		s.format = bdVideo576p
	default:
		s.format = bdVideo480p
	}
	rate, ok := bdFrameRate(v.FrameRate)
	if !ok {
		return s, fmt.Errorf("frame rate %g cannot be on a Blu-ray", v.FrameRate)
	}
	s.rate = rate
	if s.coding == bdCodingHEVC {
		s.colorSpace = bdColorSpaceBT709
		if v.ColorPrimaries == "bt2020" || v.ColorSpace == "bt2020nc" {
			s.colorSpace = bdColorSpaceBT2020
		}
		switch {
		case v.ColorTrc == "smpte2084":
			s.dynRange = bdDynamicRangeHDR10
		case v.DOVIProfile > 0:
			s.dynRange = bdDynamicRangeDV
		default:
			s.dynRange = bdDynamicRangeSDR
		}
		s.hdrPlus = v.HDR10Plus
	}
	return s, nil
}

// bdAudioStream maps an audio track to a primary audio stream.
func bdAudioStream(a Audio, i int) (bdStream, error) {
	s := bdStream{pid: uint16(bdPIDAudio + i), rate: bdAudio48kHz, lang: strings.ToLower(a.LanguageCode)}
	p := strings.ToLower(a.Profile)
	switch {
	case a.Codec == "truehd":
		s.coding = bdCodingTrueHD
	case (a.Codec == "dca" || a.Codec == "dts") && strings.Contains(p, "ma"):
		s.coding = bdCodingDTSHDMA
	case (a.Codec == "dca" || a.Codec == "dts") && strings.Contains(p, "hra"):
		s.coding = bdCodingDTSHRA
	case a.Codec == "dca" || a.Codec == "dts":
		s.coding = bdCodingDTS
	case a.Codec == "eac3":
		s.coding = bdCodingEAC3
	case a.Codec == "ac3":
		s.coding = bdCodingAC3
	case a.Codec == "pcm":
		s.coding = bdCodingLPCM
	default:
		return s, fmt.Errorf("audio codec %q cannot be on a Blu-ray (truehd, dca, eac3, ac3, pcm)", a.Codec)
	}
	switch {
	case a.Channels <= 1:
		s.format = bdAudioMono
	case a.Channels == 2:
		s.format = bdAudioStereo
	default:
		s.format = bdAudioMulti
	}
	if len(s.lang) != 3 {
		return s, fmt.Errorf("audio language %q must be an ISO 639-2 code", a.LanguageCode)
	}
	return s, nil
}

// bdMainStreams maps a disc's main feature to its STN streams (video, Dolby Vision enhancement
// layer, audio, PG subtitles and one IG menu stream).
func bdMainStreams(d *Disc) (bdStreams, error) {
	var s bdStreams
	v, err := bdVideoStream(d.Video)
	if err != nil {
		return s, err
	}
	s.video = []bdStream{v}
	if d.Video.DOVIProfile > 0 && v.coding == bdCodingHEVC {
		el := v
		el.pid, el.format, el.dynRange = bdPIDDVEL, bdVideo1080p, bdDynamicRangeDV
		s.dv = []bdStream{el}
	}
	for i, a := range d.Audio {
		as, err := bdAudioStream(a, i)
		if err != nil {
			return s, err
		}
		s.audio = append(s.audio, as)
	}
	for i, sub := range d.Subtitles {
		lang := strings.ToLower(sub.LanguageCode)
		if len(lang) != 3 {
			return s, fmt.Errorf("subtitle language %q must be an ISO 639-2 code", sub.LanguageCode)
		}
		s.pg = append(s.pg, bdStream{pid: uint16(bdPIDPG + i), coding: bdCodingPG, lang: lang})
	}
	menuLang := "eng"
	if len(s.audio) > 0 {
		menuLang = s.audio[0].lang
	}
	s.ig = []bdStream{{pid: bdPIDIG, coding: bdCodingIG, lang: menuLang}}
	return s, nil
}

// bdExtraStreams are the streams of warnings, logos, menus and extras: 1080p H.264 + AC-3 stereo.
func bdExtraStreams() bdStreams {
	return bdStreams{
		video: []bdStream{{pid: bdPIDVideo, coding: bdCodingH264, format: bdVideo1080p, rate: 1}},
		audio: []bdStream{{pid: bdPIDAudio, coding: bdCodingAC3, format: bdAudioStereo, rate: bdAudio48kHz, lang: "eng"}},
	}
}

// checkDiscStreams reports why a disc's main feature cannot be encoded (nil when it can).
func checkDiscStreams(d *Disc) error {
	switch {
	case isBluray(d.Kind):
		if _, err := bdMainStreams(d); err != nil {
			return err
		}
		if d.Kind == DiscUHDBluray && (d.Video.Codec != "hevc" || d.Video.Height < 2160) {
			return fmt.Errorf("a UHD Blu-ray carries 2160p HEVC video, not %dp %s", d.Video.Height, d.Video.Codec)
		}
	case d.Kind == DiscDVD:
		if d.Video.Codec != "mpeg2video" || d.Video.Height > 576 {
			return fmt.Errorf("a DVD carries 480/576-line MPEG-2 video, not %dp %s", d.Video.Height, d.Video.Codec)
		}
		if len(d.Audio) > 8 || len(d.Subtitles) > 32 {
			return fmt.Errorf("a DVD title has at most 8 audio and 32 subtitle streams")
		}
		for _, a := range d.Audio {
			if _, ok := dvdAudioFormat(a.Codec); !ok {
				return fmt.Errorf("audio codec %q cannot be on a DVD (ac3, dca, pcm, mp2)", a.Codec)
			}
		}
		if d.Chapters > 99 {
			return fmt.Errorf("a DVD title has at most 99 chapters")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Planning: the files of a disc (pure, deterministic)
// ---------------------------------------------------------------------------

// discFile is one file of a disc tree: size bytes, with data written at offset at (the rest is
// sparse).
type discFile struct {
	rel  string // media-root relative
	size int64
	data []byte
	at   int64
}

// discClip is one BDMV/STREAM clip.
type discClip struct {
	id      string
	size    int64
	durMs   int64
	feature bool
}

// discPlan is a disc's complete layout plus the facts the fixture reports.
type discPlan struct {
	d       Disc // defaults applied
	title   string
	section string
	show    bool
	folder  string
	owned   []string
	dirs    []string // media-root relative directories (including empty ones)
	files   []discFile
	clips   []discClip // Blu-ray, in name order

	mainFeature string
	mainClips   []string
	readable    bool
	extras      bool
	setNumber   int
	setSize     int
	plexVisible bool

	trackedRel   string
	trackedSize  int64
	trackedDurMs int64
	dvdIFO       discFile // DVD: VIDEO_TS.IFO and the largest VOB (the disc-image scanner's Parts)
	dvdLargest   discFile
}

// totalSize returns the bytes under the disc's owned entries.
func (p *discPlan) totalSize() int64 {
	var n int64
	for _, f := range p.files {
		n += f.size
	}
	return n
}

// planDisc lays out a (validated) disc.
func planDisc(d Disc, title, section string, show bool) *discPlan {
	d = d.withDefaults()
	p := &discPlan{d: d, title: title, section: section, show: show, folder: discFolder(&d), owned: discOwned(&d)}
	p.extras = reExtrasFolder.MatchString(path.Base(d.Root)) && d.Kind != DiscISO
	switch d.Kind {
	case DiscDVD:
		p.planDVD()
	case DiscISO:
		off, data := isoDescriptors(title, d.FeatureSize)
		p.files = []discFile{{rel: d.Root, size: d.FeatureSize, data: data, at: off}}
		p.trackedRel, p.trackedSize, p.trackedDurMs = d.Root, d.FeatureSize, d.DurationMs
	default:
		p.planBluray()
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].rel < p.files[j].rel })
	return p
}

// weights spreads n shares unevenly (so the largest clip is not the first).
func weights(n int) []int64 {
	w := make([]int64, n)
	for i := range w {
		w[i] = int64(4 + (i*5)%7)
	}
	return w
}

// split divides total by weights; the remainder goes to the last share.
func split(total int64, w []int64) []int64 {
	var sum, used int64
	for _, x := range w {
		sum += x
	}
	out := make([]int64, len(w))
	for i, x := range w {
		if i == len(w)-1 {
			out[i] = total - used
			break
		}
		out[i] = total / sum * x
		used += out[i]
	}
	return out
}

// extraClip returns the size and duration of extra clip i (see Disc.ExtraClips).
func extraClip(i int) (size, durMs int64) {
	switch {
	case i == 0:
		return MiB(6), 15_000 // anti-piracy warning
	case i == 1:
		return MiB(9), 20_000 // studio logo
	case i == 2:
		return MiB(48), 90_000 // menu background loop
	case i < 12:
		return MiB(float64(120 + 45*i)), int64(90_000 + 20_000*i) // trailers, featurettes
	default:
		return int64(512+(i*389)%3584) << 10, int64(2_000 + (i*733)%7_000) // menu fragments
	}
}

const (
	bdClipStart  = 27_000_000 // IN_time of every clip (10 minutes, 45 kHz)
	bdTicksPerMs = 45
)

func (p *discPlan) planBluray() {
	d := &p.d
	root := d.Root
	version := bdVersionBD
	rate := uint32(6_000_000) // TS_recording_rate: 48 Mbit/s
	if d.Kind == DiscUHDBluray {
		version, rate = bdVersionUHD, 13_500_000 // 108 Mbit/s
	}
	main, _ := bdMainStreams(d) // validated
	extra := bdExtraStreams()

	w := weights(d.FeatureClips)
	sizes, durs := split(d.FeatureSize, w), split(d.DurationMs, w)
	var feature []bdPlayItem
	for i := range w {
		c := discClip{id: fmt.Sprintf("%05d", 800+i), size: sizes[i], durMs: durs[i], feature: true}
		p.clips = append(p.clips, c)
		feature = append(feature, bdPlayItem{clip: c.id, inTime: bdClipStart, outTime: uint32(bdClipStart + c.durMs*bdTicksPerMs), streams: main})
		p.mainClips = append(p.mainClips, "BDMV/STREAM/"+c.id+".m2ts")
	}
	var extras []discClip
	for i := 0; i < d.ExtraClips; i++ {
		size, dur := extraClip(i)
		extras = append(extras, discClip{id: fmt.Sprintf("%05d", i), size: size, durMs: dur})
	}
	p.clips = append(extras, p.clips...)

	item := func(c discClip) bdPlayItem {
		return bdPlayItem{clip: c.id, inTime: bdClipStart, outTime: uint32(bdClipStart + c.durMs*bdTicksPerMs), streams: extra}
	}
	type playlist struct {
		num   int
		items []bdPlayItem
		marks []bdMark
	}
	playlists := []playlist{{num: 800, items: feature, marks: chapterMarks(feature, d.Chapters)}}
	if len(extras) > 0 { // warning (+ logo)
		pl := playlist{num: 0, items: []bdPlayItem{item(extras[0])}}
		if len(extras) > 1 {
			pl.items = append(pl.items, item(extras[1]))
		}
		pl.marks = []bdMark{{item: 0, time: bdClipStart}}
		playlists = append(playlists, pl)
	}
	if len(extras) > 2 { // looping menu: longer than the feature, one segment repeated
		loop := extras[2]
		repeats := max(3, int((d.DurationMs*5/4)/loop.durMs)+1)
		pl := playlist{num: 1, marks: []bdMark{{item: 0, time: bdClipStart}}}
		for range repeats {
			pl.items = append(pl.items, item(loop))
		}
		playlists = append(playlists, pl)
	}
	for i := 3; i < min(len(extras), 12); i++ { // trailers and featurettes
		playlists = append(playlists, playlist{num: i - 1, items: []bdPlayItem{item(extras[i])}, marks: []bdMark{{item: 0, time: bdClipStart}}})
	}
	sort.Slice(playlists, func(i, j int) bool { return playlists[i].num < playlists[j].num })

	objects := []bdMovieObject{{playlist: 800}, {playlist: 800}, {playlist: 800}} // first play, top menu, title 1
	if len(extras) > 0 {
		objects[0].playlist = 0
	}
	if len(extras) > 2 {
		objects[1].playlist = 1
	}
	vs := main.video[0]
	index := encodeIndex(bdIndexInfo{
		version: version, videoFormat: vs.format, frameRate: vs.rate, dynamicRange: vs.dynRange, titles: 1,
		hdrPlus: d.Video.HDR10Plus, dolbyVision: len(main.dv) > 0,
	})
	mobj := encodeMovieObjects(version, objects)

	add := func(rel string, data []byte) {
		p.files = append(p.files, discFile{rel: root + "/" + rel, size: int64(len(data)), data: data})
	}
	for _, dir := range []string{"BDMV", "BDMV/BACKUP"} {
		add(dir+"/index.bdmv", index)
		add(dir+"/MovieObject.bdmv", mobj)
		for _, pl := range playlists {
			data := encodeMPLS(version, pl.items, pl.marks)
			if d.Damaged {
				data = data[:16] // header only: the playlist and mark blocks are past the end
			}
			add(fmt.Sprintf("%s/PLAYLIST/%05d.mpls", dir, pl.num), data)
		}
		for _, c := range p.clips {
			streams := extra
			if c.feature {
				streams = main
			}
			add(dir+"/CLIPINF/"+c.id+".clpi", encodeCLPI(version, bdClipInfo{
				recordingRate: rate, packets: uint32(min(c.size/192, math.MaxUint32)),
				start: bdClipStart, end: uint32(bdClipStart + c.durMs*bdTicksPerMs), streams: streams,
			}))
		}
	}
	for _, c := range p.clips {
		p.files = append(p.files, discFile{rel: root + "/BDMV/STREAM/" + c.id + ".m2ts", size: c.size, data: m2tsHead()})
	}
	add("BDMV/META/DL/bdmt_eng.xml", discTitleXML(p.title))
	id := encodeDiscID(version, root)
	add("CERTIFICATE/id.bdmv", id)
	add("CERTIFICATE/BACKUP/id.bdmv", id)
	sparse := func(rel string, size int64) { p.files = append(p.files, discFile{rel: root + "/" + rel, size: size}) }
	if d.AACS {
		sparse("AACS/MKB_RO.inf", MiB(1))
		sparse("AACS/Unit_Key_RO.inf", 4096)
		sparse("AACS/Content000.cer", 92)
		sparse("AACS/ContentHash000.tbl", 64*1024)
		sparse("AACS/CPSUnit00001.cci", 2048)
		sparse("AACS/DUPLICATE/MKB_RO.inf", MiB(1))
	}
	if d.MakeMKV {
		sparse("MAKEMKV/discatt.dat", 40*1024) // contents UNVERIFIED (docs/research §4.1)
	}
	for _, dir := range []string{"BDMV/AUXDATA", "BDMV/BDJO", "BDMV/JAR", "BDMV/BACKUP/BDJO"} {
		p.dirs = append(p.dirs, root+"/"+dir)
	}
	if d.EmptySSIF {
		p.dirs = append(p.dirs, root+"/BDMV/STREAM/SSIF")
	}
	p.readable = !d.Damaged
	if p.readable {
		p.mainFeature = DiscMainPlaylist
	}
	first := p.clips[len(extras)]
	p.trackedRel, p.trackedSize, p.trackedDurMs = root+"/"+DiscMainClip, first.size, first.durMs
}

// chapterMarks spreads n entry marks evenly over the play items.
func chapterMarks(items []bdPlayItem, n int) []bdMark {
	var total int64
	for _, it := range items {
		total += int64(it.outTime - it.inTime)
	}
	var marks []bdMark
	for k := 0; k < n; k++ {
		t := total * int64(k) / int64(n)
		for i, it := range items {
			d := int64(it.outTime - it.inTime)
			if t < d || i == len(items)-1 {
				marks = append(marks, bdMark{item: uint16(i), time: it.inTime + uint32(min(t, d))})
				break
			}
			t -= d
		}
	}
	return marks
}

func (p *discPlan) planDVD() {
	d := &p.d
	root := d.Root + "/VIDEO_TS/"
	pal := d.Video.Height == 576
	var vobs []int64
	for left := d.FeatureSize / dvdSector * dvdSector; left > 0; left -= min(left, dvdVOBMax) {
		vobs = append(vobs, min(left, dvdVOBMax))
	}
	main := dvdTitleSet{
		number: 1, menuVOB: MiB(12), vobs: vobs, durationMs: d.DurationMs, chapters: d.Chapters, pal: pal,
		audio: d.Audio, subs: d.Subtitles,
	}
	trailer := dvdTitleSet{
		number: 2, vobs: []int64{MiB(180)}, durationMs: 180_000, chapters: 1, pal: pal,
		audio: []Audio{AC3("eng", 2)},
	}
	sets := []dvdTitleSet{main, trailer}
	add := func(name string, data []byte) discFile {
		if d.Damaged {
			data = data[:12] // identifier only
		}
		f := discFile{rel: root + name, size: int64(len(data)), data: data}
		p.files = append(p.files, f)
		return f
	}
	vob := func(name string, size int64) discFile {
		f := discFile{rel: root + name, size: size, data: vobHead()}
		p.files = append(p.files, f)
		return f
	}
	vmg := encodeVMGIFO(MiB(2), sets)
	p.dvdIFO = add("VIDEO_TS.IFO", vmg)
	add("VIDEO_TS.BUP", vmg)
	vob("VIDEO_TS.VOB", MiB(2))
	for _, ts := range sets {
		ifo := encodeVTSIFO(ts)
		add(fmt.Sprintf("VTS_%02d_0.IFO", ts.number), ifo)
		add(fmt.Sprintf("VTS_%02d_0.BUP", ts.number), ifo)
		if ts.menuVOB > 0 {
			vob(fmt.Sprintf("VTS_%02d_0.VOB", ts.number), ts.menuVOB)
		}
		for i, size := range ts.vobs {
			f := vob(fmt.Sprintf("VTS_%02d_%d.VOB", ts.number, i+1), size)
			if f.size > p.dvdLargest.size {
				p.dvdLargest = f
			}
		}
	}
	p.dirs = append(p.dirs, d.Root+"/AUDIO_TS")
	p.readable = !d.Damaged
	if p.readable {
		p.mainFeature = DiscMainTitleSet
	}
	p.trackedRel, p.trackedSize = d.Root+"/"+DiscMainVOB, vobs[0]
	p.trackedDurMs = d.DurationMs * vobs[0] / max(d.FeatureSize, 1)
}

// featureSize is the size of the distinct main-feature files.
func (p *discPlan) featureSize() int64 {
	switch p.d.Kind {
	case DiscISO:
		return p.d.FeatureSize
	case DiscDVD:
		var n int64
		for _, f := range p.files {
			if strings.HasPrefix(path.Base(f.rel), "VTS_01_") && path.Ext(f.rel) == ".VOB" && !strings.HasSuffix(f.rel, "_0.VOB") {
				n += f.size
			}
		}
		return n
	}
	var n int64
	for _, c := range p.clips {
		if c.feature {
			n += c.size
		}
	}
	return n
}

// plexVersion is the version a disc-image scanner makes of the disc (docs/research §2.4).
func (p *discPlan) plexVersion() Version {
	d := &p.d
	v := Version{Video: d.Video, Audio: d.Audio, Subtitles: d.Subtitles, Age: d.Age}
	switch d.Kind {
	case DiscISO:
		v.Parts = []Part{{File: d.Root, Size: d.FeatureSize}}
		v.Unanalyzed = true // PMS cannot analyze a disc image
		v.Video, v.Audio, v.Subtitles = Video{}, nil, nil
	case DiscDVD:
		// VIDEO_TS.IFO first, then the largest VOB ("so that we can get thumbnail/art/analysis").
		vobDur := d.DurationMs * p.dvdLargest.size / max(p.featureSize(), 1)
		v.Parts = []Part{{File: p.dvdIFO.rel, Size: p.dvdIFO.size, DurationMs: 1}, {File: p.dvdLargest.rel, Size: p.dvdLargest.size, DurationMs: max(vobDur, 1)}}
		v.DurationMs = 1 + max(vobDur, 1)
		v.Container = "mpeg" // UNVERIFIED: what PMS reports for a VOB-based media
	default:
		// Every file of BDMV/STREAM becomes a Part (menus, warnings, trailers and extras included,
		// in name order; the order PMS uses is UNVERIFIED). How PMS derives the media duration is
		// UNVERIFIED; the fake reports the sum of the Parts, which includes every extra.
		for _, c := range p.clips {
			v.Parts = append(v.Parts, Part{File: d.Root + "/BDMV/STREAM/" + c.id + ".m2ts", Size: c.size, DurationMs: c.durMs})
			v.DurationMs += c.durMs
		}
	}
	return v
}

// trackedVersion is the pseudo-version an *arr's tracked disc file is derived from (quality and,
// for a probed Blu-ray clip, the main feature's streams).
func (p *discPlan) trackedVersion(kind string) (*Version, bool) {
	d := &p.d
	v := &Version{Video: d.Video, Audio: d.Audio, Subtitles: d.Subtitles, Quality: d.Quality}
	probed := isBluray(d.Kind) // .vob / .iso / .img are never probed (VideoFileInfoReader)
	if v.Quality == "" {
		switch {
		case isBluray(d.Kind) && kind == KindRadarr:
			v.Quality = "BR-DISK"
		case d.Kind == DiscISO && reBRDiskName.MatchString(path.Base(d.Root)) && kind == KindRadarr:
			v.Quality = "BR-DISK"
		case d.Kind == DiscISO, d.Kind == DiscDVD:
			v.Quality = "DVD"
		}
	}
	return v, probed
}

// assignDiscSets numbers the discs of one movie that form a multi-disc set ("Disc 1", "Disc 2"
// siblings of the same kind); extras discs are never part of a set.
func assignDiscSets(plans []*discPlan) {
	groups := map[string][]*discPlan{}
	for _, p := range plans {
		p.setSize = 1
		if p.extras || p.d.Kind == DiscISO {
			continue
		}
		m := reDiscSetFolder.FindStringSubmatch(path.Base(p.d.Root))
		if m == nil {
			continue
		}
		p.setNumber = atoiDefault(m[1], 0)
		k := path.Dir(p.d.Root) + "\x00" + p.d.Kind
		groups[k] = append(groups[k], p)
	}
	for _, g := range groups {
		for _, p := range g {
			p.setSize = len(g)
		}
	}
}

// ---------------------------------------------------------------------------
// World integration
// ---------------------------------------------------------------------------

// planMovieDiscs plans the discs of a movie; with the disc-image scanner it also returns the Plex
// versions the scanner makes of them.
func (w *world) planMovieDiscs(m *Movie, discScanner bool) []Version {
	var plans []*discPlan
	for _, d := range m.Discs {
		plans = append(plans, planDisc(d, m.Title, m.Section, false))
	}
	assignDiscSets(plans)
	var out []Version
	for _, p := range plans {
		if discScanner && !p.extras {
			p.plexVisible = true
			out = append(out, p.plexVersion())
		}
	}
	w.discs = append(w.discs, plans...)
	w.moviePlans[m] = plans
	return out
}

// planShowDiscs plans the discs of a show (never exposed by Plex, never tracked).
func (w *world) planShowDiscs(sh *Show) {
	var plans []*discPlan
	for _, d := range sh.Discs {
		plans = append(plans, planDisc(d, sh.Title, sh.Section, true))
	}
	assignDiscSets(plans)
	w.discs = append(w.discs, plans...)
}

// materializeDiscs writes every disc tree and registers its files (the version materializer
// skips them: a disc-image scanner's Parts are disc files).
func (w *world) materializeDiscs() error {
	for _, p := range w.discs {
		for _, dir := range p.dirs {
			if err := os.MkdirAll(w.local(dir), 0o755); err != nil {
				return fmt.Errorf("create disc dir %s: %w", dir, err)
			}
		}
		for _, f := range p.files {
			if err := writeDiscFile(w.local(f.rel), f); err != nil {
				return fmt.Errorf("create disc file %s: %w", f.rel, err)
			}
			w.discFiles[f.rel] = true
		}
	}
	return nil
}

func writeDiscFile(lp string, f discFile) (err error) {
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
	}()
	if len(f.data) > 0 {
		if _, err := fh.WriteAt(f.data, f.at); err != nil {
			return err
		}
	}
	return fh.Truncate(f.size)
}

// discImage returns the plan of the disc image at rel, or nil.
func (w *world) discImage(rel string) *discPlan {
	for _, p := range w.discs {
		if p.d.Kind == DiscISO && p.d.Root == rel {
			return p
		}
	}
	return nil
}

// ownedByDisc reports whether rel lies under a non-image disc's owned entries.
func (w *world) ownedByDisc(rel string) bool {
	for _, p := range w.discs {
		if p.d.Kind == DiscISO {
			continue
		}
		for _, o := range p.owned {
			if under(rel, o) {
				return true
			}
		}
	}
	return false
}

// reportDiscRemoval records RuleDeleteDiscMember when a Plex media delete or *arr file delete
// targets an existing file inside a disc structure (the delete would corrupt the disc; only
// Dupearr's whole-disc filesystem removal may touch it), and RuleDeleteDiscImage when it targets
// a disc image (discs are removed only by Dupearr's filesystem method). A stale entry whose
// files are all gone may still be deleted. Callers hold w.mu.
func (w *world) reportDiscRemoval(server string, r *http.Request, g *removalGuard) {
	rels := make([]string, 0, len(g.rels))
	for rel := range g.rels {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	member, image, clip := "", "", ""
	for _, rel := range rels {
		if !w.fileExists(rel) {
			continue
		}
		switch {
		case member == "" && (isDiscMember(rel) || w.ownedByDisc(rel)):
			member = rel
		case image == "" && w.discImage(rel) != nil:
			image = rel
		case clip == "" && !isDiscMember(rel) && !w.ownedByDisc(rel) && reBlurayClip.MatchString(path.Base(rel)):
			clip = rel
		}
	}
	if clip != "" {
		w.violate(server, r, RuleDeleteLooseClip, fmt.Sprintf("%q is a loose disc clip; a movie can span several clips, so the clips of a folder are one copy and a clip is never removed on its own", remote(clip)))
	}
	if member != "" {
		w.violate(server, r, RuleDeleteDiscMember, fmt.Sprintf("%q belongs to a full-disc backup; a per-file delete corrupts the disc", remote(member)))
	}
	if image != "" {
		w.violate(server, r, RuleDeleteDiscImage, fmt.Sprintf("%q is a disc image; discs are removed only by Dupearr's filesystem method", remote(image)))
	}
}

// ---------------------------------------------------------------------------
// Env API
// ---------------------------------------------------------------------------

// DiscFixtures returns the ground truth about every disc of the scenario (movies first, in
// scenario order).
func (e *Env) DiscFixtures() []DiscFixture {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	out := make([]DiscFixture, 0, len(e.w.discs))
	for _, p := range e.w.discs {
		d := &p.d
		f := DiscFixture{
			Title: p.title, Section: p.section, Show: p.show, Kind: d.Kind,
			Root: remote(d.Root), LocalRoot: e.w.local(d.Root), Folder: remote(p.folder),
			SetNumber: p.setNumber, SetSize: p.setSize, Extras: p.extras,
			Files: len(p.files), TotalSize: p.totalSize(), FeatureSize: p.featureSize(),
			MainFeature: p.mainFeature, MainClips: append([]string(nil), p.mainClips...),
			Readable: p.readable, PlexVisible: p.plexVisible, TrackedBy: d.Tracked,
		}
		for _, o := range p.owned {
			f.OwnedEntries = append(f.OwnedEntries, e.w.local(o))
		}
		if isBluray(d.Kind) {
			f.Clips = len(p.clips)
		}
		if d.Kind != DiscISO {
			f.Video, f.Chapters = d.Video, d.Chapters
			f.Audio = append([]Audio(nil), d.Audio...)
			f.Subtitles = append([]Subtitle(nil), d.Subtitles...)
			if p.readable {
				f.DurationMs = d.DurationMs
			}
		}
		if d.Tracked != "" {
			f.TrackedPath = remote(p.trackedRel)
		}
		out = append(out, f)
	}
	return out
}

// DiscProblems lists discs — and loose clip sets (ClipSet) — that are neither complete nor gone as
// a whole: some of their files were removed or moved while others stayed (a per-file removal
// corrupted them). A disc whose files are all absent (moved to a recycle bin as a unit, or
// unmounted) is fine.
func (e *Env) DiscProblems() []string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	var out []string
	for _, p := range e.w.discs {
		var missing []string
		for _, f := range p.files {
			fi, err := os.Lstat(e.w.local(f.rel))
			if err != nil || !fi.Mode().IsRegular() {
				missing = append(missing, remote(f.rel))
			}
		}
		if len(missing) > 0 && len(missing) < len(p.files) {
			out = append(out, fmt.Sprintf("%s disc %s of %q is incomplete: %d of %d files missing (first: %s)",
				p.d.Kind, remote(p.d.Root), p.title, len(missing), len(p.files), missing[0]))
		}
	}
	return append(out, e.w.looseClipProblems()...)
}

// AssertDiscsIntact fails the test when a disc or loose clip set was partially removed (see
// DiscProblems).
func (e *Env) AssertDiscsIntact(t TB) {
	t.Helper()
	for _, p := range e.DiscProblems() {
		t.Errorf("fakemedia: %s", p)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// discs validates the discs of a movie (folder == "") or show (folder = the show folder).
// Tracked discs are counted by the caller.
func (v *validator) discs(owner string, lib *Library, discs []Disc, showFolder string) {
	for i := range discs {
		d := discs[i].withDefaults()
		do := fmt.Sprintf("%s disc %q", owner, discs[i].Root)
		switch d.Kind {
		case DiscBluray, DiscUHDBluray, DiscDVD, DiscISO:
		default:
			v.addf("%s: kind must be %q, %q, %q or %q", do, DiscBluray, DiscUHDBluray, DiscDVD, DiscISO)
			continue
		}
		if !validRel(d.Root) {
			v.addf("%s: invalid root (relative to the media root)", do)
			continue
		}
		if isImage := reImageExt.MatchString(d.Root); isImage != (d.Kind == DiscISO) {
			v.addf("%s: only an image root (and every image root) ends in .iso or .img", do)
		}
		if isDiscMember(d.Root) {
			v.addf("%s: the root must not lie inside a disc structure", do)
		}
		folder := discFolder(&d)
		if lib != nil {
			ok := false
			for _, dir := range lib.Dirs {
				ok = ok || (under(folder, dir) && folder != dir)
			}
			if !ok {
				v.addf("%s: the disc must sit in a movie/season folder inside a location of section %q", do, lib.Key)
			}
		}
		if showFolder != "" && !under(folder, showFolder) {
			v.addf("%s: outside the show folder %q", do, showFolder)
		}
		if d.FeatureSize < 0 || d.DurationMs < 0 || d.FeatureClips < 0 || d.Chapters < 0 || d.Age < 0 {
			v.addf("%s: negative size, duration, clip count, chapters or age", do)
		}
		switch {
		case isBluray(d.Kind) && (d.FeatureClips > 199 || d.ExtraClips > 799):
			v.addf("%s: at most 199 feature clips (00800…00998) and 799 extra clips", do)
		case isBluray(d.Kind) && d.FeatureSize < 192*int64(d.FeatureClips):
			v.addf("%s: feature size too small for %d clips", do, d.FeatureClips)
		case d.Kind == DiscDVD && (d.FeatureSize < dvdSector || d.FeatureSize > 9*dvdVOBMax):
			v.addf("%s: a DVD title holds 1 sector to 9 VOBs of 1 GiB", do)
		}
		if err := checkDiscStreams(&d); err != nil {
			v.addf("%s: %v", do, err)
		}
		if d.Tracked != "" {
			switch {
			case showFolder != "":
				v.addf("%s: Sonarr cannot track a disc file (TV discs are protect-only)", do)
			case reExtrasFolder.MatchString(path.Base(d.Root)):
				v.addf("%s: an extras disc cannot be tracked", do)
			}
		}
		if by, ok := v.discRoots[d.Root]; ok {
			v.addf("%s: has the same root as the disc of %s (one disc per root)", do, by)
		}
		v.discRoots[d.Root] = do
		for _, o := range discOwned(&d) {
			for other, by := range v.discOwned {
				if under(o, other) || under(other, o) {
					v.addf("%s: overlaps the disc of %s", do, by)
				}
			}
			v.discOwned[o] = do
		}
	}
}

// discOverlaps rejects scenario files (versions) inside a disc structure or a disc's owned
// entries: disc files are described by Disc, and a disc-image scanner's versions are generated.
func (v *validator) discOverlaps() {
	rels := make([]string, 0, len(v.files))
	for rel := range v.files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if isDiscMember(rel) {
			v.addf("part %q: lies inside a disc structure (describe full-disc backups with Movie.Discs / Show.Discs)", rel)
			continue
		}
		for o, by := range v.discOwned {
			if under(rel, o) {
				v.addf("part %q: belongs to the disc of %s", rel, by)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// *arr helpers
// ---------------------------------------------------------------------------

var reYear = regexp.MustCompile(`(?:^|[^0-9])(?:18|19|20)[0-9]{2}(?:[^0-9]|$)`)

// radarrParses reports whether Radarr's Parser.ParseMoviePath can parse a file during a disk scan:
// it tries the file name, "<parent folder> <file name>" and the parent folder, all of which need
// a year. Disc clips (BDMV/STREAM/00800.m2ts, VIDEO_TS/VTS_01_1.VOB) never parse, so a rescan
// rejects them ("Unable to parse file", docs/research/disc-structures.md §3.3 B).
func radarrParses(rel string) bool {
	name := path.Base(rel)
	dir := path.Base(path.Dir(rel))
	return reYear.MatchString(name) || reYear.MatchString(dir)
}

// discExtensionQuality is the *arr quality of a file whose name names no source, by extension
// (MediaFileExtensions): images and VOBs are DVD, .m2ts is Blu-ray.
func discExtensionQuality(name string) (source string, ok bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".iso", ".img", ".vob":
		return "dvd", true
	case ".m2ts":
		return "bluray", true
	}
	return "", false
}
