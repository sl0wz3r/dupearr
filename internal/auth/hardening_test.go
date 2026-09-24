package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/logging"
)

// ---------------------------------------------------------------------------
// SEC-001: External authentication only trusts the reverse proxy
// ---------------------------------------------------------------------------

// Without TrustedProxies / AllowedHosts, External must not trust a DNS-rebinding page (a public
// host name re-resolved to Dupearr's LAN address): it would read the API key from
// initialize.json, download a backup with every secret and approve removals.
func TestExternalRefusesRebindingHostsByDefault(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	const evil = "http://rebind.attacker.example:3873"
	tests := []struct {
		name    string
		method  string
		target  string
		remote  string
		headers map[string]string
		want    int
	}{
		{name: "rebinding initialize.json", target: evil + "/initialize.json", remote: localAddr, want: 401},
		{name: "rebinding backup list", target: evil + "/api/v1/system/backup", remote: localAddr, want: 401},
		{name: "rebinding same-origin write", method: http.MethodPost, target: evil + "/api/v1/system/backup", remote: localAddr,
			headers: map[string]string{"Origin": evil}, want: 401},
		{name: "public forwarded host", target: "http://dupearr:3873/api/v1/system/status", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Forwarded-Host": "dupearr.example.com"}, want: 401},
		{name: "private host", target: "http://192.168.1.10:3873/initialize.json", remote: localAddr, want: 200},
		{name: "api key still works", target: evil + "/initialize.json", remote: localAddr,
			headers: map[string]string{"X-Api-Key": e.cfg.Get().ApiKey}, want: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(reqOpts{method: tt.method, target: tt.target, remote: tt.remote, headers: tt.headers})
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, evil+"/api/v1/auth/status", nil)
	req.RemoteAddr = localAddr
	if e.svc.Authenticated(req) {
		t.Error("Authenticated() = true for a rebinding host under External")
	}
}

// With TrustedProxies, only requests relayed by the proxy are trusted: a client on the network
// that reaches the port directly gets 401 (no API key, no secrets). AllowedHosts adds a host check.
func TestExternalTrustedProxiesAndAllowedHosts(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	e.createUser()
	const proxy = "10.0.0.2:40000"
	e.svc.SetNetworkTrust(func() (string, string) { return "10.0.0.2", "" })
	tests := []struct {
		name    string
		target  string
		remote  string
		headers map[string]string
		cookie  bool
		want    int
		wantVia string
	}{
		{name: "LAN client directly", target: "http://192.168.1.10:3873/initialize.json", remote: localAddr, want: 401},
		{name: "LAN client directly, spoofed forwarding", target: "http://192.168.1.10:3873/initialize.json", remote: localAddr,
			headers: map[string]string{"X-Forwarded-For": "10.0.0.2", "Forwarded": "for=10.0.0.2"}, want: 401},
		{name: "relayed by the proxy", target: "http://dupearr.example.com/initialize.json", remote: proxy, want: 200, wantVia: ViaExternal},
		{name: "direct client with a session", target: "http://192.168.1.10:3873/initialize.json", remote: localAddr, cookie: true, want: 200, wantVia: ViaCookie},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := reqOpts{target: tt.target, remote: tt.remote, headers: tt.headers}
			if tt.cookie {
				o.cookie = e.login(false)
			}
			rr := e.do(o)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
			if tt.wantVia != "" && rr.Header().Get("X-Via") != tt.wantVia {
				t.Errorf("via = %q, want %q", rr.Header().Get("X-Via"), tt.wantVia)
			}
		})
	}

	e.svc.SetNetworkTrust(func() (string, string) { return "10.0.0.0/24", "dupearr.example.com, *.home.example.net" })
	for _, tt := range []struct {
		host, xfh string
		want      int
	}{
		{host: "dupearr.example.com", want: 200},
		{host: "DUPEARR.EXAMPLE.COM:443", want: 200},
		{host: "media.home.example.net", want: 200},
		{host: "home.example.net", want: 401},
		{host: "rebind.attacker.example", want: 401},
		{host: "dupearr.example.com", xfh: "rebind.attacker.example", want: 401},
	} {
		h := map[string]string{}
		if tt.xfh != "" {
			h["X-Forwarded-Host"] = tt.xfh
		}
		rr := e.do(reqOpts{target: "http://" + tt.host + "/api/v1/system/status", remote: proxy, headers: h})
		if rr.Code != tt.want {
			t.Errorf("host %s (xfh %q) = %d, want %d", tt.host, tt.xfh, rr.Code, tt.want)
		}
	}
}

