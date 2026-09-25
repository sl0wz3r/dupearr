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
	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Several Plex servers end to end in-process (docs/DECISIONS.md D11): the scanner and the executor
// with the real clients against two fakemedia Plex servers, the test tree declared ext4 (inode
// numbers prove different files) by an injected file identity prober.

const (
	msHeat4K   = "movies4k/Heat (1995)/Heat (1995) Remux-2160p.mkv"
	msHeat1080 = "movies/Heat (1995)/Heat (1995) WEBDL-1080p.mkv"
	msBID      = "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"
	msBToken   = "fAkEpLeXtOkEnB000001"
)

func msScenario(name string, tracked bool) *fakemedia.Scenario {
	s := fakemedia.Base(name)
	s.Libraries = []fakemedia.Library{
		{Key: fakemedia.SectionMovies, Title: "Movies", Type: fakemedia.LibraryMovie, Dirs: []string{fakemedia.DirMovies, fakemedia.DirMovies4K}},
	}
	s.Instances = s.Instances[:1] // radarr
	v1080 := fakemedia.Version{Parts: []fakemedia.Part{{File: msHeat1080, Size: fakemedia.GiB(12)}}, Video: fakemedia.FHD("h264"),
		Audio: []fakemedia.Audio{fakemedia.EAC3("eng", 6)}, DurationMs: fakemedia.Mins(170)}
	if tracked {
		v1080.Tracked = fakemedia.InstanceRadarr
	}
	s.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: "Heat", Year: 1995, TmdbID: 949, ImdbID: "tt0113277",
		Versions: []fakemedia.Version{
			{Parts: []fakemedia.Part{{File: msHeat4K, Size: fakemedia.GiB(60)}}, Video: fakemedia.HDR10UHD(),
				Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")}, DurationMs: fakemedia.Mins(170)},
			v1080,
		},
		Arr: []fakemedia.ArrMovie{{Instance: fakemedia.InstanceRadarr}},
	})
	return s
}

type msWorld struct {
	t          *testing.T
	ctx        context.Context
	db         *database.DB
	sc         *scanner.Service
	ex         *Service
	a, b       *fakemedia.Env
	srvA, srvB models.MediaServer
	radarr     models.ArrInstance
}

type msOpts struct {
	b        func(base *fakemedia.Scenario) *fakemedia.Scenario
	shared   bool
	mapB     bool
	separate bool
	tracked  bool
}

func newMSWorld(t *testing.T, name string, o msOpts) *msWorld {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	base := msScenario(name, o.tracked)
	a := fakemedia.Start(t, base)
	bo := fakemedia.Options{Scenario: o.b(base)}
	if o.shared {
		bo.ShareMedia = a
	} else {
		bo.Dir = t.TempDir()
	}
	b := fakemedia.StartWithOptions(t, bo)
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatal(err)
	}
	ids := fileid.New(fileid.Hooks{Supported: true, AssumeType: "ext4"})
	plexFactory := func(s models.MediaServer) *plex.Client {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-ms-test", Product: "Dupearr", Version: "test"})
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	bus := events.New()
	w := &msWorld{t: t, ctx: ctx, db: db, a: a, b: b}
	w.sc = scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now, FileIdentity: ids,
		PlexFactory: func(s models.MediaServer) scanner.PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	w.ex = New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now, FileIdentity: ids,
		PlexFactory: func(s models.MediaServer) PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue:     func(context.Context, string, any, string) error { return nil },
	})
	w.ex.goneWait, w.ex.goneStep = 2*time.Second, 20*time.Millisecond
	st := models.DefaultSettings()
	st.DryRun, st.MinAgeHours = false, 0
	st.RecycleBinPath = filepath.Join(t.TempDir(), "recycle")
	if err := db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	w.srvA = models.MediaServer{Name: "Plex A", Kind: models.MediaServerPlex, URL: a.Plex.URL, Token: a.PlexToken, Enabled: true}
	w.srvB = models.MediaServer{Name: "Plex B", Kind: models.MediaServerPlex, URL: b.Plex.URL, Token: b.PlexToken, Enabled: true}
	if o.separate {
		w.srvB.Storage = models.StorageSeparate
	}
	for _, srv := range []*models.MediaServer{&w.srvA, &w.srvB} {
		if err := db.MediaServers().Create(ctx, srv); err != nil {
			t.Fatal(err)
		}
		if err := w.sc.SyncLibraries(ctx, srv.ID); err != nil {
			t.Fatal(err)
		}
	}
	rs := a.Instances[fakemedia.InstanceRadarr]
	w.radarr = models.ArrInstance{Name: "Radarr", Kind: models.ArrRadarr, URL: rs.URL, APIKey: rs.APIKey, Enabled: true}
	if err := db.ArrInstances().Create(ctx, &w.radarr); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: w.srvA.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, w.radarr.ID
		}
		if err := db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	if o.mapB {
		m := b.PathMappings()[0]
		if err := db.PathMappings().Create(ctx, &models.PathMapping{SourceType: models.PathSourceServer, SourceID: w.srvB.ID,
			RemotePath: m.Remote, LocalPath: m.Local}); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *msWorld) scan() {
	w.t.Helper()
	if _, err := w.sc.FullScan(w.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		w.t.Fatal(err)
	}
}

