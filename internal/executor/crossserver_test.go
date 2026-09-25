package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Several Plex servers (docs/DECISIONS.md D11): the run-time checks of a group whose files another
// server lists. Server A is the test env's server; server B is a second fake Plex over the same
// media root (both mapped).

const (
	serverBMachine = "b-machine-0000"
	xRel           = "movies/Heat (1995)/Heat 2160p.mkv"
	yRel           = "movies/Heat (1995)/Heat 1080p.mkv"
)

type multiEnv struct {
	*testEnv
	serverB models.MediaServer
	plexB   *fakePlex
}

// newMultiEnv adds server B (mapped, one movie library over the media root) with a fake Plex whose
// item 900 lists Y (media 9002) and X (media 9001), and declares the tree ext4.
func newMultiEnv(t *testing.T) *multiEnv {
	t.Helper()
	e := newEnv(t)
	m := &multiEnv{testEnv: e}
	m.serverB = models.MediaServer{Name: "Plex B", Kind: models.MediaServerPlex, URL: "http://plexb.invalid:32400", Token: "b-token",
		MachineIdentifier: serverBMachine, Enabled: true}
	if err := e.db.MediaServers().Create(e.ctx, &m.serverB); err != nil {
		t.Fatal(err)
	}
	if err := e.db.PathMappings().Create(e.ctx, &models.PathMapping{SourceType: models.PathSourceServer, SourceID: m.serverB.ID,
		RemotePath: remoteRoot, LocalPath: e.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Libraries().Sync(e.ctx, m.serverB.ID, []models.Library{
		{SectionKey: "1", Title: "Movies B", Type: "movie", Locations: []string{remoteRoot}},
	}); err != nil {
		t.Fatal(err)
	}
	m.plexB = &fakePlex{log: e.log, root: e.root, items: map[string]*models.MediaItem{}, allowed: true,
		exists: map[string]bool{}, sizes: map[string]int64{}, itemErr: map[string]error{}, nilItem: map[string]bool{},
		machineID: serverBMachine,
		sections:  []plex.Section{{Key: "1", Type: "movie", Title: "Movies B", Locations: []string{remoteRoot}, ScannedAt: 100, ContentChangedAt: 100}},
	}
	e.plex.sections = []plex.Section{{Key: "1", Type: "movie", Title: "Movies", Locations: []string{remoteRoot}, ScannedAt: 100, ContentChangedAt: 100}}
	e.svc.d.PlexFactory = func(s models.MediaServer) PlexClient {
		if s.ID == m.serverB.ID {
			return m.plexB
		}
		return e.plex
	}
	e.svc.d.FileIdentity = fileid.New(fileid.Hooks{Supported: true, AssumeType: "ext4"})
	return m
}

// bItem registers B's item 900 listing the given files (media ids 9001…).
func (m *multiEnv) bItem(rels ...string) {
	it := &models.MediaItem{RatingKey: "900", MediaType: models.MediaTypeMovie, Title: "Heat", Year: 1995, SectionKey: "1"}
	for i, rel := range rels {
		id := int64(9001 + i)
		it.Versions = append(it.Versions, models.MediaVersion{MediaID: id, RatingKey: "900",
			Parts: []models.MediaPart{{ID: id * 10, Path: m.remote(rel), Size: m.size(rel)}}})
	}
	m.plexB.mu.Lock()
	m.plexB.items["900"] = it
	m.plexB.mu.Unlock()
}

func (m *multiEnv) size(rel string) int64 {
	fi, err := os.Stat(m.local(rel))
	if err != nil {
		m.t.Fatal(err)
	}
	return fi.Size()
}

// heatGroup stores A's group: X (4K, kept) and Y (1080p, removed), Y listed by B's item 900 (Y is
// B's media 9001, X is 9002), with a complete cross-server record; then approves it.
func (m *multiEnv) heatGroup() (*models.DuplicateGroup, []models.Action) {
	m.t.Helper()
	g := m.addGroup("Heat", keep(1, xRel).sized(5000), remove(2, yRel).sized(3000))
	m.bItem(yRel, xRel)
	for i := range g.Files {
		v := &g.Files[i].Version
		self, other := int64(9001), int64(9002)
		if v.MediaID == 1 {
			self, other = 9002, 9001
		}
		v.OtherServers = []models.OtherListing{{
			ServerID: m.serverB.ID, ServerName: "Plex B", LibraryID: 2, LibraryTitle: "Movies B", RatingKey: "900",
			MediaID: self, VersionKey: vkey(m.serverB.ID, self), ItemTitle: "Heat (1995)", Path: v.Parts[0].Path,
			Match: models.OtherSameFile,
			Others: []models.OtherMedia{{MediaID: other, VersionKey: vkey(m.serverB.ID, other),
				Same: []string{g.Files[1-i].Version.Key}, Distinct: []string{v.Key}}},
		}}
	}
	g.CrossServer = m.record()
	if _, err := m.db.Groups().Upsert(m.ctx, g); err != nil {
		m.t.Fatal(err)
	}
	return m.group(g.ID), m.approve(g.ID)
}

func vkey(server, media int64) string { return fmt.Sprintf("plex:%d:%d", server, media) }

func (m *multiEnv) record() *models.CrossServerRecord {
	return &models.CrossServerRecord{Complete: true,
		Servers: []models.CrossServerServer{
			{ServerID: m.server.ID, MachineIdentifier: testMachineID, Mapped: true, Mappings: m.mappingPrint(m.server.ID)},
			{ServerID: m.serverB.ID, MachineIdentifier: serverBMachine, Mapped: true, Mappings: m.mappingPrint(m.serverB.ID)},
		},
		Libraries: []models.CrossServerLibrary{{ServerID: m.serverB.ID, LibraryID: 2, SectionKey: "1", Type: "movie",
			Locations: []string{remoteRoot}, ScannedAt: 100, ContentChangedAt: 100}},
	}
}

// mappingPrint is the fingerprint of server id's stored path mappings (as a scan records it).
func (m *multiEnv) mappingPrint(id int64) string {
	m.t.Helper()
	ms, err := m.db.PathMappings().List(m.ctx)
	if err != nil {
		m.t.Fatal(err)
	}
	return pathmap.New(ms).Fingerprint(models.PathSourceServer, id)
}

func (m *multiEnv) wantUntouched(g *models.DuplicateGroup) {
	m.t.Helper()
	if muts := m.log.mutations(); len(muts) != 0 {
		m.t.Fatalf("mutations %v", muts)
	}
	for _, rel := range []string{xRel, yRel} {
		if !exists(m.local(rel)) {
			m.t.Fatalf("%s was removed", rel)
		}
	}
	_ = g
}

func TestCrossServerDistinctSurvivorProceeds(t *testing.T) {
	m := newMultiEnv(t)
	g, acts := m.heatGroup()
	sum := m.mustProcess()
	if sum.Succeeded != 1 {
		t.Fatalf("summary %+v (%s)", sum, m.action(acts[0].ID).Message)
	}
	if exists(m.local(yRel)) || !exists(m.local(xRel)) {
		t.Fatal("wrong file removed")
	}
	wantStatus(t, m.group(g.ID), models.GroupResolved)
	if m.log.count("plex.Sections") != 1 {
		t.Fatalf("B's sections read %d times", m.log.count("plex.Sections"))
	}
}

func TestCrossServerRecordRequired(t *testing.T) {
	cases := map[string]func(m *multiEnv, g *models.DuplicateGroup){
		"no record": func(m *multiEnv, g *models.DuplicateGroup) { g.CrossServer = nil },
		"incomplete": func(m *multiEnv, g *models.DuplicateGroup) {
			g.CrossServer.Complete = false
		},
		"server enabled since": func(m *multiEnv, g *models.DuplicateGroup) {
			g.CrossServer.Servers = g.CrossServer.Servers[:1]
		},
		"storage changed": func(m *multiEnv, g *models.DuplicateGroup) {
			b := m.serverB
			b.Storage = models.StorageSeparate
			if err := m.db.MediaServers().Update(m.ctx, &b); err != nil {
				m.t.Fatal(err)
			}
		},
		// The scan compared the servers' files (and chose a separate server's libraries) through
		// the path mappings it had: a mapping added or removed since sends the group back.
		"mapping added for B": func(m *multiEnv, g *models.DuplicateGroup) {
			if err := m.db.PathMappings().Create(m.ctx, &models.PathMapping{SourceType: models.PathSourceServer,
				SourceID: m.serverB.ID, RemotePath: "/other", LocalPath: filepath.Join(m.root, "other")}); err != nil {
				m.t.Fatal(err)
			}
		},
		"mapping removed for B": func(m *multiEnv, g *models.DuplicateGroup) {
			pms, _ := m.db.PathMappings().List(m.ctx)
			for _, pm := range pms {
				if pm.SourceType == models.PathSourceServer && pm.SourceID == m.serverB.ID {
					if err := m.db.PathMappings().Delete(m.ctx, pm.ID); err != nil {
						m.t.Fatal(err)
					}
				}
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMultiEnv(t)
			g, acts := m.heatGroup()
			stored := m.group(g.ID)
			change(m, stored)
			if _, err := m.db.Groups().Upsert(m.ctx, stored); err != nil {
				t.Fatal(err)
			}
			m.mustProcess()
			a := m.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, "nothing is removed until a scan has compared it with every media server")
			wantStatus(t, m.group(g.ID), models.GroupReview)
			if len(m.scans()) == 0 {
				t.Fatal("no re-scan queued")
			}
			m.wantUntouched(g)
		})
	}
}

// With several servers the re-scans of the groups a run skips are merged: one targeted scan per
// server at the end of the run (each one lists every movie and TV library of the other servers).
func TestCrossServerRescansMerged(t *testing.T) {
	m := newMultiEnv(t)
	var ids []int64
	for i, title := range []string{"Heat", "Ronin"} {
		rk, base := fmt.Sprint(100+i), int64(10*i)
		g := m.addGroup(title, keep(base+1, xRel).at(rk), remove(base+2, yRel).at(rk))
		g.CrossServer = nil // scanned before the second server was enabled (M25)
		if _, err := m.db.Groups().Upsert(m.ctx, g); err != nil {
			t.Fatal(err)
		}
		m.approve(g.ID)
		ids = append(ids, g.ID)
	}
	m.mustProcess()
	for _, id := range ids {
		wantStatus(t, m.group(id), models.GroupReview)
	}
	scans := m.scans()
	if len(scans) != 1 || scans[0].ServerID != m.server.ID || !slices.Equal(scans[0].RatingKeys, []string{"100", "101"}) {
		t.Fatalf("re-scans %+v, want one targeted scan of server A", scans)
	}
}

// With one server no record is needed and no other server's libraries are read.
func TestCrossServerOneServerNeedsNoRecord(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, xRel), remove(2, yRel))
	e.approve(g.ID)
	if sum := e.mustProcess(); sum.Succeeded != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if e.log.count("plex.Sections") != 0 {
		t.Fatal("sections read with one server")
	}
}

