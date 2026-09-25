//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Several Plex servers (docs/DECISIONS.md D11, issue #8 Phase 1): the real binary against two
// fake Plex servers. Whether two paths can be proven different files depends on the filesystem
// the test tree lies on (ext4/XFS/btrfs/ZFS on Linux: yes; APFS, tmpfs, overlay …: never), so the
// scenarios that depend on it branch on the tree's type, and the run-time checks use setups that
// do not depend on it.

const (
	msTitle     = "Heat"
	ms4K        = "movies4k/Heat (1995)/Heat (1995) Remux-2160p.mkv"
	ms1080      = "movies/Heat (1995)/Heat (1995) WEBDL-1080p.mkv"
	msEpisode   = "tv/Dark (2017)/Season 01/Dark (2017) - S01E01 - Secrets [WEBDL-1080p].mkv"
	msSecondID  = "5ec0ad5ec0ad5ec0ad5ec0ad5ec0ad5ec0ad5ec0"
	msSecondTok = "fAkEpLeXtOkEnSeCoNd01"
)

// msScenario: one "Movies" library over movies/ and movies4k/ with Heat in 4K and 1080p (the
// 1080p optionally tracked by Radarr), and a TV library with one episode (a second server listing
// only TV lists nothing of Heat).
func msScenario(name string, tracked bool) *fakemedia.Scenario {
	s := fakemedia.Base(name)
	s.Libraries = []fakemedia.Library{
		{Key: fakemedia.SectionMovies, Title: "Movies", Type: fakemedia.LibraryMovie, Dirs: []string{fakemedia.DirMovies, fakemedia.DirMovies4K}},
		{Key: fakemedia.SectionTV, Title: "TV Shows", Type: fakemedia.LibraryShow, Dirs: []string{fakemedia.DirTV}},
	}
	v1080 := fakemedia.Version{Parts: []fakemedia.Part{{File: ms1080, Size: fakemedia.GiB(12)}}, Video: fakemedia.FHD("h264"),
		Audio: []fakemedia.Audio{fakemedia.EAC3("eng", 6)}, DurationMs: fakemedia.Mins(170)}
	if tracked {
		v1080.Tracked = fakemedia.InstanceRadarr
	}
	s.AddMovie(fakemedia.Movie{
		Section: fakemedia.SectionMovies, Title: msTitle, Year: 1995, TmdbID: 949, ImdbID: "tt0113277",
		Versions: []fakemedia.Version{
			{Parts: []fakemedia.Part{{File: ms4K, Size: fakemedia.GiB(60)}}, Video: fakemedia.HDR10UHD(),
				Audio: []fakemedia.Audio{fakemedia.TrueHDAtmos("eng")}, DurationMs: fakemedia.Mins(170)},
			v1080,
		},
		Arr: []fakemedia.ArrMovie{{Instance: fakemedia.InstanceRadarr}},
	})
	s.AddShow(fakemedia.Show{
		Section: fakemedia.SectionTV, Title: "Dark", Year: 2017, TvdbID: 334824, Folder: "tv/Dark (2017)",
		Episodes: []fakemedia.Episode{{Season: 1, Episode: 1, Title: "Secrets", Versions: []fakemedia.Version{
			{Parts: []fakemedia.Part{{File: msEpisode, Size: fakemedia.GiB(2)}}, Video: fakemedia.FHD("h264")},
		}}},
	})
	return s
}

// msStack is dupearr with server A (and optionally B).
type msStack struct {
	t        *testing.T
	a, b     *fakemedia.Env
	d        *dupearr
	idA, idB int64
	radarr   int64
}

type msOptions struct {
	tracked bool
	// second builds B's scenario (nil = no second server yet).
	second func(base *fakemedia.Scenario) *fakemedia.Scenario
	shared bool // B shares A's tree
	mapB   bool
	// storageB is B's storage setting ("" or "separate").
	storageB string
}

