// Package arr is the Radarr (v5/v6) and Sonarr (v4) API v3 client used to enrich versions with
// *arr data and to delete files through the owning *arr, plus the inbound webhook payload types.
//
// Wire formats follow docs/research/arr-api.md; behaviour follows docs/DECISIONS.md D3:
//   - every request sends X-Api-Key (never the ?apikey= query parameter, which the *arr would
//     prefer over the header even when stale), User-Agent: Dupearr/<version> and
//     Accept: application/json;
//   - timeouts are always set (default 30s; library-sized listings get at least 2 minutes);
//   - redirects are never followed: an *arr answers a request that lacks its URL base with a 307
//     (ErrRedirect tells the user to include it), and following a redirect could silently turn a
//     DELETE into a GET;
//   - files are deleted one at a time (never the bulk endpoints), and commands are built from typed
//     structs whose field names are unit-tested, because a misspelled id field makes the *arr
//     rescan the whole library.
//
// The package does no logging and never puts the API key in URLs or error messages.
package arr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// DefaultTimeout is the per-request timeout used when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Second

const (
	// listTimeoutFloor is the minimum timeout for library-sized listings (GET /movie, /series,
	// /episode…), which can take tens of seconds on large libraries (research §2.1).
	listTimeoutFloor = 2 * time.Minute
	// maxConcurrency caps concurrent requests to one instance (research §9).
	maxConcurrency = 4
	// queuePageSize is the page size used to walk GET /api/v3/queue.
	queuePageSize = 200
	// maxQueuePages bounds the queue walk (50k entries) so a server that ignores paging cannot
	// keep Dupearr looping.
	maxQueuePages = 250
	// defaultRetryDelay is the pause before retrying an idempotent GET.
	defaultRetryDelay = time.Second
	// defaultCommandPoll is the polling interval while waiting for a running *arr command.
	defaultCommandPoll = time.Second
	// defaultCommandWait bounds how long Rescan waits for an identical, already running rescan.
	defaultCommandWait = 2 * time.Minute
)

// Options configures a Client.
type Options struct {
	// VerifyTLS enables TLS certificate verification (models.ArrInstance.VerifyTLS).
	VerifyTLS bool
	// Timeout bounds each request (default 30s). Library-sized listings get at least 2 minutes.
	Timeout time.Duration
	// HTTPClient overrides the HTTP client (tests). A copy is used whose redirect policy never
	// follows redirects; VerifyTLS is then the caller's responsibility.
	HTTPClient *http.Client
}

// Client talks to one Radarr or Sonarr instance. It is safe for concurrent use.
type Client struct {
	inst       models.ArrInstance
	base       *url.URL // instance URL incl. URL base; no query, fragment or trailing slash
	baseErr    error    // non-nil when inst.URL is unusable; returned by every call
	hc         *http.Client
	timeout    time.Duration
	retryDelay time.Duration
	// commandPoll / commandWait: polling interval and limit while Rescan waits for an identical
	// running command (fields so tests can shorten them).
	commandPoll time.Duration
	commandWait time.Duration
	// limits bounds decoded responses (defaultLimits; a field so tests can lower it).
	limits responseLimits
}