func TestCrossServerOtherServerWaits(t *testing.T) {
	cases := map[string]func(m *multiEnv){
		"identity unreadable": func(m *multiEnv) { m.plexB.identityErr = errors.New("connection refused") },
		"sections unreadable": func(m *multiEnv) { m.plexB.sectionsErr = errors.New("timeout") },
		// M26: B was stored (and scanned) without its identity: another server could answer at
		// its URL, so nothing relies on it until the identity is stored.
		"stored without identity": func(m *multiEnv) {
			b := m.serverB
			b.MachineIdentifier = ""
			if err := m.db.MediaServers().Update(m.ctx, &b); err != nil {
				m.t.Fatal(err)
			}
			gs, err := m.db.Groups().ListByRatingKeys(m.ctx, m.server.ID, []string{"100"})
			if err != nil {
				m.t.Fatal(err)
			}
			for i := range gs {
				gs[i].CrossServer.Servers[1].MachineIdentifier = ""
				if _, err := m.db.Groups().Upsert(m.ctx, &gs[i]); err != nil {
					m.t.Fatal(err)
				}
			}
		},
		"playing on B":   func(m *multiEnv) { m.plexB.sessions = map[string]bool{"900": true} },
		"B's item gone":  func(m *multiEnv) { m.plexB.itemErr["900"] = plex.ErrNotFound },
		"item read fail": func(m *multiEnv) { m.plexB.itemErr["900"] = errors.New("boom") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMultiEnv(t)
			g, acts := m.heatGroup()
			change(m)
			sum := m.mustProcess()
			if sum.Deferred != 1 {
				t.Fatalf("summary %+v", sum)
			}
			wantActionStatus(t, m.action(acts[0].ID), models.ActionPending)
			wantStatus(t, m.group(g.ID), models.GroupQueued)
			m.wantUntouched(g)
		})
	}
}