func newMSStack(t *testing.T, o msOptions) *msStack {
	t.Helper()
	base := msScenario("multi", o.tracked)
	a := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: base, Dir: t.TempDir()})
	s := &msStack{t: t, a: a, d: startDupearr(t, startOptions{})}
	t.Cleanup(func() {
		a.AssertNoViolations(t)
		a.AssertEveryItemHasAFile(t)
		if s.b != nil {
			s.b.AssertNoViolations(t)
			s.b.AssertEveryItemHasAFile(t)
		}
	})
	d := s.d
	var ms models.MediaServer
	d.expect(http.MethodPost, "/api/v1/mediaserver", map[string]any{"name": "Plex A", "url": a.Plex.URL, "token": a.PlexToken}, http.StatusCreated, &ms)
	s.idA = ms.ID
	// Radarr is added while A is the only server: linked to A and confirmed (like the upgrade).
	rs := a.Instances[fakemedia.InstanceRadarr]
	var arr models.ArrInstance
	d.expect(http.MethodPost, "/api/v1/arr", map[string]any{"name": "Radarr", "kind": "radarr", "url": rs.URL, "apiKey": rs.APIKey},
		http.StatusCreated, &arr)
	s.radarr = arr.ID
	for _, m := range a.PathMappings() {
		pm := map[string]any{"sourceType": models.PathSourceServer, "sourceId": s.idA, "remotePath": m.Remote, "localPath": m.Local}
		if m.Server == fakemedia.InstanceRadarr {
			pm["sourceType"], pm["sourceId"] = models.PathSourceArr, s.radarr
		} else if m.Server != fakemedia.ServerPlex {
			continue
		}
		d.expect(http.MethodPost, "/api/v1/pathmapping", pm, http.StatusCreated, nil)
	}
	d.putSettings(map[string]any{"minAgeHours": 0, "dryRun": false})
	d.waitIdle()
	if o.second != nil {
		s.addSecond(o)
	}
	return s
}

// addSecond starts server B and adds it to dupearr.
func (s *msStack) addSecond(o msOptions) {
	s.t.Helper()
	bo := fakemedia.Options{Scenario: o.second(msScenario("multi", o.tracked))}
	if o.shared {
		bo.ShareMedia = s.a
	} else {
		bo.Dir = s.t.TempDir()
	}
	s.b = fakemedia.StartWithOptions(s.t, bo)
	var ms models.MediaServer
	s.d.expect(http.MethodPost, "/api/v1/mediaserver", map[string]any{"name": "Plex B", "url": s.b.Plex.URL, "token": s.b.PlexToken,
		"storage": o.storageB}, http.StatusCreated, &ms)
	s.idB = ms.ID
	if o.mapB {
		m := s.b.PathMappings()[0]
		s.d.expect(http.MethodPost, "/api/v1/pathmapping", map[string]any{"sourceType": models.PathSourceServer, "sourceId": s.idB,
			"remotePath": m.Remote, "localPath": m.Local}, http.StatusCreated, nil)
	}
	s.d.waitIdle()
}

func (s *msStack) scan() { s.t.Helper(); s.d.runCommand(models.CmdDuplicateScan, nil) }

// groupOn returns the group of Heat on server sid.
func (s *msStack) groupOn(sid int64) groupDetail {
	s.t.Helper()
	for _, g := range s.d.groupsBy(msTitle) {
		if d := s.d.group(g.ID); d.ServerID == sid {
			return d
		}
	}
	s.t.Fatalf("no group of %s on server %d: %s", msTitle, sid, s.d.describeGroups())
	return groupDetail{}
}

func (s *msStack) requireFiles(rels ...string) {
	s.t.Helper()
	for _, rel := range rels {
		if !s.a.FileExists(s.a.RemoteMediaPath(rel)) {
			s.t.Fatalf("%s was removed", rel)
		}
	}
}

// treeAllowlisted reports whether the test tree's filesystem can prove two paths different files.
func (s *msStack) treeAllowlisted() bool {
	info, err := fileid.Default().PathInfo(filepath.Join(s.a.MediaRoot, filepath.FromSlash(ms1080)))
	return err == nil && info.Allowlisted
}

func (s *msStack) noArrDeletes() {
	s.t.Helper()
	for _, r := range s.a.Requests() {
		if r.Method == http.MethodDelete && strings.Contains(r.Path, "moviefile") {
			s.t.Fatalf("Radarr delete: %s", r.Path)
		}
	}
}

