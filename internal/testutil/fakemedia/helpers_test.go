package fakemedia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test helpers: raw HTTP + JSON decoding only. The fake is deliberately tested without Dupearr's own
// Plex / *arr clients so the two can be developed (and fail) independently.

var testClient = &http.Client{
	Timeout: 20 * time.Second,
	// Redirects are asserted, never followed.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r response) String() string {
	b := string(r.Body)
	if len(b) > 400 {
		b = b[:400] + "…"
	}
	return fmt.Sprintf("HTTP %d %q", r.Status, b)
}

// send performs one request; body may be nil, a string, []byte or any JSON-marshalable value.
func send(t testing.TB, method, rawURL string, body any, hdr http.Header) response {
	t.Helper()
	r, err := trySend(method, rawURL, body, hdr)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	return r
}

// trySend is send without testing.TB (safe to call from helper goroutines).
func trySend(method, rawURL string, body any, hdr http.Header) (response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return response{}, fmt.Errorf("marshal body: %w", err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, rd)
	if err != nil {
		return response{}, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if rd != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := testClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, fmt.Errorf("read body: %w", err)
	}
	return response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

func plexHeaders(e *Env) http.Header {
	return http.Header{
		"Accept":                   {"application/json"},
		"X-Plex-Token":             {e.PlexToken},
		"X-Plex-Client-Identifier": {"fakemedia-test"},
	}
}

// plexDo sends an authenticated JSON request to the fake Plex server.
func plexDo(t testing.TB, e *Env, method, pathQuery string) response {
	t.Helper()
	return send(t, method, e.Plex.URL+pathQuery, nil, plexHeaders(e))
}

// plexMC GETs pathQuery, requires 200 and decodes the typed MediaContainer.
func plexMC(t testing.TB, e *Env, pathQuery string) tContainer {
	t.Helper()
	r := plexDo(t, e, http.MethodGet, pathQuery)
	if r.Status != http.StatusOK {
		t.Fatalf("GET %s: %s", pathQuery, r)
	}
	var env struct {
		MediaContainer tContainer `json:"MediaContainer"`
	}
	decodeJSON(t, r.Body, &env)
	return env.MediaContainer
}

// plexRaw GETs pathQuery, requires 200 and decodes the MediaContainer generically (numbers as
// json.Number) so tests can assert exact wire types and absent keys.
func plexRaw(t testing.TB, e *Env, pathQuery string) map[string]any {
	t.Helper()
	r := plexDo(t, e, http.MethodGet, pathQuery)
	if r.Status != http.StatusOK {
		t.Fatalf("GET %s: %s", pathQuery, r)
	}
	var env map[string]any
	decodeJSON(t, r.Body, &env)
	mc, ok := env["MediaContainer"].(map[string]any)
	if !ok {
		t.Fatalf("GET %s: no MediaContainer object in %s", pathQuery, r)
	}
	return mc
}

func decodeJSON(t testing.TB, b []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %q: %v", truncate(string(b), 300), err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func arrHeaders(s *Server) http.Header {
	return http.Header{"X-Api-Key": {s.APIKey}, "Accept": {"application/json"}}
}

// arrDo sends an authenticated request (X-Api-Key) to an *arr server; path is relative to its URL
// (which includes the URL base).
func arrDo(t testing.TB, s *Server, method, path string, body any) response {
	t.Helper()
	return send(t, method, s.URL+path, body, arrHeaders(s))
}

// arrJSON sends a request, requires status want and decodes the body into v (when non-nil).
func arrJSON(t testing.TB, s *Server, method, path string, body any, want int, v any) {
	t.Helper()
	r := arrDo(t, s, method, path, body)
	if r.Status != want {
		t.Fatalf("%s %s: got %s, want %d", method, path, r, want)
	}
	if v != nil {
		decodeJSON(t, r.Body, v)
	}
}

// requireViolation fails unless a violation with rule was recorded; it returns it.
func requireViolation(t testing.TB, e *Env, rule string) Violation {
	t.Helper()
	for _, v := range e.Violations() {
		if v.Rule == rule {
			return v
		}
	}
	t.Fatalf("no %q violation recorded; got %v", rule, e.Violations())
	return Violation{}
}

func requireNoViolations(t testing.TB, e *Env) {
	t.Helper()
	if v := e.Violations(); len(v) > 0 {
		t.Fatalf("unexpected violations: %v", v)
	}
}

// ---------------------------------------------------------------------------
// Typed Plex wire shapes (only what the tests assert)
// ---------------------------------------------------------------------------

type tContainer struct {
	Size               json.RawMessage `json:"size"`
	TotalSize          *int            `json:"totalSize"`
	Offset             *int            `json:"offset"`
	MachineIdentifier  string          `json:"machineIdentifier"`
	Version            string          `json:"version"`
	FriendlyName       string          `json:"friendlyName"`
	MyPlexUsername     string          `json:"myPlexUsername"`
	AllowMediaDeletion *bool           `json:"allowMediaDeletion"`
	LibrarySectionID   int             `json:"librarySectionID"`
	Directory          []tDirectory    `json:"Directory"`
	Metadata           []tMeta         `json:"Metadata"`
	Setting            []tSetting      `json:"Setting"`
}

type tDirectory struct {
	Key      json.RawMessage `json:"key"`
	Type     string          `json:"type"`
	Title    string          `json:"title"`
	Agent    string          `json:"agent"`
	Location []tLocation     `json:"Location"`
}

type tLocation struct {
	Path string `json:"path"`
}

type tSetting struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

type tGuid struct {
	ID string `json:"id"`
}

type tMeta struct {
	RatingKey            string      `json:"ratingKey"`
	Key                  string      `json:"key"`
	Type                 string      `json:"type"`
	Title                string      `json:"title"`
	Year                 int         `json:"year"`
	GUID                 string      `json:"guid"`
	EditionTitle         string      `json:"editionTitle"`
	Duration             int64       `json:"duration"`
	AddedAt              int64       `json:"addedAt"`
	Index                int         `json:"index"`
	ParentIndex          int         `json:"parentIndex"`
	ParentRatingKey      string      `json:"parentRatingKey"`
	GrandparentRatingKey string      `json:"grandparentRatingKey"`
	GrandparentGUID      *string     `json:"grandparentGuid"`
	GrandparentTitle     string      `json:"grandparentTitle"`
	LibrarySectionID     int         `json:"librarySectionID"`
	LeafCount            int         `json:"leafCount"`
	SessionKey           string      `json:"sessionKey"`
	Guids                []tGuid     `json:"Guid"`
	Location             []tLocation `json:"Location"`
	Media                []tMedia    `json:"Media"`
}

type tMedia struct {
	ID              int64   `json:"id"`
	Duration        *int64  `json:"duration"`
	Bitrate         *int    `json:"bitrate"`
	Width           *int    `json:"width"`
	Height          int     `json:"height"`
	VideoCodec      string  `json:"videoCodec"`
	VideoResolution string  `json:"videoResolution"`
	AudioCodec      string  `json:"audioCodec"`
	AudioChannels   int     `json:"audioChannels"`
	Container       string  `json:"container"`
	ProxyType       int     `json:"proxyType"`
	Target          string  `json:"target"`
	Parts           []tPart `json:"Part"`
}

type tPart struct {
	ID         json.RawMessage  `json:"id"`
	Key        string           `json:"key"`
	File       string           `json:"file"`
	Size       int64            `json:"size"`
	Duration   *int64           `json:"duration"`
	Exists     *bool            `json:"exists"`
	Accessible *bool            `json:"accessible"`
	Streams    []map[string]any `json:"Stream"`
}

// detail fetches one item with checkFiles=1&includeGuids=1 (the executor's re-verify call).
func detail(t testing.TB, e *Env, rk string) tMeta {
	t.Helper()
	mc := plexMC(t, e, "/library/metadata/"+rk+"?checkFiles=1&includeGuids=1&skipRefresh=1")
	if len(mc.Metadata) != 1 {
		t.Fatalf("detail %s: %d items", rk, len(mc.Metadata))
	}
	return mc.Metadata[0]
}

// mediaWithFile returns the media of m whose first part path contains substr.
func mediaWithFile(t testing.TB, m tMeta, substr string) tMedia {
	t.Helper()
	for _, md := range m.Media {
		if len(md.Parts) > 0 && strings.Contains(md.Parts[0].File, substr) {
			return md
		}
	}
	t.Fatalf("%s: no media with a file containing %q", m.Title, substr)
	return tMedia{}
}

// streamsOfType returns the streams of a part with the given streamType (1 video, 2 audio, 3 sub).
func streamsOfType(p tPart, typ int) []map[string]any {
	var out []map[string]any
	for _, s := range p.Streams {
		if fmt.Sprint(s["streamType"]) == fmt.Sprint(typ) {
			out = append(out, s)
		}
	}
	return out
}

// str renders a decoded JSON value for comparisons ("" for absent).
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// ---------------------------------------------------------------------------
// Typed *arr shapes
// ---------------------------------------------------------------------------

type tQualityDef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
	Modifier   string `json:"modifier"`
}

