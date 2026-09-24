package fakemedia

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Server names in the request log / Options.Addrs besides the *arr instance names.
const ServerPlex = "plex"

const (
	maxRequestBody = 1 << 20 // bytes kept per logged request body
	maxRequests    = 10000   // request log entries kept (oldest dropped in batches: ≤10% more are held)
)

// TB is the subset of testing.TB used by [Start]; *testing.T and *testing.B satisfy it.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Cleanup(func())
	TempDir() string
}

// Options configures [New].
type Options struct {
	// Dir is the local directory that corresponds to RemoteRoot (/data). "" creates a temporary
	// directory that Close removes. An existing directory must be empty or have been created by
	// fakemedia (it then gets reset); anything else is refused.
	Dir string
	// Scenario to serve; nil = Default().
	Scenario *Scenario
	// Addrs maps a server name (ServerPlex or an instance name) to its listen address;
	// missing entries listen on 127.0.0.1:0.
	Addrs map[string]string
	// Now overrides the clock (addedAt values are relative to it).
	Now func() time.Time
	// Logf, when set, receives one line per request with secrets redacted. It is called from the
	// servers' goroutines and must be safe for concurrent use (log.Logger.Printf is).
	Logf func(format string, args ...any)
	// LenientAccept serves Plex JSON even when the request lacks Accept: application/json (the
	// real server would answer XML; by default the fake answers 406 so client bugs surface).
	LenientAccept bool
	// DiscImageScanner makes every movie library use the legacy "Plex Movie Scanner with Disc Image
	// Support" (ScannerMovieDiscImage): full-disc backups become Plex versions with one Part per
	// BDMV/STREAM clip (see Disc). The default is Plex's own scanner, which skips discs.
	DiscImageScanner bool
}

// Server is one running fake server.
type Server struct {
	Name         string // ServerPlex or the instance name
	Kind         string // "plex", KindRadarr or KindSonarr
	InstanceName string // *arr instanceName / Plex friendly name
	URL          string // base URL (including the *arr URL base)
	Addr         string // host:port
	APIKey       string // *arr API key or Plex token
	hs           *http.Server
	ln           net.Listener
}

// Env is a running fake media stack. All methods are safe for concurrent use.
type Env struct {
	Scenario  string
	Dir       string // local directory ↔ RemoteRoot (/data)
	MediaRoot string // local directory ↔ RemoteMediaRoot (/data/media)

	Plex              *Server
	PlexToken         string
	MachineIdentifier string

	// Radarr, Radarr4K and Sonarr are the servers of the standard instance names (nil when the
	// scenario has no such instance); Instances holds every *arr server by instance name.
	Radarr, Radarr4K, Sonarr                   *Server
	RadarrAPIKey, Radarr4KAPIKey, SonarrAPIKey string
	Instances                                  map[string]*Server

	w         *world
	servers   []*Server
	removeDir bool
	logf      func(format string, args ...any)
	lenient   bool

	inflight  atomic.Int64 // requests currently being served (for Close)
	closeOnce sync.Once
	closeErr  error
}

// Start runs the scenario (nil = Default()) in t.TempDir() on loopback ports and stops it when the
// test ends. It fails the test on error.
func Start(t TB, sc *Scenario) *Env {
	t.Helper()
	return StartWithOptions(t, Options{Scenario: sc})
}

// StartWithOptions is Start with options; opts.Dir defaults to t.TempDir().
func StartWithOptions(t TB, opts Options) *Env {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	e, err := New(opts)
	if err != nil {
		t.Fatalf("fakemedia: %v", err)
		return nil
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Errorf("fakemedia: close: %v", err)
		}
	})
	return e
}

