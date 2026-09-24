package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func TestPing(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(http.MethodGet, "/ping", nil, noKey)
	var body map[string]string
	expect(t, rr, http.StatusOK, &body)
	if body["status"] != "OK" {
		t.Fatalf("body = %v", body)
	}
	if rr := ts.do(http.MethodHead, "/ping", nil, noKey); rr.Code != http.StatusOK {
		t.Fatalf("HEAD /ping = %d", rr.Code)
	}
	// A closed database fails the check (after the cache expires).
	ts.srv.ping.checked = time.Time{}
	_ = ts.db.Close()
	rr = ts.do(http.MethodGet, "/ping", nil, noKey)
	expect(t, rr, http.StatusInternalServerError, &body)
	if body["status"] != "Error" {
		t.Fatalf("body = %v", body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts := newTestServer(t)
	for _, target := range []string{"/ping", "/", "/api/v1/system/status", "/api/v1/nope"} {
		rr := ts.do(http.MethodGet, target, nil)
		for k, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "SAMEORIGIN",
			"Referrer-Policy":        "same-origin",
		} {
			if got := rr.Header().Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", target, k, got, want)
			}
		}
	}
	if cc := ts.do(http.MethodGet, "/initialize.json", nil).Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("initialize.json Cache-Control = %q", cc)
	}
}

func TestUnauthenticatedAPI(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(http.MethodGet, "/api/v1/system/status", nil, noKey)
	if msg := message(t, rr, http.StatusUnauthorized); msg != "Unauthorized" {
		t.Fatalf("message = %q", msg)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusOK, nil)
	// r2-outbound-web#2: the key is never taken from the query string.
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status?apikey="+ts.key, nil, noKey), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil, withHeader("X-Api-Key", "wrong-key-0000000000")), http.StatusUnauthorized, nil)
}

func TestURLBase(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.UrlBase = "/dupearr" }
	})
	tests := []struct {
		target   string
		status   int
		location string
	}{
		{"/ping", http.StatusTemporaryRedirect, "/dupearr/ping"},
		{"/api/v1/system/status?page=2", http.StatusTemporaryRedirect, "/dupearr/api/v1/system/status?page=2"},
		{"/", http.StatusTemporaryRedirect, "/dupearr/"},
		{"/dupearrx", http.StatusTemporaryRedirect, "/dupearr/dupearrx"},
		{"/dupearr/ping", http.StatusOK, ""},
		{"/dupearr/api/v1/system/status", http.StatusOK, ""},
		{"/dupearr", http.StatusOK, ""},
		{"/dupearr/settings/general", http.StatusOK, ""},
	}
	for _, tt := range tests {
		rr := ts.do(http.MethodGet, tt.target, nil)
		if rr.Code != tt.status {
			t.Errorf("%s: status %d, want %d", tt.target, rr.Code, tt.status)
			continue
		}
		if tt.location != "" && rr.Header().Get("Location") != tt.location {
			t.Errorf("%s: Location %q, want %q", tt.target, rr.Header().Get("Location"), tt.location)
		}
	}
	// A POST keeps its method through the 307.
	if rr := ts.do(http.MethodPost, "/api/v1/command", map[string]string{"name": "CheckHealth"}); rr.Code != http.StatusTemporaryRedirect {
		t.Errorf("POST outside base = %d", rr.Code)
	}
	rr := ts.do(http.MethodGet, "/dupearr/", nil)
	if !strings.Contains(rr.Body.String(), `<base href="/dupearr/"><script>window.Dupearr={urlBase:"/dupearr"}</script>`) {
		t.Errorf("index not injected with the url base: %s", rr.Body.String())
	}
	var init initializeResponse
	expect(t, ts.do(http.MethodGet, "/dupearr/initialize.json", nil), http.StatusOK, &init)
	if init.APIRoot != "/dupearr/api/v1" || init.URLBase != "/dupearr" {
		t.Errorf("initialize = %+v", init)
	}
	// Logging out works inside the base (POST only).
	if rr := ts.do(http.MethodGet, "/dupearr/logout", nil, noKey); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET logout = %d, want 405", rr.Code)
	}
	expect(t, ts.do(http.MethodPost, "/dupearr/logout", nil, noKey), http.StatusOK, nil)
}

