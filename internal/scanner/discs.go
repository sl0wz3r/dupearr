package scanner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Full-disc backups (docs/research/disc-structures.md §6, docs/DECISIONS.md D9).
//
// Plex's default scanners never index a disc structure (BDMV/, VIDEO_TS/, .iso …), so a movie
// folder holding "Movie.mkv" and a Blu-ray backup is ONE Plex version. With settings.DetectDiscs
// the scan therefore looks into the (mapped, local) folder of every listed movie: each disc found
// there (a single disc, a "Disc 1"/"Disc 2" set, a DVD, an image) is inspected — read only, never
// following symlinks, bounded — and added to the movie as one version (key "disc:<server>:<hash of
// its root>", origin "filesystem"), which makes "mkv + disc" a duplicate candidate.
//
// A Plex version whose parts lie inside a disc structure (a custom "Disc Image" scanner lists one
// part per BDMV/STREAM clip) or that is a disc image is a disc version too (origin "plex"): it keeps
// its Plex key and parts, and takes its attributes from the disc on disk when Dupearr can read it
// (Plex's summed durations and first-clip streams describe menus and trailers, not the film);
// otherwise its attributes stay unknown and it is protected. That conversion does not depend on the
// setting: a file inside a disc must never look like an ordinary, removable version.
//
// TV libraries: discs are only detected next to the scanned episodes and flag their groups
// (full_disc); they are never versions of an episode and never removed (v1).
//
// Only folders strictly inside a mapped library folder are looked into, never a library root (a
// flat library's root holds many titles) and never a folder several different titles share. A
// disc is attributed to one item: when the same disc is also exposed by a Plex item (custom
// scanner) it is that Plex version, never a second one; when another Plex item this scan does not
// act on exposes its files, the disc is protected (DiscInfo.PlexItems).

// discInventory caches, for one scan, the discs found in each local folder.
type discInventory struct {
	opts disc.Options

	mu      sync.Mutex
	folders map[string]*folderDiscs // clean local folder → result
}

// folderDiscs is the detection result of one folder: its inspected discs.
type folderDiscs struct {
	discs []disc.Disc
}

// discPlan is where the scan looks for discs: the local folders of the listed movies.
type discPlan struct {
	owner  map[string]refKey   // local movie folder → the item its discs belong to
	folder map[refKey][]string // item → its local movie folders (sorted)
}

// discInv returns the run's disc inventory (created on first use).
func (p *pipeline) discInv() *discInventory {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discs == nil {
		p.discs = &discInventory{folders: map[string]*folderDiscs{}}
	}
	return p.discs
}

// result returns the cached detection result of folder (nil when it was not looked into).
func (inv *discInventory) result(folder string) *folderDiscs {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	return inv.folders[folder]
}

// detectFolders detects and inspects the discs of every folder not looked into yet in this scan,
// with at most Deps.Concurrency folders in flight. A folder that cannot be read is cached as
// holding no disc (it is only a missed detection: removals never depend on it, and the per-file
// guard does not depend on detection).
func (p *pipeline) detectFolders(folders []string) {
	inv := p.discInv()
	var todo []string
	inv.mu.Lock()
	for _, f := range folders {
		if _, done := inv.folders[f]; !done && !slices.Contains(todo, f) {
			todo = append(todo, f)
		}
	}
	inv.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	sem := make(chan struct{}, p.s.d.Concurrency)
	var wg sync.WaitGroup
	for _, f := range todo {
		select {
		case sem <- struct{}{}:
		case <-p.ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := p.detectFolder(f, inv.opts)
			if res == nil {
				return // cancelled: not cached
			}
			inv.mu.Lock()
			inv.folders[f] = res
			inv.mu.Unlock()
		}()
	}
	wg.Wait()
}

// detectFolder detects and inspects the discs of one folder (nil when the scan was cancelled). A
// panicking parser is converted into "no disc" (logged): one bad disc must not end the scan.
func (p *pipeline) detectFolder(folder string, opts disc.Options) (res *folderDiscs) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("Disc detection failed unexpectedly", "folder", folder, "panic", fmt.Sprint(r))
			res = &folderDiscs{}
		}
	}()
	found, err := disc.Detect(p.ctx, folder, opts)
	switch {
	case p.ctx.Err() != nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return &folderDiscs{}
	case err != nil:
		p.log.Debug("Could not look for full-disc backups in a folder", "folder", folder, "error", err)
		return &folderDiscs{}
	}
	out := &folderDiscs{}
	for i := range found {
		d := found[i]
		if err := disc.Inspect(p.ctx, &d); err != nil {
			if p.ctx.Err() != nil {
				return nil
			}
			p.log.Debug("Could not inspect a full-disc backup", "root", d.Root, "error", err)
			continue
		}
		out.discs = append(out.discs, d)
	}
	return out
}

// ---------------------------------------------------------------------------
// Where to look
// ---------------------------------------------------------------------------

// libraryFolders returns the mapped, local folders of a library (clean, OS separators).
func (p *pipeline) libraryFolders(lib models.Library) []string {
	var out []string
	for _, loc := range lib.Locations {
		if local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, lib.ServerID, loc); ok && filepath.IsAbs(local) {
			if c := filepath.Clean(local); !slices.Contains(out, c) && filepath.Dir(c) != c {
				out = append(out, c)
			}
		}
	}
	return out
}

