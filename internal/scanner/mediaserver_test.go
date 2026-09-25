package scanner

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// coreClient exposes only the kind-neutral core (mediaserver.Client) of a fake Plex: no
// DuplicateItems, nothing Plex-only. It is what a client of another kind looks like to the scanner.
type coreClient struct{ f *fakePlex }

func (c coreClient) Identity(ctx context.Context) (*mediaserver.Identity, error) {
	return c.f.Identity(ctx)
}

func (c coreClient) Sections(ctx context.Context) ([]mediaserver.Section, error) {
	return c.f.Sections(ctx)
}

func (c coreClient) AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]mediaserver.ItemRef, error) {
	return c.f.AllItems(ctx, sectionKey, mt)
}

func (c coreClient) Item(ctx context.Context, itemID string) (*models.MediaItem, error) {
	return c.f.Item(ctx, itemID)
}

func (c coreClient) ActiveSessions(context.Context) (map[string]bool, error) {
	return map[string]bool{}, nil
}

var _ mediaserver.Client = coreClient{}

// TestMediaServerFactoryPreferredOverPlexFactory: with Deps.MediaServerFactory set, syncs and scans
// read through it (a client with only the neutral core is enough) and Deps.PlexFactory is never
// asked; the stored keys are the legacy Plex ones.
func TestMediaServerFactoryPreferredOverPlexFactory(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	px.sections = []mediaserver.Section{{Key: "1", Type: "movie", Title: "Movies", Locations: []string{"/data/1"}}}
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("100", 603, "The Matrix", 1999,
		ver(11, "/data/1/The Matrix (1999)/The Matrix 2160p.mkv", 40*gb, 3840),
		ver(12, "/data/1/The Matrix (1999)/The Matrix 1080p.mkv", 10*gb, 1920)))

	var neutral, legacy atomic.Int32
	deps := h.deps
	deps.MediaServerFactory = func(s models.MediaServer) mediaserver.Client {
		neutral.Add(1)
		if s.ID != srv.ID {
			return nil
		}
		return coreClient{px}
	}
	deps.PlexFactory = func(models.MediaServer) PlexClient {
		legacy.Add(1)
		return nil
	}
	h.svc = New(deps)
	if err := h.svc.SyncLibraries(h.ctx, srv.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	h.fullScan()
	g := h.group("movie:tmdb:603")
	for _, f := range g.Files {
		if want := "plex:" + strconv.FormatInt(srv.ID, 10) + ":" + strconv.FormatInt(f.Version.MediaID, 10); f.Version.Key != want {
			t.Errorf("version key %q, want the legacy %q", f.Version.Key, want)
		}
		if f.Version.ItemKeyID != "" {
			t.Errorf("a Plex version got ItemKeyID %q", f.Version.ItemKeyID)
		}
	}
	if neutral.Load() == 0 {
		t.Error("MediaServerFactory was never asked")
	}
	if n := legacy.Load(); n != 0 {
		t.Errorf("PlexFactory was asked %d times although MediaServerFactory is set", n)
	}
}

// TestPlexFactoryFallbackAndNilClient: without MediaServerFactory the scanner uses PlexFactory as
// before; a factory's nil is "no client" with the unchanged messages, and a MediaServerFactory
// that has no client for a server never falls back to PlexFactory.
func TestPlexFactoryFallbackAndNilClient(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	px.sections = []mediaserver.Section{{Key: "1", Type: "movie", Title: "Movies", Locations: []string{"/data/1"}}}

	// PlexFactory alone (the harness): works.
	if err := h.svc.SyncLibraries(h.ctx, srv.ID); err != nil {
		t.Fatalf("sync through PlexFactory: %v", err)
	}

	// PlexFactory without a client for the server.
	deps := h.deps
	deps.PlexFactory = func(models.MediaServer) PlexClient { return nil }
	if err := New(deps).SyncLibraries(h.ctx, srv.ID); err == nil || !strings.Contains(err.Error(), "no client available") {
		t.Errorf("PlexFactory returning nil: err = %v, want \"no client available\"", err)
	}
	if c := New(deps).serverClient(srv); c != nil {
		t.Errorf("serverClient = %#v, want a nil interface", c)
	}

	// MediaServerFactory without a client: no fallback to the PlexFactory that has one.
	deps = h.deps
	deps.MediaServerFactory = func(models.MediaServer) mediaserver.Client { return nil }
	if err := New(deps).SyncLibraries(h.ctx, srv.ID); err == nil || !strings.Contains(err.Error(), "no client available") {
		t.Errorf("MediaServerFactory returning nil: err = %v, want \"no client available\"", err)
	}

	// No factory at all.
	deps = h.deps
	deps.PlexFactory, deps.MediaServerFactory = nil, nil
	svc := New(deps)
	if err := svc.SyncLibraries(h.ctx, srv.ID); err == nil || !strings.Contains(err.Error(), "no media server client factory configured") {
		t.Errorf("no factory: err = %v, want \"no media server client factory configured\"", err)
	}
	p := newPipeline(h.ctx, svc, &models.ScanRun{}, nil)
	if _, err := p.serverClient(srv); err == nil || err.Error() != "no media server client factory configured" {
		t.Errorf("pipeline without a factory: err = %v", err)
	}
	deps.PlexFactory = func(models.MediaServer) PlexClient { return nil }
	p = newPipeline(h.ctx, New(deps), &models.ScanRun{}, nil)
	if _, err := p.serverClient(srv); err == nil || err.Error() != `media server "Plex": no client available` {
		t.Errorf("pipeline without a client: err = %v", err)
	}
}

