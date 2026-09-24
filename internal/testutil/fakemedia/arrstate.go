package fakemedia

import (
	"fmt"
	"io/fs"
	"math"
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

// arrState is one fake Radarr or Sonarr instance.
type arrState struct {
	name, kind, instanceName, version, apiKey, urlBase string

	startingUp    bool
	recycleBin    string // remote path; "" = permanent deletes
	cleanupDays   int
	autoUnmonitor bool

	tags        []arrTag
	rootFolders []string // media-root relative

	movies     []*arrMovie
	series     []*arrSeries
	episodes   []*arrEpisode
	files      map[int64]*arrFile
	exclusions []*arrExclusion
	queue      []*arrQueueItem
	commands   []*arrCommand

	nextMovieID, nextSeriesID, nextEpisodeID, nextFileID   int64
	nextExclusionID, nextQueueID, nextCommandID, nextTagID int64
}

type arrTag struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

type arrMovie struct {
	id               int64
	title            string
	year             int
	tmdbID           int
	imdbID           string
	folder           string // media-root relative
	monitored        bool
	tags             []int64
	qualityProfileID int
	added            time.Time
	fileID           int64 // the tracked movie file (0 = none)
	runtime          int   // minutes
}

type arrSeries struct {
	id               int64
	title            string
	year             int
	tvdbID, tmdbID   int
	imdbID           string
	folder           string
	monitored        bool
	tags             []int64
	qualityProfileID int
	added            time.Time
}

type arrEpisode struct {
	id, seriesID   int64
	season, number int
	title          string
	tvdbID         int
	fileID         int64
	monitored      bool
	airDate        time.Time
	runtime        int
}

type arrFile struct {
	id, itemID    int64 // file id, movie id (Radarr) or series id (Sonarr)
	rel           string
	size          int64
	dateAdded     time.Time
	quality       qualityDef
	languages     []string // language names
	releaseGroup  string
	edition       string
	sceneName     string
	customFormats []string
	cfScore       int
	mediaInfo     *arrMediaInfo
	season        int
	releaseType   string
	cutoffNotMet  bool
}

type arrExclusion struct {
	id     int64
	tmdbID int
	tvdbID int
	title  string
	year   int
}

type arrQueueItem struct {
	id                           int64
	movieID, seriesID, episodeID int64
	season                       int
	title, status, state         string
	size, sizeLeft               int64
	protocol, client, indexer    string
	downloadID                   string
	added                        time.Time
	quality                      qualityDef
}

type arrCommand struct {
	id                      int64
	name                    string
	body                    map[string]any
	status, result, message string
	queued, started, ended  time.Time
}

func (a *arrState) isRadarr() bool { return a.kind == KindRadarr }

// Radarr versions whose wire format changed (docs/research/arr-api.md §2.1/§2.2, verified per tag).
const (
	// radarrNullableCFScore: customFormatScore became int? in v5.22.4.9896. Before it, the movieFile
	// embedded in GET /movie carried a misleading "customFormatScore": 0 instead of omitting it.
	radarrNullableCFScore = "5.22.4.9896"
	// radarrAllMovieFiles: from v5.3.3.8535 GET /moviefile?movieId= returns every row of the movie;
	// before, movieId was a single int? and only the first file was returned.
	radarrAllMovieFiles = "5.3.3.8535"
)

// versionBefore reports whether the dotted numeric version v is lower than ref ("6.4.4.10685" <
// "6.10"). Non-numeric suffixes ("-ls123") are ignored; an unparsable version counts as current.
func versionBefore(v, ref string) bool {
	parse := func(s string) ([]int, bool) {
		if i := strings.IndexAny(s, "-+ "); i >= 0 {
			s = s[:i]
		}
		var out []int
		for _, f := range strings.Split(s, ".") {
			n, err := strconv.Atoi(f)
			if err != nil || n < 0 {
				return nil, false
			}
			out = append(out, n)
		}
		return out, len(out) > 0
	}
	a, ok1 := parse(v)
	b, ok2 := parse(ref)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// appName is the system/status appName ("Radarr" / "Sonarr").
func (a *arrState) appName() string {
	if a.isRadarr() {
		return "Radarr"
	}
	return "Sonarr"
}

// remotePath returns the *arr's view of a media-root relative path.
func (a *arrState) remotePath(rel string) string { return remote(rel) }

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

func (w *world) buildArrs(sc *Scenario) error {
	for _, in := range sc.Instances {
		a := &arrState{
			name: in.Name, kind: in.Kind, instanceName: in.InstanceName, version: in.Version,
			apiKey: in.APIKey, urlBase: in.URLBase, recycleBin: in.RecycleBin,
			cleanupDays: in.RecycleBinCleanupDays, autoUnmonitor: in.AutoUnmonitor,
			rootFolders: append([]string(nil), in.RootFolders...),
			files:       map[int64]*arrFile{},
			nextMovieID: 1, nextSeriesID: 1, nextEpisodeID: 1001, nextFileID: 101,
			nextExclusionID: 1, nextQueueID: 1, nextCommandID: 1001, nextTagID: 1,
		}
		if a.kind == KindSonarr {
			a.nextFileID = 501
		}
		if a.instanceName == "" {
			a.instanceName = a.appName()
		}
		if a.version == "" {
			a.version = DefaultRadarrVersion
			if a.kind == KindSonarr {
				a.version = DefaultSonarrVersion
			}
		}
		for _, t := range in.Tags {
			a.tags = append(a.tags, arrTag{ID: a.nextTagID, Label: t})
			a.nextTagID++
		}
		w.arrs[in.Name] = a
		w.arrOrder = append(w.arrOrder, in.Name)
	}
	for i := range sc.Movies {
		m := &sc.Movies[i]
		if err := w.buildArrMovies(m, w.moviePlans[m]); err != nil {
			return err
		}
	}
	for i := range sc.Shows {
		w.buildArrSeries(&sc.Shows[i])
	}
	return nil
}

func versionAdded(start time.Time, v *Version) time.Time {
	age := v.Age
	if age <= 0 {
		age = defaultAge
	}
	return start.Add(-age)
}

// buildArrMovies creates the *arr movies of a scenario movie and the files they track (versions
// and discs).
func (w *world) buildArrMovies(m *Movie, discs []*discPlan) error {
	// The movie folder and runtime when nothing more specific is known: the first version's, else
	// the first disc's.
	// The Plex versions of the movie's listed loose clips count like its declared versions.
	versions := append(slices.Clone(m.Versions), w.movieClips[m]...)
	defaultFolder, defaultRuntime := "", int64(0)
	switch {
	case len(m.Versions) > 0:
		defaultFolder, defaultRuntime = path.Dir(m.Versions[0].Parts[0].File), m.Versions[0].DurationMs
	case len(discs) > 0:
		defaultFolder, defaultRuntime = discs[0].folder, discs[0].d.DurationMs
	case len(m.ClipSets) > 0:
		defaultFolder = m.ClipSets[0].Folder
		if i := mainClipIndex(m.ClipSets[0].Clips); i >= 0 {
			defaultRuntime = m.ClipSets[0].Clips[i].DurationMs
		}
	}
	created := map[*arrMovie]bool{}
	ensure := func(inst string, tmdb int, imdb, title string, year int) *arrMovie {
		a := w.arrs[inst]
		for _, mv := range a.movies {
			if mv.tmdbID == tmdb {
				return mv
			}
		}
		mv := &arrMovie{
			id: a.nextMovieID, title: title, year: year, tmdbID: tmdb, imdbID: imdb, monitored: true,
			qualityProfileID: 1, runtime: int(defaultRuntime / 60_000),
		}
		if mv.runtime == 0 {
			mv.runtime = 120
		}
		a.nextMovieID++
		a.movies = append(a.movies, mv)
		created[mv] = true
		return mv
	}
	earliest := time.Time{}
	for i := range versions {
		t := versionAdded(w.start, &versions[i])
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	for _, p := range discs {
		t := versionAdded(w.start, &Version{Age: p.d.Age})
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	for _, am := range m.Arr {
		tmdb, imdb := am.TmdbID, am.ImdbID
		if tmdb == 0 {
			tmdb, imdb = m.TmdbID, m.ImdbID
		}
		title, year := am.Title, am.Year
		if title == "" {
			title = m.Title
		}
		if year == 0 && am.TmdbID == 0 {
			year = m.Year
		}
		mv := ensure(am.Instance, tmdb, imdb, title, year)
		if am.Folder != "" {
			mv.folder = am.Folder
		}
		mv.monitored = !am.Unmonitored
		mv.tags = w.arrs[am.Instance].tagIDs(am.Tags)
	}
	for i := range versions {
		v := &versions[i]
		if v.Tracked == "" {
			continue
		}
		tmdb := v.TrackedTmdbID
		if tmdb == 0 {
			tmdb = m.TmdbID
		}
		a := w.arrs[v.Tracked]
		mv := ensure(v.Tracked, tmdb, m.ImdbID, m.Title, m.Year)
		rel := v.Parts[0].File
		if mv.folder == "" {
			mv.folder = path.Dir(rel)
		}
		if !under(rel, mv.folder) {
			return fmt.Errorf("fakemedia: movie %q: tracked file %q is outside the %s movie folder %q", m.Title, rel, v.Tracked, mv.folder)
		}
		dur := v.DurationMs
		if dur <= 0 {
			dur = Mins(120)
		}
		f := w.newArrFile(a, mv.id, v, rel, v.Parts[0].Size, dur, versionAdded(w.start, v))
		mv.fileID = f.id
	}
	// A disc file tracked after a manual in-place import (docs/research/disc-structures.md §3.3 C):
	// the relative path stays inside the disc; size and mediaInfo describe that one file.
	for _, p := range discs {
		if p.d.Tracked == "" {
			continue
		}
		a := w.arrs[p.d.Tracked]
		mv := ensure(p.d.Tracked, m.TmdbID, m.ImdbID, m.Title, m.Year)
		if mv.folder == "" {
			mv.folder = p.folder
		}
		if !under(p.trackedRel, mv.folder) {
			return fmt.Errorf("fakemedia: movie %q: tracked disc file %q is outside the %s movie folder %q", m.Title, p.trackedRel, p.d.Tracked, mv.folder)
		}
		v, probed := p.trackedVersion(a.kind)
		f := w.newArrFile(a, mv.id, v, p.trackedRel, p.trackedSize, p.trackedDurMs, versionAdded(w.start, &Version{Age: p.d.Age}))
		if !probed {
			f.mediaInfo = nil // images and VOBs are never probed (VideoFileInfoReader)
		}
		mv.fileID = f.id
	}
	for mv := range created {
		if mv.folder == "" {
			mv.folder = defaultFolder
		}
		mv.added = earliest
	}
	return nil
}

func (w *world) buildArrSeries(sh *Show) {
	var instances []string
	seen := map[string]bool{}
	addInst := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			instances = append(instances, n)
		}
	}
	for _, a := range sh.Arr {
		addInst(a.Instance)
	}
	for _, e := range sh.Episodes {
		for _, v := range e.Versions {
			addInst(v.Tracked)
		}
	}
	eps := append([]Episode(nil), sh.Episodes...)
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Episode < eps[j].Episode
	})
	for _, inst := range instances {
		a := w.arrs[inst]
		s := &arrSeries{
			id: a.nextSeriesID, title: sh.Title, year: sh.Year, tvdbID: sh.TvdbID, tmdbID: sh.TmdbID,
			imdbID: sh.ImdbID, folder: sh.Folder, monitored: true, qualityProfileID: 1,
			added: w.start.Add(-defaultAge),
		}
		a.nextSeriesID++
		for _, as := range sh.Arr {
			if as.Instance == inst {
				s.monitored = !as.Unmonitored
				s.tags = a.tagIDs(as.Tags)
			}
		}
		a.series = append(a.series, s)
		byFile := map[string]*arrFile{}
		for i := range eps {
			e := &eps[i]
			ae := &arrEpisode{
				id: a.nextEpisodeID, seriesID: s.id, season: e.Season, number: e.Episode, title: e.Title,
				tvdbID: e.TvdbID, monitored: !e.Unmonitored, runtime: 45,
				airDate: time.Date(max(sh.Year, 1970), 1, 1, 2, 0, 0, 0, time.UTC).AddDate(0, 0, 7*(e.Episode-1)+365*max(e.Season-1, 0)),
			}
			if ae.title == "" {
				ae.title = fmt.Sprintf("Episode %d", e.Episode)
			}
			a.nextEpisodeID++
			a.episodes = append(a.episodes, ae)
			for j := range e.Versions {
				v := &e.Versions[j]
				if v.Tracked != inst {
					continue
				}
				added := versionAdded(w.start, v)
				if added.Before(s.added) {
					s.added = added
				}
				rel := v.Parts[0].File
				f := byFile[rel]
				if f == nil {
					dur := v.DurationMs
					if dur <= 0 {
						dur = Mins(45)
					}
					f = w.newArrFile(a, s.id, v, rel, v.Parts[0].Size, dur, added)
					f.season = e.Season
					f.releaseType = "singleEpisode"
					byFile[rel] = f
				} else {
					f.releaseType = "multiEpisode"
				}
				ae.fileID = f.id
			}
		}
	}
}

