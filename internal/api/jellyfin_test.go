package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Jellyfin connections (docs/DECISIONS.md D12, research §5.3.6) against the fake Jellyfin 12.1.

func jellyfinBody(env *fakemedia.Env, over map[string]any) map[string]any {
	b := map[string]any{"name": "Jelly", "kind": "jellyfin", "url": env.Jellyfin.URL, "token": env.JellyfinAPIKey, "enabled": true}
	for k, v := range over {
		b[k] = v
	}
	return b
}

// noKeyInURLs fails when any request to the fake carried the key outside the Authorization header,
// or when a public identity read carried a credential.
func noKeyInURLs(t *testing.T, env *fakemedia.Env) {
	t.Helper()
	for _, r := range env.RequestsTo(fakemedia.ServerJellyfin) {
		if strings.Contains(r.Query.Encode(), env.JellyfinAPIKey) || r.Header.Get("X-Emby-Token") != "" {
			t.Errorf("%s %s carried the key outside the Authorization header", r.Method, r.Path)
		}
		if r.Path == "/System/Info/Public" && r.Header.Get("Authorization") != "" {
			t.Errorf("the public identity read sent a credential")
		}
	}
	env.AssertNoViolations(t)
}

func TestJellyfinServerCRUD(t *testing.T) {
	ts := newTestServer(t)
	env := fakemedia.Start(t, fakemedia.JellyfinAppendixA())
	defer noKeyInURLs(t, env)

	var res mediaServerTestResult
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", jellyfinBody(env, nil)), http.StatusOK, &res)
	if res.Product != "Jellyfin Server" || !res.Administrator || res.MachineIdentifier != env.JellyfinServerID ||
		res.Version != "12.1.0" || res.RemovalsDisabled != "" || res.MediaDeletionAllowed || res.Owned != nil {
		t.Fatalf("test result %+v", res)
	}

	var ms models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", jellyfinBody(env, nil)), http.StatusCreated, &ms)
	if ms.Kind != models.MediaServerJellyfin || ms.Token != maskedSecret || ms.MachineIdentifier != env.JellyfinServerID {
		t.Fatalf("created %+v", ms)
	}
	stored, err := ts.db.MediaServers().Get(context.Background(), ms.ID)
	if err != nil || stored.Token != env.JellyfinAPIKey {
		t.Fatalf("stored %+v %v", stored, err)
	}
	for _, path := range []string{"/api/v1/mediaserver", "/api/v1/mediaserver/" + itoa64(ms.ID)} {
		if rr := ts.do(http.MethodGet, path, nil); strings.Contains(rr.Body.String(), env.JellyfinAPIKey) {
			t.Fatalf("GET %s shows the key", path)
		}
	}
	// The same server twice is refused.
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/mediaserver", jellyfinBody(env, map[string]any{"name": "Again"})), http.StatusConflict); !strings.Contains(msg, "This Jellyfin server is already configured") {
		t.Fatalf("duplicate: %q", msg)
	}
	// An update with the masked key keeps it for the same address; another address needs it again.
	upd := jellyfinBody(env, map[string]any{"token": maskedSecret, "name": "Jellyfin"})
	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), upd), http.StatusAccepted, &ms)
	if ms.Name != "Jellyfin" || ms.Token != maskedSecret {
		t.Fatalf("updated %+v", ms)
	}
	moved := jellyfinBody(env, map[string]any{"token": maskedSecret, "url": "http://127.0.0.1:1"})
	if rr := ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), moved); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Re-enter the API key") {
		t.Fatalf("moved: %d %s", rr.Code, rr.Body.String())
	}
	// The kind of a stored server cannot change.
	if rr := ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), jellyfinBody(env, map[string]any{"kind": "plex", "token": maskedSecret})); rr.Code != http.StatusBadRequest ||
		!strings.Contains(rr.Body.String(), "cannot be changed") {
		t.Fatalf("kind change: %d %s", rr.Code, rr.Body.String())
	}
	// Testing the saved server with its masked key and no kind tests it as Jellyfin.
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"id": ms.ID, "name": "Jellyfin", "url": env.Jellyfin.URL, "token": maskedSecret}), http.StatusOK, &res)
	if res.Product != "Jellyfin Server" {
		t.Fatalf("saved test %+v", res)
	}
	// Tautulli records Plex servers only.
	tb := map[string]any{"name": "T", "url": "http://127.0.0.1:1", "apiKey": "k", "serverId": ms.ID}
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/tautulli?forceSave=true", tb)); !hasProp(props, "serverId") {
		t.Fatalf("tautulli on a Jellyfin server: %v", props)
	}
}

