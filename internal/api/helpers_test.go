package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/commands"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// discard is a logger that drops everything.
var discard = slog.New(slog.DiscardHandler)

const testRemote = "203.0.113.9:40000" // a non-local client

// testIndex is the web UI shell used by the SPA tests.
const testIndex = `<!doctype html><html><head><!--DUPEARR_HEAD--><title>Dupearr</title></head><body></body></html>`

type testServer struct {
	t     *testing.T
	dir   string
	cfg   *config.Manager
	db    *database.DB
	bus   *events.Bus
	auth  *auth.Service
	cmds  *commands.Manager
	srv   *Server
	h     http.Handler
	key   string
	webFS fstest.MapFS

	scanner  *fakeScanner
	executor *fakeExecutor
	backups  *fakeBackups
	plex     *fakePlex
	arr      *fakeArr
	plexTV   *httptest.Server
	// plexTVOwner scripts what the fake plex.tv says about fakePlexToken and fakePlexMachineID:
	// plexTVUnknownToken (401, the default), plexTVOwned, plexTVShared or plexTVNotListed.
	plexTVOwner atomic.Int32

	restarts atomic.Int32
}

// plexTVOwner values.
const (
	plexTVUnknownToken = iota
	plexTVOwned
	plexTVShared
	plexTVNotListed
)

type serverOpts struct {
	configure  func(c *config.Config)
	noScanner  bool
	noExecutor bool
	noWeb      bool
}

func newTestServer(t *testing.T, opts ...func(*serverOpts)) *testServer {
	t.Helper()
	var o serverOpts
	for _, f := range opts {
		f(&o)
	}
	ctx := context.Background()
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if o.configure != nil {
		if _, err := cfg.Update(o.configure); err != nil {
			t.Fatalf("config.Update: %v", err)
		}
	}
	db, err := database.Open(ctx, filepath.Join(dir, "dupearr.db"), discard)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	bus := events.New()
	cmds := commands.New(db, bus, discard)
	for _, name := range []string{models.CmdDuplicateScan, models.CmdTargetedScan, models.CmdProcessQueue,
		models.CmdSyncLibraries, models.CmdCheckHealth, models.CmdBackup, models.CmdHousekeeping, models.CmdCleanRecycleBin} {
		cmds.Register(name, func(context.Context, *models.Command, func(string)) (string, error) { return "ok", nil }, true)
	}
	authSvc, err := auth.New(cfg, db, discard)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	ts := &testServer{
		t: t, dir: dir, cfg: cfg, db: db, bus: bus, auth: authSvc, cmds: cmds,
		key:      cfg.Get().ApiKey,
		scanner:  newFakeScanner(db),
		executor: newFakeExecutor(db),
		backups:  &fakeBackups{},
		plex:     newFakePlex(t),
		arr:      newFakeArr(t),
	}
	ts.plexTV = newFakePlexTV(t, &ts.plexTVOwner)
	if !o.noWeb {
		ts.webFS = fstest.MapFS{
			"index.html":          {Data: []byte(testIndex)},
			"assets/index-abc.js": {Data: []byte("console.log('dupearr')")},
			"favicon.svg":         {Data: []byte("<svg/>")},
			".gitkeep":            {Data: []byte("")},
		}
	}
	d := Deps{
		Config:   cfg,
		Store:    db,
		Bus:      bus,
		Log:      discard,
		Auth:     authSvc,
		Commands: cmds,
		Health:   health.New(health.Deps{Store: db, Config: cfg, Bus: bus, Log: discard}),
		Notifier: notifications.New(db, discard),
		PlexOpts: plex.Options{ClientIdentifier: "dupearr-test", Product: "Dupearr", Version: "test",
			Timeout: 5 * time.Second, PlexTVURL: ts.plexTV.URL, ClientsPlexTVURL: ts.plexTV.URL},
		PlexFactory: func(s models.MediaServer) *plex.Client {
			return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-test", Timeout: 5 * time.Second})
		},
		ArrFactory: func(a models.ArrInstance) *arr.Client {
			return arr.New(a, arr.Options{Timeout: 5 * time.Second})
		},
		StartTime: time.Now().Add(-time.Minute),
		Restart:   func() { ts.restarts.Add(1) },
	}
	if ts.webFS != nil {
		d.WebFS = ts.webFS
	}
	ts.srv = New(d)
	if !o.noScanner {
		ts.srv.scanner = ts.scanner
	}
	if !o.noExecutor {
		ts.srv.executor = ts.executor
	}
	ts.srv.backups = ts.backups
	ts.srv.isDocker = func() bool { return true }
	ts.h = ts.srv.Handler()
	return ts
}

