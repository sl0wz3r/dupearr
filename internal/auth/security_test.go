package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// The router (net/http.ServeMux) matches the escaped path segment by segment, so an encoded "/"
// stays inside one segment ("{filename}" = "../../../../x"), while the decoded path climbs out of
// /api once cleaned. The middleware must classify requests the way they are routed.
func TestMiddlewareClassifiesTheRoutedPath(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	for _, target := range []string{
		"/api/v1/log/file/..%2F..%2F..%2F..%2Fdupearr.txt",
		"/api/v1/log/file/..%2f..%2f..%2f..%2fx",
		"/backup/manual/..%2F..%2Fdupearr_backup.zip",
		"/backup/%2e%2e/%2e%2e",
		"/backup/..%2F..%2Fmanual/x.zip",
		"/api/v1/duplicate/..%2F..%2F..%2Fx/approve",
		"/api/%2e%2e/x",
		"/api/v1/auth/status%2F..%2F..%2Fsystem/status",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			rr := e.do(reqOpts{method: method, target: target})
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without credentials = %d, want 401", method, target, rr.Code)
			}
		}
	}
	// The same paths still work with credentials, and public paths stay public.
	key := e.cfg.Get().ApiKey
	if rr := e.do(reqOpts{target: "/api/v1/log/file/..%2F..%2Fx", headers: map[string]string{"X-Api-Key": key}}); rr.Code != http.StatusOK {
		t.Errorf("with the API key = %d, want 200", rr.Code)
	}
	for _, target := range []string{"/", "/ping", "/login", "/api/v1/auth/status", "/assets/a%20b.js", "/movies/some%2Ftitle"} {
		if rr := e.do(reqOpts{target: target}); rr.Code != http.StatusOK {
			t.Errorf("public %s = %d, want 200", target, rr.Code)
		}
	}
}

func TestRoutedPath(t *testing.T) {
	tests := map[string]string{
		"/api/v1/system/status":          "/api/v1/system/status",
		"/%61pi/v1/system/status":        "/api/v1/system/status",
		"/api/v1/log/file/..%2F..%2Fx":   "/api/v1/log/file/..%2F..%2Fx",
		"/backup/%2e%2e/%2E%2E":          "/backup/%2e%2e/%2E%2E",
		"/api/v1/log/file/a%20b.txt":     "/api/v1/log/file/a b.txt",
		"/api/v1/log/file/bad%zzescape":  "/api/v1/log/file/bad%zzescape",
		"/x/../api/v1/system/status":     "/x/../api/v1/system/status",
		"/api/v1/webhook/radarr":         "/api/v1/webhook/radarr",
		"/api/v1/webhook%2Fradarr":       "/api/v1/webhook%2Fradarr",
		"":                               "",
		"/":                              "/",
		"/api/v1/auth/%73tatus":          "/api/v1/auth/status",
		"/api/v1/log/file/%2E":           "/api/v1/log/file/%2E",
		"/api/v1/log/file/.%2E":          "/api/v1/log/file/.%2E",
		"/api/v1/log/file/%2E%2E%2E.txt": "/api/v1/log/file/....txt",
	}
	for in, want := range tests {
		if got := routedPath(in); got != want {
			t.Errorf("routedPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// DNS rebinding against AuthenticationMethod None: a page on an attacker's domain that
// re-resolves to Dupearr's address is same-origin with itself, so the Origin check passes and,
// without a host check, None would hand it the whole API (turn off dry run, approve removals,
// read the API key from initialize.json).
func TestNoneMethodRefusesPublicHostNames(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthNone })
	key := e.cfg.Get().ApiKey
	tests := []struct {
		name    string
		method  string
		target  string
		remote  string
		headers map[string]string
		want    int
	}{
		{name: "IP literal", target: "http://192.168.1.10:3873/api/v1/system/status", want: 200},
		{name: "public IP literal (None allows remote clients)", target: "http://203.0.113.5:3873/api/v1/system/status", want: 200},
		{name: "localhost", target: "http://localhost:3873/initialize.json", want: 200},
		{name: "single-label host", target: "http://tower:3873/api/v1/system/status", want: 200},
		{name: "private suffix", target: "http://dupearr.lan:3873/api/v1/system/status", want: 200},
		{name: "rebinding read", target: "http://rebind.attacker.example:3873/initialize.json", remote: localAddr, want: 401},
		{name: "rebinding write (same origin)", method: http.MethodPut, target: "http://rebind.attacker.example:3873/api/v1/config/settings", remote: localAddr,
			headers: map[string]string{"Origin": "http://rebind.attacker.example:3873"}, want: 401},
		{name: "public forwarded host", target: "http://dupearr:3873/api/v1/system/status", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Forwarded-Host": "dupearr.example.com"}, want: 401},
		{name: "public host with the API key", target: "http://dupearr.example.com/api/v1/system/status", headers: map[string]string{"X-Api-Key": key}, want: 200},
		{name: "public host, public route", target: "http://dupearr.example.com/api/v1/auth/status", want: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(reqOpts{method: tt.method, target: tt.target, remote: tt.remote, headers: tt.headers})
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "http://rebind.attacker.example:3873/api/v1/system/status", nil)
	if e.svc.Authenticated(req) {
		t.Error("Authenticated() = true for a public host name under None")
	}
}

// The local-address bypass must not trust an IP literal that is not a local address: behind
// NAT or Docker's userland proxy, internet clients reaching a forwarded port show up with a local
// peer address and address the server by its public IP.
func TestLocalBypassNeedsALocalAddressLiteral(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal })
	e.createUser()
	tests := []struct {
		target string
		xfh    string
		want   int
	}{
		{target: "http://192.168.1.10:3873/api/v1/system/status", want: 200},
		{target: "http://[fd00::10]:3873/api/v1/system/status", want: 200},
		{target: "http://127.0.0.1:3873/api/v1/system/status", want: 200},
		{target: "http://203.0.113.5:3873/api/v1/system/status", want: 401},
		{target: "http://[2001:db8::10]:3873/api/v1/system/status", want: 401},
		{target: "http://0.0.0.0:3873/api/v1/system/status", want: 401},
		{target: "http://192.168.1.10:3873/api/v1/system/status", xfh: "203.0.113.5", want: 401},
		{target: "http://192.168.1.10:3873/api/v1/system/status", xfh: "192.168.1.10:443", want: 200},
	}
	for _, tt := range tests {
		t.Run(tt.target+" "+tt.xfh, func(t *testing.T) {
			h := map[string]string{}
			if tt.xfh != "" {
				h["X-Forwarded-Host"] = tt.xfh
			}
			rr := e.do(reqOpts{target: tt.target, remote: localAddr, headers: h})
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
		})
	}
	// First-run setup follows the same rule.
	req := httptest.NewRequest(http.MethodPost, "http://203.0.113.5:3873/api/v1/auth/setup", nil)
	req.RemoteAddr = localAddr
	if e.svc.LocalAccessAllowed(req) {
		t.Error("LocalAccessAllowed(public IP literal) = true")
	}
}
