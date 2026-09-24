package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// Encoded slashes and dots stay inside one path segment for the router, so these requests reach
// the {filename} / {type}/{name} handlers; the decoded path cleans to a public path. They must
// be refused by the auth middleware, not only by the handlers' own name checks.
func TestEncodedTraversalNeedsAuthentication(t *testing.T) {
	for _, base := range []string{"", "/dupearr"} {
		ts := newTestServer(t, func(o *serverOpts) {
			o.configure = func(c *config.Config) { c.UrlBase = base }
		})
		ts.createUser("admin", "correct horse")
		for _, target := range []string{
			"/api/v1/log/file/..%2F..%2F..%2F..%2Fdupearr.txt",
			"/api/v1/log/file/..%2f..%2f..%2f..%2f",
			"/backup/manual/..%2F..%2Fdupearr_backup.zip",
			"/backup/%2e%2e/%2e%2e",
			"/backup/..%2F..%2Fmanual/x.zip",
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rr := ts.do(method, base+target, nil, noKey)
				if rr.Code != http.StatusUnauthorized {
					t.Errorf("%s %s%s without credentials = %d %s, want 401", method, base, target, rr.Code, strings.TrimSpace(rr.Body.String()))
				}
			}
		}
	}
}

// With authentication method None, a DNS-rebinding page (an attacker's domain re-resolved to
// Dupearr's LAN address) must get neither the API key nor the ability to change settings.
func TestNoneMethodDNSRebinding(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationMethod = config.AuthNone }
	})
	const evil = "http://rebind.attacker.example:3873"
	lan := withRemote("192.168.1.20:50000")

	rr := ts.do(http.MethodGet, evil+"/initialize.json", nil, noKey, lan)
	if rr.Code != http.StatusUnauthorized || strings.Contains(rr.Body.String(), ts.key) {
		t.Fatalf("initialize.json via a rebinding host = %d %s, want 401 without the key", rr.Code, rr.Body.String())
	}
	rr = ts.do(http.MethodPut, evil+"/api/v1/config/settings", map[string]any{"dryRun": false}, noKey, lan,
		withHeader("Origin", evil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("settings PUT via a rebinding host = %d %s, want 401", rr.Code, rr.Body.String())
	}
	var st struct {
		DryRun bool `json:"dryRun"`
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/config/settings", nil), http.StatusOK, &st)
	if !st.DryRun {
		t.Fatal("dry run was turned off through a rebinding host")
	}

	// Opened by IP or a local name, None still needs no credentials; the API key works anywhere.
	expect(t, ts.do(http.MethodGet, "http://192.168.1.10:3873/initialize.json", nil, noKey, lan), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "http://tower:3873/api/v1/system/status", nil, noKey, lan), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, evil+"/api/v1/system/status", nil), http.StatusOK, nil)
	var status struct {
		Authenticated bool `json:"authenticated"`
	}
	expect(t, ts.do(http.MethodGet, evil+"/api/v1/auth/status", nil, noKey, lan), http.StatusOK, &status)
	if status.Authenticated {
		t.Error("auth/status reports authenticated for a rebinding host")
	}
}

// Unauthenticated endpoints that read a body must not wait forever for it: the server only has a
// ReadHeaderTimeout, so a client that sends the headers and then trickles (or withholds) the body
// would hold a connection and a goroutine indefinitely.
func TestUnauthenticatedBodyReadDeadline(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })

	ts := newTestServer(t)
	srv := httptest.NewServer(ts.h)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/login", "/api/v1/auth/setup"} {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"username\":", path)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		_, err = io.ReadAll(c)
		_ = c.Close()
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("%s: a withheld body held the connection for %v (read error %v)", path, elapsed, err)
		}
	}
}
