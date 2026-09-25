package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// ---------------------------------------------------------------------------
// Fake Plex
// ---------------------------------------------------------------------------

// fakePlex is an in-memory PlexClient. Items are stored as details; listings are derived from
// them (like the real server, a listing row carries media/part ids, files and sizes).
type fakePlex struct {
	mu        sync.Mutex
	machineID string
	sections  []plex.Section
	// items by section key, in insertion order
	bySection map[string][]*models.MediaItem
	listErr   map[string]error
	itemErr   map[string]error
	identErr  error
	itemCalls map[string]int
	listCalls int

	delay       time.Duration
	inflight    atomic.Int32
	maxInflight atomic.Int32
	listing     atomic.Int32
	maxListing  atomic.Int32
}

func newFakePlex(machineID string) *fakePlex {
	return &fakePlex{
		machineID: machineID,
		bySection: map[string][]*models.MediaItem{},
		listErr:   map[string]error{},
		itemErr:   map[string]error{},
		itemCalls: map[string]int{},
	}
}

// put adds or replaces an item (by rating key) in a section.
func (f *fakePlex) put(section string, it *models.MediaItem) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it.SectionKey = section
	for s, items := range f.bySection {
		for i, x := range items {
			if x.RatingKey == it.RatingKey {
				f.bySection[s] = append(items[:i:i], items[i+1:]...)
				break
			}
		}
	}
	f.bySection[section] = append(f.bySection[section], it)
}

// remove deletes an item.
func (f *fakePlex) remove(rk string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for s, items := range f.bySection {
		for i, x := range items {
			if x.RatingKey == rk {
				f.bySection[s] = append(items[:i:i], items[i+1:]...)
				return
			}
		}
	}
}

func (f *fakePlex) setListErr(section string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr[section] = err
}

func (f *fakePlex) setItemErr(rk string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.itemErr[rk] = err
}

func (f *fakePlex) calls(rk string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.itemCalls[rk]
}

func (f *fakePlex) Identity(ctx context.Context) (*plex.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identErr != nil {
		return nil, f.identErr
	}
	return &plex.Identity{MachineIdentifier: f.machineID, Version: "1.43.0", FriendlyName: "fake"}, nil
}

func (f *fakePlex) Sections(ctx context.Context) ([]plex.Section, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]plex.Section{}, f.sections...), nil
}

func (f *fakePlex) DuplicateItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error) {
	all, err := f.AllItems(ctx, sectionKey, mt)
	if err != nil {
		return nil, err
	}
	var out []plex.ItemRef
	for _, r := range all {
		if r.MediaCount >= 2 {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakePlex) AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error) {
	n := f.listing.Add(1)
	defer f.listing.Add(-1)
	storeMax(&f.maxListing, n)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if err := f.listErr[sectionKey]; err != nil {
		return nil, err
	}
	out := []plex.ItemRef{}
	for _, it := range f.bySection[sectionKey] {
		if it.MediaType != mt {
			continue
		}
		out = append(out, refOf(it))
	}
	return out, nil
}

func (f *fakePlex) Item(ctx context.Context, ratingKey string) (*models.MediaItem, error) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	storeMax(&f.maxInflight, n)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.itemCalls[ratingKey]++
	if err := f.itemErr[ratingKey]; err != nil {
		return nil, err
	}
	for _, items := range f.bySection {
		for _, it := range items {
			if it.RatingKey == ratingKey {
				return cloneItem(it), nil
			}
		}
	}
	return nil, fmt.Errorf("item %s: %w", ratingKey, plex.ErrNotFound)
}

func storeMax(m *atomic.Int32, n int32) {
	for {
		cur := m.Load()
		if n <= cur || m.CompareAndSwap(cur, n) {
			return
		}
	}
}

// refOf derives the listing row of an item (episode ids are episode-level; no show ids).
func refOf(it *models.MediaItem) plex.ItemRef {
	ref := plex.ItemRef{
		RatingKey:   it.RatingKey,
		MediaType:   it.MediaType,
		Title:       it.Title,
		Year:        it.Year,
		ShowTitle:   it.ShowTitle,
		Season:      it.Season,
		Episode:     it.Episode,
		ExternalIDs: map[string]string{},
		AddedAt:     it.AddedAt,
	}
	for k, v := range it.ExternalIDs {
		ref.ExternalIDs[k] = v
	}
	for _, v := range it.Versions {
		m := plex.MediaRef{ID: v.MediaID, Optimized: v.OptimizedVersion, Width: v.Width, Height: v.Height, DurationMs: v.DurationMs}
		for _, p := range v.Parts {
			m.Parts = append(m.Parts, plex.PartRef{ID: p.ID, File: p.Path, Size: p.Size})
		}
		ref.Media = append(ref.Media, m)
		if !v.OptimizedVersion {
			ref.MediaCount++
		}
	}
	return ref
}

