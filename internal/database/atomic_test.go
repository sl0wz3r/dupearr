package database

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestGroupUpdateStatusIf: the compare-and-set only writes while the group is in one of the
// expected statuses, keeps UpdateStatus's side effects and never overwrites a concurrent change.
func TestGroupUpdateStatusIf(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Status = models.GroupQueued
	upsert(t, d, g)
	a := queueRemoval(t, d, g, 1)

	ok, err := d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupPending}, models.GroupFailed, "nope")
	must(t, err)
	if ok {
		t.Fatal("written although the group is not pending")
	}
	if got := getGroup(t, d, g.ID); got.Status != models.GroupQueued {
		t.Fatalf("status = %q", got.Status)
	}

	ok, err = d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupQueued, models.GroupFailed}, models.GroupFailed, "a removal failed")
	must(t, err)
	if !ok {
		t.Fatal("not written")
	}
	got := getGroup(t, d, g.ID)
	if got.Status != models.GroupFailed || got.StatusReason != "a removal failed" {
		t.Fatalf("status = %q (%q)", got.Status, got.StatusReason)
	}
	// failed does not block removals: the queue entry stays.
	if s := actionStatus(t, d, a); s != models.ActionPending {
		t.Fatalf("action = %q", s)
	}

	// Same side effects as UpdateStatus: review cancels the pending actions.
	ok, err = d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupFailed}, models.GroupReview, "stale")
	must(t, err)
	if !ok {
		t.Fatal("not written")
	}
	if s := actionStatus(t, d, a); s != models.ActionCancelled {
		t.Fatalf("action = %q, want cancelled", s)
	}

	// An ignored group is never overwritten by a status decided before the user ignored it.
	must(t, d.Groups().UpdateStatus(ctx, g.ID, models.GroupIgnored, "user"))
	ok, err = d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupQueued, models.GroupResolved}, models.GroupResolved, "done")
	must(t, err)
	if ok || getGroup(t, d, g.ID).Status != models.GroupIgnored {
		t.Fatal("an ignored group was overwritten")
	}

	_, err = d.Groups().UpdateStatusIf(ctx, 999, []models.GroupStatus{models.GroupQueued}, models.GroupFailed, "")
	wantNotFound(t, err)
	if _, err := d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupIgnored}, "bogus", ""); err == nil {
		t.Fatal("invalid target status accepted")
	}
	if _, err := d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{"bogus"}, models.GroupPending, ""); err == nil {
		t.Fatal("invalid source status accepted")
	}
	if ok, err := d.Groups().UpdateStatusIf(ctx, g.ID, nil, models.GroupPending, ""); err != nil || ok {
		t.Fatalf("empty from: ok=%v err=%v", ok, err)
	}
}

// TestGroupUpdateStatusIf_Concurrent: of many racing compare-and-sets from "queued", exactly one
// wins.
func TestGroupUpdateStatusIf_Concurrent(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Status = models.GroupQueued
	upsert(t, d, g)
	targets := []models.GroupStatus{models.GroupFailed, models.GroupResolved, models.GroupPending, models.GroupReview}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []models.GroupStatus
	)
	for i := range 16 {
		wg.Add(1)
		go func(to models.GroupStatus) {
			defer wg.Done()
			ok, err := d.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{models.GroupQueued}, to, "race")
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				wins = append(wins, to)
				mu.Unlock()
			}
		}(targets[i%len(targets)])
	}
	wg.Wait()
	if len(wins) != 1 || getGroup(t, d, g.ID).Status != wins[0] {
		t.Fatalf("winners = %v, stored %q", wins, getGroup(t, d, g.ID).Status)
	}
}

