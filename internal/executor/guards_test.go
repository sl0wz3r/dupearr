package executor

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// trackedGroup adds a group whose 1080p loser is tracked by Radarr (file 7, movie 70) and whose
// 720p loser is untracked (removed through Plex first, docs/DECISIONS.md D3).
func trackedGroup(e *testEnv) (*models.DuplicateGroup, []models.Action) {
	e.t.Helper()
	g := e.addGroup("Film",
		keep(1, keeperRel),
		remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)),
		remove(3, loser2Rel),
	)
	return g, e.approve(g.ID)
}

func actionFor(t *testing.T, e *testEnv, g *models.DuplicateGroup, mediaID int64) *models.Action {
	t.Helper()
	for _, a := range e.actions(g.ID) {
		if strings.HasSuffix(a.VersionKey, fmt.Sprintf(":%d", mediaID)) {
			return &a
		}
	}
	t.Fatalf("no action for media %d", mediaID)
	return nil
}

// TestArrFileIDMustStillNameTheVersion: an *arr file id that now names another file (the
// instance's URL re-pointed at another Radarr, an upgrade, a re-import) is never deleted, and it is
// detected before anything of the group is touched.
func TestArrFileIDMustStillNameTheVersion(t *testing.T) {
	cases := []struct {
		name    string
		ref     arr.TrackedFileRef
		wantMsg string
	}{
		{"another movie's file", arr.TrackedFileRef{Path: remoteRoot + "/movies/Other (2001)/Other.mkv", Size: 1002, ItemID: 70}, "is now " + remoteRoot + "/movies/Other"},
		{"another item", arr.TrackedFileRef{Path: remoteRoot + "/" + loserRel, Size: 1002, ItemID: 99}, "now belongs to item 99 instead of 70"},
		{"another size", arr.TrackedFileRef{Path: remoteRoot + "/" + loserRel, Size: 5, ItemID: 70}, "as 5 bytes"},
		{"no path", arr.TrackedFileRef{Size: 1002, ItemID: 70}, "did not report the path"},
	}
	for _, dry := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name
			if dry {
				name += " (dry run)"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				e.update(func(s *models.Settings) { s.DryRun = dry })
				g, acts := trackedGroup(e)
				e.arrs[e.radarr.ID].fileRefs[7] = tc.ref
				sum := e.mustProcess()
				for _, a := range acts {
					got := e.action(a.ID)
					wantActionStatus(t, got, models.ActionSkipped)
					contains(t, "message", got.Message, tc.wantMsg)
				}
				if sum.Skipped != 2 || sum.Succeeded+sum.DryRun != 0 {
					t.Fatalf("summary = %+v", sum)
				}
				wantStatus(t, e.group(g.ID), models.GroupReview)
				if len(e.log.mutations()) != 0 {
					t.Fatalf("mutations = %v", e.log.mutations())
				}
				for _, rel := range []string{keeperRel, loserRel, loser2Rel} {
					if !exists(e.local(rel)) {
						t.Fatalf("%s was removed", rel)
					}
				}
				if len(e.scans()) != 1 {
					t.Fatalf("want a targeted re-scan: %+v", e.scans())
				}
			})
		}
	}
}

