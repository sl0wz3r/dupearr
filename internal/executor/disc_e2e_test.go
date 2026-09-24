package executor

import (
	"context"
	"log/slog"
	"net/http"
	"os"
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
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// discE2E is a scanned fakemedia "discs" world with disc removal allowed.
type discE2E struct {
	env *fakemedia.Env
	db  *database.DB
	sc  *scanner.Service
	ex  *Service
	st  models.Settings
}

func newDiscE2E(t *testing.T, opts fakemedia.Options) *discE2E {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	opts.Scenario = fakemedia.Discs()
	env := fakemedia.StartWithOptions(t, opts)
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
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-disc-test", Product: "Dupearr", Version: "test"})
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	bus := events.New()
	sc := scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now,
		PlexFactory: func(s models.MediaServer) scanner.PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	ex := New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now,
		PlexFactory: func(s models.MediaServer) PlexClient { return plexFactory(s) },
		ArrFactory:  func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue:     func(context.Context, string, any, string) error { return nil },
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
	st, err := db.Settings().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.DryRun = false
	st.MinAgeHours = 0 // the scenario's files were just written
	st.AllowDiscRemoval = true
	// The bin shares the media's filesystem (a disc is only ever renamed into it).
	st.RecycleBinPath = filepath.Join(env.Dir, "recycle")
	if err := db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	return &discE2E{env: env, db: db, sc: sc, ex: ex, st: st}
}

func (e *discE2E) group(t *testing.T, key string) *models.DuplicateGroup {
	t.Helper()
	g, err := e.db.Groups().GetByKey(context.Background(), key)
	if err != nil {
		t.Fatalf("group %s: %v", key, err)
	}
	return g
}

func discOf(t *testing.T, g *models.DuplicateGroup) *models.GroupFile {
	t.Helper()
	for i := range g.Files {
		if g.Files[i].Version.Disc != nil {
			return &g.Files[i]
		}
	}
	t.Fatalf("group %s has no disc", g.Key)
	return nil
}

// TestEndToEndDiscRemoval scans the fakemedia "discs" scenario and removes whole discs with the
// real clients: a UHD Blu-ray folder structure is moved into the recycle bin as a whole (its
// BDMV/, CERTIFICATE/ and AACS/ — never the MKV next to it), without a single Plex or *arr delete,
// and restored; a disc an *arr tracks a clip of is followed by a rescan of that *arr.
func TestEndToEndDiscRemoval(t *testing.T) {
	e := newDiscE2E(t, fakemedia.Options{})
	env, db, sc, ex, st := e.env, e.db, e.sc, e.ex, e.st
	ctx := context.Background()
	group := func(key string) *models.DuplicateGroup { t.Helper(); return e.group(t, key) }
	discOf := func(g *models.DuplicateGroup) *models.GroupFile { t.Helper(); return discOf(t, g) }

	// Blade Runner 2049: the remux wins; the UHD disc (300 clips) is removed as a whole.
	br := group("movie:tmdb:335984")
	d := discOf(br)
	if br.Status != models.GroupPending || d.Decision != models.DecisionRemove {
		t.Fatalf("Blade Runner: status %s (%s), disc %s", br.Status, br.StatusReason, d.Decision)
	}
	owned := append([]string{}, d.Version.Disc.OwnedEntries...)
	var mkv string
	for _, f := range br.Files {
		if f.Version.Disc == nil {
			mkv, _ = env.LocalPath(f.Version.Parts[0].Path)
		}
	}
	env.ResetRequests()
	acts, err := ex.Approve(ctx, br.ID, models.TriggerManual)
	if err != nil || len(acts) != 1 {
		t.Fatalf("approve: %v %+v", err, acts)
	}
	if _, err := ex.ProcessQueue(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := db.Actions().Get(ctx, acts[0].ID)
	if a.Status != models.ActionSucceeded || a.Method != models.MethodFilesystem || a.Permanent {
		t.Fatalf("disc removal: %s via %s (%s)", a.Status, a.Method, a.Message)
	}
	moved := strings.Split(a.RecyclePath, "\n")
	if len(moved) != len(owned) {
		t.Fatalf("moved %v, owned %v", moved, owned)
	}
	for _, o := range owned {
		if _, err := os.Lstat(o); err == nil {
			t.Fatalf("%s is still in place", o)
		}
	}
	for _, m := range moved {
		if fi, err := os.Lstat(m); err != nil || !strings.HasPrefix(m, st.RecycleBinPath) {
			t.Fatalf("%s not in the recycle bin (%v %v)", m, fi, err)
		}
	}
	if _, err := os.Stat(mkv); err != nil {
		t.Fatalf("the MKV next to the disc is gone: %v", err)
	}
	for _, r := range env.Requests() {
		if r.Method == http.MethodDelete {
			t.Fatalf("a disc was deleted through %s: %s %s", r.Server, r.Method, r.Path)
		}
	}
	env.AssertNoViolations(t)
	env.AssertDiscsIntact(t)

	if err := ex.Restore(ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, o := range owned {
		if _, err := os.Lstat(o); err != nil {
			t.Fatalf("%s was not restored: %v", o, err)
		}
	}
	env.AssertDiscsIntact(t)

	// Gladiator: Radarr tracks the main clip inside the disc; a person keeps the WEB-DL and removes
	// the disc. The clip is never deleted through Radarr; Radarr is rescanned afterwards.
	gl := group("movie:tmdb:98")
	gd := discOf(gl)
	for i := range gl.Files {
		f := &gl.Files[i]
		want := models.DecisionKeep
		if f.Version.Disc != nil {
			want = models.DecisionRemove
		}
		if err := db.Groups().SetOverride(ctx, gl.ID, f.ID, want); err != nil {
			t.Fatal(err)
		}
	}
	gl, err = sc.Reevaluate(ctx, gl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gl.Status != models.GroupReview && gl.Status != models.GroupPending {
		t.Fatalf("Gladiator: status %s (%s)", gl.Status, gl.StatusReason)
	}
	env.ResetRequests()
	acts, err = ex.Approve(ctx, gl.ID, models.TriggerManual)
	if err != nil {
		t.Fatalf("approve Gladiator: %v", err)
	}
	if _, err := ex.ProcessQueue(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if a, _ := db.Actions().Get(ctx, acts[0].ID); a.Status != models.ActionSucceeded {
		t.Fatalf("Gladiator disc: %s (%s)", a.Status, a.Message)
	}
	rescanned := false
	for _, r := range env.Requests() {
		if r.Method == http.MethodDelete {
			t.Fatalf("delete sent: %s %s to %s", r.Method, r.Path, r.Server)
		}
		if r.Server == fakemedia.InstanceRadarr && r.Method == http.MethodPost && strings.Contains(r.Path, "/command") {
			rescanned = true
		}
	}
	if !rescanned {
		t.Fatalf("Radarr was not rescanned after its disc clip went (requests %+v)", env.Requests())
	}
	if _, err := os.Lstat(gd.Version.Disc.OwnedEntries[0]); err == nil {
		t.Fatal("the Gladiator disc is still in place")
	}
	env.AssertNoViolations(t)
	env.AssertDiscsIntact(t)

	// Tenet: an unreadable disc is never removable.
	tenet := group("movie:tmdb:577922")
	if _, err := ex.Approve(ctx, tenet.ID, models.TriggerManual); err == nil {
		t.Fatal("an unreadable disc's group was approved")
	}
}

// TestEndToEndDiscSetRemoval: a two-disc set is one version, removed as a whole ("Disc 1" and
// "Disc 2" folders), never the bonus disc next to it; the restore puts both back.
func TestEndToEndDiscSetRemoval(t *testing.T) {
	e := newDiscE2E(t, fakemedia.Options{})
	ctx := context.Background()
	lotr := e.group(t, "movie:tmdb:120")
	d := discOf(t, lotr)
	if d.Decision != models.DecisionRemove || d.Version.Disc.Discs != 2 {
		t.Fatalf("LotR set: %s %+v", d.Decision, d.Version.Disc)
	}
	owned := d.Version.Disc.OwnedEntries
	var bonus string
	for _, f := range e.env.DiscFixtures() {
		if f.Title == lotr.Title && f.Extras {
			bonus = f.LocalRoot
		}
	}
	if bonus == "" {
		t.Fatal("no bonus disc fixture")
	}
	acts, err := e.ex.Approve(ctx, lotr.ID, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ex.ProcessQueue(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := e.db.Actions().Get(ctx, acts[0].ID)
	if a.Status != models.ActionSucceeded || len(strings.Split(a.RecyclePath, "\n")) != len(owned) {
		t.Fatalf("set removal: %s (%s) moved %q", a.Status, a.Message, a.RecyclePath)
	}
	for _, o := range owned {
		if _, err := os.Lstat(o); err == nil {
			t.Fatalf("%s is still in place", o)
		}
	}
	if _, err := os.Stat(bonus); err != nil {
		t.Fatalf("the bonus disc was moved: %v", err)
	}
	e.env.AssertNoViolations(t)
	e.env.AssertDiscsIntact(t)
	if err := e.ex.Restore(ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, o := range owned {
		if _, err := os.Lstat(o); err != nil {
			t.Fatalf("%s was not restored: %v", o, err)
		}
	}
	e.env.AssertDiscsIntact(t)
}

// TestEndToEndCustomScannerDiscRemoval: a disc a custom Plex scanner lists as one version (300
// clip parts) is removed as a whole through the filesystem — never through Plex, which only
// knows the clips.
func TestEndToEndCustomScannerDiscRemoval(t *testing.T) {
	e := newDiscE2E(t, fakemedia.Options{DiscImageScanner: true})
	ctx := context.Background()
	br := e.group(t, "movie:tmdb:335984")
	d := discOf(t, br)
	if d.Decision != models.DecisionRemove || d.Version.Disc.Origin != models.DiscOriginPlex || !strings.HasPrefix(d.Version.Key, "plex:") {
		t.Fatalf("Blade Runner plex disc: %s %+v", d.Decision, d.Version.Disc)
	}
	e.env.ResetRequests()
	acts, err := e.ex.Approve(ctx, br.ID, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || len(acts[0].Paths) != 1 {
		t.Fatalf("a disc action names its root, not its %d parts: %+v", len(d.Version.Parts), acts)
	}
	if _, err := e.ex.ProcessQueue(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := e.db.Actions().Get(ctx, acts[0].ID)
	if a.Status != models.ActionSucceeded || a.Method != models.MethodFilesystem {
		t.Fatalf("plex disc removal: %s via %s (%s)", a.Status, a.Method, a.Message)
	}
	// The only delete is Plex's stale entry, once every one of its parts is gone (D6 cleanup).
	for _, r := range e.env.Requests() {
		if r.Method == http.MethodDelete && (r.Server != fakemedia.ServerPlex || !strings.Contains(a.Message, "removed the stale Plex entry")) {
			t.Fatalf("delete sent: %s %s to %s (%s)", r.Method, r.Path, r.Server, a.Message)
		}
	}
	for _, o := range d.Version.Disc.OwnedEntries {
		if _, err := os.Lstat(o); err == nil {
			t.Fatalf("%s is still in place", o)
		}
	}
	e.env.AssertNoViolations(t)
	e.env.AssertDiscsIntact(t)
}