func TestNetworkTrustFromEnvironment(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "172.18.0.0/16 ::1")
	t.Setenv(EnvAllowedHosts, "dupearr.example.com")
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	tr := e.svc.NetworkTrust()
	if len(tr.TrustedProxies) != 2 || len(tr.AllowedHosts) != 1 {
		t.Fatalf("NetworkTrust = %+v", tr)
	}
	if rr := e.do(reqOpts{target: "http://dupearr.example.com/initialize.json", remote: "172.18.0.5:1"}); rr.Code != 200 {
		t.Fatalf("relayed request = %d, want 200", rr.Code)
	}
	if rr := e.do(reqOpts{target: "http://dupearr.example.com/initialize.json", remote: localAddr}); rr.Code != 401 {
		t.Fatalf("direct request = %d, want 401", rr.Code)
	}
}

func TestParseNetworkTrust(t *testing.T) {
	tr, err := ParseNetworkTrust("10.0.0.1, 172.16.0.0/12;[fd00::1] ::ffff:192.168.1.1 bogus 10.0.0.1", "Dupearr.Example.com:443 *.lan.example.org bad/host")
	if err == nil || !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "bad/host") {
		t.Fatalf("err = %v, want the invalid entries named", err)
	}
	want := []string{"10.0.0.1/32", "172.16.0.0/12", "fd00::1/128", "192.168.1.1/32"}
	if len(tr.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v", tr.TrustedProxies)
	}
	for i, p := range tr.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("proxy %d = %s, want %s", i, p, want[i])
		}
	}
	if strings.Join(tr.AllowedHosts, ",") != "dupearr.example.com,*.lan.example.org" {
		t.Errorf("AllowedHosts = %v", tr.AllowedHosts)
	}
	// Catch-all ranges would trust every client: refused.
	for _, bad := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/1", "128.0.0.0/7", "::ffff:0.0.0.0/96"} {
		if tr, err := ParseNetworkTrust(bad, ""); err == nil || len(tr.TrustedProxies) != 0 {
			t.Errorf("ParseNetworkTrust(%q) = %v, %v; want refused", bad, tr.TrustedProxies, err)
		}
	}
}

// ---------------------------------------------------------------------------
// SEC-002: throttling cannot be bypassed with forwarding headers; per-username backoff;
// bounded bcrypt; minimum password length
// ---------------------------------------------------------------------------

