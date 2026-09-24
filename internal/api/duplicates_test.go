package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestDuplicateList(t *testing.T) {
	ts := newTestServer(t)
	a := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupReview)
	ts.seedGroup("movie:tmdb:333", "Charlie", models.GroupIgnored)

	var page store.Page[duplicateSummary]
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate", nil), http.StatusOK, &page)
	if page.TotalRecords != 3 || len(page.Records) != 3 || page.Page != 1 || page.PageSize != 20 {
		t.Fatalf("page = %+v", page)
	}
	var alpha duplicateSummary
	for _, r := range page.Records {
		if r.ID == a.ID {
			alpha = r
		}
	}
	if alpha.FileCount != 2 || alpha.KeepCount != 1 || alpha.RemoveCount != 1 || alpha.BestResolution != models.Res2160 ||
		len(alpha.Files) != 2 || alpha.Files[0].ArrInstanceName != "Radarr 4K" || alpha.Files[0].Size != 40<<30 ||
		alpha.Title != "Alpha" || alpha.Status != models.GroupPending || len(alpha.LibraryIDs) != 1 {
		t.Fatalf("summary = %+v", alpha)
	}

	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?status=pending,review", nil), http.StatusOK, &page)
	if page.TotalRecords != 2 {
		t.Fatalf("status filter: %d", page.TotalRecords)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?status=pending&status=ignored", nil), http.StatusOK, &page)
	if page.TotalRecords != 2 {
		t.Fatalf("repeated status keys: %d", page.TotalRecords)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?search=brav", nil), http.StatusOK, &page)
	if page.TotalRecords != 1 || page.Records[0].Title != "Bravo" {
		t.Fatalf("search: %+v", page)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?pageSize=1&page=2&sortKey=title&sortDirection=ascending", nil), http.StatusOK, &page)
	if len(page.Records) != 1 || page.Records[0].Title != "Bravo" || page.SortKey != "title" || page.SortDirection != "ascending" {
		t.Fatalf("paging: %+v", page)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate?pageSize=5000", nil), http.StatusOK, &page)
	if page.PageSize != maxPageSize {
		t.Fatalf("pageSize not clamped: %d", page.PageSize)
	}
	for _, q := range []string{"status=bogus", "mediaType=song", "libraryId=-1", "page=first", "search=" + strings.Repeat("x", 201)} {
		if rr := ts.do(http.MethodGet, "/api/v1/duplicate?"+q, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rr.Code)
		}
	}
}

func TestDuplicateStatsAndDetail(t *testing.T) {
	ts := newTestServer(t)
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	var st struct {
		Total            int                        `json:"total"`
		ByStatus         map[models.GroupStatus]int `json:"byStatus"`
		ReclaimableBytes int64                      `json:"reclaimableBytes"`
		LastScan         *models.ScanRun            `json:"lastScan"`
	}
	rr := ts.do(http.MethodGet, "/api/v1/duplicate/stats", nil)
	expect(t, rr, http.StatusOK, &st)
	if st.Total != 1 || st.ByStatus[models.GroupPending] != 1 || len(st.ByStatus) != 8 || st.LastScan != nil ||
		!strings.Contains(rr.Body.String(), `"lastScan":null`) {
		t.Fatalf("stats = %+v (%s)", st, rr.Body.String())
	}
	run := &models.ScanRun{Trigger: models.TriggerManual, Status: "completed"}
	if err := ts.db.ScanRuns().Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/stats", nil), http.StatusOK, &st)
	if st.LastScan == nil || st.LastScan.ID != run.ID {
		t.Fatalf("lastScan = %+v", st.LastScan)
	}

	var detail duplicateDetail
	rr = ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g.ID), nil)
	expect(t, rr, http.StatusOK, &detail)
	if detail.ID != g.ID || len(detail.Files) != 2 || detail.Actions == nil || !strings.Contains(rr.Body.String(), `"actions":[]`) {
		t.Fatalf("detail = %+v", detail)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/999", nil), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/abc", nil), http.StatusBadRequest, nil)
	var scans []models.ScanRun
	expect(t, ts.do(http.MethodGet, "/api/v1/scan", nil), http.StatusOK, &scans)
	if len(scans) != 1 {
		t.Fatalf("scans = %+v", scans)
	}
}