func TestSPA(t *testing.T) {
	ts := newTestServer(t)
	for _, target := range []string{"/", "/duplicates/12", "/login", "/.gitkeep", "/index.html"} {
		rr := ts.do(http.MethodGet, target, nil, noKey)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `<base href="/"><script>window.Dupearr={urlBase:""}</script>`) {
			t.Errorf("%s: %d %s", target, rr.Code, rr.Body.String())
		}
		if cc := rr.Header().Get("Cache-Control"); cc != cacheNone {
			t.Errorf("%s: Cache-Control %q", target, cc)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type %q", target, ct)
		}
	}
	rr := ts.do(http.MethodGet, "/assets/index-abc.js", nil, noKey)
	if rr.Code != http.StatusOK || rr.Body.String() != "console.log('dupearr')" {
		t.Fatalf("asset: %d %q", rr.Code, rr.Body.String())
	}
	if cc := rr.Header().Get("Cache-Control"); cc != cacheImmutable {
		t.Errorf("asset Cache-Control = %q", cc)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset Content-Type = %q", ct)
	}
	if cc := ts.do(http.MethodGet, "/favicon.svg", nil, noKey).Header().Get("Cache-Control"); cc != cacheShort {
		t.Errorf("favicon Cache-Control = %q", cc)
	}
	for _, missing := range []string{"/assets/missing.js", "/missing.css", "/assets/"} {
		if rr := ts.do(http.MethodGet, missing, nil, noKey); rr.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", missing, rr.Code)
		}
	}
	if rr := ts.do(http.MethodPost, "/settings", "{}", noKey); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST SPA route = %d", rr.Code)
	}

	bare := newTestServer(t, func(o *serverOpts) { o.noWeb = true })
	rr = bare.do(http.MethodGet, "/", nil, noKey)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "make web") {
		t.Fatalf("not-built page: %d %s", rr.Code, rr.Body.String())
	}
}

func TestInjectHeadEscapes(t *testing.T) {
	out := string(injectHead([]byte(testIndex), `/a"</script><img src=x>`))
	if strings.Contains(out, `</script><img`) {
		t.Fatalf("script context not escaped: %s", out)
	}
	if !strings.Contains(out, `<base href="/a&#34;&lt;/script&gt;&lt;img src=x&gt;/">`) {
		t.Fatalf("attribute not escaped: %s", out)
	}
	start := strings.Index(out, "window.Dupearr={urlBase:")
	end := strings.Index(out, "}</script>")
	if start < 0 || end < start {
		t.Fatalf("script not injected: %s", out)
	}
	lit := out[start+len("window.Dupearr={urlBase:") : end]
	if strings.ContainsAny(lit, "<>") {
		t.Fatalf("JS string not escaped: %s", lit)
	}
	var decoded string
	if err := json.Unmarshal([]byte(lit), &decoded); err != nil || decoded != `/a"</script><img src=x>` {
		t.Fatalf("JS string = %s (%q, %v)", lit, decoded, err)
	}
	noPlaceholder := string(injectHead([]byte("<html><head><title>x</title></head></html>"), ""))
	if !strings.HasPrefix(noPlaceholder, `<html><head><base href="/">`) {
		t.Fatalf("fallback injection: %s", noPlaceholder)
	}
}

