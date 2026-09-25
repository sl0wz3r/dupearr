package executor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Jellyfin end to end in-process (docs/DECISIONS.md D12): the scanner and the executor with the
// real clients against the fake Jellyfin 12.1 serving the Appendix A tree.

type jfWorld struct {
	t    *testing.T
	ctx  context.Context
	db   *database.DB
	sc   *scanner.Service
	ex   *Service
	env  *fakemedia.Env
	srv  models.MediaServer
	plex models.MediaServer // with jfOpts.plex: the fake Plex over the same tree
	arrs map[string]models.ArrInstance
	bin  string
}

type jfOpts struct {
	scenario func(*fakemedia.Scenario)
	settings func(*models.Settings)
	noMap    bool // no server path mapping for Jellyfin
	noID     bool // store the server without its identity
	plex     bool // also the fake Plex server of the same tree (docs/DECISIONS.md D11)
}

func newJFWorld(t *testing.T, o jfOpts) *jfWorld {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	sc := fakemedia.JellyfinAppendixA()
	if o.scenario != nil {
		o.scenario(sc)
	}
	env := fakemedia.Start(t, sc)
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		t.Fatal(err)
	}
	factory := func(s models.MediaServer) mediaserver.Client {
		switch {
		case s.Kind == models.MediaServerJellyfin:
			return jellyfin.New(s.URL, s.Token, jellyfin.Options{DeviceID: "dupearr-test", Timeout: 10 * time.Second})
		case s.Kind.IsPlex():
			return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-test", Product: "Dupearr", Version: "test"})
		}
		return nil
	}
	arrFactory := func(a models.ArrInstance) *arr.Client { return arr.New(a, arr.Options{}) }
	ids := fileid.New(fileid.Hooks{Supported: true, AssumeType: "ext4"})
	bus := events.New()
	w := &jfWorld{t: t, ctx: ctx, db: db, env: env, arrs: map[string]models.ArrInstance{}}
	w.sc = scanner.New(scanner.Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now, FileIdentity: ids,
		MediaServerFactory: factory,
		ArrFactory:         func(a models.ArrInstance) scanner.ArrClient { return arrFactory(a) },
	})
	w.ex = New(Deps{
		Store: db, Bus: bus, Log: slog.New(slog.DiscardHandler), Now: time.Now, FileIdentity: ids,
		MediaServerFactory: factory,
		ArrFactory:         func(a models.ArrInstance) ArrClient { return arrFactory(a) },
		Enqueue:            func(context.Context, string, any, string) error { return nil },
	})
	w.ex.goneWait, w.ex.goneStep = 2*time.Second, 20*time.Millisecond
	st := models.DefaultSettings()
	st.DryRun, st.MinAgeHours = false, 0
	w.bin = filepath.Join(t.TempDir(), "recycle")
	st.RecycleBinPath = w.bin
	if o.settings != nil {
		o.settings(&st)
	}
	if err := db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	w.srv = models.MediaServer{Name: "Jelly", Kind: models.MediaServerJellyfin, URL: env.Jellyfin.URL, Token: env.JellyfinAPIKey, Enabled: true,
		MachineIdentifier: env.JellyfinServerID}
	if o.noID {
		w.srv.MachineIdentifier = ""
	}
	if err := db.MediaServers().Create(ctx, &w.srv); err != nil {
		t.Fatal(err)
	}
	if err := w.sc.SyncLibraries(ctx, w.srv.ID); err != nil {
		t.Fatal(err)
	}
	if o.noID {
		// A sync adopts the identity; take it away again (a server force-saved while unreachable).
		w.srv.MachineIdentifier = ""
		if err := db.MediaServers().Update(ctx, &w.srv); err != nil {
			t.Fatal(err)
		}
	}
	if o.plex {
		w.plex = models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
		if err := db.MediaServers().Create(ctx, &w.plex); err != nil {
			t.Fatal(err)
		}
		if err := w.sc.SyncLibraries(ctx, w.plex.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{fakemedia.InstanceRadarr, fakemedia.InstanceSonarr} {
		s := env.Instances[name]
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		a := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		w.arrs[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{RemotePath: m.Remote, LocalPath: m.Local}
		switch {
		case m.Server == fakemedia.ServerJellyfin && !o.noMap:
			pm.SourceType, pm.SourceID = models.PathSourceServer, w.srv.ID
		case m.Server == fakemedia.ServerPlex && o.plex:
			pm.SourceType, pm.SourceID = models.PathSourceServer, w.plex.ID
		case m.Server == fakemedia.InstanceRadarr || m.Server == fakemedia.InstanceSonarr:
			pm.SourceType, pm.SourceID = models.PathSourceArr, w.arrs[m.Server].ID
		default:
			continue
		}
		if err := db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { env.AssertNoViolations(t) })
	return w
}

func (w *jfWorld) scan() {
	w.t.Helper()
	if _, err := w.sc.FullScan(w.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		w.t.Fatal(err)
	}
}

// group returns the Jellyfin group holding the version whose first file is rel.
func (w *jfWorld) group(rel string) *models.DuplicateGroup {
	w.t.Helper()
	return w.groupOf(w.srv.ID, rel)
}

// groupOf returns server sid's group holding the version whose first file is rel.
func (w *jfWorld) groupOf(sid int64, rel string) *models.DuplicateGroup {
	w.t.Helper()
	page, err := w.db.Groups().List(w.ctx, store.GroupFilter{ServerID: sid}, store.Paging{Page: 1, PageSize: 500})
	if err != nil {
		w.t.Fatal(err)
	}
	want := w.env.JellyfinMediaPath(rel)
	for i := range page.Records {
		for _, f := range page.Records[i].Files {
			if len(f.Version.Parts) > 0 && f.Version.Parts[0].Path == want {
				return &page.Records[i]
			}
		}
	}
	w.t.Fatalf("no group lists %s", rel)
	return nil
}

func (w *jfWorld) approve(g *models.DuplicateGroup) {
	w.t.Helper()
	if _, err := w.ex.Approve(w.ctx, g.ID, models.TriggerManual); err != nil {
		w.t.Fatalf("approve %s: %v", g.Title, err)
	}
}

func (w *jfWorld) process() Summary {
	w.t.Helper()
	sum, err := w.ex.ProcessQueue(w.ctx, nil)
	if err != nil && !errors.Is(err, ErrAborted) {
		w.t.Fatal(err)
	}
	return sum
}

func (w *jfWorld) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(w.env.MediaRoot, filepath.FromSlash(rel)))
	return err == nil
}

