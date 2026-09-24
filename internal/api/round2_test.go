package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Round-2 security review regression tests (see the per-test comments for the finding).

const internalSecret = "INTERNAL-ONLY-SECRET build=svc-v9.1"

// internalService answers every request with HTTP 500 and a body an attacker wants to read.
func internalService(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(internalSecret))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// r2-outbound-web#5: the *arr and Plex connection tests must not reflect an internal service's
// error body (the URL, including a path prefix, is free-form).
func TestConnectionTestsDoNotReflectUpstreamBodies(t *testing.T) {
	ts := newTestServer(t)
	internal := internalService(t)
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/arr/test", map[string]any{"name": "R", "kind": "radarr", "url": internal.URL + "/any/prefix", "apiKey": "0123456789abcdef0123456789abcdef"}},
		{"/api/v1/mediaserver/test", map[string]any{"name": "P", "url": internal.URL + "/any/prefix", "token": "tok"}},
	} {
		rr := ts.do(http.MethodPost, c.path, c.body)
		if rr.Code < 400 {
			t.Fatalf("%s = %d, want an error", c.path, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "INTERNAL-ONLY-SECRET") || strings.Contains(rr.Body.String(), "svc-v9.1") {
			t.Errorf("%s reflects the upstream body: %s", c.path, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "500") {
			t.Errorf("%s: the status should still be reported: %s", c.path, rr.Body.String())
		}
	}
}

// r2-data-files#2 (SEC-022 part 2): a mapping's local folder must stay out of Dupearr's data
// folder and its recycle bin; operating-system folders are warned about.
func TestPathMappingLocalFolderPlacement(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	data := ts.cfg.DataDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := ts.db.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st.RecycleBinPath = bin
	if err := ts.db.Settings().Save(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	for _, local := range []string{data, filepath.Join(data, "Backups"), filepath.Join(data, "logs", "x"), filepath.Dir(data), bin, filepath.Join(bin, "2026-09-20")} {
		body := map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/data/movies", "localPath": local}
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/pathmapping", body)); !hasProp(props, "localPath") {
			t.Errorf("local %s: props = %v, want a 400 on localPath", local, props)
		}
	}
	// A system folder is saved with a warning.
	var m models.PathMapping
	rr := ts.do(http.MethodPost, "/api/v1/pathmapping", map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/data/movies", "localPath": "/etc"})
	expect(t, rr, http.StatusCreated, &m)
	if w := rr.Header().Get("X-Dupearr-Warning"); !strings.Contains(w, "system folder") {
		t.Errorf("warning = %q, want a system-folder warning", w)
	}
}

// r2-outbound-web#3: a browser session (cookie, local-address bypass) never receives the master
// API key without re-entering the password, and the key cannot be rotated or used to change
// credentials without it either.
func TestBrowserSessionsDoNotGetTheAPIKey(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal }
	})
	ts.createUser("admin", "secret-pw")
	cookie := ts.login("admin", "secret-pw")
	const base = "http://dupearr.local"
	origin := withHeader("Origin", base)
	lan := withRemote("192.168.1.20:50000")

	for name, opts := range map[string][]reqOption{
		"cookie":       {noKey, withCookie(cookie)},
		"local bypass": {noKey, lan},
	} {
		rr := ts.do(http.MethodGet, base+"/api/v1/config/host", nil, opts...)
		var h hostConfig
		expect(t, rr, http.StatusOK, &h)
		if strings.Contains(rr.Body.String(), ts.key) || h.APIKey != maskedSecret {
			t.Errorf("%s: GET /config/host carries the API key: %s", name, rr.Body.String())
		}
		if h.WebhookToken == "" {
			t.Errorf("%s: the webhook token (needed for webhook URLs) is missing", name)
		}
		rr = ts.do(http.MethodGet, base+"/initialize.json", nil, opts...)
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), ts.key) {
			t.Errorf("%s: initialize.json = %d %s", name, rr.Code, rr.Body.String())
		}
		// Rotating the key would hand the new one out: it needs the password too.
		if props := validationProps(t, ts.do(http.MethodPost, base+"/api/v1/config/host/apikey", nil, append(opts, origin)...)); !hasProp(props, "currentPassword") {
			t.Errorf("%s: regenerate without the password: props %v", name, props)
		}
	}
	// Reveal: the password is required and checked.
	reveal := func(body any, opts ...reqOption) *httptest.ResponseRecorder {
		return ts.do(http.MethodPost, base+"/api/v1/config/host/apikey/reveal", body, append([]reqOption{noKey, withCookie(cookie), origin}, opts...)...)
	}
	if props := validationProps(t, reveal(nil)); !hasProp(props, "currentPassword") {
		t.Errorf("reveal without the password: props %v", props)
	}
	if props := validationProps(t, reveal(map[string]any{"currentPassword": "wrong"}, withRemote("198.51.100.1:1"))); !hasProp(props, "currentPassword") {
		t.Errorf("reveal with a wrong password: props %v", props)
	}
	var got revealAPIKeyResponse
	expect(t, reveal(map[string]any{"currentPassword": "secret-pw"}), http.StatusOK, &got)
	if got.APIKey != ts.key {
		t.Fatalf("revealed %q, want the API key", got.APIKey)
	}
	// The key holder is not asked to prove what it already holds for reads.
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if h.APIKey != ts.key {
		t.Errorf("GET with the API key: apiKey = %q", h.APIKey)
	}
	// ...but rotating it needs the password as well.
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey", nil)); !hasProp(props, "currentPassword") {
		t.Errorf("regenerate with only the API key: props %v", props)
	}
	var rotated hostConfig
	expect(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey", map[string]any{"currentPassword": "secret-pw"}), http.StatusOK, &rotated)
	if rotated.APIKey == ts.key || rotated.APIKey == maskedSecret || len(rotated.APIKey) != 32 {
		t.Fatalf("regenerated key = %q", rotated.APIKey)
	}
}

// Without a Forms account (External, None, or before setup) there is no password to ask for.
func TestAPIKeyShownWithoutAPassword(t *testing.T) {
	ts := newTestServer(t)
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if h.APIKey != ts.key {
		t.Fatalf("apiKey = %q", h.APIKey)
	}
	var got revealAPIKeyResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey/reveal", nil), http.StatusOK, &got)
	if got.APIKey != ts.key {
		t.Fatalf("revealed %q", got.APIKey)
	}
}