// TestGroupSetFlag: flags are added and removed atomically without touching anything else.
func TestGroupSetFlag(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Flags = []string{models.FlagPlaying + "-not", "suspect_merge"}
	g.Status = models.GroupQueued
	g.StatusReason = "queued for removal"
	upsert(t, d, g)
	before := getGroup(t, d, g.ID)

	must(t, d.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, true))
	must(t, d.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, true)) // idempotent
	got := getGroup(t, d, g.ID)
	if !slices.Equal(got.Flags, append(slices.Clone(before.Flags), models.FlagPlaying)) {
		t.Fatalf("flags = %v", got.Flags)
	}
	if got.Status != before.Status || got.StatusReason != before.StatusReason || len(got.Files) != len(before.Files) ||
		got.Files[1].Decision != before.Files[1].Decision {
		t.Fatalf("SetFlag changed more than the flags: %+v", got)
	}

	must(t, d.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, false))
	must(t, d.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, false))
	if got := getGroup(t, d, g.ID); !slices.Equal(got.Flags, before.Flags) {
		t.Fatalf("flags after removal = %v, want %v", got.Flags, before.Flags)
	}

	wantNotFound(t, d.Groups().SetFlag(ctx, 999, models.FlagPlaying, true))
	if err := d.Groups().SetFlag(ctx, g.ID, " ", true); err == nil {
		t.Fatal("empty flag accepted")
	}

	// Concurrent adds of different flags are all kept (no lost update).
	var wg sync.WaitGroup
	names := []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8"}
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if err := d.Groups().SetFlag(ctx, g.ID, n, true); err != nil {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	got = getGroup(t, d, g.ID)
	for _, n := range names {
		if !slices.Contains(got.Flags, n) {
			t.Fatalf("flag %s lost: %v", n, got.Flags)
		}
	}
}

// TestActionsCancelIfPending: only a pending action is cancelled; a running removal is never
// marked cancelled; the group of an emptied queue is reopened.
func TestActionsCancelIfPending(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("movie:tmdb:1", 1, 1)
	g.Files = append(g.Files, testFile("movie:tmdb:1/c", "rk", gib, models.DecisionRemove))
	g.Status = models.GroupQueued
	upsert(t, d, g)
	pending := queueRemoval(t, d, g, 1)
	running := queueRemoval(t, d, g, 2)
	running.Status = models.ActionRunning
	must(t, d.Actions().Update(ctx, running))

	ok, err := d.Actions().CancelIfPending(ctx, running.ID)
	must(t, err)
	if ok || actionStatus(t, d, running) != models.ActionRunning {
		t.Fatal("a running action was cancelled")
	}

	ok, err = d.Actions().CancelIfPending(ctx, pending.ID)
	must(t, err)
	if !ok {
		t.Fatal("pending action not cancelled")
	}
	got, err := d.Actions().Get(ctx, pending.ID)
	must(t, err)
	if got.Status != models.ActionCancelled || got.FinishedAt == nil || got.Message != cancelUserMessage {
		t.Fatalf("cancelled action = %+v", got)
	}
	// Another removal is still running: the group stays queued.
	if s := getGroup(t, d, g.ID).Status; s != models.GroupQueued {
		t.Fatalf("group = %q while a removal runs", s)
	}
	ok, err = d.Actions().CancelIfPending(ctx, pending.ID)
	must(t, err)
	if ok {
		t.Fatal("cancelled twice")
	}

	_, err = d.Actions().CancelIfPending(ctx, 999)
	wantNotFound(t, err)
	if errors.Is(err, ErrActionNotPending) {
		t.Fatal("a missing action is not ErrActionNotPending")
	}

	t.Run("last queued removal reopens the group", func(t *testing.T) {
		d := newTestDB(t)
		g := testGroup("movie:tmdb:2", 1, 1)
		g.Status = models.GroupQueued
		upsert(t, d, g)
		a := queueRemoval(t, d, g, 1)
		ok, err := d.Actions().CancelIfPending(ctx, a.ID)
		must(t, err)
		if !ok {
			t.Fatal("not cancelled")
		}
		if got := getGroup(t, d, g.ID); got.Status != models.GroupPending || got.StatusReason != reopenedReason {
			t.Fatalf("group = %q (%q)", got.Status, got.StatusReason)
		}
	})
	t.Run("races with the executor starting it", func(t *testing.T) {
		for range 20 {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:3", 1, 1)
			g.Status = models.GroupQueued
			upsert(t, d, g)
			a := queueRemoval(t, d, g, 1)
			var (
				wg        sync.WaitGroup
				cancelled bool
				startErr  error
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				cancelled, _ = d.Actions().CancelIfPending(ctx, a.ID)
			}()
			go func() {
				defer wg.Done()
				start := *a
				start.Status = models.ActionRunning
				startErr = d.Actions().Update(ctx, &start)
			}()
			wg.Wait()
			final := actionStatus(t, d, a)
			switch {
			case cancelled && (startErr == nil || final != models.ActionCancelled):
				t.Fatalf("both won: cancelled, start err %v, final %q", startErr, final)
			case !cancelled && (startErr != nil || final != models.ActionRunning):
				t.Fatalf("neither won: start err %v, final %q", startErr, final)
			}
		}
	})
}