// New builds the scenario's files and starts the servers. Call Close when done.
func New(opts Options) (*Env, error) {
	sc := opts.Scenario
	if sc == nil {
		sc = Default()
	}
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	dir, removeDir := opts.Dir, false
	if dir == "" {
		d, err := os.MkdirTemp("", "fakemedia-*")
		if err != nil {
			return nil, fmt.Errorf("fakemedia: create temp dir: %w", err)
		}
		dir, removeDir = d, true
	}
	cleanupDir := func() {
		if removeDir {
			_ = os.RemoveAll(dir)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		cleanupDir()
		return nil, fmt.Errorf("fakemedia: resolve data dir: %w", err)
	}
	if err := prepareDir(abs); err != nil {
		cleanupDir()
		return nil, fmt.Errorf("fakemedia: %w", err)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	w, err := buildWorld(sc, abs, now, opts.DiscImageScanner)
	if err != nil {
		cleanupDir()
		return nil, err
	}
	if err := w.materialize(); err != nil {
		cleanupDir()
		return nil, fmt.Errorf("fakemedia: %w", err)
	}
	e := &Env{
		Scenario:          sc.Name,
		Dir:               abs,
		MediaRoot:         filepath.Join(abs, "media"),
		PlexToken:         w.plex.token,
		MachineIdentifier: w.plex.machineID,
		Instances:         map[string]*Server{},
		w:                 w,
		removeDir:         removeDir,
		logf:              opts.Logf,
		lenient:           opts.LenientAccept,
	}
	fail := func(err error) (*Env, error) {
		_ = e.Close()
		return nil, err
	}
	ps, err := e.listen(opts.Addrs[ServerPlex], ServerPlex, "plex", w.plex.friendlyName, w.plex.token, "", e.plexHandler())
	if err != nil {
		return fail(err)
	}
	e.Plex = ps
	for _, name := range w.arrOrder {
		a := w.arrs[name]
		s, err := e.listen(opts.Addrs[name], name, a.kind, a.instanceName, a.apiKey, a.urlBase, e.arrHandler(a))
		if err != nil {
			return fail(err)
		}
		e.Instances[name] = s
		switch name {
		case InstanceRadarr:
			e.Radarr, e.RadarrAPIKey = s, a.apiKey
		case InstanceRadarr4K:
			e.Radarr4K, e.Radarr4KAPIKey = s, a.apiKey
		case InstanceSonarr:
			e.Sonarr, e.SonarrAPIKey = s, a.apiKey
		}
	}
	return e, nil
}

func (e *Env) listen(addr, name, kind, instanceName, key, urlBase string, h http.Handler) (*Server, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fakemedia: listen %s on %s: %w", name, addr, err)
	}
	hs := &http.Server{
		Handler:           e.wrap(name, h),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = hs.Serve(ln) }()
	host, port := "127.0.0.1", "0"
	if ta, ok := ln.Addr().(*net.TCPAddr); ok {
		port = strconv.Itoa(ta.Port)
		if !ta.IP.IsUnspecified() {
			host = ta.IP.String()
		}
	}
	hp := net.JoinHostPort(host, port)
	s := &Server{
		Name: name, Kind: kind, InstanceName: instanceName, URL: "http://" + hp + urlBase, Addr: hp,
		APIKey: key, hs: hs, ln: ln,
	}
	e.servers = append(e.servers, s)
	return s, nil
}

// closeGrace bounds how long Close waits for in-flight requests.
const closeGrace = 5 * time.Second

// Close stops the servers and removes the data directory when New created it. It stops accepting
// connections, waits up to 5s for in-flight requests, then closes every connection. (Plain
// http.Server.Shutdown is not used: it also waits up to 5s for connections that never sent a
// request — HTTP clients dial those speculatively under concurrency.) Calling Close more than once
// is harmless.
func (e *Env) Close() error {
	e.closeOnce.Do(func() {
		var errs []error
		for _, s := range e.servers {
			s.hs.SetKeepAlivesEnabled(false) // closes idle keep-alive connections
			if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("close %s listener: %w", s.Name, err))
			}
		}
		deadline := time.Now().Add(closeGrace)
		for e.inflight.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := e.inflight.Load(); n > 0 {
			errs = append(errs, fmt.Errorf("%d request(s) still running after %s", n, closeGrace))
		}
		for _, s := range e.servers {
			if err := s.hs.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf("close %s: %w", s.Name, err))
			}
		}
		if e.removeDir && e.Dir != "" {
			if err := os.RemoveAll(e.Dir); err != nil {
				errs = append(errs, fmt.Errorf("remove data dir: %w", err))
			}
		}
		e.closeErr = errors.Join(errs...)
	})
	return e.closeErr
}

