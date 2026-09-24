package api

// Regression tests for the adversarial review of internal/api (stale approvals, re-pointed
// connections, error mapping, aliasing of stored slices, shutdown of background work).

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// setSignature stores a decision signature on a seeded group.
func (ts *testServer) setSignature(g *models.DuplicateGroup, sig string) *models.DuplicateGroup {
	ts.t.Helper()
	g.Signature = sig
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		ts.t.Fatalf("upsert: %v", err)
	}
	got, err := ts.db.Groups().Get(context.Background(), g.ID)
	if err != nil {
		ts.t.Fatal(err)
	}
	return got
}

func TestApproveRefusesStaleSignature(t *testing.T) {
	ts := newTestServer(t)
	g := ts.setSignature(ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending), "sig-current")
	id := itoa64(g.ID)

	// The list exposes the signature so a client can send back what the user reviewed.
	var page store.Page[duplicateSummary]
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate", nil), http.StatusOK, &page)
	if len(page.Records) != 1 || page.Records[0].Signature != "sig-current" {
		t.Fatalf("summary signature = %+v", page.Records)
	}

	msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/approve", map[string]string{"signature": "sig-old"}), http.StatusConflict)
	if !strings.Contains(msg, "changed since it was displayed") {
		t.Fatalf("stale approve message = %q", msg)
	}
	if acts, _ := ts.db.Actions().ListByGroup(context.Background(), g.ID); len(acts) != 0 {
		t.Fatalf("a stale approval queued removals: %+v", acts)
	}
	if rr := ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/approve", "{bad json"); rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", rr.Code)
	}
	var actions []models.Action
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+id+"/approve", map[string]string{"signature": "sig-current"}), http.StatusOK, &actions)
	if len(actions) != 1 {
		t.Fatalf("actions = %+v", actions)
	}

	// Bulk: per-group signatures; a group without one is approved as before.
	a := ts.setSignature(ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending), "sig-a")
	b := ts.setSignature(ts.seedGroup("movie:tmdb:333", "Charlie", models.GroupPending), "sig-b")
	c := ts.seedGroup("movie:tmdb:4444", "Delta", models.GroupPending)
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{
		"ids": []int64{a.ID, b.ID, c.ID}, "action": "approve",
		"signatures": map[string]string{itoa64(a.ID): "sig-a", itoa64(b.ID): "stale"},
	}), http.StatusOK, &res)
	if !slices.Equal(res.Succeeded, []int64{a.ID, c.ID}) || len(res.Failed) != 1 || res.Failed[0].ID != b.ID ||
		!strings.Contains(res.Failed[0].Message, "changed since it was displayed") {
		t.Fatalf("bulk = %+v", res)
	}
}

func TestApproveNotApprovableIsConflict(t *testing.T) {
	ts := newTestServer(t)
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	// E.g. a scan resolved the group between the API's status check and the executor's.
	ts.executor.approveErr = fmt.Errorf("approve group %d (Alpha (2020)): %w: it is resolved", g.ID, executor.ErrNotApprovable)
	msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusConflict)
	if msg != "This duplicate cannot be approved: it is resolved" {
		t.Fatalf("message = %q", msg)
	}
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{g.ID}, "action": "approve"}), http.StatusOK, &res)
	if len(res.Failed) != 1 || res.Failed[0].Message != "This duplicate cannot be approved: it is resolved" {
		t.Fatalf("bulk = %+v", res)
	}
}

func TestErrorResponseMapping(t *testing.T) {
	ts := newTestServer(t)
	r := httptestRequest(t)
	tests := []struct {
		err    error
		status int
		msg    string
	}{
		{fmt.Errorf("x: %w", store.ErrNotFound), http.StatusNotFound, "Not found"},
		{fmt.Errorf("approve: %w: it is ignored", executor.ErrNotApprovable), http.StatusConflict, "This duplicate cannot be approved: it is ignored"},
		{executor.ErrNotApprovable, http.StatusConflict, "This duplicate cannot be approved"},
		{errConflict("busy"), http.StatusConflict, "busy"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "The operation timed out"},
		{fmt.Errorf("secret internal detail /config/x.db"), http.StatusInternalServerError, "Internal server error"},
	}
	for _, tt := range tests {
		status, msg, verrs := ts.srv.errorResponse(r, tt.err)
		if status != tt.status || msg != tt.msg || verrs != nil {
			t.Errorf("errorResponse(%v) = %d %q %v, want %d %q", tt.err, status, msg, verrs, tt.status, tt.msg)
		}
		if got := ts.srv.clientMessage(r, tt.err); got != tt.msg {
			t.Errorf("clientMessage(%v) = %q, want %q", tt.err, got, tt.msg)
		}
	}
	if _, _, verrs := ts.srv.errorResponse(r, errValidation(invalid("name", "bad"))); len(verrs) != 1 {
		t.Errorf("validation errors = %v", verrs)
	}
}