func (w *jfWorld) reload(g *models.DuplicateGroup) *models.DuplicateGroup {
	w.t.Helper()
	n, err := w.db.Groups().Get(w.ctx, g.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *jfWorld) actions(g *models.DuplicateGroup) []models.Action {
	w.t.Helper()
	as, err := w.db.Actions().ListByGroup(w.ctx, g.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return as
}

// TestJellyfinRemovalIntoRecycleBin: the Alpha 1080p loser goes into Dupearr's bin by the
// filesystem method; the keeper, the sidecars and the unrelated file stay; Jellyfin is told the
// exact path once, after its identity was confirmed; nothing goes through Jellyfin.
func TestJellyfinRemovalIntoRecycleBin(t *testing.T) {
	w := newJFWorld(t, jfOpts{})
	w.scan()
	g := w.group(fakemedia.Alpha1080)
	if g.Status != models.GroupPending || !g.HasFlag(models.FlagManualOnly) || g.HasFlag(models.FlagReportOnly) {
		t.Fatalf("alpha: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	for _, f := range g.Files {
		if !strings.HasPrefix(f.Version.Key, "jellyfin:") || f.Version.SourceID == "" || f.Version.MediaID != 0 {
			t.Fatalf("version %+v", f.Version)
		}
	}
	if _, err := w.ex.Approve(w.ctx, g.ID, models.TriggerScheduled); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("automatic approval: %v", err)
	}
	w.approve(g)
	if sum := w.process(); sum.Succeeded != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if w.exists(fakemedia.Alpha1080) || !w.exists(fakemedia.Alpha2160) || !w.exists(fakemedia.AlphaMarker) ||
		!w.exists(fakemedia.AlphaDir+"/Alpha (2020) [tmdbid-603] - 1080p.en.srt") {
		t.Fatal("the wrong files moved")
	}
	a := w.actions(g)[0]
	if a.Method != models.MethodFilesystem || !strings.Contains(a.RecyclePath, w.bin) || !strings.Contains(a.Message, "reported the removed file to Jellyfin") {
		t.Fatalf("action %+v", a)
	}
	n := w.env.JellyfinNotifications()
	if len(n) != 1 || len(n[0].Paths) != 1 || n[0].Paths[0] != w.env.JellyfinMediaPath(fakemedia.Alpha1080) || n[0].UpdateType[0] != "Deleted" {
		t.Fatalf("notifications %+v", n)
	}
	for _, name := range []string{".ignore", ".plexignore", ".dupearr-recycle-bin"} {
		if _, err := os.Stat(filepath.Join(w.bin, name)); err != nil {
			t.Errorf("the bin lacks %s: %v", name, err)
		}
	}
	// The server still lists the copy until its monitor re-reads the folder: a re-scan resolves the
	// group by the local files, not by Jellyfin's listing.
	if !w.env.JellyfinListed(fakemedia.Alpha1080) {
		t.Fatal("the fake dropped the ghost before its delay")
	}
	w.scan()
	if g2 := w.reload(g); g2.Status != models.GroupResolved {
		t.Fatalf("after the re-scan: %s %q", g2.Status, g2.StatusReason)
	}
	// A restore brings the file back and tells Jellyfin it was created.
	if err := w.ex.Restore(w.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if !w.exists(fakemedia.Alpha1080) {
		t.Fatal("restore did not bring the file back")
	}
	if n := w.env.JellyfinNotifications(); len(n) != 2 || n[1].UpdateType[0] != "Created" {
		t.Fatalf("notifications after the restore %+v", n)
	}
}

// TestJellyfinArrMethodNeedsARecycleBin: a tracked loser is removed through its *arr only into
// the *arr's recycle bin; without one it is refused, and never removed by the filesystem method.
func TestJellyfinArrMethodNeedsARecycleBin(t *testing.T) {
	track := func(sc *fakemedia.Scenario) {
		for i := range sc.Movies {
			for j := range sc.Movies[i].Versions {
				if sc.Movies[i].Versions[j].Parts[0].File == fakemedia.Alpha1080 {
					sc.Movies[i].Versions[j].Tracked = fakemedia.InstanceRadarr
				}
			}
		}
	}
	t.Run("permanent", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{scenario: track})
		w.scan()
		g := w.group(fakemedia.Alpha1080)
		w.approve(g)
		w.process()
		if !w.exists(fakemedia.Alpha1080) {
			t.Fatal("the tracked file was removed without a recycle bin")
		}
		if a := w.actions(g)[0]; a.Status != models.ActionFailed || !strings.Contains(a.Message, "only removed into a recycle bin") {
			t.Fatalf("action %+v", a)
		}
	})
	t.Run("with its recycle bin", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{scenario: func(sc *fakemedia.Scenario) {
			track(sc)
			sc.Instance(fakemedia.InstanceRadarr).RecycleBin = fakemedia.RemoteRoot + "/recycle/radarr"
		}})
		w.scan()
		g := w.group(fakemedia.Alpha1080)
		w.approve(g)
		if sum := w.process(); sum.Succeeded != 1 {
			t.Fatalf("summary %+v", sum)
		}
		if a := w.actions(g)[0]; a.Method != models.MethodArr || w.exists(fakemedia.Alpha1080) || !w.exists(fakemedia.Alpha2160) {
			t.Fatalf("action %+v", a)
		}
	})
}

