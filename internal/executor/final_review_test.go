package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// hookStore runs a hook once, the first time Approve lists a group's actions (after it read the
// group, before it queues anything): the moment a concurrent scan can store new data.
type hookStore struct {
	store.Store
	beforeQueue func()
	// beforeStatusWrite runs once, right before the first UpdateStatusIf.
	beforeStatusWrite func()
}

func (h *hookStore) Actions() store.ActionRepo {
	return hookActions{ActionRepo: h.Store.Actions(), h: h}
}
func (h *hookStore) Groups() store.GroupRepo { return hookGroups{GroupRepo: h.Store.Groups(), h: h} }

type hookGroups struct {
	store.GroupRepo
	h *hookStore
}

func (g hookGroups) UpdateStatusIf(ctx context.Context, id int64, from []models.GroupStatus, to models.GroupStatus, reason string) (bool, error) {
	if f := g.h.beforeStatusWrite; f != nil {
		g.h.beforeStatusWrite = nil
		f()
	}
	return g.GroupRepo.UpdateStatusIf(ctx, id, from, to, reason)
}

type hookActions struct {
	store.ActionRepo
	h *hookStore
}

func (a hookActions) ListByGroup(ctx context.Context, groupID int64) ([]models.Action, error) {
	if f := a.h.beforeQueue; f != nil {
		a.h.beforeQueue = nil
		f()
	}
	return a.ActionRepo.ListByGroup(ctx, groupID)
}

// TestApproveRefusesGroupChangedMeanwhile: a scan storing new results for a group while it is
// being approved wins. Approve used to write "queued" over whatever the scan stored — a review
// status (suspect merge, incomplete *arr data) was lost and the executor then removed files of a
// group that needed a human look.
func TestApproveRefusesGroupChangedMeanwhile(t *testing.T) {
	cases := []struct {
		name   string
		change func(g *models.DuplicateGroup)
		status models.GroupStatus
	}{
		{"sent to review", func(g *models.DuplicateGroup) {
			g.Status, g.StatusReason = models.GroupReview, "Possible mismatched merge: years differ (1982 vs 2011)"
			g.Flags = append(g.Flags, models.FlagSuspectMerge)
		}, models.GroupReview},
		{"same status, *arr data changed", func(g *models.DuplicateGroup) {
			for i := range g.Files {
				g.Files[i].Version.Arr = nil // the loser no longer looks tracked
			}
		}, models.GroupPending},
		{"same status, decisions changed", func(g *models.DuplicateGroup) {
			g.Files[0].Decision, g.Files[0].EngineDecision = models.DecisionRemove, models.DecisionRemove
			g.Files[1].Decision, g.Files[1].EngineDecision = models.DecisionKeep, models.DecisionKeep
			g.Signature = "changed"
		}, models.GroupPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
			e.svc.d.Store = &hookStore{Store: e.db, beforeQueue: func() {
				scanned := e.group(g.ID)
				tc.change(scanned)
				if _, err := e.db.Groups().Upsert(e.ctx, scanned); err != nil {
					t.Errorf("concurrent upsert: %v", err)
				}
			}}
			_, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual)
			if !errors.Is(err, ErrNotApprovable) {
				t.Fatalf("Approve err = %v, want ErrNotApprovable", err)
			}
			got := e.group(g.ID)
			wantStatus(t, got, tc.status)
			for _, a := range e.actions(g.ID) {
				if a.Status == models.ActionPending {
					t.Fatalf("a removal stayed queued: %+v", a)
				}
			}
			e.svc.d.Store = e.db
			e.mustProcess()
			if e.log.count("arr.DeleteFile")+e.log.count("plex.DeleteMedia") != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("a file was removed: %v", e.log.all())
			}
		})
	}
}