// newArrFile registers a tracked file derived from a scenario version (v may be nil for files
// the fake knows nothing about).
func (w *world) newArrFile(a *arrState, itemID int64, v *Version, rel string, size, durationMs int64, added time.Time) *arrFile {
	f := &arrFile{
		id: a.nextFileID, itemID: itemID, rel: rel, size: size, dateAdded: added.UTC().Truncate(time.Second),
		languages: []string{"English"},
	}
	a.nextFileID++
	width := 0
	if v != nil {
		width = v.Video.Width
		f.releaseGroup, f.edition, f.sceneName = v.ReleaseGroup, v.Edition, v.SceneName
		f.customFormats = append([]string(nil), v.CustomFormats...)
		f.cfScore = v.CustomFormatScore
		f.cutoffNotMet = v.QualityCutoffNotMet
		if langs := audioLanguageNames(v); len(langs) > 0 {
			f.languages = langs
		}
		f.mediaInfo = mediaInfoFor(v, path.Base(rel), durationMs, size)
	}
	if v != nil && v.Quality != "" {
		if q, ok := qualityByName(a.kind, v.Quality); ok {
			f.quality = q
		} else {
			f.quality = parseQuality(a.kind, path.Base(rel), width)
		}
	} else {
		f.quality = parseQuality(a.kind, path.Base(rel), width)
	}
	if f.releaseGroup == "" {
		f.releaseGroup = parseReleaseGroup(path.Base(rel))
	}
	a.files[f.id] = f
	return f
}