// A client on the network (any local peer) used to choose its throttling key with
// X-Forwarded-For / X-Real-IP: a fresh key per guess meant the backoff never engaged.
func TestLoginThrottleIgnoresSpoofedForwardingHeaders(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	attempt := func(i int, pass string) error {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = localAddr
		req.Header.Set("X-Forwarded-For", "203.0.113."+strconv.Itoa(i))
		req.Header.Set("X-Real-IP", "198.51.100."+strconv.Itoa(i))
		req.Header.Set("Forwarded", "for=192.0.2."+strconv.Itoa(i))
		return e.svc.Login(httptest.NewRecorder(), req, testUser, pass, false)
	}
	for i := 0; i < freeAttempts; i++ {
		if err := attempt(i, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	var te *ThrottledError
	if err := attempt(99, testPass); !errors.As(err, &te) {
		t.Fatalf("rotated forwarding headers: %v, want throttled", err)
	}
	// The log names the real peer and what it claimed.
	var logs logBuffer
	e.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	_ = attempt(100, "wrong password")
	if out := logs.String(); !strings.Contains(out, "ip=192.168.1.20") || !strings.Contains(out, "claimed=") || !strings.Contains(out, "203.0.113.100") {
		t.Errorf("throttling log = %s", out)
	}
	// The spoofed address cannot lock out the address it names either.
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "203.0.113.1:5000"
	if err := e.svc.Login(httptest.NewRecorder(), req, testUser, testPass, false); err != nil {
		t.Fatalf("the named client was locked out: %v", err)
	}
}

// Behind a trusted proxy, the client is the address the proxy appended (right-most entry that is
// not a trusted proxy), not a left-hand entry the client sent.
func TestClientIPBehindTrustedProxy(t *testing.T) {
	e := newTestEnv(t, nil)
	e.svc.SetNetworkTrust(func() (string, string) { return "172.18.0.2, 10.0.0.0/8", "" })
	tests := []struct {
		name      string
		remote    string
		headers   map[string]string
		wantIP    string
		wantLocal bool
	}{
		{name: "proxy appends the client", remote: "172.18.0.2:1", headers: map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9"}, wantIP: "198.51.100.9"},
		{name: "chain of trusted proxies", remote: "172.18.0.2:1", headers: map[string]string{"X-Forwarded-For": "198.51.100.9, 10.1.1.1"}, wantIP: "198.51.100.9"},
		{name: "proxy forwards a local client", remote: "172.18.0.2:1", headers: map[string]string{"X-Forwarded-For": "192.168.1.30"}, wantIP: "192.168.1.30", wantLocal: true},
		{name: "rfc 7239 only", remote: "172.18.0.2:1", headers: map[string]string{"Forwarded": `for="[2001:db8::9]:1234";proto=https`}, wantIP: "2001:db8::9"},
		{name: "x-real-ip only", remote: "172.18.0.2:1", headers: map[string]string{"X-Real-IP": "198.51.100.10"}, wantIP: "198.51.100.10"},
		{name: "proxy without forwarding header", remote: "172.18.0.2:1", wantIP: "172.18.0.2"},
		{name: "unparsable entry", remote: "172.18.0.2:1", headers: map[string]string{"X-Forwarded-For": "junk"}, wantIP: "172.18.0.2"},
		{name: "untrusted peer", remote: "192.168.1.9:1", headers: map[string]string{"X-Forwarded-For": "198.51.100.9"}, wantIP: "192.168.1.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				req.Header.Set(k, v)
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

// Rotating source addresses must not give unlimited guesses at the account: the username has its
// own backoff. A browser that signed in before (device cookie) is exempt, so the attacker cannot
// lock the owner out with it.
func TestPerUsernameBackoffAndDeviceCookie(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	login := func(remote, user, pass string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, error) {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remote
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		return rr, e.svc.Login(rr, req, user, pass, false)
	}
	// The owner signs in once from their browser and gets a device cookie.
	rr, err := login("192.168.1.50:1", testUser, testPass)
	if err != nil {
		t.Fatal(err)
	}
	device := findNamedCookie(rr.Result().Cookies(), DeviceCookieName)
	if device == nil || !device.HttpOnly || device.SameSite != http.SameSiteStrictMode {
		t.Fatalf("device cookie = %+v", device)
	}

	// An attacker rotates addresses: every guess comes from a fresh IPv4 address.
	for i := 0; i < userFreeAttempts; i++ {
		if _, err := login("198.51.100."+strconv.Itoa(i+1)+":1", "ADMIN", "guess "+strconv.Itoa(i)); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("guess %d: %v", i+1, err)
		}
	}
	var te *ThrottledError
	if _, err := login("198.51.100.200:1", testUser, testPass); !errors.As(err, &te) {
		t.Fatalf("guess from a fresh address = %v, want the username backoff", err)
	}
	// Other usernames are unaffected (no global lockout).
	if _, err := login("198.51.100.201:1", "someone-else", "x-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("other username = %v, want invalid credentials", err)
	}
	// The owner's browser is not locked out.
	if _, err := login("192.168.1.50:1", testUser, testPass, device); err != nil {
		t.Fatalf("owner with device cookie = %v, want success", err)
	}
	// A forged or foreign device cookie does not help.
	forged := &http.Cookie{Name: DeviceCookieName, Value: device.Value[:len(device.Value)-2] + "AA"}
	if _, err := login("198.51.100.202:1", testUser, testPass, forged); !errors.As(err, &te) {
		t.Fatalf("forged device cookie = %v, want throttled", err)
	}
	if e.svc.deviceKey(httptest.NewRequest(http.MethodPost, "/login", nil), "other") != "" {
		t.Fatal("deviceKey without a cookie")
	}
}

func TestLimiterKeysIPv6Per56(t *testing.T) {
	a := limiterKey(parseHostIP("2001:db8:1:2::1"))
	b := limiterKey(parseHostIP("2001:db8:1:ff::1")) // another /64 in the same /56
	c := limiterKey(parseHostIP("2001:db8:1:100::1"))
	if a != b || a == c {
		t.Fatalf("keys %s %s %s: want one key per /56", a, b, c)
	}
}

// Password hashing runs in bounded slots: a flood of logins gets ErrBusy (503) instead of queueing
// without bound, and the unevaluated attempt is not counted.
func TestLoginBusyWhenHashingSlotsAreTaken(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	oldSlots, oldWait := bcryptSlots, bcryptWait
	bcryptSlots, bcryptWait = make(chan struct{}, 1), 50*time.Millisecond
	t.Cleanup(func() { bcryptSlots, bcryptWait = oldSlots, oldWait })
	bcryptSlots <- struct{}{} // every slot busy

	for i := 0; i < freeAttempts+2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remoteAddr
		for _, user := range []string{testUser, "nobody"} {
			if err := e.svc.Login(httptest.NewRecorder(), req, user, "whatever-password", false); !errors.Is(err, ErrBusy) {
				t.Fatalf("login %d (%s) with no free slot = %v, want ErrBusy", i, user, err)
			}
		}
	}
	<-bcryptSlots
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = remoteAddr
	if err := e.svc.Login(httptest.NewRecorder(), req, testUser, testPass, false); err != nil {
		t.Fatalf("busy attempts were counted: %v", err)
	}
}

func TestMinimumPasswordLength(t *testing.T) {
	if errs := ValidateCredentials("admin", "short12"); len(errs) != 1 || errs[0].PropertyName != "password" {
		t.Fatalf("7-character password: %v", errs)
	}
	if errs := ValidateCredentials("admin", "long-enough"); len(errs) != 0 {
		t.Fatalf("11-character password: %v", errs)
	}
	e := newTestEnv(t, nil)
	var ve config.ValidationErrors
	if _, err := e.svc.UpdateUser(context.Background(), "admin", "a"); !errors.As(err, &ve) {
		t.Fatalf("UpdateUser with a 1-character password = %v", err)
	}
	// An existing short password keeps working.
	h, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Users().Upsert(context.Background(), testUser, h); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := e.svc.Login(httptest.NewRecorder(), req, testUser, "pw", false); err != nil {
		t.Fatalf("login with an existing short password: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SEC-010: typed usernames are neither logged in full nor at all when unknown
// ---------------------------------------------------------------------------

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLoginDoesNotLogTypedUsernames(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	var logs logBuffer
	e.svc.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	huge := strings.Repeat("u", 1<<20)
	secretish := "correct-horse-typed-in-the-username-field"
	for _, user := range []string{huge, secretish} {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100.77:1"
		if err := e.svc.Login(httptest.NewRecorder(), req, user, "whatever-password", false); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Login = %v", err)
		}
	}
	out := logs.String()
	if len(out) > 4096 {
		t.Fatalf("log output is %d bytes for two failed logins", len(out))
	}
	if strings.Contains(out, secretish) || strings.Contains(out, strings.Repeat("u", 100)) {
		t.Fatalf("typed username logged: %s", out)
	}
	if !strings.Contains(out, "Auth-Failure") {
		t.Fatalf("failure not logged: %s", out)
	}
}

// ---------------------------------------------------------------------------
// SEC-003: local trust, RFC 7239 Forwarded, setup code
// ---------------------------------------------------------------------------

func TestLocalAccessFailsClosedOnForwardedHeader(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal })
	e.createUser()
	for _, fwd := range []string{"for=8.8.8.8", "for=unknown", "for=_hidden", "proto=https", `for="[2001:db8::1]"`} {
		rr := e.do(reqOpts{target: "http://127.0.0.1:3873/api/v1/system/status", remote: "127.0.0.1:5", headers: map[string]string{"Forwarded": fwd}})
		if rr.Code != 401 {
			t.Errorf("Forwarded %q = %d, want 401", fwd, rr.Code)
		}
	}
	// A trusted proxy that does not say who the client is: not local.
	e.svc.SetNetworkTrust(func() (string, string) { return "127.0.0.1", "" })
	if rr := e.do(reqOpts{target: "http://127.0.0.1:3873/api/v1/system/status", remote: "127.0.0.1:5"}); rr.Code != 401 {
		t.Errorf("trusted proxy without forwarding header = %d, want 401", rr.Code)
	}
	if rr := e.do(reqOpts{target: "http://127.0.0.1:3873/api/v1/system/status", remote: "127.0.0.1:5",
		headers: map[string]string{"X-Forwarded-For": "192.168.1.40"}}); rr.Code != 200 {
		t.Errorf("trusted proxy naming a local client = %d, want 200", rr.Code)
	}
}

func TestSetupCode(t *testing.T) {
	e := newTestEnv(t, nil)
	code := e.svc.SetupCode()
	if len(code) != setupCodeLength+3 || strings.Count(code, "-") != 3 {
		t.Fatalf("setup code %q", code)
	}
	for _, wrong := range []string{"", "AAAAA-AAAAA-AAAAA-AAAAA", code[:len(code)-1], code + "A"} {
		if e.svc.CheckSetupCode(wrong) {
			t.Errorf("CheckSetupCode(%q) = true", wrong)
		}
	}
	for _, ok := range []string{code, strings.ToLower(code), strings.ReplaceAll(code, "-", ""), " " + strings.ReplaceAll(code, "-", " ") + " "} {
		if !e.svc.CheckSetupCode(ok) {
			t.Errorf("CheckSetupCode(%q) = false", ok)
		}
	}
	if err := e.svc.Setup(context.Background(), config.AuthForms, "", testUser, testPass); err != nil {
		t.Fatal(err)
	}
	if e.svc.SetupCode() != "" || e.svc.CheckSetupCode(code) {
		t.Fatal("the setup code outlived the setup")
	}
	// A new code is issued per start while setup is pending, never when it is not.
	other := newTestEnv(t, nil)
	if other.svc.SetupCode() == "" || other.svc.SetupCode() == code {
		t.Fatal("no fresh setup code")
	}
	ext := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	if ext.svc.SetupCode() != "" || ext.svc.CheckSetupCode("") {
		t.Fatal("setup code without pending setup")
	}
}

// ---------------------------------------------------------------------------
// SEC-004 / r2-outbound-web#2: the master API key is never accepted in the query string (proxy
// access logs, browser history, download lists); only the webhook routes take a query credential
// ---------------------------------------------------------------------------

func TestQueryAPIKeyRefused(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	key := e.cfg.Get().ApiKey
	for _, tt := range []struct {
		method, target string
		headers        map[string]string
		want           int
	}{
		{method: http.MethodGet, target: "/api/v1/system/status?apikey=" + key, want: 401},
		{method: http.MethodHead, target: "/backup/manual/x.zip?apikey=" + key, want: 401},
		{method: http.MethodGet, target: "/api/v1/events?apikey=" + key, want: 401},
		{method: http.MethodGet, target: "/api/v1/mediacover/1?path=/library/x&apikey=" + key, want: 401},
		{method: http.MethodGet, target: "/api/v1/webhook/radarr?apikey=" + e.svc.WebhookToken(), want: 200},
		{method: http.MethodPost, target: "/api/v1/duplicate/bulk?apikey=" + key, headers: map[string]string{"Origin": "http://evil.example"}, want: 401},
		{method: http.MethodPost, target: "/api/v1/system/backup?apikey=" + key, want: 401},
		{method: http.MethodPut, target: "/api/v1/config/settings?apikey=" + key, want: 401},
		{method: http.MethodDelete, target: "/api/v1/system/backup/1?apikey=" + key, want: 401},
		{method: http.MethodPost, target: "/api/v1/system/backup", headers: map[string]string{"X-Api-Key": key}, want: 200},
	} {
		rr := e.do(reqOpts{method: tt.method, target: tt.target, headers: tt.headers})
		if rr.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.target, rr.Code, tt.want)
		}
	}
	// A query key on a write is not rescued by the session cookie either.
	rr := e.do(reqOpts{method: http.MethodPost, target: "/api/v1/command?apikey=" + key, cookie: e.login(false),
		headers: map[string]string{"Origin": "http://evil.example"}})
	if rr.Code != 401 {
		t.Errorf("query key + cookie cross-site = %d, want 401", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// SEC-005: webhook token
// ---------------------------------------------------------------------------

func TestWebhookTokenOnlyAuthenticatesWebhooks(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	tok := e.svc.WebhookToken()
	key := e.cfg.Get().ApiKey
	if len(tok) != 32 || tok == key {
		t.Fatalf("webhook token %q", tok)
	}
	for _, tt := range []struct {
		method, target string
		headers        map[string]string
		want           int
		wantVia        string
	}{
		{method: http.MethodPost, target: "/api/v1/webhook/radarr", headers: map[string]string{"X-Api-Key": tok}, want: 200, wantVia: ViaWebhookToken},
		{method: http.MethodPost, target: "/api/v1/webhook/plex?apikey=" + tok, want: 200, wantVia: ViaWebhookToken},
		{method: http.MethodPost, target: "/api/v1/webhook/sonarr?apikey=" + key, want: 200, wantVia: ViaAPIKey}, // deprecated
		{method: http.MethodPost, target: "/api/v1/webhook/radarr?apikey=wrong", want: 401},
		{method: http.MethodGet, target: "/initialize.json?apikey=" + tok, want: 401},
		{method: http.MethodGet, target: "/api/v1/system/status", headers: map[string]string{"X-Api-Key": tok}, want: 401},
		{method: http.MethodPost, target: "/api/v1/system/backup", headers: map[string]string{"X-Api-Key": tok}, want: 401},
		{method: http.MethodGet, target: "/backup/manual/x.zip?apikey=" + tok, want: 401},
	} {
		rr := e.do(reqOpts{method: tt.method, target: tt.target, headers: tt.headers})
		if rr.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.target, rr.Code, tt.want)
		}
		if tt.wantVia != "" && rr.Header().Get("X-Via") != tt.wantVia {
			t.Errorf("%s via = %q, want %q", tt.target, rr.Header().Get("X-Via"), tt.wantVia)
		}
	}
	// Regenerating replaces it; it persists across restarts.
	next, err := e.svc.RegenerateWebhookToken(context.Background())
	if err != nil || next == tok {
		t.Fatalf("RegenerateWebhookToken = %q, %v", next, err)
	}
	if rr := e.do(reqOpts{method: http.MethodPost, target: "/api/v1/webhook/radarr?apikey=" + tok}); rr.Code != 401 {
		t.Errorf("old webhook token = %d, want 401", rr.Code)
	}
	again, err := New(e.cfg, e.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.WebhookToken() != next {
		t.Fatal("webhook token not persisted")
	}
}

// ---------------------------------------------------------------------------
// SEC-012: sessions are revocable
// ---------------------------------------------------------------------------

func TestLogoutRevokesTheSession(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	c := e.login(true)
	other := e.login(false)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(c)
	e.svc.Logout(httptest.NewRecorder(), req)
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: c}); rr.Code != 401 {
		t.Fatalf("replayed cookie after logout = %d, want 401", rr.Code)
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: other}); rr.Code != 200 {
		t.Fatalf("another session after logout = %d, want 200", rr.Code)
	}
	// The revocation survives a restart.
	again, err := New(e.cfg, e.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	again.now = e.clock
	req = httptest.NewRequest(http.MethodGet, "/initialize.json", nil)
	req.AddCookie(c)
	if again.Authenticated(req) {
		t.Fatal("revoked session accepted after restart")
	}
}

func TestRevokeSessionsLogsOutEverything(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	a, b := e.login(true), e.login(false)
	changed := e.svc.CredentialsChanged()
	if err := e.svc.RevokeSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("CredentialsChanged did not fire")
	}
	for _, c := range []*http.Cookie{a, b} {
		if rr := e.do(reqOpts{target: "/initialize.json", cookie: c}); rr.Code != 401 {
			t.Fatalf("cookie after RevokeSessions = %d, want 401", rr.Code)
		}
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: e.login(false)}); rr.Code != 200 {
		t.Fatalf("new login after RevokeSessions = %d", rr.Code)
	}
	again, err := New(e.cfg, e.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.signing.Load().generation != e.svc.signing.Load().generation {
		t.Fatal("session generation not persisted")
	}
	if RememberLifetime > 14*24*time.Hour {
		t.Fatalf("RememberLifetime = %v", RememberLifetime)
	}
}

func TestStillAuthenticatedAfterKeyChange(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("X-Api-Key", e.cfg.Get().ApiKey)
	if !e.svc.StillAuthenticated(req) {
		t.Fatal("valid key not authenticated")
	}
	changed := e.svc.CredentialsChanged()
	if _, err := e.cfg.Update(func(c *config.Config) { c.ApiKey = config.GenerateAPIKey() }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("CredentialsChanged did not fire on an API key change")
	}
	if e.svc.StillAuthenticated(req.WithContext(WithInfo(req.Context(), Info{Authenticated: true, Via: ViaAPIKey}))) {
		t.Fatal("old key still authenticated")
	}
}

// ---------------------------------------------------------------------------
// SEC-024: prefixed session cookie over HTTPS
// ---------------------------------------------------------------------------

func TestHTTPSSessionCookieIsHostPrefixed(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://dupearr.example.com/login", nil)
	req.TLS = &tls.ConnectionState{}
	if err := e.svc.Login(rr, req, testUser, testPass, false); err != nil {
		t.Fatal(err)
	}
	c := findNamedCookie(rr.Result().Cookies(), "__Host-"+CookieName)
	if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookies = %+v, want a __Host- cookie", rr.Result().Cookies())
	}
	if findCookie(rr.Result().Cookies()) != nil {
		t.Fatal("an unprefixed session cookie was set over HTTPS")
	}
	use := func(name string) int {
		req := httptest.NewRequest(http.MethodGet, "https://dupearr.example.com/initialize.json", nil)
		req.TLS = &tls.ConnectionState{}
		req.AddCookie(&http.Cookie{Name: name, Value: c.Value})
		rr := httptest.NewRecorder()
		e.svc.Middleware(echo).ServeHTTP(rr, req)
		return rr.Code
	}
	if code := use("__Host-" + CookieName); code != 200 {
		t.Fatalf("prefixed cookie = %d", code)
	}
	// The same token under the plain name (which any HTTP service on the host could set) is refused.
	if code := use(CookieName); code != 401 {
		t.Fatalf("unprefixed cookie over HTTPS = %d, want 401", code)
	}
}

