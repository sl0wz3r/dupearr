package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestQueueAndActions(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	var actions []models.Action
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusOK, &actions)

	var page store.Page[models.Action]
	expect(t, ts.do(http.MethodGet, "/api/v1/queue", nil), http.StatusOK, &page)
	if page.TotalRecords != 1 || page.Records[0].ID != actions[0].ID || page.SortKey != "createdAt" || page.SortDirection != "ascending" {
		t.Fatalf("queue = %+v", page)
	}
	id := itoa64(actions[0].ID)
	expect(t, ts.do(http.MethodDelete, "/api/v1/queue/"+id, nil), http.StatusOK, nil)
	a, _ := ts.db.Actions().Get(ctx, actions[0].ID)
	if a.Status != models.ActionCancelled || a.FinishedAt == nil {
		t.Fatalf("action = %+v", a)
	}
	if got, _ := ts.db.Groups().Get(ctx, g.ID); got.Status != models.GroupPending {
		t.Fatalf("group not reopened: %s", got.Status)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/queue", nil), http.StatusOK, &page)
	if page.TotalRecords != 0 || page.Records == nil {
		t.Fatalf("queue after cancel = %+v", page)
	}
	expect(t, ts.do(http.MethodDelete, "/api/v1/queue/"+id, nil), http.StatusConflict, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/queue/999", nil), http.StatusNotFound, nil)

	expect(t, ts.do(http.MethodGet, "/api/v1/action?status=cancelled,succeeded", nil), http.StatusOK, &page)
	if page.TotalRecords != 1 {
		t.Fatalf("actions = %+v", page)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/action?status=pending", nil), http.StatusOK, &page)
	if page.TotalRecords != 0 {
		t.Fatalf("pending actions = %+v", page)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/action?status=exploded", nil), http.StatusBadRequest, nil)
}

// TestQueueCancelRunning: a removal that already started is never marked cancelled (409), and
// the queue entry keeps its status.
func TestQueueCancelRunning(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	var actions []models.Action
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusOK, &actions)
	a := actions[0]
	a.Status = models.ActionRunning
	if err := ts.db.Actions().Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if msg := message(t, ts.do(http.MethodDelete, "/api/v1/queue/"+itoa64(a.ID), nil), http.StatusConflict); !strings.Contains(msg, "already running") {
		t.Fatalf("message = %q", msg)
	}
	got, err := ts.db.Actions().Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ActionRunning || got.FinishedAt != nil {
		t.Fatalf("running action changed: %+v", got)
	}
	if grp, _ := ts.db.Groups().Get(ctx, g.ID); grp.Status != models.GroupQueued {
		t.Fatalf("group = %q", grp.Status)
	}
	// A finished action is refused with its status.
	a.Status = models.ActionFailed
	if err := ts.db.Actions().Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if msg := message(t, ts.do(http.MethodDelete, "/api/v1/queue/"+itoa64(a.ID), nil), http.StatusConflict); !strings.Contains(msg, "failed") {
		t.Fatalf("message = %q", msg)
	}
}

func TestActionRestore(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	notRecyclable := models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID, VersionKey: g.Files[1].Version.Key, Title: g.Title,
		Status: models.ActionSucceeded, Method: models.MethodArr, Permanent: true}
	if err := ts.db.Actions().Create(ctx, &notRecyclable); err != nil {
		t.Fatal(err)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/action/"+itoa64(notRecyclable.ID)+"/restore", nil), http.StatusConflict, nil)

	recycled := models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID, VersionKey: g.Files[1].Version.Key, Title: g.Title,
		Status: models.ActionSucceeded, Method: models.MethodFilesystem, RecyclePath: "/recycle/m.mkv"}
	if err := ts.db.Actions().Create(ctx, &recycled); err != nil {
		t.Fatal(err)
	}
	var got models.Action
	expect(t, ts.do(http.MethodPost, "/api/v1/action/"+itoa64(recycled.ID)+"/restore", nil), http.StatusOK, &got)
	if got.ID != recycled.ID || len(ts.executor.restored) != 1 {
		t.Fatalf("restore: %+v %v", got, ts.executor.restored)
	}
	ts.executor.restoreErr = errors.New("target already exists")
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/action/"+itoa64(recycled.ID)+"/restore", nil), http.StatusConflict); !strings.Contains(msg, "already exists") {
		t.Fatalf("message = %q", msg)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/action/999/restore", nil), http.StatusNotFound, nil)
}

func TestHistoryQuery(t *testing.T) {
	ts := newTestServer(t)
	for _, q := range []string{"groupId=-1", "groupId=x", "page=zero"} {
		if rr := ts.do(http.MethodGet, "/api/v1/history?"+q, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rr.Code)
		}
	}
	var page store.Page[models.HistoryEvent]
	rr := ts.do(http.MethodGet, "/api/v1/history", nil)
	expect(t, rr, http.StatusOK, &page)
	if page.SortKey != "createdAt" || page.SortDirection != "descending" || !strings.Contains(rr.Body.String(), `"records":[]`) {
		t.Fatalf("empty history = %s", rr.Body.String())
	}
}

func TestExclusions(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	lib := ts.seedLibrary(srv.ID, "1", "Movies")

	var e models.Exclusion
	expect(t, ts.do(http.MethodPost, "/api/v1/exclusion", map[string]any{"kind": "group_key", "value": " movie:tmdb:1 ", "title": "Alpha"}), http.StatusCreated, &e)
	if e.ID == 0 || e.Value != "movie:tmdb:1" || e.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", e)
	}
	// Creating the same exclusion again is idempotent.
	var again models.Exclusion
	expect(t, ts.do(http.MethodPost, "/api/v1/exclusion", map[string]any{"kind": "group_key", "value": "movie:tmdb:1"}), http.StatusCreated, &again)
	if again.ID != e.ID {
		t.Fatalf("duplicate exclusion created: %d vs %d", again.ID, e.ID)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/exclusion", map[string]any{"kind": "library", "value": itoa64(lib.ID)}), http.StatusCreated, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/exclusion", map[string]any{"kind": "title_regex", "value": "(?i)^sample"}), http.StatusCreated, nil)
	for _, tt := range []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"kind": "title_regex", "value": "("}, "value"},
		{map[string]any{"kind": "library", "value": "abc"}, "value"},
		{map[string]any{"kind": "library", "value": "999"}, "value"},
		{map[string]any{"kind": "tag", "value": "x"}, "kind"},
		{map[string]any{"kind": "path_prefix", "value": " "}, "value"},
	} {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/exclusion", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}
	var list []models.Exclusion
	expect(t, ts.do(http.MethodGet, "/api/v1/exclusion", nil), http.StatusOK, &list)
	if len(list) != 3 {
		t.Fatalf("list = %+v", list)
	}
	expect(t, ts.do(http.MethodDelete, "/api/v1/exclusion/"+itoa64(e.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/exclusion/"+itoa64(e.ID), nil), http.StatusNotFound, nil)
}
