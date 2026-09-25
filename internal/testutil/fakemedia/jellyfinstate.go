package fakemedia

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The fake Jellyfin 12.1 server's model (docs/research/jellyfin-emby.md §3): unlike the fake Plex,
// which lists the scenario's declared items, it resolves its items by walking its library folders
// on disk with a port of Jellyfin's rules (§3.1), so it can serve the same tree as the fake Plex
// (two servers on one share) and show what Jellyfin really groups:
//
//   - movie folders: the video files of a folder are versions of ONE movie when the folder name is
//     longer than one character, all files have the same year (or none) and every file name
//     starts with the folder name followed by nothing or "-", "_", ".", "[" (after spaces); the
//     file named like the folder is the primary, otherwise names with a resolution come first
//     (descending), then the rest alphabetically, and a stacked entry is preferred. Otherwise
//     every file is its own movie (a mixed folder: Eta's stray release). A file in the library
//     root, or a folder that yields more than one movie, is "in a mixed folder" (what a delete
//     removes depends on it, §3.3). A movie file next to another movie's subfolder is not
//     (Marvel/Thor);
//   - stacks (-cd1/-cd2, -part1, -pt1, -disc1, -disk1, -dvd1) are one entry whose other parts are
//     items of their own, listed by /Videos/{id}/AdditionalParts; an alternate version can be a
//     stack itself (Kappa);
//   - episode folders: files of one folder that parse to the same season and episode are
//     versions of one episode (resolution names first, then alphabetically); the ending episode of
//     a multi-episode file is not part of the key, so S01E03-E04 next to S01E03 is a hidden
//     alternate of episode 3 without IndexNumberEnd, and alone it is episode 3 with
//     IndexNumberEnd 4;
//   - a .strm file is an ordinary file version (Container strm, Size its own bytes, no streams);
//   - ids are MD5(type full name + path) in N format, so a file keeps its id across rescans;
//   - an empty .ignore in a folder or an ancestor hides it; the answer is cached per folder until a
//     library scan (Env.JellyfinScan), like Jellyfin's DotIgnoreIgnoreRule cache;
//   - the listing is a snapshot: files removed on disk stay listed ("ghost" entries, §3.2) until
//     POST /Library/Media/Updated names them and LibraryMonitorDelay has passed on the Env clock
//     (Env.JellyfinApplyPending applies them at once), or a library scan runs.
//
// JellyfinServer.Merges link movies like POST /Videos/MergeVersions (Zeta across libraries), the
// first one being the primary: a library that holds the primary lists it as a row with every other
// copy as a Grouping source and hides its own other copies; a library that does not lists each of
// its copies as a row with every other copy as a Grouping source (with the API key; live on 12.1).
//
// Path substitutions rewrite every path the API reports (items, sources, parts, series; first
// matching entry, a prefix on a folder boundary, any case) but not the library folders, like
// Jellyfin (live on 12.1): ids stay derived from the real paths.

// Jellyfin type names ids are derived from (Jellyfin's GetNewItemId: MD5 of type full name + path).
const (
	jfTypeMovie      = "MediaBrowser.Controller.Entities.Movies.Movie"
	jfTypeEpisode    = "MediaBrowser.Controller.Entities.TV.Episode"
	jfTypeSeries     = "MediaBrowser.Controller.Entities.TV.Series"
	jfTypeVideo      = "MediaBrowser.Controller.Entities.Video"
	jfTypeCollection = "MediaBrowser.Controller.Entities.CollectionFolder"
)

// jfID returns a Jellyfin id: MD5(type + path), 32 lower-case hex digits ("N" format).
func jfID(typ, p string) string {
	h := md5.Sum([]byte(typ + p))
	return hex.EncodeToString(h[:])
}

// jellyfinState is the fake Jellyfin server; guarded by world.mu.
type jellyfinState struct {
	name, serverID, apiKey, userToken, version, mediaRoot string

	libs         []*jfLibrary
	subst        []JellyfinPathSubstitution
	monitorDelay time.Duration
	merges       [][]string

	// declared ids of scenario files and shows (Jellyfin matches titles online; the fake takes what
	// the scenario declares when a folder or file name carries no id tag).
	fileIDs  map[string]jfDeclared
	showIDs  map[string]jfDeclared // show folder → ids
	epTitles map[string]string     // file → episode title

	items       []*jfItem
	series      map[string]*jfSeries // series folder → series
	ignoreCache map[string]bool      // folder → an empty .ignore there (cached until a library scan)
	pending     []jfPending
	notified    []JellyfinNotification
	sessions    []JellyfinSession
	lastScan    time.Time
	totalDrift  int  // added to TotalRecordCount on every page after the first
	omitSize    bool // AdditionalParts items carry no size
	pageLimit   int
}