func TestAPIFallback(t *testing.T) {
	ts := newTestServer(t)
	if msg := message(t, ts.do(http.MethodGet, "/api/v1/nope", nil), http.StatusNotFound); msg != "Not found" {
		t.Errorf("message = %q", msg)
	}
	rr := ts.do(http.MethodPut, "/api/v1/system/status", "{}")
	if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != "GET" {
		t.Errorf("PUT status: %d Allow=%q", rr.Code, rr.Header().Get("Allow"))
	}
	rr = ts.do(http.MethodPatch, "/api/v1/profile/1", "{}")
	if rr.Code != http.StatusMethodNotAllowed || !strings.Contains(rr.Header().Get("Allow"), "PUT") {
		t.Errorf("PATCH profile: %d Allow=%q", rr.Code, rr.Header().Get("Allow"))
	}
	if rr := ts.do(http.MethodGet, "/api/v1/profile/abc", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", rr.Code)
	}
}

func TestRecoverPanics(t *testing.T) {
	ts := newTestServer(t)
	h := ts.srv.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: secret stack detail")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if msg := message(t, rr, http.StatusInternalServerError); msg != "Internal server error" {
		t.Fatalf("message = %q", msg)
	}
	if strings.Contains(rr.Body.String(), "secret") || strings.Contains(rr.Body.String(), "goroutine") {
		t.Fatalf("panic details leaked: %s", rr.Body.String())
	}
}

func TestNonNil(t *testing.T) {
	type inner struct {
		List []string           `json:"list"`
		Map  map[string]int     `json:"map"`
		Raw  json.RawMessage    `json:"raw,omitempty"`
		When time.Time          `json:"when"`
		Opt  *int64             `json:"opt"`
		Any  any                `json:"any"`
		Nest []models.Criterion `json:"nest"`
	}
	type outer struct {
		Inner    inner    `json:"inner"`
		Ptr      *inner   `json:"ptr"`
		Items    []inner  `json:"items"`
		Skip     []string `json:"-"`
		hidden   []string //nolint:unused // unexported fields are ignored
		Embedded []models.GroupFile
	}
	in := outer{Items: []inner{{}}, Ptr: &inner{}, Embedded: []models.GroupFile{{}}}
	in.Inner.Any = inner{}
	b, err := json.Marshal(nonNil(in))
	if err != nil {
		t.Fatal(err)
	}
	assertNoNull(t, b, "opt", "any")
	if in.Items[0].List != nil || in.Ptr.Map != nil {
		t.Fatal("nonNil modified its input")
	}
	if !strings.Contains(string(b), `"any":{"list":[]`) {
		t.Errorf("interface value not normalised: %s", b)
	}
	if strings.Contains(string(b), `"raw"`) {
		t.Errorf("empty RawMessage should stay omitted: %s", b)
	}
	b, _ = json.Marshal(nonNil([]models.Profile(nil)))
	if string(b) != "[]" {
		t.Errorf("nil slice = %s", b)
	}
	b, _ = json.Marshal(nonNil(map[string][]int(nil)))
	if string(b) != "{}" {
		t.Errorf("nil map = %s", b)
	}
	if nonNil(nil) != nil {
		t.Error("nonNil(nil) != nil")
	}
	if v := nonNil(42); v != 42 {
		t.Error("scalar changed")
	}
}

func TestJSONNeverNull(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	ts.seedLibrary(srv.ID, "1", "Movies")
	ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	for _, target := range []string{
		"/api/v1/profile", "/api/v1/profile/schema", "/api/v1/duplicate", "/api/v1/duplicate/stats",
		"/api/v1/duplicate/1", "/api/v1/health", "/api/v1/command", "/api/v1/exclusion",
		"/api/v1/mediaserver", "/api/v1/arr", "/api/v1/tautulli", "/api/v1/pathmapping", "/api/v1/notification",
		"/api/v1/notification/schema", "/api/v1/notification/triggers", "/api/v1/queue", "/api/v1/action",
		"/api/v1/history", "/api/v1/scan", "/api/v1/system/task", "/api/v1/log", "/api/v1/log/file",
		"/api/v1/library", "/api/v1/config/host", "/api/v1/config/settings", "/api/v1/system/status",
		"/api/v1/system/backup", "/initialize.json", "/api/v1/auth/status",
	} {
		rr := ts.do(http.MethodGet, target, nil)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: %d %s", target, rr.Code, rr.Body.String())
			continue
		}
		assertNoNull(t, rr.Body.Bytes(), "profileId", "lastScan")
	}
}