// TestSyncRefusesUnsupportedKindSameMessage: a stored server of another kind (the API refuses to
// create one) is refused by the library sync with the unchanged message, never handed to a
// factory, and left out of scans.
func TestSyncRefusesUnsupportedKindSameMessage(t *testing.T) {
	h := newHarness(t)
	var asked atomic.Int32
	deps := h.deps
	deps.MediaServerFactory = func(models.MediaServer) mediaserver.Client { asked.Add(1); return nil }
	deps.PlexFactory = func(models.MediaServer) PlexClient { asked.Add(1); return nil }
	h.svc = New(deps)
	// jellyfin is supported since issue #4 Phase 1; emby (Phase 2) and a mis-cased kind are not.
	for _, kind := range []models.MediaServerKind{"emby", "Plex"} {
		srv := models.MediaServer{Name: "Other " + string(kind), Kind: kind, URL: "http://other:8096", Token: "t", Enabled: true}
		if err := h.db.MediaServers().Create(h.ctx, &srv); err != nil {
			t.Fatal(err)
		}
		err := h.svc.SyncLibraries(h.ctx, srv.ID)
		want := `unsupported media server kind "` + string(kind) + `"`
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("kind %q: err = %v, want %q", kind, err, want)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("a factory was asked %d times for an unsupported kind", n)
	}
	cfg, err := h.svc.loadScanConfig(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.servers) != 0 {
		t.Errorf("scan servers = %v, want none of an unsupported kind", cfg.servers)
	}
}

