package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// signGroup stores a signature on a seeded group (the engine sets one on every evaluation).
func (ts *testServer) signGroup(g *models.DuplicateGroup, sig string) *models.DuplicateGroup {
	ts.t.Helper()
	g.Signature = sig
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		ts.t.Fatal(err)
	}
	got, err := ts.db.Groups().Get(context.Background(), g.ID)
	if err != nil {
		ts.t.Fatal(err)
	}
	if got.Signature != sig {
		ts.t.Fatalf("signature not stored: %q", got.Signature)
	}
	return got
}

// TestApprovalsPassTheCheckedSignature: the executor approves exactly the state the API checked —
// the reviewed signature from the body, else the one read at the start of the request — so a scan
// that changes the decisions in between makes the approval fail with 409 instead of queueing
// removals nobody checked.
func TestApprovalsPassTheCheckedSignature(t *testing.T) {
	t.Run("single without a body", func(t *testing.T) {
		ts := newTestServer(t)
		g := ts.signGroup(ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending), "sig-1")
		expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusOK, nil)
		if !slices.Equal(ts.executor.signatures, []string{"sig-1"}) {
			t.Fatalf("executor signatures %v, want the group's signature", ts.executor.signatures)
		}
	})
	t.Run("single with the reviewed signature", func(t *testing.T) {
		ts := newTestServer(t)
		g := ts.signGroup(ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending), "sig-1")
		expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", map[string]string{"signature": "sig-1"}), http.StatusOK, nil)
		if !slices.Equal(ts.executor.signatures, []string{"sig-1"}) {
			t.Fatalf("executor signatures %v", ts.executor.signatures)
		}
	})
	t.Run("a scan changes the group after the API's checks", func(t *testing.T) {
		ts := newTestServer(t)
		g := ts.signGroup(ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending), "sig-1")
		ts.executor.beforeApprove = func() {
			cp := *g
			ts.signGroup(&cp, "sig-2") // the scan stored other decisions
		}
		msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusConflict)
		if !strings.Contains(msg, "changed") {
			t.Fatalf("message %q", msg)
		}
		if acts, _ := ts.db.Actions().ListByGroup(context.Background(), g.ID); len(acts) != 0 {
			t.Fatalf("removals queued: %+v", acts)
		}
	})
	t.Run("bulk", func(t *testing.T) {
		ts := newTestServer(t)
		a := ts.signGroup(ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending), "sig-a")
		b := ts.signGroup(ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending), "sig-b")
		var res bulkResponse
		expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{
			"ids": []int64{a.ID, b.ID}, "action": "approve", "signatures": map[string]string{strconv.FormatInt(a.ID, 10): "sig-a"},
		}), http.StatusOK, &res)
		if len(res.Succeeded) != 2 || len(res.Failed) != 0 {
			t.Fatalf("bulk result %+v", res)
		}
		if !slices.Equal(ts.executor.signatures, []string{"sig-a", "sig-b"}) {
			t.Fatalf("executor signatures %v, want the reviewed one, then the one read", ts.executor.signatures)
		}
	})
}

// TestExclusionChangesReevaluateCoveredGroups: creating an exclusion re-evaluates the open groups
// it covers right away (and announces the queue); deleting it re-evaluates them again.
func TestExclusionChangesReevaluateCoveredGroups(t *testing.T) {
	ts := newTestServer(t)
	covered := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupQueued)
	ts.seedGroup("movie:tmdb:22", "Bravo", models.GroupPending)
	ch, unsub := ts.bus.Subscribe(32)
	defer unsub()
	var ex models.Exclusion
	expect(t, ts.do(http.MethodPost, "/api/v1/exclusion", map[string]any{"kind": "title_regex", "value": "^alpha$"}), http.StatusCreated, &ex)
	calls, ids := ts.scanner.matching()
	if calls != 1 || !slices.Equal(ids, []int64{covered.ID}) {
		t.Fatalf("re-evaluated %v in %d calls, want only %d", ids, calls, covered.ID)
	}
	var queue bool
	for len(ch) > 0 {
		if ev := <-ch; ev.Name == events.NameQueue {
			queue = true
		}
	}
	if !queue {
		t.Error("no queue event after the exclusion was added")
	}
	expect(t, ts.do(http.MethodDelete, "/api/v1/exclusion/"+itoa64(ex.ID), nil), http.StatusOK, nil)
	if calls, ids = ts.scanner.matching(); calls != 2 || !slices.Equal(ids, []int64{covered.ID, covered.ID}) {
		t.Fatalf("after delete: re-evaluated %v in %d calls", ids, calls)
	}
}

