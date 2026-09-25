package scanner

import (
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Several Plex servers (docs/DECISIONS.md D11): the real clients against two fakemedia servers.
// The test tree's filesystem is declared by an injected file identity prober (ext4 = inode numbers
// prove different files; anything else = they never do).

const (
	heatTmdb   = 949
	heat4K     = "movies4k/Heat (1995)/Heat (1995) Remux-2160p.mkv"
	heat1080   = "movies/Heat (1995)/Heat (1995) WEBDL-1080p.mkv"
	serverBID  = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	serverBTok = "fAkEpLeXtOkEnBBBBBBB"
)

// heatScenario: one "Movies" library over movies/ and movies4k/ with Heat in 4K and 1080p (neither
// tracked by an *arr), and Radarr knowing the movie.
func heatScenario(name string) *fakemedia.Scenario {
	s := fakemedia.Base(name)
	s.Libraries = []fakemedia.Library{
		{Key: fakemedia.SectionMovies, Title: "Movies", Type: fakemedia.LibraryMovie, Dirs: []string{fakemedia.DirMovies, fakemedia.DirMovies4K}},
		{Key: fakemedia.SectionTV, Title: "TV Shows", Type: fakemedia.LibraryShow, Dirs: []string{fakemedia.DirTV}},
	}
	s.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: "Heat", Year: 1995, TmdbID: heatTmdb, ImdbID: "tt0113277",
		Versions: []fakemedia.Version{
			{Parts: []fakemedia.Part{{File: heat4K, Size: fakemedia.GiB(60)}}, Video: fakemedia.HDR10UHD(),
				Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")}, DurationMs: fakemedia.Mins(170)},
			{Parts: []fakemedia.Part{{File: heat1080, Size: fakemedia.GiB(12)}}, Video: fakemedia.FHD("h264"),
				Audio: []fakemedia.Audio{fakemedia.EAC3("eng", 6)}, DurationMs: fakemedia.Mins(170)},
		},
		Arr: []fakemedia.ArrMovie{{Instance: fakemedia.InstanceRadarr}},
	})
	return s
}

// multiE2E is Dupearr with two Plex servers A and B.
type multiE2E struct {
	h          *harness
	svc        *Service
	a, b       *fakemedia.Env
	srvA, srvB models.MediaServer
	insts      map[string]models.ArrInstance
}

type multiOpts struct {
	bScenario func(base *fakemedia.Scenario) *fakemedia.Scenario
	shared    bool   // B shares A's tree (else its own)
	mapB      bool   // configure B's path mapping
	separate  bool   // B is declared separate storage
	fsType    string // the test tree's filesystem type ("ext4" allowlisted)
	noMapA    bool   // configure no path mapping for A and its *arrs
}

