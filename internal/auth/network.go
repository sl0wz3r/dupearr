package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// Reverse-proxy trust (docs/DECISIONS.md D1).
//
// Dupearr never believes a forwarding header (X-Forwarded-For, X-Real-IP, RFC 7239 Forwarded,
// X-Forwarded-Proto) because of where a request comes from alone: a client address is the TCP
// peer, unless the peer is one of the TrustedProxies. Then, and only then, the address the
// proxy recorded (the right-most entry that is not itself a trusted proxy) is the client.
//
// TrustedProxies (IP addresses or CIDR ranges) and AllowedHosts (host names Dupearr may be
// addressed by; "*.example.com" matches sub-domains) are read from the environment variables
// EnvTrustedProxies / EnvAllowedHosts, or from whatever SetNetworkTrust installs (config.xml).
//
// They decide:
//   - External authentication: the reverse proxy is only trusted when the TCP peer is a trusted
//     proxy (TrustedProxies set) and every host the request names is allowed (AllowedHosts set).
//     With neither list, External only trusts requests that name Dupearr by a private host (the
//     DNS-rebinding guard of None) — a public name then needs a login or the API key.
//   - the login throttling key (ClientIP);
//   - which requests count as local (IsLocalRequest): a request relayed by a trusted proxy is only
//     local when the proxy says so (a forwarding header naming a local client). A proxy that does
//     not say who the client is (no header) is not local.
//   - AllowedHosts also count as private hosts for None and for local access (an admin-listed name
//     cannot be a DNS-rebinding attacker's).

// Environment variables holding the reverse-proxy trust lists (comma- or space-separated).
const (
	EnvTrustedProxies = "DUPEARR__AUTH__TRUSTEDPROXIES"
	EnvAllowedHosts   = "DUPEARR__AUTH__ALLOWEDHOSTS"
)

// NetworkTrust is the parsed reverse-proxy configuration.
type NetworkTrust struct {
	// TrustedProxies are the reverse proxies whose forwarding headers are believed.
	TrustedProxies []netip.Prefix
	// AllowedHosts are lower-case host names (no port); "*.example.com" matches sub-domains.
	AllowedHosts []string
}

// Configured reports whether either list is set.
func (t NetworkTrust) Configured() bool {
	return len(t.TrustedProxies) > 0 || len(t.AllowedHosts) > 0
}

// ParseNetworkTrust parses comma-, semicolon- or whitespace-separated lists of trusted proxies (IP
// addresses or CIDR ranges) and allowed host names. Invalid entries are skipped and returned as
// an error (the valid ones still apply, so one typo does not disable the rest).
func ParseNetworkTrust(proxies, hosts string) (NetworkTrust, error) {
	var t NetworkTrust
	var bad []string
	for _, f := range splitList(proxies) {
		p, ok := parseProxyEntry(f)
		if !ok {
			bad = append(bad, "trusted proxy "+quoteEntry(f))
			continue
		}
		if !slices.Contains(t.TrustedProxies, p) {
			t.TrustedProxies = append(t.TrustedProxies, p)
		}
	}
	for _, f := range splitList(hosts) {
		h, ok := parseAllowedHost(f)
		if !ok {
			bad = append(bad, "allowed host "+quoteEntry(f))
			continue
		}
		if !slices.Contains(t.AllowedHosts, h) {
			t.AllowedHosts = append(t.AllowedHosts, h)
		}
	}
	if len(bad) > 0 {
		return t, fmt.Errorf("invalid entries ignored: %s", strings.Join(bad, ", "))
	}
	return t, nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func quoteEntry(s string) string {
	const limit = 64
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// Trusted-proxy ranges (GAP-08): "0.0.0.0/0", or any range that covers a large part of the
// internet, would trust every client, i.e. undo the proxy check, and a one-character slip
// ("172.0.0.0/8" for Docker's "172.16.0.0/12") must not do so either. A range is accepted when it
// lies entirely inside non-public space (nonPublicRanges), or else when it is at least /16 (IPv4)
// or /48 (IPv6: one site).
const (
	minPublicProxyBits4 = 16
	minPublicProxyBits6 = 48
)

// nonPublicRanges are the address ranges that never belong to internet clients: RFC 1918, CGNAT
// (RFC 6598), loopback, link-local and IPv6 unique local and link-local addresses.
var nonPublicRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

// acceptableProxyRange reports whether p may be a trusted-proxy range (see minPublicProxyBits4).
func acceptableProxyRange(p netip.Prefix) bool {
	for _, np := range nonPublicRanges {
		if np.Addr().Is4() == p.Addr().Is4() && np.Bits() <= p.Bits() && np.Contains(p.Addr()) {
			return true
		}
	}
	if p.Addr().Is4() {
		return p.Bits() >= minPublicProxyBits4
	}
	return p.Bits() >= minPublicProxyBits6
}

// parseProxyEntry parses "10.0.0.1", "[fd00::1]", "172.18.0.0/16" or "fd00::/8".
func parseProxyEntry(s string) (netip.Prefix, bool) {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]")
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return netip.Prefix{}, false
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		p = p.Masked()
		if p.Addr().Zone() != "" || !acceptableProxyRange(p) {
			return netip.Prefix{}, false
		}
		return p, true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

// parseAllowedHost normalises a host name entry: lower case, no port or trailing dot. A leading
// "*." allows every sub-domain.
func parseAllowedHost(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	wildcard := strings.HasPrefix(s, "*.")
	if wildcard {
		s = s[2:]
	}
	s = strings.TrimSuffix(hostName(s), ".")
	if s == "" || len(s) > 253 {
		return "", false
	}
	if net.ParseIP(s) == nil {
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
				return "", false
			}
		}
		if strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
			return "", false
		}
	} else if wildcard {
		return "", false
	}
	if wildcard {
		return "*." + s, true
	}
	return s, true
}