// Scenario A: A and B list both copies of one share (both mapped, same profile).
func TestMultiServerScenarioA(t *testing.T) {
	s := newMSStack(t, msOptions{second: func(b *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(b, "Plex B", msSecondID, msSecondTok)
	}, shared: true, mapB: true})
	s.scan()
	ga, gb := s.groupOn(s.idA), s.groupOn(s.idB)
	if !s.treeAllowlisted() {
		// The tree's filesystem cannot prove the 4K and the 1080p are different files: both
		// groups keep both copies.
		for _, g := range []groupDetail{ga, gb} {
			f := fileContaining(t, g, "1080p")
			if g.Status != models.GroupProtected || !f.Protected || !strings.Contains(f.ProtectedReason, "cannot be proven a different file") {
				t.Fatalf("server %d: %s %v %q", g.ServerID, g.Status, f.Protected, f.ProtectedReason)
			}
		}
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "Nothing to remove") {
			t.Fatalf("approve: %s", r)
		}
		s.requireFiles(ms4K, ms1080)
		return
	}
	for _, g := range []groupDetail{ga, gb} {
		if g.Status != models.GroupPending || !slicesContains(g.Flags, models.FlagOtherServerListing) {
			t.Fatalf("server %d: %s %v %q", g.ServerID, g.Status, g.Flags, g.StatusReason)
		}
	}
	if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve A: %s", r)
	}
	s.d.waitIdle()
	if s.a.FileExists(s.a.RemoteMediaPath(ms1080)) {
		t.Fatalf("A's loser was not removed: %s", s.d.describeGroups())
	}
	gb = s.groupOn(s.idB)
	if r := s.d.approve(gb.ID, ""); r.Status == http.StatusOK {
		s.d.waitIdle()
	}
	if g := s.groupOn(s.idB); g.Status != models.GroupReview && g.Status != models.GroupResolved {
		t.Fatalf("B after A's removal: %s %q", g.Status, g.StatusReason)
	}
	s.requireFiles(ms4K)
}

// Scenario C: B lists only the 1080p folder (mapped): A's group protects B's only copy.
func TestMultiServerScenarioC(t *testing.T) {
	s := newMSStack(t, msOptions{second: func(b *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.SharedServer(b, "Plex B", msSecondID, msSecondTok, fakemedia.DirMovies)
	}, shared: true, mapB: true})
	s.scan()
	ga := s.groupOn(s.idA)
	f := fileContaining(t, ga, "1080p")
	if ga.Status != models.GroupProtected || !strings.Contains(f.ProtectedReason, `on Plex B (Movies)`) {
		t.Fatalf("A: %s %q", ga.Status, f.ProtectedReason)
	}
	if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusBadRequest {
		t.Fatalf("approve: %s", r)
	}
	s.requireFiles(ms4K, ms1080)
}

// Scenario C with B unmapped under /srv: same name and size, possibly the same file: review, and
// a manual approval is skipped to review by the executor (B's item keeps no provable copy).
func TestMultiServerScenarioCUnmapped(t *testing.T) {
	s := newMSStack(t, msOptions{second: func(b *fakemedia.Scenario) *fakemedia.Scenario {
		sc := fakemedia.SharedServer(b, "Plex B", msSecondID, msSecondTok, fakemedia.DirMovies)
		sc.Server.MediaRoot = "/srv"
		return sc
	}, shared: true})
	s.scan()
	ga := s.groupOn(s.idA)
	if ga.Status != models.GroupReview || !slicesContains(ga.Flags, models.FlagOtherServerPossible) {
		t.Fatalf("A: %s %v %q", ga.Status, ga.Flags, ga.StatusReason)
	}
	if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve: %s", r)
	}
	s.d.waitIdle()
	if g := s.groupOn(s.idA); g.Status != models.GroupReview {
		t.Fatalf("after the queue run: %s %q", g.Status, g.StatusReason)
	}
	s.requireFiles(ms4K, ms1080)
}