func TestInitializeAndAuthStatus(t *testing.T) {
	ts := newTestServer(t)
	var st authStatusResponse
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil, noKey), http.StatusOK, &st)
	if !st.SetupRequired || st.Authenticated || st.AuthenticationMethod != config.AuthForms {
		t.Fatalf("status = %+v", st)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil), http.StatusOK, &st)
	if !st.Authenticated {
		t.Fatal("API key request not reported as authenticated")
	}
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey), http.StatusUnauthorized, nil)

	ts.createUser("admin", "pw123456")
	cookie := ts.login("admin", "pw123456")
	var init initializeResponse
	rr := ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(cookie))
	expect(t, rr, http.StatusOK, &init)
	if init.APIRoot != "/api/v1" || init.URLBase != "" || init.InstanceName != "Dupearr" ||
		init.AuthenticationMethod != config.AuthForms || init.Version == "" {
		t.Fatalf("initialize = %+v", init)
	}
	// r2-outbound-web#3: a browser session is never handed the master API key.
	if strings.Contains(rr.Body.String(), ts.key) || strings.Contains(rr.Body.String(), "apiKey") {
		t.Fatalf("initialize.json carries the API key: %s", rr.Body.String())
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil, noKey, withCookie(cookie)), http.StatusOK, &st)
	if st.SetupRequired || !st.Authenticated {
		t.Fatalf("status after login = %+v", st)
	}
}

func TestSetupFlow(t *testing.T) {
	ts := newTestServer(t)
	code := ts.auth.SetupCode()
	body := map[string]string{
		"authenticationMethod": "Forms", "authenticationRequired": "DisabledForLocalAddresses",
		"username": "admin", "password": "first-password", "passwordConfirmation": "first-password",
		"setupCode": code,
	}
	// Local clients must address the server by IP / a private host name (DNS-rebinding guard).
	const setupURL = "http://192.168.1.10:3873/api/v1/auth/setup"
	if rr := ts.do(http.MethodPost, setupURL, body, noKey); rr.Code != http.StatusForbidden {
		t.Fatalf("remote setup = %d, want 403", rr.Code)
	}
	local := withRemote("192.168.1.50:5000")
	// DNS rebinding: a local browser running an attacker's page that resolves to this server.
	// The page is same-origin with itself, so only the host check stops it.
	rebind := "http://rebind.attacker.example:3873/api/v1/auth/setup"
	if rr := ts.do(http.MethodPost, rebind, body, noKey, local, withHeader("Origin", "http://rebind.attacker.example:3873")); rr.Code != http.StatusForbidden {
		t.Fatalf("DNS-rebinding setup = %d, want 403", rr.Code)
	}
	if !ts.auth.SetupRequired(context.Background()) {
		t.Fatal("a refused setup created the account")
	}
	bad := map[string]string{"username": "admin", "password": "a", "passwordConfirmation": "b", "setupCode": code}
	if props := validationProps(t, ts.do(http.MethodPost, setupURL, bad, noKey, local)); !hasProp(props, "passwordConfirmation") {
		t.Fatalf("props = %v", props)
	}
	if props := validationProps(t, ts.do(http.MethodPost, setupURL, map[string]string{"username": "", "setupCode": code}, noKey, local)); !hasProp(props, "username") || !hasProp(props, "password") {
		t.Fatalf("props = %v", props)
	}
	if rr := ts.do(http.MethodPost, setupURL, body, noKey, local, withHeader("Origin", "http://evil.example")); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site setup = %d, want 403", rr.Code)
	}
	expect(t, ts.do(http.MethodPost, setupURL, body, noKey, local), http.StatusOK, nil)
	if c := ts.cfg.Get(); c.AuthenticationRequired != config.AuthRequiredDisabledForLocal {
		t.Fatalf("required = %s", c.AuthenticationRequired)
	}
	// The local bypass is now on, but not for a rebinding host name.
	expect(t, ts.do(http.MethodGet, "http://192.168.1.10:3873/initialize.json", nil, noKey, local), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "http://rebind.attacker.example:3873/initialize.json", nil, noKey, local), http.StatusUnauthorized, nil)
	if rr := ts.do(http.MethodPost, setupURL, body, noKey, local); rr.Code != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", rr.Code)
	}
	var st authStatusResponse
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil, noKey), http.StatusOK, &st)
	if st.SetupRequired {
		t.Fatal("setup still required")
	}
	ts.login("admin", "first-password")
}