// TestJellyfinFilesystemMethodNeedsDupearrsBin: without Dupearr's bin the filesystem method is
// not available for a Jellyfin copy.
func TestJellyfinFilesystemMethodNeedsDupearrsBin(t *testing.T) {
	w := newJFWorld(t, jfOpts{settings: func(st *models.Settings) { st.RecycleBinPath = "" }})
	w.scan()
	g := w.group(fakemedia.Alpha1080)
	w.approve(g)
	w.process()
	a := w.actions(g)[0]
	if w.exists(fakemedia.Alpha1080) == false || a.Status != models.ActionFailed ||
		!strings.Contains(a.Message, "only removed into a recycle bin") || !strings.Contains(a.Message, "never deletes through Jellyfin") {
		t.Fatalf("action %+v", a)
	}
}

// TestJellyfinUnmappedGroupsAreReportOnly: without a path mapping every group is report-only and
// cannot be approved.
func TestJellyfinUnmappedGroupsAreReportOnly(t *testing.T) {
	w := newJFWorld(t, jfOpts{noMap: true})
	w.scan()
	g := w.group(fakemedia.Alpha1080)
	if g.Status != models.GroupProtected || !g.HasFlag(models.FlagReportOnly) || !strings.Contains(g.StatusReason, "no path mapping") {
		t.Fatalf("alpha: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	if _, err := w.ex.Approve(w.ctx, g.ID, models.TriggerManual); err == nil {
		t.Fatal("a report-only group was approved")
	}
}

// TestJellyfinPlayingDefers (S13): a session playing the alternate (by source id) or part 2 of a
// stacked alternate (by the part's item id) defers the group.
func TestJellyfinPlayingDefers(t *testing.T) {
	w := newJFWorld(t, jfOpts{})
	w.scan()
	alpha := w.group(fakemedia.Alpha1080)
	w.env.SetJellyfinSessions(fakemedia.JellyfinSession{ItemID: w.env.JellyfinRowID(fakemedia.Alpha2160),
		MediaSourceID: w.env.JellyfinSourceID(fakemedia.Alpha1080), Paused: true})
	w.approve(alpha)
	if sum := w.process(); sum.Deferred != 1 || !w.exists(fakemedia.Alpha1080) {
		t.Fatalf("playing alternate: %+v", sum)
	}
	kappa := w.group(fakemedia.KappaMain)
	w.env.SetJellyfinSessions(fakemedia.JellyfinSession{ItemID: w.env.JellyfinPartID(fakemedia.KappaCD2)})
	w.approve(kappa)
	sum := w.process() // Alpha is no longer playing: it goes; Kappa's part 2 plays
	if sum.Deferred != 1 || sum.Succeeded != 1 || w.exists(fakemedia.Alpha1080) || !w.exists(fakemedia.KappaCD1) || !w.exists(fakemedia.KappaCD2) {
		t.Fatalf("playing part 2: %+v", sum)
	}
	// Nothing plays: the stacked loser moves with both parts.
	w.env.SetJellyfinSessions()
	w.process()
	if w.exists(fakemedia.KappaCD1) || w.exists(fakemedia.KappaCD2) || !w.exists(fakemedia.KappaMain) {
		t.Fatal("the stacked loser was not moved whole")
	}
}

// TestJellyfinStackedKeeperNeedsEveryPart: a keeper whose cd2 is missing on disk is not a
// confirmed copy, so nothing is removed.
func TestJellyfinStackedKeeperNeedsEveryPart(t *testing.T) {
	w := newJFWorld(t, jfOpts{scenario: func(sc *fakemedia.Scenario) {
		// Make the stacked 720p alternate the better copy.
		for i := range sc.Movies {
			if sc.Movies[i].Title == "Kappa" {
				sc.Movies[i].Versions[0].Video = fakemedia.SD("mpeg2video", 720, 480)
				sc.Movies[i].Versions[1].Video = fakemedia.HDR10UHD()
			}
		}
	}})
	w.scan()
	g := w.group(fakemedia.KappaMain)
	var keeper models.GroupFile
	for _, f := range g.Files {
		if f.Decision == models.DecisionKeep {
			keeper = f
		}
	}
	if len(keeper.Version.Parts) != 2 {
		t.Fatalf("keeper %+v", keeper.Version)
	}
	w.approve(g)
	if err := os.Remove(filepath.Join(w.env.MediaRoot, filepath.FromSlash(fakemedia.KappaCD2))); err != nil {
		t.Fatal(err)
	}
	w.process()
	if !w.exists(fakemedia.KappaMain) {
		t.Fatal("the loser was removed although the keeper's cd2 is missing")
	}
	if g2 := w.reload(g); g2.Status != models.GroupReview {
		t.Fatalf("group %s %q", g2.Status, g2.StatusReason)
	}
}

// TestJellyfinServerIdentity (S23): a server stored without its id is never acted on; a changed id
// skips the group.
func TestJellyfinServerIdentity(t *testing.T) {
	t.Run("stored without its id", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{noID: true})
		w.scan()
		// The scan adopts the identity (like a sync); take it away before the run.
		srv, _ := w.db.MediaServers().Get(w.ctx, w.srv.ID)
		g := w.group(fakemedia.Alpha1080)
		w.approve(g)
		srv.MachineIdentifier = ""
		if err := w.db.MediaServers().Update(w.ctx, srv); err != nil {
			t.Fatal(err)
		}
		w.process()
		if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(g).StatusReason, "without its identity") {
			t.Fatalf("group %q", w.reload(g).StatusReason)
		}
	})
	t.Run("changed id", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{})
		w.scan()
		g := w.group(fakemedia.Alpha1080)
		w.approve(g)
		w.env.SetJellyfinServerID("0000000000000000000000000000beef")
		w.process()
		if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(g).StatusReason, "answers as Jellyfin server") {
			t.Fatalf("group %q", w.reload(g).StatusReason)
		}
	})
}

