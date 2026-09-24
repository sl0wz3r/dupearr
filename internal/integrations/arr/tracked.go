package arr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TrackedFiles returns every file the instance tracks (Radarr: from /movie embedded movieFile;
// Sonarr: /series filtered by TvdbIDs/ImdbIDs, then /episodefile + /episode per series).
//
// Radarr: GET /api/v3/movie lists the library. Without a filter the embedded movieFile is used
// (one request; its custom-format score is unknown). With a filter, GET /api/v3/moviefile?movieId=
// is fetched for every matching movie so CustomFormats/CustomFormatScore are accurate.
//
// Sonarr: GET /api/v3/series, then GET /api/v3/episodefile?seriesId= and
// GET /api/v3/episode?seriesId= per matching series (at most 4 series in flight). A file shared by
// several episodes (multi-episode file) is reported once with all of its EpisodeIDs/Episodes.
//
// Tag ids are resolved to labels with GET /api/v3/tag. Any failure fails the whole call: partial
// enrichment could hide a keep tag or tracked state from the decision engine.
func (c *Client) TrackedFiles(ctx context.Context, f TrackedFilter) ([]TrackedFile, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	fl := newIDFilter(f)
	if c.inst.Kind == models.ArrSonarr {
		return c.sonarrTrackedFiles(ctx, fl)
	}
	return c.radarrTrackedFiles(ctx, fl)
}

// FileByID re-reads one file right before acting on it (the "re-verify before acting" rule,
// docs/DECISIONS.md D6): the tracked file may have changed since the scan. It returns the file only
// while the *arr still tracks it (Radarr: it is the movie's movieFileId; Sonarr: at least one
// episode references it); otherwise the error wraps ErrNotFound.
func (c *Client) FileByID(ctx context.Context, fileID int64) (*TrackedFile, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	if fileID <= 0 {
		return nil, fmt.Errorf("%w: file id must be > 0 (got %d)", ErrInvalidArgument, fileID)
	}
	idStr := strconv.FormatInt(fileID, 10)
	var f fileResource
	if err := c.do(ctx, request{method: http.MethodGet, path: c.fileResourceName() + "/" + idStr}, &f); err != nil {
		return nil, err
	}
	if f.ID != fileID {
		return nil, fmt.Errorf("arr: %s %d: the response describes file %d", c.fileResourceName(), fileID, f.ID)
	}

	if c.inst.Kind == models.ArrSonarr {
		if f.SeriesID <= 0 {
			return nil, fmt.Errorf("arr: episodefile %d has no series id", fileID)
		}
		var s seriesResource
		if err := c.do(ctx, request{method: http.MethodGet, path: "series/" + strconv.FormatInt(f.SeriesID, 10)}, &s); err != nil {
			return nil, err
		}
		var all []episodeResource
		if err := c.do(ctx, request{method: http.MethodGet, path: "episode", query: url.Values{"episodeFileId": {idStr}}}, &all); err != nil {
			return nil, err
		}
		eps := make([]episodeResource, 0, len(all))
		for _, e := range all {
			if e.EpisodeFileID == fileID && (e.SeriesID == 0 || e.SeriesID == f.SeriesID) {
				eps = append(eps, e)
			}
		}
		if len(eps) == 0 {
			return nil, fmt.Errorf("%w: episodefile %d is not referenced by any episode (no longer tracked)", ErrNotFound, fileID)
		}
		tags, err := c.tagLabels(ctx, len(s.Tags) > 0)
		if err != nil {
			return nil, err
		}
		tf := c.sonarrTrackedFile(&s, &f, eps, tags)
		return &tf, nil
	}

	if f.MovieID <= 0 {
		return nil, fmt.Errorf("arr: moviefile %d has no movie id", fileID)
	}
	var m movieResource
	if err := c.do(ctx, request{method: http.MethodGet, path: "movie/" + strconv.FormatInt(f.MovieID, 10)}, &m); err != nil {
		return nil, err
	}
	if m.trackedFileID() != fileID {
		return nil, fmt.Errorf("%w: moviefile %d is no longer the file Radarr tracks for movie %d", ErrNotFound, fileID, m.ID)
	}
	tags, err := c.tagLabels(ctx, len(m.Tags) > 0)
	if err != nil {
		return nil, err
	}
	tf := c.radarrTrackedFile(&m, &f, tags)
	return &tf, nil
}

// File re-reads one file row with GET /api/v3/moviefile/{id} (Radarr) or /api/v3/episodefile/{id}
// (Sonarr) and reports what the id names right now: path, size and item.
//
// The executor calls it immediately before DeleteFile to confirm that the id still names the file
// it is about to remove: an instance whose URL was re-pointed at another *arr, a re-import or an
// upgrade can make the same id name an unrelated file. Unlike FileByID it does not check whether
// the *arr still tracks the row (DELETE removes the row either way) and costs one request.
//
// A 404 (the row no longer exists) wraps ErrNotFound. When the *arr omits the absolute path it is
// rebuilt from the item's folder (GET /api/v3/movie/{id} or /api/v3/series/{id}) and relativePath;
// a path that cannot be determined, a response describing another file or a file without an item
// is an error, never a guess.
func (c *Client) File(ctx context.Context, fileID int64) (*TrackedFileRef, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	if fileID <= 0 {
		return nil, fmt.Errorf("%w: file id must be > 0 (got %d)", ErrInvalidArgument, fileID)
	}
	res := c.fileResourceName()
	var f fileResource
	if err := c.do(ctx, request{method: http.MethodGet, path: res + "/" + strconv.FormatInt(fileID, 10)}, &f); err != nil {
		return nil, err
	}
	if f.ID != fileID {
		return nil, fmt.Errorf("arr: %s %d: the response describes file %d", res, fileID, f.ID)
	}
	itemID, itemRes, itemNoun := f.MovieID, "movie", "movie"
	if c.inst.Kind == models.ArrSonarr {
		itemID, itemRes, itemNoun = f.SeriesID, "series", "series"
	}
	if itemID <= 0 {
		return nil, fmt.Errorf("arr: %s %d has no %s id", res, fileID, itemNoun)
	}
	p := strings.TrimSpace(f.Path)
	if p == "" {
		var item struct {
			ID   int64  `json:"id"`
			Path string `json:"path"`
		}
		if err := c.do(ctx, request{method: http.MethodGet, path: itemRes + "/" + strconv.FormatInt(itemID, 10)}, &item); err != nil {
			return nil, err
		}
		if item.ID != itemID {
			return nil, fmt.Errorf("arr: %s %d: the response describes %s %d", itemRes, itemID, itemNoun, item.ID)
		}
		if p = filePath(&f, strings.TrimSpace(item.Path)); p == "" {
			return nil, fmt.Errorf("arr: %s %d: the path of the file is unknown", res, fileID)
		}
	}
	return &TrackedFileRef{Path: p, Size: f.Size, ItemID: itemID}, nil
}

// ---------------------------------------------------------------------------
// Radarr
// ---------------------------------------------------------------------------

func (c *Client) radarrTrackedFiles(ctx context.Context, fl idFilter) ([]TrackedFile, error) {
	var movies []movieResource
	listReq := request{method: http.MethodGet, path: "movie", query: url.Values{"excludeLocalCovers": {"true"}}, long: true}
	if err := c.do(ctx, listReq, &movies); err != nil {
		return nil, err
	}
	selected := make([]*movieResource, 0, len(movies))
	needTags := false
	for i := range movies {
		m := &movies[i]
		if m.ID > 0 && m.trackedFileID() > 0 && fl.movie(m.TmdbID, m.ImdbID) {
			selected = append(selected, m)
			needTags = needTags || len(m.Tags) > 0
		}
	}
	if len(selected) == 0 {
		return []TrackedFile{}, nil
	}

	files := make([]*fileResource, len(selected))
	var fetch []int
	for i, m := range selected {
		if !fl.active && m.MovieFile != nil && m.MovieFile.ID == m.trackedFileID() {
			files[i] = embeddedMovieFile(m.MovieFile)
			continue
		}
		fetch = append(fetch, i)
	}
	err := runLimited(ctx, maxConcurrency, len(fetch), func(ctx context.Context, k int) error {
		i := fetch[k]
		f, err := c.radarrMovieFile(ctx, selected[i])
		if err != nil {
			return err
		}
		files[i] = f
		return nil
	})
	if err != nil {
		return nil, err
	}

	tags, err := c.tagLabels(ctx, needTags)
	if err != nil {
		return nil, err
	}
	out := make([]TrackedFile, 0, len(selected))
	for i, m := range selected {
		if files[i] != nil {
			out = append(out, c.radarrTrackedFile(m, files[i], tags))
		}
	}
	return out, nil
}

// radarrMovieFile fetches the tracked file of m from GET /api/v3/moviefile?movieId= (accurate
// custom-format data). It returns (nil, nil) only when Radarr says the movie no longer exists
// (deleted since the movie list was read: its files are no longer tracked).
//
// When the tracked file is not among the rows — Radarr < 5.3.3 returns only the first row, which
// may be an orphan — it is read by id. If Radarr no longer has it, the movie changed during the
// scan and the call fails: silently dropping the movie would present a file Radarr tracks as
// untracked, and an untracked loser may be deleted behind Radarr's back (re-download loop).
func (c *Client) radarrMovieFile(ctx context.Context, m *movieResource) (*fileResource, error) {
	var rows []fileResource
	err := c.do(ctx, request{method: http.MethodGet, path: "moviefile", query: url.Values{"movieId": {strconv.FormatInt(m.ID, 10)}}}, &rows)
	if isArrNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want := m.trackedFileID()
	for i := range rows {
		if rows[i].ID == want {
			return &rows[i], nil
		}
	}
	var f fileResource
	err = c.do(ctx, request{method: http.MethodGet, path: "moviefile/" + strconv.FormatInt(want, 10)}, &f)
	if isArrNotFound(err) {
		return nil, fmt.Errorf("arr: movie %d changed during the scan (its tracked file %d no longer exists); scan again", m.ID, want)
	}
	if err != nil {
		return nil, err
	}
	if f.ID != want || (f.MovieID != 0 && f.MovieID != m.ID) {
		return nil, fmt.Errorf("arr: moviefile %d: the response describes file %d of movie %d, not movie %d", want, f.ID, f.MovieID, m.ID)
	}
	return &f, nil
}

// embeddedMovieFile returns a copy of a movie's embedded movieFile with its custom formats marked
// unknown: the list endpoint never computes them, and Radarr 5.0–5.22.3 serialises the score as a
// misleading 0 (research §2.1: never read the CF score from movie.movieFile).
func embeddedMovieFile(mf *fileResource) *fileResource {
	cp := *mf
	cp.CustomFormatScore = nil
	cp.CustomFormats = nil
	return &cp
}

func (c *Client) radarrTrackedFile(m *movieResource, f *fileResource, tags map[int64]string) TrackedFile {
	info := c.baseInfo()
	info.FileID = f.ID
	info.ItemID = m.ID
	info.ItemPath = m.Path
	info.Monitored = m.Monitored
	c.fillFileInfo(&info, f)
	info.Tags = labels(m.Tags, tags)
	return TrackedFile{
		Path:      filePath(f, m.Path),
		Size:      f.Size,
		Info:      info,
		TmdbID:    m.TmdbID,
		ImdbID:    strings.TrimSpace(m.ImdbID),
		MediaInfo: parseMediaInfo(f.MediaInfo),
	}
}

// ---------------------------------------------------------------------------
// Sonarr
// ---------------------------------------------------------------------------

func (c *Client) sonarrTrackedFiles(ctx context.Context, fl idFilter) ([]TrackedFile, error) {
	var series []seriesResource
	if err := c.do(ctx, request{method: http.MethodGet, path: "series", long: true}, &series); err != nil {
		return nil, err
	}
	selected := make([]*seriesResource, 0, len(series))
	needTags := false
	for i := range series {
		s := &series[i]
		if s.ID > 0 && s.mayHaveFiles() && fl.series(s.TvdbID, s.ImdbID) {
			selected = append(selected, s)
			needTags = needTags || len(s.Tags) > 0
		}
	}
	if len(selected) == 0 {
		return []TrackedFile{}, nil
	}
	tags, err := c.tagLabels(ctx, needTags)
	if err != nil {
		return nil, err
	}

	results := make([][]TrackedFile, len(selected))
	err = runLimited(ctx, maxConcurrency, len(selected), func(ctx context.Context, i int) error {
		files, err := c.sonarrSeriesFiles(ctx, selected[i], tags)
		if err != nil {
			return err
		}
		results[i] = files
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]TrackedFile, 0, len(selected))
	for _, r := range results {
		out = append(out, r...)
	}
	return out, nil
}

// sonarrSeriesFiles maps one series' episode files to the episodes referencing them. Files no
// episode references (orphan rows) are not tracked and are skipped. A series Sonarr says was
// deleted since the series list was read (its own 404) yields no files; a 404 that did not come
// from Sonarr fails the call. A file an episode references but the file list missed (imported
// between the two requests) is read by id, so a tracked file is never reported as untracked.
func (c *Client) sonarrSeriesFiles(ctx context.Context, s *seriesResource, tags map[int64]string) ([]TrackedFile, error) {
	q := url.Values{"seriesId": {strconv.FormatInt(s.ID, 10)}}
	var files []fileResource
	err := c.do(ctx, request{method: http.MethodGet, path: "episodefile", query: q, long: true}, &files)
	if isArrNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var episodes []episodeResource
	err = c.do(ctx, request{method: http.MethodGet, path: "episode", query: q, long: true}, &episodes)
	if isArrNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	byFile := make(map[int64][]episodeResource, len(files))
	for _, e := range episodes {
		if e.EpisodeFileID > 0 && (e.SeriesID == 0 || e.SeriesID == s.ID) {
			byFile[e.EpisodeFileID] = append(byFile[e.EpisodeFileID], e)
		}
	}
	listed := make(map[int64]bool, len(files))
	for i := range files {
		listed[files[i].ID] = true
	}
	missing := make([]int64, 0)
	for id := range byFile {
		if !listed[id] {
			missing = append(missing, id)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	for _, id := range missing {
		var f fileResource
		err := c.do(ctx, request{method: http.MethodGet, path: "episodefile/" + strconv.FormatInt(id, 10)}, &f)
		if isArrNotFound(err) {
			continue // deleted since the episodes were read: nothing to report
		}
		if err != nil {
			return nil, err
		}
		if f.ID != id || (f.SeriesID != 0 && f.SeriesID != s.ID) {
			return nil, fmt.Errorf("arr: episodefile %d: the response describes file %d of series %d, not series %d", id, f.ID, f.SeriesID, s.ID)
		}
		files = append(files, f)
	}

	out := make([]TrackedFile, 0, len(files))
	for i := range files {
		f := &files[i]
		if eps := byFile[f.ID]; f.ID > 0 && len(eps) > 0 {
			out = append(out, c.sonarrTrackedFile(s, f, eps, tags))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if fa, fb := firstInt(a.Episodes), firstInt(b.Episodes); fa != fb {
			return fa < fb
		}
		return a.Info.FileID < b.Info.FileID
	})
	return out, nil
}

func (c *Client) sonarrTrackedFile(s *seriesResource, f *fileResource, eps []episodeResource, tags map[int64]string) TrackedFile {
	sorted := append([]episodeResource(nil), eps...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		if a.EpisodeNumber != b.EpisodeNumber {
			return a.EpisodeNumber < b.EpisodeNumber
		}
		return a.ID < b.ID
	})
	info := c.baseInfo()
	info.FileID = f.ID
	info.ItemID = s.ID
	info.ItemPath = s.Path
	numbers := make([]int, 0, len(sorted))
	seenNum := make(map[int]bool, len(sorted))
	seenID := make(map[int64]bool, len(sorted))
	anyMonitored := false
	for _, e := range sorted {
		if e.ID > 0 && !seenID[e.ID] {
			seenID[e.ID] = true
			info.EpisodeIDs = append(info.EpisodeIDs, e.ID)
		}
		if !seenNum[e.EpisodeNumber] {
			seenNum[e.EpisodeNumber] = true
			numbers = append(numbers, e.EpisodeNumber)
		}
		anyMonitored = anyMonitored || e.Monitored
	}
	// Sonarr searches an episode only when both the series and the episode are monitored.
	info.Monitored = s.Monitored && anyMonitored
	c.fillFileInfo(&info, f)
	info.Tags = labels(s.Tags, tags)

	season := f.SeasonNumber
	if season == 0 && len(sorted) > 0 && sorted[0].SeasonNumber > 0 {
		season = sorted[0].SeasonNumber
	}
	sort.Ints(numbers)
	return TrackedFile{
		Path:      filePath(f, s.Path),
		Size:      f.Size,
		Info:      info,
		TvdbID:    s.TvdbID,
		ImdbID:    strings.TrimSpace(s.ImdbID),
		Season:    season,
		Episodes:  numbers,
		MediaInfo: parseMediaInfo(f.MediaInfo),
	}
}

// ---------------------------------------------------------------------------
// Shared mapping helpers
// ---------------------------------------------------------------------------

// baseInfo returns an ArrFileInfo with the instance identity and non-nil slices (the JSON API
// emits [] rather than null).
func (c *Client) baseInfo() models.ArrFileInfo {
	return models.ArrFileInfo{
		InstanceID:    c.inst.ID,
		InstanceName:  c.inst.Name,
		Kind:          c.inst.Kind,
		EpisodeIDs:    []int64{},
		CustomFormats: []string{},
		Languages:     []string{},
		Tags:          []string{},
	}
}

// fillFileInfo copies the file-level fields shared by Radarr and Sonarr.
func (c *Client) fillFileInfo(info *models.ArrFileInfo, f *fileResource) {
	if f.Quality != nil {
		q := f.Quality.Quality
		info.QualityName = strings.TrimSpace(q.Name)
		info.QualitySource = strings.TrimSpace(q.Source)
		info.QualityResolution = q.Resolution
		info.QualityModifier = qualityModifier(c.inst.Kind, q.Source, q.Modifier)
	}
	info.CustomFormats = names(f.CustomFormats)
	if f.CustomFormatScore != nil {
		score := *f.CustomFormatScore
		info.CustomFormatScore = &score
	}
	info.ReleaseGroup = strings.TrimSpace(f.ReleaseGroup)
	info.Edition = strings.TrimSpace(f.Edition)
	info.SceneName = strings.TrimSpace(f.SceneName)
	info.Languages = names(f.Languages)
	if f.MediaInfo != nil {
		info.DynamicRangeType = strings.TrimSpace(f.MediaInfo.VideoDynamicRangeType)
	}
	info.QualityCutoffNotMet = f.QualityCutoffNotMet
	info.DateAdded = f.DateAdded.Time
}

// qualityModifier normalizes the quality modifier: Radarr's modifier with "none" as "";
// Sonarr has no modifier, so its raw sources map to Radarr's names (blurayRaw ⇒ "remux",
// televisionRaw ⇒ "rawhd").
func qualityModifier(kind models.ArrKind, source, modifier string) string {
	if kind == models.ArrSonarr {
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "blurayraw":
			return "remux"
		case "televisionraw":
			return "rawhd"
		}
		return ""
	}
	m := strings.ToLower(strings.TrimSpace(modifier))
	if m == "none" {
		return ""
	}
	return m
}

// filePath returns the file's absolute path as the *arr sees it; when the *arr omitted it, it is
// rebuilt from the item folder and the relative path using the folder's separator style.
func filePath(f *fileResource, itemPath string) string {
	if p := strings.TrimSpace(f.Path); p != "" {
		return p
	}
	rel := strings.TrimLeft(strings.TrimSpace(f.RelativePath), `/\`)
	if rel == "" || itemPath == "" {
		return ""
	}
	sep := "/"
	if strings.Contains(itemPath, `\`) && !strings.Contains(itemPath, "/") {
		sep = `\`
	}
	return strings.TrimRight(itemPath, `/\`) + sep + rel
}

// names returns the non-empty names of refs (never nil).
func names(refs []namedRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if n := strings.TrimSpace(r.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// tagLabels fetches GET /api/v3/tag as id → label when needed.
func (c *Client) tagLabels(ctx context.Context, needed bool) (map[int64]string, error) {
	if !needed {
		return map[int64]string{}, nil
	}
	var tags []tagResource
	if err := c.do(ctx, request{method: http.MethodGet, path: "tag"}, &tags); err != nil {
		return nil, err
	}
	byID := make(map[int64]string, len(tags))
	for _, t := range tags {
		if l := strings.TrimSpace(t.Label); t.ID > 0 && l != "" {
			byID[t.ID] = l
		}
	}
	return byID, nil
}

// labels resolves tag ids to labels (unknown ids are skipped; never nil).
func labels(ids []int64, byID map[int64]string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if l, ok := byID[id]; ok && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// parseMediaInfo converts the REST media info (nil-safe).
func parseMediaInfo(mi *mediaInfoResource) *MediaInfo {
	if mi == nil {
		return nil
	}
	w, h := parseResolution(mi.Resolution)
	return &MediaInfo{
		Width:                 w,
		Height:                h,
		VideoCodec:            strings.TrimSpace(mi.VideoCodec),
		VideoBitDepth:         mi.VideoBitDepth,
		VideoBitrate:          mi.VideoBitrate,
		VideoDynamicRange:     strings.TrimSpace(mi.VideoDynamicRange),
		VideoDynamicRangeType: strings.TrimSpace(mi.VideoDynamicRangeType),
		AudioCodec:            strings.TrimSpace(mi.AudioCodec),
		AudioChannels:         mi.AudioChannels,
		AudioLanguages:        splitSlash(mi.AudioLanguages),
		Subtitles:             splitSlash(mi.Subtitles),
		RunTime:               strings.TrimSpace(mi.RunTime),
	}
}

// parseResolution parses "WIDTHxHEIGHT" (e.g. "3840x1600"); invalid input yields 0, 0.
func parseResolution(s string) (width, height int) {
	s = strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexByte(s, 'x')
	if i <= 0 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
	h, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

// splitSlash splits the *arr's "/"-joined language lists ("eng/eng/fre"), keeping one entry per
// stream (never nil).
func splitSlash(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, "/") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

// ---------------------------------------------------------------------------
// Filter
// ---------------------------------------------------------------------------

// idFilter is a normalized TrackedFilter.
type idFilter struct {
	active bool
	tmdb   map[int]bool
	tvdb   map[int]bool
	imdb   map[string]bool // lower-cased keys
}

func newIDFilter(f TrackedFilter) idFilter {
	fl := idFilter{
		active: f.TmdbIDs != nil || f.ImdbIDs != nil || f.TvdbIDs != nil,
		tmdb:   f.TmdbIDs,
		tvdb:   f.TvdbIDs,
	}
	if len(f.ImdbIDs) > 0 {
		fl.imdb = make(map[string]bool, len(f.ImdbIDs))
		for k, v := range f.ImdbIDs {
			if k = imdbKey(k); v && k != "" {
				fl.imdb[k] = true
			}
		}
	}
	return fl
}

func imdbKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// movie reports whether a Radarr movie passes the filter.
func (fl idFilter) movie(tmdbID int, imdbID string) bool {
	if !fl.active {
		return true
	}
	if tmdbID > 0 && fl.tmdb[tmdbID] {
		return true
	}
	k := imdbKey(imdbID)
	return k != "" && fl.imdb[k]
}

// series reports whether a Sonarr series passes the filter.
func (fl idFilter) series(tvdbID int, imdbID string) bool {
	if !fl.active {
		return true
	}
	if tvdbID > 0 && fl.tvdb[tvdbID] {
		return true
	}
	k := imdbKey(imdbID)
	return k != "" && fl.imdb[k]
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// runLimited calls fn(ctx, i) for i in [0, n) with at most limit calls in flight. The first error
// cancels the remaining calls and is returned; a cancelled parent context returns its error.
func runLimited(ctx context.Context, limit, n int, fn func(ctx context.Context, i int) error) error {
	if n <= 0 {
		return nil
	}
	if limit <= 0 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	sem := make(chan struct{}, limit)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
launch:
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				fail(err)
			}
		}(i)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
