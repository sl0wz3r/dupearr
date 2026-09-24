package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// GET /api/v1/duplicate/stats is a stable contract (docs/API.md, "Duplicate statistics"): dashboards
// such as Homepage's customapi widget read it by field name. Fields may be added, never renamed,
// removed or given another JSON type. This test pins every documented field and its type; it
// deliberately allows extra fields.
func TestDuplicateStatsStableContract(t *testing.T) {
	ts := newTestServer(t)
	ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)

	get := func() map[string]any {
		t.Helper()
		rr := ts.do(http.MethodGet, "/api/v1/duplicate/stats", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET stats = %d: %s", rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("stats is not a JSON object: %v", err)
		}
		return body
	}
	number := func(where string, v any) {
		t.Helper()
		if _, ok := v.(float64); !ok {
			t.Errorf("%s = %#v, want a number", where, v)
		}
	}
	object := func(where string, v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s = %#v, want an object", where, v)
		}
		return m
	}

	body := get()
	number("total", body["total"])
	number("reclaimableBytes", body["reclaimableBytes"])
	number("reclaimedBytes", body["reclaimedBytes"])
	byStatus := object("byStatus", body["byStatus"])
	for _, s := range []string{"pending", "review", "deferred", "protected", "queued", "resolved", "ignored", "failed"} {
		number("byStatus."+s, byStatus[s]) // every status is listed, 0 included
	}
	if v, ok := body["lastScan"]; !ok || v != nil {
		t.Errorf("lastScan before the first scan = %#v (present %v), want null", v, ok)
	}

	started := time.Now().Add(-time.Minute).UTC()
	finished := started.Add(30 * time.Second)
	run := &models.ScanRun{Trigger: models.TriggerManual, Status: "completed", StartedAt: started}
	if err := ts.db.ScanRuns().Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.FinishedAt = &finished
	if err := ts.db.ScanRuns().Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	scan := object("lastScan", get()["lastScan"])
	number("lastScan.id", scan["id"])
	for _, k := range []string{"trigger", "status", "startedAt", "finishedAt"} {
		if _, ok := scan[k].(string); !ok {
			t.Errorf("lastScan.%s = %#v, want a string", k, scan[k])
		}
	}
	for _, k := range []string{"startedAt", "finishedAt"} {
		if s, _ := scan[k].(string); s != "" {
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				t.Errorf("lastScan.%s = %q, want an RFC 3339 time: %v", k, s, err)
			}
		}
	}
	if _, ok := scan["targeted"].(bool); !ok {
		t.Errorf("lastScan.targeted = %#v, want a boolean", scan["targeted"])
	}
	stats := object("lastScan.stats", scan["stats"])
	for _, k := range []string{"libraries", "itemsExamined", "groupsFound", "newGroups", "resolvedGroups",
		"pendingGroups", "reviewGroups", "reclaimableBytes", "autoApproved", "errors"} {
		number("lastScan.stats."+k, stats[k])
	}
}