// Server returns the server named name (ServerPlex or an instance name), or nil.
func (e *Env) Server(name string) *Server {
	if name == ServerPlex {
		return e.Plex
	}
	return e.Instances[name]
}

// ---------------------------------------------------------------------------
// Request log, faults, violations
// ---------------------------------------------------------------------------

// Request is one request received by a fake server.
type Request struct {
	Server string // ServerPlex or the instance name
	Method string
	Path   string // as received (including an *arr URL base)
	Query  url.Values
	Header http.Header // as received, credentials included (the fake's own test secrets)
	Body   string      // at most 1 MiB
	Status int
	Time   time.Time
}

// Fault makes matching requests fail (checked before routing and auth).
type Fault struct {
	Server      string // ServerPlex or instance name; "" = any
	Method      string // "" = any
	PathPrefix  string // "" = any
	Status      int    // default 500
	Body        string
	ContentType string // default text/plain
	Times       int    // 0 = until ClearFaults
	used        int
}

// Violation is a request Dupearr must never make (see the Rule* constants).
type Violation struct {
	Server, Method, Path string
	Rule                 string
	Detail               string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s %s %s: %s (%s)", v.Server, v.Method, v.Path, v.Rule, v.Detail)
}

// Violation rules.
const (
	RulePlexProxyParam       = "plex_delete_proxy_param"
	RulePlexItemDelete       = "plex_whole_item_delete"
	RulePlexIDList           = "plex_delete_id_list"
	RulePlexSectionDelete    = "plex_section_delete"
	RulePlexEmptyTrash       = "plex_empty_trash"
	RulePlexMergeSplit       = "plex_merge_split"
	RulePlexBulkEdit         = "plex_bulk_edit"
	RulePlexElementDelete    = "plex_element_delete"
	RulePlexMalformedDelete  = "plex_malformed_delete"
	RulePlexRefreshForce     = "plex_refresh_force"
	RuleArrAPIKeyQuery       = "arr_apikey_query_param"
	RuleArrBulkDelete        = "arr_bulk_file_delete"
	RuleArrRescanAll         = "arr_rescan_without_item_id"
	RuleArrRefreshAll        = "arr_refresh_all_items"
	RuleArrItemDelete        = "arr_item_delete"
	RuleArrRootFolderPath    = "arr_editor_root_folder_path"
	RuleArrMoveFiles         = "arr_move_files"
	RuleArrUnknownCommand    = "arr_unknown_command"
	RuleArrIncompleteItemPut = "arr_incomplete_item_put"

	// RulePlexScanOutsideSection: a section scan (…/refresh?path=) for a path outside the section's
	// locations — usually a missing Plex path mapping.
	RulePlexScanOutsideSection = "plex_scan_path_outside_section"
	// RuleArrMalformedDelete: DELETE /moviefile|/episodefile with an empty, non-numeric or
	// non-positive id (or extra path segments).
	RuleArrMalformedDelete = "arr_malformed_delete"

	// Safety-invariant rules (docs/ARCHITECTURE.md §6, docs/DECISIONS.md D2/D4/D6), checked on
	// every Plex media delete and every *arr file delete that removes a file from disk.

	// RulePlexDeleteOptimized: a Plex Optimized Version (proxyType 42 or a "Plex Versions" part)
	// was deleted; optimized versions are never touched.
	RulePlexDeleteOptimized = "plex_delete_optimized_version"
	// RulePlexDeleteSameFile: the deleted media shares a file path with another version of the same
	// item (Plex DB glitch, docs/research/plex-api.md §7.10) — the delete destroyed the file that
	// version points to.
	RulePlexDeleteSameFile = "plex_delete_same_file"
	// RulePlexDeleteLastVersion: after a Plex media delete that removed files, the item has no
	// available (all parts on disk) non-optimized version left — the keeper was missing, or it was
	// the item's last version (docs/research/plex-api.md §9.4/§9.5 step 7c).
	RulePlexDeleteLastVersion = "plex_delete_last_version"
	// RuleDeleteLastCopy: a removed file was shared with another item (multi-episode file) or the
	// item's other versions were unavailable, and an item that could be played before the removal
	// no longer has any available non-optimized version.
	RuleDeleteLastCopy = "delete_last_available_copy"
	// RuleDeleteWhilePlaying: a file of an item reported by /status/sessions was removed
	// (Dupearr defers playing rating keys, docs/DECISIONS.md D2/D6).
	RuleDeleteWhilePlaying = "delete_while_playing"
	// RuleDeleteKeepTagged: a removed file is tracked by an *arr movie/series carrying the keep tag
	// (DefaultKeepTag unless changed with Env.SetKeepTag) — protected versions are always kept.
	RuleDeleteKeepTagged = "delete_keep_tagged"
	// RuleDeleteDiscMember: a Plex media delete or *arr file delete targeted an existing file inside
	// a disc structure (BDMV/, VIDEO_TS/, … or a declared disc's owned entries): a per-file delete
	// corrupts the disc; only Dupearr's whole-disc filesystem removal may move it
	// (docs/research/disc-structures.md §6.6/§6.7). Deleting a stale entry whose files are all gone
	// is allowed.
	RuleDeleteDiscMember = "delete_disc_member"
	// RuleDeleteDiscImage: a Plex media delete or *arr file delete targeted a disc image of the
	// scenario (a Disc of kind DiscISO): discs are removed only by Dupearr's filesystem method.
	RuleDeleteDiscImage = "delete_disc_image"
	// RuleDeleteLooseClip: a Plex media delete or *arr file delete targeted an existing file named
	// like a disc clip (a numbered .m2ts/.mts/.m2t such as 00174.m2ts or 00004.1.m2ts) outside a disc
	// structure — a clip of a flattened disc backup (see ClipSet), whatever folder it lies in: a
	// movie can span several clips, so the clips of a folder are one copy and a clip is never
	// removed on its own (loose DVD files are RuleDeleteDiscMember). Env.ClipDeletes also lists
	// deletes of stale clip entries.
	RuleDeleteLooseClip = "delete_loose_clip"
)

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