// TestApproveUndoneWhenScanLandsRightBeforeStatusWrite: a scan storing the same status with other
// data between the last check and the status write loses its reason to "queued"; the approval is
// undone, the group goes to review and is re-scanned.
func TestApproveUndoneWhenScanLandsRightBeforeStatusWrite(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	e.svc.d.Store = &hookStore{Store: e.db, beforeStatusWrite: func() {
		scanned := e.group(g.ID)
		scanned.Files[1].Version.Arr = nil
		if _, err := e.db.Groups().Upsert(e.ctx, scanned); err != nil {
			t.Errorf("concurrent upsert: %v", err)
		}
	}}
	if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("Approve err = %v, want ErrNotApprovable", err)
	}
	e.svc.d.Store = e.db
	wantStatus(t, e.group(g.ID), models.GroupReview)
	for _, a := range e.actions(g.ID) {
		if a.Status == models.ActionPending {
			t.Fatalf("a removal stayed queued: %+v", a)
		}
	}
	if len(e.scans()) != 1 {
		t.Fatalf("want a targeted re-scan, got %+v", e.scans())
	}
	e.mustProcess()
	if !exists(e.local(loserRel)) {
		t.Fatalf("the loser was removed: %v", e.log.all())
	}
}

// TestApproveReviewedSignature: the signature the user reviewed must still be the group's when
// the executor queues the removals (the API checks it with an earlier read).
func TestApproveReviewedSignature(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	if _, err := e.svc.ApproveReviewed(e.ctx, g.ID, models.TriggerManual, "stale-signature"); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("err = %v, want ErrNotApprovable", err)
	}
	if n := len(e.actions(g.ID)); n != 0 {
		t.Fatalf("%d actions were queued", n)
	}
	wantStatus(t, e.group(g.ID), models.GroupPending)
	if _, err := e.svc.ApproveReviewed(e.ctx, g.ID, models.TriggerManual, e.group(g.ID).Signature); err != nil {
		t.Fatalf("approve with the current signature: %v", err)
	}
	wantStatus(t, e.group(g.ID), models.GroupQueued)
}

// TestKeeperMustBeConfirmedPresent: with no path mapping, a kept version counts as present only
// when Plex reports it exists. Partial data (no "exists" attribute although checkFiles=1 was
// sent) is not a confirmation: the loser would otherwise be deleted while the only other copy is
// already gone from disk.
func TestKeeperMustBeConfirmedPresent(t *testing.T) {
	for _, reported := range []string{"unknown", "true"} {
		t.Run(reported, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			e.approve(g.ID)
			maps, err := e.db.PathMappings().List(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range maps {
				if m.SourceType == models.PathSourceServer {
					if err := e.db.PathMappings().Delete(e.ctx, m.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			e.plex.onItem = func(it *models.MediaItem) {
				for i := range it.Versions {
					if it.Versions[i].MediaID == 1 {
						for j := range it.Versions[i].Parts {
							if reported == "unknown" {
								it.Versions[i].Parts[j].Exists = nil
							} else {
								yes := true
								it.Versions[i].Parts[j].Exists = &yes
							}
						}
					}
				}
			}
			e.mustProcess()
			a := e.actions(g.ID)[0]
			if reported == "true" {
				wantActionStatus(t, &a, models.ActionSucceeded)
				return
			}
			wantActionStatus(t, &a, models.ActionSkipped)
			contains(t, "message", a.Message, "could not confirm")
			if e.log.count("plex.DeleteMedia") != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("the loser was deleted: %v", e.log.all())
			}
			wantStatus(t, e.group(g.ID), models.GroupReview)
		})
	}
}

// TestExclusionsAndDisabledLibrariesWinOverApproval: an exclusion added (or a library disabled)
// after the approval takes effect right away — the queued removals are skipped, not executed
// until the next full scan happens to rebuild the group.
func TestExclusionsAndDisabledLibrariesWinOverApproval(t *testing.T) {
	cases := []struct {
		name  string
		apply func(e *testEnv, g *models.DuplicateGroup)
		want  string
	}{
		{"path prefix (absolute)", func(e *testEnv, g *models.DuplicateGroup) {
			e.exclude(models.ExcludePathPrefix, remoteRoot+"/movies")
		}, "excluded"},
		{"path prefix (relative)", func(e *testEnv, g *models.DuplicateGroup) {
			e.exclude(models.ExcludePathPrefix, "Film (2020)")
		}, "excluded"},
		{"group key", func(e *testEnv, g *models.DuplicateGroup) {
			e.exclude(models.ExcludeGroupKey, g.Key)
		}, "excluded"},
		{"group key with a disambiguation suffix", func(e *testEnv, g *models.DuplicateGroup) {
			// The engine appends "@plex:<server>:<ratingKey>" while a key collides; the form the
			// user excluded may differ from the stored one.
			e.exclude(models.ExcludeGroupKey, g.Key+"@plex:1:100")
		}, "excluded"},
		{"title", func(e *testEnv, g *models.DuplicateGroup) {
			e.exclude(models.ExcludeRegex, "^film$")
		}, "excluded"},
		{"library", func(e *testEnv, g *models.DuplicateGroup) {
			e.exclude(models.ExcludeLibrary, "1")
		}, "excluded"},
		{"library disabled", func(e *testEnv, g *models.DuplicateGroup) {
			libs, err := e.db.Libraries().ListByServer(e.ctx, e.server.ID)
			if err != nil || len(libs) != 1 {
				t.Fatalf("libraries = %v, %v", libs, err)
			}
			libs[0].Enabled = false
			if err := e.db.Libraries().Update(e.ctx, &libs[0]); err != nil {
				t.Fatal(err)
			}
		}, "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			libs, err := e.db.Libraries().Sync(e.ctx, e.server.ID, []models.Library{
				{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot + "/movies"}},
			})
			if err != nil || len(libs) != 1 || libs[0].ID != 1 {
				t.Fatalf("library = %+v, %v (the fixtures use library id 1)", libs, err)
			}
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			e.approve(g.ID)
			tc.apply(e, g)
			sum := e.mustProcess()
			if sum.Succeeded != 0 || e.log.count("plex.DeleteMedia") != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("the loser was removed: %+v %v", sum, e.log.all())
			}
			a := e.actions(g.ID)[0]
			if a.Status == models.ActionCancelled {
				// The store already cancels queued removals for the exclusions it can match
				// itself (exact key, absolute path prefix, library id).
				contains(t, "message", a.Message, "excluded")
				return
			}
			wantActionStatus(t, &a, models.ActionSkipped)
			contains(t, "message", a.Message, tc.want)
			wantStatus(t, e.group(g.ID), models.GroupReview)
		})
	}
}