func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "/api/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNormalizedEndpoint(t *testing.T) {
	same := [][2]string{
		{"http://radarr:7878", "http://RADARR:7878/"},
		{"http://radarr", "http://radarr:80"},
		{"https://plex.example", "HTTPS://plex.example:443"},
		{"http://host/radarr", "http://host/radarr/"},
	}
	for _, p := range same {
		if endpointChanged(p[0], p[1]) {
			t.Errorf("endpointChanged(%q, %q) = true", p[0], p[1])
		}
	}
	different := [][2]string{
		{"http://radarr:7878", "http://radarr4k:7878"},
		{"http://radarr:7878", "http://radarr:7879"},
		{"http://host/radarr", "http://host/radarr4k"},
		{"http://radarr:7878", "https://radarr:7878"},
	}
	for _, p := range different {
		if !endpointChanged(p[0], p[1]) {
			t.Errorf("endpointChanged(%q, %q) = false", p[0], p[1])
		}
	}
}

// TestRepointedArrInstanceGuard: once an *arr instance's URL changes, its stored file ids may name
// unrelated files on the new instance, so queued removals are cancelled and approvals wait for a
// scan that started after the change.
func TestRepointedArrInstanceGuard(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	inst := models.ArrInstance{Name: "Radarr 4K", Kind: models.ArrRadarr, URL: ts.arr.srv.URL, APIKey: fakeArrKey, Enabled: true}
	if err := ts.db.ArrInstances().Create(ctx, &inst); err != nil {
		t.Fatal(err)
	}
	if inst.ID != 1 { // seedGroup's keeper is tracked by instance 1
		t.Fatalf("instance id = %d", inst.ID)
	}
	iid := itoa64(inst.ID)
	pending := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	queued := ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending)
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(queued.ID)+"/approve", nil), http.StatusOK, nil)

	// A rename (same URL) changes nothing.
	body := map[string]any{"name": "Radarr UHD", "kind": "radarr", "url": ts.arr.srv.URL + "/", "apiKey": maskedSecret}
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+iid, body), http.StatusAccepted, nil)
	if g, _ := ts.db.Groups().Get(ctx, queued.ID); g.Status != models.GroupQueued {
		t.Fatalf("rename quarantined the group: %s", g.Status)
	}

	// Re-point the instance (forceSave: the new address is offline; the key must be re-entered).
	before := time.Now()
	body = map[string]any{"name": "Radarr UHD", "kind": "radarr", "url": "http://127.0.0.1:1", "apiKey": fakeArrKey}
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+iid+"?forceSave=true", body), http.StatusAccepted, nil)

	g, _ := ts.db.Groups().Get(ctx, queued.ID)
	if g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "re-scan") {
		t.Fatalf("queued group after re-point = %s %q", g.Status, g.StatusReason)
	}
	acts, _ := ts.db.Actions().ListByGroup(ctx, queued.ID)
	for _, a := range acts {
		if a.Status != models.ActionCancelled {
			t.Fatalf("queued removal survived the re-point: %+v", a)
		}
	}
	msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(pending.ID)+"/approve", nil), http.StatusConflict)
	if !strings.Contains(msg, "Radarr UHD") || !strings.Contains(msg, "re-scan") {
		t.Fatalf("approve after re-point = %q", msg)
	}

	// A scan that was already running when the URL changed still read the old instance.
	early := models.ScanRun{Trigger: models.TriggerManual, Status: "completed", StartedAt: before.Add(-time.Minute)}
	if err := ts.db.ScanRuns().Create(ctx, &early); err != nil {
		t.Fatal(err)
	}
	cur, _ := ts.db.Groups().Get(ctx, pending.ID)
	cur.LastScanID, cur.LastSeenAt = early.ID, time.Now().Add(time.Second)
	if _, err := ts.db.Groups().Upsert(ctx, cur); err != nil {
		t.Fatal(err)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(pending.ID)+"/approve", nil), http.StatusConflict, nil)

	// A scan that started after the change refreshed the ids: approvals work again.
	fresh := models.ScanRun{Trigger: models.TriggerManual, Status: "completed", StartedAt: time.Now().Add(time.Second)}
	if err := ts.db.ScanRuns().Create(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	cur, _ = ts.db.Groups().Get(ctx, pending.ID)
	cur.LastScanID, cur.LastSeenAt = fresh.ID, time.Now().Add(2*time.Second)
	if _, err := ts.db.Groups().Upsert(ctx, cur); err != nil {
		t.Fatal(err)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(pending.ID)+"/approve", nil), http.StatusOK, nil)
}

