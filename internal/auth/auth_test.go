package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/store"
)

func init() {
	// bcrypt at cost 12 takes ~0.3 s (much more under -race); the cost itself is not under test.
	bcryptCost = bcrypt.MinCost
}

const (
	remoteAddr = "203.0.113.7:51000"
	localAddr  = "192.168.1.20:51000"
	testUser   = "admin"
	testPass   = "correct horse battery staple"
)

type testEnv struct {
	t   *testing.T
	cfg *config.Manager
	db  *database.DB
	svc *Service

	mu  sync.Mutex
	now time.Time
}

func newTestEnv(t *testing.T, mutate func(c *config.Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if mutate != nil {
		if _, err := cfg.Update(mutate); err != nil {
			t.Fatalf("config.Update: %v", err)
		}
	}
	db, err := database.Open(context.Background(), filepath.Join(dir, "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := New(cfg, db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e := &testEnv{t: t, cfg: cfg, db: db, svc: svc, now: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	svc.now = e.clock
	return e
}

func (e *testEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

func (e *testEnv) createUser() {
	e.t.Helper()
	if _, err := e.svc.UpdateUser(context.Background(), testUser, testPass); err != nil {
		e.t.Fatalf("UpdateUser: %v", err)
	}
}

// login returns the session cookie of a successful login.
func (e *testEnv) login(remember bool) *http.Cookie {
	e.t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = remoteAddr
	if err := e.svc.Login(rr, req, testUser, testPass, remember); err != nil {
		e.t.Fatalf("Login: %v", err)
	}
	c := findCookie(rr.Result().Cookies())
	if c == nil {
		e.t.Fatal("Login set no session cookie")
	}
	return c
}

func findCookie(cs []*http.Cookie) *http.Cookie { return findNamedCookie(cs, CookieName) }

func findNamedCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// echo is the protected handler: it reports how the request was authenticated.
var echo = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Via", FromContext(r.Context()).Via)
	w.WriteHeader(http.StatusOK)
})

type reqOpts struct {
	method  string
	target  string
	remote  string
	headers map[string]string
	cookie  *http.Cookie
}

func (e *testEnv) do(o reqOpts) *httptest.ResponseRecorder {
	e.t.Helper()
	if o.method == "" {
		o.method = http.MethodGet
	}
	req := httptest.NewRequest(o.method, o.target, nil)
	req.RemoteAddr = o.remote
	if o.remote == "" {
		req.RemoteAddr = remoteAddr
	}
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	if o.cookie != nil {
		req.AddCookie(o.cookie)
	}
	rr := httptest.NewRecorder()
	e.svc.Middleware(echo).ServeHTTP(rr, req)
	return rr
}

func TestIsLocalAddress(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.10.0.1", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"169.254.10.1", true},
		{"fe80::1", true},
		{"fd12:3456::1", true},
		{"fc00::1", true},
		{"::ffff:192.168.1.1", true},
		{"::ffff:8.8.8.8", false},
		{"8.8.8.8", false},
		{"100.64.0.1", false}, // CGNAT is not local
		{"2001:db8::1", false},
		{"0.0.0.0", false},
	}
	for _, tt := range tests {
		if got := IsLocalAddress(net.ParseIP(tt.ip)); got != tt.want {
			t.Errorf("IsLocalAddress(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
	if IsLocalAddress(nil) {
		t.Error("IsLocalAddress(nil) = true")
	}
}

func TestClientIPAndIsLocalRequest(t *testing.T) {
	e := newTestEnv(t, nil)
	// No trusted proxies: forwarding headers never change the client address, and any header
	// naming a non-local (or unparsable) client makes the request non-local.
	tests := []struct {
		name      string
		remote    string
		xff       []string
		realIP    string
		forwarded string
		wantIP    string
		wantLocal bool
	}{
		{name: "remote peer", remote: remoteAddr, wantIP: "203.0.113.7"},
		{name: "local peer", remote: localAddr, wantIP: "192.168.1.20", wantLocal: true},
		{name: "loopback v6 peer", remote: "[::1]:1234", wantIP: "::1", wantLocal: true},
		{name: "remote peer ignores forwarded headers", remote: remoteAddr, xff: []string{"10.0.0.5"}, realIP: "10.0.0.5", wantIP: "203.0.113.7"},
		{name: "untrusted local peer forwarding a remote client", remote: localAddr, xff: []string{"198.51.100.9"}, wantIP: "192.168.1.20"},
		{name: "untrusted local peer forwarding a local client", remote: localAddr, xff: []string{"10.0.0.5"}, wantIP: "192.168.1.20", wantLocal: true},
		{name: "spoofed local entry before the real client", remote: localAddr, xff: []string{"10.0.0.5, 198.51.100.9"}, wantIP: "192.168.1.20"},
		{name: "remote entry left of proxies", remote: localAddr, xff: []string{"198.51.100.9, 10.0.0.2", "172.17.0.1"}, wantIP: "192.168.1.20"},
		{name: "unparsable forwarded entry", remote: localAddr, xff: []string{"garbage"}, wantIP: "192.168.1.20"},
		{name: "x-real-ip remote", remote: localAddr, realIP: "198.51.100.9", wantIP: "192.168.1.20"},
		{name: "x-real-ip local", remote: localAddr, realIP: "10.9.9.9", wantIP: "192.168.1.20", wantLocal: true},
		{name: "forwarded with port", remote: "127.0.0.1:9", xff: []string{"[2001:db8::5]:443"}, wantIP: "127.0.0.1"},
		{name: "rfc 7239 remote client", remote: "127.0.0.1:9", forwarded: "for=8.8.8.8;proto=https", wantIP: "127.0.0.1"},
		{name: "rfc 7239 quoted v6 remote client", remote: "127.0.0.1:9", forwarded: `for="[2001:db8:cafe::17]:4711"`, wantIP: "127.0.0.1"},
		{name: "rfc 7239 unknown client", remote: "127.0.0.1:9", forwarded: "for=unknown", wantIP: "127.0.0.1"},
		{name: "rfc 7239 element without for", remote: "127.0.0.1:9", forwarded: "proto=https", wantIP: "127.0.0.1"},
		{name: "rfc 7239 local client", remote: "127.0.0.1:9", forwarded: "for=192.168.1.5, for=10.0.0.1", wantIP: "127.0.0.1", wantLocal: true},
		{name: "unparsable peer", remote: "pipe", wantIP: "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if tt.realIP != "" {
				req.Header.Set("X-Real-IP", tt.realIP)
			}
			if tt.forwarded != "" {
				req.Header.Set("Forwarded", tt.forwarded)
			}
			if got := e.svc.ClientIP(req).String(); got != tt.wantIP {
				t.Errorf("ClientIP = %s, want %s", got, tt.wantIP)
			}
			if got := e.svc.IsLocalRequest(req); got != tt.wantLocal {
				t.Errorf("IsLocalRequest = %v, want %v", got, tt.wantLocal)
			}
		})
	}
}

func TestIsPrivateHost(t *testing.T) {
	tests := map[string]bool{
		"":                          true,
		"192.168.1.10":              true,
		"192.168.1.10:3873":         true,
		"203.0.113.5:3873":          true, // IP literals cannot be rebound
		"[fd00::1]:3873":            true,
		"[::1]":                     true,
		"localhost":                 true,
		"LOCALHOST:3873":            true,
		"tower":                     true,
		"tower:3873":                true,
		"dupearr.local":             true,
		"dupearr.local.":            true,
		"nas.lan:3873":              true,
		"dupearr.home.arpa":         true,
		"media.internal":            true,
		"app.localhost":             true,
		"dupearr.example.com":       false,
		"dupearr.example.com:3873":  false,
		"rebind.attacker.example":   false,
		"192.168.1.10.nip.io":       false,
		"local.example.com":         false,
		"lan.evil.com":              false,
		"evil.com.":                 false,
		"user@tower":                false,
		"xn--80ak6aa92e.com":        false,
		"internal.attacker.example": false,
	}
	for host, want := range tests {
		if got := IsPrivateHost(host); got != want {
			t.Errorf("IsPrivateHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestLocalAccessAllowed(t *testing.T) {
	e := newTestEnv(t, nil)
	tests := []struct {
		name   string
		target string
		remote string
		xfh    string
		want   bool
	}{
		{name: "local client by IP", target: "http://192.168.1.10:3873/", remote: localAddr, want: true},
		{name: "remote client by IP", target: "http://192.168.1.10:3873/", remote: remoteAddr, want: false},
		{name: "local client by public name", target: "http://dupearr.example.com/", remote: localAddr, want: false},
		{name: "local proxy forwarding a public name", target: "http://dupearr:3873/", remote: "127.0.0.1:1", xfh: "dupearr.example.com", want: false},
		{name: "local proxy forwarding a private name", target: "http://dupearr:3873/", remote: "127.0.0.1:1", xfh: "tower.lan, dupearr", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.RemoteAddr = tt.remote
			if tt.xfh != "" {
				req.Header.Set("X-Forwarded-Host", tt.xfh)
			}
			if got := e.svc.LocalAccessAllowed(req); got != tt.want {
				t.Errorf("LocalAccessAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHashAndCheckPassword(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("not a bcrypt hash: %q", h)
	}
	if !CheckPassword(h, "s3cret") {
		t.Error("CheckPassword(correct) = false")
	}
	for _, pw := range []string{"", "s3cre", "S3cret"} {
		if CheckPassword(h, pw) {
			t.Errorf("CheckPassword(%q) = true", pw)
		}
	}
	if CheckPassword("", "s3cret") {
		t.Error("CheckPassword with empty hash = true")
	}
	for _, bad := range []string{"", MaskedPassword, strings.Repeat("x", MaxPasswordBytes+1)} {
		if _, err := HashPassword(bad); err == nil {
			t.Errorf("HashPassword(%q) succeeded", bad)
		}
	}
	if BcryptCost != 12 {
		t.Errorf("BcryptCost = %d, want 12", BcryptCost)
	}
}

func TestClassify(t *testing.T) {
	tests := map[string]routeClass{
		"/":                              routePublic,
		"":                               routePublic,
		"/ping":                          routePublic,
		"/login":                         routePublic,
		"/logout":                        routePublic,
		"/assets/index-abc.js":           routePublic,
		"/favicon.svg":                   routePublic,
		"/duplicates/5":                  routePublic,
		"/api/v1/auth/status":            routePublic,
		"/api/v1/auth/setup":             routeSetup,
		"/api/v1/webhook/radarr":         routeWebhook,
		"/api/v1/webhook/../system/task": routeProtected,
		"/api":                           routeProtected,
		"/api/":                          routeProtected,
		"/api/v1/system/status":          routeProtected,
		"//api/v1/system/status":         routeProtected,
		"/x/../api/v1/system/status":     routeProtected,
		"/initialize.json":               routeProtected,
		"/backup/manual/a.zip":           routeProtected,
		"/apix":                          routePublic,
	}
	for p, want := range tests {
		if got := classify(p); got != want {
			t.Errorf("classify(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMiddlewareMatrix(t *testing.T) {
	type tc struct {
		name    string
		method  string // auth method (config)
		req     string // AuthenticationRequired
		target  string
		remote  string
		key     string // "header", "query", "bad" or ""
		cookie  string // "valid", "tampered", "expired" or ""
		xff     string
		xfh     string // X-Forwarded-Host
		want    int
		wantVia string
	}
	// The local bypass only trusts requests that name the server by IP or a private host name
	// (DNS-rebinding guard), so the API target carries an IP host.
	api := "http://192.168.1.10:3873/api/v1/system/status"
	tests := []tc{
		{name: "forms remote no creds", target: api, want: 401},
		{name: "forms local no creds (enabled)", target: api, remote: localAddr, want: 401},
		{name: "forms local bypass", req: config.AuthRequiredDisabledForLocal, target: api, remote: localAddr, want: 200, wantVia: ViaLocal},
		{name: "forms local bypass remote", req: config.AuthRequiredDisabledForLocal, target: api, want: 401},
		{name: "forms local bypass spoofed via proxy", req: config.AuthRequiredDisabledForLocal, target: api, remote: localAddr, xff: "203.0.113.50", want: 401},
		{name: "forms local bypass loopback proxy with local client", req: config.AuthRequiredDisabledForLocal, target: api, remote: "127.0.0.1:1", xff: "192.168.1.9", want: 200, wantVia: ViaLocal},
		{name: "local bypass single-label host", req: config.AuthRequiredDisabledForLocal, target: "http://tower:3873/api/v1/system/status", remote: localAddr, want: 200, wantVia: ViaLocal},
		{name: "local bypass .local host", req: config.AuthRequiredDisabledForLocal, target: "http://dupearr.local/api/v1/system/status", remote: localAddr, want: 200, wantVia: ViaLocal},
		{name: "local bypass localhost", req: config.AuthRequiredDisabledForLocal, target: "http://localhost:3873/api/v1/system/status", remote: "127.0.0.1:5", want: 200, wantVia: ViaLocal},
		{name: "local bypass refused for a public host name (DNS rebinding)", req: config.AuthRequiredDisabledForLocal, target: "http://rebind.attacker.example:3873/api/v1/system/status", remote: localAddr, want: 401},
		{name: "initialize.json refused for a public host name (DNS rebinding)", req: config.AuthRequiredDisabledForLocal, target: "http://rebind.attacker.example:3873/initialize.json", remote: localAddr, want: 401},
		{name: "local bypass refused for a public forwarded host", req: config.AuthRequiredDisabledForLocal, target: api, remote: "127.0.0.1:1", xff: "192.168.1.9", xfh: "dupearr.example.com", want: 401},
		{name: "local bypass with a private forwarded host", req: config.AuthRequiredDisabledForLocal, target: api, remote: "127.0.0.1:1", xff: "192.168.1.9", xfh: "dupearr.home.arpa", want: 200, wantVia: ViaLocal},
		{name: "public host name still works with a cookie", target: "http://dupearr.example.com/api/v1/system/status", cookie: "valid", want: 200, wantVia: ViaCookie},
		{name: "api key header", target: api, key: "header", want: 200, wantVia: ViaAPIKey},
		{name: "api key query is refused (r2-outbound-web#2)", target: api, key: "query", want: 401},
		{name: "api key query not rescued by a cookie", target: api, key: "query", cookie: "valid", want: 401},
		{name: "bad api key", target: api, key: "bad", want: 401},
		{name: "bad api key not rescued by cookie", target: api, key: "bad", cookie: "valid", want: 401},
		{name: "bad api key not rescued by local bypass", req: config.AuthRequiredDisabledForLocal, target: api, remote: localAddr, key: "bad", want: 401},
		{name: "valid cookie", target: api, cookie: "valid", want: 200, wantVia: ViaCookie},
		{name: "tampered cookie", target: api, cookie: "tampered", want: 401},
		{name: "expired cookie", target: api, cookie: "expired", want: 401},
		{name: "none method", method: config.AuthNone, target: api, want: 200, wantVia: ViaNoAuth},
		{name: "external method", method: config.AuthExternal, target: api, want: 200, wantVia: ViaExternal},
		{name: "legacy basic is forms", method: config.AuthBasic, target: api, want: 401},
		{name: "ping public", target: "/ping", want: 200},
		{name: "login public", target: "/login", want: 200},
		{name: "spa shell public", target: "/settings/general", want: 200},
		{name: "asset public", target: "/assets/index-abc.js", want: 200},
		{name: "favicon public", target: "/favicon.svg", want: 200},
		{name: "auth status public", target: "/api/v1/auth/status", want: 200},
		{name: "setup reachable", target: "/api/v1/auth/setup", want: 200},
		{name: "initialize needs auth", target: "/initialize.json", want: 401},
		{name: "initialize with cookie", target: "/initialize.json", cookie: "valid", want: 200, wantVia: ViaCookie},
		{name: "initialize with key", target: "/initialize.json", key: "header", want: 200, wantVia: ViaAPIKey},
		{name: "initialize with query key", target: "/initialize.json", key: "query", want: 401},
		{name: "backup download needs auth", target: "/backup/manual/a.zip", want: 401},
		{name: "backup download with cookie", target: "/backup/manual/a.zip", cookie: "valid", want: 200},
		{name: "webhook without key", target: "/api/v1/webhook/radarr", want: 401},
		{name: "webhook without key (none)", method: config.AuthNone, target: "/api/v1/webhook/radarr", want: 401},
		{name: "webhook without key (local bypass)", req: config.AuthRequiredDisabledForLocal, remote: localAddr, target: "/api/v1/webhook/sonarr", want: 401},
		{name: "webhook cookie is not enough", target: "/api/v1/webhook/plex", cookie: "valid", want: 401},
		{name: "webhook with key", target: "/api/v1/webhook/radarr", key: "query", want: 200, wantVia: ViaAPIKey},
		{name: "double slash api", target: "//api/v1/system/status", want: 401},
		{name: "dot segments api", target: "/x/../api/v1/system/status", want: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) {
				if tt.method != "" {
					c.AuthenticationMethod = tt.method
				}
				if tt.req != "" {
					c.AuthenticationRequired = tt.req
				}
			})
			e.createUser()
			key := e.cfg.Get().ApiKey
			o := reqOpts{target: tt.target, remote: tt.remote, headers: map[string]string{}}
			switch tt.key {
			case "header":
				o.headers["X-Api-Key"] = key
			case "query":
				o.target += "?apikey=" + key
			case "bad":
				o.headers["X-Api-Key"] = strings.Repeat("0", len(key))
			}
			if tt.xff != "" {
				o.headers["X-Forwarded-For"] = tt.xff
			}
			if tt.xfh != "" {
				o.headers["X-Forwarded-Host"] = tt.xfh
			}
			switch tt.cookie {
			case "valid":
				o.cookie = e.login(false)
			case "tampered":
				c := e.login(false)
				raw := []byte(c.Value)
				raw[len(raw)/2] ^= 1
				c.Value = string(raw)
				o.cookie = c
			case "expired":
				o.cookie = e.login(false)
				e.advance(SessionLifetime + time.Second)
			}
			rr := e.do(o)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tt.want, rr.Body.String())
			}
			if tt.wantVia != "" && rr.Header().Get("X-Via") != tt.wantVia {
				t.Errorf("via = %q, want %q", rr.Header().Get("X-Via"), tt.wantVia)
			}
			if rr.Code == 401 {
				if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("401 content type = %q", ct)
				}
				if got := strings.TrimSpace(rr.Body.String()); got != `{"message":"Unauthorized"}` {
					t.Errorf("401 body = %s", got)
				}
			}
		})
	}
}

func TestCSRF(t *testing.T) {
	type tc struct {
		name    string
		method  string // auth method
		req     string
		remote  string
		creds   string // "cookie", "key", "none"
		headers map[string]string
		want    int
	}
	tests := []tc{
		{name: "cookie without origin", creds: "cookie", want: 403},
		{name: "cookie same origin", creds: "cookie", headers: map[string]string{"Origin": "http://dupearr.local:3873"}, want: 200},
		{name: "cookie same origin default port", creds: "cookie", headers: map[string]string{"Origin": "https://dupearr.local"}, want: 403},
		{name: "cookie cross origin", creds: "cookie", headers: map[string]string{"Origin": "http://evil.example"}, want: 403},
		{name: "cookie null origin", creds: "cookie", headers: map[string]string{"Origin": "null"}, want: 403},
		{name: "cookie same-origin referer", creds: "cookie", headers: map[string]string{"Referer": "http://dupearr.local:3873/duplicates"}, want: 200},
		{name: "cookie cross-site referer", creds: "cookie", headers: map[string]string{"Referer": "http://evil.example/x"}, want: 403},
		{name: "api key cross origin allowed", creds: "key", headers: map[string]string{"Origin": "http://evil.example"}, want: 200},
		{name: "local bypass cross origin", req: config.AuthRequiredDisabledForLocal, remote: localAddr, creds: "none", headers: map[string]string{"Origin": "http://evil.example"}, want: 403},
		{name: "local bypass no origin", req: config.AuthRequiredDisabledForLocal, remote: localAddr, creds: "none", want: 403},
		{name: "none method no origin", method: config.AuthNone, creds: "none", want: 403},
		{name: "none method same origin", method: config.AuthNone, creds: "none", headers: map[string]string{"Origin": "http://dupearr.local:3873"}, want: 200},
		{name: "forwarded host from local proxy", remote: localAddr, creds: "cookie",
			headers: map[string]string{"Origin": "https://dupearr.example.com", "X-Forwarded-Host": "dupearr.example.com"}, want: 200},
		{name: "forwarded host from remote peer ignored", creds: "cookie",
			headers: map[string]string{"Origin": "https://dupearr.example.com", "X-Forwarded-Host": "dupearr.example.com"}, want: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t, func(c *config.Config) {
				if tt.method != "" {
					c.AuthenticationMethod = tt.method
				}
				if tt.req != "" {
					c.AuthenticationRequired = tt.req
				}
			})
			e.createUser()
			o := reqOpts{method: http.MethodPost, target: "http://dupearr.local:3873/api/v1/command", remote: tt.remote, headers: map[string]string{}}
			for k, v := range tt.headers {
				o.headers[k] = v
			}
			switch tt.creds {
			case "cookie":
				o.cookie = e.login(false)
			case "key":
				o.headers["X-Api-Key"] = e.cfg.Get().ApiKey
			}
			if rr := e.do(o); rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tt.want, rr.Body.String())
			}
		})
	}

	t.Run("setup rejects cross-site but allows header-less clients", func(t *testing.T) {
		e := newTestEnv(t, nil)
		if rr := e.do(reqOpts{method: http.MethodPost, target: "http://h/api/v1/auth/setup", headers: map[string]string{"Origin": "http://evil.example"}}); rr.Code != 403 {
			t.Errorf("cross-site setup = %d, want 403", rr.Code)
		}
		if rr := e.do(reqOpts{method: http.MethodPost, target: "http://h/api/v1/auth/setup"}); rr.Code != 200 {
			t.Errorf("header-less setup = %d, want 200", rr.Code)
		}
	})
	t.Run("GET is never CSRF-checked", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.createUser()
		if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: e.login(false), headers: map[string]string{"Origin": "http://evil.example"}}); rr.Code != 200 {
			t.Errorf("GET = %d, want 200", rr.Code)
		}
	})
}