type jfDeclared struct {
	tmdb, tvdb int
	imdb       string
	title      string
	year       int
	added      time.Time
}

type jfLibrary struct {
	id, name, collection string
	dirs                 []string // media-root relative
}

// jfPart is one file of a version.
type jfPart struct {
	rel  string
	id   string // the part's own item id (MD5(Video + path)); the first part's is the version id
	size int64
}

// jfVersion is one version (media source) of an item: its own item id and files.
type jfVersion struct {
	id      string
	name    string // the source's Name (the version label)
	parts   []jfPart
	strm    bool
	epStart int // episodes: the file's own episode range
	epEnd   int
}

// jfItem is one listed row: a primary version with its local alternates.
type jfItem struct {
	typ      string // Movie | Episode
	lib      *jfLibrary
	folder   string // the folder the item was resolved from (media-root relative)
	mixed    bool   // IsInMixedFolder
	name     string
	year     int
	ids      map[string]string // ProviderIds
	season   int               // episodes (-1 = none)
	episode  int
	series   *jfSeries
	versions []*jfVersion // [0] is the primary (the row id)
	linked   []*jfItem    // merged items (Grouping sources)
	hidden   bool         // an item merged into another of the same library (not listed)
	added    time.Time
}

func (it *jfItem) id() string { return it.versions[0].id }

type jfSeries struct {
	id, name, folder string
	year             int
	ids              map[string]string
}

type jfPending struct {
	folders []string
	due     time.Time
}

// JellyfinServer describes the fake Jellyfin server (Scenario.Jellyfin). It serves the scenario's
// tree (the same one the fake Plex serves) and resolves its items from disk (see jellyfinstate.go).
type JellyfinServer struct {
	ServerName string // "" = "Fake Jellyfin"
	ServerID   string // 32 hex; "" = DefaultJellyfinServerID
	APIKey     string // "" = DefaultJellyfinAPIKey (an administrator key, like every Jellyfin API key)
	// UserToken is the token of a user who is not an administrator ("" = DefaultJellyfinUserToken):
	// GET /Library/VirtualFolders refuses it and /Sessions shows only its own sessions.
	UserToken string
	Version   string // "" = DefaultJellyfinVersion (12.1.0)
	// MediaRoot is the path the server sees the media root at ("" = RemoteMediaRoot).
	MediaRoot string
	Libraries []JellyfinLibrary
	// PathSubstitutions are returned by /System/Configuration and rewrite the paths the API reports
	// (not the library folders), like Jellyfin.
	PathSubstitutions []JellyfinPathSubstitution
	// LibraryMonitorDelay is how long after POST /Library/Media/Updated the notified folders are
	// read again (0 = 60 s, Jellyfin's default), on the Env's clock.
	LibraryMonitorDelay time.Duration
	// Merges link movies as versions of one title (POST /Videos/MergeVersions): each entry names the
	// primary files (media-root relative) of the movies, the first one being the title's primary.
	Merges [][]string
}

// JellyfinLibrary is a Jellyfin library ("virtual folder").
type JellyfinLibrary struct {
	Name           string
	CollectionType string   // "movies", "tvshows", "homevideos", "musicvideos", "music" or "" (mixed content); only movies and tvshows are synced
	Dirs           []string // relative to the media root
}

// JellyfinPathSubstitution is one entry of Jellyfin's path substitutions.
type JellyfinPathSubstitution struct{ From, To string }

// JellyfinSession is one session of GET /Sessions (Env.SetJellyfinSessions).
type JellyfinSession struct {
	ItemID           string // NowPlayingItem.Id ("" = nothing playing)
	PrimaryVersionID string // NowPlayingItem.PrimaryVersionId
	MediaSourceID    string // PlayState.MediaSourceId
	Paused           bool
	// User is true for a session of the non-administrator user (the only one its token sees).
	User bool
}

// JellyfinNotification is one POST /Library/Media/Updated the fake received.
type JellyfinNotification struct {
	Paths      []string
	UpdateType []string
	Time       time.Time
}

