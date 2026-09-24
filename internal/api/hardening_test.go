package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// SEC-001: External authentication and DNS rebinding
// ---------------------------------------------------------------------------

func TestExternalMethodDNSRebinding(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal }
	})
	const evil = "http://rebind.attacker.example:3873"
	lan := withRemote("192.168.1.20:50000")
	rr := ts.do(http.MethodGet, evil+"/initialize.json", nil, noKey, lan)
	if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), ts.key) {
		t.Fatalf("initialize.json via a rebinding host = %d %s, want 401 without the key", rr.Code, rr.Body.String())
	}
	rr = ts.do(http.MethodPost, evil+"/api/v1/system/backup", nil, noKey, lan, withHeader("Origin", evil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("backup via a rebinding host = %d, want 401", rr.Code)
	}
	if list, _ := ts.backups.List(); len(list) != 0 {
		t.Fatal("a backup was created through a rebinding host")
	}
	// Through the (trusted) proxy it works.
	ts.auth.SetNetworkTrust(func() (string, string) { return "172.18.0.9", "" })
	expect(t, ts.do(http.MethodGet, "http://dupearr.example.com/initialize.json", nil, noKey, withRemote("172.18.0.9:1")), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "http://192.168.1.10:3873/initialize.json", nil, noKey, lan), http.StatusUnauthorized, nil)
}

// ---------------------------------------------------------------------------
// SEC-003: first-run setup needs the setup code
// ---------------------------------------------------------------------------

func TestSetupRequiresSetupCode(t *testing.T) {
	ts := newTestServer(t)
	const setupURL = "http://127.0.0.1:3873/api/v1/auth/setup"
	loopback := withRemote("127.0.0.1:5000")
	body := map[string]string{"username": "admin", "password": "first-password", "passwordConfirmation": "first-password"}
	for _, code := range []string{"", "ABCDE-FGHJK-LMNPQ-RSTUV"} {
		body["setupCode"] = code
		if rr := ts.do(http.MethodPost, setupURL, body, noKey, loopback); rr.Code != http.StatusForbidden {
			t.Fatalf("setup with code %q = %d, want 403", code, rr.Code)
		}
	}
	// Even a request that looks local in every way (a remote client relayed with a local peer and
	// a local Host) cannot claim the account without it.
	if !ts.auth.SetupRequired(context.Background()) {
		t.Fatal("setup without the code created the account")
	}
	body["setupCode"] = strings.ToLower(ts.auth.SetupCode())
	expect(t, ts.do(http.MethodPost, setupURL, body, noKey, loopback), http.StatusOK, nil)
}

// ---------------------------------------------------------------------------
// SEC-004: API key in the query string, JSON content type
// ---------------------------------------------------------------------------

func TestQueryAPIKeyCannotDriveWrites(t *testing.T) {
	ts := newTestServer(t)
	// A cross-site text/plain form posting to ?apikey=<leaked key>.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/command?apikey="+ts.key, strings.NewReader(`{"name":"CheckHealth"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "http://evil.example")
	req.RemoteAddr = testRemote
	rr := httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("POST with ?apikey = %d, want 401", rr.Code)
	}
	// With the header, a non-JSON body is refused (415).
	rr = ts.do(http.MethodPost, "/api/v1/command", `{"name":"CheckHealth"}`, withHeader("Content-Type", "text/plain;charset=UTF-8"))
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain body = %d, want 415", rr.Code)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/command", `{"name":"CheckHealth"}`, withHeader("Content-Type", "application/json; charset=utf-8")), http.StatusCreated, nil)
	// Reads refuse the query key too (r2-outbound-web#2: proxy logs, browser history).
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status?apikey="+ts.key, nil, noKey), http.StatusUnauthorized, nil)
}