// TestArrFileRecheckedRightBeforeDelete: the file id is read again immediately before DeleteFile;
// a change after planning (or an unreachable *arr at that moment) deletes nothing.
func TestArrFileRecheckedRightBeforeDelete(t *testing.T) {
	setup := func(t *testing.T, change func(f *fakeArr)) (*testEnv, *models.DuplicateGroup) {
		e := newEnv(t)
		g, _ := trackedGroup(e)
		f := e.arrs[e.radarr.ID]
		var calls atomic.Int32
		e.log.onCall = func(c string) {
			if strings.HasPrefix(c, "arr.File Radarr 7") && calls.Add(1) == 2 {
				f.mu.Lock()
				change(f)
				f.mu.Unlock()
			}
		}
		return e, g
	}
	t.Run("file replaced after planning", func(t *testing.T) {
		e, g := setup(t, func(f *fakeArr) {
			f.fileRefs[7] = arr.TrackedFileRef{Path: remoteRoot + "/movies/Other (2001)/Other.mkv", Size: 1002, ItemID: 70}
		})
		e.mustProcess()
		tracked := actionFor(t, e, g, 2)
		wantActionStatus(t, tracked, models.ActionSkipped)
		contains(t, "message", tracked.Message, "is now")
		// The untracked copy ran first (D3); the tracked one was not deleted.
		wantActionStatus(t, actionFor(t, e, g, 3), models.ActionSucceeded)
		if e.log.count("arr.DeleteFile") != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("calls = %v", e.log.all())
		}
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if e.log.count("arr.File Radarr 7") != 2 {
			t.Fatalf("want the file read when planning and right before the delete: %v", e.log.all())
		}
	})
	t.Run("*arr unreachable right before the delete", func(t *testing.T) {
		e, g := setup(t, func(f *fakeArr) { f.fileErr = errors.New("connection refused") })
		e.mustProcess()
		tracked := actionFor(t, e, g, 2)
		wantActionStatus(t, tracked, models.ActionFailed)
		contains(t, "message", tracked.Message, "nothing was deleted")
		if e.log.count("arr.DeleteFile") != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("calls = %v", e.log.all())
		}
		wantStatus(t, e.group(g.ID), models.GroupFailed)
	})
}

// TestArrFileRecheckUnreachableDefers: when the *arr cannot be asked while planning, nothing of
// the group is removed and the removals stay queued.
func TestArrFileRecheckUnreachableDefers(t *testing.T) {
	e := newEnv(t)
	g, acts := trackedGroup(e)
	e.arrs[e.radarr.ID].fileErr = errors.New("connection refused")
	sum := e.mustProcess()
	if sum.Deferred != 1 || sum.Processed != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	for _, a := range acts {
		wantActionStatus(t, e.action(a.ID), models.ActionPending)
	}
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupQueued)
	contains(t, "reason", got.StatusReason, "could not re-check file 7")
	if len(e.log.mutations()) != 0 {
		t.Fatalf("mutations = %v", e.log.mutations())
	}
}

// TestArrFilePathComparison covers how the *arr's path is compared with the version's: mapped
// local paths when both sides are mapped (even under different remote roots), raw paths otherwise.
func TestArrFilePathComparison(t *testing.T) {
	t.Run("different remote roots, same mapped file", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
		inst := models.ArrInstance{Name: "Radarr4K", Kind: models.ArrRadarr, URL: "http://radarr4k.invalid", APIKey: "k", Enabled: true}
		if err := e.db.ArrInstances().Create(e.ctx, &inst); err != nil {
			t.Fatal(err)
		}
		m := models.PathMapping{SourceType: models.PathSourceArr, SourceID: inst.ID, RemotePath: "/movies4k", LocalPath: e.root}
		if err := e.db.PathMappings().Create(e.ctx, &m); err != nil {
			t.Fatal(err)
		}
		f := newFakeArr(e.log, "Radarr4K", e.root)
		e.arrs[inst.ID] = f
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(inst, 7, 70, filmDir)))
		acts := e.approve(g.ID)
		f.fileRefs[7] = arr.TrackedFileRef{Path: "/movies4k/" + loserRel, Size: 1002, ItemID: 70}
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		if exists(e.local(loserRel)) || !exists(e.local(keeperRel)) {
			t.Fatal("wrong files removed")
		}
	})
	unmapArr := func(t *testing.T, e *testEnv) {
		t.Helper()
		ms, err := e.db.PathMappings().List(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.SourceType == models.PathSourceArr && m.SourceID == e.radarr.ID {
				if err := e.db.PathMappings().Delete(e.ctx, m.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	t.Run("no *arr mapping: equal raw paths", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
		unmapArr(t, e)
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
		acts := e.approve(g.ID)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	})
	t.Run("no *arr mapping: other namespace is not confirmed", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
		unmapArr(t, e)
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
		acts := e.approve(g.ID)
		e.arrs[e.radarr.ID].fileRefs[7] = arr.TrackedFileRef{Path: "/movies/" + strings.TrimPrefix(loserRel, "movies/"), Size: 1002, ItemID: 70}
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "add path mappings")
		if e.log.count("arr.DeleteFile") != 0 || !exists(e.local(loserRel)) {
			t.Fatal("deleted an unconfirmed file")
		}
	})
	t.Run("pathKey", func(t *testing.T) {
		for _, tc := range []struct {
			a, b string
			same bool
		}{
			{"/data/Movies/a.mkv", "/data/Movies//a.mkv", true},
			{"/data/Movies/a.mkv", "/data/movies/a.mkv", false},
			{`D:\Movies\A.mkv`, "d:/movies/a.mkv", true},
			{`\\NAS\Share\A.mkv`, "//nas/share/a.mkv", true},
			{"", "", true},
		} {
			if got := pathKey(tc.a) == pathKey(tc.b); got != tc.same {
				t.Errorf("pathKey(%q) == pathKey(%q): %v, want %v", tc.a, tc.b, got, tc.same)
			}
		}
	})
}