// TestJellyfinPathSubstitutions (S12): substitutions set after an approval stop the removal; a
// re-scan then lists nothing of the server (Jellyfin rewrites the paths it reports) and leaves the
// group as the run left it (in review with the reason), never resolved.
func TestJellyfinPathSubstitutions(t *testing.T) {
	w := newJFWorld(t, jfOpts{})
	w.scan()
	g := w.group(fakemedia.Alpha1080)
	w.approve(g)
	w.env.SetJellyfinPathSubstitutions(fakemedia.JellyfinPathSubstitution{From: "/data/media", To: `\\nas\media`})
	w.process()
	if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(g).StatusReason, "path substitutions") {
		t.Fatalf("group %q", w.reload(g).StatusReason)
	}
	if _, err := w.sc.FullScan(w.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err == nil || !strings.Contains(err.Error(), "path substitutions") {
		t.Fatalf("re-scan: err = %v", err)
	}
	if g2 := w.reload(g); g2.Status != models.GroupReview || !strings.Contains(g2.StatusReason, "path substitutions") {
		t.Fatalf("after the re-scan: %s %q", g2.Status, g2.StatusReason)
	}
}

// TestJellyfinShortcutAndMultiEpisode: the Lambda group (a local .strm) is report-only, and the
// multi-episode file hidden behind episode 3 is never removed, whichever version the profile
// prefers; with it kept, only the single-episode file moves.
func TestJellyfinShortcutAndMultiEpisode(t *testing.T) {
	w := newJFWorld(t, jfOpts{scenario: func(sc *fakemedia.Scenario) {
		// A second 1080p copy of Lambda makes it a group (the .strm alone is never a version).
		sc.Movies = append(sc.Movies, fakemedia.Movie{Section: fakemedia.SectionMovies, Title: "Lambda", Year: 2019, TmdbID: 621,
			Versions: []fakemedia.Version{{Parts: []fakemedia.Part{{File: fakemedia.LambdaDir + "/Lambda (2019) - 720p.mkv", Size: fakemedia.GiB(3)}},
				Video: fakemedia.HD("h264"), Audio: []fakemedia.Audio{fakemedia.AAC("eng", 2)}, DurationMs: fakemedia.Mins(100)}}})
	}})
	w.scan()
	lambda := w.group(fakemedia.Lambda1080)
	if lambda.Status != models.GroupProtected || !strings.Contains(lambda.StatusReason, ".strm") {
		t.Fatalf("lambda: %s %q", lambda.Status, lambda.StatusReason)
	}
	for _, f := range lambda.Files {
		if strings.HasSuffix(f.Version.Parts[0].Path, ".strm") {
			t.Fatal("a .strm is a version")
		}
	}
	e3 := w.group(fakemedia.ShowS01E03)
	for _, f := range e3.Files {
		if strings.HasSuffix(f.Version.Parts[0].Path, "S01E03-E04.mkv") && (f.Decision != models.DecisionKeep || !f.Protected) {
			t.Fatalf("the multi-episode file: %+v", f)
		}
	}
	if e3.Status == models.GroupPending {
		w.approve(e3)
		w.process()
	}
	if !w.exists(fakemedia.ShowS01E0304) {
		t.Fatal("the multi-episode file was removed")
	}
}