func TestDecodeJSONContentType(t *testing.T) {
	decode := func(ct, body string) error {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		var v map[string]any
		return decodeJSON(req, &v)
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/merge-patch+json"} {
		if err := decode(ct, `{}`); err != nil {
			t.Errorf("%s: %v", ct, err)
		}
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "garbage/"} {
		err := decode(ct, `{}`)
		if ae, ok := err.(*apiError); !ok || ae.status != http.StatusUnsupportedMediaType {
			t.Errorf("%q: %v, want 415", ct, err)
		}
	}
	// An empty body stays "empty" (optional bodies), whatever the content type.
	if err := decode("", ""); !isEmptyBody(err) {
		t.Errorf("empty body without content type: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SEC-005: webhook token
// ---------------------------------------------------------------------------

func TestWebhookToken(t *testing.T) {
	ts := newTestServer(t)
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	tok := h.WebhookToken
	if tok == "" || tok == ts.key || tok != ts.auth.WebhookToken() {
		t.Fatalf("webhookToken = %q", tok)
	}
	test := map[string]any{"eventType": "Test", "instanceName": "Radarr"}
	var res webhookResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/webhook/radarr?apikey="+tok, test, noKey), http.StatusOK, &res)
	expect(t, ts.do(http.MethodPost, "/api/v1/webhook/sonarr", test, noKey, withHeader("X-Api-Key", tok)), http.StatusOK, nil)
	// It is useless anywhere else.
	for _, tt := range []struct{ method, target string }{
		{http.MethodGet, "/initialize.json?apikey=" + tok},
		{http.MethodGet, "/api/v1/config/host?apikey=" + tok},
		{http.MethodPost, "/api/v1/system/backup"},
		{http.MethodPut, "/api/v1/config/settings"},
	} {
		rr := ts.do(tt.method, tt.target, map[string]any{}, noKey, withHeader("X-Api-Key", tok))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the webhook token = %d, want 401", tt.method, tt.target, rr.Code)
		}
	}
	// Regenerating replaces it.
	var h2 hostConfig
	expect(t, ts.do(http.MethodPost, "/api/v1/config/host/webhooktoken", nil), http.StatusOK, &h2)
	if h2.WebhookToken == tok || h2.WebhookToken == "" {
		t.Fatalf("regenerated token = %q", h2.WebhookToken)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/webhook/radarr?apikey="+tok, test, noKey), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/webhook/radarr?apikey="+h2.WebhookToken, test, noKey), http.StatusOK, nil)
}

// ---------------------------------------------------------------------------
// SEC-009: request bodies cannot be withheld indefinitely
// ---------------------------------------------------------------------------

func TestWithheldBodyDoesNotHoldConnections(t *testing.T) {
	// A long deadline: requests answered without reading their body must not wait for it at all.
	old := bodyReadTimeout
	bodyReadTimeout = 10 * time.Second
	t.Cleanup(func() { bodyReadTimeout = old })

	ts := newTestServer(t)
	srv := httptest.NewServer(ts.h)
	t.Cleanup(srv.Close)
	for _, req := range []string{
		"POST /api/v1/command HTTP/1.1", // protected: refused by the auth middleware unread
		"PUT /api/v1/config/settings HTTP/1.1",
		"POST /api/v1/webhook/radarr HTTP/1.1",
		"GET /ping HTTP/1.1", // public, never reads a body
		"GET / HTTP/1.1",
	} {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "%s\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n{", req)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		resp, rerr := http.ReadResponse(bufio.NewReader(c), nil)
		if rerr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		// The connection must be closed too, not kept waiting for the body.
		_, _ = io.Copy(io.Discard, c)
		_ = c.Close()
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("%s: a withheld body held the connection for %v (response error %v)", req, elapsed, rerr)
		}
	}
	// A handler that does read the body still gets it after a pause shorter than the deadline.
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	body := `{"name":"CheckHealth"}`
	fmt.Fprintf(c, "POST /api/v1/command HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Api-Key: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", ts.key, len(body), body[:5])
	time.Sleep(300 * time.Millisecond)
	fmt.Fprint(c, body[5:])
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("slow but complete body: %v %v", resp, err)
	}
}