func (e *testEnv) exclude(kind, value string) {
	e.t.Helper()
	if err := e.db.Exclusions().Create(e.ctx, &models.Exclusion{Kind: kind, Value: value}); err != nil {
		e.t.Fatal(err)
	}
}

// TestDryRunSwitchedOnDuringRun: turning dry run on stops real deletions immediately, not after
// the run that is already going.
func TestDryRunSwitchedOnDuringRun(t *testing.T) {
	e := newEnv(t)
	g1 := e.addGroup("First", keep(1, "movies/First/a.mkv").at("100"), remove(2, "movies/First/b.mkv").at("100"))
	g2 := e.addGroup("Second", keep(3, "movies/Second/a.mkv").at("200"), remove(4, "movies/Second/b.mkv").at("200"))
	e.approve(g1.ID)
	e.approve(g2.ID)
	e.log.onCall = func(c string) {
		if strings.HasPrefix(c, "plex.DeleteMedia 100/") {
			e.update(func(s *models.Settings) { s.DryRun = true })
		}
	}
	e.mustProcess()
	if !exists(e.local("movies/Second/b.mkv")) {
		t.Fatalf("the second group was deleted after dry run was switched on: %v", e.log.all())
	}
	a := e.actions(g2.ID)[0]
	wantActionStatus(t, &a, models.ActionDryRun)
	if e.log.count("plex.DeleteMedia") != 1 {
		t.Fatalf("deletes = %v", e.log.all())
	}
}