func newMultiE2E(t *testing.T, base *fakemedia.Scenario, o multiOpts) *multiE2E {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	a := fakemedia.Start(t, base)
	bsc := o.bScenario(base)
	var bo fakemedia.Options
	bo.Scenario = bsc
	if o.shared {
		bo.ShareMedia = a
	} else {
		bo.Dir = t.TempDir()
	}
	b := fakemedia.StartWithOptions(t, bo)
	h := newHarness(t)
	deps := h.deps
	deps.Now = time.Now
	deps.PlexFactory = func(s models.MediaServer) PlexClient {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-multi-test", Product: "Dupearr", Version: "test"})
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	deps.FileIdentity = fileid.New(fileid.Hooks{Supported: true, AssumeType: o.fsType})
	m := &multiE2E{h: h, svc: New(deps), a: a, b: b, insts: map[string]models.ArrInstance{}}
	ctx := h.ctx
	// The fixture files were just created: their change times would make every copy too young.
	st := models.DefaultSettings()
	st.MinAgeHours = 0
	h.saveSettings(st)
	m.srvA = models.MediaServer{Name: "Plex A", Kind: models.MediaServerPlex, URL: a.Plex.URL, Token: a.PlexToken, Enabled: true}
	m.srvB = models.MediaServer{Name: "Plex B", Kind: models.MediaServerPlex, URL: b.Plex.URL, Token: b.PlexToken, Enabled: true}
	if o.separate {
		m.srvB.Storage = models.StorageSeparate
	}
	for _, srv := range []*models.MediaServer{&m.srvA, &m.srvB} {
		if err := h.db.MediaServers().Create(ctx, srv); err != nil {
			t.Fatal(err)
		}
		if err := m.svc.SyncLibraries(ctx, srv.ID); err != nil {
			t.Fatalf("sync %s: %v", srv.Name, err)
		}
	}
	for name, s := range a.Instances {
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		inst := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := h.db.ArrInstances().Create(ctx, &inst); err != nil {
			t.Fatal(err)
		}
		m.insts[name] = inst
	}
	for _, mp := range a.PathMappings() {
		if o.noMapA {
			break
		}
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: m.srvA.ID, RemotePath: mp.Remote, LocalPath: mp.Local}
		if mp.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, m.insts[mp.Server].ID
		}
		if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}
	if o.mapB {
		for _, mp := range b.PathMappings() {
			if mp.Server != fakemedia.ServerPlex {
				continue
			}
			pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: m.srvB.ID, RemotePath: mp.Remote, LocalPath: mp.Local}
			if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
				t.Fatal(err)
			}
		}
	}
	return m
}

func (m *multiE2E) scan(t *testing.T) *models.ScanRun {
	t.Helper()
	run, err := m.svc.FullScan(m.h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err != nil {
		t.Fatalf("full scan: %v", err)
	}
	return run
}

// groupOn returns the stored group of Heat on server sid.
func (m *multiE2E) groupOn(t *testing.T, sid int64) *models.DuplicateGroup {
	t.Helper()
	gs, err := m.h.db.Groups().ListByRatingKeys(m.h.ctx, sid, m.ratingKeys(sid))
	if err != nil {
		t.Fatal(err)
	}
	for i := range gs {
		if gs[i].ServerID == sid && gs[i].Status != models.GroupResolved {
			return &gs[i]
		}
	}
	t.Fatalf("no group of Heat on server %d", sid)
	return nil
}

func (m *multiE2E) noGroupOn(t *testing.T, sid int64) {
	t.Helper()
	gs, err := m.h.db.Groups().ListByRatingKeys(m.h.ctx, sid, m.ratingKeys(sid))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.ServerID == sid && g.Status != models.GroupResolved {
			t.Fatalf("unexpected group %q on server %d: %s", g.Key, sid, g.Status)
		}
	}
}

func (m *multiE2E) ratingKeys(sid int64) []string {
	env := m.a
	if sid == m.srvB.ID {
		env = m.b
	}
	return []string{env.RatingKey("", "Heat")}
}

func fileWith(t *testing.T, g *models.DuplicateGroup, rel string) *models.GroupFile {
	t.Helper()
	for i := range g.Files {
		for _, p := range g.Files[i].Version.Parts {
			if strings.HasSuffix(p.Path, rel) {
				return &g.Files[i]
			}
		}
	}
	t.Fatalf("no file %s in %s", rel, g.Key)
	return nil
}