// request builds and serves a request. body may be nil, a string (sent as is) or any value
// (JSON-encoded). By default the API key header is set; pass noKey to omit it.
type reqOption func(*http.Request)

var noKey reqOption = func(r *http.Request) { r.Header.Del("X-Api-Key") }

func withHeader(k, v string) reqOption { return func(r *http.Request) { r.Header.Set(k, v) } }

func withRemote(addr string) reqOption { return func(r *http.Request) { r.RemoteAddr = addr } }

func withCookie(c *http.Cookie) reqOption { return func(r *http.Request) { r.AddCookie(c) } }

func (ts *testServer) do(method, target string, body any, opts ...reqOption) *httptest.ResponseRecorder {
	ts.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			ts.t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, rd)
	req.RemoteAddr = testRemote
	req.Header.Set("X-Api-Key", ts.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rr := httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	return rr
}

// expect asserts the status code and decodes the JSON body into out (when non-nil).
func expect(t *testing.T, rr *httptest.ResponseRecorder, status int, out any) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, status, rr.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %T: %v; body: %s", out, err, rr.Body.String())
		}
	}
}

// validationProps returns the propertyNames of a 400 validation response.
func validationProps(t *testing.T, rr *httptest.ResponseRecorder) []string {
	t.Helper()
	var errs []config.ValidationError
	expect(t, rr, http.StatusBadRequest, &errs)
	props := make([]string, 0, len(errs))
	for _, e := range errs {
		props = append(props, e.PropertyName)
	}
	return props
}

func hasProp(props []string, p string) bool {
	for _, x := range props {
		if x == p {
			return true
		}
	}
	return false
}

// message decodes {"message"}.
func message(t *testing.T, rr *httptest.ResponseRecorder, status int) string {
	t.Helper()
	var m struct {
		Message string `json:"message"`
	}
	expect(t, rr, status, &m)
	return m.Message
}

// assertNoNull fails when the JSON body contains a null (nulls are only allowed for listed keys).
func assertNoNull(t *testing.T, body []byte, allowed ...string) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case nil:
			for _, a := range allowed {
				if strings.HasSuffix(path, "."+a) {
					return
				}
			}
			t.Errorf("null at %s in %s", path, truncate(string(body), 400))
		case map[string]any:
			for k, e := range x {
				walk(path+"."+k, e)
			}
		case []any:
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		}
	}
	walk("$", v)
}

// createUser creates the Forms account directly in the store with a cheap bcrypt hash (the
// production cost of 12 is slow, especially under -race; CheckPassword accepts any cost).
func (ts *testServer) createUser(user, pass string) {
	ts.t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	if err != nil {
		ts.t.Fatal(err)
	}
	if _, err := ts.db.Users().Upsert(context.Background(), user, string(h)); err != nil {
		ts.t.Fatalf("create user: %v", err)
	}
}

func (ts *testServer) login(user, pass string) *http.Cookie {
	ts.t.Helper()
	rr := ts.do(http.MethodPost, "/login", map[string]any{"username": user, "password": pass}, noKey)
	expect(ts.t, rr, http.StatusOK, nil)
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	ts.t.Fatal("login set no cookie")
	return nil
}

// ---------------------------------------------------------------------------
// Seed data
// ---------------------------------------------------------------------------

// seedServer inserts a media server pointing at the fake Plex.
func (ts *testServer) seedServer(name string) models.MediaServer {
	ts.t.Helper()
	ms := models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: ts.plex.srv.URL, Token: fakePlexToken,
		MachineIdentifier: fakePlexMachineID, Enabled: true}
	if err := ts.db.MediaServers().Create(context.Background(), &ms); err != nil {
		ts.t.Fatalf("create server: %v", err)
	}
	return ms
}

func (ts *testServer) seedLibrary(serverID int64, key, title string) models.Library {
	ts.t.Helper()
	libs, err := ts.db.Libraries().Sync(context.Background(), serverID, []models.Library{{SectionKey: key, Title: title, Type: "movie", Locations: []string{"/data/movies"}}})
	if err != nil {
		ts.t.Fatalf("sync libraries: %v", err)
	}
	for _, l := range libs {
		if l.SectionKey == key {
			return l
		}
	}
	ts.t.Fatal("library not created")
	return models.Library{}
}

