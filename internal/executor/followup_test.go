package executor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Regression tests for the follow-up review items: episode labels, Restore ignoring its group and
// one verified keeper per keepPer partition.

// TestDisplayTitleUnknownSeason: an episode whose season is unknown (-1) is labelled "S??", never
// "S-1", in action titles (engine.EpisodeLabel).
func TestDisplayTitleUnknownSeason(t *testing.T) {
	cases := []struct {
		season, episode int
		want            string
	}{
		{-1, 5, "Lost - S??E05 - Pilot"},
		{0, 3, "Lost - S00E03 - Pilot"},
		{2, 0, "Lost - S02E?? - Pilot"},
		{1, 1, "Lost - S01E01 - Pilot"},
	}
	for _, tc := range cases {
		g := &models.DuplicateGroup{MediaType: models.MediaTypeEpisode, Title: "Pilot", ShowTitle: "Lost", Season: tc.season, Episode: tc.episode}
		if got := displayTitle(g); got != tc.want {
			t.Errorf("S%d E%d: title = %q, want %q", tc.season, tc.episode, got, tc.want)
		}
	}

	// The same label reaches the queued action.
	e := newEnv(t)
	g := e.addGroup("Pilot", keep(1, keeperRel), remove(2, loserRel))
	g.MediaType, g.ShowTitle, g.Season, g.Episode = models.MediaTypeEpisode, "Lost", -1, 5
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	acts := e.approve(g.ID)
	if len(acts) != 1 || acts[0].Title != "Lost - S??E05 - Pilot" || strings.Contains(acts[0].Title, "S-1") {
		t.Fatalf("action titles = %+v", acts)
	}
}

// restoredEnv removes the loser of a two-version group into the recycle bin and returns the env,
// the group and the succeeded action.
func restoredEnv(t *testing.T, specs ...vspec) (*testEnv, *models.DuplicateGroup, []models.Action) {
	t.Helper()
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = filepath.Join(e.dir, "recycle")
	})
	if len(specs) == 0 {
		specs = []vspec{keep(1, keeperRel), remove(2, loserRel)}
	}
	g := e.addGroup("Film", specs...)
	acts := e.approve(g.ID)
	e.mustProcess()
	return e, g, acts
}

// TestRestoreIgnoresTheGroup: a restored file is not removed again — the group is ignored (with a
// reason saying how to undo it, and a history event), later scans keep it ignored and automatic
// approval refuses it.
func TestRestoreIgnoresTheGroup(t *testing.T) {
	e, g, acts := restoredEnv(t)
	wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	wantStatus(t, e.group(g.ID), models.GroupResolved)

	if err := e.svc.Restore(e.ctx, acts[0].ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !exists(e.local(loserRel)) {
		t.Fatal("the file was not restored")
	}
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupIgnored)
	if got.StatusReason != "Restored by you — ignored so it is not removed again (unignore to re-evaluate)" {
		t.Fatalf("reason = %q", got.StatusReason)
	}
	ign := e.history(models.EventGroupIgnored)
	if len(ign) != 1 || ign[0].Message != restoredIgnoreReason || ign[0].GroupID == nil || *ign[0].GroupID != g.ID ||
		ign[0].ActionID == nil || *ign[0].ActionID != acts[0].ID {
		t.Fatalf("groupIgnored history = %+v", ign)
	}
	all := e.history(models.EventFileRestored, models.EventGroupIgnored)
	if len(all) != 2 || all[0].EventType != models.EventFileRestored || all[1].EventType != models.EventGroupIgnored {
		t.Fatalf("history order = %+v", all)
	}

	// The targeted re-scan finds the restored file as a new, pending duplicate: the stored group
	// stays ignored, so neither auto mode nor the queue can remove it again.
	back := e.group(g.ID)
	back.Status, back.StatusReason, back.StableCount = models.GroupPending, "", 10
	back.Files[1].Version.MediaID, back.Files[1].Version.Key = 22, fmt.Sprintf("plex:%d:22", e.server.ID)
	if _, err := e.db.Groups().Upsert(e.ctx, back); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.group(g.ID), models.GroupIgnored)
	e.update(func(s *models.Settings) { s.Mode = models.ModeAuto; s.StableScansRequired = 1 })
	if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerScheduled); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("automatic approval of the restored group: err = %v", err)
	}
	e.mustProcess()
	if !exists(e.local(loserRel)) || len(e.actions(g.ID)) != 1 {
		t.Fatalf("the restored file was removed again: %v", e.log.all())
	}
}

// TestRestoreCancelsTheGroupsOtherQueuedRemovals: restoring one removal of a group whose other
// removal is still queued (per-run limit) ignores the group, which cancels the queued one.
func TestRestoreCancelsTheGroupsOtherQueuedRemovals(t *testing.T) {
	const third = filmDir + "/Film.720p.mkv"
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = filepath.Join(e.dir, "recycle")
		s.MaxDeletionsPerRun = 1
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel), remove(3, third))
	e.approve(g.ID)
	e.mustProcess()
	var done, queued *models.Action
	for _, a := range e.actions(g.ID) {
		switch a.Status {
		case models.ActionSucceeded:
			done = &a
		case models.ActionPending:
			queued = &a
		}
	}
	if done == nil || queued == nil {
		t.Fatalf("actions = %+v", e.actions(g.ID))
	}
	if err := e.svc.Restore(e.ctx, done.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	wantStatus(t, e.group(g.ID), models.GroupIgnored)
	wantActionStatus(t, e.action(queued.ID), models.ActionCancelled)
	e.update(func(s *models.Settings) { s.MaxDeletionsPerRun = 25 })
	e.mustProcess()
	for _, rel := range []string{loserRel, third} {
		if !exists(e.local(rel)) {
			t.Fatalf("%s was removed after the restore", rel)
		}
	}
}

