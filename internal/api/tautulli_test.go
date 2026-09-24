package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const fakeTautulliKey = "tautullikey0123456789abcdef"

// fakeTautulli emulates the Tautulli commands of a connection test. It fails the test when the API
// key ever reaches a URL.
type fakeTautulli struct {
	srv     *httptest.Server
	mu      sync.Mutex
	version string
	pms     string
}

func newFakeTautulli(t *testing.T) *fakeTautulli {
	f := &fakeTautulli{version: "v2.18.1", pms: fakePlexMachineID}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.ToLower(r.URL.RawQuery), "apikey") || strings.Contains(r.URL.String(), fakeTautulliKey) {
			t.Errorf("the API key reached a URL: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		answer := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": "success", "message": nil, "data": data}})
		}
		if r.Header.Get("X-Api-Key") != fakeTautulliKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"response":{"result":"error","message":"Invalid apikey","data":{}}}`))
			return
		}
		f.mu.Lock()
		version, pms := f.version, f.pms
		f.mu.Unlock()
		q := r.URL.Query()
		switch q.Get("cmd") {
		case "get_tautulli_info":
			answer(map[string]any{"tautulli_version": version})
		case "get_server_info":
			answer(map[string]any{"pms_identifier": pms, "pms_name": "Test Plex"})
		case "get_users":
			answer([]map[string]any{
				{"user_id": 1, "username": "owner", "email": "owner@example.com", "is_active": 1, "keep_history": 1},
				{"user_id": 2, "username": "guest", "email": "guest@example.com", "is_active": 1, "keep_history": 0},
			})
		case "get_library":
			keep := 1
			if q.Get("section_id") == "2" {
				keep = 0
			}
			answer(map[string]any{"section_id": q.Get("section_id"), "keep_history": keep})
		case "get_history":
			if q.Get("section_id") == "1" {
				answer(map[string]any{"recordsFiltered": 1, "data": []map[string]any{{"row_id": 7, "rating_key": "101",
					"user_id": 1, "user": "owner", "started": 1740859200, "stopped": 1740866400}}})
				return
			}
			answer(map[string]any{"recordsFiltered": 0, "data": []any{}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTautulli) set(version, pms string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version, f.pms = version, pms
}

// seedLibraries syncs "Movies" (section 1) and "Movies 4K" (section 2) on the server.
func (ts *testServer) seedLibraries(serverID int64) {
	ts.t.Helper()
	if _, err := ts.db.Libraries().Sync(context.Background(), serverID, []models.Library{
		{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/movies"}},
		{SectionKey: "2", Title: "Movies 4K", Type: "movie", Locations: []string{"/data/movies4k"}},
	}); err != nil {
		ts.t.Fatal(err)
	}
}

func TestTautulliCRUD(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	tt := newFakeTautulli(t)
	srv := ts.seedServer("Plex")
	ts.seedLibraries(srv.ID)
	good := map[string]any{"name": "Tautulli", "serverId": srv.ID, "url": tt.srv.URL + "/", "apiKey": fakeTautulliKey}

	for _, c := range []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"serverId": srv.ID, "url": tt.srv.URL, "apiKey": fakeTautulliKey}, "name"},
		{map[string]any{"name": "T", "serverId": srv.ID, "apiKey": fakeTautulliKey}, "url"},
		{map[string]any{"name": "T", "serverId": srv.ID, "url": "http://u:p@tautulli", "apiKey": fakeTautulliKey}, "url"},
		{map[string]any{"name": "T", "serverId": srv.ID, "url": tt.srv.URL + "/?apikey=x", "apiKey": fakeTautulliKey}, "url"},
		{map[string]any{"name": "T", "serverId": srv.ID, "url": tt.srv.URL}, "apiKey"},
		{map[string]any{"name": "T", "serverId": srv.ID, "url": tt.srv.URL, "apiKey": maskedSecret}, "apiKey"},
		{map[string]any{"name": "T", "url": tt.srv.URL, "apiKey": fakeTautulliKey}, "serverId"},
		{map[string]any{"name": "T", "serverId": 999, "url": tt.srv.URL, "apiKey": fakeTautulliKey}, "serverId"},
	} {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/tautulli", c.body)); !hasProp(props, c.prop) {
			t.Errorf("%v: props %v, want %s", c.body, props, c.prop)
		}
	}
	bad := map[string]any{"name": "T", "serverId": srv.ID, "url": tt.srv.URL, "apiKey": "wrong"}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/tautulli", bad), http.StatusBadRequest); !strings.Contains(msg, "rejected the API key") {
		t.Errorf("wrong key message = %q", msg)
	}

	var created models.TautulliInstance
	rr := ts.do(http.MethodPost, "/api/v1/tautulli", good)
	expect(t, rr, http.StatusCreated, &created)
	if created.ID == 0 || created.APIKey != maskedSecret || created.URL != tt.srv.URL || !created.Enabled || !created.VerifyTLS {
		t.Fatalf("created = %+v", created)
	}
	stored, _ := ts.db.Tautullis().Get(ctx, created.ID)
	if stored.APIKey != fakeTautulliKey {
		t.Fatalf("stored key = %q", stored.APIKey)
	}
	var list []models.TautulliInstance
	expect(t, ts.do(http.MethodGet, "/api/v1/tautulli", nil), http.StatusOK, &list)
	if len(list) != 1 || list[0].APIKey != maskedSecret {
		t.Fatalf("list = %+v", list)
	}
	var one models.TautulliInstance
	expect(t, ts.do(http.MethodGet, "/api/v1/tautulli/"+itoa64(created.ID), nil), http.StatusOK, &one)
	if one.APIKey != maskedSecret {
		t.Fatalf("get = %+v", one)
	}

	// One Tautulli per media server, and the same Tautulli only once.
	expect(t, ts.do(http.MethodPost, "/api/v1/tautulli?forceSave=true", map[string]any{"name": "Again", "serverId": srv.ID,
		"url": closedURL(t), "apiKey": "k"}), http.StatusConflict, nil)
	other := ts.seedServer("Other Plex")
	expect(t, ts.do(http.MethodPost, "/api/v1/tautulli?forceSave=true", map[string]any{"name": "Same", "serverId": other.ID,
		"url": tt.srv.URL, "apiKey": "k"}), http.StatusConflict, nil)

	// Update with the masked key: kept for the same endpoint …
	upd := map[string]any{"name": "Renamed", "serverId": srv.ID, "url": tt.srv.URL, "apiKey": maskedSecret, "enabled": true}
	expect(t, ts.do(http.MethodPut, "/api/v1/tautulli/"+itoa64(created.ID), upd), http.StatusAccepted, &one)
	if stored, _ := ts.db.Tautullis().Get(ctx, created.ID); stored.APIKey != fakeTautulliKey || stored.Name != "Renamed" {
		t.Fatalf("after update: %+v", stored)
	}
	// … refused for another endpoint (the key would be sent there) …
	moved := map[string]any{"name": "Renamed", "serverId": srv.ID, "url": closedURL(t), "apiKey": maskedSecret}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/tautulli/"+itoa64(created.ID)+"?forceSave=true", moved)); !hasProp(props, "apiKey") {
		t.Fatalf("moved with a masked key: props %v", props)
	}
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/tautulli/test", map[string]any{"id": created.ID, "name": "T",
		"serverId": srv.ID, "url": closedURL(t), "apiKey": maskedSecret})); !hasProp(props, "apiKey") {
		t.Fatalf("test of another endpoint with a masked key: props %v", props)
	}
	// … and for weaker TLS verification.
	secure := "https://tautulli.example:8181"
	expect(t, ts.do(http.MethodPut, "/api/v1/tautulli/"+itoa64(created.ID)+"?forceSave=true", map[string]any{"name": "S",
		"serverId": srv.ID, "url": secure, "apiKey": fakeTautulliKey, "verifyTls": true}), http.StatusAccepted, nil)
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/tautulli/"+itoa64(created.ID)+"?forceSave=true", map[string]any{"name": "S",
		"serverId": srv.ID, "url": secure, "apiKey": maskedSecret, "verifyTls": false})); !hasProp(props, "apiKey") {
		t.Fatalf("weaker TLS with a masked key: props %v", props)
	}

	// Every change is audited, without the key.
	events := securityEvents(t, ts, audit.KindConnectionChanged)
	if len(events) < 3 {
		t.Fatalf("%d connection events, want the create and the updates", len(events))
	}
	for _, e := range events {
		if strings.Contains(e.Message+string(e.Data), fakeTautulliKey) {
			t.Fatalf("a security event holds the key: %+v", e)
		}
	}

	expect(t, ts.do(http.MethodDelete, "/api/v1/tautulli/"+itoa64(created.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/tautulli/"+itoa64(created.ID), nil), http.StatusNotFound, nil)
	if n := len(securityEvents(t, ts, audit.KindConnectionChanged)); n != len(events)+1 {
		t.Fatalf("the delete was not audited (%d events)", n)
	}
}

func TestTautulliTest(t *testing.T) {
	ts := newTestServer(t)
	tt := newFakeTautulli(t)
	srv := ts.seedServer("Plex")
	ts.seedLibraries(srv.ID)
	body := map[string]any{"name": "T", "serverId": srv.ID, "url": tt.srv.URL, "apiKey": fakeTautulliKey}

	var res tautulliTestResult
	rr := ts.do(http.MethodPost, "/api/v1/tautulli/test", body)
	expect(t, rr, http.StatusOK, &res)
	if res.Version != "v2.18.1" || !res.ServerMatches || res.PMSIdentifier != fakePlexMachineID || res.UsersWithoutHistory != 1 ||
		len(res.LibrariesWithoutHistory) != 1 || res.LibrariesWithoutHistory[0] != "Movies 4K" ||
		res.HistorySince == nil || res.HistorySince.Unix() != 1740859200 {
		t.Fatalf("result = %+v", res)
	}
	for _, private := range []string{"owner@example.com", "guest", fakeTautulliKey} {
		if strings.Contains(rr.Body.String(), private) {
			t.Fatalf("the test result shows %q: %s", private, rr.Body.String())
		}
	}

	// Another Plex server behind this Tautulli.
	tt.set("v2.18.1", "another-machine")
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/tautulli/test", body), http.StatusBadRequest); !strings.Contains(msg, "another Plex server") {
		t.Fatalf("identity message = %q", msg)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/tautulli", body), http.StatusBadRequest, nil)
	// Too old: the key would have to travel in the URL.
	tt.set("v2.17.2", fakePlexMachineID)
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/tautulli/test", body), http.StatusBadRequest); !strings.Contains(msg, "2.18.0") {
		t.Fatalf("version message = %q", msg)
	}
	tt.set("v2.18.1", fakePlexMachineID)
	// Unreachable: 502; forceSave stores it anyway.
	down := map[string]any{"name": "Down", "serverId": srv.ID, "url": closedURL(t), "apiKey": fakeTautulliKey}
	message(t, ts.do(http.MethodPost, "/api/v1/tautulli/test", down), http.StatusBadGateway)
	expect(t, ts.do(http.MethodPost, "/api/v1/tautulli", down), http.StatusBadGateway, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/tautulli?forceSave=true", down), http.StatusCreated, nil)

	// A media server without a stored identity cannot be matched.
	blank := models.MediaServer{Name: "Blank", Kind: models.MediaServerPlex, URL: "http://blank:32400", Token: "t", Enabled: true}
	if err := ts.db.MediaServers().Create(context.Background(), &blank); err != nil {
		t.Fatal(err)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/tautulli/test", map[string]any{"name": "T", "serverId": blank.ID,
		"url": tt.srv.URL, "apiKey": fakeTautulliKey}), http.StatusBadRequest); !strings.Contains(msg, "machine identifier") {
		t.Fatalf("blank identity message = %q", msg)
	}
}

// TestTautulliTestDoesNotReflectUpstreamBodies: like the *arr and Plex tests (SEC-031), a Tautulli
// test against an internal service reports the status, never the body.
func TestTautulliTestDoesNotReflectUpstreamBodies(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	internal := internalService(t)
	rr := ts.do(http.MethodPost, "/api/v1/tautulli/test", map[string]any{"name": "T", "serverId": srv.ID,
		"url": internal.URL + "/any/prefix", "apiKey": fakeTautulliKey})
	if rr.Code < 400 {
		t.Fatalf("status %d, want an error", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "INTERNAL-ONLY-SECRET") || strings.Contains(rr.Body.String(), "svc-v9.1") {
		t.Fatalf("the upstream body is reflected: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "500") {
		t.Fatalf("the status should still be reported: %s", rr.Body.String())
	}
}