func TestRepointedMediaServerGuard(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	srv := ts.seedServer("Plex") // id 1, like seedGroup's versions
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	body := map[string]any{"name": "Plex", "url": "http://127.0.0.1:1", "token": fakePlexToken}
	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(srv.ID)+"?forceSave=true", body), http.StatusAccepted, nil)
	if v, ok, _ := ts.db.Settings().GetValue(ctx, endpointKey(endpointKindServer, srv.ID)); !ok || v == "" {
		t.Fatal("no endpoint-change marker recorded")
	}
	msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusConflict)
	if !strings.Contains(msg, `Media server "Plex"`) {
		t.Fatalf("message = %q", msg)
	}
}

// racingGroups runs onList once, right after the first List — like a queue run that resolves a
// group between the re-point sweep's listing and its status update.
type racingGroups struct {
	store.GroupRepo
	once   sync.Once
	onList func()
}

func (r *racingGroups) List(ctx context.Context, f store.GroupFilter, p store.Paging) (store.Page[models.DuplicateGroup], error) {
	res, err := r.GroupRepo.List(ctx, f, p)
	r.once.Do(r.onList)
	return res, err
}

type racingStore struct {
	store.Store
	groups *racingGroups
}

func (s racingStore) Groups() store.GroupRepo { return s.groups }

// Regression (seen in the end-to-end suite): the URL-change sweep listed queued groups and then
// set them to review unconditionally, so a group a running queue run had resolved in between came
// back as "review … queued removals were cancelled" although its removals were done.
func TestRepointDoesNotReopenAGroupResolvedMeanwhile(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	srv := ts.seedServer("Plex") // id 1, like seedGroup's versions
	done := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupQueued)
	queued := ts.seedGroup("movie:tmdb:2", "Beta", models.GroupQueued)
	ts.srv.d.Store = racingStore{Store: ts.db, groups: &racingGroups{GroupRepo: ts.db.Groups(), onList: func() {
		if err := ts.db.Groups().UpdateStatus(ctx, done.ID, models.GroupResolved, "Removed 1 duplicate file(s)"); err != nil {
			t.Error(err)
		}
	}}}
	if err := ts.srv.endpointRepointed(ctx, endpointKindServer, srv.ID, "Plex"); err != nil {
		t.Fatal(err)
	}
	if g, err := ts.db.Groups().Get(ctx, done.ID); err != nil || g.Status != models.GroupResolved {
		t.Fatalf("group resolved during the sweep: %+v, %v; want it to stay resolved", g, err)
	}
	if g, err := ts.db.Groups().Get(ctx, queued.ID); err != nil || g.Status != models.GroupReview {
		t.Fatalf("queued group: %+v, %v; want review", g, err)
	}
}

func TestLibraryUpdateNeverChangesLocations(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	lib := ts.seedLibrary(srv.ID, "1", "Movies")
	var updated models.Library
	expect(t, ts.do(http.MethodPut, "/api/v1/library/"+itoa64(lib.ID), map[string]any{
		"enabled": true, "locations": []string{"/etc"}, "title": "Other",
	}), http.StatusAccepted, &updated)
	stored, _ := ts.db.Libraries().Get(context.Background(), lib.ID)
	if !slices.Equal(stored.Locations, []string{"/data/movies"}) || !slices.Equal(updated.Locations, []string{"/data/movies"}) || stored.Title != "Movies" {
		t.Fatalf("locations changed: stored %v, response %v, title %q", stored.Locations, updated.Locations, stored.Title)
	}
}

func TestArrUpdateKeepsOmittedTags(t *testing.T) {
	ts := newTestServer(t)
	var a models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey, "tags": []string{"4k", "hdr"}}), http.StatusCreated, &a)
	id := itoa64(a.ID)
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret}), http.StatusAccepted, &a)
	if !slices.Equal(a.Tags, []string{"4k", "hdr"}) {
		t.Fatalf("tags after update without tags = %v", a.Tags)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret, "tags": []string{"x"}}), http.StatusAccepted, &a)
	if !slices.Equal(a.Tags, []string{"x"}) {
		t.Fatalf("tags = %v", a.Tags)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret, "tags": []string{}}), http.StatusAccepted, &a)
	if len(a.Tags) != 0 {
		t.Fatalf("tags not cleared: %v", a.Tags)
	}
}