// TestLibraryChangesReevaluateItsGroups: disabling a library (or changing its profile or scope
// group) re-evaluates the groups with a version in it before the response is written.
func TestLibraryChangesReevaluateItsGroups(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	lib := ts.seedLibrary(srv.ID, "1", "Movies")
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending) // library 1
	if lib.ID != 1 {
		t.Fatalf("setup: library id %d", lib.ID)
	}
	lid := itoa64(lib.ID)
	for i, patch := range []map[string]any{
		{"enabled": false},
		{"enabled": false, "scopeGroup": "movies"},
		{"enabled": true, "scopeGroup": "movies"},
	} {
		expect(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, patch), http.StatusAccepted, nil)
		if calls, ids := ts.scanner.matching(); calls != i+1 || ids[len(ids)-1] != g.ID {
			t.Fatalf("patch %v: re-evaluated %v in %d calls", patch, ids, calls)
		}
	}
	// No relevant change: nothing re-evaluated.
	expect(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, map[string]any{"enabled": true, "scopeGroup": "Movies "}), http.StatusAccepted, nil)
	if calls, _ := ts.scanner.matching(); calls != 3 {
		t.Fatalf("an unchanged library was re-evaluated (%d calls)", calls)
	}
}

// otherPlex starts a second fake Plex server with another machine identifier.
func otherPlex(t *testing.T) *fakePlex {
	t.Helper()
	p := newFakePlex(t)
	p.machineID.Store("machine-other")
	return p
}