// Scenario C: B lists only the 1080p copy (mapped). A keeps the 4K and would remove the 1080p,
// B's only copy: protected.
func TestMultiServerScenarioC(t *testing.T) {
	m := newMultiE2E(t, heatScenario("scenario-c"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
		},
		shared: true, mapB: true, fsType: "ext4",
	})
	run := m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	f := fileWith(t, g, heat1080)
	if !f.Protected || f.Decision != models.DecisionKeep || !strings.Contains(f.ProtectedReason, `the only copy of "Heat (1995)" on Plex B (Movies)`) {
		t.Fatalf("1080p: %s protected=%v %q", f.Decision, f.Protected, f.ProtectedReason)
	}
	if g.Status != models.GroupProtected {
		t.Fatalf("status %s (%s)", g.Status, g.StatusReason)
	}
	e := f.Version.OtherServers
	if len(e) != 1 || e[0].Match != models.OtherSameFile || e[0].ServerID != m.srvB.ID || len(e[0].Others) != 0 {
		t.Fatalf("listing %+v", e)
	}
	rec := g.CrossServer
	if rec == nil || !rec.Complete || len(rec.Servers) != 2 || len(rec.Libraries) != 1 {
		t.Fatalf("record %+v", rec)
	}
	for _, l := range rec.Libraries {
		if l.ServerID != m.srvB.ID || l.ScannedAt == 0 || l.ContentChangedAt == 0 || l.LibraryID == 0 {
			t.Fatalf("record library %+v", l)
		}
	}
	for _, s := range rec.Servers {
		if s.MachineIdentifier == "" || !s.Mapped || s.Separate {
			t.Fatalf("record server %+v", s)
		}
	}
	m.noGroupOn(t, m.srvB.ID)
	if run.Stats.Errors != 0 {
		t.Fatalf("errors %+v", run.Stats)
	}
	m.a.AssertNoViolations(t)
	m.b.AssertNoViolations(t)
}

// Scenario A: B lists both copies (same share, mapped, same profile). The 4K copy B keeps is proven
// different from the 1080p (ext4): removable, information flag only. On a filesystem type that
// cannot prove it, both copies stay.
func TestMultiServerScenarioA(t *testing.T) {
	for _, fs := range []string{"ext4", "fuse.shfs", "nfs4"} {
		t.Run(fs, func(t *testing.T) {
			m := newMultiE2E(t, heatScenario("scenario-a"), multiOpts{
				bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
					return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok)
				},
				shared: true, mapB: true, fsType: fs,
			})
			m.scan(t)
			for _, sid := range []int64{m.srvA.ID, m.srvB.ID} {
				g := m.groupOn(t, sid)
				f := fileWith(t, g, heat1080)
				if fs == "ext4" {
					if f.Decision != models.DecisionRemove || g.Status != models.GroupPending || !g.HasFlag(models.FlagOtherServerListing) {
						t.Fatalf("server %d: %s %s %v (%s)", sid, f.Decision, g.Status, g.Flags, g.StatusReason)
					}
					if e := f.Version.OtherServers[0]; e.ItemKeepsAnother == nil || !*e.ItemKeepsAnother || e.KeptByGroup == nil || *e.KeptByGroup {
						t.Fatalf("server %d listing %+v", sid, e)
					}
					continue
				}
				if !f.Protected || g.Status != models.GroupProtected || !strings.Contains(f.ProtectedReason, "cannot be proven a different file") {
					t.Fatalf("server %d: %s %v %q", sid, g.Status, f.Protected, f.ProtectedReason)
				}
				if fs == "fuse.shfs" && !strings.Contains(f.ProtectedReason, "Unraid user share") {
					t.Fatalf("hint %q", f.ProtectedReason)
				}
			}
		})
	}
}