func (w *msWorld) group(env *fakemedia.Env, sid int64) *models.DuplicateGroup {
	w.t.Helper()
	gs, err := w.db.Groups().ListByRatingKeys(w.ctx, sid, []string{env.RatingKey("", "Heat")})
	if err != nil {
		w.t.Fatal(err)
	}
	for i := range gs {
		if gs[i].ServerID == sid {
			return &gs[i]
		}
	}
	w.t.Fatalf("no group on server %d", sid)
	return nil
}

func (w *msWorld) process() Summary {
	w.t.Helper()
	sum, err := w.ex.ProcessQueue(w.ctx, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return sum
}

func (w *msWorld) finalChecks() {
	w.t.Helper()
	for _, env := range []*fakemedia.Env{w.a, w.b} {
		env.AssertNoViolations(w.t)
		env.AssertEveryItemHasAFile(w.t)
	}
	if lost := fakemedia.TitlesWithoutFile(w.a, w.b); len(lost) > 0 {
		w.t.Fatalf("titles without a file: %v", lost)
	}
}

// Scenario A: both servers list both copies. A removes the 1080p once, relying on B's 4K, which is
// proven a different file; B's group, approved afterwards, goes to review (its file is gone, and
// A's removal refreshed A's library).
func TestMultiServerScenarioAFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-a", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(base, "Plex B", msBID, msBToken)
	}, shared: true, mapB: true})
	w.scan()
	ga, gb := w.group(w.a, w.srvA.ID), w.group(w.b, w.srvB.ID)
	for _, g := range []*models.DuplicateGroup{ga, gb} {
		if g.Status != models.GroupPending || !g.HasFlag(models.FlagOtherServerListing) {
			t.Fatalf("group %d: %s %v %q", g.ServerID, g.Status, g.Flags, g.StatusReason)
		}
	}
	if _, err := w.ex.Approve(w.ctx, ga.ID, models.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if sum := w.process(); sum.Succeeded != 1 {
		t.Fatalf("A: %+v", sum)
	}
	if w.a.FileExists(w.a.RemoteMediaPath(msHeat1080)) || !w.a.FileExists(w.a.RemoteMediaPath(msHeat4K)) {
		t.Fatal("A removed the wrong file")
	}
	if _, err := w.ex.Approve(w.ctx, gb.ID, models.TriggerManual); err != nil {
		t.Fatal(err)
	}
	w.process()
	if g := w.group(w.b, w.srvB.ID); g.Status != models.GroupReview {
		t.Fatalf("B after A's removal: %s %q", g.Status, g.StatusReason)
	}
	deletes := 0
	for _, env := range []*fakemedia.Env{w.a, w.b} {
		for _, r := range env.Requests() {
			if r.Method == http.MethodDelete {
				deletes++
			}
		}
	}
	if deletes > 1 {
		t.Fatalf("%d deletes", deletes)
	}
	w.finalChecks()
}

// Scenario C: B lists only the 1080p copy: A's group is protected; once B's library lists the 4K
// copy too (B scanned it), the 1080p is removable.
func TestMultiServerScenarioCFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-c", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(base, "Plex B", msBID, msBToken, fakemedia.DirMovies)
	}, shared: true, mapB: true})
	w.scan()
	ga := w.group(w.a, w.srvA.ID)
	if ga.Status != models.GroupProtected {
		t.Fatalf("A: %s %q", ga.Status, ga.StatusReason)
	}
	if _, err := w.ex.Approve(w.ctx, ga.ID, models.TriggerManual); err == nil {
		t.Fatal("a protected group was approved")
	}
	w.finalChecks()
}

// Scenario B: A keeps the 4K, B's profile keeps the 1080p (both on one share): A's removal would
// override B's decision: review, and a manual approval is refused by the executor.
func TestMultiServerScenarioBFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-b", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(base, "Plex B", msBID, msBToken)
	}, shared: true, mapB: true})
	small := models.Profile{Name: "Smallest", KeepCount: 1, Criteria: []models.Criterion{{Type: models.CritFileSize, Enabled: true, Direction: models.DirectionLower}}}
	if err := w.db.Profiles().Create(w.ctx, &small); err != nil {
		t.Fatal(err)
	}
	libs, _ := w.db.Libraries().ListByServer(w.ctx, w.srvB.ID)
	for _, l := range libs {
		l.ProfileID = &small.ID
		if err := w.db.Libraries().Update(w.ctx, &l); err != nil {
			t.Fatal(err)
		}
	}
	w.scan()
	ga := w.group(w.a, w.srvA.ID)
	if ga.Status != models.GroupReview || !ga.HasFlag(models.FlagOtherServerKeeps) {
		t.Fatalf("A: %s %v", ga.Status, ga.Flags)
	}
	acts, err := w.ex.Approve(w.ctx, ga.ID, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	w.process()
	a, _ := w.db.Actions().Get(w.ctx, acts[0].ID)
	if a.Status != models.ActionSkipped || !strings.Contains(a.Message, "is kept by a duplicate group of Plex B") {
		t.Fatalf("action %s %q", a.Status, a.Message)
	}
	w.finalChecks()
}

