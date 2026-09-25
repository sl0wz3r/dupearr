package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// updateGoldens rewrites testdata/singleserver/*.golden.json. The golden was generated once on
// the tree before multi-server support (issue #8) and is never regenerated: it proves that the
// executor acts on a one-server installation exactly as it did before.
var updateGoldens = flag.Bool("update", false, "rewrite the single-server golden file (never after it was first generated)")

// goldenClock is the fixed clock of the golden run (after the fixture files were created).
var goldenClock = time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)

type goldenAction struct {
	VersionKey  string              `json:"versionKey"`
	Status      models.ActionStatus `json:"status"`
	Method      string              `json:"method"`
	Message     string              `json:"message"`
	RecyclePath string              `json:"recyclePath,omitempty"`
	Permanent   bool                `json:"permanent"`
}

type goldenQueueRun struct {
	Name     string         `json:"name"`
	Summary  string         `json:"summary"`
	Actions  []goldenAction `json:"actions"`
	Groups   []string       `json:"groups"` // key: status (reason)
	Requests []string       `json:"requests"`
	Enqueued []string       `json:"enqueued"`
}

// TestSingleServerExecutorGolden approves two groups of the default scenario and runs the queue in
// dry run and for real (recycle bin), with one Plex server and the real clients: the actions,
// methods, messages, recycle paths and requests must equal the golden generated before
// multi-server support.
func TestSingleServerExecutorGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	got := runExecutorGolden(t)
	path := filepath.Join("testdata", "singleserver", "default.golden.json")
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		out := filepath.Join(t.TempDir(), "got.json")
		_ = os.WriteFile(out, got, 0o644)
		t.Fatalf("single-server queue runs differ from %s (got written to %s):\n%s", path, out, goldenFirstDiff(want, got))
	}
}

func goldenFirstDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n want %s\n  got %s", i+1, w, g)
		}
	}
	return "equal lines, different bytes"
}

func runExecutorGolden(t *testing.T) []byte {
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
	plexFactory := func(s models.MediaServer) *plex.Client {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-golden", Product: "Dupearr", Version: "test"})
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	bus := events.New()
	sc := scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: now, Concurrency: 1,
		PlexFactory: func(s models.MediaServer) scanner.PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	var enqueued []string
	ex := New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: now,
		PlexFactory: func(s models.MediaServer) PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue: func(_ context.Context, name string, body any, _ string) error {
			b, _ := json.Marshal(body)
			enqueued = append(enqueued, name+" "+string(b))
			return nil
		},
	})
	ex.goneWait, ex.goneStep = 2*time.Second, 20*time.Millisecond

	srv := models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
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