// Default identity and credentials of the fake Jellyfin (fake values for a local fake).
const (
	ServerJellyfin           = "jellyfin"
	DefaultJellyfinServerID  = "4a9f0000000000000000000000001201"
	DefaultJellyfinAPIKey    = "a11ce0000000000000000000000000e7"
	DefaultJellyfinUserToken = "b0b0000000000000000000000000usr1"
	DefaultJellyfinVersion   = "12.1.0"
)

// ExtraFile is a file of the scenario tree that no Plex item lists (Scenario.ExtraFiles): sidecars,
// unrelated files, .strm shortcuts (Content is written; otherwise a sparse file of Size bytes).
type ExtraFile struct {
	File    string // relative to the media root
	Size    int64
	Content string
}

// buildJellyfin builds the Jellyfin state of a scenario (nil when it has none).
func (w *world) buildJellyfin(sc *Scenario) error {
	j := sc.Jellyfin
	if j == nil {
		return nil
	}
	s := &jellyfinState{
		name: j.ServerName, serverID: strings.ToLower(j.ServerID), apiKey: j.APIKey, userToken: j.UserToken,
		version: j.Version, mediaRoot: j.MediaRoot, subst: append([]JellyfinPathSubstitution(nil), j.PathSubstitutions...),
		monitorDelay: j.LibraryMonitorDelay,
		fileIDs:      map[string]jfDeclared{}, showIDs: map[string]jfDeclared{}, epTitles: map[string]string{},
		series: map[string]*jfSeries{}, ignoreCache: map[string]bool{},
	}
	if s.name == "" {
		s.name = "Fake Jellyfin"
	}
	if s.serverID == "" {
		s.serverID = DefaultJellyfinServerID
	}
	if s.apiKey == "" {
		s.apiKey = DefaultJellyfinAPIKey
	}
	if s.userToken == "" {
		s.userToken = DefaultJellyfinUserToken
	}
	if s.version == "" {
		s.version = DefaultJellyfinVersion
	}
	if s.mediaRoot == "" {
		s.mediaRoot = RemoteMediaRoot
	}
	if s.monitorDelay <= 0 {
		s.monitorDelay = 60 * time.Second
	}
	for _, m := range j.Merges {
		s.merges = append(s.merges, append([]string(nil), m...))
	}
	for _, l := range j.Libraries {
		s.libs = append(s.libs, &jfLibrary{id: jfID(jfTypeCollection, l.Name), name: l.Name, collection: l.CollectionType, dirs: append([]string(nil), l.Dirs...)})
	}
	for _, m := range sc.Movies {
		for _, v := range m.Versions {
			age := v.Age
			if age <= 0 {
				age = defaultAge
			}
			for _, p := range v.Parts {
				if _, ok := s.fileIDs[p.File]; !ok {
					s.fileIDs[p.File] = jfDeclared{tmdb: m.TmdbID, imdb: m.ImdbID, title: m.Title, year: m.Year, added: w.start.Add(-age)}
				}
			}
		}
	}
	for _, sh := range sc.Shows {
		if sh.Folder != "" {
			s.showIDs[sh.Folder] = jfDeclared{tvdb: sh.TvdbID, tmdb: sh.TmdbID, imdb: sh.ImdbID, title: sh.Title, year: sh.Year}
		}
		for _, e := range sh.Episodes {
			for _, v := range e.Versions {
				age := v.Age
				if age <= 0 {
					age = defaultAge
				}
				for _, p := range v.Parts {
					if _, ok := s.fileIDs[p.File]; !ok {
						s.fileIDs[p.File] = jfDeclared{tvdb: e.TvdbID, tmdb: e.TmdbID, title: e.Title, added: w.start.Add(-age)}
						s.epTitles[p.File] = e.Title
					}
				}
			}
		}
	}
	w.jellyfin = s
	return nil
}

// jfRemote returns the path the Jellyfin server sees for a media-root relative path.
func (s *jellyfinState) remote(rel string) string { return s.mediaRoot + "/" + rel }