// wrap adds the request log and fault injection around a server handler.
func (e *Env) wrap(server string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		e.inflight.Add(1)
		defer e.inflight.Add(-1)
		started := time.Now()
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
			if err != nil {
				http.Error(rw, "read body", http.StatusBadRequest)
				return
			}
			if len(b) > maxRequestBody {
				http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			body = b
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		rec := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}
		if f := e.w.takeFault(server, r); f != nil {
			ct := f.ContentType
			if ct == "" {
				ct = "text/plain; charset=utf-8"
			}
			rec.Header().Set("Content-Type", ct)
			rec.WriteHeader(f.Status)
			_, _ = io.WriteString(rec, f.Body)
		} else {
			next.ServeHTTP(rec, r)
		}
		req := Request{
			Server: server, Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
			Header: r.Header.Clone(), Body: string(body), Status: rec.status, Time: started,
		}
		e.w.mu.Lock()
		e.w.requests = append(e.w.requests, req)
		// Trim in batches (keeping the newest maxRequests) so a long-running CLI does not copy the
		// whole log on every request once the cap is reached.
		if n := len(e.w.requests); n > maxRequests+maxRequests/10 {
			e.w.requests = append([]Request(nil), e.w.requests[n-maxRequests:]...)
		}
		e.w.mu.Unlock()
		if e.logf != nil {
			e.logf("%-9s %-6s %s -> %d (%s)", server, r.Method, redactURL(r.URL), rec.status, time.Since(started).Round(time.Microsecond))
		}
	})
}

// redactURL renders path?query (keys sorted) with secret query values masked.
func redactURL(u *url.URL) string {
	if u.RawQuery == "" {
		return u.Path
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range q[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			switch strings.ToLower(k) {
			case "x-plex-token", "apikey", "api_key", "x-api-key", "token", "access_token":
				b.WriteString("********")
			default:
				b.WriteString(url.QueryEscape(v))
			}
		}
	}
	return u.Path + "?" + b.String()
}

func (w *world) takeFault(server string, r *http.Request) *Fault {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, f := range w.faults {
		if (f.Server != "" && f.Server != server) || (f.Method != "" && !strings.EqualFold(f.Method, r.Method)) ||
			!strings.HasPrefix(r.URL.Path, f.PathPrefix) {
			continue
		}
		f.used++
		cp := *f
		if f.Times > 0 && f.used >= f.Times {
			w.faults = append(w.faults[:i], w.faults[i+1:]...)
		}
		return &cp
	}
	return nil
}