// strictlyWithin reports whether the clean local path p lies strictly inside dir.
func strictlyWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// insideLibrary reports whether the local folder lies strictly inside one of the library folders.
func insideLibrary(folder string, libDirs []string) bool {
	for _, d := range libDirs {
		if strictlyWithin(folder, d) {
			return true
		}
	}
	return false
}

// titleFolderOf returns the local folder a (non-disc) media file of a title lies in: the parent of
// its mapped path, skipping a "CD1"/"Disc 2"-style set folder; "" when the path is not mapped, lies
// inside a disc structure or is a disc image, or the folder is not strictly inside a library folder.
func (p *pipeline) titleFolderOf(serverID int64, serverPath string, libDirs []string) string {
	if strings.TrimSpace(serverPath) == "" || disc.IsDiscPath(serverPath) || disc.IsImagePath(serverPath) {
		return ""
	}
	local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, serverID, serverPath)
	if !ok || !filepath.IsAbs(local) {
		return ""
	}
	dir := filepath.Dir(filepath.Clean(local))
	if _, set := disc.IsSetFolderName(filepath.Base(dir)); set {
		dir = filepath.Dir(dir)
	}
	if !insideLibrary(dir, libDirs) {
		return ""
	}
	return dir
}

// planDiscFolders maps the movie folders of every listed movie item to the item their discs are
// attributed to. A folder several items use is attributed to the first of them (listing order,
// not index-only) only when they all share an external id (the same movie in overlapping
// libraries); otherwise it holds several titles and is not looked into.
func (p *pipeline) planDiscFolders() *discPlan {
	if p.discPlan != nil {
		return p.discPlan
	}
	users := map[string][]refKey{}
	libDirs := map[int64][]string{}
	for _, k := range p.index.order {
		ir := p.index.refs[k]
		if mediaTypeOf(ir.lib.Type) != models.MediaTypeMovie {
			continue
		}
		dirs, ok := libDirs[ir.lib.ID]
		if !ok {
			dirs = p.libraryFolders(ir.lib)
			libDirs[ir.lib.ID] = dirs
		}
		seen := map[string]bool{}
		for _, m := range ir.ref.Media {
			if m.Optimized {
				continue
			}
			for _, part := range m.Parts {
				if f := p.titleFolderOf(k.server, part.File, dirs); f != "" && !seen[f] {
					seen[f] = true
					users[f] = append(users[f], k)
				}
			}
		}
	}
	plan := &discPlan{owner: map[string]refKey{}, folder: map[refKey][]string{}}
	for f, ks := range users {
		owner, ok := p.folderOwner(ks)
		if !ok {
			p.log.Debug("A folder holds several titles; it is not searched for full-disc backups", "folder", f, "items", len(ks))
			continue
		}
		plan.owner[f] = owner
		plan.folder[owner] = append(plan.folder[owner], f)
	}
	for k := range plan.folder {
		sort.Strings(plan.folder[k])
	}
	p.discPlan = plan
	return plan
}

// folderOwner picks the item a shared folder's discs belong to (see planDiscFolders).
func (p *pipeline) folderOwner(ks []refKey) (refKey, bool) {
	var owner *refKey
	for i := range ks {
		ir := p.index.refs[ks[i]]
		if ir.indexOnly || p.cfg.libraryExcluded(ir.lib.ID) {
			continue
		}
		owner = &ks[i]
		break
	}
	if owner == nil {
		return refKey{}, false
	}
	if len(ks) == 1 {
		return *owner, true
	}
	first := p.index.refs[ks[0]].ref.ExternalIDs
	for _, k := range ks[1:] {
		if !shareExternalID(first, p.index.refs[k].ref.ExternalIDs) {
			return refKey{}, false
		}
	}
	return *owner, true
}

// shareExternalID reports whether two items share a tmdb, imdb or plex id.
func shareExternalID(a, b map[string]string) bool {
	for _, sp := range []string{"tmdb", "imdb", "plex"} {
		if x := normID(sp, a[sp]); x != "" && x == normID(sp, b[sp]) {
			return true
		}
	}
	return false
}