// cloneItem deep-copies an item (the scanner decorates what it receives).
func cloneItem(it *models.MediaItem) *models.MediaItem {
	b, err := json.Marshal(it)
	if err != nil {
		panic(err)
	}
	var out models.MediaItem
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return &out
}

// ---------------------------------------------------------------------------
// Fake *arr
// ---------------------------------------------------------------------------

type fakeArr struct {
	mu    sync.Mutex
	files []arr.TrackedFile
	queue map[int64]bool
	// queueEntries (optional) are the queue entries Queue reports for a busy item (default: one
	// entry without a summary).
	queueEntries map[int64][]arr.QueueEntry
	filesErr     error
	queueErr     error
	onceErr      error // returned by the next TrackedFiles call only
	lastFilter   arr.TrackedFilter
	calls        int
}

func (f *fakeArr) TrackedFiles(ctx context.Context, flt arr.TrackedFilter) ([]arr.TrackedFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastFilter = flt
	if err := f.onceErr; err != nil {
		f.onceErr = nil
		return nil, err
	}
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	var out []arr.TrackedFile
	for _, tf := range f.files {
		if flt.TmdbIDs == nil && flt.ImdbIDs == nil && flt.TvdbIDs == nil {
			out = append(out, tf)
			continue
		}
		if flt.TmdbIDs[tf.TmdbID] || flt.TvdbIDs[tf.TvdbID] || (tf.ImdbID != "" && flt.ImdbIDs[strings.ToLower(tf.ImdbID)]) {
			out = append(out, tf)
		}
	}
	return out, nil
}