func TestLogin(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")

	c := ts.login("admin", "secret-pw")
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	// rememberMe as a string ("on") gives a persistent cookie.
	rr := ts.do(http.MethodPost, "/login", map[string]any{"username": "admin", "password": "secret-pw", "rememberMe": "on"}, noKey)
	expect(t, rr, http.StatusOK, nil)
	if rc := sessionCookie(rr); rc == nil || rc.MaxAge <= 0 {
		t.Fatalf("remember cookie = %+v", rr.Result().Cookies())
	}
	if msg := message(t, ts.do(http.MethodPost, "/login", map[string]any{"username": "admin", "password": "nope"}, noKey), http.StatusUnauthorized); msg != "Invalid username or password" {
		t.Fatalf("message = %q", msg)
	}
	if rr := ts.do(http.MethodPost, "/login", "{not json", noKey); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", rr.Code)
	}

	// HTML form posts redirect like Servarr.
	form := func(values url.Values, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		ts.h.ServeHTTP(rr, req)
		return rr
	}
	rr = form(url.Values{"username": {"admin"}, "password": {"secret-pw"}, "rememberMe": {"on"}, "returnUrl": {"/duplicates?x=1"}}, testRemote)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/duplicates?x=1" || sessionCookie(rr) == nil {
		t.Fatalf("form login: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	rr = form(url.Values{"username": {"admin"}, "password": {"bad"}, "returnUrl": {"//evil.example"}}, testRemote)
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(rr.Header().Get("Location"), "/login?loginFailed=true") {
		t.Fatalf("failed form login: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	rr = form(url.Values{"username": {"admin"}, "password": {"secret-pw"}, "returnUrl": {"//evil.example/x"}}, testRemote)
	if rr.Header().Get("Location") != "/" {
		t.Fatalf("open redirect: %q", rr.Header().Get("Location"))
	}

	// Brute force: after 5 failures the client must back off (429 + Retry-After).
	const attacker = "198.51.100.77:1"
	for i := 0; i < 5; i++ {
		form(url.Values{"username": {"admin"}, "password": {"guess"}}, attacker)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"secret-pw"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = attacker
	rr = httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("throttled login = %d Retry-After=%q", rr.Code, rr.Header().Get("Retry-After"))
	}
}

func TestSafeReturnURL(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) { o.configure = func(c *config.Config) { c.UrlBase = "/d" } })
	for in, want := range map[string]string{
		"":                  "/d/",
		"/duplicates":       "/d/duplicates",
		"/d/duplicates":     "/d/duplicates",
		"//evil.example":    "/d/",
		"/\\evil.example":   "/d/",
		"https://evil.test": "/d/",
		"relative":          "/d/",
		"/a\r\nSet-Cookie":  "/d/",
	} {
		if got := ts.srv.safeReturnURL(in); got != want {
			t.Errorf("safeReturnURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogout(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "pw")
	c := ts.login("admin", "pw")
	// GET /logout would let any page sign the user out (<img src=…/logout>): refused.
	rr := ts.do(http.MethodGet, "/logout", nil, noKey, withCookie(c))
	if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != http.MethodPost || len(rr.Result().Cookies()) != 0 {
		t.Fatalf("GET logout: %d %v", rr.Code, rr.Result().Cookies())
	}
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(c)), http.StatusOK, nil)
	// Cross-site POST: refused.
	if rr := ts.do(http.MethodPost, "http://dupearr.local/logout", nil, noKey, withCookie(c), withHeader("Origin", "http://evil.example")); rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout = %d, want 403", rr.Code)
	}
	rr = ts.do(http.MethodPost, "http://dupearr.local/logout", nil, noKey, withCookie(c), withHeader("Origin", "http://dupearr.local"))
	expect(t, rr, http.StatusOK, nil)
	if cleared := sessionCookie(rr); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout cookie = %+v", rr.Result().Cookies())
	}
	// The session is revoked: a copy of the cookie no longer works.
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(c)), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodPost, "/logout", nil, noKey), http.StatusOK, nil)
}