// TestJellyfinConnectionTestRefusals: the test refuses another product, an old version, a wrong
// key and a credential that is not an API key or administrator; path substitutions are reported.
func TestJellyfinConnectionTestRefusals(t *testing.T) {
	ts := newTestServer(t)
	env := fakemedia.Start(t, fakemedia.JellyfinAppendixA())
	test := func(body map[string]any) (int, string) {
		rr := ts.do(http.MethodPost, "/api/v1/mediaserver/test", body)
		return rr.Code, rr.Body.String()
	}
	if code, body := test(jellyfinBody(env, map[string]any{"token": "00000000000000000000000000000bad"})); code != http.StatusBadRequest || !strings.Contains(body, "rejected the API key") {
		t.Fatalf("wrong key: %d %s", code, body)
	}
	if code, body := test(jellyfinBody(env, map[string]any{"token": env.JellyfinUserToken})); code != http.StatusBadRequest || !strings.Contains(body, "Dashboard") {
		t.Fatalf("user token: %d %s", code, body)
	}
	if code, body := test(jellyfinBody(env, map[string]any{"url": ts.plex.srv.URL})); code != http.StatusBadRequest && code != http.StatusBadGateway {
		t.Fatalf("a Plex URL: %d %s", code, body)
	}
	env.SetJellyfinPathSubstitutions(fakemedia.JellyfinPathSubstitution{From: "/data", To: "/mnt"})
	var res mediaServerTestResult
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", jellyfinBody(env, nil)), http.StatusOK, &res)
	if !strings.Contains(res.RemovalsDisabled, "path substitutions") {
		t.Fatalf("substitutions: %+v", res)
	}
	env.SetJellyfinVersion("12.0.4")
	env.ResetRequests()
	if code, body := test(jellyfinBody(env, nil)); code != http.StatusBadRequest || !strings.Contains(body, "12.1.0") {
		t.Fatalf("12.0.4: %d %s", code, body)
	}
	// An old server never receives the key: it is refused on its public identity.
	if reqs := env.RequestsTo(fakemedia.ServerJellyfin); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "" {
		t.Fatalf("requests to an old server: %+v", reqs)
	}
	noKeyInURLs(t, env)
}

// TestJellyfinForcedSaveProbe: a forced save asks only /System/Info/Public, without a credential.
func TestJellyfinForcedSaveProbe(t *testing.T) {
	ts := newTestServer(t)
	env := fakemedia.Start(t, fakemedia.JellyfinAppendixA())
	env.ResetRequests()
	var ms models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver?forceSave=true", jellyfinBody(env, nil)), http.StatusCreated, &ms)
	if ms.MachineIdentifier != env.JellyfinServerID {
		t.Fatalf("forced save identity %q", ms.MachineIdentifier)
	}
	reqs := env.RequestsTo(fakemedia.ServerJellyfin)
	if len(reqs) != 1 || reqs[0].Path != "/System/Info/Public" || reqs[0].Header.Get("Authorization") != "" {
		t.Fatalf("forced save requests: %+v", reqs)
	}
}

// TestJellyfinGroupsAreNotBulkApproved: a Jellyfin group is only approved on its own page.
func TestJellyfinGroupsAreNotBulkApproved(t *testing.T) {
	ts := newTestServer(t)
	g := ts.seedGroup("movie:tmdb:603", "Alpha", models.GroupPending)
	for i := range g.Files {
		g.Files[i].Version.Key = "jellyfin:1:" + strings.Repeat(string(rune('a'+i)), 32)
		g.Files[i].Version.MediaID, g.Files[i].Version.SourceID = 0, strings.Repeat(string(rune('a'+i)), 32)
	}
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	var res bulkResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"action": "approve", "ids": []int64{g.ID}}), http.StatusOK, &res)
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "Jellyfin") {
		t.Fatalf("bulk: %+v", res)
	}
	rr := ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil)
	if rr.Code != http.StatusOK && rr.Code != http.StatusAccepted {
		t.Fatalf("single approval: %d %s", rr.Code, rr.Body.String())
	}
}