// Scenario B: different profiles. A removes the 1080p, which B's group keeps: review.
func TestMultiServerScenarioBKeptByOtherGroup(t *testing.T) {
	m := newMultiE2E(t, heatScenario("scenario-b"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok)
		},
		shared: true, mapB: true, fsType: "ext4",
	})
	// B's library keeps the smallest copy.
	small := models.Profile{Name: "Smallest", KeepCount: 1, Criteria: []models.Criterion{
		{Type: models.CritFileSize, Enabled: true, Direction: models.DirectionLower},
	}}
	if err := m.h.db.Profiles().Create(m.h.ctx, &small); err != nil {
		t.Fatal(err)
	}
	libs, err := m.h.db.Libraries().ListByServer(m.h.ctx, m.srvB.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range libs {
		l.ProfileID = &small.ID
		if err := m.h.db.Libraries().Update(m.h.ctx, &l); err != nil {
			t.Fatal(err)
		}
	}
	m.h.saveSettings(func() models.Settings {
		s := models.DefaultSettings()
		s.MinAgeHours, s.Mode, s.StableScansRequired = 0, models.ModeAuto, 1
		return s
	}())
	m.scan(t)
	a, b := m.groupOn(t, m.srvA.ID), m.groupOn(t, m.srvB.ID)
	if fileWith(t, a, heat1080).Decision != models.DecisionRemove || fileWith(t, b, heat4K).Decision != models.DecisionRemove {
		t.Fatal("unexpected decisions")
	}
	for _, g := range []*models.DuplicateGroup{a, b} {
		if g.Status != models.GroupReview || !g.HasFlag(models.FlagOtherServerKeeps) {
			t.Fatalf("group of server %d: %s %v (%s)", g.ServerID, g.Status, g.Flags, g.StatusReason)
		}
	}
	if len(m.h.auto) != 0 {
		t.Fatalf("auto-approved %v", m.h.auto)
	}
}

// B lists the same file under /srv without a mapping: equal name and size, possibly the same file.
func TestMultiServerUnmappedPossiblySame(t *testing.T) {
	m := newMultiE2E(t, heatScenario("scenario-c-unmapped"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			s := fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
			s.Server.MediaRoot = "/srv"
			return s
		},
		shared: true, fsType: "ext4",
	})
	m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagOtherServerPossible) || !strings.Contains(g.StatusReason, "Plex B lists a file with the same name and size") {
		t.Fatalf("%s %v %q", g.Status, g.Flags, g.StatusReason)
	}
	if e := fileWith(t, g, heat1080).Version.OtherServers; len(e) != 1 || e[0].Match != models.OtherPossiblySame || !strings.HasPrefix(e[0].Path, "/srv/") {
		t.Fatalf("listing %+v", e)
	}
}

// B cannot be read during the scan: A's groups with removals go to review and cannot be approved.
func TestMultiServerUnreadServer(t *testing.T) {
	m := newMultiE2E(t, heatScenario("unread"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			s := fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirTV)
			return s
		},
		shared: true, mapB: true, fsType: "ext4",
	})
	m.b.InjectFault(fakemedia.Fault{Server: fakemedia.ServerPlex, Status: 503})
	m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagOtherServerUnread) || !CrossServerDataMissing(g) ||
		!strings.Contains(g.StatusReason, `could not read the media server "Plex B"`) || g.CrossServer == nil || g.CrossServer.Complete {
		t.Fatalf("%s %v %q %+v", g.Status, g.Flags, g.StatusReason, g.CrossServer)
	}
	// Once B answers, the next scan has complete data.
	m.b.ClearFaults()
	m.scan(t)
	g = m.groupOn(t, m.srvA.ID)
	if g.Status != models.GroupPending || CrossServerDataMissing(g) || !g.CrossServer.Complete {
		t.Fatalf("after B is back: %s %v %q", g.Status, g.Flags, g.StatusReason)
	}
}

// An unsynced library of B (not known to Dupearr) leaves B's files unknown: for a separate B too,
// when the library's folders are mapped (only its mapped folders are compared).
func TestMultiServerUnsyncedLibrary(t *testing.T) {
	for _, separate := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared storage", true: "separate"}[separate], func(t *testing.T) {
			m := newMultiE2E(t, heatScenario("unsynced"), multiOpts{
				bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
					return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok)
				},
				shared: true, mapB: true, separate: separate, fsType: "ext4",
			})
			libs, err := m.h.db.Libraries().ListByServer(m.h.ctx, m.srvB.ID)
			if err != nil {
				t.Fatal(err)
			}
			var keep []models.Library
			for _, l := range libs {
				if l.Type != "movie" {
					keep = append(keep, l)
				}
			}
			if _, err := m.h.db.Libraries().Sync(m.h.ctx, m.srvB.ID, keep); err != nil {
				t.Fatal(err)
			}
			m.scan(t)
			g := m.groupOn(t, m.srvA.ID)
			if !g.HasFlag(models.FlagOtherServerUnread) || !strings.Contains(g.StatusReason, "sync its libraries") ||
				g.CrossServer == nil || g.CrossServer.Complete {
				t.Fatalf("%s %v %q %+v", g.Status, g.Flags, g.StatusReason, g.CrossServer)
			}
			for _, l := range g.CrossServer.Libraries {
				if l.Type == "movie" {
					t.Fatalf("the unsynced library is recorded as compared: %+v", l)
				}
			}
		})
	}
}