func TestCrossServerOtherServerProblems(t *testing.T) {
	cases := []struct {
		name    string
		change  func(m *multiEnv)
		want    string
		rescanB bool
	}{
		{"identity changed", func(m *multiEnv) { m.plexB.machineID = "someone-else" }, "server identity changed", false},
		{"listed media changed", func(m *multiEnv) { m.bItem(xRel) }, "changed since the scan", true},
		{"survivor missing", func(m *multiEnv) { m.plexB.exists[m.remote(xRel)] = false }, "reported missing", true},
		{"survivor inaccessible", func(m *multiEnv) {
			m.plexB.onItem = func(it *models.MediaItem) {
				for i := range it.Versions {
					if it.Versions[i].MediaID == 9002 {
						f := false
						it.Versions[i].Parts[0].Accessible = &f
					}
				}
			}
		}, "not accessible", true},
		{"survivor size differs", func(m *multiEnv) { m.plexB.sizes[m.remote(xRel)] = 1 }, "bytes on disk", true},
		{"survivor unmapped", func(m *multiEnv) {
			// B's other version lies outside B's mappings (the mappings themselves are unchanged:
			// a change since the scan is refused before, see TestCrossServerRecordRequired).
			const elsewhere = "/elsewhere/Heat 2160p.mkv"
			m.plexB.mu.Lock()
			m.plexB.items["900"].Versions[1].Parts[0].Path = elsewhere
			m.plexB.exists[elsewhere] = true
			m.plexB.mu.Unlock()
		}, "not covered by a path mapping", true},
		{"filesystem type cannot prove it", func(m *multiEnv) {
			m.svc.d.FileIdentity = fileid.New(fileid.Hooks{Supported: true, AssumeType: "nfs4"})
		}, "cannot be proven a different file", true},
		{"Unraid user share", func(m *multiEnv) {
			m.svc.d.FileIdentity = fileid.New(fileid.Hooks{Supported: true, AssumeType: "fuse.shfs"})
		}, "Unraid user share", true},
		{"another device", func(m *multiEnv) {
			m.svc.d.FileIdentity = fileid.New(fileid.Hooks{Supported: true, AssumeType: "ext4",
				FStat: func(f *os.File) (fileid.Stat, error) {
					fi, err := f.Stat()
					if err != nil {
						return fileid.Stat{}, err
					}
					dev := uint64(1)
					if filepath.Base(f.Name()) == filepath.Base(xRel) {
						dev = 2
					}
					return fileid.Stat{Dev: dev, Ino: uint64(len(f.Name())), Nlink: 1, Size: fi.Size(), Regular: true}, nil
				}})
		}, "different devices", true},
		{"survivor is the file to remove", func(m *multiEnv) { m.bItem(yRel, yRel) }, "is the file to remove", true},
		{"only this file", func(m *multiEnv) { m.bItem(yRel) }, "keeps no other version", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newMultiEnv(t)
			g, acts := m.heatGroup()
			c.change(m)
			m.mustProcess()
			a := m.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, c.want)
			wantStatus(t, m.group(g.ID), models.GroupReview)
			m.wantUntouched(g)
			if c.rescanB {
				found := false
				for _, s := range m.scans() {
					found = found || (s.ServerID == m.serverB.ID && len(s.RatingKeys) == 1 && s.RatingKeys[0] == "900")
				}
				if !found {
					t.Fatalf("no re-scan of B's item: %+v", m.scans())
				}
			}
		})
	}
}

