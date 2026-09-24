package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestApproveCreatesPendingActions(t *testing.T) {
	e := newEnv(t)
	sub, unsub := e.bus.Subscribe(64)
	defer unsub()
	g := e.addGroup("Film",
		keep(1, "movies/Film (2020)/Film.2160p.mkv"),
		remove(2, "movies/Film (2020)/Film.1080p.mkv"),
		remove(3, "movies/Film (2020)/Film.cd1.avi", "movies/Film (2020)/Film.cd2.avi").sized(500),
		keep(4, "movies/Film (2020)/Film.720p.mkv").protected(),
	)
	acts, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if len(acts) != 2 {
		t.Fatalf("got %d actions, want 2", len(acts))
	}
	byKey := map[string]models.Action{}
	for _, a := range acts {
		byKey[a.VersionKey] = a
		if a.ID == 0 || a.Status != models.ActionPending || a.GroupID != g.ID || a.Title != "Film (2020)" || a.DryRun {
			t.Errorf("unexpected action %+v", a)
		}
	}
	stacked := byKey[g.Files[2].Version.Key]
	if len(stacked.Paths) != 2 || stacked.Size != 1000 || stacked.GroupFileID != g.Files[2].ID {
		t.Errorf("stacked action = %+v", stacked)
	}
	if acts[0].CreatedAt != acts[1].CreatedAt {
		t.Errorf("actions of one approval must share a batch stamp: %v vs %v", acts[0].CreatedAt, acts[1].CreatedAt)
	}
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupQueued)
	contains(t, "reason", got.StatusReason, "manual")
	h := e.history(models.EventGroupApproved)
	if len(h) != 1 || h[0].GroupID == nil || *h[0].GroupID != g.ID {
		t.Fatalf("history = %+v", h)
	}
	seen := map[string]int{}
	for len(sub) > 0 {
		ev := <-sub
		seen[ev.Name]++
	}
	if seen[events.NameQueue] != 2 || seen[events.NameDuplicate] != 1 || seen[events.NameHistory] != 1 {
		t.Errorf("events = %v", seen)
	}
}

func TestApproveDryRunComesFromSettings(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DryRun = true })
	g := e.addGroup("Film", keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv"))
	acts := e.approve(g.ID)
	if len(acts) != 1 || !acts[0].DryRun {
		t.Fatalf("actions = %+v, want one dry-run action", acts)
	}
	contains(t, "reason", e.group(g.ID).StatusReason, "dry run")
}

func TestApproveStatusAndTriggerRules(t *testing.T) {
	cases := []struct {
		name    string
		status  models.GroupStatus
		trigger string
		auto    bool
		stable  int
		wantErr bool
	}{
		{"pending manual", models.GroupPending, models.TriggerManual, false, 0, false},
		{"review manual", models.GroupReview, models.TriggerManual, false, 0, false},
		{"deferred manual", models.GroupDeferred, models.TriggerManual, false, 0, false},
		{"failed manual", models.GroupFailed, models.TriggerManual, false, 0, false},
		{"review scheduled", models.GroupReview, models.TriggerScheduled, true, 5, true},
		{"review webhook", models.GroupReview, models.TriggerWebhook, true, 5, true},
		{"review empty trigger", models.GroupReview, "", true, 5, true},
		{"failed scheduled", models.GroupFailed, models.TriggerScheduled, true, 5, true},
		{"deferred scheduled", models.GroupDeferred, models.TriggerScheduled, true, 5, true},
		{"resolved manual", models.GroupResolved, models.TriggerManual, false, 0, true},
		{"ignored manual", models.GroupIgnored, models.TriggerManual, false, 0, true},
		{"protected manual", models.GroupProtected, models.TriggerManual, false, 0, true},
		{"pending scheduled in manual mode", models.GroupPending, models.TriggerScheduled, false, 5, true},
		{"pending scheduled, not stable yet", models.GroupPending, models.TriggerScheduled, true, 1, true},
		{"pending scheduled, stable", models.GroupPending, models.TriggerScheduled, true, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.auto {
				e.update(func(s *models.Settings) { s.Mode = models.ModeAuto })
			}
			g := e.addGroup("Film", keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv"))
			g.Status, g.StableCount = tc.status, tc.stable
			if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
				t.Fatal(err)
			}
			_, err := e.svc.Approve(e.ctx, g.ID, tc.trigger)
			if tc.wantErr {
				if !errors.Is(err, ErrNotApprovable) {
					t.Fatalf("err = %v, want ErrNotApprovable", err)
				}
				if n := len(e.actions(g.ID)); n != 0 {
					t.Fatalf("%d actions were created for a refused approval", n)
				}
				if got := e.group(g.ID).Status; got != tc.status {
					t.Fatalf("status changed to %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Approve: %v", err)
			}
			wantStatus(t, e.group(g.ID), models.GroupQueued)
		})
	}
}