// Queue reports every item set in queue (true) with one entry, or with queueEntries[item] when
// set, like arr.Client.Queue.
func (f *fakeArr) Queue(ctx context.Context) (map[int64]*arr.QueueItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queueErr != nil {
		return nil, f.queueErr
	}
	out := map[int64]*arr.QueueItem{}
	for k, v := range f.queue {
		if !v {
			continue
		}
		it := &arr.QueueItem{Count: 1}
		if entries := f.queueEntries[k]; len(entries) > 0 {
			it.Count, it.Entries = len(entries), append([]arr.QueueEntry(nil), entries...)
		}
		out[k] = it
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Harness (real SQLite store)
// ---------------------------------------------------------------------------

type harness struct {
	t     *testing.T
	ctx   context.Context
	db    *database.DB
	bus   *events.Bus
	svc   *Service
	now   time.Time
	plex  map[int64]*fakePlex
	arrs  map[int64]ArrClient
	deps  Deps
	mu    sync.Mutex
	auto  []int64
	autoT []string
	// autoSig are the signatures AutoApprove was called with.
	autoSig []string
	progs   []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &harness{
		t:    t,
		ctx:  ctx,
		db:   db,
		bus:  events.New(),
		now:  time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		plex: map[int64]*fakePlex{},
		arrs: map[int64]ArrClient{},
	}
	h.deps = Deps{
		Store: db,
		Bus:   h.bus,
		Log:   slog.New(slog.DiscardHandler),
		PlexFactory: func(s models.MediaServer) PlexClient {
			h.mu.Lock()
			defer h.mu.Unlock()
			if f, ok := h.plex[s.ID]; ok {
				return f
			}
			return nil
		},
		ArrFactory: func(a models.ArrInstance) ArrClient {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.arrs[a.ID]
		},
		Now:         func() time.Time { return h.now },
		Concurrency: 3,
		AutoApprove: func(ctx context.Context, groupID int64, trigger, signature string) error {
			h.mu.Lock()
			h.auto = append(h.auto, groupID)
			h.autoT = append(h.autoT, trigger)
			h.autoSig = append(h.autoSig, signature)
			h.mu.Unlock()
			return db.Groups().UpdateStatus(ctx, groupID, models.GroupQueued, "approved")
		},
	}
	h.svc = New(h.deps)
	h.svc.arrRetryDelay = time.Millisecond
	// Settings: defaults, but a short min age so month-old fixtures are removable.
	s := models.DefaultSettings()
	s.MinAgeHours = 24
	h.saveSettings(s)
	return h
}

func (h *harness) saveSettings(s models.Settings) {
	h.t.Helper()
	if err := h.db.Settings().Save(h.ctx, s); err != nil {
		h.t.Fatalf("save settings: %v", err)
	}
}

func (h *harness) settings() models.Settings {
	h.t.Helper()
	s, err := h.db.Settings().Get(h.ctx)
	if err != nil {
		h.t.Fatalf("get settings: %v", err)
	}
	return s
}

// addServer creates an enabled Plex server with a fake client.
func (h *harness) addServer(name string) (models.MediaServer, *fakePlex) {
	h.t.Helper()
	srv := models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: "http://" + strings.ToLower(name) + ":32400",
		Token: "secret-token", MachineIdentifier: "mid-" + strings.ToLower(name), Enabled: true}
	if err := h.db.MediaServers().Create(h.ctx, &srv); err != nil {
		h.t.Fatalf("create server: %v", err)
	}
	f := newFakePlex(srv.MachineIdentifier)
	h.mu.Lock()
	h.plex[srv.ID] = f
	h.mu.Unlock()
	return srv, f
}

// addLibrary creates a library (section key = key) on a server.
func (h *harness) addLibrary(serverID int64, key, title, typ, scope string) models.Library {
	h.t.Helper()
	existing, err := h.db.Libraries().ListByServer(h.ctx, serverID)
	if err != nil {
		h.t.Fatalf("list libraries: %v", err)
	}
	secs := []models.Library{}
	for _, l := range existing {
		secs = append(secs, models.Library{ServerID: serverID, SectionKey: l.SectionKey, Title: l.Title, Type: l.Type, Locations: l.Locations})
	}
	secs = append(secs, models.Library{ServerID: serverID, SectionKey: key, Title: title, Type: typ, Locations: []string{"/data/" + key}})
	libs, err := h.db.Libraries().Sync(h.ctx, serverID, secs)
	if err != nil {
		h.t.Fatalf("sync libraries: %v", err)
	}
	for _, l := range libs {
		if l.SectionKey == key {
			l.ScopeGroup = scope
			l.Enabled = true
			if err := h.db.Libraries().Update(h.ctx, &l); err != nil {
				h.t.Fatalf("update library: %v", err)
			}
			return l
		}
	}
	h.t.Fatalf("library %s not synced", key)
	return models.Library{}
}

// addArr creates an enabled *arr instance backed by client.
func (h *harness) addArr(name string, kind models.ArrKind, client ArrClient) models.ArrInstance {
	h.t.Helper()
	a := models.ArrInstance{Name: name, Kind: kind, URL: "http://" + strings.ToLower(name) + ":7878", APIKey: "0123456789abcdef0123456789abcdef", Enabled: true}
	if err := h.db.ArrInstances().Create(h.ctx, &a); err != nil {
		h.t.Fatalf("create arr: %v", err)
	}
	h.mu.Lock()
	h.arrs[a.ID] = client
	h.mu.Unlock()
	return a
}

func (h *harness) addMapping(sourceType string, sourceID int64, remote, local string) {
	h.t.Helper()
	m := models.PathMapping{SourceType: sourceType, SourceID: sourceID, RemotePath: remote, LocalPath: local}
	if err := h.db.PathMappings().Create(h.ctx, &m); err != nil {
		h.t.Fatalf("create mapping: %v", err)
	}
}

// fullScan runs a full scan and fails the test on an unexpected error.
func (h *harness) fullScan(ids ...int64) *models.ScanRun {
	h.t.Helper()
	run, err := h.svc.FullScan(h.ctx, models.DuplicateScanBody{LibraryIDs: ids}, models.TriggerManual, func(m string) {
		h.mu.Lock()
		h.progs = append(h.progs, m)
		h.mu.Unlock()
	})
	if err != nil {
		h.t.Fatalf("full scan: %v", err)
	}
	if run.Status != runCompleted {
		h.t.Fatalf("scan status = %q (%s)", run.Status, run.Error)
	}
	return run
}

func (h *harness) group(key string) *models.DuplicateGroup {
	h.t.Helper()
	g, err := h.db.Groups().GetByKey(h.ctx, key)
	if err != nil {
		h.t.Fatalf("group %q: %v", key, err)
	}
	return g
}

func (h *harness) noGroup(key string) {
	h.t.Helper()
	if _, err := h.db.Groups().GetByKey(h.ctx, key); !errors.Is(err, store.ErrNotFound) {
		h.t.Fatalf("group %q exists (err=%v), want none", key, err)
	}
}

func (h *harness) history(eventType string) []models.HistoryEvent {
	h.t.Helper()
	pg, err := h.db.History().List(h.ctx, []string{eventType}, 0, store.Paging{Page: 1, PageSize: 100})
	if err != nil {
		h.t.Fatalf("history: %v", err)
	}
	return pg.Records
}

func (h *harness) approvals() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64{}, h.auto...)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var fixtureAdded = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// vopt customizes a fixture version.
type vopt func(*models.MediaVersion)