// The setup code is only useful if the admin can read it in the log: the log redaction (which
// masks anything that looks like a token or key) must leave it intact.
func TestSetupCodeSurvivesLogRedaction(t *testing.T) {
	var logs logBuffer
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, nil)
	svc, err := New(cfg, e.db, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	code := svc.SetupCode()
	if code == "" {
		t.Fatal("no setup code")
	}
	if out := logging.Redact(logs.String()); !strings.Contains(out, "setup code when asked: "+code) {
		t.Fatalf("setup code not readable in the redacted log: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Re-review: logout revokes every token of a session, renewals included
// ---------------------------------------------------------------------------

// A copied session cookie must not outlive its owner's logout because one of the two tokens was
// renewed (sliding expiration) in between: renewals keep the session id, which logout revokes.
func TestLogoutRevokesRenewedTokensOfTheSession(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	original := e.login(false)
	e.advance(SessionLifetime/2 + time.Hour)
	// A thief renews a copy of the cookie ...
	rr := e.do(reqOpts{target: "/initialize.json", cookie: original})
	if rr.Code != 200 {
		t.Fatalf("copy = %d, want 200", rr.Code)
	}
	renewed := findCookie(rr.Result().Cookies())
	if renewed == nil || renewed.Value == original.Value {
		t.Fatal("the stale token was not renewed")
	}
	// ... and the owner signs out with the original token.
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(original)
	e.svc.Logout(httptest.NewRecorder(), req)
	for name, c := range map[string]*http.Cookie{"original": original, "renewed copy": renewed} {
		if rr := e.do(reqOpts{target: "/initialize.json", cookie: c}); rr.Code != 401 {
			t.Errorf("%s after logout = %d, want 401", name, rr.Code)
		}
	}
	if rr := e.do(reqOpts{target: "/initialize.json", cookie: e.login(false)}); rr.Code != 200 {
		t.Fatalf("a new session after the logout = %d, want 200", rr.Code)
	}
}

// KeepSession (after a password change or "log out all sessions") must also re-issue the device
// cookie, which the new session generation invalidated: otherwise the owner loses the lockout
// protection exactly when reacting to an attack.
func TestKeepSessionReissuesTheDeviceCookie(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sessions/revoke", nil)
	req.AddCookie(e.login(false))
	keep := e.svc.CurrentSession(req)
	if keep == nil {
		t.Fatal("no current session")
	}
	if err := e.svc.RevokeSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	if err := e.svc.KeepSession(rr, req, keep); err != nil {
		t.Fatal(err)
	}
	dev := findNamedCookie(rr.Result().Cookies(), DeviceCookieName)
	if dev == nil {
		t.Fatal("KeepSession set no device cookie")
	}
	login := httptest.NewRequest(http.MethodPost, "/login", nil)
	login.AddCookie(dev)
	if e.svc.deviceKey(login, testUser) == "" {
		t.Fatal("the re-issued device cookie is not valid for the new generation")
	}
}

// ---------------------------------------------------------------------------
// Re-review: client-triggerable warnings cannot flood (and so rotate away) the log
// ---------------------------------------------------------------------------

func TestClientTriggerableWarningsAreSampled(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal })
	e.createUser()
	var logs logBuffer
	e.svc.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Throttled logins cost the client nothing.
	for i := range 500 {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "198.51.100.9:" + strconv.Itoa(1000+i)
		_ = e.svc.Login(httptest.NewRecorder(), req, testUser, "wrong password", false)
	}
	// A malicious page can loop cross-site requests with ~64 KiB paths through a LAN visitor's
	// browser (the local-address bypass authenticates them, the Origin check rejects them).
	long := "/api/v1/command/" + strings.Repeat("A", 60<<10)
	for range 200 {
		rr := e.do(reqOpts{method: http.MethodPost, target: "http://192.168.1.2:3873" + long, remote: localAddr,
			headers: map[string]string{"Origin": "https://evil.example"}})
		if rr.Code != http.StatusForbidden {
			t.Fatalf("cross-site request = %d, want 403", rr.Code)
		}
	}
	out := logs.String()
	if n := strings.Count(out, "Auth-Throttled"); n == 0 || n > logSampleBurst {
		t.Errorf("%d Auth-Throttled lines, want 1..%d", n, logSampleBurst)
	}
	if n := strings.Count(out, "Rejected a cross-site request"); n == 0 || n > logSampleBurst {
		t.Errorf("%d cross-site lines, want 1..%d", n, logSampleBurst)
	}
	if len(out) > 64<<10 {
		t.Fatalf("log output is %d bytes", len(out))
	}
	// The next window reports how many were suppressed.
	e.advance(logSampleWindow + time.Second)
	e.do(reqOpts{method: http.MethodPost, target: "http://192.168.1.2:3873/api/v1/command", remote: localAddr,
		headers: map[string]string{"Origin": "https://evil.example"}})
	if !strings.Contains(logs.String(), "suppressed=190") {
		t.Fatalf("no suppressed count reported: %s", logs.String()[max(0, len(logs.String())-500):])
	}
	// A logout without a session is not worth an Info line.
	before := len(logs.String())
	for range 100 {
		e.svc.Logout(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/logout", nil))
	}
	if strings.Contains(logs.String()[before:], "Auth-Logout") {
		t.Fatal("anonymous logouts are logged at Info")
	}
}

// ---------------------------------------------------------------------------
// Re-review: a trusted proxy that names no client is not local
// ---------------------------------------------------------------------------

func TestTrustedProxyWithEmptyForwardingHeaderIsNotLocal(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal })
	e.createUser()
	e.svc.SetNetworkTrust(func() (string, string) { return "172.18.0.5", "" })
	for _, h := range []map[string]string{nil, {"X-Forwarded-For": ","}, {"X-Forwarded-For": " "}, {"Forwarded": ""}} {
		rr := e.do(reqOpts{target: "http://192.168.1.2:3873/api/v1/system/status", remote: "172.18.0.5:4000", headers: h})
		if rr.Code != 401 {
			t.Errorf("trusted proxy with headers %v = %d, want 401", h, rr.Code)
		}
	}
	rr := e.do(reqOpts{target: "http://192.168.1.2:3873/api/v1/system/status", remote: "172.18.0.5:4000",
		headers: map[string]string{"X-Forwarded-For": "192.168.1.30"}})
	if rr.Code != 200 {
		t.Fatalf("trusted proxy naming a local client = %d, want 200", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Re-review: the setup code is shown even when the log level hides warnings
// ---------------------------------------------------------------------------

func TestSetupCodeLoggedAtErrorLevel(t *testing.T) {
	e := newTestEnv(t, nil)
	var logs logBuffer
	svc, err := New(e.cfg, e.db, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	code := svc.SetupCode()
	if code == "" || !strings.Contains(logs.String(), code) {
		t.Fatalf("setup code %q not in the error-level log: %q", code, logs.String())
	}
}
