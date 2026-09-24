package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

var t0 = time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func TestActions_CreateGetUpdate(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Actions()

	g := testGroup("movie:tmdb:1", 1, 1)
	upsert(t, d, g)
	a := &models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID, VersionKey: g.Files[1].Version.Key,
		Title: "Movie", Size: 42}
	must(t, r.Create(ctx, a))
	if a.ID == 0 || a.Status != models.ActionPending || a.CreatedAt.IsZero() || a.Paths == nil {
		t.Fatalf("Create = %+v", a)
	}
	got, err := r.Get(ctx, a.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *a) {
		t.Fatalf("Get = %+v, want %+v", got, a)
	}

	started, finished := at(1), at(2)
	a.Status, a.Method, a.Message, a.RecyclePath, a.Permanent = models.ActionSucceeded, models.MethodFilesystem, "moved", "/recycle/x", false
	a.Paths, a.StartedAt, a.FinishedAt, a.DryRun = []string{"/m/a.mkv", "/m/b.mkv"}, &started, &finished, true
	must(t, r.Update(ctx, a))
	got, err = r.Get(ctx, a.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *a) {
		t.Fatalf("after Update = %+v, want %+v", got, a)
	}

	// Times can be cleared again.
	a.StartedAt, a.FinishedAt = nil, nil
	must(t, r.Update(ctx, a))
	got, err = r.Get(ctx, a.ID)
	must(t, err)
	if got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("times not cleared: %+v", got)
	}

	if err := r.Create(ctx, &models.Action{Status: "exploded"}); err == nil {
		t.Fatal("Create with an invalid status succeeded")
	}
	a.Status = "bogus"
	if err := r.Update(ctx, a); err == nil {
		t.Fatal("Update with an invalid status succeeded")
	}
	_, err = r.Get(ctx, 999)
	wantNotFound(t, err)
	wantNotFound(t, r.Update(ctx, &models.Action{ID: 999, Status: models.ActionFailed}))
}

// TestActions_StartGuard: status changes that would let a stale copy of an action undo a
// cancellation or rewrite a finished removal are refused and leave the row untouched: an action
// can only be started while pending (or already running), a cancelled/succeeded one is never
// re-queued, and a finished one is never re-labelled cancelled.
func TestActions_StartGuard(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		from    models.ActionStatus
		to      models.ActionStatus
		wantErr error
	}{
		{"pending to running", models.ActionPending, models.ActionRunning, nil},
		{"running to running", models.ActionRunning, models.ActionRunning, nil},
		{"running to succeeded", models.ActionRunning, models.ActionSucceeded, nil},
		{"running to pending (deferred)", models.ActionRunning, models.ActionPending, nil},
		{"pending to pending (message)", models.ActionPending, models.ActionPending, nil},
		{"pending to skipped", models.ActionPending, models.ActionSkipped, nil},
		{"pending to dry run", models.ActionPending, models.ActionDryRun, nil},
		{"pending to cancelled", models.ActionPending, models.ActionCancelled, nil},
		{"running to cancelled", models.ActionRunning, models.ActionCancelled, nil},
		{"failed to pending (retry)", models.ActionFailed, models.ActionPending, nil},
		{"skipped to pending (retry)", models.ActionSkipped, models.ActionPending, nil},
		{"dry run to pending (run for real)", models.ActionDryRun, models.ActionPending, nil},
		{"cancelled to failed (record)", models.ActionCancelled, models.ActionFailed, nil},
		{"cancelled to running", models.ActionCancelled, models.ActionRunning, ErrActionNotPending},
		{"succeeded to running", models.ActionSucceeded, models.ActionRunning, ErrActionNotPending},
		{"dry run to running", models.ActionDryRun, models.ActionRunning, ErrActionNotPending},
		{"failed to running", models.ActionFailed, models.ActionRunning, ErrActionNotPending},
		{"cancelled to pending (resurrection)", models.ActionCancelled, models.ActionPending, ErrActionNotPending},
		{"succeeded to pending", models.ActionSucceeded, models.ActionPending, ErrActionNotPending},
		{"succeeded to cancelled", models.ActionSucceeded, models.ActionCancelled, ErrActionNotPending},
		{"failed to cancelled", models.ActionFailed, models.ActionCancelled, ErrActionNotPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			r := d.Actions()
			g := testGroup("movie:tmdb:1", 1, 1)
			upsert(t, d, g)
			a := &models.Action{GroupID: g.ID, VersionKey: g.Files[1].Version.Key, Status: tt.from, Message: "before"}
			must(t, r.Create(ctx, a))

			upd := *a
			upd.Status, upd.Message, upd.StartedAt = tt.to, "after", ptr(at(1))
			err := r.Update(ctx, &upd)
			got, gerr := r.Get(ctx, a.ID)
			must(t, gerr)
			if tt.wantErr == nil {
				must(t, err)
				if got.Status != tt.to || got.Message != "after" {
					t.Fatalf("stored %q/%q, want %q/after", got.Status, got.Message, tt.to)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got.Status != tt.from || got.Message != "before" || got.StartedAt != nil {
				t.Fatalf("refused update changed the row: %+v", got)
			}
		})
	}
	d := newTestDB(t)
	wantNotFound(t, d.Actions().Update(ctx, &models.Action{ID: 42, Status: models.ActionRunning}))
}