// TestDisambiguatedKeyKindFromVersionKeys: the collision suffix names the primary item's server
// kind from the group's version keys; groups whose versions carry no media-server key (discs
// found on disk, key-less versions) are Plex's, as every stored group was.
func TestDisambiguatedKeyKindFromVersionKeys(t *testing.T) {
	files := func(keys ...string) []models.GroupFile {
		out := make([]models.GroupFile, len(keys))
		for i, k := range keys {
			out[i] = models.GroupFile{Version: models.MediaVersion{Key: k, ServerID: 2, LibraryID: 4, RatingKey: " 95 "}}
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		files []models.GroupFile
	}{
		{"plex keys", files("plex:2:11", "plex:2:12")},
		{"disc keys only", files("disc:2:abcdef", "disc:2:123456")},
		{"no keys", files("", "")},
		{"disc and plex", files("disc:2:abcdef", "plex:2:12")},
	} {
		g := &models.DuplicateGroup{Key: "movie:tmdb:1#ed-dc", ServerID: 2, Files: tc.files}
		if got := disambiguatedKey(g); got != "movie:tmdb:1@plex:2:95#ed-dc" {
			t.Errorf("%s: disambiguatedKey = %q", tc.name, got)
		}
	}
	// A plex key of another server does not decide this server's kind (still Plex by default).
	g := &models.DuplicateGroup{Key: "movie:tmdb:1", Files: []models.GroupFile{
		{Version: models.MediaVersion{Key: "disc:2:ab", ServerID: 2, LibraryID: 1, RatingKey: "95"}},
		{Version: models.MediaVersion{Key: "plex:3:7", ServerID: 3, LibraryID: 9, RatingKey: "7"}},
	}}
	if got := disambiguatedKey(g); got != "movie:tmdb:1@plex:2:95" {
		t.Errorf("mixed servers: disambiguatedKey = %q", got)
	}
	if got := stripDisambiguation("movie:tmdb:1@plex:2:95#ed-dc"); got != "movie:tmdb:1#ed-dc" {
		t.Errorf("stripDisambiguation = %q", got)
	}
}

// TestDecorateSetsServerKindAndLegacyVersionKey: decorate records the server kind on the item and
// keys versions "plex:<serverID>:<mediaID>" for Plex (kind "plex" or ""); a version without a
// media id keeps no key.
func TestDecorateSetsServerKindAndLegacyVersionKey(t *testing.T) {
	for _, kind := range []models.MediaServerKind{models.MediaServerPlex, ""} {
		p := &pipeline{cfg: &scanConfig{mapper: pathmap.New(nil)}, index: &itemIndex{}}
		ir := &indexedRef{ref: mediaserver.ItemRef{RatingKey: "100"}, lib: models.Library{ID: 4, Type: "movie", SectionKey: "1"},
			server: models.MediaServer{ID: 7, Kind: kind}}
		item := &models.MediaItem{RatingKey: "100", MediaType: models.MediaTypeMovie, Versions: []models.MediaVersion{
			{MediaID: 31, Key: "stale", Parts: []models.MediaPart{{Path: "/data/a.mkv"}}},
			{MediaID: 0, Key: "stale", Parts: []models.MediaPart{{Path: "/data/b.mkv"}}},
		}}
		p.decorate(item, ir)
		if item.ServerKind != kind {
			t.Errorf("kind %q: item.ServerKind = %q", kind, item.ServerKind)
		}
		if got := item.Versions[0].Key; got != "plex:7:31" {
			t.Errorf("kind %q: version key %q, want plex:7:31", kind, got)
		}
		if got := item.Versions[1].Key; got != "" {
			t.Errorf("kind %q: key of a version without media id = %q, want none", kind, got)
		}
		if item.Versions[0].ServerID != 7 || item.Versions[0].LibraryID != 4 || item.Versions[0].ItemKeyID != "" {
			t.Errorf("kind %q: identity %+v", kind, item.Versions[0])
		}
	}
}

// TestOtherListingsKeepVersionsWithoutNumericIDs: another server's versions are told apart by
// their version id, so a server whose ids are not numbers (MediaRef.ID 0, VersionID set) keeps one
// listing per version for the executor's D11 re-check. Plex listings keep their numeric order
// (99 before 100, not the string order), so OtherServers stores the same bytes as before.
func TestOtherListingsKeepVersionsWithoutNumericIDs(t *testing.T) {
	const rel = "movies/Heat (1995)/Heat (1995).mkv"
	ref := func(rk string, m mediaserver.MediaRef) mediaserver.ItemRef {
		m.Parts = []mediaserver.PartRef{{ID: 1, File: "/b/" + rel, Size: 200}}
		return mediaserver.ItemRef{RatingKey: rk, MediaType: models.MediaTypeMovie, Title: "Heat", Year: 1995,
			Media: []mediaserver.MediaRef{m}}
	}
	check := func(name string, want [][2]string, refs ...mediaserver.ItemRef) {
		t.Helper()
		f := newCrossFixture(t, true, refs...)
		f.write(rel, 200)
		got := f.listings(rel, 200)
		if len(got) != len(want) {
			t.Fatalf("%s: %d listings %+v, want %d", name, len(got), got, len(want))
		}
		for i, w := range want {
			if got[i].RatingKey != w[0] || got[i].VersionKey != w[1] || got[i].Match != models.OtherSameFile {
				t.Errorf("%s: listing %d = %s %s %s, want %s %s same_file", name, i, got[i].RatingKey, got[i].VersionKey, got[i].Match, w[0], w[1])
			}
		}
	}
	check("string version ids", [][2]string{{"8", "plex:2:a"}, {"7", "plex:2:b"}},
		ref("7", mediaserver.MediaRef{VersionID: "b"}), ref("8", mediaserver.MediaRef{VersionID: "a"}))
	check("plex media ids", [][2]string{{"8", "plex:2:99"}, {"7", "plex:2:100"}},
		ref("7", mediaserver.MediaRef{ID: 100}), ref("8", mediaserver.MediaRef{ID: 99}))
}