// mkVersion builds a MediaVersion.
func mkVersion(serverID, mediaID int64, rk, res string, size int64) models.MediaVersion {
	return models.MediaVersion{
		Key:          fmt.Sprintf("plex:%d:%d", serverID, mediaID),
		ServerID:     serverID,
		LibraryID:    1,
		LibraryTitle: "Movies",
		SectionKey:   "1",
		RatingKey:    rk,
		MediaID:      mediaID,
		Parts:        []models.MediaPart{{ID: mediaID * 10, Path: fmt.Sprintf("/data/movies/m%d.mkv", mediaID), Size: size}},
		Resolution:   res,
		VideoCodec:   models.VCodecHEVC,
		DynamicRange: models.DRSDR,
		Container:    "mkv",
		Width:        1920,
		Height:       1080,
		BitrateKbps:  8000,
		DurationMs:   7200000,
	}
}

// seedGroup inserts a pending movie group with a keeper (2160) and a removal (1080).
func (ts *testServer) seedGroup(key, title string, status models.GroupStatus) *models.DuplicateGroup {
	ts.t.Helper()
	keep := mkVersion(1, 100+int64(len(key)), "501", models.Res2160, 40<<30)
	keep.Arr = &models.ArrFileInfo{InstanceID: 1, InstanceName: "Radarr 4K", Kind: models.ArrRadarr, FileID: 7, ItemID: 9}
	remove := mkVersion(1, 200+int64(len(key)), "501", models.Res1080, 10<<30)
	remove.Key = fmt.Sprintf("plex:1:%s-b", key)
	keep.Key = fmt.Sprintf("plex:1:%s-a", key)
	now := time.Now().UTC().Truncate(time.Second)
	g := &models.DuplicateGroup{
		Key: key, MediaType: models.MediaTypeMovie, Title: title, Year: 2020, ServerID: 1,
		LibraryIDs: []int64{1}, ExternalIDs: map[string]string{"tmdb": "1"}, Status: status,
		Flags: []string{}, ProfileID: 1, ReclaimableBytes: 10 << 30,
		FirstSeenAt: now, LastSeenAt: now,
		Files: []models.GroupFile{
			{Version: keep, Decision: models.DecisionKeep, EngineDecision: models.DecisionKeep, Rank: 1, Reasons: []string{"best"}, Values: map[string]string{}},
			{Version: remove, Decision: models.DecisionRemove, EngineDecision: models.DecisionRemove, Rank: 2, Reasons: []string{"lower resolution"}, Values: map[string]string{}},
		},
	}
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		ts.t.Fatalf("upsert group: %v", err)
	}
	if status == models.GroupIgnored {
		if err := ts.db.Groups().UpdateStatus(context.Background(), g.ID, models.GroupIgnored, "Ignored by user"); err != nil {
			ts.t.Fatalf("ignore group: %v", err)
		}
	}
	got, err := ts.db.Groups().Get(context.Background(), g.ID)
	if err != nil {
		ts.t.Fatalf("get group: %v", err)
	}
	return got
}

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeScanner re-evaluates by recomputing effective decisions from overrides (like the engine
// would for these simple groups) and reopening ignored/pending groups.
type fakeScanner struct {
	db *database.DB

	mu          sync.Mutex
	reevalAll   int
	reevaluated []int64
	matchCalls  int
	matched     []int64
	syncErr     error
	reevalErr   error
	synced      []int64
	// blockAll makes ReevaluateAll wait for its context to end (shutdown tests).
	blockAll bool
	started  chan struct{} // receives when a blocking ReevaluateAll starts (buffered)
}

func newFakeScanner(db *database.DB) *fakeScanner { return &fakeScanner{db: db} }

func (f *fakeScanner) SyncLibraries(ctx context.Context, serverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = append(f.synced, serverID)
	if f.syncErr != nil {
		return f.syncErr
	}
	_, err := f.db.Libraries().Sync(ctx, serverID, []models.Library{{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/movies"}}})
	return err
}

func (f *fakeScanner) Reevaluate(ctx context.Context, groupID int64) (*models.DuplicateGroup, error) {
	f.mu.Lock()
	f.reevaluated = append(f.reevaluated, groupID)
	err := f.reevalErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	g, err := f.db.Groups().Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	for i := range g.Files {
		file := &g.Files[i]
		file.Decision = file.EngineDecision
		if file.Override != "" {
			file.Decision = file.Override
		}
	}
	if _, err := f.db.Groups().Upsert(ctx, g); err != nil {
		return nil, err
	}
	return f.db.Groups().Get(ctx, groupID)
}

func (f *fakeScanner) ReevaluateAll(ctx context.Context) error {
	f.mu.Lock()
	f.reevalAll++
	block, started := f.blockAll, f.started
	f.mu.Unlock()
	if !block {
		return nil
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// ReevaluateMatching records the matching groups (open ones) and re-evaluates them like
// Reevaluate.
func (f *fakeScanner) ReevaluateMatching(ctx context.Context, match func(*models.DuplicateGroup) bool) (int, error) {
	f.mu.Lock()
	f.matchCalls++
	f.mu.Unlock()
	page, err := f.db.Groups().List(ctx, store.GroupFilter{}, store.Paging{Page: 1, PageSize: 1000})
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range page.Records {
		g := &page.Records[i]
		if g.Status == models.GroupResolved || g.Status == models.GroupIgnored || !match(g) {
			continue
		}
		n++
		f.mu.Lock()
		f.matched = append(f.matched, g.ID)
		f.mu.Unlock()
	}
	return n, nil
}

// matching returns how often ReevaluateMatching ran and the group ids it matched.
func (f *fakeScanner) matching() (calls int, ids []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.matchCalls, append([]int64(nil), f.matched...)
}

func (f *fakeScanner) counts() (all int, one []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reevalAll, append([]int64(nil), f.reevaluated...)
}

// fakeExecutor creates pending actions for "remove" files (store-checked) and queues the group.
type fakeExecutor struct {
	db *database.DB

	mu         sync.Mutex
	approveErr error
	restoreErr error
	restored   []int64
	// signatures are the signatures ApproveReviewed was called with.
	signatures []string
	// beforeApprove runs when ApproveReviewed starts (e.g. to change the group like a scan would).
	beforeApprove func()
}

func newFakeExecutor(db *database.DB) *fakeExecutor { return &fakeExecutor{db: db} }

// ApproveReviewed refuses a signature that is not the group's current one (like the executor).
func (f *fakeExecutor) ApproveReviewed(ctx context.Context, groupID int64, _, signature string) ([]models.Action, error) {
	f.mu.Lock()
	err, before := f.approveErr, f.beforeApprove
	f.signatures = append(f.signatures, signature)
	f.mu.Unlock()
	if before != nil {
		before()
	}
	if err != nil {
		return nil, err
	}
	g, err := f.db.Groups().Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if signature != "" && signature != g.Signature {
		return nil, fmt.Errorf("approve group %d: %w: the duplicate changed since it was reviewed", groupID, executor.ErrNotApprovable)
	}
	var out []models.Action
	for _, file := range g.Files {
		if file.Decision != models.DecisionRemove || file.Protected {
			continue
		}
		a := models.Action{GroupID: g.ID, GroupFileID: file.ID, VersionKey: file.Version.Key, Title: g.Title,
			Paths: []string{file.Version.Parts[0].Path}, Size: file.Version.TotalSize()}
		if err := f.db.Actions().Create(ctx, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, errNothing
	}
	if err := f.db.Groups().UpdateStatus(ctx, groupID, models.GroupQueued, ""); err != nil {
		return nil, err
	}
	return out, nil
}

var errNothing = fmt.Errorf("fake: %w", executor.ErrNothingToRemove)

func (f *fakeExecutor) Restore(ctx context.Context, actionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, actionID)
	return f.restoreErr
}

// fakeBackups is an in-memory backup service.
type fakeBackups struct {
	mu        sync.Mutex
	list      []backup.Backup
	file      string // path returned by File
	restored  []int64
	uploaded  [][]byte
	uploadErr error

	staged     bool
	confirmed  int
	discarded  int
	confirmErr error
}

func (f *fakeBackups) Create(_ context.Context, kind string) (*backup.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := backup.Backup{ID: int64(len(f.list) + 1), Name: fmt.Sprintf("dupearr_backup_%d.zip", len(f.list)+1), Type: kind, Size: 10, Time: time.Now().UTC()}
	b.Path = "/backup/" + kind + "/" + b.Name
	f.list = append(f.list, b)
	return &b, nil
}

func (f *fakeBackups) List() ([]backup.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]backup.Backup(nil), f.list...), nil
}

func (f *fakeBackups) File(kind, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == "" || kind != "manual" {
		return "", backup.ErrNotFound
	}
	return f.file, nil
}

func (f *fakeBackups) Delete(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.list {
		if b.ID == id {
			f.list = append(f.list[:i], f.list[i+1:]...)
			return nil
		}
	}
	return backup.ErrNotFound
}

func (f *fakeBackups) StageRestore(_ context.Context, id int64, _ backup.RestoreOptions) (*backup.RestoreSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.list {
		if b.ID == id {
			f.restored = append(f.restored, id)
			f.staged = true
			return &backup.RestoreSummary{Changes: []backup.RestoreChange{{Setting: "dryRun", Backup: "false"}}}, nil
		}
	}
	return nil, backup.ErrNotFound
}

func (f *fakeBackups) StageRestoreUpload(_ context.Context, r io.Reader, _ int64, _ backup.RestoreOptions) (*backup.RestoreSummary, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	f.uploaded = append(f.uploaded, data)
	f.staged = true
	return &backup.RestoreSummary{Changes: []backup.RestoreChange{}}, nil
}

func (f *fakeBackups) ConfirmRestore(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confirmErr != nil {
		return f.confirmErr
	}
	if !f.staged {
		return backup.ErrNoStagedRestore
	}
	f.staged = false
	f.confirmed++
	return nil
}

func (f *fakeBackups) DiscardRestore() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged = false
	f.discarded++
	return nil
}

// ---------------------------------------------------------------------------
// Fake upstream servers (loopback only)
// ---------------------------------------------------------------------------

const (
	fakePlexToken     = "plex-token-123"
	fakePlexMachineID = "machine-abc"
	fakeArrKey        = "arrkey0123456789"
)

// fakePlex emulates the Plex endpoints the API uses: GET / (identity + allowMediaDeletion) and
// the photo transcoder.
type fakePlex struct {
	srv       *httptest.Server
	machineID atomic.Value // string
}

func newFakePlex(t *testing.T) *fakePlex {
	f := &fakePlex{}
	f.machineID.Store(fakePlexMachineID)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != fakePlexToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"MediaContainer":{"machineIdentifier":%q,"version":"1.41.0","friendlyName":"Test Plex","allowMediaDeletion":"1"}}`,
				f.machineID.Load().(string))
		case "/photo/:/transcode":
			if !strings.HasPrefix(r.URL.Query().Get("url"), "/library/") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// fakeArr emulates Radarr: system/status and config/mediamanagement.
type fakeArr struct {
	srv *httptest.Server
}

func newFakeArr(t *testing.T) *fakeArr {
	f := &fakeArr{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != fakeArrKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = io.WriteString(w, `{"appName":"Radarr","instanceName":"Radarr 4K","version":"5.9.0"}`)
		case "/api/v3/config/mediamanagement":
			_, _ = io.WriteString(w, `{"recycleBin":"/recycle","recycleBinCleanupDays":7}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newFakePlexTV emulates plex.tv PIN and resource endpoints. owner scripts the resources of
// fakePlexToken (see testServer.plexTVOwner).
func newFakePlexTV(t *testing.T, owner *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/resources" && r.Header.Get("X-Plex-Token") == fakePlexToken {
			switch owner.Load() {
			case plexTVOwned, plexTVShared:
				fmt.Fprintf(w, `[{"name":"Test Plex","clientIdentifier":%q,"provides":"server","owned":%t,"accessToken":"x","connections":[]}]`,
					strings.ToUpper(fakePlexMachineID), owner.Load() == plexTVOwned)
			case plexTVNotListed:
				_, _ = io.WriteString(w, `[{"name":"Other","clientIdentifier":"another-machine","provides":"server","owned":true}]`)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
			_, _ = io.WriteString(w, `{"id":4242,"code":"abcd1234","authToken":null,"expiresIn":900}`)
		case r.URL.Path == "/api/v2/pins/4242":
			_, _ = io.WriteString(w, `{"id":4242,"code":"abcd1234","authToken":"user-token","expiresIn":800}`)
		case r.URL.Path == "/api/v2/pins/4243":
			_, _ = io.WriteString(w, `{"id":4243,"code":"efgh","authToken":null,"expiresIn":800}`)
		case r.URL.Path == "/api/v2/resources":
			if r.Header.Get("X-Plex-Token") != "user-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `[{"name":"Home","clientIdentifier":"machine-abc","productVersion":"1.41","provides":"server","owned":true,"accessToken":"srv-token","connections":[{"uri":"http://10.0.0.5:32400","address":"10.0.0.5","port":32400,"protocol":"http","local":true,"relay":false}]},{"name":"Phone","provides":"player"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