// shown returns the path the API reports for a media-root relative path: rewritten by the first
// path substitution whose From is a folder prefix of it (any case), like Jellyfin's
// TryReplaceSubPath.
func (s *jellyfinState) shown(rel string) string {
	p := s.remote(rel)
	for _, sub := range s.subst {
		from := strings.TrimRight(sub.From, "/")
		if from == "" || len(p) < len(from) || !strings.EqualFold(p[:len(from)], from) {
			continue
		}
		if len(p) == len(from) || p[len(from)] == '/' {
			return strings.TrimRight(sub.To, `/\`) + p[len(from):]
		}
	}
	return p
}

// relOf converts a path of the Jellyfin server back to a media-root relative path.
func (s *jellyfinState) relOf(remotePath string) (string, bool) {
	return relUnder(s.mediaRoot, remotePath)
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

var (
	jfVideoExt = map[string]bool{
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true, ".ts": true,
		".m2ts": true, ".mts": true, ".mpg": true, ".mpeg": true, ".webm": true, ".strm": true,
	}
	// jfSkipDirs are never resolved as movie folders: extras and disc structures.
	jfSkipDirs = map[string]bool{
		"extras": true, "featurettes": true, "behind the scenes": true, "deleted scenes": true, "interviews": true,
		"scenes": true, "shorts": true, "trailers": true, "other": true, "bdmv": true, "video_ts": true,
		"certificate": true, "aacs": true, "backup": true,
	}
	reJFExtra      = regexp.MustCompile(`(?i)[-_. ](trailer|sample|featurette|behindthescenes|deleted|interview|scene|short|other)$`)
	reJFStack      = regexp.MustCompile(`(?i)^(.*?)[ _.-]*(?:cd|dvd|part|pt|disc|disk)[ _.-]*([0-9]+)$`)
	reJFYear       = regexp.MustCompile(`(?:^|[ ._(\[])((?:19|20)[0-9]{2})(?:$|[ ._)\]])`)
	reJFResolution = regexp.MustCompile(`(?i)([0-9]{2}[0-9]+)[ip]`)
	// reJFEpisode parses SxxEyy with an optional ending episode in Jellyfin's multi-episode forms
	// (E03E04, E03-E04, E03-04, E03x04, E03-x04, E03xE04; Emby.Naming MultipleEpisodeExpressions).
	reJFEpisode   = regexp.MustCompile(`(?i)s([0-9]{1,4})e([0-9]{1,4})(?:(?:-| - )?x?e([0-9]{1,4})|(?:-| - )?x([0-9]{1,4})|-([0-9]{1,4})\b)?`)
	reJFSeason    = regexp.MustCompile(`(?i)^(?:season|series|staffel)[ ._-]*([0-9]{1,4})$`)
	reJFTag       = regexp.MustCompile(`(?i)[\[{(]\s*(tmdb|tmdbid|imdb|imdbid|tvdb|tvdbid)\s*[-=]\s*([a-z0-9]+)\s*[\]})]`)
	reJFTags      = regexp.MustCompile(`\s*(\[[^\]]*\]|\{[^}]*\})`)
	reJFYearParen = regexp.MustCompile(`\s*\((?:19|20)[0-9]{2}\)`)
)

// jfEntry is a stack or a single file of a folder (one candidate version).
type jfEntry struct {
	name  string // file base name without extension (a stack: without the part token)
	files []string
	strm  bool
}

// ignored reports whether a folder (media-root relative) or an ancestor holds an empty .ignore;
// the answer per folder is cached until a library scan (Jellyfin caches its lookups).
func (w *world) jfIgnored(rel string) bool {
	s := w.jellyfin
	for d := path.Clean(rel); ; d = path.Dir(d) {
		ign, ok := s.ignoreCache[d]
		if !ok {
			fi, err := os.Stat(filepath.Join(w.local(d), ".ignore"))
			ign = err == nil && fi.Mode().IsRegular() && fi.Size() == 0
			s.ignoreCache[d] = ign
		}
		if ign {
			return true
		}
		if d == "." || d == "/" {
			return false
		}
	}
}

// jfResolveAll rebuilds the snapshot from disk (a library scan).
func (w *world) jfResolveAll() {
	s := w.jellyfin
	s.items = nil
	s.series = map[string]*jfSeries{}
	for _, lib := range s.libs {
		for _, d := range lib.dirs {
			w.jfResolveTree(lib, d, true, true)
		}
	}
	w.jfApplyMerges()
}

// jfResolveTree resolves a folder (and, recursively, its subfolders).
func (w *world) jfResolveTree(lib *jfLibrary, rel string, isRoot, recursive bool) {
	if w.jfIgnored(rel) || lib.collection == "music" {
		return // a music library resolves no video items
	}
	entries, err := os.ReadDir(w.local(rel))
	if err != nil {
		return
	}
	var files, dirs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		fi, err := os.Stat(filepath.Join(w.local(rel), name))
		if err != nil {
			continue
		}
		switch {
		case fi.IsDir():
			if !jfSkipDirs[strings.ToLower(name)] {
				dirs = append(dirs, path.Join(rel, name))
			}
		case fi.Mode().IsRegular() && jfVideoExt[strings.ToLower(path.Ext(name))] &&
			!reJFExtra.MatchString(strings.TrimSuffix(name, path.Ext(name))):
			files = append(files, path.Join(rel, name))
		}
	}
	sort.Strings(files)
	sort.Strings(dirs)
	switch lib.collection {
	case "tvshows":
		if isRoot {
			// Series folders: every first-level folder of the library.
			if recursive {
				for _, d := range dirs {
					w.jfSeriesOf(d)
					w.jfResolveTree(lib, d, false, true)
				}
			}
			return
		}
		w.jfResolveEpisodes(lib, rel, files)
	default:
		w.jfResolveMovies(lib, rel, files, isRoot)
	}
	if recursive {
		for _, d := range dirs {
			w.jfResolveTree(lib, d, false, true)
		}
	}
}

// jfStacks groups a folder's files into entries (stacks of ≥ 2 parts, or single files).
func jfStacks(files []string) []*jfEntry {
	type key struct{ prefix, ext string }
	stacks := map[key][]string{}
	var order []key
	var singles []*jfEntry
	for _, f := range files {
		base := path.Base(f)
		ext := path.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if m := reJFStack.FindStringSubmatch(stem); m != nil && strings.TrimSpace(m[1]) != "" {
			k := key{strings.ToLower(strings.TrimSpace(m[1])), strings.ToLower(ext)}
			if _, ok := stacks[k]; !ok {
				order = append(order, k)
			}
			stacks[k] = append(stacks[k], f)
			continue
		}
		singles = append(singles, &jfEntry{name: stem, files: []string{f}, strm: strings.EqualFold(ext, ".strm")})
	}
	var out []*jfEntry
	for _, k := range order {
		fs := stacks[k]
		if len(fs) == 1 {
			base := path.Base(fs[0])
			out = append(out, &jfEntry{name: strings.TrimSuffix(base, path.Ext(base)), files: fs, strm: k.ext == ".strm"})
			continue
		}
		sort.Slice(fs, func(i, j int) bool { return jfPartNo(fs[i]) < jfPartNo(fs[j]) })
		stem := strings.TrimSuffix(path.Base(fs[0]), path.Ext(fs[0]))
		m := reJFStack.FindStringSubmatch(stem)
		out = append(out, &jfEntry{name: strings.TrimSpace(m[1]), files: fs, strm: k.ext == ".strm"})
	}
	out = append(out, singles...)
	sort.Slice(out, func(i, j int) bool { return out[i].files[0] < out[j].files[0] })
	return out
}

func jfPartNo(f string) int {
	stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
	if m := reJFStack.FindStringSubmatch(stem); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n
	}
	return 0
}

// jfResolution returns the resolution a name carries (0 = none).
func jfResolution(name string) int {
	if m := reJFResolution.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// jfOrder orders entries for primary selection: resolution names first (descending), then the rest
// alphabetically.
func jfOrder(entries []*jfEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := jfResolution(entries[i].name), jfResolution(entries[j].name)
		switch {
		case ri > 0 && rj > 0 && ri != rj:
			return ri > rj
		case (ri > 0) != (rj > 0):
			return ri > 0
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})
}

// jfYear returns the year a name carries (0 = none).
func jfYear(name string) int {
	if m := reJFYear.FindAllStringSubmatch(name, -1); len(m) > 0 {
		n, _ := strconv.Atoi(m[len(m)-1][1])
		return n
	}
	return 0
}

// jfGroupable reports whether a movie folder's entries are versions of one movie (§3.1 rules).
func jfGroupable(folder string, entries []*jfEntry) bool {
	if len([]rune(folder)) <= 1 {
		return false
	}
	year := -1
	for _, e := range entries {
		y := jfYear(e.name)
		switch {
		case year < 0:
			year = y
		case y != year:
			return false
		}
		if !strings.HasPrefix(strings.ToLower(e.name), strings.ToLower(folder)) {
			return false
		}
		rest := strings.TrimSpace(e.name[len(folder):])
		if rest != "" && !strings.ContainsAny(rest[:1], "-_.[") {
			return false
		}
	}
	return true
}

// jfVersionName is a version's label: what follows the folder name ("2160p"), else the name.
func jfVersionName(folder, name string) string {
	if strings.HasPrefix(strings.ToLower(name), strings.ToLower(folder)) {
		if rest := strings.Trim(name[len(folder):], " -_."); rest != "" {
			return rest
		}
	}
	return name
}

// jfCleanName removes id tags and the year from a folder or file name.
func jfCleanName(name string) string {
	n := reJFTags.ReplaceAllString(name, "")
	n = reJFYearParen.ReplaceAllString(n, "")
	n = strings.NewReplacer(".", " ", "_", " ").Replace(n)
	return strings.TrimSpace(strings.Join(strings.Fields(n), " "))
}

// jfTagIDs reads id tags ([tmdbid-603], {tmdb-603}, [tmdbid=603], [imdbid-tt…], [tvdbid-…]).
func jfTagIDs(name string) map[string]string {
	out := map[string]string{}
	for _, m := range reJFTag.FindAllStringSubmatch(name, -1) {
		key := strings.ToLower(m[1])
		switch {
		case strings.HasPrefix(key, "tmdb"):
			out["Tmdb"] = m[2]
		case strings.HasPrefix(key, "imdb"):
			out["Imdb"] = strings.ToLower(m[2])
		case strings.HasPrefix(key, "tvdb"):
			out["Tvdb"] = m[2]
		}
	}
	return out
}

// jfFileSize returns a file's size on disk (0 when unreadable).
func (w *world) jfFileSize(rel string) int64 {
	fi, err := os.Stat(w.local(rel))
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}

// jfNewVersion builds a version from an entry.
func (w *world) jfNewVersion(typ string, e *jfEntry, name string) *jfVersion {
	s := w.jellyfin
	v := &jfVersion{name: name, strm: e.strm}
	for i, f := range e.files {
		id := jfID(jfTypeVideo, s.remote(f))
		if i == 0 {
			id = jfID(typ, s.remote(f))
			v.id = id
		}
		v.parts = append(v.parts, jfPart{rel: f, id: id, size: w.jfFileSize(f)})
	}
	return v
}

// jfResolveMovies resolves the video files directly in a movie folder.
func (w *world) jfResolveMovies(lib *jfLibrary, rel string, files []string, isRoot bool) {
	s := w.jellyfin
	entries := jfStacks(files)
	if len(entries) == 0 {
		return
	}
	folder := path.Base(rel)
	var groups [][]*jfEntry
	if !isRoot && (len(entries) == 1 || jfGroupable(folder, entries)) {
		ordered := append([]*jfEntry(nil), entries...)
		jfOrder(ordered)
		// The file named like the folder is the primary; otherwise a stacked entry is preferred.
		primary := slices.IndexFunc(ordered, func(e *jfEntry) bool { return strings.EqualFold(e.name, folder) })
		if primary < 0 {
			primary = slices.IndexFunc(ordered, func(e *jfEntry) bool { return len(e.files) > 1 })
		}
		if primary > 0 {
			p := ordered[primary]
			ordered = append([]*jfEntry{p}, slices.Delete(ordered, primary, primary+1)...)
		}
		groups = [][]*jfEntry{ordered}
	} else {
		for _, e := range entries {
			groups = append(groups, []*jfEntry{e})
		}
	}
	mixed := isRoot || len(groups) > 1
	for _, g := range groups {
		it := &jfItem{typ: "Movie", lib: lib, folder: rel, mixed: mixed, season: -1}
		for _, e := range g {
			name := e.name
			if len(g) > 1 {
				name = jfVersionName(folder, e.name)
			}
			it.versions = append(it.versions, w.jfNewVersion(jfTypeMovie, e, name))
		}
		primary := g[0]
		source := primary.name
		if !mixed {
			source = folder
		}
		it.name, it.year = jfCleanName(source), jfYear(source)
		// Id tags come from the folder name, or the file name when the movie is in a mixed folder
		// (a stray release gets its own match, research §3.1 Eta).
		it.ids = jfTagIDs(primary.name)
		if !mixed {
			for k, v := range jfTagIDs(folder) {
				it.ids[k] = v
			}
		}
		if d, ok := s.fileIDs[primary.files[0]]; ok {
			// Online matching: what the scenario declares for the file.
			if len(it.ids) == 0 {
				if d.tmdb > 0 {
					it.ids["Tmdb"] = strconv.Itoa(d.tmdb)
				}
				if d.imdb != "" {
					it.ids["Imdb"] = d.imdb
				}
			}
			if it.year == 0 {
				it.year = d.year
			}
			if d.title != "" && (mixed || it.name == "") {
				it.name = d.title
			}
			it.added = d.added
		}
		if it.added.IsZero() {
			it.added = w.start.Add(-defaultAge)
		}
		s.items = append(s.items, it)
	}
}

// jfSeriesOf returns (and creates) the series of a series folder.
func (w *world) jfSeriesOf(folder string) *jfSeries {
	s := w.jellyfin
	if sr, ok := s.series[folder]; ok {
		return sr
	}
	base := path.Base(folder)
	sr := &jfSeries{id: jfID(jfTypeSeries, s.remote(folder)), folder: folder, name: jfCleanName(base), year: jfYear(base), ids: jfTagIDs(base)}
	if d, ok := s.showIDs[folder]; ok {
		if d.title != "" {
			sr.name = d.title
		}
		if len(sr.ids) == 0 {
			if d.tvdb > 0 {
				sr.ids["Tvdb"] = strconv.Itoa(d.tvdb)
			}
			if d.tmdb > 0 {
				sr.ids["Tmdb"] = strconv.Itoa(d.tmdb)
			}
			if d.imdb != "" {
				sr.ids["Imdb"] = d.imdb
			}
		}
	}
	s.series[folder] = sr
	return sr
}

// jfEpisodeOf parses a file name's season and episode range (ok false when it has none).
func jfEpisodeOf(name, folder string) (season, ep, end int, ok bool) {
	m := reJFEpisode.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, 0, false
	}
	season, _ = strconv.Atoi(m[1])
	ep, _ = strconv.Atoi(m[2])
	for _, g := range m[3:] {
		if g != "" {
			end, _ = strconv.Atoi(g)
			break
		}
	}
	if sm := reJFSeason.FindStringSubmatch(path.Base(folder)); sm != nil && m[1] == "" {
		season, _ = strconv.Atoi(sm[1])
	}
	return season, ep, end, true
}

// jfResolveEpisodes resolves the video files of one folder of a series.
func (w *world) jfResolveEpisodes(lib *jfLibrary, rel string, files []string) {
	s := w.jellyfin
	seriesFolder := rel
	for _, d := range lib.dirs {
		if strings.HasPrefix(rel, d+"/") {
			seriesFolder = path.Join(d, strings.SplitN(strings.TrimPrefix(rel, d+"/"), "/", 2)[0])
		}
	}
	sr := w.jfSeriesOf(seriesFolder)
	type key struct{ season, ep int }
	groups := map[key][]*jfEntry{}
	var order []key
	for _, e := range jfStacks(files) {
		// The key is the season and FIRST episode only: a multi-episode file next to a file of
		// its first episode becomes a hidden alternate of that episode (research §3.1).
		season, ep, _, ok := jfEpisodeOf(e.name, rel)
		if !ok {
			continue
		}
		k := key{season, ep}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	for _, k := range order {
		g := groups[k]
		jfOrder(g)
		it := &jfItem{typ: "Episode", lib: lib, folder: rel, mixed: true, season: k.season, episode: k.ep, series: sr, ids: map[string]string{}}
		for i, e := range g {
			name := e.name
			if i > 0 {
				name = strings.Trim(strings.TrimPrefix(e.name, g[0].name), " -_.")
				if name == "" {
					name = e.name
				}
			}
			v := w.jfNewVersion(jfTypeEpisode, e, name)
			_, v.epStart, v.epEnd, _ = jfEpisodeOf(e.name, rel)
			it.versions = append(it.versions, v)
		}
		primary := g[0].files[0]
		it.name = strings.TrimSuffix(path.Base(primary), path.Ext(primary))
		if t := s.epTitles[primary]; t != "" {
			it.name = t
		}
		if d, ok := s.fileIDs[primary]; ok {
			if d.tvdb > 0 {
				it.ids["Tvdb"] = strconv.Itoa(d.tvdb)
			}
			if d.tmdb > 0 {
				it.ids["Tmdb"] = strconv.Itoa(d.tmdb)
			}
			it.added = d.added
		}
		if it.added.IsZero() {
			it.added = w.start.Add(-defaultAge)
		}
		s.items = append(s.items, it)
	}
}

// jfApplyMerges links the merged movies (Merges): every copy lists every other as a Grouping
// source; a copy in the primary's library is hidden behind the primary, the copies of the other
// libraries are rows of their own (live on 12.1: a title merged from two copies of one library
// and a primary in another is listed there as two rows, each with the other two as Grouping).
func (w *world) jfApplyMerges() {
	s := w.jellyfin
	for _, it := range s.items {
		it.linked, it.hidden = nil, false
	}
	for _, m := range s.merges {
		var members []*jfItem
		for _, rel := range m {
			if it := w.jfItemByFile(rel); it != nil && !slices.Contains(members, it) {
				members = append(members, it)
			}
		}
		if len(members) < 2 {
			continue
		}
		primary := members[0]
		for _, a := range members {
			for _, b := range members {
				if a != b {
					a.linked = append(a.linked, b)
				}
			}
			if a != primary && a.lib == primary.lib {
				a.hidden = true
			}
		}
	}
}

// jfItemByFile returns the listed item whose primary's first file is rel.
func (w *world) jfItemByFile(rel string) *jfItem {
	for _, it := range w.jellyfin.items {
		if it.versions[0].parts[0].rel == rel {
			return it
		}
	}
	return nil
}

// jfRescan re-reads folders (the library monitor after a notification): the items resolved from
// them are replaced by what is on disk now; everything else stays as listed.
func (w *world) jfRescan(folders []string) {
	s := w.jellyfin
	done := map[string]bool{}
	for _, f := range folders {
		if done[f] {
			continue
		}
		done[f] = true
		kept := s.items[:0:0]
		for _, it := range s.items {
			if it.folder != f {
				kept = append(kept, it)
			}
		}
		s.items = kept
		for _, lib := range s.libs {
			for _, d := range lib.dirs {
				if f == d || strings.HasPrefix(f, d+"/") {
					w.jfResolveTree(lib, f, f == d, false)
				}
			}
		}
	}
	w.jfApplyMerges()
}

// jfApplyDue applies the notifications whose monitor delay has passed (force: all of them).
func (w *world) jfApplyDue(force bool) {
	s := w.jellyfin
	if s == nil || len(s.pending) == 0 {
		return
	}
	now := w.now()
	var rest []jfPending
	var folders []string
	for _, p := range s.pending {
		if force || !now.Before(p.due) {
			folders = append(folders, p.folders...)
			continue
		}
		rest = append(rest, p)
	}
	s.pending = rest
	if len(folders) > 0 {
		w.jfRescan(folders)
	}
}

// jfLibraryItems returns the listed items of a library (nil lib = every library), in listing order
// (sort name, then date created, then id).
func (s *jellyfinState) libraryItems(lib *jfLibrary, typ string) []*jfItem {
	var out []*jfItem
	for _, it := range s.items {
		if (lib == nil || it.lib == lib) && (typ == "" || it.typ == typ) && !it.hidden {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].name), strings.ToLower(out[j].name); a != b {
			return a < b
		}
		if !out[i].added.Equal(out[j].added) {
			return out[i].added.Before(out[j].added)
		}
		return out[i].id() < out[j].id()
	})
	return out
}

// findVersion returns the item and version whose id is id (a row's primary or an alternate).
func (s *jellyfinState) findVersion(id string) (*jfItem, *jfVersion) {
	for _, it := range s.items {
		for _, v := range it.versions {
			if v.id == id {
				return it, v
			}
		}
	}
	return nil, nil
}

// findPart returns the item, version and part whose part id is id.
func (s *jellyfinState) findPart(id string) (*jfItem, *jfVersion, int) {
	for _, it := range s.items {
		for _, v := range it.versions {
			for i, p := range v.parts {
				if p.id == id {
					return it, v, i
				}
			}
		}
	}
	return nil, nil, -1
}

// describe names an item in violation details.
func (it *jfItem) describe() string {
	return fmt.Sprintf("%s %q (%s)", it.typ, it.name, it.versions[0].parts[0].rel)
}