// A separate server counts through its mapped folders as its libraries report them now: a library
// moved onto A's folders since Dupearr last synced B's libraries is compared all the same.
func TestMultiServerSeparateStaleFolders(t *testing.T) {
	m := newMultiE2E(t, heatScenario("separate-stale"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
		},
		shared: true, mapB: true, separate: true, fsType: "ext4",
	})
	libs, err := m.h.db.Libraries().ListByServer(m.h.ctx, m.srvB.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range libs {
		l.Locations, l.Enabled = []string{"/data/media/elsewhere"}, false
		if err := m.h.db.Libraries().Update(m.h.ctx, &l); err != nil {
			t.Fatal(err)
		}
	}
	m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	if f := fileWith(t, g, heat1080); !f.Protected || len(f.Version.OtherServers) != 1 || g.Status != models.GroupProtected {
		t.Fatalf("1080p: %s protected=%v %+v (%s)", f.Decision, f.Protected, f.Version.OtherServers, g.Status)
	}
}

// The record only names the libraries the scan listed: a separate server's library that did not
// overlap is left out, so the executor treats it as new if it lists a file to remove later (a
// mapping added since, for instance).
func TestMultiServerRecordOnlyListedLibraries(t *testing.T) {
	m := newMultiE2E(t, heatScenario("record-listed"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
		},
		shared: true, separate: true, fsType: "ext4",
	})
	m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	rec := g.CrossServer
	if rec == nil || !rec.Complete || len(rec.Libraries) != 0 || len(rec.Servers) != 2 {
		t.Fatalf("record %+v", rec)
	}
	for _, s := range rec.Servers {
		if s.ServerID == m.srvA.ID && s.Mappings == "" || s.ServerID == m.srvB.ID && s.Mappings != "" {
			t.Fatalf("mapping fingerprints %+v", rec.Servers)
		}
	}
}

// Scenario D: B on another host (its own disks, the same /data/media layout, declared separate, not
// mapped). The local Radarr (mapped) tracks /data/media/movies/Heat…/1080p; B's mirror of it has
// the same path and size. B's version is never attributed to the local file: tracking unknown.
func TestMultiServerScenarioD(t *testing.T) {
	base := heatScenario("scenario-d")
	base.Movies[0].Versions[1].Tracked = fakemedia.InstanceRadarr
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "links not confirmed", true: "links wrongly confirmed"}[confirmed], func(t *testing.T) {
			m := newMultiE2E(t, base, multiOpts{
				bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
					return fakemedia.MirrorServer(base, "Plex B", serverBID, serverBTok)
				},
				separate: true, fsType: "ext4",
			})
			if confirmed {
				inst := m.insts[fakemedia.InstanceRadarr]
				inst.ServerIDs, inst.LinksConfirmed = []int64{m.srvA.ID, m.srvB.ID}, true
				if err := m.h.db.ArrInstances().Update(m.h.ctx, &inst); err != nil {
					t.Fatal(err)
				}
			}
			m.scan(t)
			g := m.groupOn(t, m.srvB.ID)
			mirror := fileWith(t, g, heat1080)
			if mirror.Version.Arr != nil {
				t.Fatalf("B's mirror attributed to %+v", mirror.Version.Arr)
			}
			if g.Status != models.GroupReview || !ArrTrackingUnknown(g) || !strings.Contains(g.StatusReason, "add a path mapping for Plex B") {
				t.Fatalf("%s %q", g.Status, g.StatusReason)
			}
			// A's own group is matched by mapped paths as before.
			if a := m.groupOn(t, m.srvA.ID); fileWith(t, a, heat1080).Version.Arr == nil {
				t.Fatal("A's 1080p is not matched to its Radarr file")
			}
			// A separate server is never compared with A's files by path or name.
			if a := m.groupOn(t, m.srvA.ID); len(fileWith(t, a, heat1080).Version.OtherServers) != 0 {
				t.Fatalf("separate server compared: %+v", fileWith(t, a, heat1080).Version.OtherServers)
			}
		})
	}
}