// violate records a violation; callers hold w.mu.
func (w *world) violate(server string, r *http.Request, rule, detail string) {
	w.violations = append(w.violations, Violation{Server: server, Method: r.Method, Path: r.URL.Path, Rule: rule, Detail: detail})
}

// Requests returns a copy of the request log (oldest first).
func (e *Env) Requests() []Request {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return append([]Request(nil), e.w.requests...)
}

// RequestsTo returns the logged requests of one server.
func (e *Env) RequestsTo(server string) []Request {
	var out []Request
	for _, r := range e.Requests() {
		if r.Server == server {
			out = append(out, r)
		}
	}
	return out
}

// ResetRequests clears the request log (violations are kept).
func (e *Env) ResetRequests() {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.requests = nil
}

// Violations returns the forbidden calls received so far.
func (e *Env) Violations() []Violation {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return append([]Violation(nil), e.w.violations...)
}

// AssertNoViolations fails the test when any forbidden call was received.
func (e *Env) AssertNoViolations(t TB) {
	t.Helper()
	for _, v := range e.Violations() {
		t.Errorf("fakemedia: forbidden request: %s", v)
	}
}

// InjectFault adds a fault (see Fault).
func (e *Env) InjectFault(f Fault) {
	if f.Status == 0 {
		f.Status = http.StatusInternalServerError
	}
	f.used = 0
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.faults = append(e.w.faults, &f)
}

// ClearFaults removes every injected fault.
func (e *Env) ClearFaults() {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.faults = nil
}

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

// SetAllowDeletion toggles Plex's "Allow media deletion".
func (e *Env) SetAllowDeletion(allow bool) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.plex.allowDeletion = allow
}

// SetKeepTag sets the *arr tag label whose tracked files must never be removed (checked as
// RuleDeleteKeepTagged; the default is DefaultKeepTag, "" disables the check — e.g. for a test that
// configures Dupearr without the arr_tag protection).
func (e *Env) SetKeepTag(label string) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.keepTag = strings.TrimSpace(label)
}

// SetAutoEmptyTrash toggles Plex's "Empty trash automatically after every scan".
func (e *Env) SetAutoEmptyTrash(on bool) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.plex.autoEmptyTrash = on
}

// SetPlaying replaces the items reported by /status/sessions (rating keys of movies/episodes).
func (e *Env) SetPlaying(ratingKeys ...string) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.plex.playing = append([]string(nil), ratingKeys...)
}

// SetPlexPageLimit caps the number of items Plex returns per listing page regardless of the
// requested X-Plex-Container-Size (0 = no cap), to exercise "advance by the returned size".
func (e *Env) SetPlexPageLimit(n int) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.plex.pageLimit = max(n, 0)
}

func (e *Env) arr(name string) (*arrState, error) {
	a := e.w.arrs[name]
	if a == nil {
		return nil, fmt.Errorf("fakemedia: unknown instance %q", name)
	}
	return a, nil
}

// SetStartingUp makes an *arr answer 503 "starting up" to everything (like StartingUpMiddleware).
func (e *Env) SetStartingUp(instance string, starting bool) error {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a, err := e.arr(instance)
	if err != nil {
		return err
	}
	a.startingUp = starting
	return nil
}

// SetRecycleBin sets an *arr's recycle bin (a remote path such as /data/recycle/radarr; "" makes
// deletes permanent).
func (e *Env) SetRecycleBin(instance, remotePath string) error {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a, err := e.arr(instance)
	if err != nil {
		return err
	}
	a.recycleBin = remotePath
	return nil
}

// SetAutoUnmonitor toggles "Unmonitor Deleted Movies/Episodes".
func (e *Env) SetAutoUnmonitor(instance string, on bool) error {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a, err := e.arr(instance)
	if err != nil {
		return err
	}
	a.autoUnmonitor = on
	return nil
}