// With the real deadline elapsed, a stalled body ends the request.
func TestStalledBodyTimesOut(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	ts := newTestServer(t)
	srv := httptest.NewServer(ts.h)
	t.Cleanup(srv.Close)
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Authenticated, so the handler reads (and waits for) the body.
	fmt.Fprintf(c, "POST /api/v1/command HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Api-Key: %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{", ts.key)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err == nil {
		_ = resp.Body.Close()
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("a stalled body held the request for %v", elapsed)
	}
}

// A large authenticated upload may take longer than the body deadline as long as it progresses.
func TestSlowBackupUploadStillWorks(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })

	ts := newTestServer(t)
	srv := httptest.NewServer(ts.h)
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "backup.zip")
	_, _ = fw.Write(append([]byte("PK\x03\x04"), bytes.Repeat([]byte("z"), 4096)...))
	_ = mw.Close()
	data := buf.Bytes()

	pr, pw := io.Pipe()
	go func() {
		const parts = 6
		step := len(data)/parts + 1
		for i := 0; i < len(data); i += step {
			end := min(i+step, len(data))
			if _, err := pw.Write(data[i:end]); err != nil {
				return
			}
			time.Sleep(150 * time.Millisecond) // 6 × 150 ms > bodyReadTimeout
		}
		_ = pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/system/backup/restore/upload", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Api-Key", ts.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("slow upload = %d %s", resp.StatusCode, b)
	}
}

// ---------------------------------------------------------------------------
// SEC-010: unauthenticated login/setup bodies are small
// ---------------------------------------------------------------------------

func TestLoginRejectsHugeBodies(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	huge := strings.Repeat("u", 1<<20)
	rr := ts.do(http.MethodPost, "/login", map[string]string{"username": huge, "password": "x"}, noKey)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB JSON login = %d, want 413", rr.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"username": {huge}, "password": {"x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = testRemote
	rr = httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("1 MiB form login = %d, want 400", rr.Code)
	}
	body := map[string]string{"username": huge, "password": "first-password", "passwordConfirmation": "first-password", "setupCode": ts.auth.SetupCode()}
	ts2 := newTestServer(t)
	body["setupCode"] = ts2.auth.SetupCode()
	if rr := ts2.do(http.MethodPost, "http://127.0.0.1/api/v1/auth/setup", body, noKey, withRemote("127.0.0.1:1")); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB setup = %d, want 413", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// SEC-012: sessions and live-update streams end when credentials change
// ---------------------------------------------------------------------------