// sessionCookie returns the (unprefixed) session cookie a response sets.
func sessionCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

func TestCookieCSRF(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "pw")
	c := ts.login("admin", "pw")
	if rr := ts.do(http.MethodPost, "/api/v1/health/check", nil, noKey, withCookie(c)); rr.Code != http.StatusForbidden {
		t.Fatalf("cookie POST without Origin = %d, want 403", rr.Code)
	}
	if rr := ts.do(http.MethodPost, "http://example.com/api/v1/health/check", nil, noKey, withCookie(c), withHeader("Origin", "http://example.com")); rr.Code != http.StatusCreated {
		t.Fatalf("same-origin cookie POST = %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/health", nil, noKey, withCookie(c)), http.StatusOK, nil)
}

func TestSystemStatus(t *testing.T) {
	ts := newTestServer(t)
	var st systemStatus
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusOK, &st)
	if st.AppName != "Dupearr" || !st.IsDocker || st.Authentication != config.AuthForms || !st.DryRun ||
		st.Mode != models.ModeManual || st.DatabaseFile == "" || st.DatabaseSize <= 0 || st.UptimeSeconds < 59 ||
		st.DataDirectory != ts.cfg.DataDir() || st.ConfigFile != ts.cfg.Path() || st.GoVersion == "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestRestart(t *testing.T) {
	ts := newTestServer(t)
	expect(t, ts.do(http.MethodPost, "/api/v1/system/restart", nil), http.StatusAccepted, nil)
	deadline := time.Now().Add(2 * time.Second)
	for ts.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ts.restarts.Load() != 1 {
		t.Fatal("restart not requested")
	}
}

func TestLogs(t *testing.T) {
	ts := newTestServer(t)
	logger, mgr, err := logging.Setup(filepath.Join(ts.dir, "logs"), "info", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	ts.srv.d.Logs = mgr
	logger.Warn("first entry")
	logger.Info("second entry")

	var page store.Page[logging.Entry]
	expect(t, ts.do(http.MethodGet, "/api/v1/log?pageSize=1", nil), http.StatusOK, &page)
	if page.TotalRecords < 2 || len(page.Records) != 1 || page.Records[0].Message != "second entry" {
		t.Fatalf("page = %+v", page)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/log?level=warn", nil), http.StatusOK, &page)
	for _, e := range page.Records {
		if e.Message == "second entry" {
			t.Fatal("level filter ignored")
		}
	}
	for _, q := range []string{"level=loud", "page=x", "pageSize=y", "sortDirection=sideways"} {
		if rr := ts.do(http.MethodGet, "/api/v1/log?"+q, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rr.Code)
		}
	}
	var files []logging.LogFile
	expect(t, ts.do(http.MethodGet, "/api/v1/log/file", nil), http.StatusOK, &files)
	if len(files) == 0 || files[0].Filename != logging.FileName {
		t.Fatalf("files = %+v", files)
	}
	rr := ts.do(http.MethodGet, "/api/v1/log/file/"+logging.FileName, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "first entry") ||
		!strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("log file: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	for _, bad := range []string{"..%2Fconfig.xml", "config.xml", "missing.txt"} {
		if rr := ts.do(http.MethodGet, "/api/v1/log/file/"+bad, nil); rr.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, rr.Code)
		}
	}
}