// TestKeeperArrFileFlip: the *arr no longer tracks the kept file (it tracks the loser now, or
// upgraded the keeper): removing the untracked-looking loser through Plex or the filesystem would
// delete the *arr's file behind its back (re-download loop). The keeper's *arr file is re-read
// before anything of the group is removed.
func TestKeeperArrFileFlip(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, *models.DuplicateGroup) {
		e := newEnv(t)
		g := e.addGroup("Film", keep(1, keeperRel).arr(radarrInfo(e.radarr, 10, 70, filmDir)), remove(2, loserRel))
		e.approve(g.ID)
		return e, g
	}
	t.Run("the *arr tracks the loser now", func(t *testing.T) {
		e, g := setup(t)
		f := e.arrs[e.radarr.ID]
		f.mu.Lock()
		delete(f.files, 10)
		f.files[11], f.items[11] = e.local(loserRel), 70
		f.mu.Unlock()
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSkipped)
		contains(t, "message", a.Message, "kept")
		if e.log.count("plex.DeleteMedia") != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("the loser was removed: %v", e.log.all())
		}
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if len(e.scans()) == 0 {
			t.Fatal("want a targeted re-scan")
		}
	})
	t.Run("the keeper's file id names another file", func(t *testing.T) {
		e, g := setup(t)
		e.arrs[e.radarr.ID].fileRefs[10] = arr.TrackedFileRef{Path: remoteRoot + "/" + loserRel, Size: 1002, ItemID: 70}
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSkipped)
		if !exists(e.local(loserRel)) {
			t.Fatal("the loser was removed")
		}
	})
	t.Run("unreachable *arr defers", func(t *testing.T) {
		e, g := setup(t)
		e.arrs[e.radarr.ID].fileErr = errors.New("connection refused")
		sum := e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionPending)
		if sum.Deferred != 1 || !exists(e.local(loserRel)) {
			t.Fatalf("summary = %+v", sum)
		}
	})
	t.Run("unchanged keeper (same name and size, unmapped path)", func(t *testing.T) {
		e, g := setup(t)
		e.arrs[e.radarr.ID].fileRefs[10] = arr.TrackedFileRef{Path: "/movies/Film (2020)/Film.2160p.mkv", Size: 1001, ItemID: 70}
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSucceeded)
	})
}

// TestKeeperOfGroupResolvedEarlierInRun: two stale, overlapping groups — the first keeps K and
// removes L, the second (older content grouping) removes K. Once the first is resolved in the
// same run, the second must not remove its keeper.
func TestKeeperOfGroupResolvedEarlierInRun(t *testing.T) {
	e := newEnv(t)
	g1 := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	g2 := e.addGroup("Film", keep(3, "movies/Film Other/Film.mkv").at("200"), remove(1, keeperRel))
	// The fixture added media 1 to item 100 twice; Plex lists it once.
	e.plex.mu.Lock()
	it := e.plex.items["100"]
	it.Versions = it.Versions[:2]
	e.plex.mu.Unlock()
	e.approve(g1.ID)
	e.approve(g2.ID)
	e.mustProcess()
	wantStatus(t, e.group(g1.ID), models.GroupResolved)
	if !exists(e.local(keeperRel)) {
		t.Fatalf("the keeper of the first group was removed by the second: %v", e.log.all())
	}
	a := e.actions(g2.ID)[0]
	wantActionStatus(t, &a, models.ActionSkipped)
	wantStatus(t, e.group(g2.ID), models.GroupReview)
}

// TestAutoApprovalRefusesReviewFlags: automatic approvals re-check the flags that keep a group
// out of auto mode (the scanner filters them, but the group may have changed since).
func TestAutoApprovalRefusesReviewFlags(t *testing.T) {
	for _, flag := range []string{models.FlagStacked, models.FlagMultiEpisode, models.FlagSample, models.FlagSameFile,
		models.FlagSuspectMerge, models.FlagArrUntrackedKeeper, models.FlagPlaying} {
		t.Run(flag, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.Mode = models.ModeAuto })
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			g.Flags = []string{flag}
			if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerScheduled); !errors.Is(err, ErrNotApprovable) {
				t.Fatalf("err = %v, want ErrNotApprovable", err)
			}
			if n := len(e.actions(g.ID)); n != 0 {
				t.Fatalf("%d actions queued", n)
			}
			// A person may still approve it.
			if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); err != nil {
				t.Fatalf("manual approval: %v", err)
			}
		})
	}
}