// trustSource supplies the raw lists; trustCache holds the last parse.
type trustCache struct {
	proxies, hosts string
	parsed         NetworkTrust
}

type networkTrustState struct {
	mu     sync.Mutex
	source func() (proxies, hosts string)
	cache  *trustCache
}

// envTrustSource reads EnvTrustedProxies and EnvAllowedHosts once.
func envTrustSource() func() (string, string) {
	proxies, _ := config.LookupEnv(EnvTrustedProxies)
	hosts, _ := config.LookupEnv(EnvAllowedHosts)
	return func() (string, string) { return proxies, hosts }
}

// SetNetworkTrust replaces where the TrustedProxies / AllowedHosts lists come from (e.g. the
// effective config.xml values). fn is called on every request that needs them and must be
// cheap; its result is parsed again only when it changes. A nil fn restores the environment
// variables.
func (s *Service) SetNetworkTrust(fn func() (trustedProxies, allowedHosts string)) {
	if fn == nil {
		fn = envTrustSource()
	}
	s.trust.mu.Lock()
	s.trust.source = fn
	s.trust.cache = nil
	s.trust.mu.Unlock()
}

// NetworkTrust returns the effective reverse-proxy configuration.
func (s *Service) NetworkTrust() NetworkTrust {
	s.trust.mu.Lock()
	defer s.trust.mu.Unlock()
	if s.trust.source == nil {
		s.trust.source = envTrustSource()
	}
	proxies, hosts := s.trust.source()
	if c := s.trust.cache; c != nil && c.proxies == proxies && c.hosts == hosts {
		return c.parsed
	}
	parsed, err := ParseNetworkTrust(proxies, hosts)
	if err != nil {
		s.log.Warn("Reverse-proxy trust settings: "+err.Error(), "trustedProxies", truncateHost(proxies), "allowedHosts", truncateHost(hosts))
	}
	s.trust.cache = &trustCache{proxies: proxies, hosts: hosts, parsed: parsed}
	return parsed
}

