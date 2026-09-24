package fakemedia

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultAge is how long ago versions were added when Version.Age is zero (older than Dupearr's
// default 7-day minimum age, so the default scenario is actionable).
const defaultAge = 30 * 24 * time.Hour

// world is the mutable state shared by every fake server. All fields are guarded by mu; handlers
// hold mu for the whole request, which keeps the fake trivially race-free.
type world struct {
	mu    sync.Mutex
	root  string // local directory that corresponds to RemoteRoot
	now   func() time.Time
	start time.Time

	plex     *plexState
	arrs     map[string]*arrState
	arrOrder []string
	tautulli *tautulliState

	fileSpecs map[string]*Version // media-root relative path → first version declaring it
	partSpecs map[string]Part     // media-root relative path → part declaration

	discs      []*discPlan            // full-disc backups (movies first, scenario order)
	discFiles  map[string]bool        // media-root relative paths written by the disc planner
	moviePlans map[*Movie][]*discPlan // build-time only: the discs of each scenario movie

	clipSets    []*clipSetPlan       // loose clip sets (scenario order)
	movieClips  map[*Movie][]Version // build-time only: the Plex versions of each movie's listed clips
	clipDeletes []ClipDelete         // delete requests that targeted clip-named files

	requests   []Request
	violations []Violation
	faults     []*Fault
	// keepTag is the *arr tag label whose items are protected (RuleDeleteKeepTagged); "" disables
	// the check.
	keepTag string
}

// plexState is the fake Plex Media Server.
type plexState struct {
	friendlyName, machineID, version, token, owner string
	allowDeletion, autoEmptyTrash                  bool
	pageLimit                                      int
	playing                                        []string
	createdAt                                      int64

	sections []*section
	items    map[string]*item
	order    []*item // creation order (items currently in the library)
	// all holds every item ever created, including items removed from the library when their
	// last media went: a scan that finds their files again brings them back (redetect).
	all []*item

	nextMediaID, nextPartID, nextStreamID int64
}

type section struct {
	key, title, typ, uuid string
	scanner               string   // Directory.scanner
	dirs                  []string // media-root relative
	createdAt, scannedAt  int64
}

type item struct {
	rk        string
	typ       string // movie | show | season | episode
	sec       *section
	title     string
	year      int
	edition   string
	guid      string
	guids     []string // Guid[] ids: imdb://…, tmdb://…, tvdb://…
	addedAt   int64
	updatedAt int64
	index     int // episode / season number
	parent    *item
	children  []*item
	media     []*media
	folder    string // shows: media-root relative folder
	// versions are the scenario's declared versions of a movie/episode; a scan re-detects one whose
	// files are back on disk after its media was removed (e.g. restored from a recycle bin).
	versions []Version
}

type media struct {
	id         int64
	v          Version // spec copy (Parts not used after construction; see parts)
	parts      []*part
	durationMs int64
	addedAt    int64
	trashed    bool // file(s) vanished and a refresh noticed it (Plex trash)
}

type part struct {
	id         int64
	rel        string
	size       int64
	durationMs int64
	streamIDs  []int64 // video, audio…, subtitles… (in that order); nil when unanalyzed
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// local returns the local path of a media-root relative path.
func (w *world) local(rel string) string {
	return filepath.Join(w.root, "media", filepath.FromSlash(rel))
}

// remote returns the path every fake server sees for a media-root relative path.
func remote(rel string) string { return RemoteMediaRoot + "/" + rel }

// relOf converts a remote media path back to a media-root relative path.
func relOf(remotePath string) (string, bool) {
	p := path.Clean(remotePath)
	if !strings.HasPrefix(p, RemoteMediaRoot+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(p, RemoteMediaRoot+"/")
	return rel, validRel(rel)
}

// remoteToLocal maps any remote path under RemoteRoot to its local path. Traversal is rejected.
func (w *world) remoteToLocal(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	c := path.Clean(p)
	if c == RemoteRoot {
		return w.root, true
	}
	if !strings.HasPrefix(c, RemoteRoot+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(c, RemoteRoot+"/")
	if !validRel(rel) {
		return "", false
	}
	return filepath.Join(w.root, filepath.FromSlash(rel)), true
}

// localToRemote maps a local path inside the root back to the remote view.
func (w *world) localToRemote(p string) (string, bool) {
	rel, err := filepath.Rel(w.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	if rel == "." {
		return RemoteRoot, true
	}
	return RemoteRoot + "/" + filepath.ToSlash(rel), true
}

// fileState reports whether the local file exists (regular file) and is readable.
func (w *world) fileState(rel string) (exists, accessible bool) {
	lp := w.local(rel)
	fi, err := os.Stat(lp)
	if err != nil || !fi.Mode().IsRegular() {
		return false, false
	}
	f, err := os.Open(lp)
	if err != nil {
		return true, false
	}
	_ = f.Close()
	return true, true
}

func (w *world) fileExists(rel string) bool {
	ok, _ := w.fileState(rel)
	return ok
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

// hashHex returns the first n hex characters of sha1(parts…).
func hashHex(n int, parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	s := hex.EncodeToString(h[:])
	if n > len(s) {
		n = len(s)
	}
	return s[:n]
}

func uuidFrom(parts ...string) string {
	h := hashHex(32, parts...)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// buildWorld turns a validated scenario into runtime state (no filesystem access). discScanner
// makes every movie library use the disc-image scanner (Options.DiscImageScanner).
func buildWorld(sc *Scenario, root string, now func() time.Time, discScanner bool) (*world, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	start := now().UTC().Truncate(time.Second)
	w := &world{
		root:      root,
		now:       now,
		start:     start,
		arrs:      map[string]*arrState{},
		fileSpecs: map[string]*Version{},
		partSpecs: map[string]Part{},
		keepTag:   DefaultKeepTag,

		discFiles:  map[string]bool{},
		moviePlans: map[*Movie][]*discPlan{},
		movieClips: map[*Movie][]Version{},
	}
	p := &plexState{
		friendlyName:   sc.Server.FriendlyName,
		machineID:      sc.Server.MachineIdentifier,
		version:        sc.Server.Version,
		token:          sc.Server.Token,
		owner:          sc.Server.Owner,
		allowDeletion:  sc.Server.AllowMediaDeletion,
		autoEmptyTrash: sc.Server.AutoEmptyTrash,
		createdAt:      start.Add(-400 * 24 * time.Hour).Unix(),
		items:          map[string]*item{},
		nextMediaID:    1000,
		nextPartID:     2000,
		nextStreamID:   10000,
	}
	if p.friendlyName == "" {
		p.friendlyName = "Fake Plex"
	}
	if p.version == "" {
		p.version = DefaultPlexVersion
	}
	w.plex = p

	secByKey := map[string]*section{}
	for _, l := range sc.Libraries {
		s := &section{
			key: l.Key, title: l.Title, typ: l.Type, dirs: append([]string(nil), l.Dirs...),
			uuid:      uuidFrom(p.machineID, "section", l.Key),
			createdAt: p.createdAt, scannedAt: start.Add(-time.Hour).Unix(),
			scanner: l.Scanner,
		}
		switch {
		case l.Type == LibraryMovie && discScanner:
			s.scanner = ScannerMovieDiscImage
		case s.scanner == "" && l.Type == LibraryShow:
			s.scanner = ScannerSeries
		case s.scanner == "":
			s.scanner = ScannerMovie
		}
		p.sections = append(p.sections, s)
		secByKey[l.Key] = s
	}

	// Rating keys: explicit ones first, then sequential numbers starting at 101.
	used := map[string]bool{}
	for _, m := range sc.Movies {
		used[m.RatingKey] = m.RatingKey != ""
		for _, p := range m.Plays {
			if p.RetiredKey != "" {
				used[p.RetiredKey] = true // an earlier Plex item's key: never handed out again
			}
		}
	}
	for _, sh := range sc.Shows {
		used[sh.RatingKey] = sh.RatingKey != ""
		for _, e := range sh.Episodes {
			used[e.RatingKey] = e.RatingKey != ""
		}
	}
	next := 100
	rkOr := func(explicit string) string {
		if explicit != "" {
			return explicit
		}
		for {
			next++
			k := strconv.Itoa(next)
			if !used[k] {
				used[k] = true
				return k
			}
		}
	}

	playsOf := map[*item][]Play{}
	for i := range sc.Movies {
		m := &sc.Movies[i]
		// Rating keys are assigned even to movies Plex does not list (disc-only folders under the
		// default scanner), so the other items keep their keys whatever the scanner.
		it := &item{
			rk: rkOr(m.RatingKey), typ: "movie", sec: secByKey[m.Section], title: m.Title, year: m.Year,
			edition: m.Edition, guid: m.GUID,
		}
		if it.guid == "" {
			it.guid = "plex://movie/" + hashHex(24, "movie", m.Title, strconv.Itoa(m.Year), m.Edition)
		}
		it.guids = externalGUIDs(m.ImdbID, m.TmdbID, 0)
		versions := append(append([]Version(nil), m.Versions...), w.planMovieDiscs(m, it.sec.scanner == ScannerMovieDiscImage)...)
		// Plex's default scanner lists every loose clip as its own version of the movie.
		versions = append(versions, w.planMovieClipSets(m, it)...)
		if len(versions) == 0 {
			continue // only discs (or unlisted clips), which the library's scanner skips: Plex has no item
		}
		w.addVersions(it, versions, Mins(120))
		p.addItem(it)
		playsOf[it] = m.Plays
	}
	for i := range sc.Shows {
		sh := &sc.Shows[i]
		show := &item{
			rk: rkOr(sh.RatingKey), typ: "show", sec: secByKey[sh.Section], title: sh.Title, year: sh.Year,
			guid: sh.GUID, folder: sh.Folder,
		}
		if show.guid == "" {
			show.guid = "plex://show/" + hashHex(24, "show", sh.Title, strconv.Itoa(sh.Year))
		}
		show.guids = externalGUIDs(sh.ImdbID, sh.TmdbID, sh.TvdbID)
		p.addItem(show)
		w.planShowDiscs(sh)
		eps := append([]Episode(nil), sh.Episodes...)
		sort.SliceStable(eps, func(a, b int) bool {
			if eps[a].Season != eps[b].Season {
				return eps[a].Season < eps[b].Season
			}
			return eps[a].Episode < eps[b].Episode
		})
		seasons := map[int]*item{}
		for j := range eps {
			e := &eps[j]
			season := seasons[e.Season]
			if season == nil {
				season = &item{
					rk: rkOr(""), typ: "season", sec: show.sec, title: seasonTitle(e.Season), index: e.Season,
					parent: show, year: sh.Year,
					guid: "plex://season/" + hashHex(24, "season", sh.Title, strconv.Itoa(e.Season)),
				}
				seasons[e.Season] = season
				show.children = append(show.children, season)
				p.addItem(season)
			}
			ep := &item{
				rk: rkOr(e.RatingKey), typ: "episode", sec: show.sec, title: e.Title, year: sh.Year,
				index: e.Episode, parent: season, guid: e.GUID,
			}
			if ep.title == "" {
				ep.title = fmt.Sprintf("Episode %d", e.Episode)
			}
			if ep.guid == "" {
				ep.guid = "plex://episode/" + hashHex(24, "episode", sh.Title, strconv.Itoa(e.Season), strconv.Itoa(e.Episode))
			}
			ep.guids = externalGUIDs("", e.TmdbID, e.TvdbID)
			w.addVersions(ep, e.Versions, Mins(45))
			season.children = append(season.children, ep)
			p.addItem(ep)
		}
		// Show/season addedAt = earliest episode.
		for _, season := range show.children {
			for _, ep := range season.children {
				season.addedAt = minNonZero(season.addedAt, ep.addedAt)
				season.updatedAt = max(season.updatedAt, ep.updatedAt)
			}
			show.addedAt = minNonZero(show.addedAt, season.addedAt)
			show.updatedAt = max(show.updatedAt, season.updatedAt)
		}
	}

	w.buildTautulli(sc, playsOf)
	if err := w.buildArrs(sc); err != nil {
		return nil, err
	}
	w.moviePlans, w.movieClips = nil, nil
	return w, nil
}

func seasonTitle(n int) string {
	if n == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", n)
}

func minNonZero(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

// externalGUIDs builds Plex Guid[] ids in Plex's order (imdb, tmdb, tvdb).
func externalGUIDs(imdb string, tmdb, tvdb int) []string {
	var g []string
	if imdb != "" {
		g = append(g, "imdb://"+imdb)
	}
	if tmdb > 0 {
		g = append(g, "tmdb://"+strconv.Itoa(tmdb))
	}
	if tvdb > 0 {
		g = append(g, "tvdb://"+strconv.Itoa(tvdb))
	}
	return g
}

// addVersions creates the media of an item and registers its files.
func (w *world) addVersions(it *item, versions []Version, defaultDur int64) {
	for i := range versions {
		ver := versions[i]
		age := ver.Age
		if age <= 0 {
			age = defaultAge
		}
		added := w.start.Add(-age).Unix()
		m := w.plex.newMedia(ver, defaultDur, added)
		it.media = append(it.media, m)
		it.versions = append(it.versions, ver)
		it.addedAt = minNonZero(it.addedAt, added)
		it.updatedAt = max(it.updatedAt, added)
		for _, sp := range ver.Parts {
			if _, ok := w.fileSpecs[sp.File]; !ok {
				v := versions[i]
				w.fileSpecs[sp.File] = &v
				w.partSpecs[sp.File] = sp
			}
		}
	}
}

func (p *plexState) newMedia(ver Version, defaultDur int64, addedAt int64) *media {
	m := &media{id: p.nextMediaID, v: ver, addedAt: addedAt, durationMs: ver.DurationMs}
	p.nextMediaID++
	if m.durationMs <= 0 {
		m.durationMs = defaultDur
	}
	for _, sp := range ver.Parts {
		pd := sp.DurationMs
		if pd <= 0 {
			pd = m.durationMs / int64(len(ver.Parts))
		}
		pt := &part{id: p.nextPartID, rel: sp.File, size: sp.Size, durationMs: pd}
		p.nextPartID++
		if !ver.Unanalyzed {
			n := 1 + len(ver.Audio) + len(ver.Subtitles)
			for k := 0; k < n; k++ {
				pt.streamIDs = append(pt.streamIDs, p.nextStreamID)
				p.nextStreamID++
			}
		}
		m.parts = append(m.parts, pt)
	}
	return m
}

func (p *plexState) addItem(it *item) {
	p.items[it.rk] = it
	p.order = append(p.order, it)
	p.all = append(p.all, it)
}

// restoreItem puts an item removed by removeItem back into the library (and its removed
// ancestors, for an episode), keeping children sorted by index.
func (p *plexState) restoreItem(it *item) {
	if p.items[it.rk] == it {
		return
	}
	p.items[it.rk] = it
	p.order = append(p.order, it)
	par := it.parent
	if par == nil {
		return
	}
	found := false
	for _, c := range par.children {
		found = found || c == it
	}
	if !found {
		par.children = append(par.children, it)
		sort.SliceStable(par.children, func(a, b int) bool { return par.children[a].index < par.children[b].index })
	}
	p.restoreItem(par)
}

// isDescendant reports whether it is anc or lies below it (season/show ancestry).
func isDescendant(it, anc *item) bool {
	for x := it; x != nil; x = x.parent {
		if x == anc {
			return true
		}
	}
	return false
}

// redetect is the "new files" half of a Plex scan: a declared, non-optimized version of a candidate
// movie/episode whose parts are all on disk again (inside scope, a media-root relative folder; ""
// = anywhere) while the item has no media using those files gets a new media (new ids; its part
// key carries the current time, the item keeps its addedAt: Plex has no per-version date) — a file
// restored from a recycle bin shows up again. An item removed from the library when its last media
// went is brought back. Files the scenario never declared are not picked up.
// Callers hold w.mu.
func (w *world) redetect(candidates []*item, scope string) {
	p := w.plex
	now := w.now().Unix()
	for _, it := range candidates {
		if it.typ != "movie" && it.typ != "episode" {
			continue
		}
		defaultDur := Mins(120)
		if it.typ == "episode" {
			defaultDur = Mins(45)
		}
		for _, v := range it.versions {
			if len(v.Parts) == 0 || v.Optimized || v.OptimizedTarget != "" {
				continue // a "Plex Versions" file is never picked up as a scanned version
			}
			rels := map[string]bool{}
			inScope, onDisk := scope == "", true
			for _, sp := range v.Parts {
				rels[sp.File] = true
				if strings.Contains(strings.ToLower("/"+sp.File), "/plex versions/") {
					onDisk = false
				}
				if scope != "" && under(sp.File, scope) {
					inScope = true
				}
				if sp.Missing || !w.fileExists(sp.File) {
					onDisk = false
				}
			}
			if !inScope || !onDisk {
				continue
			}
			known := false
			for _, m := range it.media {
				known = known || m.hasFile(rels)
			}
			if known {
				continue
			}
			it.media = append(it.media, p.newMedia(v, defaultDur, now))
			it.updatedAt = now
			p.restoreItem(it)
		}
	}
}

// removeItem drops an item (and empty ancestors for episodes) from the library.
func (p *plexState) removeItem(it *item) {
	delete(p.items, it.rk)
	for i, x := range p.order {
		if x == it {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	if par := it.parent; par != nil {
		for i, c := range par.children {
			if c == it {
				par.children = append(par.children[:i], par.children[i+1:]...)
				break
			}
		}
		if len(par.children) == 0 {
			p.removeItem(par)
		}
	}
}

func (m *media) totalSize() int64 {
	var n int64
	for _, p := range m.parts {
		n += p.size
	}
	return n
}

// bitrateKbps returns the overall bitrate (explicit or derived from size and duration).
func (m *media) bitrateKbps() int {
	if m.v.BitrateKbps > 0 {
		return m.v.BitrateKbps
	}
	if m.durationMs <= 0 {
		return 0
	}
	return int(m.totalSize() * 8 / m.durationMs) // bytes*8/ms = kbit/s
}

// isOptimized reports a Plex Optimized Version: proxyType 42, or any part under a "Plex Versions"
// folder (how Dupearr recognises them, docs/DECISIONS.md D2).
func (m *media) isOptimized() bool {
	if m.v.Optimized {
		return true
	}
	for _, pt := range m.parts {
		if strings.Contains(strings.ToLower("/"+pt.rel), "/plex versions/") {
			return true
		}
	}
	return false
}

// hasFile reports whether one of m's parts is in rels (media-root relative paths).
func (m *media) hasFile(rels map[string]bool) bool {
	for _, pt := range m.parts {
		if rels[pt.rel] {
			return true
		}
	}
	return false
}

// mediaAvailable reports whether every part of m exists on disk.
func (w *world) mediaAvailable(m *media) bool {
	for _, pt := range m.parts {
		if !w.fileExists(pt.rel) {
			return false
		}
	}
	return len(m.parts) > 0
}

// hasAvailableCopy reports whether it still has a non-optimized version whose parts all exist on
// disk (i.e. something a user can play).
func (w *world) hasAvailableCopy(it *item) bool {
	for _, m := range it.media {
		if !m.isOptimized() && w.mediaAvailable(m) {
			return true
		}
	}
	return false
}

// isPlaying reports whether rk is listed by /status/sessions.
func (p *plexState) isPlaying(rk string) bool {
	for _, x := range p.playing {
		if x == rk {
			return true
		}
	}
	return false
}

// removalGuard checks a file removal against Dupearr's safety invariants (docs/ARCHITECTURE.md §6
// "Safety invariants"): it snapshots, before the removal, which Plex items reference the files and
// whether they can be played, so the losses can be reported afterwards. Callers hold w.mu.
type removalGuard struct {
	rels    map[string]bool
	items   []*item // movies/episodes with a media part at one of rels (creation order)
	before  []bool  // hasAvailableCopy per item, before the removal
	existed bool    // at least one of rels existed on disk
}

// newRemovalGuard snapshots the Plex items using any of rels (media-root relative paths) before they
// are removed. Callers hold w.mu.
func (w *world) newRemovalGuard(rels ...string) *removalGuard {
	g := &removalGuard{rels: map[string]bool{}}
	for _, r := range rels {
		g.rels[r] = true
		if w.fileExists(r) {
			g.existed = true
		}
	}
	for _, it := range w.plex.order {
		if it.typ != "movie" && it.typ != "episode" {
			continue
		}
		for _, m := range it.media {
			if m.hasFile(g.rels) {
				g.items = append(g.items, it)
				g.before = append(g.before, w.hasAvailableCopy(it))
				break
			}
		}
	}
	return g
}

// otherVersion reports whether it has a non-optimized version that does not use any removed file.
func (g *removalGuard) otherVersion(it *item) bool {
	for _, m := range it.media {
		if !m.isOptimized() && !m.hasFile(g.rels) {
			return true
		}
	}
	return false
}

// reportPlaying records RuleDeleteWhilePlaying for every playing item that used a removed file.
func (w *world) reportPlaying(server string, r *http.Request, g *removalGuard) {
	if !g.existed {
		return
	}
	for _, it := range g.items {
		if w.plex.isPlaying(it.rk) {
			w.violate(server, r, RuleDeleteWhilePlaying, fmt.Sprintf("%s (rating key %s) is being played; Dupearr defers playing items", describeItem(it), it.rk))
		}
	}
}

// reportLosses records RuleDeleteLastCopy for items (other than skip) that could be played before
// the removal and cannot anymore. With sharedOnly, an item whose only versions used the removed
// file is reported only when the file was shared with another item (a multi-episode file): a lone
// item losing its only copy is a legitimate cross-library removal when the keeper lives in another
// library, which the fake cannot see.
func (w *world) reportLosses(server string, r *http.Request, g *removalGuard, skip *item, sharedOnly bool) {
	for i, it := range g.items {
		if it == skip || !g.before[i] || w.hasAvailableCopy(it) {
			continue
		}
		if sharedOnly && len(g.items) < 2 && !g.otherVersion(it) {
			continue
		}
		w.violate(server, r, RuleDeleteLastCopy, fmt.Sprintf("%s (rating key %s) has no available version left", describeItem(it), it.rk))
	}
}

// reportKeepTagged records RuleDeleteKeepTagged when a removed file is the tracked file of an *arr
// movie/series carrying the keep tag (docs/DECISIONS.md D3 A9: such versions are protected).
func (w *world) reportKeepTagged(server string, r *http.Request, g *removalGuard) {
	if w.keepTag == "" || !g.existed {
		return
	}
	for _, name := range w.arrOrder {
		a := w.arrs[name]
		tagID := int64(-1)
		for _, t := range a.tags {
			if strings.EqualFold(t.Label, w.keepTag) {
				tagID = t.ID
			}
		}
		if tagID < 0 {
			continue
		}
		for _, f := range a.files {
			if !g.rels[f.rel] {
				continue
			}
			var tags []int64
			var title string
			if a.isRadarr() {
				if m := a.movieByID(f.itemID); m != nil {
					tags, title = m.tags, m.title
				}
			} else if s := a.seriesByID(f.itemID); s != nil {
				tags, title = s.tags, s.title
			}
			for _, id := range tags {
				if id == tagID {
					w.violate(server, r, RuleDeleteKeepTagged, fmt.Sprintf("%q is tracked by %s item %q, which is tagged %q", remote(f.rel), name, title, w.keepTag))
					return
				}
			}
		}
	}
}

// describeItem names a movie or episode for violation details.
func describeItem(it *item) string {
	if it.typ == "episode" && it.parent != nil && it.parent.parent != nil {
		return fmt.Sprintf("episode %q S%02dE%02d", it.parent.parent.title, it.parent.index, it.index)
	}
	return fmt.Sprintf("%s %q", it.typ, it.title)
}

// sectionOf returns the section with key k.
func (p *plexState) sectionOf(k string) *section {
	for _, s := range p.sections {
		if s.key == k {
			return s
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Filesystem
// ---------------------------------------------------------------------------

// markerName marks a directory as owned by fakemedia (so it may be reset safely).
const markerName = ".fakemedia"

// prepareDir makes dir usable: an empty (or missing) directory gets a marker; a directory that
// already carries the marker has its media/recycle trees removed. Any other non-empty directory is
// refused — the fake never deletes data it did not create.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read data dir: %w", err)
	}
	marker := filepath.Join(dir, markerName)
	if len(entries) > 0 {
		if _, err := os.Lstat(marker); err != nil {
			return fmt.Errorf("refusing to use non-empty directory %s: it was not created by fakemedia (no %s marker)", dir, markerName)
		}
		for _, sub := range []string{"media", "recycle", ".unmounted"} {
			if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
				return fmt.Errorf("reset %s: %w", sub, err)
			}
		}
	}
	const note = "This directory is managed by Dupearr's fakemedia test fixture; its media/ and recycle/ trees are recreated on every start.\n"
	if err := os.WriteFile(marker, []byte(note), 0o644); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}

// materialize creates the directory tree and the (sparse) files of the scenario.
func (w *world) materialize() error {
	for _, s := range w.plex.sections {
		for _, d := range s.dirs {
			if err := os.MkdirAll(w.local(d), 0o755); err != nil {
				return fmt.Errorf("create library dir: %w", err)
			}
		}
	}
	for _, name := range w.arrOrder {
		for _, d := range w.arrs[name].rootFolders {
			if err := os.MkdirAll(w.local(d), 0o755); err != nil {
				return fmt.Errorf("create root folder: %w", err)
			}
		}
	}
	if err := w.materializeDiscs(); err != nil {
		return err
	}
	if err := w.materializeClipSets(); err != nil {
		return err
	}
	rels := make([]string, 0, len(w.partSpecs))
	for rel := range w.partSpecs {
		if !w.discFiles[rel] { // a disc-image scanner's Parts are disc files, already written
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	// Regular files first, then hard links (their targets must exist).
	for _, pass := range []bool{false, true} {
		for _, rel := range rels {
			sp := w.partSpecs[rel]
			if sp.Missing || (sp.LinkTo != "") != pass {
				continue
			}
			lp := w.local(rel)
			if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
				return fmt.Errorf("create dir for %s: %w", rel, err)
			}
			if pass {
				if err := os.Link(w.local(sp.LinkTo), lp); err != nil {
					return fmt.Errorf("hard link %s: %w", rel, err)
				}
				continue
			}
			if err := createSparse(lp, sp.Size); err != nil {
				return fmt.Errorf("create %s: %w", rel, err)
			}
		}
	}
	// Movie folders of *arr movies without files still exist on disk (like Radarr's).
	for _, name := range w.arrOrder {
		for _, m := range w.arrs[name].movies {
			if err := os.MkdirAll(w.local(m.folder), 0o755); err != nil {
				return fmt.Errorf("create movie folder: %w", err)
			}
		}
		for _, s := range w.arrs[name].series {
			if err := os.MkdirAll(w.local(s.folder), 0o755); err != nil {
				return fmt.Errorf("create series folder: %w", err)
			}
		}
	}
	return nil
}

// createSparse creates a new file of the given size without writing data (sparse on filesystems
// that support it).
func createSparse(p string, size int64) (err error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return f.Truncate(size)
}

// CreateFile creates a sparse file of the given size at a media-root relative path (parent
// directories included). Use it in tests to drop extra, unknown files into the tree (e.g. an
// untracked copy an *arr rescan should adopt).
func (e *Env) CreateFile(rel string, size int64) error {
	if !validRel(rel) {
		return fmt.Errorf("fakemedia: invalid relative path %q", rel)
	}
	if size < 0 {
		return errors.New("fakemedia: negative size")
	}
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	lp := e.w.local(rel)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return fmt.Errorf("fakemedia: create dir: %w", err)
	}
	if err := createSparse(lp, size); err != nil {
		return fmt.Errorf("fakemedia: create file: %w", err)
	}
	return nil
}

// RemoveFile deletes a file behind the servers' backs (simulates an external deletion).
func (e *Env) RemoveFile(rel string) error {
	if !validRel(rel) {
		return fmt.Errorf("fakemedia: invalid relative path %q", rel)
	}
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if err := os.Remove(e.w.local(rel)); err != nil {
		return fmt.Errorf("fakemedia: remove file: %w", err)
	}
	return nil
}

// hasSubdirs reports whether dir exists, is a directory and has at least one subdirectory.
func hasSubdirs(dir string) (exists, nonEmpty bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true, true
		}
		if e.Type()&fs.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && fi.IsDir() {
				return true, true
			}
		}
	}
	return true, false
}

// locationUnavailable reports whether a media-root relative directory (a library location) is
// missing, unreadable or empty — what an unmounted share looks like.
func (w *world) locationUnavailable(dir string) bool {
	entries, err := os.ReadDir(w.local(dir))
	return err != nil || len(entries) == 0
}

// removeIfExists removes a file; a file that is already gone is not an error.
func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