// TestJellyfinAndPlexOnOneShare (docs/DECISIONS.md D11 across kinds): a Plex server and a Jellyfin
// server over the same files protect each other's copies. The Plex removal relies on the Jellyfin
// item keeping another copy (matched by version key, proven a different file); the Jellyfin
// libraries' change signal is their listing fingerprint; a Jellyfin session on a stack part of the
// listing defers; a Jellyfin group keeping the file refuses the Plex removal.
func TestJellyfinAndPlexOnOneShare(t *testing.T) {
	t.Run("removal relies on the Jellyfin item's other copy", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{plex: true})
		w.scan()
		pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
		if pg.Status != models.GroupPending || !pg.HasFlag(models.FlagOtherServerListing) || pg.CrossServer == nil || !pg.CrossServer.Complete {
			t.Fatalf("plex alpha: %s %v %q %+v", pg.Status, pg.Flags, pg.StatusReason, pg.CrossServer)
		}
		fingerprints := 0
		for _, l := range pg.CrossServer.Libraries {
			if l.ServerID == w.srv.ID && l.Fingerprint != "" {
				fingerprints++
			}
			if l.ServerID == w.plex.ID && l.Fingerprint != "" {
				t.Fatal("a Plex library recorded a fingerprint")
			}
		}
		if fingerprints == 0 {
			t.Fatal("no Jellyfin library recorded its listing fingerprint")
		}
		w.approve(pg)
		if sum := w.process(); sum.Succeeded != 1 {
			t.Fatalf("summary %+v; group %q", sum, w.reload(pg).StatusReason)
		}
		if w.exists(fakemedia.Alpha1080) || !w.exists(fakemedia.Alpha2160) {
			t.Fatal("the wrong copy went")
		}
	})
	t.Run("a changed Jellyfin library sends it to review", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{plex: true})
		w.scan()
		pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
		w.approve(pg)
		if err := w.env.CreateFile("movies/Omicron (2022)/Omicron (2022).mkv", fakemedia.GiB(1)); err != nil {
			t.Fatal(err)
		}
		w.env.JellyfinScan()
		w.process()
		if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(pg).StatusReason, "changed since the scan") {
			t.Fatalf("group %s %q", w.reload(pg).Status, w.reload(pg).StatusReason)
		}
	})
	t.Run("a Jellyfin session on a listed stack part defers", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{plex: true, scenario: func(sc *fakemedia.Scenario) {
			// The stacked 720p copy of Kappa is the Plex loser.
			for i := range sc.Movies {
				if sc.Movies[i].Title == "Kappa" {
					sc.Movies[i].Versions[0].Video = fakemedia.HDR10UHD()
				}
			}
		}})
		w.scan()
		pg := w.groupOf(w.plex.ID, fakemedia.KappaMain)
		w.approve(pg)
		w.env.SetJellyfinSessions(fakemedia.JellyfinSession{ItemID: w.env.JellyfinPartID(fakemedia.KappaCD2)})
		if sum := w.process(); sum.Deferred != 1 || !w.exists(fakemedia.KappaCD2) {
			t.Fatalf("summary %+v; group %q", sum, w.reload(pg).StatusReason)
		}
	})
	t.Run("a Jellyfin group keeping the file refuses the Plex removal", func(t *testing.T) {
		w := newJFWorld(t, jfOpts{plex: true})
		w.scan()
		jg := w.group(fakemedia.Alpha1080)
		for _, f := range jg.Files {
			d := models.DecisionRemove
			if f.Version.Parts[0].Path == w.env.JellyfinMediaPath(fakemedia.Alpha1080) {
				d = models.DecisionKeep
			}
			if err := w.db.Groups().SetOverride(w.ctx, jg.ID, f.ID, d); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := w.sc.Reevaluate(w.ctx, jg.ID); err != nil {
			t.Fatal(err)
		}
		pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
		w.approve(pg)
		w.process()
		if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(pg).StatusReason, "kept by a duplicate group") {
			t.Fatalf("group %s %q", w.reload(pg).Status, w.reload(pg).StatusReason)
		}
	})
}