func TestCrossServerSectionsChanged(t *testing.T) {
	cases := map[string]func(s *plex.Section) []plex.Section{
		"new library": func(s *plex.Section) []plex.Section {
			return []plex.Section{*s, {Key: "7", Type: "show", Title: "New", Locations: []string{"/tv"}, ScannedAt: 100, ContentChangedAt: 100}}
		},
		"changed folders": func(s *plex.Section) []plex.Section { s.Locations = []string{"/elsewhere"}; return []plex.Section{*s} },
		"refreshing":      func(s *plex.Section) []plex.Section { s.Refreshing = true; return []plex.Section{*s} },
		"content changed": func(s *plex.Section) []plex.Section { s.ContentChangedAt = 200; return []plex.Section{*s} },
		"scanned since":   func(s *plex.Section) []plex.Section { s.ScannedAt = 200; return []plex.Section{*s} },
		"no scan times": func(s *plex.Section) []plex.Section {
			s.ScannedAt, s.ContentChangedAt = 0, 0
			return []plex.Section{*s}
		},
		"no content changed": func(s *plex.Section) []plex.Section { s.ContentChangedAt = 0; return []plex.Section{*s} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMultiEnv(t)
			g, acts := m.heatGroup()
			sec := m.plexB.sections[0]
			m.plexB.sections = change(&sec)
			m.mustProcess()
			wantActionStatus(t, m.action(acts[0].ID), models.ActionSkipped)
			wantStatus(t, m.group(g.ID), models.GroupReview)
			if len(m.scans()) == 0 {
				t.Fatal("no targeted scan queued")
			}
			m.wantUntouched(g)
		})
	}
}