// waitClosed waits until the SSE stream ends.
func (r *sseReader) waitClosed(within time.Duration) bool {
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-r.lines:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestEventStreamEndsWhenTheAPIKeyIsRegenerated(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.sseKeepAlive = time.Hour // only the credential change may end it
	srv := httptest.NewServer(ts.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("X-Api-Key", ts.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := newSSEReader(t, resp)
	r.next() // retry
	r.next() // version
	expect(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey", nil), http.StatusOK, nil)
	if !r.waitClosed(3 * time.Second) {
		t.Fatal("the stream outlived the API key regeneration")
	}
}

func TestEventStreamEndsOnLogout(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	cookie := ts.login("admin", "secret-pw")
	ts.srv.sseKeepAlive = time.Hour
	srv := httptest.NewServer(ts.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	r := newSSEReader(t, resp)
	r.next()
	expect(t, ts.do(http.MethodPost, "/logout", nil, noKey, withCookie(cookie)), http.StatusOK, nil)
	if !r.waitClosed(3 * time.Second) {
		t.Fatal("the stream outlived the logout")
	}
}

func TestRevokeAllSessions(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	mine, other := ts.login("admin", "secret-pw"), ts.login("admin", "secret-pw")
	rr := ts.do(http.MethodPost, "http://dupearr.local/api/v1/auth/sessions/revoke", nil, noKey, withCookie(mine),
		withHeader("Origin", "http://dupearr.local"))
	expect(t, rr, http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(other)), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(mine)), http.StatusUnauthorized, nil)
	fresh := sessionCookie(rr)
	if fresh == nil {
		t.Fatal("the caller's session was not re-issued")
	}
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(fresh)), http.StatusOK, nil)
	// Unauthenticated callers cannot use it.
	expect(t, ts.do(http.MethodPost, "/api/v1/auth/sessions/revoke", nil, noKey), http.StatusUnauthorized, nil)
}

// ---------------------------------------------------------------------------
// SEC-013: credential changes need the current password; the API key rotates
// ---------------------------------------------------------------------------

func TestCredentialChangesNeedCurrentPassword(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	cookie := ts.login("admin", "secret-pw")
	const base = "http://dupearr.local"
	origin := withHeader("Origin", base)
	put := func(body map[string]any, opts ...reqOption) *httptest.ResponseRecorder {
		return ts.do(http.MethodPut, base+"/api/v1/config/host", body, opts...)
	}
	attempt := 0
	for _, change := range []map[string]any{
		{"username": "intruder"},
		{"password": "new-password", "passwordConfirmation": "new-password"},
		{"authenticationMethod": "External"},
		{"authenticationRequired": "DisabledForLocalAddresses"},
		{"apiKey": strings.Repeat("a", 32)},
	} {
		// A browser session (cookie only, or the web UI's API key + cookie).
		for name, opts := range map[string][]reqOption{
			"cookie":            {noKey, withCookie(cookie), origin},
			"web UI key+cookie": {withCookie(cookie), origin},
		} {
			if props := validationProps(t, put(change, opts...)); !hasProp(props, "currentPassword") {
				t.Errorf("%s %v without currentPassword: props %v", name, change, props)
			}
		}
		wrong := map[string]any{"currentPassword": "not-it"}
		for k, v := range change {
			wrong[k] = v
		}
		// (from another address each time: the check is throttled like a login)
		attempt++
		if props := validationProps(t, put(wrong, noKey, withCookie(cookie), origin, withRemote(fmt.Sprintf("198.51.100.%d:1", attempt)))); !hasProp(props, "currentPassword") {
			t.Errorf("%v with a wrong currentPassword: props %v", change, props)
		}
	}
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if h.Username != "admin" || h.AuthenticationMethod != config.AuthForms || h.APIKey != ts.key {
		t.Fatalf("a refused change was saved: %+v", h)
	}
	// Other settings need no password.
	expect(t, put(map[string]any{"instanceName": "Mine"}, noKey, withCookie(cookie), origin), http.StatusAccepted, nil)
	// With the right password the change goes through.
	expect(t, put(map[string]any{"username": "root", "currentPassword": "secret-pw"}, noKey, withCookie(cookie), origin), http.StatusAccepted, &h)
	if h.Username != "root" {
		t.Fatalf("username = %q", h.Username)
	}
	// r2-outbound-web#3: the API key alone is not enough either (a leaked key must not lock the
	// owner out); scripts send currentPassword.
	if props := validationProps(t, put(map[string]any{"username": "admin"})); !hasProp(props, "currentPassword") {
		t.Errorf("API key without currentPassword: props %v", props)
	}
	expect(t, put(map[string]any{"username": "admin", "currentPassword": "secret-pw"}), http.StatusAccepted, nil)
}

func TestPasswordChangeRotatesTheAPIKey(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	oldKey := ts.key
	var h hostConfig
	expect(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]any{"password": "new-password-1", "passwordConfirmation": "new-password-1", "currentPassword": "secret-pw"}), http.StatusAccepted, &h)
	if h.APIKey == oldKey || len(h.APIKey) != 32 {
		t.Fatalf("API key after a password change = %q (old %q)", h.APIKey, oldKey)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusUnauthorized, nil)
	ts.key = h.APIKey
	// keepApiKey opts out.
	expect(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]any{"password": "new-password-2", "passwordConfirmation": "new-password-2", "keepApiKey": true, "currentPassword": "new-password-1"}), http.StatusAccepted, &h)
	if h.APIKey != ts.key {
		t.Fatal("keepApiKey did not keep the key")
	}
}