// Scenario D: B on another host (its own tree, the same /data/media layout), not mapped. The local
// Radarr (mapped) tracks the 1080p at the mirror's path. Radarr is never asked to delete it.
func TestMultiServerScenarioD(t *testing.T) {
	mirror := func(b *fakemedia.Scenario) *fakemedia.Scenario {
		return fakemedia.MirrorServer(b, "Plex B", msSecondID, msSecondTok)
	}
	t.Run("separate", func(t *testing.T) {
		s := newMSStack(t, msOptions{tracked: true, second: mirror, storageB: models.StorageSeparate})
		s.scan()
		gb := s.groupOn(s.idB)
		if gb.Status != models.GroupReview || !strings.Contains(gb.StatusReason, "add a path mapping for Plex B") {
			t.Fatalf("B: %s %q", gb.Status, gb.StatusReason)
		}
		if r := s.d.approve(gb.ID, gb.Signature); r.Status != http.StatusConflict {
			t.Fatalf("approve: %s", r)
		}
		// Radarr wrongly confirmed as feeding both servers: still refused.
		s.d.expect(http.MethodPut, fmt.Sprintf("/api/v1/arr/%d", s.radarr), map[string]any{"name": "Radarr", "kind": "radarr",
			"url": s.a.Instances[fakemedia.InstanceRadarr].URL, "apiKey": "********", "serverIds": []int64{s.idA, s.idB}, "linksConfirmed": true},
			http.StatusAccepted, nil)
		s.scan()
		gb = s.groupOn(s.idB)
		if r := s.d.approve(gb.ID, gb.Signature); r.Status != http.StatusConflict {
			t.Fatalf("approve with wrong links: %s", r)
		}
		s.noArrDeletes()
		s.requireFiles(ms1080)
	})
	t.Run("not declared separate", func(t *testing.T) {
		s := newMSStack(t, msOptions{tracked: true, second: mirror})
		s.scan()
		gb := s.groupOn(s.idB)
		// The mirror's raw path equals A's file: possibly the same file, never removed on a copy
		// Dupearr cannot see.
		if f := fileContaining(t, gb, "1080p"); f.Decision == models.DecisionRemove && gb.Status == models.GroupPending {
			t.Fatalf("B's mirror removable: %s %q", gb.Status, gb.StatusReason)
		}
		if r := s.d.approve(gb.ID, gb.Signature); r.Status == http.StatusOK {
			s.d.waitIdle()
		}
		s.noArrDeletes()
		s.requireFiles(ms1080)
	})
}

// B lists only TV: A's movie group has no listings of another server, so the checks below do not
// depend on the filesystem.
func tvOnly(b *fakemedia.Scenario) *fakemedia.Scenario {
	return fakemedia.SharedServer(b, "Plex B", msSecondID, msSecondTok, fakemedia.DirTV)
}

func TestMultiServerUnreadAtScan(t *testing.T) {
	s := newMSStack(t, msOptions{second: tvOnly, shared: true, mapB: true})
	s.b.InjectFault(fakemedia.Fault{Server: fakemedia.ServerPlex, Status: http.StatusServiceUnavailable})
	s.scan()
	ga := s.groupOn(s.idA)
	if ga.Status != models.GroupReview || !slicesContains(ga.Flags, models.FlagOtherServerUnread) {
		t.Fatalf("A: %s %v %q", ga.Status, ga.Flags, ga.StatusReason)
	}
	if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusConflict {
		t.Fatalf("approve: %s", r)
	}
	s.b.ClearFaults()
	s.requireFiles(ms4K, ms1080)
}