// New returns a client for inst. It never fails: an unusable URL or kind is reported by every
// method call instead.
func New(inst models.ArrInstance, opts Options) *Client {
	c := &Client{
		inst:        inst,
		timeout:     opts.Timeout,
		retryDelay:  defaultRetryDelay,
		commandPoll: defaultCommandPoll,
		commandWait: defaultCommandWait,
		limits:      defaultLimits,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	c.base, c.baseErr = parseBaseURL(inst.URL)
	c.hc = newHTTPClient(opts)
	return c
}

// SystemStatus is the subset of /api/v3/system/status Dupearr uses.
type SystemStatus struct {
	AppName, InstanceName, Version string
}

// TrackedFile is an *arr-tracked file normalized for matching against media-server versions.
// Only the file the *arr currently tracks for an item is reported (Radarr: movie.movieFileId;
// Sonarr: files referenced by at least one episode). Orphan database rows left behind by in-place
// imports are effectively untracked duplicates and are not reported.
type TrackedFile struct {
	Path     string // as the *arr sees it
	Size     int64
	Info     models.ArrFileInfo // InstanceID/Name/Kind filled
	TmdbID   int
	ImdbID   string
	TvdbID   int // series tvdb id for Sonarr
	Season   int
	Episodes []int // episode numbers covered (multi-episode files → several)
	// MediaInfo is the *arr's ffprobe summary (nil when the *arr has not analysed the file).
	MediaInfo *MediaInfo
}

// TrackedFileRef is what one moviefile/episodefile row currently names (Client.File): the file's
// absolute path as the *arr sees it, its size in bytes and the movie (Radarr) or series (Sonarr)
// it belongs to.
type TrackedFileRef struct {
	Path   string
	Size   int64
	ItemID int64 // movieId (Radarr) / seriesId (Sonarr)
}

// MediaInfo is the *arr's media analysis of a file, parsed from its REST encoding ("WxH"
// resolution, "/"-joined language lists).
type MediaInfo struct {
	Width, Height         int // from "3840x2160"; 0 when unknown
	VideoCodec            string
	VideoBitDepth         int
	VideoBitrate          int64  // bits/s; 0 when unknown
	VideoDynamicRange     string // "HDR" or ""
	VideoDynamicRangeType string // "DV HDR10", "HDR10", "HDR10Plus", "HLG", …
	AudioCodec            string
	AudioChannels         float64  // e.g. 5.1, 7.1
	AudioLanguages        []string // one entry per audio stream, e.g. ["eng", "fre"]
	Subtitles             []string // one entry per subtitle stream
	RunTime               string   // "H:MM:SS" or "M:SS"
}

// TrackedFilter limits TrackedFiles. When all three maps are nil every tracked file is returned;
// otherwise only items whose id is set to true in one of the maps the *arr can match are returned
// (Radarr: TmdbIDs, ImdbIDs; Sonarr: TvdbIDs, ImdbIDs — at series level). IMDb ids match
// case-insensitively.
type TrackedFilter struct {
	TmdbIDs map[int]bool
	ImdbIDs map[string]bool
	TvdbIDs map[int]bool
}

// ExclusionTarget identifies an item to add to the *arr import-list exclusions.
type ExclusionTarget struct {
	TmdbID int
	TvdbID int
	Title  string
	Year   int
}

// MediaManagement is the subset of /api/v3/config/mediamanagement Dupearr uses.
type MediaManagement struct {
	// RecycleBin is the *arr's recycle bin path (as the *arr sees it). Empty means *arr deletes
	// are permanent.
	RecycleBin string
	// RecycleBinCleanupDays is the retention in days; 0 keeps files until emptied manually.
	RecycleBinCleanupDays int
}

// Errors.
var (
	ErrUnauthorized = errors.New("arr: unauthorized (check API key)")
	ErrNotFound     = errors.New("arr: not found")
	// ErrConflict is returned for HTTP 409 on file deletes (the *arr's root/series folder is missing —
	// usually an unmounted share). The executor aborts the whole run on it (docs/DECISIONS.md D3).
	ErrConflict = errors.New("arr: conflict (root folder missing or unavailable)")
	// ErrUnavailable is returned for HTTP 503 (the *arr is starting up); retry later.
	ErrUnavailable = errors.New("arr: temporarily unavailable")
	// ErrRedirect is returned when the *arr answers with a redirect. An *arr redirects (307) every
	// request that lacks its URL base, so the URL must include it. Redirects are never followed.
	ErrRedirect = errors.New("arr: unexpected redirect (include the *arr URL base in the URL, e.g. http://host:7878/radarr, and check http vs https)")
	// ErrWrongApp is returned by Status when the application at the URL is not the configured kind.
	ErrWrongApp = errors.New("arr: wrong application at this URL")
	// ErrInvalidArgument is returned, before any request is sent, for input that is unusable or
	// would make the *arr act on the wrong item or on every item (e.g. a zero id).
	ErrInvalidArgument = errors.New("arr: invalid argument")
)

// check reports configuration problems that make every call fail.
func (c *Client) check() error {
	if c.baseErr != nil {
		return c.baseErr
	}
	switch c.inst.Kind {
	case models.ArrRadarr, models.ArrSonarr:
		return nil
	}
	return fmt.Errorf("%w: unsupported *arr kind %q (want %q or %q)", ErrInvalidArgument, c.inst.Kind, models.ArrRadarr, models.ArrSonarr)
}

// appName is the SystemResource.appName of an *arr kind.
func appName(k models.ArrKind) string {
	switch k {
	case models.ArrRadarr:
		return "Radarr"
	case models.ArrSonarr:
		return "Sonarr"
	}
	return string(k)
}

// Status fetches /api/v3/system/status; also validates kind matches AppName.
func (c *Client) Status(ctx context.Context) (*SystemStatus, error) {
	var s statusResource
	if err := c.do(ctx, request{method: http.MethodGet, path: "system/status"}, &s); err != nil {
		return nil, err
	}
	want := appName(c.inst.Kind)
	got := strings.TrimSpace(s.AppName)
	if got == "" {
		return nil, fmt.Errorf("%w: the server did not report an application name; is this a %s URL?", ErrWrongApp, want)
	}
	if !strings.EqualFold(got, want) {
		return nil, fmt.Errorf("%w: expected %s but found %q; check the URL and port", ErrWrongApp, want, cleanMessage(got))
	}
	return &SystemStatus{AppName: got, InstanceName: s.InstanceName, Version: s.Version}, nil
}

// fileResourceName is the per-file REST resource of the instance kind.
func (c *Client) fileResourceName() string {
	if c.inst.Kind == models.ArrSonarr {
		return "episodefile"
	}
	return "moviefile"
}

// DeleteFile deletes one moviefile (Radarr) or episodefile (Sonarr) with
// DELETE /api/v3/{moviefile|episodefile}/{id}. The bulk endpoints are never used (they resolve
// every path from the first file's item). The file goes to the *arr's recycle bin when one is
// configured (see MediaManagement), otherwise it is deleted permanently.
//
// Errors: ErrNotFound (404: the file row no longer exists), ErrConflict (409: the item's root
// folder is missing or empty — abort the run), ErrUnavailable (503). A failed delete is never
// retried automatically.
func (c *Client) DeleteFile(ctx context.Context, fileID int64) error {
	if err := c.check(); err != nil {
		return err
	}
	if fileID <= 0 {
		return fmt.Errorf("%w: file id must be > 0 (got %d)", ErrInvalidArgument, fileID)
	}
	return c.do(ctx, request{method: http.MethodDelete, path: c.fileResourceName() + "/" + strconv.FormatInt(fileID, 10)}, nil)
}

// Rescan runs RescanMovie / RescanSeries for the item via POST /api/v3/command with exactly
// {"name":"RescanMovie","movieId":N} or {"name":"RescanSeries","seriesId":N}. itemID must be > 0:
// a command without an id rescans the whole library.
//
// The *arr merges a command into an identical one that is already queued or running
// (research §2.5). A running rescan may have listed the folder before the caller's delete, so
// Rescan first waits (up to 2 minutes) for an identical running rescan to finish — e.g. the one
// queued for the previous group of the same series — and only then queues its own. The answer is
// checked to be the requested command for this item; anything else is an error.
func (c *Client) Rescan(ctx context.Context, itemID int64) error {
	if err := c.check(); err != nil {
		return err
	}
	if itemID <= 0 {
		return fmt.Errorf("%w: rescan needs an item id > 0 (got %d); refusing to rescan the whole library", ErrInvalidArgument, itemID)
	}
	name := "RescanMovie"
	var body any = rescanMovieCommand{Name: name, MovieID: itemID}
	if c.inst.Kind == models.ArrSonarr {
		name = "RescanSeries"
		body = rescanSeriesCommand{Name: name, SeriesID: itemID}
	}
	if err := c.waitForRunningCommand(ctx, name, itemID); err != nil {
		return err
	}
	var cmd commandResource
	if err := c.do(ctx, request{method: http.MethodPost, path: "command", body: body}, &cmd); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(cmd.Name), name) {
		return fmt.Errorf("arr: POST /api/v3/command: expected a %s command in the answer but got %q; the rescan was probably not queued", name, cleanMessage(cmd.Name))
	}
	if got := cmd.itemID(c.inst.Kind); got == nil || *got != itemID {
		// The *arr accepted the command without our id: it is rescanning every item.
		return fmt.Errorf("arr: POST /api/v3/command: the *arr queued %s without item %d (answer %s); it may be rescanning the whole library", name, itemID, describeID(got))
	}
	return nil
}