func TestCloseStopsBackgroundReevaluation(t *testing.T) {
	ts := newTestServer(t)
	ts.scanner.mu.Lock()
	ts.scanner.blockAll = true
	ts.scanner.started = make(chan struct{}, 1)
	ts.scanner.mu.Unlock()

	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"dryRun": true}), http.StatusAccepted, nil)
	select {
	case <-ts.scanner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background re-evaluation did not start")
	}
	done := make(chan struct{})
	go func() {
		ts.srv.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the background re-evaluation")
	}
	ts.srv.Close() // idempotent

	// After Close, changes no longer start background work.
	all, _ := ts.scanner.counts()
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"dryRun": true}), http.StatusAccepted, nil)
	ts.srv.waitBackground()
	if again, _ := ts.scanner.counts(); again != all {
		t.Fatalf("re-evaluation started after Close (%d → %d)", all, again)
	}
}

func TestLogFileSymlinkOutsideLogDirRefused(t *testing.T) {
	ts := newTestServer(t)
	logDir := filepath.Join(ts.dir, "logs")
	_, mgr, err := logging.Setup(logDir, "info", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	ts.srv.d.Logs = mgr
	secret := filepath.Join(ts.dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("api key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(logDir, "dupearr.3.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	rr := ts.do(http.MethodGet, "/api/v1/log/file/dupearr.3.txt", nil)
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "api key") {
		t.Fatalf("symlinked log file = %d %q", rr.Code, rr.Body.String())
	}
}

func TestBulkApproveSkipsReviewGroups(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	ok := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	suspect := ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending)
	if err := ts.db.Groups().UpdateStatus(ctx, suspect.ID, models.GroupReview, "Suspect merge: folder titles differ"); err != nil {
		t.Fatal(err)
	}
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": []int64{ok.ID, suspect.ID}, "action": "approve"}), http.StatusOK, &res)
	if !slices.Equal(res.Succeeded, []int64{ok.ID}) || len(res.Failed) != 1 || res.Failed[0].ID != suspect.ID ||
		!strings.Contains(res.Failed[0].Message, "review") {
		t.Fatalf("bulk = %+v", res)
	}
	if acts, _ := ts.db.Actions().ListByGroup(ctx, suspect.ID); len(acts) != 0 {
		t.Fatalf("bulk approve queued removals of a review group: %+v", acts)
	}
	// Approving it on its own (after looking at it) is still possible.
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(suspect.ID)+"/approve", nil), http.StatusOK, nil)
}

func TestMediaCoverIsSandboxed(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	rr := ts.do(http.MethodGet, "/api/v1/mediacover/"+itoa64(srv.ID)+"?path=/library/metadata/1/thumb/2", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("mediacover = %d, CSP %q", rr.Code, rr.Header().Get("Content-Security-Policy"))
	}
}

func TestArrEndpointMustBeUnique(t *testing.T) {
	ts := newTestServer(t)
	body := map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey}
	var a models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", body), http.StatusCreated, &a)
	dup := map[string]any{"name": "Radarr again", "kind": "radarr", "url": strings.ToUpper(ts.arr.srv.URL) + "/", "apiKey": fakeArrKey}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/arr?forceSave=true", dup), http.StatusConflict); !strings.Contains(msg, `"Radarr"`) {
		t.Fatalf("duplicate endpoint message = %q", msg)
	}
	var other models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr?forceSave=true", map[string]any{"name": "Radarr 4K", "kind": "radarr", "url": "http://127.0.0.1:1", "apiKey": "k2"}), http.StatusCreated, &other)
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+itoa64(other.ID)+"?forceSave=true", map[string]any{"name": "Radarr 4K", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": "k2"}), http.StatusConflict, nil)
	// Saving an instance with its own URL is fine.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+itoa64(a.ID), map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret}), http.StatusAccepted, nil)
}

// TestApproveRefusesGroupsWithMissingArrData: when an *arr instance could not be read during the
// last scan, files it tracks look untracked (keep tags and tracked state missing), so a manual
// approval is refused until a scan read it; other review groups stay approvable on their own.
func TestApproveRefusesGroupsWithMissingArrData(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	g := ts.seedGroup("movie:tmdb:1", "Heat", models.GroupPending)
	reason := "Incomplete data: could not read Radarr 4K (keep tags, tracked files and the download queue may be missing) — re-scan once this is fixed"
	if err := ts.db.Groups().UpdateStatus(ctx, g.ID, models.GroupReview, reason); err != nil {
		t.Fatal(err)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusConflict); !strings.Contains(msg, "could not be read") {
		t.Fatalf("message = %q", msg)
	}
	if acts, _ := ts.db.Actions().ListByGroup(ctx, g.ID); len(acts) != 0 {
		t.Fatalf("actions queued: %+v", acts)
	}

	other := ts.seedGroup("movie:tmdb:2", "Ronin", models.GroupPending)
	if err := ts.db.Groups().UpdateStatus(ctx, other.ID, models.GroupReview, "Possible mismatch: years differ"); err != nil {
		t.Fatal(err)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(other.ID)+"/approve", nil), http.StatusOK, nil)
}