// TestActions_CreateRequiresRemovableTarget: a removal can only be queued (pending/running) for a
// version currently stored with decision "remove" in a live group; this closes the race between
// an approval reading the group and the user overriding/ignoring it.
func TestActions_CreateRequiresRemovableTarget(t *testing.T) {
	ctx := context.Background()
	type env struct {
		g, other *models.DuplicateGroup
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, d *DB, e env)
		action  func(e env) *models.Action
		wantErr bool
	}{
		{"by file id", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, false},
		{"by version key", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, VersionKey: e.g.Files[1].Version.Key}
		}, false},
		{"by both", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID, VersionKey: e.g.Files[1].Version.Key}
		}, false},
		{"created running", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID, Status: models.ActionRunning}
		}, false},
		{"review group (manual approval)", func(t *testing.T, d *DB, e env) {
			must(t, d.Groups().UpdateStatus(ctx, e.g.ID, models.GroupReview, "suspect"))
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, false},
		{"finished action needs no target", nil, func(e env) *models.Action {
			return &models.Action{GroupID: 999, Status: models.ActionSucceeded}
		}, false},
		{"no target", nil, func(e env) *models.Action { return &models.Action{GroupID: e.g.ID} }, true},
		{"keeper", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[0].ID}
		}, true},
		{"keeper created running", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, VersionKey: e.g.Files[0].Version.Key, Status: models.ActionRunning}
		}, true},
		{"file id and version key disagree", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID, VersionKey: e.g.Files[0].Version.Key}
		}, true},
		{"file of another group", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.other.Files[1].ID}
		}, true},
		{"unknown version key", nil, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, VersionKey: "plex:1:404"}
		}, true},
		{"unknown group", nil, func(e env) *models.Action {
			return &models.Action{GroupID: 999, GroupFileID: e.g.Files[1].ID}
		}, true},
		{"overridden to keep meanwhile", func(t *testing.T, d *DB, e env) {
			must(t, d.Groups().SetOverride(ctx, e.g.ID, e.g.Files[1].ID, models.DecisionKeep))
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, true},
		{"ignored group", func(t *testing.T, d *DB, e env) {
			must(t, d.Groups().UpdateStatus(ctx, e.g.ID, models.GroupIgnored, ""))
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, true},
		{"resolved group", func(t *testing.T, d *DB, e env) {
			_, err := d.Groups().MarkUnseenResolved(ctx, 99, []int64{1})
			must(t, err)
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, true},
		{"deleted group", func(t *testing.T, d *DB, e env) {
			must(t, d.Groups().Delete(ctx, e.g.ID))
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, true},
		{"protected version", func(t *testing.T, d *DB, e env) {
			re := testGroup("movie:tmdb:1", 1, 2)
			re.Files[1].Protected = true
			upsert(t, d, re)
		}, func(e env) *models.Action {
			return &models.Action{GroupID: e.g.ID, GroupFileID: e.g.Files[1].ID}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			e := env{g: testGroup("movie:tmdb:1", 1, 1), other: testGroup("movie:tmdb:2", 1, 1)}
			upsert(t, d, e.g)
			upsert(t, d, e.other)
			if tt.setup != nil {
				tt.setup(t, d, e)
			}
			a := tt.action(e)
			err := d.Actions().Create(ctx, a)
			if !tt.wantErr {
				must(t, err)
				if a.ID == 0 {
					t.Fatal("Create did not set ID")
				}
				return
			}
			if !errors.Is(err, ErrNotRemovable) {
				t.Fatalf("Create = %v, want ErrNotRemovable", err)
			}
			page, err := d.Actions().List(ctx, nil, store.Paging{})
			must(t, err)
			if page.TotalRecords != 0 {
				t.Fatalf("refused Create stored %d actions", page.TotalRecords)
			}
		})
	}
}

// TestActions_UpdateRevalidatesTarget: queueing or starting an action re-checks its target in the
// same transaction. Even if the target stopped being a removal without the queue being cleaned
// up (simulated by editing the row directly), the executor cannot start it: the store cancels it.
func TestActions_UpdateRevalidatesTarget(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		from, to   models.ActionStatus
		wantErr    []error
		wantStored models.ActionStatus
	}{
		{"pending to running", models.ActionPending, models.ActionRunning,
			[]error{ErrActionNotPending, ErrNotRemovable}, models.ActionCancelled},
		{"pending to pending", models.ActionPending, models.ActionPending,
			[]error{ErrActionNotPending, ErrNotRemovable}, models.ActionCancelled},
		{"running to pending", models.ActionRunning, models.ActionPending,
			[]error{ErrActionNotPending, ErrNotRemovable}, models.ActionCancelled},
		{"failed to pending (retry)", models.ActionFailed, models.ActionPending,
			[]error{ErrNotRemovable}, models.ActionFailed},
		{"running to running (progress)", models.ActionRunning, models.ActionRunning, nil, models.ActionRunning},
		{"running to succeeded", models.ActionRunning, models.ActionSucceeded, nil, models.ActionSucceeded},
		{"pending to cancelled", models.ActionPending, models.ActionCancelled, nil, models.ActionCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("movie:tmdb:1", 1, 1)
			upsert(t, d, g)
			a := queueRemoval(t, d, g, 1)
			if tt.from != models.ActionPending {
				a.Status = tt.from
				if tt.from == models.ActionFailed {
					a.Status = models.ActionRunning
					must(t, d.Actions().Update(ctx, a))
					a.Status = models.ActionFailed
				}
				must(t, d.Actions().Update(ctx, a))
			}
			// The version stops being a removal behind the queue's back.
			_, err := d.w.ExecContext(ctx, `UPDATE group_files SET decision = 'keep' WHERE id = ?`, g.Files[1].ID)
			must(t, err)

			upd := *a
			upd.Status, upd.Message = tt.to, "after"
			err = d.Actions().Update(ctx, &upd)
			for _, want := range tt.wantErr {
				if !errors.Is(err, want) {
					t.Fatalf("Update = %v, want it to wrap %v", err, want)
				}
			}
			if len(tt.wantErr) == 0 {
				must(t, err)
			}
			got, gerr := d.Actions().Get(ctx, a.ID)
			must(t, gerr)
			if got.Status != tt.wantStored {
				t.Fatalf("stored status = %q, want %q", got.Status, tt.wantStored)
			}
			if len(tt.wantErr) > 0 && got.Message == "after" {
				t.Fatal("refused update was written")
			}
			if tt.wantStored == models.ActionCancelled && len(tt.wantErr) > 0 && (got.FinishedAt == nil || got.Message != cancelStaleMessage) {
				t.Fatalf("store-cancelled action = %+v", got)
			}
		})
	}
}