// TestForcedSaveKeepsTheServerIdentity: ?forceSave=true skips the connection test, not the
// identity check — a reachable server that is not the stored one is refused (409); an unreachable
// one is saved; a server stored without an identity gets it from a forced save, a test or a sync
// that reaches it, never overwriting another one.
func TestForcedSaveKeepsTheServerIdentity(t *testing.T) {
	ctx := context.Background()
	stored := func(ts *testServer, id int64) models.MediaServer {
		ts.t.Helper()
		ms, err := ts.db.MediaServers().Get(ctx, id)
		if err != nil {
			ts.t.Fatal(err)
		}
		return *ms
	}
	t.Run("re-pointing to another reachable server", func(t *testing.T) {
		ts := newTestServer(t)
		srv := ts.seedServer("Plex")
		other := otherPlex(t)
		path := "/api/v1/mediaserver/" + itoa64(srv.ID) + "?forceSave=true"
		msg := message(t, ts.do(http.MethodPut, path, map[string]any{"name": "Plex", "url": other.srv.URL, "token": fakePlexToken}), http.StatusConflict)
		if !strings.Contains(msg, "different Plex server") {
			t.Fatalf("message %q", msg)
		}
		if got := stored(ts, srv.ID); got.URL != srv.URL || got.MachineIdentifier != fakePlexMachineID {
			t.Fatalf("stored server changed: %+v", got)
		}
		// Unreachable: saved, the stored identity stays (scans and the executor check it).
		expect(t, ts.do(http.MethodPut, path, map[string]any{"name": "Plex", "url": closedURL(t), "token": fakePlexToken}), http.StatusAccepted, nil)
		if got := stored(ts, srv.ID); got.MachineIdentifier != fakePlexMachineID {
			t.Fatalf("identity %q, want the stored one kept", got.MachineIdentifier)
		}
		// The same server again: saved.
		expect(t, ts.do(http.MethodPut, path, map[string]any{"name": "Plex", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusAccepted, nil)
	})
	t.Run("forced create reads the identity", func(t *testing.T) {
		ts := newTestServer(t)
		var ms models.MediaServer
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver?forceSave=true", map[string]any{"name": "Plex", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusCreated, &ms)
		if ms.MachineIdentifier != fakePlexMachineID {
			t.Fatalf("identity %q", ms.MachineIdentifier)
		}
		// The same server a second time, forced: refused like an unforced save.
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver?forceSave=true", map[string]any{"name": "Copy", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusConflict, nil)
	})
	t.Run("an empty identity is filled in, never overwritten", func(t *testing.T) {
		ts := newTestServer(t)
		var ms models.MediaServer
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver?forceSave=true", map[string]any{"name": "Plex", "url": closedURL(t), "token": fakePlexToken}), http.StatusCreated, &ms)
		if ms.MachineIdentifier != "" {
			t.Fatalf("identity %q for an unreachable server", ms.MachineIdentifier)
		}
		// A test of other (unsaved) values does not fill it in.
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"id": ms.ID, "name": "Plex", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusOK, nil)
		if got := stored(ts, ms.ID); got.MachineIdentifier != "" {
			t.Fatalf("identity %q taken from a test of other values", got.MachineIdentifier)
		}
		// A forced save that reaches the server fills it in.
		expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID)+"?forceSave=true", map[string]any{"name": "Plex", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusAccepted, nil)
		if got := stored(ts, ms.ID); got.MachineIdentifier != fakePlexMachineID {
			t.Fatalf("identity %q after a forced save that reached the server", got.MachineIdentifier)
		}
	})
	t.Run("a test of the saved server fills it in", func(t *testing.T) {
		ts := newTestServer(t)
		ms := models.MediaServer{Name: "Plex", Kind: models.MediaServerPlex, URL: ts.plex.srv.URL, Token: fakePlexToken, Enabled: true}
		if err := ts.db.MediaServers().Create(ctx, &ms); err != nil {
			t.Fatal(err)
		}
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"id": ms.ID, "name": "Plex", "url": ms.URL, "token": maskedSecret}), http.StatusOK, nil)
		if got := stored(ts, ms.ID); got.MachineIdentifier != fakePlexMachineID {
			t.Fatalf("identity %q after testing the saved server", got.MachineIdentifier)
		}
	})
	t.Run("not when another server has it", func(t *testing.T) {
		ts := newTestServer(t)
		ts.seedServer("Plex")
		ms := models.MediaServer{Name: "Copy", Kind: models.MediaServerPlex, URL: ts.plex.srv.URL, Token: fakePlexToken, Enabled: true}
		if err := ts.db.MediaServers().Create(ctx, &ms); err != nil {
			t.Fatal(err)
		}
		expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"id": ms.ID, "name": "Copy", "url": ms.URL, "token": maskedSecret}), http.StatusOK, nil)
		if got := stored(ts, ms.ID); got.MachineIdentifier != "" {
			t.Fatalf("identity %q stored for a second connection to the same server", got.MachineIdentifier)
		}
		expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID)+"?forceSave=true", map[string]any{"name": "Copy", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusConflict, nil)
	})
}

// TestRunningInDocker: the official image's DUPEARR_DOCKER=1 counts (Podman and Kubernetes have no
// /.dockerenv); Podman's marker file is known too.
func TestRunningInDocker(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE"} {
		t.Setenv("DUPEARR_DOCKER", v)
		if !RunningInDocker() {
			t.Errorf("DUPEARR_DOCKER=%s not detected", v)
		}
	}
	if !slices.Contains(dockerMarkers, "/run/.containerenv") || !slices.Contains(dockerMarkers, "/.dockerenv") {
		t.Errorf("markers %v", dockerMarkers)
	}
}
