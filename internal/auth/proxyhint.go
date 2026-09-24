package auth

import (
	"net"
	"net/http"
	"time"
)

// Untrusted reverse proxies.
//
// Forwarding headers (X-Forwarded-For, RFC 7239 Forwarded, X-Real-IP) are only believed from the
// TrustedProxies. A reverse proxy that is missing from that list still works, but Dupearr cannot
// see the real clients behind it: every client shares the proxy's login-throttling key (an
// attacker can keep the owner's first login throttled), External authentication cannot tell the
// proxy's requests from direct ones, and local-address checks see only the proxy. When a request
// with a forwarding header arrives from a local-network peer that is not a trusted proxy, the
// peer is almost certainly such a proxy; the ReverseProxyCheck health check reports it.
//
// Only local peers are recorded: a forwarding header from an internet address is a spoofing
// attempt, not a misconfigured proxy. A LAN client can trigger the warning on purpose, which costs
// nothing but a health notice.

// forwardingSeen is the last request with an untrusted forwarding header.
type forwardingSeen struct {
	at   time.Time
	peer string
}

// hasForwardingHeader reports whether the request carries any client-forwarding header.
func hasForwardingHeader(r *http.Request) bool {
	h := r.Header
	return len(h.Values("X-Forwarded-For")) > 0 || len(h.Values("Forwarded")) > 0 || len(h.Values("X-Real-IP")) > 0
}

// noteUntrustedForwarding records a request whose TCP peer is a local address that is not in the
// TrustedProxies but that sent a forwarding header.
func (s *Service) noteUntrustedForwarding(r *http.Request) {
	if !hasForwardingHeader(r) {
		return
	}
	peer := peerIP(r)
	if !IsLocalAddress(peer) || s.isTrustedProxy(peer) {
		return
	}
	now, p := s.now(), peer.String()
	if last := s.untrustedFwd.Load(); last != nil && last.peer == p && now.Sub(last.at) < time.Minute {
		return // behind such a proxy every request has the header: record at most once a minute
	}
	s.untrustedFwd.Store(&forwardingSeen{at: now, peer: p})
}

// UntrustedProxySeen returns when a request with a forwarding header last arrived from a local
// peer that is not one of the TrustedProxies, and that peer's address (zero time: none since
// start, or the peer has been added to the TrustedProxies since). The ReverseProxyCheck health
// check reports it.
func (s *Service) UntrustedProxySeen() (time.Time, string) {
	v := s.untrustedFwd.Load()
	if v == nil || s.isTrustedProxy(net.ParseIP(v.peer)) {
		// Once the proxy is trusted (Settings → General takes effect at once) the notice is stale.
		return time.Time{}, ""
	}
	return v.at, v.peer
}