// QueueItem is a download-queue entry to add with AddQueueItem.
type QueueItem struct {
	TmdbID  int // Radarr: the movie
	TvdbID  int // Sonarr: the series
	Season  int // Sonarr: optional episode
	Episode int
	Title   string // release title ("" = generated)
	// State is trackedDownloadState: downloading (default), importBlocked, importPending,
	// importing, imported, failedPending, failed, ignored.
	State string
}

// AddQueueItem adds a download-queue entry for a movie (Radarr) or series/episode (Sonarr) and
// returns its queue id.
func (e *Env) AddQueueItem(instance string, q QueueItem) (int64, error) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a, err := e.arr(instance)
	if err != nil {
		return 0, err
	}
	qi := &arrQueueItem{
		id: a.nextQueueID, state: q.State, status: "downloading", protocol: "torrent",
		client: "qBittorrent", indexer: "FakeIndexer", added: e.w.now().UTC().Truncate(time.Second),
		size: GiB(12), sizeLeft: GiB(3),
	}
	if qi.state == "" {
		qi.state = "downloading"
	}
	switch qi.state {
	case "importPending", "importing", "importBlocked", "imported":
		qi.status, qi.sizeLeft = "completed", 0
	case "failedPending", "failed":
		qi.status = "failed"
	}
	qi.downloadID = strings.ToUpper(hashHex(40, instance, strconv.FormatInt(qi.id, 10)))
	if a.isRadarr() {
		for _, m := range a.movies {
			if m.tmdbID == q.TmdbID {
				qi.movieID = m.id
				qi.title = fmt.Sprintf("%s.%d.2160p.UHD.BluRay.x265-FAKE", strings.ReplaceAll(m.title, " ", "."), m.year)
			}
		}
		if qi.movieID == 0 {
			return 0, fmt.Errorf("fakemedia: %s has no movie with tmdb id %d", instance, q.TmdbID)
		}
		qi.quality, _ = qualityByName(a.kind, "Remux-2160p")
	} else {
		for _, s := range a.series {
			if s.tvdbID == q.TvdbID {
				qi.seriesID = s.id
				qi.title = fmt.Sprintf("%s.S%02dE%02d.1080p.WEB-DL.x264-FAKE", strings.ReplaceAll(s.title, " ", "."), q.Season, q.Episode)
			}
		}
		if qi.seriesID == 0 {
			return 0, fmt.Errorf("fakemedia: %s has no series with tvdb id %d", instance, q.TvdbID)
		}
		qi.season = q.Season
		for _, ep := range a.episodesOf(qi.seriesID) {
			if ep.season == q.Season && ep.number == q.Episode {
				qi.episodeID = ep.id
			}
		}
		qi.quality, _ = qualityByName(a.kind, "WEBDL-1080p")
	}
	if q.Title != "" {
		qi.title = q.Title
	}
	a.nextQueueID++
	a.queue = append(a.queue, qi)
	return qi.id, nil
}

// ClearQueue empties an *arr's download queue.
func (e *Env) ClearQueue(instance string) error {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a, err := e.arr(instance)
	if err != nil {
		return err
	}
	a.queue = nil
	return nil
}

// Unmount simulates an unmounted share: the local directory rel (relative to the media root,
// e.g. "movies") is moved aside until Remount. Plex then reports its files as missing and *arr
// deletes answer 409.
func (e *Env) Unmount(rel string) error {
	if !validRel(rel) {
		return fmt.Errorf("fakemedia: invalid relative path %q", rel)
	}
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	aside := filepath.Join(e.Dir, ".unmounted", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(aside), 0o755); err != nil {
		return fmt.Errorf("fakemedia: unmount: %w", err)
	}
	if err := os.Rename(e.w.local(rel), aside); err != nil {
		return fmt.Errorf("fakemedia: unmount: %w", err)
	}
	return nil
}