// TestRestoreKeepsTheUsersIgnoreReason: a group the user already ignored keeps their reason and
// gets no second groupIgnored event.
func TestRestoreKeepsTheUsersIgnoreReason(t *testing.T) {
	e, g, acts := restoredEnv(t)
	if err := e.db.Groups().UpdateStatus(e.ctx, g.ID, models.GroupIgnored, "Ignored by user"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(e.ctx, acts[0].ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got := e.group(g.ID)
	if got.Status != models.GroupIgnored || got.StatusReason != "Ignored by user" {
		t.Fatalf("group = %s (%s)", got.Status, got.StatusReason)
	}
	if n := len(e.history(models.EventGroupIgnored)); n != 0 {
		t.Fatalf("groupIgnored events = %d", n)
	}
}

// setKeepPer sets the default profile's keepPer (creating the default from the first template when
// the database has none).
func setKeepPer(t *testing.T, e *testEnv, keepPer string) {
	t.Helper()
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
	p.KeepPer = keepPer
	if err := e.db.Profiles().Update(e.ctx, p); err != nil {
		t.Fatal(err)
	}
}

// partitionGroup stores "Film" with a kept 2160p version (1), a kept 1080p version (2) and a
// 1080p version to remove (3); tweak adjusts the stored files before the approval.
func partitionGroup(t *testing.T, e *testEnv, keeper1080 vspec, tweak func(g *models.DuplicateGroup)) *models.DuplicateGroup {
	t.Helper()
	const other1080 = filmDir + "/Film.1080p.WEB.mkv"
	g := e.addGroup("Film", keep(1, keeperRel), keeper1080, remove(3, other1080))
	g.Files[0].Version.Resolution, g.Files[0].Version.Width, g.Files[0].Version.Height = models.Res2160, 3840, 2160
	if tweak != nil {
		tweak(g)
	}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	return e.group(g.ID)
}

// TestKeepPerPartitionNeedsItsOwnKeeper: with "keep one per resolution", removing a 1080p copy needs
// the kept 1080p copy verified present; the verified 2160p keeper does not stand in for it.
func TestKeepPerPartitionNeedsItsOwnKeeper(t *testing.T) {
	const other1080 = filmDir + "/Film.1080p.WEB.mkv"
	t.Run("the partition's keeper is missing: nothing is removed", func(t *testing.T) {
		e := newEnv(t)
		setKeepPer(t, e, models.KeepPerResolution)
		g := partitionGroup(t, e, keep(2, loserRel).without(), nil)
		acts := e.approve(g.ID)
		sum := e.mustProcess()
		if sum.Succeeded != 0 || !exists(e.local(other1080)) || len(e.log.mutations()) != 0 {
			t.Fatalf("the last 1080p copy was removed: %+v %v", sum, e.log.all())
		}
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "No kept 1080p version could be verified")
		contains(t, "message", a.Message, "keeps one per resolution")
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if len(e.scans()) != 1 {
			t.Fatalf("re-scans = %+v", e.scans())
		}
	})
	t.Run("every partition's keeper is present: removed", func(t *testing.T) {
		e := newEnv(t)
		setKeepPer(t, e, models.KeepPerResolution)
		g := partitionGroup(t, e, keep(2, loserRel), nil)
		acts := e.approve(g.ID)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		if exists(e.local(other1080)) {
			t.Fatal("the loser is still there")
		}
	})
	t.Run("without keep-per one verified keeper overall suffices", func(t *testing.T) {
		e := newEnv(t)
		setKeepPer(t, e, models.KeepPerNone)
		g := partitionGroup(t, e, keep(2, loserRel).without(), nil)
		acts := e.approve(g.ID)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	})
	t.Run("the user overrode the partition's keeper to remove", func(t *testing.T) {
		e := newEnv(t)
		setKeepPer(t, e, models.KeepPerResolution)
		g := partitionGroup(t, e, keep(2, loserRel), func(g *models.DuplicateGroup) {
			// Upsert keeps the stored override: set it first, then store the effective decision.
			if err := e.db.Groups().SetOverride(e.ctx, g.ID, g.Files[1].ID, models.DecisionRemove); err != nil {
				t.Fatal(err)
			}
			g.Files[1].Decision, g.Files[1].Override = models.DecisionRemove, models.DecisionRemove
		})
		if f := g.Files[1]; f.Override != models.DecisionRemove || f.EngineDecision != models.DecisionKeep || f.Decision != models.DecisionRemove {
			t.Fatalf("file = %+v", f)
		}
		acts := e.approve(g.ID)
		if len(acts) != 2 {
			t.Fatalf("actions = %+v", acts)
		}
		e.mustProcess()
		for _, a := range acts {
			wantActionStatus(t, e.action(a.ID), models.ActionSucceeded)
		}
		if !exists(e.local(keeperRel)) {
			t.Fatal("the 2160p keeper is gone")
		}
	})
	t.Run("decisions made without keep-per: re-scan instead of removing", func(t *testing.T) {
		e := newEnv(t)
		setKeepPer(t, e, models.KeepPerResolution)
		// The engine (under the old profile) kept only the 2160p version.
		g := partitionGroup(t, e, remove(2, loserRel), nil)
		acts := e.approve(g.ID)
		e.mustProcess()
		if !exists(e.local(loserRel)) || !exists(e.local(other1080)) || len(e.log.mutations()) != 0 {
			t.Fatalf("a 1080p copy was removed: %v", e.log.all())
		}
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "keep no 1080p version")
		wantStatus(t, e.group(g.ID), models.GroupReview)
	})
}