// isTrustedProxy reports whether ip is one of the TrustedProxies.
func (s *Service) isTrustedProxy(ip net.IP) bool {
	addr, ok := netipFromIP(ip)
	if !ok {
		return false
	}
	for _, p := range s.NetworkTrust().TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// trustedPeer reports whether the request's TCP peer is a trusted reverse proxy.
func (s *Service) trustedPeer(r *http.Request) bool {
	return s.isTrustedProxy(peerIP(r))
}

// hostAllowed reports whether host (a Host / X-Forwarded-Host value) is one of the AllowedHosts.
func (s *Service) hostAllowed(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(hostName(strings.TrimSpace(host))), ".")
	if h == "" {
		return false
	}
	for _, a := range s.NetworkTrust().AllowedHosts {
		if suffix, ok := strings.CutPrefix(a, "*"); ok {
			if strings.HasSuffix(h, suffix) && len(h) > len(suffix) {
				return true
			}
			continue
		}
		if h == a {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Client addresses
// ---------------------------------------------------------------------------

// IsLocalAddress reports whether ip is loopback, RFC1918, link-local or IPv6 ULA (fc00::/7).
// IPv4-mapped IPv6 addresses are unmapped first. A nil ip is not local.
func IsLocalAddress(ip net.IP) bool {
	if ip == nil {
		return false
	}
	addr, ok := netipFromIP(ip)
	if !ok {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// peerIP is the TCP peer of the request (nil when RemoteAddr cannot be parsed).
func peerIP(r *http.Request) net.IP {
	return parseHostIP(r.RemoteAddr)
}

// parseHostIP parses "ip", "ip:port", "[v6]" or "[v6]:port".
func parseHostIP(s string) net.IP {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if i := strings.IndexByte(s, '%'); i >= 0 { // zone
		s = s[:i]
	}
	return net.ParseIP(s)
}

// forwardedChain returns the X-Forwarded-For entries (all header lines, left to right).
func forwardedChain(r *http.Request) []string {
	var out []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// forwardedFor returns the node of every element of the RFC 7239 Forwarded header(s), left to
// right: the for= value, or "" for an element without one (an unknown client). present reports
// whether the header was sent at all.
func forwardedFor(r *http.Request) (nodes []string, present bool) {
	for _, line := range r.Header.Values("Forwarded") {
		present = true
		for _, elem := range splitQuoted(line, ',') {
			if strings.TrimSpace(elem) == "" {
				continue
			}
			node := ""
			for _, pair := range splitQuoted(elem, ';') {
				k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "for") {
					node = strings.Trim(strings.TrimSpace(v), `"`)
				}
			}
			nodes = append(nodes, node)
		}
	}
	return nodes, present
}

// splitQuoted splits s at sep outside double-quoted strings.
func splitQuoted(s string, sep byte) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && quoted:
			i++
		case c == '"':
			quoted = !quoted
		case c == sep && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// forwardingClaims returns every client address a forwarding header claims (X-Forwarded-For,
// Forwarded for=, X-Real-IP), and whether any such header was present. Unparsable or unknown
// entries are returned as "".
func forwardingClaims(r *http.Request) (claims []string, present bool) {
	claims = forwardedChain(r)
	present = len(r.Header.Values("X-Forwarded-For")) > 0
	if nodes, ok := forwardedFor(r); ok {
		claims = append(claims, nodes...)
		present = true
	}
	if vals := r.Header.Values("X-Real-IP"); len(vals) > 0 {
		present = true
		for _, v := range vals {
			claims = append(claims, strings.TrimSpace(v))
		}
	}
	return claims, present
}

// ClientIP returns the client's address, used for login throttling and logging: the TCP peer,
// unless the peer is a trusted proxy (TrustedProxies). Then it is the right-most address the
// proxies recorded that is not itself a trusted proxy — from X-Forwarded-For, else the RFC 7239
// Forwarded header, else X-Real-IP; an unparsable entry stops the walk at the peer. Forwarding
// headers from any other peer are ignored: they are chosen by the client.
func (s *Service) ClientIP(r *http.Request) net.IP {
	peer := peerIP(r)
	if peer == nil || !s.isTrustedProxy(peer) {
		return peer
	}
	chain := forwardedChain(r)
	if len(chain) == 0 {
		chain, _ = forwardedFor(r)
	}
	if len(chain) == 0 {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
			chain = []string{v}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		ip := parseHostIP(chain[i])
		if ip == nil {
			return peer
		}
		if !s.isTrustedProxy(ip) {
			return ip
		}
		if i == 0 {
			return ip // every hop is a trusted proxy: the left-most one is the client
		}
	}
	return peer
}

// IsLocalRequest reports whether the request comes from a local client (loopback, RFC 1918,
// link-local, IPv6 ULA):
//   - the TCP peer must be local;
//   - every client address a forwarding header claims (X-Forwarded-For, RFC 7239 Forwarded,
//     X-Real-IP) must be a parsable local address, so a spoofed left-hand entry cannot hide a
//     remote client (CVE-2026-30975) and an unknown or obfuscated one fails closed;
//   - a request from a trusted proxy (TrustedProxies) is only local when the proxy says who the
//     client is: without a forwarding header, the client behind it is unknown.
//
// The peer address alone does not prove a local client: Docker's userland proxy (IPv6, Docker
// Desktop), NAT loopback and reverse proxies that send no forwarding header all relay internet
// clients with a local peer address. Configure TrustedProxies for proxies; first-run setup
// additionally requires the setup code printed in the log.
func (s *Service) IsLocalRequest(r *http.Request) bool {
	peer := peerIP(r)
	if !IsLocalAddress(peer) {
		return false
	}
	claims, _ := forwardingClaims(r)
	if s.isTrustedProxy(peer) && len(claims) == 0 {
		// The proxy did not name the client (no header, or only an empty one it passed on).
		return false
	}
	for _, c := range claims {
		if !IsLocalAddress(parseHostIP(c)) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Host names (DNS-rebinding guard)
// ---------------------------------------------------------------------------

// privateHostSuffixes are DNS suffixes reserved for, or conventionally used on, private networks
// (RFC 6761 .localhost, RFC 6762 .local, RFC 8375 .home.arpa, ICANN's .internal, and common LAN
// suffixes that are not delegated in the public DNS). A DNS-rebinding attacker cannot obtain a
// name under them.
var privateHostSuffixes = []string{
	".localhost", ".local", ".lan", ".home", ".home.arpa", ".internal", ".intranet",
	".localdomain", ".corp", ".private",
}

// IsPrivateHost reports whether host (a Host / X-Forwarded-Host value, with or without a port)
// names this server in a way a DNS-rebinding attack cannot produce: an IP literal, "localhost", a
// single-label name (e.g. "tower", "dupearr") or a name under a private-use suffix (.local, .lan,
// .home.arpa, .internal, …). An empty host (HTTP/1.0 clients, which are not browsers) counts as
// private. Public DNS names (e.g. "dupearr.example.com") do not.
//
// This only defends browsers: a browser cannot choose the Host header, but any other client can,
// so a private Host never proves that a request comes from the local network.
func IsPrivateHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return true
	}
	h = hostName(h)
	if net.ParseIP(h) != nil {
		return true
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "" || strings.ContainsAny(h, "/\\@ \t") {
		return false
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range privateHostSuffixes {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// LocalAccessAllowed reports whether a request may use local-client privileges — the
// DisabledForLocalAddresses bypass and first-run setup: IsLocalRequest holds, and the request
// names this server by a local host (an IP literal that is a local address, or a private host
// name — IsPrivateHost — or one of the AllowedHosts), including every X-Forwarded-Host entry.
//
// The host check defeats DNS rebinding: a page on an attacker's domain that re-resolves to this
// server's LAN address runs in the victim's browser (a local client) and is same-origin with
// itself, so neither the client address nor the Origin check would stop it from driving the API.
// It does not stop non-browser clients, which choose the Host header freely: an internet client
// that reaches Dupearr with a local peer address (Docker's userland proxy, NAT loopback, a reverse
// proxy without forwarding headers) can claim any local Host. That is why first-run setup also
// requires the setup code, and why reverse proxies belong in TrustedProxies.
func (s *Service) LocalAccessAllowed(r *http.Request) bool {
	return s.IsLocalRequest(r) && requestHostsAll(r, func(h string) bool { return isLocalHost(h) || s.hostAllowed(h) })
}

// privateHosts reports whether the request names this server only by hosts a DNS-rebinding page
// cannot produce (IsPrivateHost, or AllowedHosts): the Host header and every X-Forwarded-Host
// entry. Authentication methods None and External (without TrustedProxies/AllowedHosts) trust only
// such requests: a page on an attacker's domain that re-resolves to this server is same-origin with
// itself, so the Origin check alone would let it drive the whole API.
func (s *Service) privateHosts(r *http.Request) bool {
	return requestHostsAll(r, func(h string) bool { return IsPrivateHost(h) || s.hostAllowed(h) })
}

// requestHostsAll reports whether ok holds for the Host header and every X-Forwarded-Host entry.
func requestHostsAll(r *http.Request, ok func(string) bool) bool {
	if !ok(r.Host) {
		return false
	}
	for _, line := range r.Header.Values("X-Forwarded-Host") {
		for _, h := range strings.Split(line, ",") {
			if h = strings.TrimSpace(h); h != "" && !ok(h) {
				return false
			}
		}
	}
	return true
}

// isLocalHost is IsPrivateHost, except that an IP literal must be a local address.
func isLocalHost(host string) bool {
	if !IsPrivateHost(host) {
		return false
	}
	if ip := net.ParseIP(hostName(strings.TrimSpace(host))); ip != nil {
		return IsLocalAddress(ip)
	}
	return true
}

// hostName strips the port, IPv6 brackets and zone from a Host header value.
func hostName(h string) string {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if i := strings.IndexByte(h, '%'); i >= 0 { // IPv6 zone
		h = h[:i]
	}
	return h
}

// truncateHost bounds a client-supplied host for logging.
func truncateHost(h string) string {
	const limit = 100
	if len(h) > limit {
		return h[:limit] + "…"
	}
	return h
}

func ipString(ip net.IP) string {
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}

// clientAttrs returns log attributes naming the client: its address and, when the address came
// from a trusted proxy's forwarding header, the proxy (TCP peer) too. A forwarding header sent by
// any other peer is logged as "claimed" (bounded): it was not believed, but it shows a reverse
// proxy that is missing from the TrustedProxies — or a client trying to spoof its address.
func (s *Service) clientAttrs(r *http.Request) []any {
	ip, peer := s.ClientIP(r), peerIP(r)
	attrs := []any{"ip", ipString(ip)}
	if peer != nil && !peer.Equal(ip) {
		attrs = append(attrs, "peer", ipString(peer))
	}
	if !s.isTrustedProxy(peer) {
		if claims, present := forwardingClaims(r); present {
			attrs = append(attrs, "claimed", truncateHost(strings.Join(claims, ", ")))
		}
	}
	return attrs
}