// Another server's live group keeps the listed file (both match kinds): refused.
func TestCrossServerKeptElsewhere(t *testing.T) {
	for _, match := range []string{models.OtherSameFile, models.OtherPossiblySame} {
		t.Run(match, func(t *testing.T) {
			m := newMultiEnv(t)
			g, acts := m.heatGroup()
			stored := m.group(g.ID)
			for i := range stored.Files {
				stored.Files[i].Version.OtherServers[0].Match = match
			}
			if _, err := m.db.Groups().Upsert(m.ctx, stored); err != nil {
				t.Fatal(err)
			}
			// B's group keeps B's copy of Y (media 9001).
			bg := &models.DuplicateGroup{Key: "movie:tmdb:949@b", MediaType: models.MediaTypeMovie, Title: "Heat", ServerID: m.serverB.ID,
				LibraryIDs: []int64{2}, Status: models.GroupReview, Flags: []string{}, ExternalIDs: map[string]string{}}
			for _, id := range []int64{9001, 9002} {
				d := models.DecisionKeep
				if id == 9002 {
					d = models.DecisionRemove
				}
				bg.Files = append(bg.Files, models.GroupFile{Decision: d, EngineDecision: d, Version: models.MediaVersion{
					Key: vkey(m.serverB.ID, id), ServerID: m.serverB.ID, RatingKey: "900", MediaID: id,
					Parts: []models.MediaPart{{Path: m.remote(yRel), Size: 3000}}}})
			}
			if _, err := m.db.Groups().Upsert(m.ctx, bg); err != nil {
				t.Fatal(err)
			}
			m.mustProcess()
			a := m.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, "is kept by a duplicate group of Plex B")
			m.wantUntouched(g)
		})
	}
}