func TestLoginCookieAttributes(t *testing.T) {
	t.Run("session cookie", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.createUser()
		c := e.login(false)
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
			t.Errorf("cookie attributes = %+v", c)
		}
		if c.MaxAge != 0 || !c.Expires.IsZero() {
			t.Errorf("non-remember cookie must be a session cookie: %+v", c)
		}
	})
	t.Run("remember me", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.createUser()
		c := e.login(true)
		if c.MaxAge != int(RememberLifetime/time.Second) {
			t.Errorf("MaxAge = %d", c.MaxAge)
		}
	})
	t.Run("url base path and TLS", func(t *testing.T) {
		e := newTestEnv(t, func(c *config.Config) { c.UrlBase = "/dupearr" })
		e.createUser()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.TLS = &tls.ConnectionState{}
		if err := e.svc.Login(rr, req, testUser, testPass, false); err != nil {
			t.Fatal(err)
		}
		c := findNamedCookie(rr.Result().Cookies(), "__Secure-"+CookieName)
		if c == nil || c.Path != "/dupearr" || !c.Secure {
			t.Errorf("cookie = %+v", rr.Result().Cookies())
		}
	})
	t.Run("secure behind local TLS proxy", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.createUser()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "172.17.0.1:5000"
		req.Header.Set("X-Forwarded-Proto", "https")
		if err := e.svc.Login(rr, req, testUser, testPass, false); err != nil {
			t.Fatal(err)
		}
		if c := findNamedCookie(rr.Result().Cookies(), "__Host-"+CookieName); c == nil || !c.Secure || c.Path != "/" {
			t.Errorf("cookie = %+v, want a Secure __Host- cookie", rr.Result().Cookies())
		}
	})
	t.Run("username is case-insensitive", func(t *testing.T) {
		e := newTestEnv(t, nil)
		e.createUser()
		rr := httptest.NewRecorder()
		if err := e.svc.Login(rr, httptest.NewRequest(http.MethodPost, "/login", nil), "ADMIN", testPass, false); err != nil {
			t.Fatalf("Login: %v", err)
		}
	})
}

