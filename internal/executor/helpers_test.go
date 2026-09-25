package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// remoteRoot is the media root as the fake Plex and *arr see it; it maps to env.root locally.
const remoteRoot = "/data/media"

// testMachineID is the machineIdentifier of the test media server (stored and answered).
const testMachineID = "0123456789abcdef-test-server"

// callLog is an ordered, shared record of calls made to the fakes (and filesystem snapshots).
type callLog struct {
	mu    sync.Mutex
	calls []string
	// onCall (optional) runs after each call is recorded, before the fake acts: tests use it to
	// change state "while" the executor talks to Plex or an *arr.
	onCall func(call string)
}

func (l *callLog) add(format string, args ...any) {
	c := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.calls = append(l.calls, c)
	hook := l.onCall
	l.mu.Unlock()
	if hook != nil {
		hook(c)
	}
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// mutatingPrefixes are the calls that change media or *arr/Plex state.
var mutatingPrefixes = []string{
	"plex.DeleteMedia", "plex.RefreshItem", "plex.ScanPath",
	"arr.DeleteFile", "arr.Rescan", "arr.Unmonitor", "arr.AddExclusion",
}

func (l *callLog) mutations() []string {
	var out []string
	for _, c := range l.all() {
		for _, p := range mutatingPrefixes {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
			}
		}
	}
	return out
}