func TestMultiServerRunTimeChecks(t *testing.T) {
	t.Run("B faulted at run time defers, then removes", func(t *testing.T) {
		s := newMSStack(t, msOptions{second: tvOnly, shared: true, mapB: true})
		s.scan()
		ga := s.groupOn(s.idA)
		s.b.InjectFault(fakemedia.Fault{Server: fakemedia.ServerPlex, Status: http.StatusServiceUnavailable})
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
			t.Fatalf("approve: %s", r)
		}
		s.d.waitIdle()
		if g := s.groupOn(s.idA); g.Status != models.GroupQueued || !strings.Contains(g.StatusReason, "Waiting") {
			t.Fatalf("deferred: %s %q", g.Status, g.StatusReason)
		}
		s.requireFiles(ms1080)
		s.b.ClearFaults()
		s.d.runCommand(models.CmdProcessQueue, nil)
		if s.a.FileExists(s.a.RemoteMediaPath(ms1080)) {
			t.Fatalf("not removed once B is back: %s", s.d.describeGroups())
		}
	})
	t.Run("B scanned since the scan (M14)", func(t *testing.T) {
		s := newMSStack(t, msOptions{second: tvOnly, shared: true, mapB: true})
		s.scan()
		ga := s.groupOn(s.idA)
		st, _ := s.b.SectionStateOf(fakemedia.SectionTV)
		st.ContentChangedAt++
		if err := s.b.SetSectionState(fakemedia.SectionTV, st); err != nil {
			t.Fatal(err)
		}
		before := len(s.d.commandsNamed(models.CmdTargetedScan))
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
			t.Fatalf("approve: %s", r)
		}
		s.d.waitIdle()
		if len(s.d.commandsNamed(models.CmdTargetedScan)) <= before {
			t.Fatal("no targeted scan queued")
		}
		s.requireFiles(ms1080)
		// The targeted scan recorded B's new state: approving again removes the file.
		ga = s.groupOn(s.idA)
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
			t.Fatalf("approve again: %s (%s)", r, ga.StatusReason)
		}
		s.d.waitIdle()
		if s.a.FileExists(s.a.RemoteMediaPath(ms1080)) {
			t.Fatalf("not removed after the re-scan: %s", s.d.describeGroups())
		}
	})
	t.Run("group scanned before the second server (M25)", func(t *testing.T) {
		s := newMSStack(t, msOptions{})
		s.scan()
		ga := s.groupOn(s.idA)
		if ga.CrossServer != nil {
			t.Fatalf("one server stored a record: %+v", ga.CrossServer)
		}
		s.addSecond(msOptions{second: tvOnly, shared: true, mapB: true})
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
			t.Fatalf("approve: %s", r)
		}
		s.d.waitIdle()
		if g := s.groupOn(s.idA); !strings.Contains(g.StatusReason, "compared it with every media server") &&
			!strings.Contains(strings.Join(actionMessages(s.d.actionsOf(ga.ID)), "|"), "compared it with every media server") {
			t.Fatalf("not skipped for the missing record: %s %q", g.Status, g.StatusReason)
		}
		s.requireFiles(ms1080)
		s.scan()
		ga = s.groupOn(s.idA)
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusOK {
			t.Fatalf("approve after the scan: %s", r)
		}
		s.d.waitIdle()
		if s.a.FileExists(s.a.RemoteMediaPath(ms1080)) {
			t.Fatalf("not removed after a full scan: %s", s.d.describeGroups())
		}
	})
	t.Run("B stored without identity (M26)", func(t *testing.T) {
		// A server saved while it answered without an identity cannot be trusted: its libraries
		// cannot even be synced, so it counts as unread and A's removals wait for it (the executor
		// defers a group that relies on such a server; see internal/executor).
		s := newMSStack(t, msOptions{})
		bo := fakemedia.Options{Scenario: tvOnly(msScenario("multi", false)), ShareMedia: s.a}
		s.b = fakemedia.StartWithOptions(t, bo)
		s.b.SetMachineIdentifier("") // answers without an identity: saved only with forceSave
		var ms models.MediaServer
		s.d.expect(http.MethodPost, "/api/v1/mediaserver?forceSave=true", map[string]any{"name": "Plex B", "url": s.b.Plex.URL,
			"token": s.b.PlexToken}, http.StatusCreated, &ms)
		s.idB = ms.ID
		m := s.b.PathMappings()[0]
		s.d.expect(http.MethodPost, "/api/v1/pathmapping", map[string]any{"sourceType": models.PathSourceServer, "sourceId": s.idB,
			"remotePath": m.Remote, "localPath": m.Local}, http.StatusCreated, nil)
		s.d.waitIdle()
		s.scan()
		ga := s.groupOn(s.idA)
		if r := s.d.approve(ga.ID, ga.Signature); r.Status != http.StatusConflict {
			t.Fatalf("approve: %s", r)
		}
		identity := false
		for _, h := range s.d.health() {
			identity = identity || h.Source == "MediaServerIdentityCheck"
		}
		if !identity {
			t.Fatalf("no identity warning: %+v", s.d.health())
		}
		s.requireFiles(ms1080)
	})
}

// With one server nothing changes on the wire or in the JSON: no library sections read by a scan,
// no cross-server fields, no multi-server health issue.
func TestSingleServerNoMultiServerTraces(t *testing.T) {
	s := newMSStack(t, msOptions{})
	s.a.ResetRequests()
	s.scan()
	for _, r := range s.a.Requests() {
		if r.Server == fakemedia.ServerPlex && r.Method == http.MethodGet && strings.HasPrefix(r.Path, "/library/sections") &&
			!strings.Contains(r.Path, "/all") && !strings.Contains(r.Path, "/refresh") {
			t.Fatalf("a one-server scan read %s", r.Path)
		}
	}
	ga := s.groupOn(s.idA)
	raw := s.d.request(http.MethodGet, fmt.Sprintf("/api/v1/duplicate/%d", ga.ID), nil)
	if b := string(raw.Body); strings.Contains(b, "otherServers") || strings.Contains(b, "crossServer") {
		t.Fatalf("group JSON: %s", b)
	}
	for _, h := range s.d.health() {
		switch h.Source {
		case "MultiServerFoldersCheck", "ArrServerLinksCheck", "MultiServerMappingCheck", "SeparateServerCheck", "MediaServerIdentityCheck":
			t.Fatalf("health issue with one server: %+v", h)
		}
	}
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func actionMessages(as []models.Action) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Message)
	}
	return out
}