// A group removed files earlier in the run and kept a file another server lists: a later group
// that removes that listing's file is refused (keptInRun, by the listing's version key).
func TestCrossServerKeptInRun(t *testing.T) {
	m := newMultiEnv(t)
	first, _ := m.heatGroup() // keeps X, whose B listing is media 9002
	// A second group (another item on A) removes a copy B lists as media 9002 too.
	second := m.addGroup("Heat again", keep(11, "movies/Heat2/keep.mkv"), remove(12, "movies/Heat2/lose.mkv"))
	second.CrossServer = m.record()
	for i := range second.Files {
		if second.Files[i].Decision == models.DecisionRemove {
			second.Files[i].Version.OtherServers = []models.OtherListing{{ServerID: m.serverB.ID, ServerName: "Plex B",
				RatingKey: "901", MediaID: 9002, VersionKey: vkey(m.serverB.ID, 9002), Match: models.OtherPossiblySame}}
		}
	}
	if _, err := m.db.Groups().Upsert(m.ctx, second); err != nil {
		t.Fatal(err)
	}
	acts := m.approve(second.ID)
	m.mustProcess()
	wantStatus(t, m.group(first.ID), models.GroupResolved)
	a := m.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSkipped)
	contains(t, "message", a.Message, "whose copies were removed in this run")
	if !exists(m.local("movies/Heat2/lose.mkv")) {
		t.Fatal("the second group's loser was removed")
	}
}

// arrFileProblem with several servers: a mapped *arr file is never confirmed against a part the
// version's server has no mapping for, and raw paths only for a confirmed, linked server.
func TestCrossServerArrFileGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *multiEnv)
		want  string
	}{
		{"unlinked server, raw path", func(m *multiEnv) {
			m.unmapA()
			m.unmapRadarr()
		}, "is not confirmed to feed Plex"},
		{"mapped *arr, unmapped server, linked", func(m *multiEnv) {
			m.unmapA()
			r := m.radarr
			r.ServerIDs, r.LinksConfirmed = []int64{m.server.ID, m.serverB.ID}, true
			if err := m.db.ArrInstances().Update(m.ctx, &r); err != nil {
				m.t.Fatal(err)
			}
		}, "has no path mapping for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMultiEnv(t)
			g := m.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(m.radarr, 7, 70, filmDir)))
			tc.setup(m) // the configuration the scan saw
			g.CrossServer = m.record()
			g.CrossServer.Servers[0].Mapped = false
			if _, err := m.db.Groups().Upsert(m.ctx, g); err != nil {
				t.Fatal(err)
			}
			acts := m.approve(g.ID)
			m.mustProcess()
			a := m.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, tc.want)
			if m.log.count("arr.DeleteFile") != 0 || !exists(m.local(loserRel)) {
				t.Fatal("the *arr file was deleted")
			}
		})
	}
}

func (m *multiEnv) unmapA() {
	m.t.Helper()
	m.deleteMappings(models.PathSourceServer, m.server.ID)
}

func (m *multiEnv) unmapRadarr() {
	m.t.Helper()
	m.deleteMappings(models.PathSourceArr, m.radarr.ID)
}

func (m *multiEnv) deleteMappings(src string, id int64) {
	pms, err := m.db.PathMappings().List(m.ctx)
	if err != nil {
		m.t.Fatal(err)
	}
	for _, pm := range pms {
		if pm.SourceType == src && pm.SourceID == id {
			if err := m.db.PathMappings().Delete(m.ctx, pm.ID); err != nil {
				m.t.Fatal(err)
			}
		}
	}
}