// withDiscCandidates adds to a full scan's candidates every listed movie whose folder holds a
// full-disc backup (settings.DetectDiscs): with its disc it has two versions even though Plex lists
// one. Keys keep the listing order.
func (p *pipeline) withDiscCandidates(targets []refKey) []refKey {
	if !p.cfg.settings.DetectDiscs {
		return targets
	}
	plan := p.planDiscFolders()
	if len(plan.owner) == 0 {
		return targets
	}
	folders := make([]string, 0, len(plan.owner))
	for f := range plan.owner {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	p.progress(fmt.Sprintf("Looking for full-disc backups in %d movie %s", len(folders), plural(len(folders), "folder", "folders")))
	p.detectFolders(folders)
	if p.ctx.Err() != nil {
		return targets
	}
	inv := p.discInv()
	withDisc := map[refKey]bool{}
	found := 0
	for _, f := range folders {
		if res := inv.result(f); res != nil && len(res.discs) > 0 {
			withDisc[plan.owner[f]] = true
			found += len(res.discs)
		}
	}
	if found > 0 {
		p.progress(fmt.Sprintf("Found %d full-disc %s", found, plural(found, "backup", "backups")))
	}
	in := make(map[refKey]bool, len(targets))
	for _, k := range targets {
		in[k] = true
	}
	var out []refKey
	for _, k := range p.index.order {
		if in[k] || withDisc[k] {
			out = append(out, k)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Attaching discs to the fetched items
// ---------------------------------------------------------------------------

// attachDiscs turns the fetched items' Plex versions that are discs into disc versions, adds the
// discs found in the movies' folders as versions (settings.DetectDiscs) and notes the TV episodes
// whose folder holds a disc. Call after decorate (local paths known), before enrich. Items of a
// read-only server (Jellyfin) take no part: a disc there is only reported (docs/DECISIONS.md D12,
// research S20), and its versions are never merged with discs found on disk.
func (p *pipeline) attachDiscs(items []models.MediaItem) {
	var idx []int
	for i := range items {
		if !items[i].ServerKind.ReadOnly() {
			idx = append(idx, i)
		}
	}
	if len(idx) == len(items) {
		p.attachPlexDiscs(items)
		return
	}
	sub := make([]models.MediaItem, len(idx))
	for j, i := range idx {
		sub[j] = items[i]
	}
	p.attachPlexDiscs(sub)
	for j, i := range idx {
		items[i] = sub[j]
	}
}

// attachPlexDiscs is attachDiscs for the items of servers that take part.
func (p *pipeline) attachPlexDiscs(items []models.MediaItem) {
	detect := p.cfg.settings.DetectDiscs
	plan := &discPlan{}
	if detect {
		plan = p.planDiscFolders()
	}

	// 1. The folders to look into.
	var folders []string
	add := func(f string) {
		if f != "" && !slices.Contains(folders, f) {
			folders = append(folders, f)
		}
	}
	if detect {
		for i := range items {
			it := &items[i]
			k := refKey{server: it.ServerID, rk: strings.TrimSpace(it.RatingKey)}
			switch it.MediaType {
			case models.MediaTypeMovie:
				for _, f := range plan.folder[k] {
					add(f)
				}
				for j := range it.Versions {
					if root, ok := p.plexDiscLocalRoot(it, &it.Versions[j]); ok {
						add(p.detectDirFor(it, root))
					}
				}
				for _, cs := range looseClipSets(it) {
					if root, ok := p.clipSetLocalRoot(it, cs); ok {
						add(p.detectDirFor(it, root))
					}
				}
			case models.MediaTypeEpisode:
				for _, f := range p.episodeFolders(it) {
					add(f)
				}
			}
		}
		sort.Strings(folders)
		p.detectFolders(folders)
		if p.ctx.Err() != nil {
			return
		}
	}

	// 2. Plex versions that are discs (always: the loose clips of one folder become ONE version,
	// a custom scanner's disc another), then the discs found on disk (movies).
	claimed := map[int64]map[string]bool{} // server → local disc roots a Plex version already is
	for i := range items {
		it := &items[i]
		p.mergeClipSets(it, detect)
		for j := range it.Versions {
			v := &it.Versions[j]
			if isPlexDisc(v) {
				p.toPlexDisc(it, v, detect)
			}
			if v.Disc == nil || v.Disc.Origin != models.DiscOriginPlex {
				continue
			}
			if v.Disc != nil && v.Disc.LocalRoot != "" {
				if claimed[it.ServerID] == nil {
					claimed[it.ServerID] = map[string]bool{}
				}
				for _, r := range discLocalRoots(v.Disc) {
					claimed[it.ServerID][filepath.Clean(r)] = true
				}
			}
		}
	}
	if !detect {
		return
	}
	inv := p.discInv()
	for i := range items {
		it := &items[i]
		k := refKey{server: it.ServerID, rk: strings.TrimSpace(it.RatingKey)}
		if it.MediaType == models.MediaTypeEpisode {
			for _, f := range p.episodeFolders(it) {
				if res := inv.result(f); res != nil && len(res.discs) > 0 {
					p.mu.Lock()
					p.discNearby[k] = true
					p.mu.Unlock()
					break
				}
			}
			continue
		}
		if it.MediaType != models.MediaTypeMovie {
			continue
		}
		for _, f := range plan.folder[k] {
			res := inv.result(f)
			if res == nil {
				continue
			}
			names := p.itemNamesIn(it, f)
			for di := range res.discs {
				d := &res.discs[di]
				if claimedRoot(claimed[it.ServerID], d) {
					continue // the same disc is a Plex version of a (custom-scanner) item
				}
				if !discNamesItem(f, d, names) {
					p.log.Debug("A full-disc backup in a folder that does not name the movie is not attributed to it",
						"root", d.Root, "movie", it.Title)
					continue
				}
				v, ok := p.discVersion(it, d)
				if !ok || slices.ContainsFunc(it.Versions, func(o models.MediaVersion) bool { return o.Key == v.Key }) {
					continue
				}
				v.Disc.PlexItems = p.plexItemsInDisc(it.ServerID, it.RatingKey, v.Disc)
				it.Versions = append(it.Versions, v)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Attribution: whose disc is it?
// ---------------------------------------------------------------------------

// Plex's default scanners never list a disc or an image, so the folder of a Plex-listed movie is only
// known to hold THAT movie's discs when the folder is the movie's own folder. The *arr layout names
// it after the title ("Heat (1995)/"), and that is what is checked: the folder's name must name the
// movie — its Plex title, or the name of one of its own files in that folder (a localized Plex title
// next to an English file name). In any other folder — a genre or collection folder that holds one
// Plex-listed movie and discs or images Plex never showed — only an image whose own name names the
// movie ("Heat (1995).iso" next to "Heat (1995).mkv") is attributed; every other disc there may be
// another film's, and a person approving the group would move it to the recycle bin.

// titleWords reduces a folder or file name (without extension) to the words of its title: lower
// case letters and digits, without [..]/{..} tags, apostrophes and articles, cut before the first
// year after the first word ("Heat.1995.1080p.BluRay" → [heat], "1917 (2019)" → [1917]).
func titleWords(name string) []string { return nameWords(name, true) }

// nameWords is titleWords, cut before the first year after the first word only when cutAtYear.
func nameWords(name string, cutAtYear bool) []string {
	var b strings.Builder
	depth := 0
	for _, r := range name {
		switch {
		case r == '[' || r == '{':
			depth++
		case (r == ']' || r == '}') && depth > 0:
			depth--
		case depth > 0, r == '\'', r == '’', r == '`':
		case r == '&':
			b.WriteString(" and ")
		default:
			b.WriteRune(r)
		}
	}
	fields := strings.FieldsFunc(strings.ToLower(b.String()), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := make([]string, 0, len(fields))
	for i, w := range fields {
		if cutAtYear && i > 0 && isYearWord(w) {
			break
		}
		if w == "the" || w == "a" || w == "an" {
			continue
		}
		out = append(out, w)
	}
	return out
}

// isYearWord reports a plausible release year (1900–2099).
func isYearWord(w string) bool {
	return len(w) == 4 && (strings.HasPrefix(w, "19") || strings.HasPrefix(w, "20")) &&
		strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// namesTitle reports whether name names one of the titles: its words are the title's, optionally
// followed by release, disc or edition words ("Heat COMPLETE UHD BLURAY", "Heat - Disc 1", "Heat
// Director's Cut") — never by other title words, so a sequel or another film of a franchise ("Rocky
// II", "Alien Covenant", "Scream 2") does not name the first film.
func namesTitle(name string, titles [][]string) bool {
	words := titleWords(name)
	for _, t := range titles {
		if len(t) > 0 && len(words) >= len(t) && slices.Equal(words[:len(t)], t) &&
			(len(words) == len(t) || isReleaseWord(words[len(t)])) {
			return true
		}
	}
	return false
}

// releaseWords are the words a release, disc or edition adds after a title in a folder or file name.
var releaseWords = map[string]bool{
	"complete": true, "bluray": true, "blu": true, "bdrip": true, "brrip": true, "bdmv": true, "bdiso": true,
	"iso": true, "img": true, "uhd": true, "4k": true, "hdr": true, "hdr10": true, "dv": true, "dovi": true,
	"remux": true, "disc": true, "disk": true, "cd": true, "dvd": true, "dvd5": true, "dvd9": true, "dvdr": true,
	"part": true, "pt": true, "untouched": true, "full": true, "fullbd": true, "retail": true, "custom": true,
	"multi": true, "avc": true, "hevc": true, "vc1": true, "mpeg2": true, "x264": true, "x265": true, "h264": true,
	"h265": true, "truehd": true, "atmos": true, "dts": true, "hd": true, "pal": true, "ntsc": true, "backup": true,
	"extended": true, "directors": true, "director": true, "theatrical": true, "unrated": true, "uncut": true,
	"remastered": true, "special": true, "collectors": true, "anniversary": true, "final": true, "ultimate": true,
	"imax": true, "criterion": true, "edition": true, "cut": true, "version": true, "3d": true,
}

// reReleaseWord matches numbered release and disc words: "1080p", "2160p", "cd2", "disc1", "bd50".
var reReleaseWord = regexp.MustCompile(`^(?:[0-9]{3,4}[pi]|(?:cd|dvd|disc|disk|part|pt)[0-9]{1,2}|bd(?:[0-9]{1,3})?)$`)

// isReleaseWord reports a word that adds release, disc or edition details to a title.
func isReleaseWord(w string) bool { return releaseWords[w] || reReleaseWord.MatchString(w) }

// itemNamesIn returns the title words that name movie it in its folder: its Plex title and the names
// (without extension) of its own files there.
func (p *pipeline) itemNamesIn(it *models.MediaItem, folder string) [][]string {
	var out [][]string
	if w := titleWords(it.Title); len(w) > 0 {
		out = append(out, w)
	}
	lib, ok := p.cfg.libraries[it.LibraryID]
	if !ok {
		return out
	}
	dirs := p.libraryFolders(lib)
	for j := range it.Versions {
		if it.Versions[j].Disc != nil {
			continue
		}
		for _, pt := range it.Versions[j].Parts {
			if p.titleFolderOf(it.ServerID, pt.Path, dirs) != folder {
				continue
			}
			base := path.Base(strings.ReplaceAll(pt.Path, `\`, "/"))
			if w := titleWords(strings.TrimSuffix(base, path.Ext(base))); len(w) > 0 {
				out = append(out, w)
			}
		}
	}
	return out
}

// discNamesItem reports whether a disc found in folder belongs to the movie named by titles: the
// folder names the movie, or the disc is an image (every image of a stacked set) whose name does.
// Even in the movie's own folder a disc named for something else is not the movie's (GAP-02; see
// discNamedForOther).
func discNamesItem(folder string, d *disc.Disc, titles [][]string) bool {
	if discNamedForOther(folder, d, titles) {
		return false
	}
	if namesTitle(filepath.Base(folder), titles) {
		return true
	}
	if d.Type != disc.ISO || len(d.Roots) == 0 {
		return false
	}
	for _, r := range d.Roots {
		base := filepath.Base(r)
		if !namesTitle(strings.TrimSuffix(base, filepath.Ext(base)), titles) {
			return false
		}
	}
	return true
}

// discNamedForOther reports a disc whose own names say it is not a version of the movie named by
// titles (docs/research/disc-structures.md §6.10 #12: an extras disc is never a version, and never
// removed with the feature): an image, a set folder or a bundle whose name holds an extras word
// after the title ("Heat (1995) - Special Features.iso", "Special Features - Disc 1"), or a set
// whose folders put another title before their numbers ("Ronin Part 1" in "Heat (1995)"). Such a
// disc is left alone (a missed detection, never a bonus disc or another film offered for removal).
func discNamedForOther(folder string, d *disc.Disc, titles [][]string) bool {
	for _, r := range d.Roots {
		if filepath.Clean(r) == filepath.Clean(folder) {
			continue // the disc's root is the movie folder itself
		}
		base := filepath.Base(r)
		if prefix, set := disc.SetFolderPrefix(base); set && d.Type != disc.ISO {
			if prefix != "" && (!namesTitle(prefix, titles) || extrasBeyondTitle(prefix, titles)) {
				return true
			}
			continue
		}
		stem := strings.TrimSuffix(base, filepath.Ext(base)) // an image, a .dvdmedia bundle
		if extrasBeyondTitle(stem, titles) {
			return true
		}
	}
	return false
}

// extrasBeyondTitle reports whether name holds an extras word (disc.HasExtrasWord) outside the
// title words it starts with: "Heat (1995) - Special Features" for Heat, but not "The Interview
// (2014)" for The Interview.
func extrasBeyondTitle(name string, titles [][]string) bool {
	words := nameWords(name, false)
	for _, t := range titles {
		if len(t) > 0 && len(words) >= len(t) && slices.Equal(words[:len(t)], t) {
			words = words[len(t):]
			break
		}
	}
	return disc.HasExtrasWord(strings.Join(words, " "))
}

// rePlexDiscPart matches the parts a custom "Disc Image" Plex scanner lists for a disc
// (docs/research/disc-structures.md §6.2): the clips of BDMV/STREAM (BDAV, AVCHD alike), the IFO and
// VOB files of a DVD title (VIDEO_TS/ or flat) and HD DVD's EVO files.
var rePlexDiscPart = regexp.MustCompile(`(?i)(?:^|/)(?:(?:BDMV|BDAV)/STREAM/[^/]+\.(?:m2ts|mts|ssif)|(?:VIDEO_TS/)?(?:VIDEO_TS\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]\.(?:IFO|BUP|VOB))|HVDVD_TS/[^/]+\.EVO)$`)

// isPlexDisc reports a Plex version that is (part of) a disc: a part a disc-image scanner lists
// for a disc (rePlexDiscPart), or a disc image. A file merely under a folder named like a disc
// component (a movie folder called "Certificate") is not converted — the per-file guard still
// keeps it from being removed on its own.
func isPlexDisc(v *models.MediaVersion) bool {
	if v.Disc != nil || v.OptimizedVersion {
		return false
	}
	for _, pt := range v.Parts {
		if disc.IsImagePath(pt.Path) || (disc.IsDiscPath(pt.Path) && rePlexDiscPart.MatchString(strings.ReplaceAll(pt.Path, `\`, "/"))) {
			return true
		}
	}
	return false
}

// plexDiscRoots returns the disc roots (server paths, first-seen order) and kind of a Plex disc
// version's parts. Linear in the parts: a (broken or hostile) custom-scanner answer may list tens of
// thousands of them.
func plexDiscRoots(v *models.MediaVersion) (roots []string, kind disc.Type) {
	seen := map[string]bool{}
	for _, pt := range v.Parts {
		var root string
		var t disc.Type
		switch {
		case disc.IsImagePath(pt.Path) && !disc.IsDiscPath(pt.Path):
			root, t = pt.Path, disc.ISO
		default:
			r, tt, ok := disc.RootOf(pt.Path)
			if !ok || r == "" {
				continue
			}
			root, t = r, tt
		}
		if kind == "" {
			kind = t
		}
		if n := pathmap.Normalize(root); !seen[n] {
			seen[n] = true
			roots = append(roots, root)
		}
	}
	return roots, kind
}

// plexDiscLocalRoot returns the local root of a Plex disc version's first disc, when mapped.
func (p *pipeline) plexDiscLocalRoot(it *models.MediaItem, v *models.MediaVersion) (string, bool) {
	if !isPlexDisc(v) {
		return "", false
	}
	roots, _ := plexDiscRoots(v)
	if len(roots) == 0 {
		return "", false
	}
	local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, it.ServerID, roots[0])
	if !ok || !filepath.IsAbs(local) {
		return "", false
	}
	return filepath.Clean(local), true
}

// detectDirFor returns the folder to look into for a disc root (disc.DetectDir), "" when it is not
// strictly inside a library folder of the item.
func (p *pipeline) detectDirFor(it *models.MediaItem, localRoot string) string {
	dir := disc.DetectDir(localRoot)
	lib, ok := p.cfg.libraries[it.LibraryID]
	if !ok || !insideLibrary(dir, p.libraryFolders(lib)) {
		return ""
	}
	return dir
}

// episodeFolders returns the local folders of an episode's files (season folders), strictly inside
// its library's folders.
func (p *pipeline) episodeFolders(it *models.MediaItem) []string {
	lib, ok := p.cfg.libraries[it.LibraryID]
	if !ok {
		return nil
	}
	dirs := p.libraryFolders(lib)
	var out []string
	for j := range it.Versions {
		for _, pt := range it.Versions[j].Parts {
			if f := p.titleFolderOf(it.ServerID, pt.Path, dirs); f != "" && !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

// discLocalRoots returns the local roots of a disc (all discs of a set).
func discLocalRoots(d *models.DiscInfo) []string {
	if len(d.LocalRoots) > 0 {
		return d.LocalRoots
	}
	if d.LocalRoot != "" {
		return []string{d.LocalRoot}
	}
	return nil
}

// claimedRoot reports whether a Plex version already is (one disc of) d.
func claimedRoot(claimed map[string]bool, d *disc.Disc) bool {
	for _, r := range d.Roots {
		if claimed[filepath.Clean(r)] {
			return true
		}
	}
	return claimed[filepath.Clean(d.Root)]
}

// findDisc returns the inspected disc of the folder whose roots include localRoot.
func (p *pipeline) findDisc(folder, localRoot string) *disc.Disc {
	res := p.discInv().result(folder)
	if res == nil {
		return nil
	}
	want := filepath.Clean(localRoot)
	for i := range res.discs {
		d := &res.discs[i]
		if filepath.Clean(d.Root) == want || slices.ContainsFunc(d.Roots, func(r string) bool { return filepath.Clean(r) == want }) {
			return d
		}
	}
	return nil
}

// toPlexDisc turns a Plex version whose parts are a disc into a disc version (origin "plex"): its
// Plex key and parts stay; its attributes come from the disc on disk when it was found and read,
// and are otherwise unknown (Plex's describe one clip, and a summed duration includes menus and
// extras) — such a disc is protected and its group reviewed.
func (p *pipeline) toPlexDisc(it *models.MediaItem, v *models.MediaVersion, detect bool) {
	roots, kind := plexDiscRoots(v)
	if len(roots) == 0 {
		return
	}
	info := &models.DiscInfo{
		Type: string(kind), Root: roots[0], Roots: roots, Discs: len(roots), FileCount: len(v.Parts),
		Origin: models.DiscOriginPlex,
	}
	var found *disc.Disc
	var foundIn string // the folder the disc was detected in
	localRoot, mapped := p.plexDiscLocalRoot(it, v)
	switch {
	case it.MediaType == models.MediaTypeEpisode:
		// TV discs are only shown and kept in v1: never read, never a removal candidate.
		info.LocalRoot = localRoot
		info.Problem = "a full-disc backup in a TV library (not read; always kept)"
	case !detect:
		info.Problem = "full-disc detection is off (Settings → Media Management), so this disc was not read"
	case !mapped:
		info.Problem = "no path mapping covers " + roots[0] + ", so Dupearr cannot read this disc"
	default:
		info.LocalRoot = localRoot
		if dir := p.detectDirFor(it, localRoot); dir != "" {
			found, foundIn = p.findDisc(dir, localRoot), dir
		}
		if found == nil {
			info.Problem = "the disc at " + roots[0] + " was not found on disk or is not a complete disc structure"
		}
	}
	clearAttributes(v)
	v.Source, v.Container = models.SourceDisc, "disc"
	if found != nil {
		fillDisc(info, found, p.cfg.mapper, it.ServerID)
		info.Origin = models.DiscOriginPlex
		info.FileCount = found.FileCount
		if n := found.Discs(); n > 1 && len(roots) < n {
			info.Problem = joinProblems(info.Problem, fmt.Sprintf("Plex lists only %d of the %d discs of this set", len(roots), n))
			info.Removable = false
		}
		toLocal := func(r string) (string, bool) { return p.cfg.mapper.ToLocal(models.PathSourceServer, it.ServerID, r) }
		if other := uncoveredPlexRoots(roots, found, toLocal); len(other) > 0 {
			// Moving the disc found for the first root would leave the version half removed.
			info.Problem = joinProblems(info.Problem, "Plex also lists files of other disc folders in this version ("+strings.Join(other, ", ")+")")
			info.Removable = false
		}
		if discNamedForOther(foundIn, found, p.itemNamesIn(it, foundIn)) {
			// A custom scanner may list an extras disc as a version (GAP-02): never removed.
			info.Problem = joinProblems(info.Problem, "the disc's folders are named for extras or for another title")
			info.Removable = false
		}
		applyFeature(v, found, it)
	}
	v.Disc = info
	v.DisplayTitle = discDisplayTitle(info, v)
}

// uncoveredPlexRoots returns the disc roots (server paths) of a Plex disc version that are not
// roots of the disc found on disk for it (or cannot be mapped): the version spans more than that
// disc, so removing the disc would not remove the version.
func uncoveredPlexRoots(roots []string, found *disc.Disc, toLocal func(string) (string, bool)) []string {
	var out []string
	for _, r := range roots {
		local, ok := toLocal(r)
		if !ok || !slices.ContainsFunc(found.Roots, func(fr string) bool { return filepath.Clean(fr) == filepath.Clean(local) }) {
			out = append(out, r)
		}
	}
	return out
}

// clearAttributes forgets the media attributes Plex reported for a disc version.
func clearAttributes(v *models.MediaVersion) {
	v.DurationMs, v.BitrateKbps, v.VideoBitrate, v.Width, v.Height = 0, 0, 0, 0, 0
	v.Resolution, v.VideoCodec, v.VideoProfile, v.FrameRate, v.DynamicRange = "", "", "", "", ""
	v.BitDepth, v.DVProfile = 0, 0
	v.AudioTracks, v.SubtitleTracks = []models.AudioTrack{}, []models.SubtitleTrack{}
}

// discVersion builds the version of a disc found on disk next to movie it (origin "filesystem").
// ok is false when the disc cannot be described (no root).
func (p *pipeline) discVersion(it *models.MediaItem, d *disc.Disc) (models.MediaVersion, bool) {
	if strings.TrimSpace(d.Root) == "" {
		return models.MediaVersion{}, false
	}
	info := &models.DiscInfo{Origin: models.DiscOriginFilesystem}
	fillDisc(info, d, p.cfg.mapper, it.ServerID)
	v := models.MediaVersion{
		Key:            fmt.Sprintf("%s%d:%s", models.DiscKeyPrefix, it.ServerID, disc.RootHash(d.Root)),
		ServerID:       it.ServerID,
		LibraryID:      it.LibraryID,
		LibraryTitle:   it.LibraryTitle,
		SectionKey:     it.SectionKey,
		RatingKey:      it.RatingKey,
		ItemTitle:      it.Title,
		Container:      "disc",
		Source:         models.SourceDisc,
		AudioTracks:    []models.AudioTrack{},
		SubtitleTracks: []models.SubtitleTrack{},
		AddedAt:        discAddedAt(it.AddedAt, d),
		Disc:           info,
	}
	for i, root := range d.Roots {
		part := models.MediaPart{Path: info.Roots[i], LocalPath: root, Size: d.TotalSize}
		if len(d.Roots) > 1 {
			part.Size = p.rootSize(d, root)
		}
		v.Parts = append(v.Parts, part)
	}
	if len(v.Parts) == 0 {
		v.Parts = []models.MediaPart{{Path: info.Root, LocalPath: d.Root, Size: d.TotalSize}}
	}
	applyFeature(&v, d, it)
	v.DisplayTitle = discDisplayTitle(info, &v)
	return v, true
}

// rootSize measures the owned entries of one disc of a set (its part's size).
func (p *pipeline) rootSize(d *disc.Disc, root string) int64 {
	var entries []string
	for _, e := range d.OwnedEntries {
		if filepath.Clean(e) == filepath.Clean(root) || strictlyWithin(filepath.Clean(e), filepath.Clean(root)) {
			entries = append(entries, e)
		}
	}
	st, err := disc.Measure(p.ctx, entries, disc.Options{})
	if err != nil {
		return 0
	}
	return st.Bytes
}

// fillDisc copies an inspected disc into info (local paths from the disc, server paths mapped back
// with the media server's path mappings).
func fillDisc(info *models.DiscInfo, d *disc.Disc, mapper *pathmap.Mapper, serverID int64) {
	remote := func(local string) string {
		if r, ok := mapper.ToRemote(models.PathSourceServer, serverID, local); ok {
			return r
		}
		return local
	}
	info.Type = string(d.Type)
	info.LocalRoot = d.Root
	info.LocalRoots = slices.Clone(d.Roots)
	info.Roots = make([]string, 0, len(d.Roots))
	for _, r := range d.Roots {
		info.Roots = append(info.Roots, remote(r))
	}
	info.Root = remote(d.Root)
	info.Discs = d.Discs()
	info.FileCount = d.FileCount
	info.OwnedEntries = slices.Clone(d.OwnedEntries)
	info.TotalBytes, info.FeatureBytes, info.FreedBytes = d.TotalSize, d.FeatureBytes(), d.FreedBytes
	info.HardlinkedFiles = d.HardlinkedFiles
	info.Fingerprint = d.Fingerprint
	info.NewestModTime = d.NewestModTime.UTC()
	info.Is3D = d.Is3D
	info.Alternates = len(d.Alternates)
	info.Readable = d.Readable()
	info.MainFeature = ""
	if d.Main != nil {
		info.MainFeature = d.Main.Playlist
	}
	ok, why := d.Removable()
	info.Removable = ok
	info.Problem = ""
	switch {
	case d.Err != nil:
		info.Problem = errorText(d.Err)
	case !ok && !d.Type.ProtectOnly():
		info.Problem = why
	}
}

// joinProblems joins two problem texts.
func joinProblems(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// applyFeature sets a disc version's media attributes from its main feature (Blu-ray playlist,
// DVD title set); an image or unreadable disc leaves them unknown.
func applyFeature(v *models.MediaVersion, d *disc.Disc, it *models.MediaItem) {
	v.Edition = mediainfo.EditionWithTitles(discEditionPath(d), it.EditionTitle, "", it.Title)
	f := d.Main
	if f == nil || f.VideoCodec == "" {
		return
	}
	v.DurationMs = f.DurationMs
	v.Width, v.Height = f.Width, f.Height
	v.Resolution = mediainfo.ResolutionTier(f.Width, f.Height, "")
	v.VideoCodec = f.VideoCodec
	v.FrameRate = f.FrameRate
	v.BitDepth = f.BitDepth
	v.DynamicRange = f.DynamicRange
	v.DVProfile = f.DVProfile
	v.AudioTracks = append([]models.AudioTrack{}, f.AudioTracks...)
	v.SubtitleTracks = append([]models.SubtitleTrack{}, f.SubtitleTracks...)
	if bytes := d.FeatureBytes(); bytes > 0 && f.DurationMs > 0 {
		// kbps = bits per millisecond: the feature's clips (video and every audio track) over its
		// play time (docs/research/disc-structures.md §6.4).
		kbps := bytes * 8 / f.DurationMs
		v.BitrateKbps, v.VideoBitrate = int(min(kbps, 1<<31-1)), int(min(kbps, 1<<31-1))
	}
}

// discEditionPath is a path whose file and folder names carry a disc's edition tokens: inside the
// movie folder (the parent of a "Disc N" set folder), or the image file itself.
func discEditionPath(d *disc.Disc) string {
	if d.Type == disc.ISO {
		return d.Root
	}
	dir := d.Root
	if len(d.Roots) > 1 {
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "BDMV")
}

// discAddedAt dates a disc version: the later of the movie's Plex addedAt, the disc's newest file
// and the change time of its owned entries (a disc copied in with preserved modification times is
// still new). A later date only delays a removal (minimum age).
func discAddedAt(itemAdded time.Time, d *disc.Disc) time.Time {
	t := itemAdded
	if d.NewestModTime.After(t) {
		t = d.NewestModTime
	}
	for _, e := range d.OwnedEntries {
		if fi, err := os.Lstat(e); err == nil {
			if c := fileChangeTime(fi); c.After(t) {
				t = c
			}
		}
	}
	return t.UTC()
}

// discDisplayTitle is e.g. "UHD Blu-ray (BDMV) · 2160p (4K) HEVC".
func discDisplayTitle(info *models.DiscInfo, v *models.MediaVersion) string {
	label := disc.Type(info.Type).Label()
	if info.Discs > 1 {
		label += fmt.Sprintf(" · %d discs", info.Discs)
	}
	if info.IsLooseClips() && info.ClipCount > 0 {
		label += fmt.Sprintf(" · %d %s", info.ClipCount, plural(info.ClipCount, "clip", "clips"))
	}
	if v.Resolution != "" {
		label += " · " + mediainfo.ResolutionLabel(v.Resolution)
		if v.VideoCodec != "" {
			label += " " + strings.ToUpper(v.VideoCodec)
		}
	}
	return label
}

// plexItemsInDisc returns the other Plex items (rating keys, sorted) whose listed files lie inside
// a disc found on disk: removing the disc would take their files too.
func (p *pipeline) plexItemsInDisc(serverID int64, ownRK string, info *models.DiscInfo) []string {
	idx := p.discPathIndex()[serverID]
	if len(idx) == 0 {
		return nil
	}
	var out []string
	for _, r := range info.Roots {
		for _, rk := range idx[sharedKey(r)] {
			if rk != ownRK && !slices.Contains(out, rk) {
				out = append(out, rk)
			}
		}
	}
	sort.Strings(out)
	return out
}

// discPathIndex maps, per server, the disc root of every listed Plex file that lies inside a disc
// structure (or is a disc image) to the rating keys listing it (built once per scan).
func (p *pipeline) discPathIndex() map[int64]map[string][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discRoots != nil {
		return p.discRoots
	}
	out := map[int64]map[string][]string{}
	if p.index != nil {
		for server, paths := range p.index.paths {
			for key, rks := range paths {
				var root string
				if r, _, ok := disc.RootOf(key); ok {
					root = r
				} else if disc.IsImagePath(key) {
					root = key
				} else {
					continue
				}
				rk := sharedKey(root)
				if out[server] == nil {
					out[server] = map[string][]string{}
				}
				for _, x := range rks {
					if !slices.Contains(out[server][rk], x) {
						out[server][rk] = append(out[server][rk], x)
					}
				}
			}
		}
	}
	p.discRoots = out
	return out
}

// removesDisc reports whether a group decides to remove a disc version.
func removesDisc(g *models.DuplicateGroup) bool {
	for i := range g.Files {
		if f := &g.Files[i]; f.Decision == models.DecisionRemove && f.Version.Disc != nil {
			return true
		}
	}
	return false
}

// discTracked attaches the *arr file tracked inside a disc (or the tracked image) to a disc version:
// the version is tracked, but its attributes stay the disc's (the *arr's quality and media info
// describe one clip, or are absent for an image).
func discTracked(v *models.MediaVersion, info models.ArrFileInfo, trackedPath string) {
	v.Arr = &info
	if v.Disc != nil && !v.Disc.IsImage() {
		v.Disc.TrackedClip = trackedPath
	}
}

// withinPathKey reports whether the pathKey-normalized p equals root or lies below it.
func withinPathKey(p, root string) bool {
	return root != "" && (p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/"))
}