// ---------------------------------------------------------------------------
// SEC-014: Content-Security-Policy
// ---------------------------------------------------------------------------

func TestContentSecurityPolicy(t *testing.T) {
	ts := newTestServer(t)
	theme := `(function(){document.documentElement.dataset.x='1'})();`
	ts.webFS["index.html"].Data = []byte(`<!doctype html><html><head><!--DUPEARR_HEAD--><script>` + theme +
		`</script><script type="module" crossorigin src="/assets/index-abc.js"></script></head><body></body></html>`)
	rr := ts.do(http.MethodGet, "/", nil, noKey)
	csp := rr.Header().Get("Content-Security-Policy")
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	for _, want := range []string{
		"default-src 'self'", "object-src 'none'", "base-uri 'self'", "frame-ancestors 'self'", "form-action 'self'",
		hash(theme), hash(`window.Dupearr={urlBase:""}`),
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("index CSP %q lacks %s", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("index CSP allows inline code: %q", csp)
	}
	// Every other response forbids everything.
	for _, target := range []string{"/api/v1/system/status", "/initialize.json", "/assets/index-abc.js"} {
		if got := ts.do(http.MethodGet, target, nil).Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
			t.Errorf("%s CSP = %q", target, got)
		}
	}
	// The "not built" page gets a policy allowing its own inline style only.
	nb := newTestServer(t, func(o *serverOpts) { o.noWeb = true })
	csp = nb.do(http.MethodGet, "/", nil, noKey).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self' 'sha256-") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("not-built CSP = %q", csp)
	}
}

// ---------------------------------------------------------------------------
// SEC-018: a stored secret only goes back to the same endpoint, with TLS verification kept
// ---------------------------------------------------------------------------

// recordingArr answers like Radarr on any path prefix and records which paths received which key.
type recordingArr struct {
	mu   sync.Mutex
	hits []string // "path key"
}