// Remount undoes Unmount.
func (e *Env) Remount(rel string) error {
	if !validRel(rel) {
		return fmt.Errorf("fakemedia: invalid relative path %q", rel)
	}
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	aside := filepath.Join(e.Dir, ".unmounted", filepath.FromSlash(rel))
	if err := os.Rename(aside, e.w.local(rel)); err != nil {
		return fmt.Errorf("fakemedia: remount: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Paths and lookups
// ---------------------------------------------------------------------------

// Mapping is a remote → local path prefix to configure in Dupearr.
type Mapping struct {
	Server string // ServerPlex or instance name
	Remote string
	Local  string
}

// PathMappings returns the path mappings Dupearr needs for every fake server (all of them see
// RemoteMediaRoot, which lives at MediaRoot locally).
func (e *Env) PathMappings() []Mapping {
	out := []Mapping{{Server: ServerPlex, Remote: RemoteMediaRoot, Local: e.MediaRoot}}
	for _, name := range e.w.arrOrder {
		out = append(out, Mapping{Server: name, Remote: RemoteMediaRoot, Local: e.MediaRoot})
	}
	return out
}

// LocalPath maps a remote path (under /data) to the local file system.
func (e *Env) LocalPath(remotePath string) (string, bool) { return e.w.remoteToLocal(remotePath) }

// RemotePath maps a local path inside Dir to the servers' view.
func (e *Env) RemotePath(localPath string) (string, bool) { return e.w.localToRemote(localPath) }

// RemoteMediaPath returns the remote path of a media-root relative path.
func (e *Env) RemoteMediaPath(rel string) string { return remote(rel) }

// FileExists reports whether the file behind a remote path exists locally.
func (e *Env) FileExists(remotePath string) bool {
	lp, ok := e.w.remoteToLocal(remotePath)
	if !ok {
		return false
	}
	fi, err := os.Stat(lp)
	return err == nil && fi.Mode().IsRegular()
}

// RatingKey returns the rating key of the movie or show titled title in section ("" = any
// section), or "" when absent.
func (e *Env) RatingKey(section, title string) string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	for _, it := range e.w.plex.order {
		if (it.typ == "movie" || it.typ == "show") && it.title == title && (section == "" || it.sec.key == section) {
			return it.rk
		}
	}
	return ""
}

// EpisodeRatingKey returns the rating key of an episode, or "".
func (e *Env) EpisodeRatingKey(show string, season, episode int) string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	for _, it := range e.w.plex.order {
		if it.typ == "episode" && it.index == episode && it.parent != nil && it.parent.index == season &&
			it.parent.parent != nil && it.parent.parent.title == show {
			return it.rk
		}
	}
	return ""
}

// MediaIDs returns the Plex media ids of an item (in order).
func (e *Env) MediaIDs(ratingKey string) []int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	it := e.w.plex.items[ratingKey]
	if it == nil {
		return nil
	}
	ids := make([]int64, 0, len(it.media))
	for _, m := range it.media {
		ids = append(ids, m.id)
	}
	return ids
}

// MediaIDForFile returns the id of the item's media whose (first) part path contains substr, or 0.
func (e *Env) MediaIDForFile(ratingKey, substr string) int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	it := e.w.plex.items[ratingKey]
	if it == nil {
		return 0
	}
	for _, m := range it.media {
		for _, p := range m.parts {
			if strings.Contains(p.rel, substr) {
				return m.id
			}
		}
	}
	return 0
}

// ArrMovieID returns the Radarr movie id for a tmdb id, or 0.
func (e *Env) ArrMovieID(instance string, tmdbID int) int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if a := e.w.arrs[instance]; a != nil {
		for _, m := range a.movies {
			if m.tmdbID == tmdbID {
				return m.id
			}
		}
	}
	return 0
}

// ArrMovieFileID returns the movie's currently tracked file id, or 0.
func (e *Env) ArrMovieFileID(instance string, tmdbID int) int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if a := e.w.arrs[instance]; a != nil {
		for _, m := range a.movies {
			if m.tmdbID == tmdbID {
				return m.fileID
			}
		}
	}
	return 0
}

// ArrMovieFilePath returns the remote path of the movie's tracked file, or "".
func (e *Env) ArrMovieFilePath(instance string, tmdbID int) string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if a := e.w.arrs[instance]; a != nil {
		for _, m := range a.movies {
			if f := a.files[m.fileID]; m.tmdbID == tmdbID && f != nil {
				return remote(f.rel)
			}
		}
	}
	return ""
}

