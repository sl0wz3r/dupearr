package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// NEW-F (round-1 review): a reverse proxy missing from the TrustedProxies makes every client share
// the proxy's throttling key. The middleware records such a proxy so a health check can say so.
func TestUntrustedProxySeen(t *testing.T) {
	e := newTestEnv(t, nil)
	e.svc.SetNetworkTrust(func() (string, string) { return "172.18.0.2", "" })
	h := e.svc.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	send := func(remote string, header map[string]string) {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = remote
		for k, v := range header {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	send("172.18.0.9:1", nil)                                               // local peer, no header
	send("172.18.0.2:1", map[string]string{"X-Forwarded-For": "1.2.3.4"})   // the trusted proxy
	send("203.0.113.7:1", map[string]string{"X-Forwarded-For": "10.0.0.1"}) // internet peer: a spoof, not a proxy
	if at, peer := e.svc.UntrustedProxySeen(); !at.IsZero() || peer != "" {
		t.Fatalf("recorded %s %q; want nothing", at, peer)
	}

	for _, hdr := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP"} {
		t.Run(hdr, func(t *testing.T) {
			e.svc.untrustedFwd.Store(nil)
			send("172.18.0.5:1", map[string]string{hdr: "for=198.51.100.9"})
			at, peer := e.svc.UntrustedProxySeen()
			if !at.Equal(e.clock()) || peer != "172.18.0.5" {
				t.Fatalf("recorded %s %q; want %s 172.18.0.5", at, peer, e.clock())
			}
		})
	}

	// Recorded at most once a minute per peer, but a new peer or a later request updates it.
	first, _ := e.svc.UntrustedProxySeen()
	e.advance(30 * time.Second)
	send("172.18.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.9"})
	if at, _ := e.svc.UntrustedProxySeen(); !at.Equal(first) {
		t.Fatalf("updated within a minute: %s", at)
	}
	e.advance(time.Minute)
	send("172.18.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.9"})
	if at, _ := e.svc.UntrustedProxySeen(); !at.Equal(e.clock()) {
		t.Fatalf("not updated after a minute: %s", at)
	}
	send("192.168.1.4:1", map[string]string{"X-Real-IP": "198.51.100.9"})
	if _, peer := e.svc.UntrustedProxySeen(); peer != "192.168.1.4" {
		t.Fatalf("peer = %q, want the latest one", peer)
	}
}

// Issue #1: once the reported peer is added to the trusted proxies (Settings → General takes
// effect at once), ReverseProxyCheck no longer reports it — not only after a restart.
func TestUntrustedProxySeenClearsOnceTrusted(t *testing.T) {
	e := newTestEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "192.168.1.4:1"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	e.svc.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	if _, peer := e.svc.UntrustedProxySeen(); peer != "192.168.1.4" {
		t.Fatalf("peer = %q, want 192.168.1.4 recorded", peer)
	}
	if _, err := e.cfg.Update(func(c *config.Config) { c.TrustedProxies = "192.168.1.0/24" }); err != nil {
		t.Fatal(err)
	}
	if at, peer := e.svc.UntrustedProxySeen(); !at.IsZero() || peer != "" {
		t.Fatalf("UntrustedProxySeen = %s %q after trusting the peer, want nothing", at, peer)
	}
}