// seedActions creates actions with increasing CreatedAt; returns them in creation order.
func seedActions(t *testing.T, d *DB) []*models.Action {
	t.Helper()
	// Groups 1..3, each with a version marked "remove" (Files[1]) that pending actions target.
	targets := map[int64]int64{} // group id → file id
	for i := int64(1); i <= 3; i++ {
		g := testGroup(fmt.Sprint("movie:tmdb:", i), 1, 1)
		upsert(t, d, g)
		if g.ID != i {
			t.Fatalf("group %d got id %d", i, g.ID)
		}
		targets[g.ID] = g.Files[1].ID
	}
	specs := []struct {
		group  int64
		status models.ActionStatus
		dry    bool
		size   int64
	}{
		{1, models.ActionPending, false, 10},
		{1, models.ActionSucceeded, false, 100},
		{2, models.ActionPending, false, 20},
		{2, models.ActionSucceeded, true, 1000}, // dry run: not reclaimed
		{3, models.ActionFailed, false, 5},
		{1, models.ActionPending, false, 30},
		{3, models.ActionSucceeded, false, 7},
	}
	out := make([]*models.Action, len(specs))
	for i, s := range specs {
		// Created out of order to prove ordering uses CreatedAt, not insertion order.
		minute := i
		if i == 0 {
			minute = 3 // the first inserted action is not the oldest pending one
		}
		a := &models.Action{GroupID: s.group, GroupFileID: targets[s.group], Status: s.status, DryRun: s.dry, Size: s.size,
			Title: fmt.Sprint("a", i), CreatedAt: at(minute)}
		must(t, d.Actions().Create(context.Background(), a))
		out[i] = a
	}
	return out
}