func TestApproveRefusesAlreadyQueued(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv"))
	e.approve(g.ID)
	if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); !errors.Is(err, ErrNotApprovable) {
		t.Fatalf("second approval: err = %v, want ErrNotApprovable", err)
	}
	if n := len(e.actions(g.ID)); n != 1 {
		t.Fatalf("%d actions, want 1", n)
	}
}

func TestApproveNothingToRemove(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, "movies/a.mkv"), keep(2, "movies/b.mkv"))
	if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); !errors.Is(err, ErrNothingToRemove) {
		t.Fatalf("err = %v, want ErrNothingToRemove", err)
	}
	wantStatus(t, e.group(g.ID), models.GroupPending)
}

func TestApproveRejectsInvariantViolations(t *testing.T) {
	cases := []struct {
		name  string
		specs []vspec
		flag  string
	}{
		{"no keeper", []vspec{remove(1, "movies/a.mkv"), remove(2, "movies/b.mkv")}, ""},
		{"removing a protected version", []vspec{keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv").protected()}, ""},
		{"same file as the keeper", []vspec{keep(1, "movies/a.mkv"), remove(2, "movies/a.mkv")}, ""},
		{"younger than the minimum age", []vspec{keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv")}, models.FlagMinAge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", tc.specs...)
			if tc.flag != "" {
				g.Flags = append(g.Flags, tc.flag)
				if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
					t.Fatal(err)
				}
				g = e.group(g.ID)
			}
			if g.Status != models.GroupPending {
				// The store sends a group without keeper to review; approve it manually anyway.
				if g.Status != models.GroupReview {
					t.Fatalf("unexpected status %q", g.Status)
				}
			}
			_, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual)
			if err == nil {
				t.Fatal("Approve succeeded, want an error")
			}
			if !errors.Is(err, engine.ErrInvariant) && !errors.Is(err, ErrNothingToRemove) {
				t.Fatalf("err = %v, want engine.ErrInvariant", err)
			}
			if n := len(e.actions(g.ID)); n != 0 {
				t.Fatalf("%d actions created", n)
			}
		})
	}
}

func TestApproveMissingGroup(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Approve(e.ctx, 999, models.TriggerManual); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func TestApproveReplacesLeftoverActionsOfFailedGroup(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv"), remove(3, "movies/c.mkv"))
	first := e.approve(g.ID)
	// Simulate an aborted run: the group failed with one removal still queued.
	if err := e.db.Groups().UpdateStatus(e.ctx, g.ID, models.GroupFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	second := e.approve(g.ID)
	if len(second) != 2 {
		t.Fatalf("second approval created %d actions, want 2", len(second))
	}
	for _, a := range first {
		if got := e.action(a.ID); got.Status != models.ActionCancelled {
			t.Errorf("leftover action %d status = %q, want cancelled", a.ID, got.Status)
		}
	}
	if !second[0].CreatedAt.After(first[0].CreatedAt) {
		t.Errorf("the new batch stamp %v must be after the old one %v", second[0].CreatedAt, first[0].CreatedAt)
	}
	wantStatus(t, e.group(g.ID), models.GroupQueued)
}

func TestCancelGroup(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, "movies/a.mkv"), remove(2, "movies/b.mkv"))
	acts := e.approve(g.ID)
	if err := e.svc.CancelGroup(e.ctx, g.ID); err != nil {
		t.Fatalf("CancelGroup: %v", err)
	}
	wantActionStatus(t, e.action(acts[0].ID), models.ActionCancelled)
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupPending)
	if !strings.Contains(got.StatusReason, "cancelled") {
		t.Errorf("reason = %q", got.StatusReason)
	}
	// Nothing left to run.
	sum := e.mustProcess()
	if sum.Processed != 0 || len(e.log.mutations()) != 0 {
		t.Fatalf("summary %+v, mutations %v", sum, e.log.mutations())
	}
	// A resolved group keeps its status.
	g2 := e.addGroup("Other", keep(5, "movies/x.mkv"), remove(6, "movies/y.mkv"))
	if err := e.db.Groups().UpdateStatus(e.ctx, g2.ID, models.GroupResolved, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CancelGroup(e.ctx, g2.ID); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.group(g2.ID), models.GroupResolved)
	if err := e.svc.CancelGroup(e.ctx, 12345); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group: err = %v", err)
	}
}