type tArrFile struct {
	ID                int64  `json:"id"`
	MovieID           int64  `json:"movieId"`
	SeriesID          int64  `json:"seriesId"`
	SeasonNumber      int    `json:"seasonNumber"`
	RelativePath      string `json:"relativePath"`
	Path              string `json:"path"`
	Size              int64  `json:"size"`
	DateAdded         string `json:"dateAdded"`
	ReleaseGroup      string `json:"releaseGroup"`
	SceneName         string `json:"sceneName"`
	Edition           string `json:"edition"`
	ReleaseType       string `json:"releaseType"`
	CustomFormatScore *int   `json:"customFormatScore"`
	CustomFormats     []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"customFormats"`
	Quality struct {
		Quality tQualityDef `json:"quality"`
	} `json:"quality"`
	Languages []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"languages"`
	MediaInfo *struct {
		AudioLanguages        string  `json:"audioLanguages"`
		Subtitles             string  `json:"subtitles"`
		Resolution            string  `json:"resolution"`
		VideoCodec            string  `json:"videoCodec"`
		VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
		AudioCodec            string  `json:"audioCodec"`
		AudioChannels         float64 `json:"audioChannels"`
		RunTime               string  `json:"runTime"`
	} `json:"mediaInfo"`
	QualityCutoffNotMet bool `json:"qualityCutoffNotMet"`
}