func audioLanguageNames(v *Version) []string {
	var out []string
	seen := map[string]bool{}
	for _, au := range v.Audio {
		n := lookupLanguage(au.LanguageCode).Name
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

var reReleaseGroup = regexp.MustCompile(`-([A-Za-z0-9]{2,})$`)

func parseReleaseGroup(name string) string {
	base := strings.TrimSuffix(name, path.Ext(name))
	if m := reReleaseGroup.FindStringSubmatch(base); m != nil {
		switch strings.ToLower(m[1]) {
		case "sample", "cd1", "cd2", "dl", "rip", "hd", "sbs", "ou", "trailer", "4k":
			return ""
		}
		if strings.HasSuffix(strings.ToLower(m[1]), "p") && strings.Trim(strings.ToLower(m[1]), "0123456789p") == "" {
			return ""
		}
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Lookups
// ---------------------------------------------------------------------------

func (a *arrState) movieByID(id int64) *arrMovie {
	for _, m := range a.movies {
		if m.id == id {
			return m
		}
	}
	return nil
}

func (a *arrState) seriesByID(id int64) *arrSeries {
	for _, s := range a.series {
		if s.id == id {
			return s
		}
	}
	return nil
}

func (a *arrState) episodeByID(id int64) *arrEpisode {
	for _, e := range a.episodes {
		if e.id == id {
			return e
		}
	}
	return nil
}

func (a *arrState) episodesOf(seriesID int64) []*arrEpisode {
	var out []*arrEpisode
	for _, e := range a.episodes {
		if e.seriesID == seriesID {
			out = append(out, e)
		}
	}
	return out
}

// filesOf returns the file rows of a movie/series, ordered by id.
func (a *arrState) filesOf(itemID int64) []*arrFile {
	var out []*arrFile
	for _, f := range a.files {
		if f.itemID == itemID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (a *arrState) tagIDs(labels []string) []int64 {
	ids := []int64{}
	for _, l := range labels {
		for _, t := range a.tags {
			if strings.EqualFold(t.Label, l) {
				ids = append(ids, t.ID)
			}
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

// deleteFile implements DELETE /api/v3/moviefile/{id} and /episodefile/{id}: the file goes to the
// recycle bin (or is removed permanently), the row is always deleted, and the movie/episodes lose
// their file link (unmonitored when auto-unmonitor is on). It returns the HTTP status and, for
// errors, the message.
func (w *world) arrDeleteFile(a *arrState, id int64) (int, string) {
	f := a.files[id]
	noun := "EpisodeFile"
	if a.isRadarr() {
		noun = "MovieFile"
	}
	if f == nil {
		return 404, fmt.Sprintf("%s with ID %d does not exist", noun, id)
	}
	var itemFolder string
	var mv *arrMovie
	var sr *arrSeries
	if a.isRadarr() {
		mv = a.movieByID(f.itemID)
		if mv == nil {
			return 404, fmt.Sprintf("Movie with ID %d does not exist", f.itemID)
		}
		itemFolder = mv.folder
	} else {
		sr = a.seriesByID(f.itemID)
		if sr == nil {
			return 404, fmt.Sprintf("Series with ID %d does not exist", f.itemID)
		}
		itemFolder = sr.folder
	}
	// Failsafe of MediaFileDeletionService: the parent of the movie/series folder must exist and
	// contain at least one directory (otherwise the share is probably not mounted).
	rootRel := path.Dir(itemFolder)
	exists, nonEmpty := hasSubdirs(w.local(rootRel))
	what, plural := "Series'", "series"
	if a.isRadarr() {
		what, plural = "Movie's", "movies"
	}
	if !exists {
		return 409, fmt.Sprintf("%s root folder (%s) doesn't exist.", what, a.remotePath(rootRel))
	}
	if !nonEmpty {
		return 409, fmt.Sprintf("%s root folder (%s) is empty. Rescan will not update %s as a failsafe.", what, a.remotePath(rootRel), plural)
	}
	lp := w.local(f.rel)
	if fi, err := os.Stat(w.local(itemFolder)); err == nil && fi.IsDir() {
		if _, err := os.Lstat(lp); err == nil {
			if a.recycleBin != "" {
				sub, err := filepath.Rel(filepath.Dir(w.local(itemFolder)), filepath.Dir(lp))
				if err != nil {
					sub = filepath.Base(filepath.Dir(lp))
				}
				if _, err := w.recycle(a, lp, sub); err != nil {
					return 500, fmt.Sprintf("Unable to delete %s file", strings.ToLower(strings.TrimSuffix(noun, "File")))
				}
			} else if err := os.Remove(lp); err != nil {
				return 500, fmt.Sprintf("Unable to delete %s file", strings.ToLower(strings.TrimSuffix(noun, "File")))
			}
		}
	}
	delete(a.files, id)
	if mv != nil && mv.fileID == id {
		mv.fileID = 0
		if a.autoUnmonitor {
			mv.monitored = false
		}
	}
	if sr != nil {
		for _, e := range a.episodesOf(sr.id) {
			if e.fileID == id {
				e.fileID = 0
				if a.autoUnmonitor {
					e.monitored = false
				}
			}
		}
	}
	return 200, ""
}

// recycle moves a file into the instance's recycle bin (<bin>/<sub>/<name>, "_N" suffix on
// collisions) and sets its modification time to now, like RecycleBinProvider.
func (w *world) recycle(a *arrState, lp, sub string) (string, error) {
	binLocal, ok := w.remoteToLocal(a.recycleBin)
	if !ok {
		return "", fmt.Errorf("recycle bin %s is outside %s", a.recycleBin, RemoteRoot)
	}
	destDir := filepath.Join(binLocal, sub)
	if destDir != binLocal && !strings.HasPrefix(destDir, binLocal+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid recycle bin subfolder %q", sub)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(lp)
	dest := filepath.Join(destDir, base)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dest); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(base)
		dest = filepath.Join(destDir, strings.TrimSuffix(base, ext)+"_"+strconv.Itoa(i)+ext)
	}
	if err := os.Rename(lp, dest); err != nil {
		return "", err
	}
	now := w.now()
	_ = os.Chtimes(dest, now, now)
	return dest, nil
}

// rescanMovie emulates RescanMovie (DiskScanService.Scan): rows of vanished files are removed
// (MissingFromDisk), then — when the movie has no tracked file — the LARGEST untracked video file
// in the movie folder whose path parses (a year in the file or folder name) is adopted in place,
// like the real import engine (ImportApprovedMovie orders candidates by size). Simplification: a
// movie that still has a tracked file adopts nothing (the real UpgradeSpecification could accept
// an equal-or-better file).
func (w *world) rescanMovie(a *arrState, m *arrMovie) string {
	if fi, err := os.Stat(w.local(m.folder)); err != nil || !fi.IsDir() {
		exists, nonEmpty := hasSubdirs(w.local(path.Dir(m.folder)))
		if !exists || !nonEmpty {
			return "Movie's root folder is missing or empty, skipping scan"
		}
		for _, f := range a.filesOf(m.id) {
			delete(a.files, f.id)
		}
		if m.fileID != 0 {
			m.fileID = 0
			if a.autoUnmonitor {
				m.monitored = false
			}
		}
		return "Movie folder does not exist"
	}
	tracked := map[string]bool{}
	for _, f := range a.filesOf(m.id) {
		if !w.fileExists(f.rel) {
			delete(a.files, f.id)
			if m.fileID == f.id {
				m.fileID = 0
				if a.autoUnmonitor {
					m.monitored = false
				}
			}
			continue
		}
		tracked[f.rel] = true
	}
	if m.fileID != 0 {
		return "Completed"
	}
	var cands []candidate
	for _, c := range w.untrackedVideos(m.folder, tracked) {
		// Files whose name and folder carry no year are rejected ("Unable to parse file"): disc
		// clips such as BDMV/STREAM/00800.m2ts are never adopted.
		if radarrParses(c.rel) {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return "Completed (no untracked video files)"
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].size != cands[j].size {
			return cands[i].size > cands[j].size
		}
		return cands[i].rel < cands[j].rel
	})
	c := cands[0]
	f := w.newArrFile(a, m.id, w.fileSpecs[c.rel], c.rel, c.size, specDuration(w.fileSpecs[c.rel], Mins(120)), w.now())
	m.fileID = f.id
	return "Imported " + path.Base(c.rel)
}

func specDuration(v *Version, def int64) int64 {
	if v == nil || v.DurationMs <= 0 {
		return def
	}
	return v.DurationMs
}

// rescanSeries emulates RescanSeries: vanished files are unlinked, then untracked video files that
// parse to SxxEyy are adopted (lowest episode first, then largest size) for episodes that have no
// file. A tracked (multi-episode) file is never replaced.
func (w *world) rescanSeries(a *arrState, s *arrSeries) string {
	eps := a.episodesOf(s.id)
	detached := map[*arrEpisode]bool{}
	unlink := func(f *arrFile) {
		delete(a.files, f.id)
		for _, e := range eps {
			if e.fileID == f.id {
				e.fileID = 0
				detached[e] = true
			}
		}
	}
	defer func() {
		if !a.autoUnmonitor {
			return
		}
		for e := range detached {
			if e.fileID == 0 {
				e.monitored = false
			}
		}
	}()
	if fi, err := os.Stat(w.local(s.folder)); err != nil || !fi.IsDir() {
		exists, nonEmpty := hasSubdirs(w.local(path.Dir(s.folder)))
		if !exists || !nonEmpty {
			detached = nil
			return "Series' root folder is missing or empty, skipping scan"
		}
		for _, f := range a.filesOf(s.id) {
			unlink(f)
		}
		return "Series folder does not exist"
	}
	tracked := map[string]bool{}
	for _, f := range a.filesOf(s.id) {
		if !w.fileExists(f.rel) {
			unlink(f)
			continue
		}
		tracked[f.rel] = true
	}
	type parsed struct {
		candidate
		season int
		eps    []int
	}
	var cands []parsed
	for _, c := range w.untrackedVideos(s.folder, tracked) {
		season, nums, ok := parseEpisodeNumbers(path.Base(c.rel))
		if !ok {
			continue
		}
		cands = append(cands, parsed{candidate: c, season: season, eps: nums})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].season != cands[j].season {
			return cands[i].season < cands[j].season
		}
		if cands[i].eps[0] != cands[j].eps[0] {
			return cands[i].eps[0] < cands[j].eps[0]
		}
		if cands[i].size != cands[j].size {
			return cands[i].size > cands[j].size
		}
		return cands[i].rel < cands[j].rel
	})
	imported := 0
	for _, c := range cands {
		var targets []*arrEpisode
		busy := false
		for _, n := range c.eps {
			for _, e := range eps {
				if e.season == c.season && e.number == n {
					targets = append(targets, e)
					if e.fileID != 0 {
						busy = true
					}
				}
			}
		}
		if len(targets) == 0 || busy {
			continue
		}
		f := w.newArrFile(a, s.id, w.fileSpecs[c.rel], c.rel, c.size, specDuration(w.fileSpecs[c.rel], Mins(45)), w.now())
		f.season = c.season
		f.releaseType = "singleEpisode"
		if len(targets) > 1 {
			f.releaseType = "multiEpisode"
		}
		for _, e := range targets {
			e.fileID = f.id
		}
		imported++
	}
	return fmt.Sprintf("Completed (%d file(s) imported)", imported)
}

type candidate struct {
	rel  string
	size int64
}

// Disk-scan exclusions (DiskScanService / MediaFileExtensions, docs/research/arr-api.md §4.2).
var (
	reExcludedDir  = regexp.MustCompile(`(?i)^(?:@eadir|\.@__thumb|plex versions|\..*|extras|extrafanart|behind the scenes|deleted scenes|featurettes|interviews|other|scenes|samples?|shorts|trailers)$`)
	reExcludedFile = regexp.MustCompile(`(?i)(?:-trailer|-other|-behindthescenes|-deleted|-featurette|-interview|-scene|-short)$`)
	reMultiPart    = regexp.MustCompile(`(?i)[ ._-](?:cd|part|pt|disc|disk|dvd)[ ._-]?\d+$`)
	videoExts      = map[string]bool{}
)

func init() {
	for _, e := range strings.Fields(".webm .m4v .3gp .nsv .ty .strm .rm .rmvb .m3u .ifo .mov .qt .divx .xvid .bivx .nrg .pva .wmv .asf .asx .ogm .ogv .m2v .avi .bin .dat .dvr-ms .mpg .mpeg .mp4 .avc .vp3 .svq3 .nuv .viv .dv .fli .flv .wpl .img .iso .vob .mkv .mk3d .ts .wtv .m2ts") {
		videoExts[e] = true
	}
}

func isVideoFile(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// untrackedVideos lists video files under folder (media-root relative) that a disk scan would
// consider, excluding tracked paths and multi-part (cd1/cd2) sets.
func (w *world) untrackedVideos(folder string, tracked map[string]bool) []candidate {
	root := w.local(folder)
	var out []candidate
	multi := map[string][]int{} // local dir → indexes in out
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && reExcludedDir.MatchString(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !isVideoFile(name) || strings.HasPrefix(name, "._") {
			return nil
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if reExcludedFile.MatchString(base) {
			return nil
		}
		relLocal, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel := folder + "/" + filepath.ToSlash(relLocal)
		if tracked[rel] {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, candidate{rel: rel, size: fi.Size()})
		if reMultiPart.MatchString(base) {
			dir := filepath.Dir(p)
			multi[dir] = append(multi[dir], len(out)-1)
		}
		return nil
	})
	drop := map[int]bool{}
	for _, idx := range multi {
		if len(idx) > 1 {
			for _, i := range idx {
				drop[i] = true
			}
		}
	}
	if len(drop) == 0 {
		return out
	}
	kept := out[:0]
	for i, c := range out {
		if !drop[i] {
			kept = append(kept, c)
		}
	}
	return kept
}

var (
	reSeasonEp = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])S(\d{1,3})((?:[ ._-]*E\d{1,4})+)`)
	reEpNum    = regexp.MustCompile(`(?i)([ ._-]*)E(\d{1,4})`)
)

// parseEpisodeNumbers parses "S01E01", "S01E01E02" and "S01E01-E03" (a range) from a file name.
func parseEpisodeNumbers(name string) (season int, eps []int, ok bool) {
	m := reSeasonEp.FindStringSubmatch(name)
	if m == nil {
		return 0, nil, false
	}
	season, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, nil, false
	}
	prev := -1
	for _, em := range reEpNum.FindAllStringSubmatch(m[2], -1) {
		n, err := strconv.Atoi(em[2])
		if err != nil {
			return 0, nil, false
		}
		if prev >= 0 && strings.Contains(em[1], "-") && n > prev+1 {
			for k := prev + 1; k < n; k++ {
				eps = append(eps, k)
			}
		}
		if n > prev {
			eps = append(eps, n)
			prev = n
		}
	}
	return season, eps, len(eps) > 0
}

// ---------------------------------------------------------------------------
// Qualities
// ---------------------------------------------------------------------------

// qualityDef is an *arr quality (QualityDefinition). Modifier is Radarr-only.
type qualityDef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
	Modifier   string `json:"modifier,omitempty"`
}

// Radarr quality ids (Quality.cs). Names are the lookup keys.
var radarrQualities = []qualityDef{
	{0, "Unknown", "unknown", 0, "none"},
	{1, "SDTV", "tv", 480, "none"},
	{2, "DVD", "dvd", 0, "none"}, // Radarr's DVD quality has no resolution (Quality.cs)
	{23, "DVD-R", "dvd", 480, "remux"},
	{4, "HDTV-720p", "tv", 720, "none"},
	{9, "HDTV-1080p", "tv", 1080, "none"},
	{16, "HDTV-2160p", "tv", 2160, "none"},
	{8, "WEBDL-480p", "webdl", 480, "none"},
	{5, "WEBDL-720p", "webdl", 720, "none"},
	{3, "WEBDL-1080p", "webdl", 1080, "none"},
	{18, "WEBDL-2160p", "webdl", 2160, "none"},
	{12, "WEBRip-480p", "webrip", 480, "none"},
	{14, "WEBRip-720p", "webrip", 720, "none"},
	{15, "WEBRip-1080p", "webrip", 1080, "none"},
	{17, "WEBRip-2160p", "webrip", 2160, "none"},
	{20, "Bluray-480p", "bluray", 480, "none"},
	{21, "Bluray-576p", "bluray", 576, "none"},
	{6, "Bluray-720p", "bluray", 720, "none"},
	{7, "Bluray-1080p", "bluray", 1080, "none"},
	{19, "Bluray-2160p", "bluray", 2160, "none"},
	{30, "Remux-1080p", "bluray", 1080, "remux"},
	{31, "Remux-2160p", "bluray", 2160, "remux"},
	{22, "BR-DISK", "bluray", 1080, "brdisk"},
	{10, "Raw-HD", "tv", 1080, "rawhd"},
}

// Sonarr quality ids (Quality.cs) — they differ from Radarr's; Sonarr has no modifier.
var sonarrQualities = []qualityDef{
	{0, "Unknown", "unknown", 0, ""},
	{1, "SDTV", "television", 480, ""},
	{2, "DVD", "dvd", 480, ""},
	{4, "HDTV-720p", "television", 720, ""},
	{9, "HDTV-1080p", "television", 1080, ""},
	{16, "HDTV-2160p", "television", 2160, ""},
	{10, "Raw-HD", "televisionRaw", 1080, ""},
	{8, "WEBDL-480p", "web", 480, ""},
	{5, "WEBDL-720p", "web", 720, ""},
	{3, "WEBDL-1080p", "web", 1080, ""},
	{18, "WEBDL-2160p", "web", 2160, ""},
	{12, "WEBRip-480p", "webRip", 480, ""},
	{14, "WEBRip-720p", "webRip", 720, ""},
	{15, "WEBRip-1080p", "webRip", 1080, ""},
	{17, "WEBRip-2160p", "webRip", 2160, ""},
	{13, "Bluray-480p", "bluray", 480, ""},
	{22, "Bluray-576p", "bluray", 576, ""},
	{6, "Bluray-720p", "bluray", 720, ""},
	{7, "Bluray-1080p", "bluray", 1080, ""},
	{19, "Bluray-2160p", "bluray", 2160, ""},
	{20, "Bluray-1080p Remux", "blurayRaw", 1080, ""},
	{21, "Bluray-2160p Remux", "blurayRaw", 2160, ""},
}

// sonarrAliases maps Radarr-style names accepted in scenarios to Sonarr names.
var sonarrAliases = map[string]string{
	"remux-1080p": "Bluray-1080p Remux",
	"remux-2160p": "Bluray-2160p Remux",
}

func qualityByName(kind, name string) (qualityDef, bool) {
	table := radarrQualities
	if kind == KindSonarr {
		table = sonarrQualities
		if alias, ok := sonarrAliases[strings.ToLower(name)]; ok {
			name = alias
		}
	}
	for _, q := range table {
		if strings.EqualFold(q.Name, name) {
			return q, true
		}
	}
	return qualityDef{}, false
}

var (
	reRes2160 = regexp.MustCompile(`(?i)(?:2160p|\b4k\b|\buhd\b)`)
	reRes1080 = regexp.MustCompile(`(?i)1080[pi]`)
	reRes720  = regexp.MustCompile(`(?i)720p`)
	reRes576  = regexp.MustCompile(`(?i)576p`)
	reRes480  = regexp.MustCompile(`(?i)480p`)
	reWeb     = regexp.MustCompile(`(?i)(?:^|[^a-z])web(?:[^a-z]|$)`)
)

// parseQuality derives the *arr quality from file-name tokens (like the *arr QualityParser),
// falling back to the video width for the resolution.
func parseQuality(kind, name string, width int) qualityDef {
	n := strings.ToLower(name)
	has := func(tokens ...string) bool {
		for _, t := range tokens {
			if strings.Contains(n, t) {
				return true
			}
		}
		return false
	}
	src, byExtension := "", false
	switch {
	case has("remux"):
		src = "remux"
	case has("bluray", "blu-ray", "bdrip", "brrip"):
		src = "bluray"
	case has("web-dl", "webdl", "web.dl", "web dl"):
		src = "webdl"
	case has("webrip", "web-rip", "web.rip"):
		src = "webrip"
	case has("hdtv", "pdtv"):
		src = "hdtv"
	case has("dvdrip", "dvd"):
		src = "dvd"
	case reWeb.MatchString(n):
		src = "webdl"
	default:
		// No source in the name: the extension decides for disc formats (MediaFileExtensions:
		// .iso/.img/.vob → DVD, .m2ts → Bluray-720p).
		src, byExtension = discExtensionQuality(n)
	}
	res := 0
	switch {
	case reRes2160.MatchString(n):
		res = 2160
	case reRes1080.MatchString(n):
		res = 1080
	case reRes720.MatchString(n):
		res = 720
	case reRes576.MatchString(n):
		res = 576
	case reRes480.MatchString(n):
		res = 480
	case width >= 3200:
		res = 2160
	case width >= 1700:
		res = 1080
	case width >= 1100:
		res = 720
	case width > 0:
		res = 480
	}
	qn := "Unknown"
	switch src {
	case "remux":
		switch {
		case res == 2160:
			qn = "Remux-2160p"
		case res >= 1080 || res == 0:
			qn = "Remux-1080p"
		default:
			qn = fmt.Sprintf("Bluray-%dp", res)
		}
	case "bluray":
		switch {
		case res == 0 && byExtension:
			res = 720
		case res == 0:
			res = 1080
		}
		qn = fmt.Sprintf("Bluray-%dp", res)
	case "webdl", "webrip":
		if res == 0 {
			res = 1080
		}
		if res == 576 {
			res = 480
		}
		prefix := "WEBDL"
		if src == "webrip" {
			prefix = "WEBRip"
		}
		qn = fmt.Sprintf("%s-%dp", prefix, res)
	case "dvd":
		qn = "DVD"
	default: // hdtv or unknown source with a resolution
		switch {
		case res == 0 && src == "":
			qn = "Unknown"
		case res <= 576:
			qn = "SDTV"
		default:
			qn = fmt.Sprintf("HDTV-%dp", res)
		}
	}
	if q, ok := qualityByName(kind, qn); ok {
		return q
	}
	q, _ := qualityByName(kind, "Unknown")
	return q
}

// ---------------------------------------------------------------------------
// mediaInfo
// ---------------------------------------------------------------------------

// arrMediaInfo is MediaInfoResource (shared by Radarr and Sonarr): slash-joined language strings,
// "WxH" resolution, "H:MM:SS" run time.
type arrMediaInfo struct {
	AudioBitrate          int64   `json:"audioBitrate"`
	AudioChannels         float64 `json:"audioChannels"`
	AudioCodec            string  `json:"audioCodec"`
	AudioLanguages        string  `json:"audioLanguages"`
	AudioStreamCount      int     `json:"audioStreamCount"`
	VideoBitDepth         int     `json:"videoBitDepth"`
	VideoBitrate          int64   `json:"videoBitrate"`
	VideoCodec            string  `json:"videoCodec"`
	VideoFps              float64 `json:"videoFps"`
	VideoDynamicRange     string  `json:"videoDynamicRange"`
	VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
	Resolution            string  `json:"resolution"`
	RunTime               string  `json:"runTime"`
	ScanType              string  `json:"scanType"`
	Subtitles             string  `json:"subtitles"`
}

// mediaInfoFor derives the *arr mediaInfo of a version (nil when the file was never analyzed).
func mediaInfoFor(v *Version, fileName string, durationMs, size int64) *arrMediaInfo {
	if v == nil || v.Unanalyzed || v.Video.Width == 0 {
		return nil
	}
	mi := &arrMediaInfo{
		VideoBitDepth: v.Video.BitDepth,
		VideoCodec:    arrVideoCodec(v.Video.Codec, fileName),
		VideoFps:      math.Round(v.Video.FrameRate*1000) / 1000,
		Resolution:    fmt.Sprintf("%dx%d", v.Video.Width, v.Video.Height),
		RunTime:       formatRunTime(durationMs),
		ScanType:      "Progressive",
	}
	mi.VideoDynamicRange, mi.VideoDynamicRangeType = arrDynamicRange(v.Video)
	var langs, subs []string
	for i, au := range v.Audio {
		langs = append(langs, strings.ToLower(au.LanguageCode))
		if i == 0 {
			mi.AudioCodec = arrAudioCodec(au)
			mi.AudioChannels = arrChannels(au.Channels)
			mi.AudioBitrate = int64(au.BitrateKbps) * 1000
		}
	}
	mi.AudioStreamCount = len(v.Audio)
	mi.AudioLanguages = strings.Join(langs, "/")
	for _, s := range v.Subtitles {
		subs = append(subs, strings.ToLower(s.LanguageCode))
	}
	mi.Subtitles = strings.Join(subs, "/")
	vb := v.Video.BitrateKbps
	if vb == 0 && durationMs > 0 {
		vb = int(float64(size*8/durationMs) * 0.85)
	}
	mi.VideoBitrate = int64(vb) * 1000
	return mi
}

func arrVideoCodec(codec, fileName string) string {
	n := strings.ToLower(fileName)
	switch codec {
	case "hevc", "h265":
		if strings.Contains(n, "x265") {
			return "x265"
		}
		return "h265"
	case "h264":
		if strings.Contains(n, "x264") {
			return "x264"
		}
		return "h264"
	case "av1":
		return "AV1"
	case "vc1":
		return "VC1"
	case "mpeg2video":
		return "MPEG2"
	case "mpeg4":
		if strings.Contains(n, "divx") {
			return "DivX"
		}
		return "XviD"
	default:
		return strings.ToUpper(codec)
	}
}

func arrAudioCodec(a Audio) string {
	p := strings.ToLower(a.Profile)
	switch a.Codec {
	case "truehd":
		if a.Atmos {
			return "TrueHD Atmos"
		}
		return "TrueHD"
	case "eac3":
		if a.Atmos {
			return "EAC3 Atmos"
		}
		return "EAC3"
	case "dca", "dts":
		switch {
		case strings.Contains(p, "x") && !strings.Contains(p, "express"):
			return "DTS-X"
		case strings.Contains(p, "ma"):
			return "DTS-HD MA"
		case strings.Contains(p, "hra"):
			return "DTS-HD HRA"
		}
		return "DTS"
	case "ac3":
		return "AC3"
	case "aac":
		return "AAC"
	case "flac":
		return "FLAC"
	case "pcm":
		return "PCM"
	case "opus":
		return "Opus"
	case "mp3":
		return "MP3"
	}
	return strings.ToUpper(a.Codec)
}

func arrChannels(ch int) float64 {
	switch ch {
	case 8:
		return 7.1
	case 7:
		return 6.1
	case 6:
		return 5.1
	default:
		return float64(ch)
	}
}

// arrDynamicRange returns videoDynamicRange ("HDR"/"") and videoDynamicRangeType.
func arrDynamicRange(v Video) (string, string) {
	switch {
	case v.DOVIProfile > 0 && v.ColorTrc == "arib-std-b67":
		return "HDR", "DV HLG"
	case v.DOVIProfile > 0 && (v.DOVIBLCompatID == 1 || v.DOVIBLCompatID == 6 || v.ColorTrc == "smpte2084"):
		if v.HDR10Plus {
			return "HDR", "DV HDR10Plus"
		}
		return "HDR", "DV HDR10"
	case v.DOVIProfile > 0:
		return "HDR", "DV"
	case v.HDR10Plus:
		return "HDR", "HDR10Plus"
	case v.ColorTrc == "smpte2084":
		return "HDR", "HDR10"
	case v.ColorTrc == "arib-std-b67":
		return "HDR", "HLG"
	}
	return "", ""
}

func formatRunTime(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