func (f *recordingArr) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.Path+" "+r.Header.Get("X-Api-Key"))
		f.mu.Unlock()
		if r.Header.Get("X-Api-Key") != fakeArrKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v3/system/status"):
			_, _ = io.WriteString(w, `{"appName":"Radarr","instanceName":"Radarr","version":"5.9.0"}`)
		case strings.HasSuffix(r.URL.Path, "/api/v3/config/mediamanagement"):
			_, _ = io.WriteString(w, `{"recycleBin":"","recycleBinCleanupDays":7}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *recordingArr) sawKeyAt(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if strings.HasPrefix(h, prefix) && strings.HasSuffix(h, " "+fakeArrKey) {
			return true
		}
	}
	return false
}

func TestMaskedSecretStaysWithItsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	// Like production (cmd/dupearr): the client verifies certificates as the instance says.
	ts.srv.d.ArrFactory = func(a models.ArrInstance) *arr.Client {
		return arr.New(a, arr.Options{VerifyTLS: a.VerifyTLS, Timeout: 5 * time.Second})
	}
	rec := &recordingArr{}
	plain := httptest.NewServer(rec.handler())
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(rec.handler())
	t.Cleanup(secure.Close)

	var a models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", map[string]any{"name": "A", "kind": "radarr", "url": plain.URL + "/tenant-a", "apiKey": fakeArrKey}), http.StatusCreated, &a)
	// Same host and port, another path (a path-routing proxy): the stored key must not go there.
	rr := ts.do(http.MethodPost, "/api/v1/arr/test", map[string]any{"id": a.ID, "name": "A", "kind": "radarr", "url": plain.URL + "/tenant-b", "apiKey": maskedSecret})
	if props := validationProps(t, rr); !hasProp(props, "apiKey") {
		t.Fatalf("path change props = %v", props)
	}
	rr = ts.do(http.MethodPut, fmt.Sprintf("/api/v1/arr/%d", a.ID), map[string]any{"url": plain.URL + "/tenant-b", "apiKey": maskedSecret})
	if props := validationProps(t, rr); !hasProp(props, "apiKey") {
		t.Fatalf("path change on update props = %v", props)
	}
	if rec.sawKeyAt("/tenant-b") {
		t.Fatal("the stored key was sent to another path")
	}
	// Same path, trailing slash: still the same endpoint.
	expect(t, ts.do(http.MethodPost, "/api/v1/arr/test", map[string]any{"id": a.ID, "name": "A", "kind": "radarr", "url": plain.URL + "/tenant-a/", "apiKey": maskedSecret}), http.StatusOK, nil)

	// TLS: stored with verification on (force-saved: the test certificate is not trusted).
	var s models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr?forceSave=true", map[string]any{"name": "S", "kind": "radarr", "url": secure.URL, "apiKey": fakeArrKey, "verifyTls": true}), http.StatusCreated, &s)
	rr = ts.do(http.MethodPost, "/api/v1/arr/test", map[string]any{"id": s.ID, "name": "S", "kind": "radarr", "url": secure.URL, "apiKey": maskedSecret, "verifyTls": false})
	if props := validationProps(t, rr); !hasProp(props, "apiKey") {
		t.Fatalf("TLS downgrade props = %v", props)
	}
	if rec.sawKeyAt("/api/v3") {
		t.Fatal("the stored key was sent without certificate verification")
	}
	// Testing a new instance without verifyTls verifies the certificate (like create).
	rr = ts.do(http.MethodPost, "/api/v1/arr/test", map[string]any{"name": "N", "kind": "radarr", "url": secure.URL, "apiKey": fakeArrKey})
	if rr.Code == http.StatusOK || rec.sawKeyAt("/api/v3") {
		t.Fatalf("arr test with verifyTls omitted = %d (key delivered: %v), want a certificate error", rr.Code, rec.sawKeyAt("/api/v3"))
	}
}

func TestSameEndpoint(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"http://radarr:7878", "http://RADARR:7878/", true},
		{"http://radarr", "http://radarr:80", true},
		{"https://proxy/radarr", "https://proxy/radarr/", true},
		{"https://proxy/radarr", "https://proxy/sonarr", false},
		{"https://proxy/radarr", "https://proxy", false},
		{"http://radarr:7878", "https://radarr:7878", false},
		{"http://radarr:7878", "http://radarr:7879", false},
		{"", "", false},
	} {
		if got := sameEndpoint(tt.a, tt.b); got != tt.want {
			t.Errorf("sameEndpoint(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Re-review: regenerating the API key signs every other session out
// ---------------------------------------------------------------------------

// Whoever read the old key from a session could read the new one from initialize.json, so API
// key regeneration revokes every session except the caller's.
func TestAPIKeyRegenerationRevokesOtherSessions(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	mine, other := ts.login("admin", "secret-pw"), ts.login("admin", "secret-pw")
	rr := ts.do(http.MethodPost, "http://dupearr.local/api/v1/config/host/apikey", map[string]any{"currentPassword": "secret-pw"}, withCookie(mine),
		withHeader("Origin", "http://dupearr.local"))
	expect(t, rr, http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(other)), http.StatusUnauthorized, nil)
	fresh := sessionCookie(rr)
	if fresh == nil {
		t.Fatal("the caller's session was not re-issued")
	}
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(fresh)), http.StatusOK, nil)
}

// Once an account exists, the public setup route answers 409 to anyone, local or not (and so
// cannot be used to write warnings to the log).
func TestSetupAfterSetupIsConflictForEveryone(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	body := map[string]any{"username": "x", "password": "long-enough-pw", "passwordConfirmation": "long-enough-pw"}
	for _, remote := range []string{"203.0.113.9:4000", "127.0.0.1:4000"} {
		rr := ts.do(http.MethodPost, "http://127.0.0.1/api/v1/auth/setup", body, noKey, withRemote(remote))
		expect(t, rr, http.StatusConflict, nil)
	}
}