// TestKeeperInRecycleBin: a kept copy that lies in a recycle bin (an *arr's bin inside the Plex
// library, which Plex indexes as another version; or Dupearr's own bin) is purged automatically
// after a few days, so it never counts as the kept copy: removing the other copy would lose the
// title once the bin is emptied.
func TestKeeperInRecycleBin(t *testing.T) {
	t.Run("*arr recycle bin", func(t *testing.T) {
		e := newEnv(t)
		e.arrs[e.radarr.ID].recycleBin = remoteRoot + "/movies/.radarr-recycle"
		g := e.addGroup("Film", keep(1, "movies/.radarr-recycle/Film (2020)/Film.Remux.mkv"), remove(2, loserRel))
		e.approve(g.ID)
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSkipped)
		contains(t, "message", a.Message, "recycle bin")
		if !exists(e.local(loserRel)) {
			t.Fatalf("the loser was removed: %v", e.log.all())
		}
		wantStatus(t, e.group(g.ID), models.GroupReview)
	})
	t.Run("Dupearr's recycle bin", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.RecycleBinPath = e.local("recycle") })
		g := e.addGroup("Film", keep(1, "recycle/2026-09-01/movies/Film (2020)/Film.2160p.mkv"), remove(2, loserRel))
		e.approve(g.ID)
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSkipped)
		contains(t, "message", a.Message, "recycle bin")
		if !exists(e.local(loserRel)) {
			t.Fatalf("the loser was removed: %v", e.log.all())
		}
	})
	t.Run("another keeper outside the bin", func(t *testing.T) {
		e := newEnv(t)
		e.arrs[e.radarr.ID].recycleBin = remoteRoot + "/movies/.radarr-recycle"
		g := e.addGroup("Film", keep(1, "movies/.radarr-recycle/Film (2020)/Film.Remux.mkv"), keep(3, keeperRel), remove(2, loserRel))
		e.approve(g.ID)
		e.mustProcess()
		a := e.actions(g.ID)[0]
		wantActionStatus(t, &a, models.ActionSucceeded)
	})
}

// TestProfileProtectionAddedAfterApproval: a protection added to the group's profile after the
// approval wins right away. The API re-evaluates groups in the background after a profile change;
// a queue run starting before that re-evaluation reached the group used to delete the file the
// user had just protected.
func TestProfileProtectionAddedAfterApproval(t *testing.T) {
	cases := []struct {
		name string
		pr   models.Protection
		spec func(e *testEnv) vspec
	}{
		{"path glob", models.Protection{Type: models.ProtectPathGlob, Value: "**/Film.1080p.mkv"}, func(e *testEnv) vspec {
			return remove(2, loserRel)
		}},
		{"*arr tag", models.Protection{Type: models.ProtectArrTag, Value: "dupearr-keep"}, func(e *testEnv) vspec {
			info := radarrInfo(e.radarr, 7, 70, filmDir)
			info.Tags = []string{"dupearr-keep"}
			return remove(2, loserRel).arr(info)
		}},
		{"library", models.Protection{Type: models.ProtectLibrary, Value: "1"}, func(e *testEnv) vspec {
			return remove(2, loserRel)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), tc.spec(e))
			e.approve(g.ID)
			p, err := e.db.Profiles().GetDefault(e.ctx)
			if errors.Is(err, store.ErrNotFound) {
				tpl := engine.ProfileTemplates()[0]
				tpl.IsDefault = true
				if err := e.db.Profiles().Create(e.ctx, &tpl); err != nil {
					t.Fatal(err)
				}
				p = &tpl
			} else if err != nil {
				t.Fatal(err)
			}
			p.Protections = append(p.Protections, tc.pr)
			if err := e.db.Profiles().Update(e.ctx, p); err != nil {
				t.Fatal(err)
			}
			sum := e.mustProcess()
			if sum.Succeeded != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("the protected file was removed: %+v %v", sum, e.log.all())
			}
			a := e.actions(g.ID)[0]
			wantActionStatus(t, &a, models.ActionSkipped)
			contains(t, "message", a.Message, "protect")
			wantStatus(t, e.group(g.ID), models.GroupReview)
		})
	}
}