func TestLoginFailures(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	for _, c := range []struct{ user, pass string }{
		{testUser, "wrong"},
		{"nobody", testPass},
		{"", ""},
		{testUser, ""},
		{testUser, strings.Repeat("p", 100)},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100." + "1:1" // distinct from the backoff test
		err := e.svc.Login(rr, req, c.user, c.pass, false)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q, %q) = %v, want ErrInvalidCredentials", c.user, c.pass, err)
		}
		if findCookie(rr.Result().Cookies()) != nil {
			t.Error("failed login set a cookie")
		}
	}
}

func TestLoginBackoff(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	attempt := func(remote, pass string) error {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remote
		return e.svc.Login(httptest.NewRecorder(), req, testUser, pass, false)
	}
	for i := 0; i < freeAttempts; i++ {
		if err := attempt(remoteAddr, "bad"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	// Sixth attempt: throttled, even with the right password.
	err := attempt(remoteAddr, testPass)
	var te *ThrottledError
	if !errors.As(err, &te) || !errors.Is(err, ErrTooManyAttempts) || te.RetryAfter != time.Second {
		t.Fatalf("6th attempt = %v, want throttled for 1s", err)
	}
	// Other clients are unaffected.
	if err := attempt("198.51.100.20:1", testPass); err != nil {
		t.Fatalf("other client: %v", err)
	}
	e.advance(time.Second)
	if err := attempt(remoteAddr, "bad"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("after 1s: %v", err)
	}
	if err := attempt(remoteAddr, testPass); !errors.As(err, &te) || te.RetryAfter != 2*time.Second {
		t.Fatalf("7th attempt = %v, want 2s backoff", err)
	}
	e.advance(2 * time.Second)
	if err := attempt(remoteAddr, testPass); err != nil {
		t.Fatalf("correct password after backoff: %v", err)
	}
	// Success resets the counter.
	if err := attempt(remoteAddr, "bad"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("after reset: %v", err)
	}
	// IPv6 clients are throttled per /64.
	for i := 0; i < freeAttempts; i++ {
		_ = attempt("[2001:db8:1:2::1]:1", "bad")
	}
	if err := attempt("[2001:db8:1:2::ffff]:1", testPass); !errors.As(err, &te) {
		t.Fatalf("same /64 = %v, want throttled", err)
	}
}

func TestLimiter(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 0, 4: 0, 5: time.Second, 6: 2 * time.Second, 10: 32 * time.Second, 15: maxDelay, 100: maxDelay} {
		if got := delay(n); got != want {
			t.Errorf("delay(%d) = %v, want %v", n, got, want)
		}
	}
	l := newLimiter()
	now := time.Unix(1000, 0)
	for i := 0; i < freeAttempts; i++ {
		if w := l.begin("k", now); w != 0 {
			t.Fatalf("begin %d waited %v", i, w)
		}
	}
	if w := l.begin("k", now); w != time.Second {
		t.Fatalf("wait = %v", w)
	}
	l.abort("k")
	if w := l.begin("k", now.Add(forgetAfter+time.Second)); w != 0 {
		t.Fatalf("forgotten key waited %v", w)
	}
	// Eviction keeps the map bounded.
	l = newLimiter()
	for i := 0; i < maxLimiterKeys+10; i++ {
		l.begin("key-"+time.Duration(i).String(), now.Add(time.Duration(i)*time.Millisecond))
	}
	if n := len(l.entries); n > maxLimiterKeys {
		t.Fatalf("limiter holds %d keys", n)
	}
}

func TestSlidingRenewal(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	c := e.login(false)
	e.advance(24 * time.Hour)
	rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: c})
	if rr.Code != 200 || findCookie(rr.Result().Cookies()) != nil {
		t.Fatalf("fresh session renewed or rejected: %d %v", rr.Code, rr.Result().Cookies())
	}
	e.advance(3 * 24 * time.Hour) // 4 days in: more than half of 7 days
	rr = e.do(reqOpts{target: "/api/v1/system/status", cookie: c})
	renewed := findCookie(rr.Result().Cookies())
	if rr.Code != 200 || renewed == nil {
		t.Fatalf("stale session not renewed: %d", rr.Code)
	}
	e.advance(4 * 24 * time.Hour) // the original cookie has expired, the renewed one has not
	if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: c}); rr.Code != 401 {
		t.Errorf("original cookie = %d, want 401", rr.Code)
	}
	if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: renewed}); rr.Code != 200 {
		t.Errorf("renewed cookie = %d, want 200", rr.Code)
	}
}