type tMovie struct {
	ID             int64          `json:"id"`
	Title          string         `json:"title"`
	Year           int            `json:"year"`
	TmdbID         int            `json:"tmdbId"`
	ImdbID         string         `json:"imdbId"`
	Path           string         `json:"path"`
	FolderName     string         `json:"folderName"`
	RootFolderPath string         `json:"rootFolderPath"`
	HasFile        bool           `json:"hasFile"`
	MovieFileID    int64          `json:"movieFileId"`
	Monitored      bool           `json:"monitored"`
	Tags           []int64        `json:"tags"`
	MovieFile      map[string]any `json:"movieFile"`
	SizeOnDisk     int64          `json:"sizeOnDisk"`
}

type tSeries struct {
	ID             int64   `json:"id"`
	Title          string  `json:"title"`
	TvdbID         int     `json:"tvdbId"`
	Path           string  `json:"path"`
	RootFolderPath string  `json:"rootFolderPath"`
	Monitored      bool    `json:"monitored"`
	Tags           []int64 `json:"tags"`
	Statistics     struct {
		EpisodeFileCount  int `json:"episodeFileCount"`
		EpisodeCount      int `json:"episodeCount"`
		TotalEpisodeCount int `json:"totalEpisodeCount"`
	} `json:"statistics"`
}

type tEpisode struct {
	ID            int64     `json:"id"`
	SeriesID      int64     `json:"seriesId"`
	SeasonNumber  int       `json:"seasonNumber"`
	EpisodeNumber int       `json:"episodeNumber"`
	EpisodeFileID int64     `json:"episodeFileId"`
	HasFile       bool      `json:"hasFile"`
	Monitored     bool      `json:"monitored"`
	EpisodeFile   *tArrFile `json:"episodeFile"`
}

type tPage[T any] struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey"`
	SortDirection string `json:"sortDirection"`
	TotalRecords  int    `json:"totalRecords"`
	Records       []T    `json:"records"`
}

type tQueueRecord struct {
	ID                   int64   `json:"id"`
	MovieID              int64   `json:"movieId"`
	SeriesID             int64   `json:"seriesId"`
	EpisodeID            int64   `json:"episodeId"`
	Status               string  `json:"status"`
	TrackedDownloadState string  `json:"trackedDownloadState"`
	Sizeleft             float64 `json:"sizeleft"`
	Title                string  `json:"title"`
}

type tCommand struct {
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Result  string         `json:"result"`
	Message string         `json:"message"`
	Body    map[string]any `json:"body"`
}

// ---------------------------------------------------------------------------
// recordingTB captures Fatalf/Errorf so failure paths of Start / AssertNoViolations can be tested.
// ---------------------------------------------------------------------------

type recordingTB struct {
	t        *testing.T
	mu       sync.Mutex
	fatals   []string
	errors   []string
	cleanups []func()
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *recordingTB) TempDir() string { return r.t.TempDir() }

// runCleanups runs registered cleanups in LIFO order, like testing.T.
func (r *recordingTB) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}
