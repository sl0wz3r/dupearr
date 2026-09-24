package executor

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
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

// TestEndToEndGuardsFakeMedia runs a scan and the executor with the real Plex and *arr clients
// against the fakemedia servers: the identity guards (GET / on Plex, GET episodefile/{id} on
// Sonarr) work on the wire, an *arr-tracked loser is deleted through Sonarr only after its file id
// was confirmed, and an *arr instance whose URL now reaches another server deletes nothing.
func TestEndToEndGuardsFakeMedia(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	env := fakemedia.Start(t, fakemedia.Default())
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatal(err)
	}
	plexFactory := func(s models.MediaServer) *plex.Client {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-executor-test", Product: "Dupearr", Version: "test"})
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	bus := events.New()
	sc := scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now,
		PlexFactory: func(s models.MediaServer) scanner.PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	var scans []models.TargetedScanBody
	ex := New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now,
		PlexFactory: func(s models.MediaServer) PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue: func(_ context.Context, _ string, body any, _ string) error {
			scans = append(scans, body.(models.TargetedScanBody))
			return nil
		},
	})

	srv := models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := sc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	insts := map[string]models.ArrInstance{}
	for name, s := range env.Instances {
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
	if s, _ := db.MediaServers().Get(ctx, srv.ID); s.MachineIdentifier == "" {
		t.Fatal("the scan did not store the machine identifier")
	}
	st, err := db.Settings().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DryRun = false
	st.DeletionMethods = []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem}
	st.RecycleBinPath = filepath.Join(t.TempDir(), "recycle")
	if err := db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}

	severance := func() *models.DuplicateGroup {
		t.Helper()
		pg, err := db.Groups().List(ctx, store.GroupFilter{Search: "Severance"}, store.Paging{Page: 1, PageSize: 10})
		if err != nil || len(pg.Records) != 1 {
			t.Fatalf("Severance group: %+v %v", pg.Records, err)
		}
		return &pg.Records[0]
	}
	g := severance()
	var tracked *models.GroupFile
	for i := range g.Files {
		if f := &g.Files[i]; f.Decision == models.DecisionRemove && f.Version.Arr != nil {
			tracked = f
		}
	}
	if tracked == nil {
		t.Fatalf("Severance has no *arr-tracked loser: %+v", g.Files)
	}
	loserPath := tracked.Version.Parts[0].Path

	t.Run("instance re-pointed at another server deletes nothing", func(t *testing.T) {
		son := insts[fakemedia.InstanceSonarr]
		orig := son
		// The Sonarr connection now reaches the Radarr server (same API key accepted there).
		radarr := env.Instances[fakemedia.InstanceRadarr]
		son.URL, son.APIKey = radarr.URL, radarr.APIKey
		if err := db.ArrInstances().Update(ctx, &son); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.ArrInstances().Update(ctx, &orig); err != nil {
				t.Fatal(err)
			}
		}()
		acts, err := ex.Approve(ctx, g.ID, models.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		env.ResetRequests()
		if _, err := ex.ProcessQueue(ctx, nil); err != nil {
			t.Fatal(err)
		}
		for _, a := range acts {
			got, _ := db.Actions().Get(ctx, a.ID)
			if got.Status != models.ActionSkipped || !strings.Contains(got.Message, "no longer tracks file") {
				t.Fatalf("action %d = %s (%s)", got.ID, got.Status, got.Message)
			}
		}
		for _, r := range env.Requests() {
			if r.Method == http.MethodDelete {
				t.Fatalf("a delete was sent: %s %s to %s", r.Method, r.Path, r.Server)
			}
		}
		if !env.FileExists(loserPath) {
			t.Fatal("the loser was removed")
		}
		// The skip sent the group to review; make it approvable again for the next case.
		if err := db.Groups().UpdateStatus(ctx, g.ID, models.GroupPending, "test"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("confirmed file is deleted through Sonarr", func(t *testing.T) {
		acts, err := ex.Approve(ctx, g.ID, models.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		env.ResetRequests()
		sum, err := ex.ProcessQueue(ctx, nil)
		if err != nil {
			t.Fatalf("process: %v (%+v)", err, sum)
		}
		var viaArr bool
		for _, a := range acts {
			got, _ := db.Actions().Get(ctx, a.ID)
			if got.Status != models.ActionSucceeded {
				t.Fatalf("action %d = %s (%s)", got.ID, got.Status, got.Message)
			}
			viaArr = viaArr || got.Method == models.MethodArr
		}
		if !viaArr || env.FileExists(loserPath) {
			t.Fatalf("the tracked loser was not removed through Sonarr")
		}
		var identity, getFile, del int
		for _, r := range env.Requests() {
			switch {
			case r.Server == fakemedia.ServerPlex && r.Method == http.MethodGet && (r.Path == "/" || r.Path == "/identity"):
				identity++
			case r.Server == fakemedia.InstanceSonarr && r.Method == http.MethodGet && strings.Contains(r.Path, "/episodefile/"):
				getFile++
			case r.Server == fakemedia.InstanceSonarr && r.Method == http.MethodDelete && strings.Contains(r.Path, "/episodefile/"):
				if getFile < 2 {
					t.Fatalf("DELETE before the file id was confirmed twice (%d reads)", getFile)
				}
				del++
			}
		}
		if identity == 0 || del != 1 {
			t.Fatalf("identity reads %d, deletes %d: %+v", identity, del, env.Requests())
		}
		env.AssertNoViolations(t)
	})
}