func (l *callLog) count(prefix string) int {
	n := 0
	for _, c := range l.all() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (l *callLog) index(prefix string) int {
	for i, c := range l.all() {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// fakePlex emulates the Plex endpoints the executor uses on top of a real directory tree.
type fakePlex struct {
	mu   sync.Mutex
	log  *callLog
	root string // local directory that remoteRoot maps to

	items       map[string]*models.MediaItem
	sessions    map[string]bool
	sessionsErr error
	allowed     bool
	allowedErr  error
	itemErr     map[string]error
	nilItem     map[string]bool // Item answers (nil, nil) for these rating keys
	deleteErr   error
	keepFiles   bool // DeleteMedia removes the entry but leaves the files on disk
	// exists / sizes override what Item reports for a part (by server path).
	exists map[string]bool
	sizes  map[string]int64
	// onItem edits every item Item returns (e.g. to report a part as inaccessible).
	onItem func(it *models.MediaItem)
	// machineID / identityErr are what Identity answers.
	machineID   string
	identityErr error
	// sections / sectionsErr are what Sections answers (several media servers).
	sections    []plex.Section
	sectionsErr error
}

func (p *fakePlex) Identity(context.Context) (*plex.Identity, error) {
	p.log.add("plex.Identity")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.identityErr != nil {
		return nil, p.identityErr
	}
	return &plex.Identity{MachineIdentifier: p.machineID, Version: "1.40.0", FriendlyName: "Fake"}, nil
}

func (p *fakePlex) localOf(remote string) string {
	return filepath.Join(p.root, filepath.FromSlash(strings.TrimPrefix(remote, remoteRoot+"/")))
}

func (p *fakePlex) Item(_ context.Context, rk string) (*models.MediaItem, error) {
	p.log.add("plex.Item %s", rk)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.itemErr[rk]; err != nil {
		return nil, err
	}
	if p.nilItem[rk] {
		return nil, nil
	}
	it, ok := p.items[rk]
	if !ok {
		return nil, fmt.Errorf("item %s: %w", rk, plex.ErrNotFound)
	}
	out := *it
	out.Versions = make([]models.MediaVersion, len(it.Versions))
	for i, v := range it.Versions {
		v.Parts = slices.Clone(v.Parts)
		for j := range v.Parts {
			part := &v.Parts[j]
			_, err := os.Stat(p.localOf(part.Path))
			ex := err == nil
			if o, ok := p.exists[part.Path]; ok {
				ex = o
			}
			part.Exists = &ex
			if s, ok := p.sizes[part.Path]; ok {
				part.Size = s
			}
		}
		out.Versions[i] = v
	}
	if p.onItem != nil {
		p.onItem(&out)
	}
	return &out, nil
}

func (p *fakePlex) DeleteMedia(_ context.Context, rk string, mediaID int64) error {
	p.log.add("plex.DeleteMedia %s/%d", rk, mediaID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deleteErr != nil {
		return p.deleteErr
	}
	it, ok := p.items[rk]
	if !ok {
		return plex.ErrNotFound
	}
	for i, v := range it.Versions {
		if v.MediaID != mediaID {
			continue
		}
		if !p.keepFiles {
			for _, part := range v.Parts {
				_ = os.Remove(p.localOf(part.Path))
			}
		}
		it.Versions = slices.Delete(it.Versions, i, i+1)
		return nil
	}
	return plex.ErrNotFound
}

func (p *fakePlex) RefreshItem(_ context.Context, rk string) error {
	p.log.add("plex.RefreshItem %s", rk)
	return nil
}

func (p *fakePlex) ScanPath(_ context.Context, sectionKey, dir string) error {
	p.log.add("plex.ScanPath %s %s", sectionKey, dir)
	return nil
}

func (p *fakePlex) MediaDeletionAllowed(context.Context) (bool, error) {
	p.log.add("plex.MediaDeletionAllowed")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed, p.allowedErr
}

func (p *fakePlex) ActiveSessions(context.Context) (map[string]bool, error) {
	p.log.add("plex.ActiveSessions")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionsErr != nil {
		return nil, p.sessionsErr
	}
	return maps(p.sessions), nil
}

func (p *fakePlex) Sections(context.Context) ([]plex.Section, error) {
	p.log.add("plex.Sections")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sectionsErr != nil {
		return nil, p.sectionsErr
	}
	return slices.Clone(p.sections), nil
}

func maps(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// fakeArr emulates one Radarr/Sonarr instance.
type fakeArr struct {
	mu         sync.Mutex
	log        *callLog
	name       string
	recycleBin string
	mmErr      error
	deleteErr  error
	files      map[int64]string // file id → local path removed by DeleteFile
	onDelete   func(fileID int64)
	// keepFiles: DeleteFile drops the tracking but leaves the file on disk (the *arr cannot see it).
	keepFiles bool
	// root is the local folder remoteRoot maps to (File reports paths as the *arr sees them).
	root string
	// items: file id → movie/series id reported by File (0 when absent).
	items map[int64]int64
	// fileRefs override what File reports for a file id; fileErr makes File fail.
	fileRefs map[int64]arr.TrackedFileRef
	fileErr  error
}

func (a *fakeArr) File(_ context.Context, fileID int64) (*arr.TrackedFileRef, error) {
	a.log.add("arr.File %s %d", a.name, fileID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fileErr != nil {
		return nil, a.fileErr
	}
	if ref, ok := a.fileRefs[fileID]; ok {
		return &ref, nil
	}
	p, ok := a.files[fileID]
	if !ok {
		return nil, fmt.Errorf("moviefile %d: %w", fileID, arr.ErrNotFound)
	}
	ref := arr.TrackedFileRef{Path: p, ItemID: a.items[fileID]}
	if rel, err := filepath.Rel(a.root, p); err == nil && a.root != "" && !strings.HasPrefix(rel, "..") {
		ref.Path = remoteRoot + "/" + filepath.ToSlash(rel)
	}
	if fi, err := os.Stat(p); err == nil {
		ref.Size = fi.Size()
	}
	return &ref, nil
}

func (a *fakeArr) DeleteFile(_ context.Context, fileID int64) error {
	a.log.add("arr.DeleteFile %s %d", a.name, fileID)
	if a.onDelete != nil {
		a.onDelete(fileID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleteErr != nil {
		return a.deleteErr
	}
	p, ok := a.files[fileID]
	if !ok {
		return fmt.Errorf("moviefile %d: %w", fileID, arr.ErrNotFound)
	}
	delete(a.files, fileID)
	if a.keepFiles {
		return nil
	}
	return os.Remove(p)
}

func (a *fakeArr) Rescan(_ context.Context, itemID int64) error {
	a.log.add("arr.Rescan %s %d", a.name, itemID)
	return nil
}

func (a *fakeArr) Unmonitor(_ context.Context, info models.ArrFileInfo) error {
	a.log.add("arr.Unmonitor %s item=%d episodes=%v", a.name, info.ItemID, info.EpisodeIDs)
	return nil
}

func (a *fakeArr) AddExclusion(_ context.Context, t arr.ExclusionTarget) error {
	a.log.add("arr.AddExclusion %s tmdb=%d tvdb=%d %s (%d)", a.name, t.TmdbID, t.TvdbID, t.Title, t.Year)
	return nil
}

func (a *fakeArr) MediaManagement(context.Context) (*arr.MediaManagement, error) {
	a.log.add("arr.MediaManagement %s", a.name)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mmErr != nil {
		return nil, a.mmErr
	}
	return &arr.MediaManagement{RecycleBin: a.recycleBin, RecycleBinCleanupDays: 7}, nil
}

// testEnv is a real database, fake Plex/*arr clients and a temp directory as media root.
type testEnv struct {
	t      *testing.T
	ctx    context.Context
	db     *database.DB
	svc    *Service
	log    *callLog
	plex   *fakePlex
	arrs   map[int64]*fakeArr
	server models.MediaServer
	radarr models.ArrInstance
	dir    string // temp dir (resolved)
	root   string // local media root (resolved)
	bus    *events.Bus
	now    time.Time

	seq      int
	mu       sync.Mutex
	enqueued []models.TargetedScanBody
	progress []string
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "media")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, filepath.Join(dir, "dupearr.db"), nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &testEnv{
		t: t, ctx: ctx, db: db, dir: dir, root: root,
		log:  &callLog{},
		arrs: map[int64]*fakeArr{},
		bus:  events.New(),
		now:  time.Date(2026, 9, 22, 10, 30, 0, 0, time.UTC),
	}
	st := models.DefaultSettings()
	st.DryRun = false
	st.DeletionMethods = []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem}
	st.RefreshPlexAfterDelete = false
	st.CleanupPlexStaleEntries = false
	e.saveSettings(st)

	e.server = models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: "http://plex.invalid:32400", Token: "secret-plex-token",
		MachineIdentifier: testMachineID, Enabled: true}
	if err := db.MediaServers().Create(ctx, &e.server); err != nil {
		t.Fatal(err)
	}
	e.radarr = models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: "http://radarr.invalid:7878", APIKey: "secret-arr-key", Enabled: true}
	if err := db.ArrInstances().Create(ctx, &e.radarr); err != nil {
		t.Fatal(err)
	}
	for _, m := range []models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: e.server.ID, RemotePath: remoteRoot, LocalPath: root},
		{SourceType: models.PathSourceArr, SourceID: e.radarr.ID, RemotePath: remoteRoot, LocalPath: root},
	} {
		if err := db.PathMappings().Create(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	// The server's library (id 1, as the fixtures use), covering the whole media root: the
	// filesystem method only removes parts inside a library folder of their server.
	if libs, err := db.Libraries().Sync(ctx, e.server.ID, []models.Library{
		{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot}},
	}); err != nil || len(libs) != 1 || libs[0].ID != 1 {
		t.Fatalf("sync libraries = %+v, %v", libs, err)
	}
	e.plex = &fakePlex{log: e.log, root: root, items: map[string]*models.MediaItem{}, allowed: true,
		exists: map[string]bool{}, sizes: map[string]int64{}, itemErr: map[string]error{}, nilItem: map[string]bool{},
		machineID: testMachineID}
	e.arrs[e.radarr.ID] = newFakeArr(e.log, "Radarr", root)

	e.svc = New(Deps{
		Store: db,
		Bus:   e.bus,
		Log:   slog.New(slog.DiscardHandler),
		PlexFactory: func(models.MediaServer) PlexClient {
			return e.plex
		},
		ArrFactory: func(a models.ArrInstance) ArrClient {
			if f, ok := e.arrs[a.ID]; ok {
				return f
			}
			return nil
		},
		Now: func() time.Time { return e.now },
		Enqueue: func(_ context.Context, name string, body any, _ string) error {
			if name != models.CmdTargetedScan {
				t.Errorf("unexpected command %s", name)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			e.enqueued = append(e.enqueued, body.(models.TargetedScanBody))
			return nil
		},
	})
	e.svc.goneWait = 50 * time.Millisecond
	e.svc.goneStep = 10 * time.Millisecond
	return e
}

func (e *testEnv) settings() models.Settings {
	e.t.Helper()
	st, err := e.db.Settings().Get(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *testEnv) saveSettings(st models.Settings) {
	e.t.Helper()
	if err := e.db.Settings().Save(e.ctx, st); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) update(fn func(*models.Settings)) {
	st := e.settings()
	fn(&st)
	e.saveSettings(st)
}

// addArr registers another *arr instance (with a path mapping) and its fake.
func (e *testEnv) addArr(name string, kind models.ArrKind) (models.ArrInstance, *fakeArr) {
	e.t.Helper()
	inst := models.ArrInstance{Name: name, Kind: kind, URL: "http://" + strings.ToLower(name) + ".invalid", APIKey: "k", Enabled: true}
	if err := e.db.ArrInstances().Create(e.ctx, &inst); err != nil {
		e.t.Fatal(err)
	}
	m := models.PathMapping{SourceType: models.PathSourceArr, SourceID: inst.ID, RemotePath: remoteRoot, LocalPath: e.root}
	if err := e.db.PathMappings().Create(e.ctx, &m); err != nil {
		e.t.Fatal(err)
	}
	f := newFakeArr(e.log, name, e.root)
	e.arrs[inst.ID] = f
	return inst, f
}

func newFakeArr(log *callLog, name, root string) *fakeArr {
	return &fakeArr{log: log, name: name, root: root, files: map[int64]string{}, items: map[int64]int64{},
		fileRefs: map[int64]arr.TrackedFileRef{}}
}

// vspec describes one version of a test group.
type vspec struct {
	mediaID  int64
	rk       string   // rating key (default "100")
	rel      []string // part paths relative to the media root
	size     int64    // bytes per part (default 1000+mediaID)
	decision models.Decision
	protect  bool
	tracked  *models.ArrFileInfo // *arr tracking (FileID registered with the fake)
	noFile   bool                // do not create the file on disk
}

func keep(id int64, rel ...string) vspec {
	return vspec{mediaID: id, rel: rel, decision: models.DecisionKeep}
}

func remove(id int64, rel ...string) vspec {
	return vspec{mediaID: id, rel: rel, decision: models.DecisionRemove}
}

func (v vspec) at(rk string) vspec                { v.rk = rk; return v }
func (v vspec) sized(n int64) vspec               { v.size = n; return v }
func (v vspec) arr(info models.ArrFileInfo) vspec { v.tracked = &info; return v }
func (v vspec) protected() vspec                  { v.protect = true; return v }
func (v vspec) without() vspec                    { v.noFile = true; return v }
func (e *testEnv) local(rel string) string        { return filepath.Join(e.root, filepath.FromSlash(rel)) }
func (e *testEnv) remote(rel string) string       { return remoteRoot + "/" + rel }
func radarrInfo(inst models.ArrInstance, fileID, itemID int64, itemRel string) models.ArrFileInfo {
	return models.ArrFileInfo{InstanceID: inst.ID, InstanceName: inst.Name, Kind: inst.Kind, FileID: fileID, ItemID: itemID,
		ItemPath: remoteRoot + "/" + itemRel, Monitored: true}
}

// writeFile creates a file of n bytes.
func writeFile(t *testing.T, p string, n int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if n > 1<<20 {
		// Large sizes are sparse: realistic byte counts without using the disk.
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(n); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte{'x'}, int(n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addGroup stores a pending movie group (and the matching Plex items and files).
func (e *testEnv) addGroup(title string, specs ...vspec) *models.DuplicateGroup {
	e.t.Helper()
	e.seq++
	seq := e.seq
	g := &models.DuplicateGroup{
		Key:         fmt.Sprintf("movie:tmdb:%d", 1000+seq),
		MediaType:   models.MediaTypeMovie,
		Title:       title,
		Year:        2020,
		ServerID:    e.server.ID,
		LibraryIDs:  []int64{1},
		ExternalIDs: map[string]string{"tmdb": fmt.Sprint(1000 + seq)},
		Status:      models.GroupPending,
		Flags:       []string{},
		ProfileID:   1,
		StableCount: 3,
	}
	for _, s := range specs {
		if s.rk == "" {
			s.rk = "100"
		}
		if s.size == 0 {
			s.size = 1000 + s.mediaID
		}
		v := models.MediaVersion{
			Key: fmt.Sprintf("plex:%d:%d", e.server.ID, s.mediaID), ServerID: e.server.ID, LibraryID: 1,
			LibraryTitle: "Movies", SectionKey: "1", RatingKey: s.rk, MediaID: s.mediaID, ItemTitle: title,
			Container: "mkv", Width: 1920, Height: 1080, Resolution: models.Res1080, VideoCodec: models.VCodecH264,
			BitrateKbps: 8000, DurationMs: 7_200_000, AddedAt: e.now.Add(-30 * 24 * time.Hour),
		}
		for i, rel := range s.rel {
			v.Parts = append(v.Parts, models.MediaPart{ID: s.mediaID*10 + int64(i), Path: e.remote(rel), Size: s.size, Duration: 7_200_000})
			if !s.noFile {
				writeFile(e.t, e.local(rel), s.size)
			}
		}
		if s.tracked != nil {
			info := *s.tracked
			v.Arr = &info
			if f := e.arrs[info.InstanceID]; f != nil && len(s.rel) > 0 {
				f.files[info.FileID] = e.local(s.rel[0])
				f.items[info.FileID] = info.ItemID
			}
		}
		g.Files = append(g.Files, models.GroupFile{Version: v, Decision: s.decision, EngineDecision: s.decision, Protected: s.protect})
		// The Plex view of the same version (what plex.Client.Item returns: no key/server/arr).
		pv := v
		pv.Key, pv.ServerID, pv.LibraryID, pv.Arr = "", 0, 0, nil
		pv.Parts = slices.Clone(v.Parts)
		e.plex.mu.Lock()
		it := e.plex.items[s.rk]
		if it == nil {
			it = &models.MediaItem{RatingKey: s.rk, MediaType: models.MediaTypeMovie, Title: title, Year: 2020, SectionKey: "1"}
			e.plex.items[s.rk] = it
		}
		it.Versions = append(it.Versions, pv)
		e.plex.mu.Unlock()
	}
	for _, f := range g.Files {
		if f.Decision == models.DecisionRemove {
			g.ReclaimableBytes += f.Version.TotalSize()
		}
	}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		e.t.Fatalf("upsert group: %v", err)
	}
	return e.group(g.ID)
}

func (e *testEnv) group(id int64) *models.DuplicateGroup {
	e.t.Helper()
	g, err := e.db.Groups().Get(e.ctx, id)
	if err != nil {
		e.t.Fatalf("get group %d: %v", id, err)
	}
	return g
}

func (e *testEnv) approve(id int64) []models.Action {
	e.t.Helper()
	acts, err := e.svc.Approve(e.ctx, id, models.TriggerManual)
	if err != nil {
		e.t.Fatalf("approve group %d: %v", id, err)
	}
	return acts
}

func (e *testEnv) process() (Summary, error) {
	return e.svc.ProcessQueue(e.ctx, func(msg string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.progress = append(e.progress, msg)
	})
}

func (e *testEnv) mustProcess() Summary {
	e.t.Helper()
	sum, err := e.process()
	if err != nil {
		e.t.Fatalf("process queue: %v (summary %+v)", err, sum)
	}
	return sum
}

func (e *testEnv) actions(groupID int64) []models.Action {
	e.t.Helper()
	acts, err := e.db.Actions().ListByGroup(e.ctx, groupID)
	if err != nil {
		e.t.Fatal(err)
	}
	return acts
}

func (e *testEnv) action(id int64) *models.Action {
	e.t.Helper()
	a, err := e.db.Actions().Get(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *testEnv) history(types ...string) []models.HistoryEvent {
	e.t.Helper()
	page, err := e.db.History().List(e.ctx, types, 0, store.Paging{Page: 1, PageSize: 500, SortKey: "createdAt", SortDirection: "ascending"})
	if err != nil {
		e.t.Fatal(err)
	}
	return page.Records
}

func (e *testEnv) scans() []models.TargetedScanBody {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.enqueued)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func statuses(acts []models.Action) []models.ActionStatus {
	out := make([]models.ActionStatus, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.Status)
	}
	return out
}

func wantStatus(t *testing.T, g *models.DuplicateGroup, want models.GroupStatus) {
	t.Helper()
	if g.Status != want {
		t.Fatalf("group status = %q (%s), want %q", g.Status, g.StatusReason, want)
	}
}

func wantActionStatus(t *testing.T, a *models.Action, want models.ActionStatus) {
	t.Helper()
	if a.Status != want {
		t.Fatalf("action %d status = %q (%s), want %q", a.ID, a.Status, a.Message, want)
	}
}

func contains(t *testing.T, what, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%s = %q, want it to contain %q", what, s, sub)
	}
}

// webhookRecorder is an httptest server collecting generic-webhook notifications.
type webhookRecorder struct {
	mu     sync.Mutex
	events []notifications.WebhookPayload
}

func (w *webhookRecorder) payloads() []notifications.WebhookPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

// withNotifier wires a real notifications.Service posting to a local webhook server.
func (e *testEnv) withNotifier() (*webhookRecorder, func()) {
	e.t.Helper()
	rec := &webhookRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p notifications.WebhookPayload
		if err := json.Unmarshal(body, &p); err == nil {
			rec.mu.Lock()
			rec.events = append(rec.events, p)
			rec.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	e.t.Cleanup(srv.Close)
	settings, _ := json.Marshal(map[string]any{"url": srv.URL + "/hook"})
	cfg := models.NotificationConfig{Name: "hook", Kind: notifications.KindWebhook, Settings: settings,
		Triggers: []string{models.OnFileDeleted, models.OnDeleteFailed}, Enabled: true}
	if err := e.db.Notifications().Create(e.ctx, &cfg); err != nil {
		e.t.Fatal(err)
	}
	n := notifications.New(e.db, nil)
	e.svc.d.Notifier = n
	return rec, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = n.Shutdown(ctx)
	}
}