// TestJellyfinLibrariesDupearrNeverLists (docs/DECISIONS.md D11, D12): a Jellyfin library of mixed
// content (or home videos, music videos) is never listed, so it may list any file: the Plex group
// is not removed when such a library appears after the scan, and a scan counts the server as
// unread for it.
func TestJellyfinLibrariesDupearrNeverLists(t *testing.T) {
	w := newJFWorld(t, jfOpts{plex: true})
	w.scan()
	pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
	w.approve(pg)
	w.env.AddJellyfinLibrary(fakemedia.JellyfinLibrary{Name: "Everything", Dirs: []string{fakemedia.DirMovies}})
	w.process()
	if !w.exists(fakemedia.Alpha1080) || !strings.Contains(w.reload(pg).StatusReason, "only reads movie and TV libraries") {
		t.Fatalf("group %s %q", w.reload(pg).Status, w.reload(pg).StatusReason)
	}
	w.scan()
	if g := w.reload(pg); g.Status != models.GroupReview || !g.HasFlag(models.FlagOtherServerUnread) || g.CrossServer == nil || g.CrossServer.Complete {
		t.Fatalf("after the re-scan: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	if _, err := w.ex.Approve(w.ctx, pg.ID, models.TriggerManual); err == nil {
		// The API refuses it (CrossServerDataMissing); the executor refuses the incomplete record.
		w.process()
		if !w.exists(fakemedia.Alpha1080) {
			t.Fatal("a file a mixed Jellyfin library may list was removed")
		}
	}
}

// TestJellyfinGateOfAnotherServer (S14): a Jellyfin credential that lost its administrator proof
// after the scan sees only its own sessions, so a Plex removal of a file Jellyfin lists is not
// made on its word.
func TestJellyfinGateOfAnotherServer(t *testing.T) {
	w := newJFWorld(t, jfOpts{plex: true})
	w.scan()
	pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
	w.approve(pg)
	w.srv.Token = w.env.JellyfinUserToken
	if err := w.db.MediaServers().Update(w.ctx, &w.srv); err != nil {
		t.Fatal(err)
	}
	w.process()
	if g := w.reload(pg); !w.exists(fakemedia.Alpha1080) || g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "administrator") {
		t.Fatalf("group %s %q", g.Status, g.StatusReason)
	}
}

