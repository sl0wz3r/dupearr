package scanner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestSingleServerGoldenThroughMediaServerFactory runs the single-server golden scenarios the way
// production wires the scanner since issue #4 Phase 0 (cmd/dupearr: Deps.MediaServerFactory only,
// a Plex client for kinds "plex" and "", PlexFactory nil), for a server stored as "plex" and as ""
// (rows from before kinds were checked). The results must equal the same goldens byte for byte:
// the neutral path decides, stores and requests exactly what the Plex path did. It only reads the
// goldens (TestSingleServerGolden owns them).
func TestSingleServerGoldenThroughMediaServerFactory(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	for _, kind := range []models.MediaServerKind{models.MediaServerPlex, ""} {
		for _, c := range singleServerGoldenCases() {
			t.Run(fmt.Sprintf("kind=%q/%s", kind, c.name), func(t *testing.T) {
				got := runSingleServerGoldenViaFactory(t, c, kind)
				path := filepath.Join("testdata", "singleserver", c.name+".golden.json")
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden: %v", err)
				}
				if !bytes.Equal(got, want) {
					diffPath := filepath.Join(t.TempDir(), c.name+".got.json")
					_ = os.WriteFile(diffPath, got, 0o644)
					t.Fatalf("results through MediaServerFactory differ from %s (got written to %s): %s", path, diffPath, firstDiff(want, got))
				}
			})
		}
	}
}

// runSingleServerGoldenViaFactory is runSingleServerGolden with the production factory wiring and
// the server stored with kind. Keep the two in step: only the marked lines differ.
func runSingleServerGoldenViaFactory(t *testing.T, c goldenCase, kind models.MediaServerKind) []byte {
	t.Helper()
	opts := c.opts
	opts.Now = func() time.Time { return goldenClock }
	env := fakemedia.StartWithOptions(t, opts)
	h := newHarness(t)
	deps := h.deps
	deps.Now = func() time.Time { return goldenClock }
	deps.Concurrency = 1 // one item at a time: the Plex request order is deterministic
	deps.AutoApprove = nil
	// Differs: the cmd/dupearr wiring (mediaServerFactory) instead of PlexFactory.
	deps.PlexFactory = nil
	deps.MediaServerFactory = func(s models.MediaServer) mediaserver.Client {
		if !s.Kind.IsPlex() {
			return nil
		}
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-golden", Product: "Dupearr", Version: "test"})
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	deps.TautulliFactory = func(ti models.TautulliInstance) WatchClient { return tautulli.New(ti, tautulli.Options{}) }
	svc := New(deps)
	svc.arrRetryDelay = time.Millisecond
	ctx := h.ctx

	// Differs: the stored kind.
	srv := models.MediaServer{Name: "Fake Plex", Kind: kind, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := h.db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatalf("sync libraries: %v", err)
	}
	libs, err := h.db.Libraries().ListByServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range libs {
		if l.Type == "movie" {
			l.ScopeGroup = "movies"
			if err := h.db.Libraries().Update(ctx, &l); err != nil {
				t.Fatal(err)
			}
		}
	}
	names := make([]string, 0, len(env.Instances))
	for name := range env.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	insts := map[string]models.ArrInstance{}
	for _, name := range names {
		s := env.Instances[name]
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		a := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := h.db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		insts[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, insts[m.Server].ID
		}
		if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	if c.name == "watch" {
		ti := models.TautulliInstance{Name: "Tautulli", ServerID: srv.ID, URL: env.Tautulli.URL, APIKey: env.TautulliAPIKey, Enabled: true}
		if err := h.db.Tautullis().Create(ctx, &ti); err != nil {
			t.Fatal(err)
		}
	}

	norm := newGoldenNormalizer(env)
	out := goldenRun{Scenario: c.name}
	scan := func(name string, do func() error) {
		t.Helper()
		env.ResetRequests()
		if err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out.Scans = append(out.Scans, norm.requests(name, env.Requests()))
	}
	state := func(after string) {
		t.Helper()
		out.States = append(out.States, goldenState{After: after, Groups: norm.groups(t, h)})
	}

	full := func() error {
		_, err := svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
		return err
	}
	scan("full scan 1", full)
	state("full scan 1")
	scan("full scan 2", full)
	state("full scan 2")
	if tmdb := firstMovieTmdb(t, h); tmdb > 0 {
		scan(fmt.Sprintf("targeted scan tmdb %d", tmdb), func() error {
			_, err := svc.TargetedScan(ctx, models.TargetedScanBody{TmdbID: tmdb}, models.TriggerWebhook)
			return err
		})
		state("targeted scan")
	}
	if err := svc.ReevaluateAll(ctx); err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	state("re-evaluation")
	env.AssertNoViolations(t)

	data, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}