// A separate server is not compared with the other servers' files, but its name and size matches
// are counted for the health warning.
func TestMultiServerSeparateNameMatches(t *testing.T) {
	m := newMultiE2E(t, heatScenario("separate"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			s := fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
			s.Server.MediaRoot = "/srv"
			return s
		},
		shared: true, separate: true, fsType: "ext4",
	})
	run := m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	if len(fileWith(t, g, heat1080).Version.OtherServers) != 0 || g.Status != models.GroupPending {
		t.Fatalf("separate server compared: %s %v", g.Status, fileWith(t, g, heat1080).Version.OtherServers)
	}
	if run.Stats.SeparateNameMatches[m.srvB.ID] != 1 {
		t.Fatalf("separate name matches %+v", run.Stats.SeparateNameMatches)
	}
}

// A targeted scan lists every movie and TV library of B (whatever the targeted type: B may list a
// movie in a TV library) and writes a record.
func TestMultiServerTargetedScan(t *testing.T) {
	m := newMultiE2E(t, heatScenario("targeted"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies, fakemedia.DirTV)
		},
		shared: true, mapB: true, fsType: "ext4",
	})
	m.b.ResetRequests()
	if _, err := m.svc.TargetedScan(m.h.ctx, models.TargetedScanBody{ServerID: m.srvA.ID, TmdbID: heatTmdb}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range m.b.Requests() {
		for _, key := range []string{fakemedia.SectionMovies, fakemedia.SectionTV} {
			listed[key] = listed[key] || strings.HasPrefix(r.Path, "/library/sections/"+key+"/all")
		}
	}
	if !listed[fakemedia.SectionMovies] || !listed[fakemedia.SectionTV] {
		t.Fatalf("B's libraries listed: %v", listed)
	}
	g := m.groupOn(t, m.srvA.ID)
	if g.CrossServer == nil || !g.CrossServer.Complete || !fileWith(t, g, heat1080).Protected {
		t.Fatalf("record %+v, 1080p protected=%v", g.CrossServer, fileWith(t, g, heat1080).Protected)
	}
}