func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	c := e.login(true)
	ctx := context.Background()

	// Renaming keeps sessions (the password hash is unchanged).
	if _, err := e.svc.UpdateUser(ctx, "root", ""); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: c}); rr.Code != 200 {
		t.Fatalf("after rename = %d", rr.Code)
	}

	// The caller captures its session before changing the password and keeps it afterwards.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/host", nil)
	req.AddCookie(c)
	keep := e.svc.CurrentSession(req)
	if keep == nil || !keep.remember {
		t.Fatalf("CurrentSession = %+v", keep)
	}
	if _, err := e.svc.UpdateUser(ctx, "root", "a new password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: c}); rr.Code != 401 {
		t.Fatalf("old session after password change = %d, want 401", rr.Code)
	}
	if e.svc.CurrentSession(req) != nil {
		t.Fatal("CurrentSession accepted the old cookie")
	}
	rr := httptest.NewRecorder()
	if err := e.svc.KeepSession(rr, req, keep); err != nil {
		t.Fatalf("KeepSession: %v", err)
	}
	fresh := findCookie(rr.Result().Cookies())
	if fresh == nil || fresh.MaxAge == 0 {
		t.Fatalf("reissued cookie = %+v (want persistent)", fresh)
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: fresh}); rr.Code != 200 {
		t.Fatalf("reissued session = %d", rr.Code)
	}
	// No captured session: nothing is issued.
	rr = httptest.NewRecorder()
	if err := e.svc.KeepSession(rr, req, nil); err != nil || len(rr.Result().Cookies()) != 0 {
		t.Fatalf("KeepSession(nil): %v %v", err, rr.Result().Cookies())
	}
}

func TestUpdateUserValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	var ve config.ValidationErrors
	if _, err := e.svc.UpdateUser(ctx, "admin", ""); !errors.As(err, &ve) || ve[0].PropertyName != "password" {
		t.Fatalf("no password without user = %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, "admin", MaskedPassword); !errors.As(err, &ve) {
		t.Fatalf("mask without user = %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, " ", "pw"); !errors.As(err, &ve) || ve[0].PropertyName != "username" {
		t.Fatalf("blank username = %v", err)
	}
	e.createUser()
	u, err := e.svc.UpdateUser(ctx, "admin", MaskedPassword)
	if err != nil {
		t.Fatalf("mask with user: %v", err)
	}
	if !CheckPassword(u.PasswordHash, testPass) {
		t.Fatal("the mask changed the password")
	}
}

func TestSetupFlow(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, nil)
	if !e.svc.SetupRequired(ctx) {
		t.Fatal("SetupRequired = false on a fresh install")
	}
	var ve config.ValidationErrors
	if err := e.svc.Setup(ctx, config.AuthExternal, "", "admin", "pw"); !errors.As(err, &ve) {
		t.Fatalf("setup External = %v, want validation error", err)
	}
	if err := e.svc.Setup(ctx, config.AuthForms, "Sometimes", "", ""); !errors.As(err, &ve) || len(ve) != 3 {
		t.Fatalf("setup with bad input = %v, want 3 validation errors", err)
	}
	if err := e.svc.Setup(ctx, "forms", "disabledforlocaladdresses", testUser, testPass); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if e.svc.SetupRequired(ctx) {
		t.Fatal("SetupRequired after setup")
	}
	c := e.cfg.Get()
	if c.AuthenticationMethod != config.AuthForms || c.AuthenticationRequired != config.AuthRequiredDisabledForLocal {
		t.Fatalf("config after setup = %s/%s", c.AuthenticationMethod, c.AuthenticationRequired)
	}
	u, err := e.svc.User(ctx)
	if err != nil || u.Username != testUser {
		t.Fatalf("User = %+v, %v", u, err)
	}
	if err := e.svc.Setup(ctx, config.AuthForms, "", "other", "pw2"); !errors.Is(err, ErrSetupNotRequired) {
		t.Fatalf("second setup = %v, want ErrSetupNotRequired", err)
	}

	ext := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	if ext.svc.SetupRequired(ctx) {
		t.Fatal("SetupRequired with External auth")
	}
}