// ArrSeriesID returns the Sonarr series id for a tvdb id, or 0.
func (e *Env) ArrSeriesID(instance string, tvdbID int) int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if a := e.w.arrs[instance]; a != nil {
		for _, s := range a.series {
			if s.tvdbID == tvdbID {
				return s.id
			}
		}
	}
	return 0
}

// ArrEpisodeFileID returns the file id linked to an episode, or 0.
func (e *Env) ArrEpisodeFileID(instance string, tvdbID, season, episode int) int64 {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	a := e.w.arrs[instance]
	if a == nil {
		return 0
	}
	for _, s := range a.series {
		if s.tvdbID != tvdbID {
			continue
		}
		for _, ep := range a.episodesOf(s.id) {
			if ep.season == season && ep.number == episode {
				return ep.fileID
			}
		}
	}
	return 0
}

// Describe writes a human-readable summary (URLs, credentials, path mappings, content).
func (e *Env) Describe(out io.Writer) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	p := e.w.plex
	var movies, episodes, dupes int
	for _, it := range p.order {
		switch it.typ {
		case "movie":
			movies++
		case "episode":
			episodes++
		}
		if len(it.media) > 1 {
			dupes++
		}
	}
	fmt.Fprintf(out, "fakemedia scenario %q: %d movies, %d episodes (%d items with several versions)\n", e.Scenario, movies, episodes, dupes)
	fmt.Fprintf(out, "data dir: %s (servers see it as %s)\n\n", e.Dir, RemoteRoot)
	fmt.Fprintf(out, "Plex      %-28s token=%s machineIdentifier=%s allowMediaDeletion=%t\n", e.Plex.URL, p.token, p.machineID, p.allowDeletion)
	for _, s := range p.sections {
		locs := make([]string, 0, len(s.dirs))
		for _, d := range s.dirs {
			locs = append(locs, remote(d))
		}
		scanner := ""
		if s.scanner == ScannerMovieDiscImage || s.scanner == ScannerSeriesDiscImage {
			scanner = fmt.Sprintf(" scanner=%q", s.scanner)
		}
		fmt.Fprintf(out, "          library %s %-10q (%s) %s%s\n", s.key, s.title, s.typ, strings.Join(locs, ", "), scanner)
	}
	names := append([]string(nil), e.w.arrOrder...)
	sort.Strings(names)
	for _, name := range names {
		a := e.w.arrs[name]
		s := e.Instances[name]
		bin := a.recycleBin
		if bin == "" {
			bin = "(none: deletes are permanent)"
		}
		fmt.Fprintf(out, "%-9s %-28s apiKey=%s instanceName=%q recycleBin=%s\n", name, s.URL, a.apiKey, a.instanceName, bin)
	}
	fmt.Fprintf(out, "\nPath mappings to configure in Dupearr (remote → local):\n")
	for _, m := range e.PathMappings() {
		fmt.Fprintf(out, "  %-9s %s → %s\n", m.Server, m.Remote, m.Local)
	}
	if len(e.w.discs) > 0 {
		fmt.Fprintf(out, "\nFull-disc backups (%d):\n", len(e.w.discs))
		for _, d := range e.w.discs {
			var notes []string
			if d.plexVisible {
				notes = append(notes, fmt.Sprintf("Plex version with %d part(s)", len(d.plexVersion().Parts)))
			} else {
				notes = append(notes, "hidden from Plex")
			}
			if d.setSize > 1 {
				notes = append(notes, fmt.Sprintf("disc %d of %d", d.setNumber, d.setSize))
			}
			if d.extras {
				notes = append(notes, "extras disc")
			}
			if !d.readable && d.d.Kind != DiscISO {
				notes = append(notes, "damaged")
			}
			if d.d.Tracked != "" {
				notes = append(notes, fmt.Sprintf("%s tracks %s", d.d.Tracked, strings.TrimPrefix(d.trackedRel, d.folder+"/")))
			}
			fmt.Fprintf(out, "  %-10s %s (%d files, %.1f GiB; %s)\n", d.d.Kind, remote(d.d.Root), len(d.files),
				float64(d.totalSize())/(1<<30), strings.Join(notes, ", "))
		}
	}
	e.w.describeClipSets(func(format string, args ...any) { fmt.Fprintf(out, format, args...) })
}