// Scenario D: B mirrors A's layout on its own disks, declared separate and unmapped; Radarr (mapped)
// tracks A's 1080p at the same path as B's mirror. B's group never deletes through Radarr.
func TestMultiServerScenarioDFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-d", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.MirrorServer(base, "Plex B", msBID, msBToken)
	}, separate: true, tracked: true})
	w.scan()
	gb := w.group(w.b, w.srvB.ID)
	if gb.Status != models.GroupReview || !scanner.ArrTrackingUnknown(gb) {
		t.Fatalf("B: %s %q", gb.Status, gb.StatusReason)
	}
	for _, f := range gb.Files {
		if f.Version.Arr != nil {
			t.Fatalf("B's version attributed to Radarr: %+v", f.Version.Arr)
		}
	}
	if _, err := w.ex.Approve(w.ctx, gb.ID, models.TriggerManual); err == nil {
		// The executor alone does not refuse unknown tracking (the API does, 409); nothing it does
		// may reach Radarr's file either way.
		w.process()
	}
	for _, r := range w.a.Requests() {
		if r.Method == http.MethodDelete && strings.Contains(r.Path, "moviefile") {
			t.Fatalf("Radarr delete: %s", r.Path)
		}
	}
	if !w.a.FileExists(w.a.RemoteMediaPath(msHeat1080)) {
		t.Fatal("the local file was removed")
	}
	w.a.AssertNoViolations(t)
	w.a.AssertEveryItemHasAFile(t)
}

// B is declared separate storage and has no mapping during the scan, so its library is not
// compared; a mapping for B added before the queue runs (after the SeparateServerCheck warning,
// say) shows the file is B's only copy. The record no longer covers the servers: nothing removed.
func TestMultiServerSeparateMappedAfterScanFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-sep-mapped", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(base, "Plex B", msBID, msBToken, fakemedia.DirMovies)
	}, shared: true, separate: true})
	w.scan()
	ga := w.group(w.a, w.srvA.ID)
	if ga.Status != models.GroupPending {
		t.Fatalf("A: %s %q", ga.Status, ga.StatusReason)
	}
	m := w.b.PathMappings()[0]
	if err := w.db.PathMappings().Create(w.ctx, &models.PathMapping{SourceType: models.PathSourceServer, SourceID: w.srvB.ID,
		RemotePath: m.Remote, LocalPath: m.Local}); err != nil {
		t.Fatal(err)
	}
	acts, err := w.ex.Approve(w.ctx, ga.ID, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	w.process()
	a, _ := w.db.Actions().Get(w.ctx, acts[0].ID)
	if a.Status != models.ActionSkipped || !strings.Contains(a.Message, "path mappings of the media server Plex B changed") {
		t.Fatalf("action %s %q", a.Status, a.Message)
	}
	// The next scan compares B's mapped library: the 1080p is B's only copy.
	w.scan()
	if g := w.group(w.a, w.srvA.ID); g.Status != models.GroupProtected {
		t.Fatalf("A after the re-scan: %s %q", g.Status, g.StatusReason)
	}
	w.finalChecks()
}

// B lists A's 1080p in a TV library: a targeted scan of the movie (a Radarr webhook, the executor's
// own re-scan) keeps the protection the full scan found, and nothing is removed.
func TestMultiServerTargetedCrossTypeFakeMedia(t *testing.T) {
	w := newMSWorld(t, "ms-cross-type", msOpts{b: func(base *fakemedia.Scenario) *fakemedia.Scenario {
		s := fakemedia.SharedServer(base, "Plex B", msBID, msBToken, fakemedia.DirMovies)
		s.Libraries = []fakemedia.Library{{Key: fakemedia.SectionTV, Title: "Everything", Type: fakemedia.LibraryShow, Dirs: []string{fakemedia.DirMovies}}}
		v := s.Movies[0].Versions[0]
		s.Movies = nil
		s.AddShow(fakemedia.Show{Section: fakemedia.SectionTV, Title: "Heat", Year: 1995, TvdbID: 777, Folder: "movies/Heat (1995)",
			Episodes: []fakemedia.Episode{{Season: 1, Episode: 1, Title: "Heat", Versions: []fakemedia.Version{v}}}})
		return s
	}, shared: true, mapB: true})
	w.scan()
	if _, err := w.sc.TargetedScan(w.ctx, models.TargetedScanBody{ServerID: w.srvA.ID, TmdbID: 949}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	ga := w.group(w.a, w.srvA.ID)
	if ga.Status != models.GroupProtected {
		t.Fatalf("A after the targeted scan: %s %q", ga.Status, ga.StatusReason)
	}
	if _, err := w.ex.Approve(w.ctx, ga.ID, models.TriggerManual); err == nil {
		t.Fatal("a protected group was approved")
	}
	w.process()
	w.finalChecks()
}