func actionIDs(actions []models.Action) []int64 {
	out := make([]int64, len(actions))
	for i, a := range actions {
		out[i] = a.ID
	}
	return out
}

func TestActions_Listing(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	a := seedActions(t, d)
	r := d.Actions()

	t.Run("ListPending oldest first", func(t *testing.T) {
		got, err := r.ListPending(ctx, 0)
		must(t, err)
		want := []int64{a[2].ID, a[0].ID, a[5].ID} // minutes 2, 3, 5
		if fmt.Sprint(actionIDs(got)) != fmt.Sprint(want) {
			t.Fatalf("ListPending = %v, want %v", actionIDs(got), want)
		}
		got, err = r.ListPending(ctx, 2)
		must(t, err)
		if len(got) != 2 || got[0].ID != a[2].ID {
			t.Fatalf("ListPending(2) = %v", actionIDs(got))
		}
	})

	t.Run("ListByGroup", func(t *testing.T) {
		got, err := r.ListByGroup(ctx, 1)
		must(t, err)
		want := []int64{a[1].ID, a[0].ID, a[5].ID} // minutes 1, 3, 5
		if fmt.Sprint(actionIDs(got)) != fmt.Sprint(want) {
			t.Fatalf("ListByGroup = %v, want %v", actionIDs(got), want)
		}
		got, err = r.ListByGroup(ctx, 99)
		must(t, err)
		if got == nil || len(got) != 0 {
			t.Fatalf("ListByGroup(99) = %#v", got)
		}
	})

	tests := []struct {
		name     string
		statuses []models.ActionStatus
		p        store.Paging
		want     []int64
		total    int
	}{
		{"all newest first", nil, store.Paging{}, []int64{a[6].ID, a[5].ID, a[4].ID, a[3].ID, a[0].ID, a[2].ID, a[1].ID}, 7},
		{"status filter", []models.ActionStatus{models.ActionSucceeded, models.ActionFailed}, store.Paging{}, []int64{a[6].ID, a[4].ID, a[3].ID, a[1].ID}, 4},
		{"paged", nil, store.Paging{Page: 2, PageSize: 3}, []int64{a[3].ID, a[0].ID, a[2].ID}, 7},
		{"queue view oldest first", []models.ActionStatus{models.ActionPending, models.ActionRunning}, store.Paging{SortKey: "createdAt", SortDirection: "ascending"}, []int64{a[2].ID, a[0].ID, a[5].ID}, 3},
		{"by size", nil, store.Paging{SortKey: "size", PageSize: 2}, []int64{a[3].ID, a[1].ID}, 7},
		{"no match", []models.ActionStatus{models.ActionCancelled}, store.Paging{}, []int64{}, 0},
	}
	for _, tt := range tests {
		t.Run("List "+tt.name, func(t *testing.T) {
			page, err := r.List(ctx, tt.statuses, tt.p)
			must(t, err)
			if fmt.Sprint(actionIDs(page.Records)) != fmt.Sprint(tt.want) || page.TotalRecords != tt.total {
				t.Fatalf("List = %v (total %d), want %v (total %d)", actionIDs(page.Records), page.TotalRecords, tt.want, tt.total)
			}
			if page.Records == nil {
				t.Fatal("Records is nil")
			}
		})
	}
}

