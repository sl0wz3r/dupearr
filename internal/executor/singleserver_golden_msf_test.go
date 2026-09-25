package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestSingleServerExecutorGoldenThroughMediaServerFactory runs the executor golden the way
// production wires the scanner and the executor since issue #4 Phase 0 (cmd/dupearr:
// Deps.MediaServerFactory only, a Plex client for kinds "plex" and "", PlexFactory nil), for a
// server stored as "plex" and as "". The actions, methods, messages, recycle paths and requests
// must equal the same golden byte for byte. It only reads the golden
// (TestSingleServerExecutorGolden owns it).
func TestSingleServerExecutorGoldenThroughMediaServerFactory(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	path := filepath.Join("testdata", "singleserver", "default.golden.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	for _, kind := range []models.MediaServerKind{models.MediaServerPlex, ""} {
		t.Run(fmt.Sprintf("kind=%q", kind), func(t *testing.T) {
			got := runExecutorGoldenViaFactory(t, kind)
			if !bytes.Equal(got, want) {
				out := filepath.Join(t.TempDir(), "got.json")
				_ = os.WriteFile(out, got, 0o644)
				t.Fatalf("queue runs through MediaServerFactory differ from %s (got written to %s):\n%s", path, out, goldenFirstDiff(want, got))
			}
		})
	}
}

// runExecutorGoldenViaFactory is runExecutorGolden with the production factory wiring and the
// server stored with kind. Keep the two in step: only the marked lines differ.
func runExecutorGoldenViaFactory(t *testing.T, kind models.MediaServerKind) []byte {
	t.Helper()
	env := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: fakemedia.Default(), Now: func() time.Time { return goldenClock }})
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return goldenClock }
	// Differs: the cmd/dupearr wiring (mediaServerFactory) instead of PlexFactory.
	mediaServerFactory := func(s models.MediaServer) mediaserver.Client {
		if !s.Kind.IsPlex() {
			return nil
		}
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-golden", Product: "Dupearr", Version: "test"})
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	bus := events.New()
	sc := scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: now, Concurrency: 1,
		MediaServerFactory: mediaServerFactory,
		ArrFactory:         func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	var enqueued []string
	ex := New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: now,
		MediaServerFactory: mediaServerFactory,
		ArrFactory:         func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue: func(_ context.Context, name string, body any, _ string) error {
			b, _ := json.Marshal(body)
			enqueued = append(enqueued, name+" "+string(b))
			return nil
		},
	})
	ex.goneWait, ex.goneStep = 2*time.Second, 20*time.Millisecond

	// Differs: the stored kind.
	srv := models.MediaServer{Name: "Fake Plex", Kind: kind, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := sc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatal(err)
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
		if err := db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		insts[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, insts[m.Server].ID
		}
		if err := db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}

	// The first two pending groups (by key) that remove something.
	pg, err := db.Groups().List(ctx, store.GroupFilter{Statuses: []models.GroupStatus{models.GroupPending}}, store.Paging{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(pg.Records, func(i, j int) bool { return pg.Records[i].Key < pg.Records[j].Key })
	var picked []models.DuplicateGroup
	for _, g := range pg.Records {
		if g.ReclaimableBytes > 0 && len(picked) < 2 {
			picked = append(picked, g)
		}
	}
	if len(picked) != 2 {
		t.Fatalf("expected two pending groups with removals, got %d", len(picked))
	}

	binParent := t.TempDir()
	binDir := filepath.Join(binParent, "recycle")
	resolvedBin := binDir // the executor reports the bin's resolved path (/private/var/… on macOS)
	if rp, err := filepath.EvalSymlinks(binParent); err == nil {
		resolvedBin = filepath.Join(rp, "recycle")
	}
	replacer := strings.NewReplacer(env.Dir, "$DIR", resolvedBin, "$BIN", binDir, "$BIN")
	var runs []goldenQueueRun
	queue := func(name string) {
		t.Helper()
		for _, g := range picked {
			if _, err := ex.Approve(ctx, g.ID, models.TriggerManual); err != nil {
				t.Fatalf("%s: approve %s: %v", name, g.Key, err)
			}
		}
		env.ResetRequests()
		enqueued = nil
		sum, err := ex.ProcessQueue(ctx, nil)
		if err != nil {
			t.Fatalf("%s: process: %v", name, err)
		}
		qr := goldenQueueRun{Name: name, Summary: replacer.Replace(sum.Message), Requests: []string{}, Enqueued: append([]string{}, enqueued...)}
		for _, g := range picked {
			acts, err := db.Actions().ListByGroup(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range acts {
				if a.Status == models.ActionCancelled {
					continue
				}
				if (a.DryRun || a.Status == models.ActionDryRun) != (name == "dry run") {
					continue
				}
				qr.Actions = append(qr.Actions, goldenAction{
					VersionKey: a.VersionKey, Status: a.Status, Method: a.Method, Message: replacer.Replace(a.Message),
					RecyclePath: replacer.Replace(a.RecyclePath), Permanent: a.Permanent,
				})
			}
			sg, err := db.Groups().Get(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			qr.Groups = append(qr.Groups, fmt.Sprintf("%s: %s (%s)", sg.Key, sg.Status, replacer.Replace(sg.StatusReason)))
		}
		for _, r := range env.Requests() {
			qr.Requests = append(qr.Requests, r.Server+" "+r.Method+" "+r.Path)
		}
		runs = append(runs, qr)
	}

	queue("dry run")
	st, err := db.Settings().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DryRun = false
	st.RecycleBinPath = binDir
	if err := db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	// Plex deletion off: the untracked loser goes through the filesystem method into the bin.
	env.SetAllowDeletion(false)
	queue("recycle bin")
	env.AssertNoViolations(t)

	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}