// TestJellyfinMergedTitleOfOneLibrary: a title merged from two copies of one library and a primary
// in another (listed as two rows, live on 12.1) is one Jellyfin item with both copies, and its
// libraries are read completely, so the Plex groups keep their complete cross-server record.
func TestJellyfinMergedTitleOfOneLibrary(t *testing.T) {
	remux := "movies/Zeta Remux (2017)/Zeta Remux (2017).mkv"
	w := newJFWorld(t, jfOpts{plex: true, scenario: func(sc *fakemedia.Scenario) {
		sc.AddMovie(fakemedia.Movie{Section: fakemedia.SectionMovies, Title: "Zeta Remux", Year: 2017, TmdbID: 606, Versions: []fakemedia.Version{{
			Parts: []fakemedia.Part{{File: remux, Size: fakemedia.GiB(20)}}, Video: fakemedia.FHD("h264"), Audio: []fakemedia.Audio{fakemedia.AAC("eng", 2)},
			DurationMs: fakemedia.Mins(100)}}})
		sc.Jellyfin.Merges = [][]string{{fakemedia.Zeta4K, fakemedia.ZetaMovies, remux}}
	}})
	w.scan()
	jg := w.groupOf(w.srv.ID, fakemedia.ZetaMovies)
	if len(jg.Files) != 2 {
		t.Fatalf("the merged title: %d copies", len(jg.Files))
	}
	pg := w.groupOf(w.plex.ID, fakemedia.Alpha2160)
	if pg.Status != models.GroupPending || pg.CrossServer == nil || !pg.CrossServer.Complete {
		t.Fatalf("plex alpha: %s %v %q", pg.Status, pg.Flags, pg.StatusReason)
	}
}
