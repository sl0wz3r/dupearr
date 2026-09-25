package fakemedia

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// arrAPI serves one fake Radarr or Sonarr instance.
type arrAPI struct {
	e *Env
	w *world
	a *arrState
}

func (e *Env) arrHandler(a *arrState) http.Handler {
	h := &arrAPI{e: e, w: e.w, a: a}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", h.ping)
	mux.HandleFunc("HEAD /ping", h.ping)
	mux.HandleFunc("GET /api/v3/system/status", h.systemStatus)
	mux.HandleFunc("GET /api/v3/tag", h.tags)
	mux.HandleFunc("POST /api/v3/tag", h.createTag)
	mux.HandleFunc("GET /api/v3/rootfolder", h.rootFolders)
	mux.HandleFunc("GET /api/v3/config/mediamanagement", h.mediaManagement)
	mux.HandleFunc("PUT /api/v3/config/mediamanagement", h.putMediaManagement)
	mux.HandleFunc("PUT /api/v3/config/mediamanagement/{id}", h.putMediaManagement)
	mux.HandleFunc("GET /api/v3/queue", h.queue)
	mux.HandleFunc("POST /api/v3/command", h.postCommand)
	mux.HandleFunc("GET /api/v3/command", h.listCommands)
	mux.HandleFunc("GET /api/v3/command/{id}", h.getCommand)
	if a.isRadarr() {
		mux.HandleFunc("GET /api/v3/movie", h.movies)
		mux.HandleFunc("GET /api/v3/movie/{id}", h.movie)
		mux.HandleFunc("PUT /api/v3/movie/{id}", h.putMovie)
		mux.HandleFunc("DELETE /api/v3/movie/{id}", h.deleteItem)
		mux.HandleFunc("PUT /api/v3/movie/editor", h.movieEditor)
		mux.HandleFunc("GET /api/v3/moviefile", h.fileList)
		mux.HandleFunc("GET /api/v3/moviefile/{id}", h.fileOne)
		mux.HandleFunc("DELETE /api/v3/moviefile/{id}", h.deleteFile)
		mux.HandleFunc("DELETE /api/v3/moviefile/bulk", h.bulkDelete)
		mux.HandleFunc("DELETE /api/v3/moviefile", h.malformedDelete)
		mux.HandleFunc("DELETE /api/v3/moviefile/", h.malformedDelete)
		mux.HandleFunc("GET /api/v3/exclusions", h.exclusions)
		mux.HandleFunc("GET /api/v3/exclusions/paged", h.exclusionsPaged)
		mux.HandleFunc("POST /api/v3/exclusions", h.addExclusion)
		mux.HandleFunc("POST /api/v3/exclusions/bulk", h.addExclusionsBulk)
		mux.HandleFunc("DELETE /api/v3/exclusions/{id}", h.deleteExclusion)
	} else {
		mux.HandleFunc("GET /api/v3/series", h.seriesList)
		mux.HandleFunc("GET /api/v3/series/{id}", h.seriesOne)
		mux.HandleFunc("DELETE /api/v3/series/{id}", h.deleteItem)
		mux.HandleFunc("GET /api/v3/episode", h.episodes)
		mux.HandleFunc("GET /api/v3/episode/{id}", h.episodeOne)
		mux.HandleFunc("PUT /api/v3/episode/monitor", h.episodeMonitor)
		mux.HandleFunc("PUT /api/v3/episode/{id}", h.putEpisode)
		mux.HandleFunc("GET /api/v3/episodefile", h.fileList)
		mux.HandleFunc("GET /api/v3/episodefile/{id}", h.fileOne)
		mux.HandleFunc("DELETE /api/v3/episodefile/{id}", h.deleteFile)
		mux.HandleFunc("DELETE /api/v3/episodefile/bulk", h.bulkDelete)
		mux.HandleFunc("DELETE /api/v3/episodefile", h.malformedDelete)
		mux.HandleFunc("DELETE /api/v3/episodefile/", h.malformedDelete)
		mux.HandleFunc("GET /api/v3/importlistexclusion", h.exclusions)
		mux.HandleFunc("GET /api/v3/importlistexclusion/paged", h.exclusionsPaged)
		mux.HandleFunc("POST /api/v3/importlistexclusion", h.addExclusion)
		mux.HandleFunc("DELETE /api/v3/importlistexclusion/{id}", h.deleteExclusion)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	return h.middleware(mux)
}

// middleware emulates the Servarr pipeline: StartingUpMiddleware (503), API key authentication
// (?apikey= wins over X-Api-Key, then Authorization: Bearer), UrlBaseMiddleware (307 when the URL
// base is missing) and case-insensitive routing.
func (h *arrAPI) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.w.mu.Lock()
		starting := h.a.startingUp
		key := h.a.apiKey
		base := h.a.urlBase
		h.w.mu.Unlock()

		p := r.URL.Path
		hasBase := base == "" || strings.EqualFold(p, base) || strings.HasPrefix(strings.ToLower(p), strings.ToLower(base)+"/")
		rel := p
		if base != "" && hasBase {
			rel = p[len(base):]
			if rel == "" {
				rel = "/"
			}
		}
		lower := strings.ToLower(rel)
		if starting {
			msg := h.a.appName() + " is starting up, please try again later"
			if strings.HasPrefix(lower, "/api") {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"errorMessage": msg})
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(msg))
			}
			return
		}
		if lower != "/ping" {
			got, viaQuery := apiKeyFrom(r)
			if viaQuery {
				h.w.mu.Lock()
				h.w.violate(h.a.name, r, RuleArrAPIKeyQuery, "the ?apikey= query parameter overrides the X-Api-Key header (docs/DECISIONS.md D3)")
				h.w.mu.Unlock()
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if !hasBase {
			loc := base + p
			if r.URL.RawQuery != "" {
				loc += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if r.Method == http.MethodDelete && path.Clean(lower) != lower {
			// Go's ServeMux would redirect to the cleaned path and clients re-send the DELETE there
			// (e.g. /moviefile//5 → /moviefile/5): record it and refuse instead.
			h.w.mu.Lock()
			h.w.violate(h.a.name, r, RuleArrMalformedDelete, "non-canonical DELETE path (empty or dot segment, or trailing slash)")
			h.w.mu.Unlock()
			arrErr(w, http.StatusNotFound, "Not Found")
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		u := *r.URL
		u.Path, u.RawPath = lower, ""
		r2.URL = &u
		next.ServeHTTP(w, r2)
	})
}

// apiKeyFrom returns the API key the way ApiKeyAuthenticationHandler finds it.
func apiKeyFrom(r *http.Request) (key string, viaQuery bool) {
	for k, v := range r.URL.Query() {
		if strings.EqualFold(k, "apikey") {
			if len(v) > 0 {
				return v[0], true
			}
			return "", true
		}
	}
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k, false
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer "), false
	}
	return "", false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func arrErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg, "description": msg})
}

type validationFailure struct {
	PropertyName   string `json:"propertyName"`
	ErrorMessage   string `json:"errorMessage"`
	AttemptedValue any    `json:"attemptedValue,omitempty"`
	Severity       string `json:"severity"`
	ErrorCode      string `json:"errorCode,omitempty"`
}

func arrValidation(w http.ResponseWriter, fails ...validationFailure) {
	for i := range fails {
		if fails[i].Severity == "" {
			fails[i].Severity = "error"
		}
	}
	writeJSON(w, http.StatusBadRequest, fails)
}

func arrTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// lowerQuery returns the query with lower-cased keys (ASP.NET binds case-insensitively).
func lowerQuery(r *http.Request) url.Values {
	out := url.Values{}
	for k, v := range r.URL.Query() {
		out[strings.ToLower(k)] = append(out[strings.ToLower(k)], v...)
	}
	return out
}