// B lists A's 1080p file in a TV library (as an episode): a targeted scan of the movie keeps B's
// listing that the full scan found, and the record names B's TV library.
func TestMultiServerTargetedScanCrossType(t *testing.T) {
	m := newMultiE2E(t, heatScenario("targeted-cross-type"), multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			s := fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirMovies)
			s.Libraries = []fakemedia.Library{{Key: fakemedia.SectionTV, Title: "Everything", Type: fakemedia.LibraryShow, Dirs: []string{fakemedia.DirMovies}}}
			v := s.Movies[0].Versions[0]
			s.Movies = nil
			s.AddShow(fakemedia.Show{Section: fakemedia.SectionTV, Title: "Heat", Year: 1995, TvdbID: 777, Folder: "movies/Heat (1995)",
				Episodes: []fakemedia.Episode{{Season: 1, Episode: 1, Title: "Heat", Versions: []fakemedia.Version{v}}}})
			return s
		},
		shared: true, mapB: true, fsType: "ext4",
	})
	check := func(when string) {
		t.Helper()
		g := m.groupOn(t, m.srvA.ID)
		f := fileWith(t, g, heat1080)
		if !f.Protected || f.Decision != models.DecisionKeep || len(f.Version.OtherServers) != 1 || g.Status != models.GroupProtected {
			t.Fatalf("%s: 1080p %s protected=%v %+v (%s)", when, f.Decision, f.Protected, f.Version.OtherServers, g.Status)
		}
		if rec := g.CrossServer; rec == nil || !rec.Complete || len(rec.Libraries) != 1 || rec.Libraries[0].Type != "show" {
			t.Fatalf("%s: record %+v", when, rec)
		}
	}
	m.scan(t)
	check("full scan")
	if _, err := m.svc.TargetedScan(m.h.ctx, models.TargetedScanBody{ServerID: m.srvA.ID, TmdbID: heatTmdb}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	check("targeted scan")
}

// Nothing is mapped and the *arr links are not confirmed: rule 1 cannot decide, so versions of a
// title the instance tracks have an unknown tracking (review) until a person confirms the links.
// Confirmed links re-enable raw-path matching for the linked server only.
func TestMultiServerUnconfirmedLinks(t *testing.T) {
	base := heatScenario("unconfirmed")
	base.Movies[0].Versions[1].Tracked = fakemedia.InstanceRadarr
	m := newMultiE2E(t, base, multiOpts{
		bScenario: func(base *fakemedia.Scenario) *fakemedia.Scenario {
			return fakemedia.SharedServer(base, "Plex B", serverBID, serverBTok, fakemedia.DirTV)
		},
		shared: true, fsType: "ext4", noMapA: true,
	})
	m.scan(t)
	g := m.groupOn(t, m.srvA.ID)
	if !ArrTrackingUnknown(g) || !strings.Contains(g.StatusReason, "confirm which Plex servers radarr feeds") ||
		fileWith(t, g, heat1080).Version.Arr != nil {
		t.Fatalf("before confirming: %s %q", g.Status, g.StatusReason)
	}
	for name, inst := range m.insts {
		inst.LinksConfirmed = true
		if name == fakemedia.InstanceRadarr {
			inst.ServerIDs = []int64{m.srvA.ID}
		}
		if err := m.h.db.ArrInstances().Update(m.h.ctx, &inst); err != nil {
			t.Fatal(err)
		}
	}
	m.scan(t)
	g = m.groupOn(t, m.srvA.ID)
	if ArrTrackingUnknown(g) || fileWith(t, g, heat1080).Version.Arr == nil {
		t.Fatalf("after confirming: %s %q", g.Status, g.StatusReason)
	}
	// Linked to server B only: A's versions are not matched by raw path, and A's tracked title is
	// no longer unknown for an instance whose links are confirmed (it simply does not feed A).
	inst := m.insts[fakemedia.InstanceRadarr]
	inst.ServerIDs, inst.LinksConfirmed = []int64{m.srvB.ID}, true
	if err := m.h.db.ArrInstances().Update(m.h.ctx, &inst); err != nil {
		t.Fatal(err)
	}
	m.scan(t)
	g = m.groupOn(t, m.srvA.ID)
	if ArrTrackingUnknown(g) || fileWith(t, g, heat1080).Version.Arr != nil {
		t.Fatalf("linked elsewhere: %s %q", g.Status, g.StatusReason)
	}
	// Confirmed without any enabled server is no confirmation: unknown again.
	inst.ServerIDs = []int64{}
	if err := m.h.db.ArrInstances().Update(m.h.ctx, &inst); err != nil {
		t.Fatal(err)
	}
	m.scan(t)
	if g = m.groupOn(t, m.srvA.ID); !ArrTrackingUnknown(g) {
		t.Fatalf("confirmed without a server: %s %q", g.Status, g.StatusReason)
	}
}