// waitForRunningCommand waits until no identical command (same name and item id) is running.
// Queued duplicates are harmless (they have not listed the folder yet). The check is a safeguard:
// if the command list cannot be read for a reason other than the instance being unusable, the
// rescan proceeds.
func (c *Client) waitForRunningCommand(ctx context.Context, name string, itemID int64) error {
	var cmds []commandResource
	switch err := c.do(ctx, request{method: http.MethodGet, path: "command"}, &cmds); {
	case err == nil:
	case ctx.Err() != nil, errors.Is(err, ErrUnauthorized), errors.Is(err, ErrUnavailable), errors.Is(err, ErrRedirect):
		return err
	default:
		return nil
	}
	var running []int64
	for _, cmd := range cmds {
		if id := cmd.itemID(c.inst.Kind); cmd.ID > 0 && id != nil && *id == itemID &&
			strings.EqualFold(cmd.Name, name) && strings.EqualFold(cmd.Status, "started") {
			running = append(running, cmd.ID)
		}
	}
	if len(running) == 0 {
		return nil
	}
	deadline := time.Now().Add(c.commandWait)
	for _, id := range running {
		for {
			var cmd commandResource
			err := c.do(ctx, request{method: http.MethodGet, path: "command/" + strconv.FormatInt(id, 10)}, &cmd)
			if isArrNotFound(err) {
				break // finished and already trimmed
			}
			if err != nil {
				return fmt.Errorf("arr: waiting for the running %s of item %d: %w", name, itemID, err)
			}
			if !strings.EqualFold(cmd.Status, "queued") && !strings.EqualFold(cmd.Status, "started") {
				break // completed, failed, aborted, cancelled, orphaned
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("arr: an identical %s of item %d (command %d) is still running after %s; a new one would be merged into it, retry later", name, itemID, id, c.commandWait)
			}
			if err := sleepCtx(ctx, c.commandPoll); err != nil {
				return fmt.Errorf("arr: waiting for the running %s of item %d: %w", name, itemID, err)
			}
		}
	}
	return nil
}