func TestDuplicateApprove(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	id := itoa64(g.ID)

	var actions []models.Action
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/approve", nil), http.StatusOK, &actions)
	if len(actions) != 1 || actions[0].Status != models.ActionPending || actions[0].GroupFileID != g.Files[1].ID {
		t.Fatalf("actions = %+v", actions)
	}
	got, _ := ts.db.Groups().Get(ctx, g.ID)
	if got.Status != models.GroupQueued {
		t.Fatalf("status = %s", got.Status)
	}
	cmds, _ := ts.cmds.Recent(ctx, 5)
	if len(cmds) == 0 || cmds[0].Name != models.CmdProcessQueue {
		t.Fatalf("ProcessQueue not queued: %+v", cmds)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/approve", nil), http.StatusConflict); !strings.Contains(msg, "queued") {
		t.Fatalf("second approve: %q", msg)
	}

	// Invariants: a group that would keep nothing is refused before the executor runs.
	bad := ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending)
	if err := ts.db.Groups().SetOverride(ctx, bad.ID, bad.Files[0].ID, models.DecisionRemove); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.scanner.Reevaluate(ctx, bad.ID); err != nil {
		t.Fatal(err)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(bad.ID)+"/approve", nil), http.StatusBadRequest); !strings.Contains(msg, "Cannot approve") {
		t.Fatalf("invariant message = %q", msg)
	}

	ignored := ts.seedGroup("movie:tmdb:333", "Charlie", models.GroupIgnored)
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(ignored.ID)+"/approve", nil), http.StatusConflict, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/999/approve", nil), http.StatusNotFound, nil)

	other := ts.seedGroup("movie:tmdb:4444", "Delta", models.GroupPending)
	ts.executor.approveErr = errNothing
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(other.ID)+"/approve", nil), http.StatusBadRequest); !strings.Contains(msg, "Nothing to remove") {
		t.Fatalf("nothing message = %q", msg)
	}
	ts.executor.approveErr = errors.New("database is locked")
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(other.ID)+"/approve", nil), http.StatusInternalServerError); strings.Contains(msg, "locked") {
		t.Fatalf("internal error leaked: %q", msg)
	}

	noExec := newTestServer(t, func(o *serverOpts) { o.noExecutor = true })
	g2 := noExec.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	expect(t, noExec.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g2.ID)+"/approve", nil), http.StatusServiceUnavailable, nil)
}

func TestDuplicateIgnoreUnignore(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	id := itoa64(g.ID)
	ch, unsub := ts.bus.Subscribe(16)
	defer unsub()

	var got models.DuplicateGroup
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/ignore", map[string]bool{"addExclusion": true}), http.StatusOK, &got)
	if got.Status != models.GroupIgnored {
		t.Fatalf("status = %s", got.Status)
	}
	exclusions, _ := ts.db.Exclusions().List(ctx)
	if len(exclusions) != 1 || exclusions[0].Kind != models.ExcludeGroupKey || exclusions[0].Value != g.Key {
		t.Fatalf("exclusions = %+v", exclusions)
	}
	seen := map[string]bool{}
	for len(ch) > 0 {
		ev := <-ch
		seen[ev.Name] = true
	}
	if !seen[events.NameDuplicate] || !seen[events.NameHistory] {
		t.Fatalf("events = %v", seen)
	}
	// Ignoring twice is idempotent (no duplicate exclusion).
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/ignore", map[string]bool{"addExclusion": true}), http.StatusOK, &got)
	// No body is fine too.
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/ignore", nil), http.StatusOK, &got)

	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/unignore", nil), http.StatusOK, &got)
	if got.Status != models.GroupPending {
		t.Fatalf("unignored status = %s", got.Status)
	}
	if exclusions, _ = ts.db.Exclusions().List(ctx); len(exclusions) != 0 {
		t.Fatalf("exclusion kept: %+v", exclusions)
	}
	if _, one := ts.scanner.counts(); len(one) == 0 || one[len(one)-1] != g.ID {
		t.Fatalf("not re-evaluated: %v", one)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/unignore", nil), http.StatusConflict, nil)

	var hist store.Page[models.HistoryEvent]
	expect(t, ts.do(http.MethodGet, "/api/v1/history?groupId="+id+"&eventType=groupIgnored,groupUnignored", nil), http.StatusOK, &hist)
	if hist.TotalRecords != 2 {
		t.Fatalf("history = %+v", hist)
	}

	// A failed re-evaluation leaves the group in review, never pending with stale decisions.
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/ignore", nil), http.StatusOK, &got)
	ts.scanner.reevalErr = errors.New("plex down")
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/unignore", nil), http.StatusInternalServerError, nil)
	after, _ := ts.db.Groups().Get(ctx, g.ID)
	if after.Status != models.GroupReview {
		t.Fatalf("status after failed re-evaluation = %s", after.Status)
	}
}

func TestDuplicateOverride(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	keeper, loser := g.Files[0], g.Files[1]
	path := func(fileID int64) string {
		return "/api/v1/duplicate/" + itoa64(g.ID) + "/file/" + itoa64(fileID) + "/override"
	}
	decisionOf := func(gr *models.DuplicateGroup, fileID int64) (models.Decision, models.Decision) {
		for _, f := range gr.Files {
			if f.ID == fileID {
				return f.Decision, f.Override
			}
		}
		t.Fatalf("file %d missing", fileID)
		return "", ""
	}

	var got models.DuplicateGroup
	expect(t, ts.do(http.MethodPut, path(loser.ID), map[string]any{"decision": "keep"}), http.StatusOK, &got)
	if d, o := decisionOf(&got, loser.ID); d != models.DecisionKeep || o != models.DecisionKeep {
		t.Fatalf("keep override: %s/%s", d, o)
	}
	// Removing the only other keeper would leave nothing: refused, nothing stored.
	expect(t, ts.do(http.MethodPut, path(loser.ID), map[string]any{"decision": nil}), http.StatusOK, &got)
	msg := message(t, ts.do(http.MethodPut, path(keeper.ID), map[string]any{"decision": "remove"}), http.StatusBadRequest)
	if !strings.Contains(msg, "not allowed") {
		t.Fatalf("message = %q", msg)
	}
	stored, _ := ts.db.Groups().Get(ctx, g.ID)
	if _, o := decisionOf(stored, keeper.ID); o != "" {
		t.Fatalf("rejected override stored: %q", o)
	}
	// Swapping: keep the loser, then remove the former keeper — allowed.
	expect(t, ts.do(http.MethodPut, path(loser.ID), map[string]any{"decision": "keep"}), http.StatusOK, &got)
	expect(t, ts.do(http.MethodPut, path(keeper.ID), map[string]any{"decision": "REMOVE"}), http.StatusOK, &got)
	if d, _ := decisionOf(&got, keeper.ID); d != models.DecisionRemove {
		t.Fatalf("swap: keeper decision %s", d)
	}
	var hist store.Page[models.HistoryEvent]
	expect(t, ts.do(http.MethodGet, "/api/v1/history?eventType=overrideChanged", nil), http.StatusOK, &hist)
	if hist.TotalRecords != 4 {
		t.Fatalf("history = %d", hist.TotalRecords)
	}

	if props := validationProps(t, ts.do(http.MethodPut, path(keeper.ID), map[string]any{"decision": "delete"})); !hasProp(props, "decision") {
		t.Fatalf("props = %v", props)
	}
	expect(t, ts.do(http.MethodPut, path(99999), map[string]any{"decision": "keep"}), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodPut, "/api/v1/duplicate/999/file/1/override", map[string]any{"decision": "keep"}), http.StatusNotFound, nil)

	// A failed re-evaluation reverts a removal override.
	expect(t, ts.do(http.MethodPut, path(keeper.ID), map[string]any{"decision": nil}), http.StatusOK, &got)
	ts.scanner.reevalErr = errors.New("boom")
	expect(t, ts.do(http.MethodPut, path(loser.ID), map[string]any{"decision": "remove"}), http.StatusInternalServerError, nil)
	stored, _ = ts.db.Groups().Get(ctx, g.ID)
	if _, o := decisionOf(stored, loser.ID); o != models.DecisionKeep {
		t.Fatalf("override not reverted: %q", o)
	}
}