// decodeLowerKeys decodes a JSON object into a map with lower-cased keys.
func decodeLowerKeys(r *http.Request) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		out[strings.ToLower(k)] = v
	}
	return out, nil
}

func parseID(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

func (h *arrAPI) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
}

func (h *arrAPI) systemStatus(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	start := h.w.start
	writeJSON(w, http.StatusOK, map[string]any{
		"appName": a.appName(), "instanceName": a.instanceName, "version": a.version,
		"buildTime": arrTime(start.Add(-30 * 24 * time.Hour)), "isDebug": false, "isProduction": true,
		"isAdmin": false, "isUserInteractive": false, "startupPath": "/app/" + strings.ToLower(a.appName()) + "/bin",
		"appData": "/config", "osName": "ubuntu", "osVersion": "22.04", "isNetCore": true, "isLinux": true,
		"isOsx": false, "isWindows": false, "isDocker": true, "isContainerized": true, "mode": "console",
		"branch": "master", "databaseType": "sqLite", "databaseVersion": "3.45.1", "authentication": "forms",
		"migrationVersion": 245, "urlBase": a.urlBase, "runtimeVersion": "8.0.8", "runtimeName": ".NET",
		"startTime": arrTime(start), "packageVersion": a.version + "-fake", "packageAuthor": "fakemedia",
		"packageUpdateMechanism": "docker",
	})
}

func (h *arrAPI) tags(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := append([]arrTag{}, h.a.tags...)
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) createTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		arrValidation(w, validationFailure{PropertyName: "Label", ErrorMessage: "'Label' must not be empty."})
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	for _, t := range h.a.tags {
		if strings.EqualFold(t.Label, label) {
			writeJSON(w, http.StatusCreated, t)
			return
		}
	}
	t := arrTag{ID: h.a.nextTagID, Label: strings.ToLower(label)}
	h.a.nextTagID++
	h.a.tags = append(h.a.tags, t)
	writeJSON(w, http.StatusCreated, t)
}