// describeID renders a command's item id for an error message.
func describeID(id *int64) string {
	if id == nil {
		return "without an id"
	}
	return "for id " + strconv.FormatInt(*id, 10)
}

// Unmonitor unmonitors the movie (Radarr: PUT /api/v3/movie/editor {"movieIds":[id],"monitored":false})
// or info.EpisodeIDs (Sonarr: PUT /api/v3/episode/monitor {"episodeIds":[…],"monitored":false}).
// info must belong to this instance (ids are per instance); a Sonarr info without episode ids is
// an error.
func (c *Client) Unmonitor(ctx context.Context, info models.ArrFileInfo) error {
	if err := c.check(); err != nil {
		return err
	}
	if err := c.checkOwnership(info); err != nil {
		return err
	}
	if c.inst.Kind == models.ArrSonarr {
		ids, err := positiveUnique(info.EpisodeIDs)
		if err != nil {
			return fmt.Errorf("%w: episode ids: %v", ErrInvalidArgument, err)
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: no episode ids to unmonitor", ErrInvalidArgument)
		}
		return c.do(ctx, request{method: http.MethodPut, path: "episode/monitor", body: episodeMonitorBody{EpisodeIDs: ids, Monitored: false}}, nil)
	}
	if info.ItemID <= 0 {
		return fmt.Errorf("%w: movie id must be > 0 (got %d)", ErrInvalidArgument, info.ItemID)
	}
	return c.do(ctx, request{method: http.MethodPut, path: "movie/editor", body: movieEditorBody{MovieIDs: []int64{info.ItemID}, Monitored: false}}, nil)
}

// checkOwnership rejects file info that belongs to another instance or kind: acting on another
// instance's ids would hit an unrelated item.
func (c *Client) checkOwnership(info models.ArrFileInfo) error {
	if info.Kind != "" && info.Kind != c.inst.Kind {
		return fmt.Errorf("%w: file info is for %s but this client is %s", ErrInvalidArgument, info.Kind, c.inst.Kind)
	}
	if info.InstanceID != 0 && c.inst.ID != 0 && info.InstanceID != c.inst.ID {
		return fmt.Errorf("%w: file info is for *arr instance %d but this client is instance %d", ErrInvalidArgument, info.InstanceID, c.inst.ID)
	}
	return nil
}