func TestActions_CancelAndReclaimed(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	a := seedActions(t, d)
	r := d.Actions()

	n, err := r.ReclaimedBytes(ctx)
	must(t, err)
	if n != 107 {
		t.Fatalf("ReclaimedBytes = %d, want 107", n)
	}

	cancelled, err := r.CancelPendingForGroup(ctx, 1)
	must(t, err)
	if cancelled != 2 {
		t.Fatalf("cancelled %d, want 2", cancelled)
	}
	for i, want := range map[int]models.ActionStatus{
		0: models.ActionCancelled, 5: models.ActionCancelled, // group 1 pending
		1: models.ActionSucceeded, // group 1 finished: untouched
		2: models.ActionPending,   // other group
	} {
		got, err := r.Get(ctx, a[i].ID)
		must(t, err)
		if got.Status != want {
			t.Errorf("action %d status = %q, want %q", i, got.Status, want)
		}
		if want == models.ActionCancelled && (got.FinishedAt == nil || got.Message == "") {
			t.Errorf("cancelled action %d lacks FinishedAt/Message: %+v", i, got)
		}
	}
	cancelled, err = r.CancelPendingForGroup(ctx, 1)
	must(t, err)
	if cancelled != 0 {
		t.Fatalf("second cancel = %d", cancelled)
	}
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func TestHistory(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.History()

	events := []*models.HistoryEvent{
		{EventType: models.EventScanCompleted, Title: "scan", CreatedAt: at(0)},
		{EventType: models.EventGroupDetected, GroupID: ptr(int64(1)), Title: "g1", CreatedAt: at(1),
			Data: json.RawMessage(`{"files":2}`)},
		{EventType: models.EventFileDeleted, GroupID: ptr(int64(1)), ActionID: ptr(int64(7)), Title: "g1", CreatedAt: at(2)},
		{EventType: models.EventGroupDetected, GroupID: ptr(int64(2)), Title: "g2", CreatedAt: at(3)},
		{EventType: models.EventFileDeleteDryRun, GroupID: ptr(int64(2)), Title: "g2", CreatedAt: at(4)},
	}
	for _, e := range events {
		must(t, r.Add(ctx, e))
		if e.ID == 0 {
			t.Fatal("Add did not set ID")
		}
	}
	now := &models.HistoryEvent{EventType: models.EventGroupIgnored}
	must(t, r.Add(ctx, now))
	if now.CreatedAt.IsZero() {
		t.Fatal("Add did not default CreatedAt")
	}

	ids := func(p store.Page[models.HistoryEvent]) []int64 {
		out := []int64{}
		for _, e := range p.Records {
			out = append(out, e.ID)
		}
		return out
	}
	tests := []struct {
		name  string
		types []string
		group int64
		p     store.Paging
		want  []int64
		total int
	}{
		{"all newest first", nil, 0, store.Paging{}, []int64{now.ID, events[4].ID, events[3].ID, events[2].ID, events[1].ID, events[0].ID}, 6},
		{"event types", []string{models.EventGroupDetected, models.EventFileDeleted}, 0, store.Paging{}, []int64{events[3].ID, events[2].ID, events[1].ID}, 3},
		{"group", nil, 1, store.Paging{}, []int64{events[2].ID, events[1].ID}, 2},
		{"type and group", []string{models.EventGroupDetected}, 2, store.Paging{}, []int64{events[3].ID}, 1},
		{"paged ascending", nil, 0, store.Paging{Page: 2, PageSize: 2, SortDirection: "ascending"}, []int64{events[2].ID, events[3].ID}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := r.List(ctx, tt.types, tt.group, tt.p)
			must(t, err)
			if fmt.Sprint(ids(page)) != fmt.Sprint(tt.want) || page.TotalRecords != tt.total {
				t.Fatalf("List = %v (total %d), want %v (total %d)", ids(page), page.TotalRecords, tt.want, tt.total)
			}
		})
	}

	page, err := r.List(ctx, nil, 1, store.Paging{SortDirection: "ascending"})
	must(t, err)
	e := page.Records[0]
	if *e.GroupID != 1 || e.ActionID != nil || string(e.Data) != `{"files":2}` || !e.CreatedAt.Equal(at(1)) {
		t.Fatalf("event round trip = %+v", e)
	}
	if page.Records[1].ActionID == nil || *page.Records[1].ActionID != 7 || page.Records[1].Data != nil {
		t.Fatalf("event round trip = %+v", page.Records[1])
	}

	if err := r.Add(ctx, &models.HistoryEvent{EventType: "x", Data: json.RawMessage(`{bad`)}); err == nil {
		t.Fatal("invalid data accepted")
	}
	if err := r.Add(ctx, &models.HistoryEvent{}); err == nil {
		t.Fatal("empty event type accepted")
	}

	// Boundary: events strictly older than t are removed (whole-second vs fractional times).
	n, err := r.DeleteOlderThan(ctx, at(2))
	must(t, err)
	if n != 2 {
		t.Fatalf("DeleteOlderThan removed %d, want 2", n)
	}
	page, err = r.List(ctx, nil, 0, store.Paging{})
	must(t, err)
	if page.TotalRecords != 4 {
		t.Fatalf("%d events left, want 4", page.TotalRecords)
	}
	n, err = r.DeleteOlderThan(ctx, at(2).Add(time.Nanosecond))
	must(t, err)
	if n != 1 {
		t.Fatalf("DeleteOlderThan(+1ns) removed %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Exclusions
// ---------------------------------------------------------------------------

func TestExclusions(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Exclusions()

	e := &models.Exclusion{Kind: models.ExcludeGroupKey, Value: "movie:tmdb:1", Title: "Alien", Reason: "keep both"}
	must(t, r.Create(ctx, e))
	if e.ID == 0 || e.CreatedAt.IsZero() {
		t.Fatalf("Create = %+v", e)
	}

	// Idempotent on (kind, value): returns the existing row.
	dup := &models.Exclusion{Kind: models.ExcludeGroupKey, Value: "movie:tmdb:1", Title: "other"}
	must(t, r.Create(ctx, dup))
	if dup.ID != e.ID || dup.Title != "Alien" {
		t.Fatalf("duplicate Create = %+v, want existing %+v", dup, e)
	}
	must(t, r.Create(ctx, &models.Exclusion{Kind: models.ExcludePathPrefix, Value: "movie:tmdb:1"}))

	list, err := r.List(ctx)
	must(t, err)
	if len(list) != 2 || !reflect.DeepEqual(list[0], *e) {
		t.Fatalf("List = %+v", list)
	}
	for _, bad := range []models.Exclusion{{Kind: "x"}, {Value: "y"}, {Kind: " ", Value: " "}} {
		if err := r.Create(ctx, &bad); err == nil {
			t.Errorf("Create(%+v) succeeded", bad)
		}
	}
	must(t, r.Delete(ctx, e.ID))
	wantNotFound(t, r.Delete(ctx, e.ID))
}

// TestExclusions_CreateCancelsQueue: excluding content cancels the queued removals of the groups
// the exclusion covers; exclusions the store cannot evaluate (title regex) leave it to the next
// scan.
func TestExclusions_CreateCancelsQueue(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		exclusion models.Exclusion
		cancelled []string // group keys whose removal is cancelled
	}{
		{"group key", models.Exclusion{Kind: models.ExcludeGroupKey, Value: "movie:tmdb:1"}, []string{"movie:tmdb:1"}},
		{"library", models.Exclusion{Kind: models.ExcludeLibrary, Value: "2"}, []string{"movie:tmdb:2"}},
		{"library id not a number", models.Exclusion{Kind: models.ExcludeLibrary, Value: "movies"}, nil},
		{"path prefix (case-insensitive)", models.Exclusion{Kind: models.ExcludePathPrefix, Value: "/MEDIA/movie:tmdb:3"},
			[]string{"movie:tmdb:3"}},
		{"path prefix on the local path", models.Exclusion{Kind: models.ExcludePathPrefix, Value: "/mnt/local"},
			[]string{"movie:tmdb:1"}},
		{"path prefix matching nothing", models.Exclusion{Kind: models.ExcludePathPrefix, Value: "/elsewhere"}, nil},
		{"title regex is left to the next scan", models.Exclusion{Kind: models.ExcludeRegex, Value: ".*"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			queued := map[string]*models.Action{}
			for i, lib := range []int64{1, 2, 3} {
				g := testGroup(fmt.Sprint("movie:tmdb:", i+1), lib, 1)
				if i == 0 {
					g.Files[0].Version.Parts[0].LocalPath = "/mnt/local/movie.mkv" // the keeper's local path
				}
				upsert(t, d, g)
				queued[g.Key] = queueRemoval(t, d, g, 1)
			}
			e := tt.exclusion
			must(t, d.Exclusions().Create(ctx, &e))
			for key, a := range queued {
				want := models.ActionPending
				if slices.Contains(tt.cancelled, key) {
					want = models.ActionCancelled
				}
				got, err := d.Actions().Get(ctx, a.ID)
				must(t, err)
				if got.Status != want {
					t.Errorf("%s: removal = %q, want %q", key, got.Status, want)
				}
				if want == models.ActionCancelled && got.Message != cancelExcludedMessage {
					t.Errorf("%s: message = %q", key, got.Message)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scan runs
// ---------------------------------------------------------------------------

func TestScanRuns(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.ScanRuns()

	_, err := r.Latest(ctx)
	wantNotFound(t, err)

	first := &models.ScanRun{Trigger: models.TriggerScheduled, StartedAt: at(0)}
	must(t, r.Create(ctx, first))
	if first.ID == 0 || first.Status != "running" {
		t.Fatalf("Create = %+v", first)
	}
	finished := at(5)
	first.Status, first.FinishedAt, first.Error = "failed", &finished, "boom"
	first.Stats = models.ScanStats{Libraries: 2, GroupsFound: 3, ReclaimableBytes: 1 << 40}
	must(t, r.Update(ctx, first))

	second := &models.ScanRun{Trigger: models.TriggerWebhook, Targeted: true}
	must(t, r.Create(ctx, second))

	latest, err := r.Latest(ctx)
	must(t, err)
	if latest.ID != second.ID || !latest.Targeted || latest.FinishedAt != nil {
		t.Fatalf("Latest = %+v", latest)
	}
	list, err := r.List(ctx, 0)
	must(t, err)
	if len(list) != 2 || list[0].ID != second.ID || !reflect.DeepEqual(list[1], *first) {
		t.Fatalf("List = %+v\nwant second then %+v", list, first)
	}
	list, err = r.List(ctx, 1)
	must(t, err)
	if len(list) != 1 {
		t.Fatalf("List(1) = %d runs", len(list))
	}
	wantNotFound(t, r.Update(ctx, &models.ScanRun{ID: 999}))
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func TestCommands(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Commands()

	queued := &models.Command{Name: models.CmdDuplicateScan, Trigger: models.TriggerManual,
		Body: json.RawMessage(`{"libraryIds":[1]}`), QueuedAt: at(0)}
	started := &models.Command{Name: models.CmdProcessQueue, Status: models.CommandStarted, QueuedAt: at(1), StartedAt: ptr(at(1))}
	done := &models.Command{Name: models.CmdBackup, Status: models.CommandCompleted, QueuedAt: at(2),
		StartedAt: ptr(at(2)), EndedAt: ptr(at(3)), Duration: "00:01:00.000"}
	old := &models.Command{Name: models.CmdHousekeeping, Status: models.CommandFailed, QueuedAt: at(-100), Message: "x"}
	for _, c := range []*models.Command{queued, started, done, old} {
		must(t, r.Create(ctx, c))
	}
	if queued.Status != models.CommandQueued || queued.ID == 0 {
		t.Fatalf("Create = %+v", queued)
	}
	got, err := r.Get(ctx, done.ID)
	must(t, err)
	if !reflect.DeepEqual(*got, *done) || string(got.Body) != "{}" {
		t.Fatalf("Get = %+v, want %+v with body {}", got, done)
	}

	recent, err := r.ListRecent(ctx, 2)
	must(t, err)
	if len(recent) != 2 || recent[0].ID != done.ID || recent[1].ID != started.ID {
		t.Fatalf("ListRecent(2) = %+v", recent)
	}

	done.Message = "ok"
	must(t, r.Update(ctx, done))
	got, err = r.Get(ctx, done.ID)
	must(t, err)
	if got.Message != "ok" {
		t.Fatalf("after Update: %+v", got)
	}

	must(t, r.FailRunning(ctx))
	for _, c := range []*models.Command{queued, started} {
		got, err := r.Get(ctx, c.ID)
		must(t, err)
		if got.Status != models.CommandAborted || got.EndedAt == nil || got.Message == "" {
			t.Fatalf("after FailRunning: %+v", got)
		}
	}
	if got, _ := r.Get(ctx, done.ID); got.Status != models.CommandCompleted {
		t.Fatalf("completed command changed: %+v", got)
	}
	if got, _ := r.Get(ctx, old.ID); got.Message != "x" {
		t.Fatalf("message overwritten: %+v", got)
	}

	// Retention: only finished commands older than t (by end time, else queue time).
	live := &models.Command{Name: models.CmdDuplicateScan, QueuedAt: at(-200)}
	must(t, r.Create(ctx, live))
	n, err := r.DeleteOlderThan(ctx, at(3))
	must(t, err)
	if n != 1 { // only "old" (done ended exactly at at(3); aborted ones ended now)
		t.Fatalf("DeleteOlderThan removed %d, want 1", n)
	}
	_, err = r.Get(ctx, old.ID)
	wantNotFound(t, err)
	if _, err := r.Get(ctx, live.ID); err != nil {
		t.Fatalf("queued command deleted: %v", err)
	}

	if err := r.Create(ctx, &models.Command{}); err == nil {
		t.Fatal("Create without a name succeeded")
	}
	if err := r.Create(ctx, &models.Command{Name: "x", Body: json.RawMessage(`[`)}); err == nil {
		t.Fatal("Create with invalid body succeeded")
	}
	_, err = r.Get(ctx, 999)
	wantNotFound(t, err)
	wantNotFound(t, r.Update(ctx, &models.Command{ID: 999, Name: "x"}))
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

func TestTasks(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	r := d.Tasks()

	_, err := r.Get(ctx, models.CmdDuplicateScan)
	wantNotFound(t, err)

	scan := &models.ScheduledTask{Name: "Duplicate Scan", TaskName: models.CmdDuplicateScan, IntervalMinutes: 360}
	backup := &models.ScheduledTask{Name: "Backup", TaskName: models.CmdBackup, IntervalMinutes: 10080,
		LastExecution: ptr(at(0)), LastStartTime: ptr(at(-1)), LastDuration: "00:00:01", NextExecution: ptr(at(10080))}
	must(t, r.Upsert(ctx, scan))
	must(t, r.Upsert(ctx, backup))

	got, err := r.Get(ctx, models.CmdBackup)
	must(t, err)
	if !reflect.DeepEqual(*got, *backup) {
		t.Fatalf("Get = %+v, want %+v", got, backup)
	}

	scan.LastExecution, scan.IntervalMinutes = ptr(at(5)), 0
	must(t, r.Upsert(ctx, scan))
	got, err = r.Get(ctx, models.CmdDuplicateScan)
	must(t, err)
	if !reflect.DeepEqual(*got, *scan) {
		t.Fatalf("after second Upsert = %+v, want %+v", got, scan)
	}

	list, err := r.List(ctx)
	must(t, err)
	if len(list) != 2 || list[0].TaskName != models.CmdBackup || list[1].TaskName != models.CmdDuplicateScan {
		t.Fatalf("List = %+v", list)
	}
	if err := r.Upsert(ctx, &models.ScheduledTask{Name: "x"}); err == nil {
		t.Fatal("Upsert without TaskName succeeded")
	}
}