func TestDuplicateRescanAndBulk(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	a := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	b := ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending)

	var cmd models.Command
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(a.ID)+"/rescan", nil), http.StatusCreated, &cmd)
	if cmd.Name != models.CmdTargetedScan || !strings.Contains(string(cmd.Body), `"ratingKeys":["501"]`) || !strings.Contains(string(cmd.Body), `"serverId":1`) {
		t.Fatalf("rescan command = %+v %s", cmd, cmd.Body)
	}

	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{a.ID, b.ID, a.ID, 999}, "action": "ignore"}), http.StatusOK, &res)
	if len(res.Succeeded) != 2 || len(res.Failed) != 1 || res.Failed[0].ID != 999 || !strings.Contains(res.Failed[0].Message, "not found") {
		t.Fatalf("bulk ignore = %+v", res)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{a.ID, b.ID}, "action": "unignore"}), http.StatusOK, &res)
	if len(res.Succeeded) != 2 {
		t.Fatalf("bulk unignore = %+v", res)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{a.ID, b.ID}, "action": "approve"}), http.StatusOK, &res)
	if len(res.Succeeded) != 2 || len(res.Failed) != 0 {
		t.Fatalf("bulk approve = %+v", res)
	}
	cmds, _ := ts.cmds.Recent(ctx, 10)
	processQueue := 0
	for _, c := range cmds {
		if c.Name == models.CmdProcessQueue {
			processQueue++
		}
	}
	if processQueue != 1 {
		t.Fatalf("ProcessQueue queued %d times", processQueue)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{a.ID}, "action": "approve"}), http.StatusOK, &res)
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "queued") {
		t.Fatalf("re-approve = %+v", res)
	}
	for _, body := range []map[string]any{
		{"ids": []int64{a.ID}, "action": "delete"},
		{"ids": []int64{}, "action": "ignore"},
		{"action": "ignore"},
	} {
		expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", body), http.StatusBadRequest, nil)
	}
}