// positiveUnique returns ids sorted and de-duplicated; any id ≤ 0 is an error.
func positiveUnique(ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid id %d", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// AddExclusion adds an import-list exclusion so import lists do not re-add the item.
// Radarr: POST /api/v3/exclusions {"tmdbId","movieTitle","movieYear"} (TmdbID > 0, Title and
// Year > 0 required: Radarr < 5.9 rejects year 0). Sonarr: POST /api/v3/importlistexclusion
// {"tvdbId","title"} (TvdbID > 0 and Title required). An exclusion that already exists counts as
// success.
func (c *Client) AddExclusion(ctx context.Context, t ExclusionTarget) error {
	if err := c.check(); err != nil {
		return err
	}
	title := strings.TrimSpace(t.Title)
	if title == "" {
		return fmt.Errorf("%w: exclusion needs a title", ErrInvalidArgument)
	}
	var (
		listPath string
		body     any
		matches  func(e exclusionResource) bool
	)
	if c.inst.Kind == models.ArrSonarr {
		if t.TvdbID <= 0 {
			return fmt.Errorf("%w: Sonarr exclusion needs a TVDB id > 0 (got %d)", ErrInvalidArgument, t.TvdbID)
		}
		listPath = "importlistexclusion"
		body = sonarrExclusionBody{TvdbID: t.TvdbID, Title: title}
		matches = func(e exclusionResource) bool { return e.TvdbID == t.TvdbID }
	} else {
		if t.TmdbID <= 0 {
			return fmt.Errorf("%w: Radarr exclusion needs a TMDb id > 0 (got %d)", ErrInvalidArgument, t.TmdbID)
		}
		if t.Year <= 0 {
			return fmt.Errorf("%w: Radarr exclusion needs a movie year > 0 (got %d)", ErrInvalidArgument, t.Year)
		}
		listPath = "exclusions"
		body = radarrExclusionBody{TmdbID: t.TmdbID, MovieTitle: title, MovieYear: t.Year}
		matches = func(e exclusionResource) bool { return e.TmdbID == t.TmdbID }
	}

	// Pre-check: Radarr < 5.9 has no duplicate validation, so a second POST would add a duplicate
	// row. A failed pre-check is not fatal unless it shows the instance is unusable.
	var existing []exclusionResource
	switch err := c.do(ctx, request{method: http.MethodGet, path: listPath, long: true}, &existing); {
	case err == nil:
		for _, e := range existing {
			if matches(e) {
				return nil
			}
		}
	case ctx.Err() != nil, errors.Is(err, ErrUnauthorized), errors.Is(err, ErrUnavailable), errors.Is(err, ErrRedirect), errors.Is(err, ErrInvalidArgument):
		return err
	}

	err := c.do(ctx, request{method: http.MethodPost, path: listPath, body: body}, nil)
	if err != nil && isAlreadyExcluded(err) {
		return nil
	}
	return err
}

// exclusionResource is the subset of an exclusion used by the pre-check.
type exclusionResource struct {
	TmdbID int `json:"tmdbId"`
	TvdbID int `json:"tvdbId"`
}

// isAlreadyExcluded reports whether a failed exclusion POST means "already present": a 400
// validation failure mentioning "already" (≥ Radarr 5.9, Sonarr) or a 409 unique-constraint
// conflict.
func isAlreadyExcluded(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	switch he.StatusCode {
	case http.StatusConflict:
		return true
	case http.StatusBadRequest:
		return strings.Contains(strings.ToLower(he.Message), "already")
	}
	return false
}

// MediaManagement fetches the recycle bin settings (GET /api/v3/config/mediamanagement).
func (c *Client) MediaManagement(ctx context.Context) (*MediaManagement, error) {
	var mm mediaManagementResource
	if err := c.do(ctx, request{method: http.MethodGet, path: "config/mediamanagement"}, &mm); err != nil {
		return nil, err
	}
	return &MediaManagement{RecycleBin: strings.TrimSpace(mm.RecycleBin), RecycleBinCleanupDays: mm.RecycleBinCleanupDays}, nil
}

// QueueItemIDs returns the movie ids (Radarr) or series ids (Sonarr) that currently have entries in
// the download/import queue (GET /api/v3/queue, all pages). Groups whose *arr item is busy are
// deferred by the scanner (docs/DECISIONS.md D3).
func (c *Client) QueueItemIDs(ctx context.Context) (map[int64]bool, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	unknownParam := "includeUnknownMovieItems"
	if c.inst.Kind == models.ArrSonarr {
		unknownParam = "includeUnknownSeriesItems"
	}
	ids := make(map[int64]bool)
	for page := 1; ; page++ {
		if page > maxQueuePages {
			return nil, fmt.Errorf("arr: queue has more than %d pages; refusing to continue", maxQueuePages)
		}
		q := url.Values{
			"page":       {strconv.Itoa(page)},
			"pageSize":   {strconv.Itoa(queuePageSize)},
			unknownParam: {"false"},
		}
		var p queuePage
		if err := c.do(ctx, request{method: http.MethodGet, path: "queue", query: q}, &p); err != nil {
			return nil, err
		}
		for _, r := range p.Records {
			id := r.MovieID
			if c.inst.Kind == models.ArrSonarr {
				id = r.SeriesID
			}
			if id > 0 {
				ids[id] = true
			}
		}
		size := p.PageSize
		if size <= 0 {
			size = queuePageSize
		}
		// A short or empty page is the last one. totalRecords only ends the walk when it is set:
		// a missing (0) total with a full page must not hide the next pages, or a busy item would
		// not be deferred.
		if len(p.Records) == 0 || len(p.Records) < size || (p.TotalRecords > 0 && page*size >= p.TotalRecords) {
			return ids, nil
		}
	}
}