func (h *arrAPI) rootFolders(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	owned := map[string]bool{}
	for _, m := range h.a.movies {
		owned[m.folder] = true
	}
	for _, s := range h.a.series {
		owned[s.folder] = true
	}
	out := []map[string]any{}
	for i, rf := range h.a.rootFolders {
		fi, err := os.Stat(h.w.local(rf))
		accessible := err == nil && fi.IsDir()
		unmapped := []map[string]string{}
		if entries, err := os.ReadDir(h.w.local(rf)); err == nil {
			for _, e := range entries {
				rel := rf + "/" + e.Name()
				if e.IsDir() && !owned[rel] && !strings.HasPrefix(e.Name(), ".") {
					unmapped = append(unmapped, map[string]string{"name": e.Name(), "path": remote(rel), "relativePath": e.Name()})
				}
			}
		}
		out = append(out, map[string]any{
			"id": i + 1, "path": remote(rf), "accessible": accessible, "freeSpace": int64(5) << 40,
			"unmappedFolders": unmapped,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) mediaManagementJSON() map[string]any {
	a := h.a
	j := map[string]any{
		"id": 1, "recycleBin": a.recycleBin, "recycleBinCleanupDays": a.cleanupDays,
		"downloadPropersAndRepacks": "preferAndUpgrade", "deleteEmptyFolders": false, "fileDate": "none",
		"rescanAfterRefresh": "always", "setPermissionsLinux": false, "chmodFolder": "755", "chownGroup": "",
		"skipFreeSpaceCheckWhenImporting": false, "minimumFreeSpaceWhenImporting": 100,
		"copyUsingHardlinks": true, "useScriptImport": false, "scriptImportPath": "", "importExtraFiles": false,
		"extraFileExtensions": "srt", "enableMediaInfo": true,
	}
	if a.isRadarr() {
		j["autoUnmonitorPreviouslyDownloadedMovies"] = a.autoUnmonitor
		j["createEmptyMovieFolders"] = false
		j["autoRenameFolders"] = false
		j["pathsDefaultStatic"] = false
	} else {
		j["autoUnmonitorPreviouslyDownloadedEpisodes"] = a.autoUnmonitor
		j["createEmptySeriesFolders"] = false
		j["episodeTitleRequired"] = "always"
	}
	return j
}

func (h *arrAPI) mediaManagement(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	writeJSON(w, http.StatusOK, h.mediaManagementJSON())
}

func (h *arrAPI) putMediaManagement(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLowerKeys(r)
	if err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	if v, ok := body["recyclebin"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			a.recycleBin = s
		}
	}
	if v, ok := body["recyclebincleanupdays"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil {
			a.cleanupDays = n
		}
	}
	for _, k := range []string{"autounmonitorpreviouslydownloadedmovies", "autounmonitorpreviouslydownloadedepisodes"} {
		if v, ok := body[k]; ok {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				a.autoUnmonitor = b
			}
		}
	}
	writeJSON(w, http.StatusAccepted, h.mediaManagementJSON())
}

// ---------------------------------------------------------------------------
// Resources
// ---------------------------------------------------------------------------

type languageJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type revisionJSON struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

type qualityModelJSON struct {
	Quality  qualityDef   `json:"quality"`
	Revision revisionJSON `json:"revision"`
}

type cfRefJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func languagesJSON(names []string) []languageJSON {
	out := []languageJSON{}
	for _, n := range names {
		id := 0
		for _, l := range languages {
			if l.Name == n {
				id = l.ID
			}
		}
		out = append(out, languageJSON{ID: id, Name: n})
	}
	return out
}

func cfRefs(names []string) []cfRefJSON {
	out := []cfRefJSON{}
	for _, n := range names {
		out = append(out, cfRefJSON{ID: int(crc32.ChecksumIEEE([]byte(n))%900) + 1, Name: n})
	}
	return out
}

type movieFileJSON struct {
	ID                  int64            `json:"id"`
	MovieID             int64            `json:"movieId"`
	RelativePath        string           `json:"relativePath"`
	Path                string           `json:"path"`
	Size                int64            `json:"size"`
	DateAdded           string           `json:"dateAdded"`
	SceneName           string           `json:"sceneName,omitempty"`
	ReleaseGroup        string           `json:"releaseGroup,omitempty"`
	Edition             string           `json:"edition,omitempty"`
	Languages           []languageJSON   `json:"languages"`
	Quality             qualityModelJSON `json:"quality"`
	CustomFormats       *[]cfRefJSON     `json:"customFormats,omitempty"`
	CustomFormatScore   *int             `json:"customFormatScore,omitempty"`
	IndexerFlags        int              `json:"indexerFlags"`
	MediaInfo           *arrMediaInfo    `json:"mediaInfo,omitempty"`
	QualityCutoffNotMet bool             `json:"qualityCutoffNotMet"`
}

// movieFileJSON renders a movie file. The copy embedded in /movie carries no custom formats and no
// score (the list mapper passes no format calculator, docs/research/arr-api.md §2.1).
func (h *arrAPI) movieFileJSON(f *arrFile, m *arrMovie, embedded bool) *movieFileJSON {
	relPath := strings.TrimPrefix(f.rel, m.folder+"/")
	j := &movieFileJSON{
		ID: f.id, MovieID: f.itemID, RelativePath: relPath, Path: remote(f.rel), Size: f.size,
		DateAdded: arrTime(f.dateAdded), SceneName: f.sceneName, ReleaseGroup: f.releaseGroup, Edition: f.edition,
		Languages: languagesJSON(f.languages), Quality: qualityModelJSON{Quality: f.quality, Revision: revisionJSON{Version: 1}},
		MediaInfo: f.mediaInfo, QualityCutoffNotMet: f.cutoffNotMet,
	}
	switch {
	case !embedded:
		cfs := cfRefs(f.customFormats)
		score := f.cfScore
		j.CustomFormats, j.CustomFormatScore = &cfs, &score
	case versionBefore(h.a.version, radarrNullableCFScore):
		// Radarr < 5.22.4: the embedded file's non-nullable score serialises as a misleading 0.
		zero := 0
		j.CustomFormatScore = &zero
	}
	return j
}

type movieStatsJSON struct {
	MovieFileCount     int          `json:"movieFileCount"`
	SizeOnDisk         int64        `json:"sizeOnDisk"`
	ReleaseGroups      []string     `json:"releaseGroups"`
	MovieFileQualities []qualityDef `json:"movieFileQualities"`
}

type movieJSON struct {
	ID                  int64          `json:"id"`
	Title               string         `json:"title"`
	OriginalTitle       string         `json:"originalTitle"`
	OriginalLanguage    languageJSON   `json:"originalLanguage"`
	SortTitle           string         `json:"sortTitle"`
	SizeOnDisk          int64          `json:"sizeOnDisk"`
	Status              string         `json:"status"`
	Year                int            `json:"year"`
	Path                string         `json:"path"`
	QualityProfileID    int            `json:"qualityProfileId"`
	HasFile             bool           `json:"hasFile"`
	MovieFileID         int64          `json:"movieFileId"`
	Monitored           bool           `json:"monitored"`
	MinimumAvailability string         `json:"minimumAvailability"`
	IsAvailable         bool           `json:"isAvailable"`
	FolderName          string         `json:"folderName"`
	Runtime             int            `json:"runtime"`
	CleanTitle          string         `json:"cleanTitle"`
	ImdbID              string         `json:"imdbId,omitempty"`
	TmdbID              int            `json:"tmdbId"`
	TitleSlug           string         `json:"titleSlug"`
	RootFolderPath      string         `json:"rootFolderPath"`
	Genres              []string       `json:"genres"`
	Tags                []int64        `json:"tags"`
	Added               string         `json:"added"`
	Images              []any          `json:"images"`
	Popularity          float64        `json:"popularity"`
	MovieFile           *movieFileJSON `json:"movieFile,omitempty"`
	Statistics          movieStatsJSON `json:"statistics"`
}

func cleanTitle(t string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(t) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rootFolderOf returns the best-matching configured root folder (media-root relative).
func (a *arrState) rootFolderOf(folder string) string {
	best := ""
	for _, rf := range a.rootFolders {
		if under(folder, rf) && len(rf) > len(best) {
			best = rf
		}
	}
	if best == "" {
		best = path.Dir(folder)
	}
	return best
}

func (h *arrAPI) movieJSON(m *arrMovie) movieJSON {
	a := h.a
	j := movieJSON{
		ID: m.id, Title: m.title, OriginalTitle: m.title, OriginalLanguage: languageJSON{ID: 1, Name: "English"},
		SortTitle: strings.ToLower(m.title), Status: "released", Year: m.year, Path: remote(m.folder),
		QualityProfileID: m.qualityProfileID, HasFile: m.fileID > 0, MovieFileID: m.fileID, Monitored: m.monitored,
		MinimumAvailability: "released", IsAvailable: true, FolderName: remote(m.folder), Runtime: m.runtime,
		CleanTitle: cleanTitle(m.title), ImdbID: m.imdbID, TmdbID: m.tmdbID, TitleSlug: strconv.Itoa(m.tmdbID),
		RootFolderPath: remote(a.rootFolderOf(m.folder)), Genres: []string{}, Tags: append([]int64{}, m.tags...),
		Added: arrTime(m.added), Images: []any{}, Popularity: 42.5,
		Statistics: movieStatsJSON{ReleaseGroups: []string{}, MovieFileQualities: []qualityDef{}},
	}
	for _, f := range a.filesOf(m.id) {
		j.SizeOnDisk += f.size
		j.Statistics.MovieFileQualities = append(j.Statistics.MovieFileQualities, f.quality)
		if f.releaseGroup != "" {
			j.Statistics.ReleaseGroups = append(j.Statistics.ReleaseGroups, f.releaseGroup)
		}
	}
	j.Statistics.SizeOnDisk = j.SizeOnDisk
	if f := a.files[m.fileID]; f != nil {
		j.Statistics.MovieFileCount = 1
		j.MovieFile = h.movieFileJSON(f, m, true)
	}
	return j
}

func (h *arrAPI) movies(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := []movieJSON{}
	var tmdb int64
	if s := q.Get("tmdbid"); s != "" {
		n, ok := parseID(s)
		if !ok {
			arrErr(w, http.StatusBadRequest, "invalid tmdbId")
			return
		}
		tmdb = n
	}
	for _, m := range h.a.movies {
		if tmdb == 0 || int64(m.tmdbID) == tmdb {
			out = append(out, h.movieJSON(m))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) movie(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	m := h.a.movieByID(id)
	if !ok || m == nil {
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Movie with ID %s does not exist", r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, h.movieJSON(m))
}

// putMovie replaces the editable fields of a movie from the full resource; missing fields reset to
// the resource defaults (monitored → true, tags → empty) like the real API.
func (h *arrAPI) putMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	body, err := decodeLowerKeys(r)
	if err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	m := h.a.movieByID(id)
	if !ok || m == nil {
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Movie with ID %s does not exist", r.PathValue("id")))
		return
	}
	if lowerQuery(r).Get("movefiles") == "true" {
		h.w.violate(h.a.name, r, RuleArrMoveFiles, "moveFiles=true queues a folder move")
	}
	var missing []string
	for _, k := range []string{"monitored", "tags", "path", "qualityprofileid"} {
		if _, ok := body[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		h.w.violate(h.a.name, r, RuleArrIncompleteItemPut, "PUT /movie needs the full resource (GET → modify → PUT); missing: "+strings.Join(missing, ", "))
	}
	var p string
	if raw, ok := body["path"]; !ok || json.Unmarshal(raw, &p) != nil || p == "" {
		arrValidation(w, validationFailure{PropertyName: "Path", ErrorMessage: "'Path' must not be empty."})
		return
	}
	monitored := true
	if raw, ok := body["monitored"]; ok {
		_ = json.Unmarshal(raw, &monitored)
	}
	var tags []int64
	if raw, ok := body["tags"]; ok {
		_ = json.Unmarshal(raw, &tags)
	}
	if raw, ok := body["qualityprofileid"]; ok {
		_ = json.Unmarshal(raw, &m.qualityProfileID)
	}
	m.monitored = monitored
	m.tags = append([]int64{}, tags...)
	if rel, ok := relOf(p); ok {
		m.folder = rel // re-points the path without moving files
	}
	writeJSON(w, http.StatusAccepted, h.movieJSON(m))
}

func (h *arrAPI) movieEditor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MovieIDs         []int64 `json:"movieIds"`
		Monitored        *bool   `json:"monitored"`
		QualityProfileID *int    `json:"qualityProfileId"`
		Tags             []int64 `json:"tags"`
		ApplyTags        string  `json:"applyTags"`
		RootFolderPath   *string `json:"rootFolderPath"`
		MoveFiles        bool    `json:"moveFiles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if body.RootFolderPath != nil {
		h.w.violate(h.a.name, r, RuleArrRootFolderPath, "rootFolderPath re-points movie paths without moving files")
	}
	var targets []*arrMovie
	for _, id := range body.MovieIDs {
		if m := h.a.movieByID(id); m != nil {
			targets = append(targets, m)
		}
	}
	if len(targets) != len(body.MovieIDs) {
		arrErr(w, http.StatusInternalServerError, fmt.Sprintf("Expected query to return %d rows but returned %d", len(body.MovieIDs), len(targets)))
		return
	}
	out := []movieJSON{}
	for _, m := range targets {
		if body.Monitored != nil {
			m.monitored = *body.Monitored
		}
		if body.QualityProfileID != nil {
			m.qualityProfileID = *body.QualityProfileID
		}
		if body.Tags != nil {
			m.tags = applyTags(m.tags, body.Tags, body.ApplyTags)
		}
		out = append(out, h.movieJSON(m))
	}
	writeJSON(w, http.StatusAccepted, out)
}

func applyTags(cur, tags []int64, mode string) []int64 {
	set := map[int64]bool{}
	switch strings.ToLower(mode) {
	case "remove":
		for _, t := range cur {
			set[t] = true
		}
		for _, t := range tags {
			delete(set, t)
		}
	case "replace":
		for _, t := range tags {
			set[t] = true
		}
	default: // add
		for _, t := range append(append([]int64{}, cur...), tags...) {
			set[t] = true
		}
	}
	out := []int64{}
	for t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ---------------------------------------------------------------------------
// Files (moviefile / episodefile)
// ---------------------------------------------------------------------------

type episodeFileJSON struct {
	ID                  int64            `json:"id"`
	SeriesID            int64            `json:"seriesId"`
	SeasonNumber        int              `json:"seasonNumber"`
	RelativePath        string           `json:"relativePath"`
	Path                string           `json:"path"`
	Size                int64            `json:"size"`
	DateAdded           string           `json:"dateAdded"`
	SceneName           string           `json:"sceneName,omitempty"`
	ReleaseGroup        string           `json:"releaseGroup,omitempty"`
	Languages           []languageJSON   `json:"languages"`
	Quality             qualityModelJSON `json:"quality"`
	CustomFormats       []cfRefJSON      `json:"customFormats"`
	CustomFormatScore   int              `json:"customFormatScore"`
	IndexerFlags        int              `json:"indexerFlags"`
	ReleaseType         string           `json:"releaseType,omitempty"`
	MediaInfo           *arrMediaInfo    `json:"mediaInfo,omitempty"`
	QualityCutoffNotMet bool             `json:"qualityCutoffNotMet"`
}

func (h *arrAPI) episodeFileJSON(f *arrFile) *episodeFileJSON {
	relPath := f.rel
	if s := h.a.seriesByID(f.itemID); s != nil {
		relPath = strings.TrimPrefix(f.rel, s.folder+"/")
	}
	return &episodeFileJSON{
		ID: f.id, SeriesID: f.itemID, SeasonNumber: f.season, RelativePath: relPath, Path: remote(f.rel),
		Size: f.size, DateAdded: arrTime(f.dateAdded), SceneName: f.sceneName, ReleaseGroup: f.releaseGroup,
		Languages: languagesJSON(f.languages), Quality: qualityModelJSON{Quality: f.quality, Revision: revisionJSON{Version: 1}},
		CustomFormats: cfRefs(f.customFormats), CustomFormatScore: f.cfScore, ReleaseType: f.releaseType,
		MediaInfo: f.mediaInfo, QualityCutoffNotMet: f.cutoffNotMet,
	}
}

func (h *arrAPI) fileJSON(f *arrFile) any {
	if h.a.isRadarr() {
		m := h.a.movieByID(f.itemID)
		if m == nil {
			m = &arrMovie{folder: path.Dir(f.rel)}
		}
		return h.movieFileJSON(f, m, false)
	}
	return h.episodeFileJSON(f)
}

// fileList serves GET /moviefile?movieId= (repeatable) | ?movieFileIds= and
// GET /episodefile?seriesId= | ?episodeFileIds=.
func (h *arrAPI) fileList(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	itemParam, idsParam, noun := "seriesid", "episodefileids", "seriesId or episodeFileIds must be provided"
	if a.isRadarr() {
		itemParam, idsParam, noun = "movieid", "moviefileids", "movieId or movieFileIds must be provided"
	}
	out := []any{}
	switch {
	case len(q[itemParam]) > 0:
		vals := q[itemParam]
		if !a.isRadarr() {
			vals = vals[:1] // Sonarr: a single int
		}
		for _, s := range vals {
			id, ok := parseID(s)
			if !ok {
				arrValidation(w, validationFailure{PropertyName: itemParam, ErrorMessage: fmt.Sprintf("The value '%s' is not valid.", s)})
				return
			}
			if !a.isRadarr() && a.seriesByID(id) == nil {
				arrErr(w, http.StatusNotFound, fmt.Sprintf("Series with ID %d does not exist", id))
				return
			}
			files := a.filesOf(id)
			if a.isRadarr() && versionBefore(a.version, radarrAllMovieFiles) && len(files) > 1 {
				files = files[:1] // Radarr < 5.3.3: GetFilesByMovie(movieId).FirstOrDefault()
			}
			for _, f := range files {
				out = append(out, h.fileJSON(f))
			}
			if a.isRadarr() && versionBefore(a.version, radarrAllMovieFiles) {
				break // a single int? movieId: repeated values are not bound
			}
		}
	case len(q[idsParam]) > 0:
		seen := map[int64]bool{}
		var fs []*arrFile
		for _, s := range q[idsParam] {
			id, ok := parseID(s)
			if !ok {
				arrValidation(w, validationFailure{PropertyName: idsParam, ErrorMessage: fmt.Sprintf("The value '%s' is not valid.", s)})
				return
			}
			if f := a.files[id]; f != nil && !seen[id] {
				fs = append(fs, f)
			}
			seen[id] = true
		}
		// BasicRepository.Get(ids) throws for stale or repeated ids → HTTP 500.
		if len(fs) != len(q[idsParam]) {
			arrErr(w, http.StatusInternalServerError, fmt.Sprintf("Expected query to return %d rows but returned %d", len(q[idsParam]), len(fs)))
			return
		}
		for _, f := range fs {
			out = append(out, h.fileJSON(f))
		}
	default:
		arrErr(w, http.StatusBadRequest, noun)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) fileOne(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	f := h.a.files[id]
	if !ok || f == nil {
		noun := "EpisodeFile"
		if h.a.isRadarr() {
			noun = "MovieFile"
		}
		arrErr(w, http.StatusNotFound, fmt.Sprintf("%s with ID %s does not exist", noun, r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, h.fileJSON(f))
}

func (h *arrAPI) deleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		h.w.mu.Lock()
		h.w.violate(h.a.name, r, RuleArrMalformedDelete, fmt.Sprintf("file id %q must be a positive integer", r.PathValue("id")))
		h.w.mu.Unlock()
		arrErr(w, http.StatusNotFound, fmt.Sprintf("File with ID %s does not exist", r.PathValue("id")))
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	status, msg := h.guardedDelete(r, id)
	if status != http.StatusOK {
		arrErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusOK) // void action: empty body
}

// guardedDelete runs arrDeleteFile and checks it against Dupearr's safety invariants. Like the Plex
// media delete, the request itself is checked first (protected by the keep tag, item playing, a
// file of a full-disc backup — asking is the bug, whatever the outcome), then the outcome
// (multi-episode file still needed, keeper unavailable). Callers hold w.mu.
func (h *arrAPI) guardedDelete(r *http.Request, id int64) (int, string) {
	var g *removalGuard
	if f := h.a.files[id]; f != nil {
		g = h.w.newRemovalGuard(f.rel)
		h.w.recordClipDeletes(h.a.name, r, []string{f.rel})
		h.w.reportKeepTagged(h.a.name, r, g) // before the row (and its tags link) is gone
		h.w.reportPlaying(h.a.name, r, g)
		h.w.reportDiscRemoval(h.a.name, r, g)
	}
	status, msg := h.w.arrDeleteFile(h.a, id)
	if status == http.StatusOK && g != nil && g.existed {
		h.w.reportLosses(h.a.name, r, g, nil, true)
	}
	return status, msg
}

// malformedDelete answers DELETEs under /moviefile/ or /episodefile/ that match no endpoint (an
// empty id, extra segments): 404 like the real API, recorded because a client treating that 404
// as "already deleted" would report a delete that never happened.
func (h *arrAPI) malformedDelete(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	h.w.violate(h.a.name, r, RuleArrMalformedDelete, "DELETE path matches no endpoint (empty id or extra segments)")
	h.w.mu.Unlock()
	arrErr(w, http.StatusNotFound, "Not Found")
}

func (h *arrAPI) bulkDelete(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLowerKeys(r)
	if err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(h.a.name, r, RuleArrBulkDelete, "bulk deletes resolve every path from the first file's item; delete one file per call")
	var ids []int64
	for _, k := range []string{"moviefileids", "episodefileids"} {
		if raw, ok := body[k]; ok {
			_ = json.Unmarshal(raw, &ids)
		}
	}
	if len(ids) == 0 {
		arrErr(w, http.StatusBadRequest, "movieFileIds must be provided")
		return
	}
	for _, id := range ids {
		if status, msg := h.guardedDelete(r, id); status != http.StatusOK {
			arrErr(w, status, msg)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// deleteItem (DELETE /movie/{id}, /series/{id}) is never used by Dupearr; it removes the item and,
// with deleteFiles=true, its folder.
func (h *arrAPI) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	h.w.violate(a.name, r, RuleArrItemDelete, "deleting a whole movie/series")
	var folder string
	switch {
	case a.isRadarr() && ok && a.movieByID(id) != nil:
		m := a.movieByID(id)
		folder = m.folder
		for i, x := range a.movies {
			if x == m {
				a.movies = append(a.movies[:i], a.movies[i+1:]...)
				break
			}
		}
	case !a.isRadarr() && ok && a.seriesByID(id) != nil:
		s := a.seriesByID(id)
		folder = s.folder
		for i, x := range a.series {
			if x == s {
				a.series = append(a.series[:i], a.series[i+1:]...)
				break
			}
		}
		kept := a.episodes[:0]
		for _, e := range a.episodes {
			if e.seriesID != id {
				kept = append(kept, e)
			}
		}
		a.episodes = kept
	default:
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Item with ID %s does not exist", r.PathValue("id")))
		return
	}
	for _, f := range a.filesOf(id) {
		delete(a.files, f.id)
	}
	if q.Get("deletefiles") == "true" && validRel(folder) {
		if err := os.RemoveAll(h.w.local(folder)); err != nil {
			arrErr(w, http.StatusInternalServerError, "Unable to delete folder")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Sonarr series / episodes
// ---------------------------------------------------------------------------

type seasonStatsJSON struct {
	EpisodeFileCount  int      `json:"episodeFileCount"`
	EpisodeCount      int      `json:"episodeCount"`
	TotalEpisodeCount int      `json:"totalEpisodeCount"`
	SizeOnDisk        int64    `json:"sizeOnDisk"`
	ReleaseGroups     []string `json:"releaseGroups"`
	PercentOfEpisodes float64  `json:"percentOfEpisodes"`
}

type seasonJSON struct {
	SeasonNumber int             `json:"seasonNumber"`
	Monitored    bool            `json:"monitored"`
	Statistics   seasonStatsJSON `json:"statistics"`
}

type seriesStatsJSON struct {
	SeasonCount int `json:"seasonCount"`
	seasonStatsJSON
}

type seriesJSON struct {
	ID                int64           `json:"id"`
	Title             string          `json:"title"`
	SortTitle         string          `json:"sortTitle"`
	Status            string          `json:"status"`
	Ended             bool            `json:"ended"`
	Year              int             `json:"year"`
	Path              string          `json:"path"`
	QualityProfileID  int             `json:"qualityProfileId"`
	LanguageProfileID int             `json:"languageProfileId"`
	SeasonFolder      bool            `json:"seasonFolder"`
	Monitored         bool            `json:"monitored"`
	MonitorNewItems   string          `json:"monitorNewItems"`
	UseSceneNumbering bool            `json:"useSceneNumbering"`
	Runtime           int             `json:"runtime"`
	TvdbID            int             `json:"tvdbId"`
	TvRageID          int             `json:"tvRageId"`
	TvMazeID          int             `json:"tvMazeId"`
	TmdbID            int             `json:"tmdbId"`
	ImdbID            string          `json:"imdbId,omitempty"`
	SeriesType        string          `json:"seriesType"`
	CleanTitle        string          `json:"cleanTitle"`
	TitleSlug         string          `json:"titleSlug"`
	RootFolderPath    string          `json:"rootFolderPath"`
	Genres            []string        `json:"genres"`
	Tags              []int64         `json:"tags"`
	Added             string          `json:"added"`
	Images            []any           `json:"images"`
	Seasons           []seasonJSON    `json:"seasons"`
	Statistics        seriesStatsJSON `json:"statistics"`
}

func (h *arrAPI) seriesJSON(s *arrSeries) *seriesJSON {
	a := h.a
	j := &seriesJSON{
		ID: s.id, Title: s.title, SortTitle: strings.ToLower(s.title), Status: "continuing", Year: s.year,
		Path: remote(s.folder), QualityProfileID: s.qualityProfileID, LanguageProfileID: 1, SeasonFolder: true,
		Monitored: s.monitored, MonitorNewItems: "all", Runtime: 45, TvdbID: s.tvdbID, TmdbID: s.tmdbID,
		ImdbID: s.imdbID, SeriesType: "standard", CleanTitle: cleanTitle(s.title),
		TitleSlug:      strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s.title)), " ", "-"),
		RootFolderPath: remote(a.rootFolderOf(s.folder)) + "/", Genres: []string{}, Tags: append([]int64{}, s.tags...),
		Added: arrTime(s.added), Images: []any{}, Seasons: []seasonJSON{},
	}
	eps := a.episodesOf(s.id)
	seasons := map[int]*seasonJSON{}
	var order []int
	files := map[int64]bool{}
	var total seasonStatsJSON
	total.ReleaseGroups = []string{}
	for _, e := range eps {
		sj := seasons[e.season]
		if sj == nil {
			sj = &seasonJSON{SeasonNumber: e.season, Monitored: true, Statistics: seasonStatsJSON{ReleaseGroups: []string{}}}
			seasons[e.season] = sj
			order = append(order, e.season)
		}
		sj.Statistics.TotalEpisodeCount++
		total.TotalEpisodeCount++
		if e.fileID > 0 {
			sj.Statistics.EpisodeCount++
			total.EpisodeCount++
			if f := a.files[e.fileID]; f != nil && !files[f.id] {
				files[f.id] = true
				sj.Statistics.EpisodeFileCount++
				sj.Statistics.SizeOnDisk += f.size
				total.EpisodeFileCount++
				total.SizeOnDisk += f.size
				if f.releaseGroup != "" {
					sj.Statistics.ReleaseGroups = append(sj.Statistics.ReleaseGroups, f.releaseGroup)
					total.ReleaseGroups = append(total.ReleaseGroups, f.releaseGroup)
				}
			}
		}
	}
	sort.Ints(order)
	for _, n := range order {
		sj := seasons[n]
		if sj.Statistics.TotalEpisodeCount > 0 {
			sj.Statistics.PercentOfEpisodes = 100 * float64(sj.Statistics.EpisodeCount) / float64(sj.Statistics.TotalEpisodeCount)
		}
		j.Seasons = append(j.Seasons, *sj)
	}
	if total.TotalEpisodeCount > 0 {
		total.PercentOfEpisodes = 100 * float64(total.EpisodeCount) / float64(total.TotalEpisodeCount)
	}
	j.Statistics = seriesStatsJSON{SeasonCount: len(order), seasonStatsJSON: total}
	return j
}

func (h *arrAPI) seriesList(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	var tvdb int64
	if s := q.Get("tvdbid"); s != "" {
		n, ok := parseID(s)
		if !ok {
			arrErr(w, http.StatusBadRequest, "invalid tvdbId")
			return
		}
		tvdb = n
	}
	out := []*seriesJSON{}
	for _, s := range h.a.series {
		if tvdb == 0 || int64(s.tvdbID) == tvdb {
			out = append(out, h.seriesJSON(s))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) seriesOne(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	s := h.a.seriesByID(id)
	if !ok || s == nil {
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Series with ID %s does not exist", r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, h.seriesJSON(s))
}

type episodeJSON struct {
	ID                       int64            `json:"id"`
	SeriesID                 int64            `json:"seriesId"`
	TvdbID                   int              `json:"tvdbId"`
	EpisodeFileID            int64            `json:"episodeFileId"`
	SeasonNumber             int              `json:"seasonNumber"`
	EpisodeNumber            int              `json:"episodeNumber"`
	Title                    string           `json:"title"`
	AirDate                  string           `json:"airDate"`
	AirDateUtc               string           `json:"airDateUtc"`
	Runtime                  int              `json:"runtime"`
	HasFile                  bool             `json:"hasFile"`
	Monitored                bool             `json:"monitored"`
	UnverifiedSceneNumbering bool             `json:"unverifiedSceneNumbering"`
	EpisodeFile              *episodeFileJSON `json:"episodeFile,omitempty"`
	Series                   *seriesJSON      `json:"series,omitempty"`
}

func (h *arrAPI) episodeJSON(e *arrEpisode, withFile, withSeries bool) episodeJSON {
	j := episodeJSON{
		ID: e.id, SeriesID: e.seriesID, TvdbID: e.tvdbID, EpisodeFileID: e.fileID, SeasonNumber: e.season,
		EpisodeNumber: e.number, Title: e.title, AirDate: e.airDate.Format("2006-01-02"), AirDateUtc: arrTime(e.airDate),
		Runtime: e.runtime, HasFile: e.fileID > 0, Monitored: e.monitored,
	}
	if f := h.a.files[e.fileID]; withFile && f != nil {
		j.EpisodeFile = h.episodeFileJSON(f)
	}
	if s := h.a.seriesByID(e.seriesID); withSeries && s != nil {
		j.Series = h.seriesJSON(s)
	}
	return j
}

func (h *arrAPI) episodes(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	withFile, withSeries := q.Get("includeepisodefile") == "true", q.Get("includeseries") == "true"
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	var eps []*arrEpisode
	switch {
	case q.Get("seriesid") != "":
		id, ok := parseID(q.Get("seriesid"))
		if !ok || a.seriesByID(id) == nil {
			arrErr(w, http.StatusNotFound, fmt.Sprintf("Series with ID %s does not exist", q.Get("seriesid")))
			return
		}
		eps = a.episodesOf(id)
		if s := q.Get("seasonnumber"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				arrErr(w, http.StatusBadRequest, "invalid seasonNumber")
				return
			}
			var f []*arrEpisode
			for _, e := range eps {
				if e.season == n {
					f = append(f, e)
				}
			}
			eps = f
		}
	case len(q["episodeids"]) > 0:
		for _, s := range q["episodeids"] {
			id, _ := parseID(s)
			if e := a.episodeByID(id); e != nil {
				eps = append(eps, e)
			}
		}
	case q.Get("episodefileid") != "":
		id, _ := parseID(q.Get("episodefileid"))
		for _, e := range a.episodes {
			if id > 0 && e.fileID == id {
				eps = append(eps, e)
			}
		}
	default:
		arrErr(w, http.StatusBadRequest, "seriesId or episodeIds must be provided")
		return
	}
	out := []episodeJSON{}
	for _, e := range eps {
		out = append(out, h.episodeJSON(e, withFile, withSeries))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) episodeOne(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	e := h.a.episodeByID(id)
	if !ok || e == nil {
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Episode with ID %s does not exist", r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, h.episodeJSON(e, true, false))
}

func (h *arrAPI) episodeMonitor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EpisodeIDs []int64 `json:"episodeIds"`
		Monitored  bool    `json:"monitored"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := []episodeJSON{}
	for _, id := range body.EpisodeIDs {
		if e := h.a.episodeByID(id); e != nil {
			e.monitored = body.Monitored
			out = append(out, h.episodeJSON(e, false, false))
		}
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (h *arrAPI) putEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	e := h.a.episodeByID(id)
	if !ok || e == nil {
		arrErr(w, http.StatusNotFound, fmt.Sprintf("Episode with ID %s does not exist", r.PathValue("id")))
		return
	}
	e.monitored = body.Monitored
	writeJSON(w, http.StatusAccepted, h.episodeJSON(e, false, false))
}

// ---------------------------------------------------------------------------
// Exclusions
// ---------------------------------------------------------------------------

func (h *arrAPI) exclusionJSON(x *arrExclusion) map[string]any {
	if h.a.isRadarr() {
		return map[string]any{"id": x.id, "tmdbId": x.tmdbID, "movieTitle": x.title, "movieYear": x.year}
	}
	return map[string]any{"id": x.id, "tvdbId": x.tvdbID, "title": x.title}
}

func (h *arrAPI) exclusions(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := []map[string]any{}
	for _, x := range h.a.exclusions {
		out = append(out, h.exclusionJSON(x))
	}
	w.Header().Set("Deprecation", "true") // [Obsolete] unpaged endpoint
	writeJSON(w, http.StatusOK, out)
}

type pagingJSON struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey"`
	SortDirection string `json:"sortDirection"`
	TotalRecords  int    `json:"totalRecords"`
	Records       any    `json:"records"`
}

// pageParams reads PagingResource query parameters (page 1, pageSize 10, omitted direction →
// descending).
func pageParams(q url.Values, defaultSortKey string) (page, size int, sortKey, dir string) {
	page, size = atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("pagesize"), 10)
	sortKey, dir = q.Get("sortkey"), q.Get("sortdirection")
	if sortKey == "" {
		sortKey = defaultSortKey
	}
	switch dir {
	case "ascending", "descending":
	default:
		dir = "descending"
	}
	return page, size, sortKey, dir
}

// pageSlice returns page (1-based) of all; page and size are ≥ 1 but may be as large as MaxInt, so
// the bounds are computed with divisions and comparisons instead of overflowing products.
func pageSlice[T any](all []T, page, size int) []T {
	if page < 1 || size < 1 || page-1 > len(all)/size {
		return all[:0]
	}
	lo := min((page-1)*size, len(all)) // no overflow: (page-1)*size ≤ len(all)
	hi := len(all)
	if size < hi-lo {
		hi = lo + size
	}
	return all[lo:hi]
}

func (h *arrAPI) exclusionsPaged(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	def := "title"
	if h.a.isRadarr() {
		def = "movieTitle"
	}
	page, size, key, dir := pageParams(q, def)
	all := []map[string]any{}
	for _, x := range h.a.exclusions {
		all = append(all, h.exclusionJSON(x))
	}
	writeJSON(w, http.StatusOK, pagingJSON{Page: page, PageSize: size, SortKey: key, SortDirection: dir, TotalRecords: len(all), Records: pageSlice(all, page, size)})
}

type exclusionBody struct {
	TmdbID     int    `json:"tmdbId"`
	TvdbID     int    `json:"tvdbId"`
	MovieTitle string `json:"movieTitle"`
	MovieYear  int    `json:"movieYear"`
	Title      string `json:"title"`
}

// validateExclusion applies the exclusion validators; callers hold w.mu.
func (h *arrAPI) validateExclusion(b exclusionBody) []validationFailure {
	var fails []validationFailure
	a := h.a
	if a.isRadarr() {
		if b.TmdbID <= 0 {
			fails = append(fails, validationFailure{PropertyName: "TmdbId", ErrorMessage: "'Tmdb Id' must not be empty."})
		}
		for _, x := range a.exclusions {
			if b.TmdbID > 0 && x.tmdbID == b.TmdbID {
				fails = append(fails, validationFailure{PropertyName: "TmdbId", ErrorMessage: "This exclusion has already been added.", AttemptedValue: b.TmdbID})
			}
		}
		if strings.TrimSpace(b.MovieTitle) == "" {
			fails = append(fails, validationFailure{PropertyName: "MovieTitle", ErrorMessage: "'Movie Title' must not be empty."})
		}
		if b.MovieYear < 0 {
			fails = append(fails, validationFailure{PropertyName: "MovieYear", ErrorMessage: "'Movie Year' must be greater than or equal to '0'."})
		}
		return fails
	}
	if b.TvdbID <= 0 {
		fails = append(fails, validationFailure{PropertyName: "TvdbId", ErrorMessage: "'Tvdb Id' must not be empty."})
	}
	for _, x := range a.exclusions {
		if b.TvdbID > 0 && x.tvdbID == b.TvdbID {
			fails = append(fails, validationFailure{PropertyName: "TvdbId", ErrorMessage: "This exclusion has already been added.", AttemptedValue: b.TvdbID})
		}
	}
	if strings.TrimSpace(b.Title) == "" {
		fails = append(fails, validationFailure{PropertyName: "Title", ErrorMessage: "'Title' must not be empty."})
	}
	return fails
}

func (h *arrAPI) insertExclusion(b exclusionBody) *arrExclusion {
	x := &arrExclusion{id: h.a.nextExclusionID, tmdbID: b.TmdbID, tvdbID: b.TvdbID, title: b.Title, year: b.MovieYear}
	if h.a.isRadarr() {
		x.title, x.tvdbID = b.MovieTitle, 0
	} else {
		x.tmdbID, x.year = 0, 0
	}
	h.a.nextExclusionID++
	h.a.exclusions = append(h.a.exclusions, x)
	return x
}

func (h *arrAPI) addExclusion(w http.ResponseWriter, r *http.Request) {
	var b exclusionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if fails := h.validateExclusion(b); len(fails) > 0 {
		arrValidation(w, fails...)
		return
	}
	x := h.insertExclusion(b)
	w.Header().Set("Location", fmt.Sprintf("%s/%d", strings.TrimSuffix(r.URL.Path, "/"), x.id))
	writeJSON(w, http.StatusCreated, h.exclusionJSON(x))
}

func (h *arrAPI) addExclusionsBulk(w http.ResponseWriter, r *http.Request) {
	var bs []exclusionBody
	if err := json.NewDecoder(r.Body).Decode(&bs); err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := []map[string]any{}
	for _, b := range bs {
		if len(h.validateExclusion(b)) > 0 {
			continue // bulk skips existing / invalid entries
		}
		out = append(out, h.exclusionJSON(h.insertExclusion(b)))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) deleteExclusion(w http.ResponseWriter, r *http.Request) {
	id, _ := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	for i, x := range h.a.exclusions {
		if x.id == id {
			h.a.exclusions = append(h.a.exclusions[:i], h.a.exclusions[i+1:]...)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	arrErr(w, http.StatusNotFound, fmt.Sprintf("ImportListExclusion with ID %s does not exist", r.PathValue("id")))
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

type queueJSON struct {
	ID                                  int64            `json:"id"`
	MovieID                             int64            `json:"movieId,omitempty"`
	SeriesID                            int64            `json:"seriesId,omitempty"`
	EpisodeID                           int64            `json:"episodeId,omitempty"`
	SeasonNumber                        int              `json:"seasonNumber,omitempty"`
	Languages                           []languageJSON   `json:"languages"`
	Quality                             qualityModelJSON `json:"quality"`
	CustomFormats                       []cfRefJSON      `json:"customFormats"`
	CustomFormatScore                   int              `json:"customFormatScore"`
	Size                                float64          `json:"size"`
	Title                               string           `json:"title"`
	Sizeleft                            float64          `json:"sizeleft"`
	Timeleft                            string           `json:"timeleft,omitempty"`
	EstimatedCompletionTime             string           `json:"estimatedCompletionTime,omitempty"`
	Added                               string           `json:"added"`
	Status                              string           `json:"status"`
	TrackedDownloadStatus               string           `json:"trackedDownloadStatus"`
	TrackedDownloadState                string           `json:"trackedDownloadState"`
	StatusMessages                      []statusMsgJSON  `json:"statusMessages"`
	ErrorMessage                        string           `json:"errorMessage,omitempty"`
	DownloadID                          string           `json:"downloadId"`
	Protocol                            string           `json:"protocol"`
	DownloadClient                      string           `json:"downloadClient"`
	DownloadClientHasPostImportCategory bool             `json:"downloadClientHasPostImportCategory"`
	Indexer                             string           `json:"indexer"`
	OutputPath                          string           `json:"outputPath,omitempty"`
}

// statusMsgJSON is TrackedDownloadStatusMessage.
type statusMsgJSON struct {
	Title    string   `json:"title"`
	Messages []string `json:"messages"`
}

func (h *arrAPI) queue(w http.ResponseWriter, r *http.Request) {
	q := lowerQuery(r)
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	filterKey := "seriesids"
	if a.isRadarr() {
		filterKey = "movieids"
	}
	filter := map[int64]bool{}
	for _, s := range q[filterKey] {
		if id, ok := parseID(s); ok {
			filter[id] = true
		}
	}
	page, size, key, dir := pageParams(q, "timeleft")
	all := []queueJSON{}
	for _, qi := range a.queue {
		itemID := qi.seriesID
		if a.isRadarr() {
			itemID = qi.movieID
		}
		if len(filter) > 0 && !filter[itemID] {
			continue
		}
		j := queueJSON{
			ID: qi.id, MovieID: qi.movieID, SeriesID: qi.seriesID, EpisodeID: qi.episodeID, SeasonNumber: qi.season,
			Languages: languagesJSON([]string{"English"}), Quality: qualityModelJSON{Quality: qi.quality, Revision: revisionJSON{Version: 1}},
			CustomFormats: []cfRefJSON{}, Size: float64(qi.size), Title: qi.title, Sizeleft: float64(qi.sizeLeft),
			Added: arrTime(qi.added), Status: qi.status, TrackedDownloadStatus: qi.trackedStatus, TrackedDownloadState: qi.state,
			StatusMessages: []statusMsgJSON{}, ErrorMessage: qi.errorMessage, DownloadID: qi.downloadID, Protocol: qi.protocol,
			DownloadClient: qi.client, Indexer: qi.indexer, OutputPath: RemoteRoot + "/torrents/" + qi.title,
		}
		for _, m := range qi.statusMessages {
			j.StatusMessages = append(j.StatusMessages, statusMsgJSON{Title: m.Title, Messages: append([]string{}, m.Messages...)})
		}
		if qi.sizeLeft > 0 {
			j.Timeleft = "00:10:00"
			j.EstimatedCompletionTime = arrTime(h.w.now().Add(10 * time.Minute))
		}
		all = append(all, j)
	}
	writeJSON(w, http.StatusOK, pagingJSON{Page: page, PageSize: size, SortKey: key, SortDirection: dir, TotalRecords: len(all), Records: pageSlice(all, page, size)})
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

type commandJSON struct {
	ID                  int64          `json:"id"`
	Name                string         `json:"name"`
	CommandName         string         `json:"commandName"`
	Message             string         `json:"message,omitempty"`
	Body                map[string]any `json:"body"`
	Priority            string         `json:"priority"`
	Status              string         `json:"status"`
	Result              string         `json:"result"`
	Queued              string         `json:"queued"`
	Started             string         `json:"started,omitempty"`
	Ended               string         `json:"ended,omitempty"`
	Duration            string         `json:"duration,omitempty"`
	Trigger             string         `json:"trigger"`
	StateChangeTime     string         `json:"stateChangeTime,omitempty"`
	SendUpdatesToClient bool           `json:"sendUpdatesToClient"`
	UpdateScheduledTask bool           `json:"updateScheduledTask"`
}

var commandDisplay = map[string]string{
	"RescanMovie": "Rescan Movie", "RefreshMovie": "Refresh Movie",
	"RescanSeries": "Rescan Series", "RefreshSeries": "Refresh Series",
	"CheckHealth": "Check Health", "Housekeeping": "Housekeeping",
}

func commandJSONOf(c *arrCommand, asQueued bool) commandJSON {
	j := commandJSON{
		ID: c.id, Name: c.name, CommandName: commandDisplay[c.name], Body: c.body, Priority: "normal",
		Status: c.status, Result: c.result, Message: c.message, Queued: arrTime(c.queued), Trigger: "manual",
		SendUpdatesToClient: true, UpdateScheduledTask: true,
	}
	if asQueued {
		j.Status, j.Result, j.Message = "queued", "unknown", ""
		return j
	}
	j.Started, j.Ended = arrTime(c.started), arrTime(c.ended)
	j.StateChangeTime = j.Ended
	j.Duration = "00:00:00.0100000"
	return j
}

// optionalID decodes a nullable integer field of a lower-cased command body.
func optionalID(body map[string]json.RawMessage, key string) (int64, bool) {
	raw, present := body[key]
	if !present {
		return 0, false
	}
	var p *int64
	if err := json.Unmarshal(raw, &p); err != nil || p == nil {
		return 0, false
	}
	return *p, true
}

func idList(body map[string]json.RawMessage, key string) []int64 {
	raw, present := body[key]
	if !present {
		return nil
	}
	var ids []int64
	_ = json.Unmarshal(raw, &ids)
	return ids
}

// postCommand queues (and synchronously runs) a command. Unknown JSON properties are ignored like
// the real API, which is why a wrong field name silently targets every item — those cases are
// recorded as violations.
func (h *arrAPI) postCommand(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLowerKeys(r)
	if err != nil {
		arrErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var name string
	if raw, ok := body["name"]; ok {
		_ = json.Unmarshal(raw, &name)
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	a := h.a
	known := map[string]string{"checkhealth": "CheckHealth", "housekeeping": "Housekeeping"}
	if a.isRadarr() {
		known["rescanmovie"], known["refreshmovie"] = "RescanMovie", "RefreshMovie"
	} else {
		known["rescanseries"], known["refreshseries"] = "RescanSeries", "RefreshSeries"
	}
	canonical, ok := known[strings.ToLower(name)]
	if !ok {
		h.w.violate(a.name, r, RuleArrUnknownCommand, fmt.Sprintf("unknown command %q", name))
		arrErr(w, http.StatusInternalServerError, "Sequence contains no matching element")
		return
	}
	now := h.w.now().UTC().Truncate(time.Second)
	c := &arrCommand{
		id: a.nextCommandID, name: canonical, status: "completed", result: "successful",
		queued: now, started: now, ended: now,
		body: map[string]any{
			"name": canonical, "sendUpdatesToClient": true, "updateScheduledTask": true, "requiresDiskAccess": false,
			"isExclusive": false, "isTypeExclusive": false, "isLongRunning": false, "trigger": "manual",
			"suppressMessages": false,
		},
	}
	a.nextCommandID++
	fail := func(msg string) { c.status, c.result, c.message = "failed", "unsuccessful", msg }
	switch canonical {
	case "RescanMovie":
		if _, ok := body["movieids"]; ok {
			h.w.violate(a.name, r, RuleArrRescanAll, "RescanMovie takes a singular movieId; movieIds is ignored")
		}
		id, has := optionalID(body, "movieid")
		if !has {
			h.w.violate(a.name, r, RuleArrRescanAll, "RescanMovie without movieId rescans every movie")
			for _, m := range a.movies {
				h.w.rescanMovie(a, m)
			}
			c.message = "Completed (all movies)"
			break
		}
		c.body["movieId"] = id
		if m := a.movieByID(id); m != nil {
			c.message = h.w.rescanMovie(a, m)
		} else {
			fail(fmt.Sprintf("Movie with ID %d does not exist", id))
		}
	case "RefreshMovie":
		ids := idList(body, "movieids")
		if len(ids) == 0 {
			h.w.violate(a.name, r, RuleArrRefreshAll, "RefreshMovie without movieIds (plural) refreshes every movie")
			for _, m := range a.movies {
				h.w.rescanMovie(a, m)
			}
			break
		}
		c.body["movieIds"] = ids
		for _, id := range ids {
			if m := a.movieByID(id); m != nil {
				h.w.rescanMovie(a, m)
			} else {
				fail(fmt.Sprintf("Movie with ID %d does not exist", id))
			}
		}
	case "RescanSeries":
		if _, ok := body["seriesids"]; ok {
			h.w.violate(a.name, r, RuleArrRescanAll, "RescanSeries takes a singular seriesId; seriesIds is ignored")
		}
		id, has := optionalID(body, "seriesid")
		if !has {
			h.w.violate(a.name, r, RuleArrRescanAll, "RescanSeries without seriesId rescans every series")
			for _, s := range a.series {
				h.w.rescanSeries(a, s)
			}
			c.message = "Completed (all series)"
			break
		}
		c.body["seriesId"] = id
		if s := a.seriesByID(id); s != nil {
			c.message = h.w.rescanSeries(a, s)
		} else {
			fail(fmt.Sprintf("Series with ID %d does not exist", id))
		}
	case "RefreshSeries":
		ids := idList(body, "seriesids")
		if id, has := optionalID(body, "seriesid"); has && len(ids) == 0 {
			ids = []int64{id} // legacy write-only SeriesId setter
		}
		if len(ids) == 0 {
			h.w.violate(a.name, r, RuleArrRefreshAll, "RefreshSeries without seriesIds refreshes every series")
			for _, s := range a.series {
				h.w.rescanSeries(a, s)
			}
			break
		}
		c.body["seriesIds"] = ids
		for _, id := range ids {
			if s := a.seriesByID(id); s != nil {
				h.w.rescanSeries(a, s)
			} else {
				fail(fmt.Sprintf("Series with ID %d does not exist", id))
			}
		}
	}
	a.commands = append(a.commands, c)
	if len(a.commands) > 500 {
		a.commands = a.commands[len(a.commands)-500:]
	}
	w.Header().Set("Location", fmt.Sprintf("%s/api/v3/command/%d", a.urlBase, c.id))
	writeJSON(w, http.StatusCreated, commandJSONOf(c, true))
}

func (h *arrAPI) listCommands(w http.ResponseWriter, _ *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	out := []commandJSON{}
	for _, c := range h.a.commands {
		out = append(out, commandJSONOf(c, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *arrAPI) getCommand(w http.ResponseWriter, r *http.Request) {
	id, _ := parseID(r.PathValue("id"))
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	for _, c := range h.a.commands {
		if c.id == id {
			writeJSON(w, http.StatusOK, commandJSONOf(c, false))
			return
		}
	}
	arrErr(w, http.StatusNotFound, fmt.Sprintf("Command with ID %s does not exist", r.PathValue("id")))
}