func TestUserLookupFallback(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, nil)
	if _, err := e.svc.User(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("User without account = %v", err)
	}
	// Simulate `dupearr reset-auth` + an account created elsewhere: the recorded id is stale.
	e.createUser()
	if err := e.db.Users().DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	h, _ := HashPassword("pw")
	created, err := e.db.Users().Upsert(ctx, "second", h)
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.User(ctx)
	if err != nil || u.ID != created.ID || u.Username != "second" {
		t.Fatalf("User = %+v, %v; want id %d", u, err, created.ID)
	}
}

func TestSessionKeyPersists(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	c := e.login(false)
	again, err := New(e.cfg, e.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	again.now = e.clock
	req := httptest.NewRequest(http.MethodGet, "/initialize.json", nil)
	req.AddCookie(c)
	rr := httptest.NewRecorder()
	again.Middleware(echo).ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("cookie rejected after restart: %d", rr.Code)
	}
	if _, err := New(nil, e.db, nil); err == nil {
		t.Error("New(nil config) succeeded")
	}
}

func TestLogout(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.UrlBase = "/d" })
	rr := httptest.NewRecorder()
	e.svc.Logout(rr, httptest.NewRequest(http.MethodPost, "/logout", nil))
	c := findCookie(rr.Result().Cookies())
	if c == nil || c.MaxAge >= 0 || c.Value != "" || c.Path != "/d" {
		t.Fatalf("logout cookie = %+v", c)
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "!!!", "YWJj", strings.Repeat("A", maxCookieLength+1)} {
		if _, ok := parseToken(v); ok {
			t.Errorf("parseToken(%q) ok", v)
		}
	}
	e := newTestEnv(t, nil)
	id := newSessionID()
	tok := e.svc.encodeToken(1, time.Unix(2000000000, 0), true, id, "hash")
	if tk, ok := parseToken(tok); !ok || tk.userID != 1 || tk.exp.Unix() != 2000000000 || !tk.remember || tk.id != id {
		t.Fatalf("round trip failed: %v %+v", ok, tk)
	}
}

func TestAuthenticatedHelper(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	if e.svc.Authenticated(req) {
		t.Fatal("anonymous request authenticated")
	}
	req.AddCookie(e.login(false))
	if !e.svc.Authenticated(req) {
		t.Fatal("cookie request not authenticated")
	}
	if e.svc.Method() != config.AuthForms {
		t.Fatalf("Method = %s", e.svc.Method())
	}
}