// TestPlexServerIdentityGuard: a media server whose URL now reaches another Plex server is never
// re-verified or deleted through.
func TestPlexServerIdentityGuard(t *testing.T) {
	plexOnly := func(e *testEnv) (*models.DuplicateGroup, []models.Action) {
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		return g, e.approve(g.ID)
	}
	t.Run("another server before verification", func(t *testing.T) {
		e := newEnv(t)
		g, acts := plexOnly(e)
		e.plex.machineID = "another-server"
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "server identity changed")
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if e.log.count("plex.Item") != 0 || len(e.log.mutations()) != 0 {
			t.Fatalf("calls = %v", e.log.all())
		}
		if len(e.scans()) != 0 {
			t.Fatalf("a re-scan cannot fix an identity change: %+v", e.scans())
		}
	})
	t.Run("identity unreadable", func(t *testing.T) {
		e := newEnv(t)
		g, acts := plexOnly(e)
		e.plex.identityErr = errors.New("connection refused")
		sum := e.mustProcess()
		if sum.Deferred != 1 {
			t.Fatalf("summary = %+v", sum)
		}
		wantActionStatus(t, e.action(acts[0].ID), models.ActionPending)
		got := e.group(g.ID)
		wantStatus(t, got, models.GroupQueued)
		contains(t, "reason", got.StatusReason, "could not confirm the identity")
		if len(e.log.mutations()) != 0 {
			t.Fatalf("mutations = %v", e.log.mutations())
		}
	})
	t.Run("server changes right before the delete", func(t *testing.T) {
		e := newEnv(t)
		g, acts := plexOnly(e)
		e.log.onCall = func(c string) {
			if strings.HasPrefix(c, "plex.MediaDeletionAllowed") {
				e.plex.mu.Lock()
				e.plex.machineID = "another-server"
				e.plex.mu.Unlock()
			}
		}
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "server identity changed")
		if e.log.count("plex.DeleteMedia") != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("calls = %v", e.log.all())
		}
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if len(e.scans()) != 0 {
			t.Fatalf("scans = %+v", e.scans())
		}
	})
	t.Run("no stored identifier is not checked", func(t *testing.T) {
		e := newEnv(t)
		_, acts := plexOnly(e)
		e.server.MachineIdentifier = ""
		if err := e.db.MediaServers().Update(e.ctx, &e.server); err != nil {
			t.Fatal(err)
		}
		e.plex.machineID = "whatever"
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		if e.log.count("plex.Identity") != 0 {
			t.Fatalf("calls = %v", e.log.all())
		}
	})
	t.Run("identity checked before verification and before the delete", func(t *testing.T) {
		e := newEnv(t)
		_, acts := plexOnly(e)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		first, item, del := e.log.index("plex.Identity"), e.log.index("plex.Item"), e.log.index("plex.DeleteMedia")
		if first < 0 || item < 0 || first > item || e.log.count("plex.Identity") != 2 || del < 0 {
			t.Fatalf("calls = %v", e.log.all())
		}
	})
	t.Run("stale-entry cleanup", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodFilesystem}
			s.CleanupPlexStaleEntries = true
		})
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		var items atomic.Int32
		e.log.onCall = func(c string) {
			if strings.HasPrefix(c, "plex.Item") && items.Add(1) == 2 {
				e.plex.mu.Lock()
				e.plex.machineID = "another-server"
				e.plex.mu.Unlock()
			}
		}
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		contains(t, "message", a.Message, "identity could not be confirmed")
		if e.log.count("plex.DeleteMedia") != 0 {
			t.Fatalf("calls = %v", e.log.all())
		}
	})
}