// ver returns an analyzed version: one part at path, 2 h long, AC3 English audio.
func ver(mediaID int64, path string, size int64, width int, opts ...vopt) models.MediaVersion {
	height := width * 9 / 16
	res := models.Res1080
	switch {
	case width >= 3200:
		res = models.Res2160
	case width >= 1700:
		res = models.Res1080
	case width >= 1100:
		res = models.Res720
	default:
		res = models.Res480
	}
	v := models.MediaVersion{
		MediaID:      mediaID,
		Container:    "mkv",
		DurationMs:   7_200_000,
		BitrateKbps:  int(size / 900_000),
		VideoBitrate: int(size / 1_000_000),
		Width:        width,
		Height:       height,
		Resolution:   res,
		VideoCodec:   models.VCodecHEVC,
		BitDepth:     10,
		DynamicRange: models.DRSDR,
		AudioTracks: []models.AudioTrack{{Format: models.AudioAC3, Codec: "ac3", Channels: 6,
			Language: "English", LanguageCode: "eng", Default: true}},
		SubtitleTracks: []models.SubtitleTrack{},
		Source:         models.SourceUnknown,
		AddedAt:        fixtureAdded,
		Parts:          []models.MediaPart{{ID: mediaID * 10, Path: path, Size: size, Duration: 7_200_000}},
	}
	for _, o := range opts {
		o(&v)
	}
	return v
}

func withDuration(ms int64) vopt {
	return func(v *models.MediaVersion) { v.DurationMs = ms; v.Parts[0].Duration = ms }
}

// movie returns a movie item.
func movie(rk string, tmdb int, title string, year int, versions ...models.MediaVersion) *models.MediaItem {
	ids := map[string]string{}
	if tmdb > 0 {
		ids["tmdb"] = strconv.Itoa(tmdb)
	}
	return &models.MediaItem{
		RatingKey:   rk,
		MediaType:   models.MediaTypeMovie,
		Title:       title,
		Year:        year,
		ExternalIDs: ids,
		ShowIDs:     map[string]string{},
		AddedAt:     fixtureAdded,
		Versions:    versions,
	}
}

// episode returns an episode item of a show (show-level tvdb id; no episode-level ids).
func episode(rk string, showTvdb int, show string, season, ep int, versions ...models.MediaVersion) *models.MediaItem {
	return &models.MediaItem{
		RatingKey:   rk,
		MediaType:   models.MediaTypeEpisode,
		Title:       fmt.Sprintf("Episode %d", ep),
		ShowTitle:   show,
		Season:      season,
		Episode:     ep,
		ExternalIDs: map[string]string{},
		ShowIDs:     map[string]string{"tvdb": strconv.Itoa(showTvdb)},
		AddedAt:     fixtureAdded,
		Versions:    versions,
	}
}

// fileByMedia returns the group file of a Plex media id.
func fileByMedia(t *testing.T, g *models.DuplicateGroup, mediaID int64) *models.GroupFile {
	t.Helper()
	for i := range g.Files {
		if g.Files[i].Version.MediaID == mediaID {
			return &g.Files[i]
		}
	}
	t.Fatalf("group %q has no file for media %d", g.Key, mediaID)
	return nil
}

func hasFlag(g *models.DuplicateGroup, f string) bool { return g.HasFlag(f) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