func TestCommands(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(http.MethodPost, "/api/v1/command", map[string]any{"name": "DuplicateScan", "libraryIds": []int{1}, "trigger": "scheduled"})
	var cmd models.Command
	expect(t, rr, http.StatusCreated, &cmd)
	if cmd.Name != models.CmdDuplicateScan || cmd.Trigger != models.TriggerManual || cmd.ID == 0 ||
		rr.Header().Get("Location") == "" || string(cmd.Body) != `{"libraryIds":[1]}` {
		t.Fatalf("command = %+v (%s)", cmd, rr.Header().Get("Location"))
	}
	// Identical commands are deduplicated while queued.
	var again models.Command
	expect(t, ts.do(http.MethodPost, "/api/v1/command", map[string]any{"name": "duplicatescan", "libraryIds": []int{1}}), http.StatusCreated, &again)
	if again.ID != cmd.ID {
		t.Fatalf("duplicate command queued: %d vs %d", again.ID, cmd.ID)
	}
	tests := []struct {
		body any
		prop string
	}{
		{map[string]any{}, "name"},
		{map[string]any{"name": 5}, "name"},
		{map[string]any{"name": "RefreshSeries"}, "name"},
		{map[string]any{"name": "TargetedScan"}, "ratingKeys"},
		{map[string]any{"name": "TargetedScan", "ratingKeys": []string{"1/2"}}, "ratingKeys"},
		{map[string]any{"name": "TargetedScan", "imdbId": "nm123"}, "imdbId"},
		{map[string]any{"name": "DuplicateScan", "libraryIds": []int{0}}, "libraryIds"},
		{map[string]any{"name": "Backup", "type": "weekly"}, "type"},
	}
	for _, tt := range tests {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/command", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}
	if rr := ts.do(http.MethodPost, "/api/v1/command", map[string]any{"name": "DuplicateScan", "libraryIds": "x"}); rr.Code != http.StatusBadRequest {
		t.Errorf("bad body type = %d", rr.Code)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/command", map[string]any{"name": "TargetedScan", "imdbId": "TT0133093"}), http.StatusCreated, &cmd)
	if !strings.Contains(string(cmd.Body), `"imdbId":"tt0133093"`) {
		t.Errorf("targeted body = %s", cmd.Body)
	}
	var list []models.Command
	expect(t, ts.do(http.MethodGet, "/api/v1/command", nil), http.StatusOK, &list)
	if len(list) != 2 {
		t.Fatalf("list = %d commands", len(list))
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/command/"+itoa64(cmd.ID), nil), http.StatusOK, &again)
	if again.ID != cmd.ID {
		t.Fatalf("get = %+v", again)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/command/9999", nil), http.StatusNotFound, nil)
}

func TestHealthAndTasks(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(http.MethodGet, "/api/v1/health", nil)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("health = %d %s", rr.Code, rr.Body.String())
	}
	var cmd models.Command
	expect(t, ts.do(http.MethodPost, "/api/v1/health/check", nil), http.StatusCreated, &cmd)
	if cmd.Name != models.CmdCheckHealth {
		t.Fatalf("command = %+v", cmd)
	}
	rr = ts.do(http.MethodGet, "/api/v1/system/task", nil)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("tasks = %d %s", rr.Code, rr.Body.String())
	}
}

func TestAuthCookieNamePublic(t *testing.T) {
	if auth.CookieName != "DupearrAuth" {
		t.Fatalf("cookie name = %s", auth.CookieName)
	}
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
