// Package netguard keeps Dupearr's outbound HTTP clients (notifications, Plex, the *arrs) away
// from addresses where no such service lives but where a server hands out credentials: link-local
// ranges (the cloud instance metadata service at 169.254.169.254, fe80::/10), AWS's IPv6 metadata
// endpoint and Alibaba Cloud's metadata address.
//
// Every connection URL is admin-chosen (or, for Plex, reported by plex.tv), so a leaked API key or
// a stolen session must not be enough to make Dupearr fetch cloud credentials. The check runs at
// connect time (CheckDialAddress, after name resolution, so DNS cannot point around it) and, when
// an HTTP(S) proxy is configured, before the request is handed to the proxy (Proxy): the dial
// check would only see the proxy's address there.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// blockedPrefixes are the refused destinations (see the package comment).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("100.100.100.200/32"),
}

// Blocked reports whether ip is a refused destination (IPv4-mapped IPv6 and zoned addresses
// included).
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckDialAddress is a net.Dialer Control hook: it refuses connections to blocked addresses. It
// runs with the resolved "ip:port" address.
func CheckDialAddress(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("connection to %q is not allowed (unexpected address)", address)
	}
	if ip := ap.Addr().Unmap().WithZone(""); Blocked(ip) {
		return fmt.Errorf("connections to %s are not allowed (link-local or cloud metadata address)", ip)
	}
	return nil
}

// NewDialer returns a dialer with the given timeout that refuses blocked addresses.
func NewDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: CheckDialAddress}
}

// lookupTimeout bounds the name resolution Proxy does before handing a request to a proxy.
const lookupTimeout = 10 * time.Second

// resolver is the resolver Proxy uses (a variable for tests).
var resolver = net.DefaultResolver

// Proxy wraps a Transport.Proxy function (nil means http.ProxyFromEnvironment): a request that
// would go through a proxy is refused when its host is, or resolves to, a blocked address — the
// proxy, not Dupearr, connects to it, so CheckDialAddress never sees that address. Requests that
// are not proxied are left to the dial check.
func Proxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	if next == nil {
		next = http.ProxyFromEnvironment
	}
	return func(req *http.Request) (*url.URL, error) {
		pu, err := next(req)
		if err != nil || pu == nil {
			return pu, err
		}
		if err := checkHost(req.Context(), req.URL.Hostname()); err != nil {
			return nil, err
		}
		return pu, nil
	}
}

// checkHost refuses a host that is a blocked address or whose name resolves to one. A name that
// cannot be resolved here is left to the proxy (it may know names Dupearr's resolver does not).
func checkHost(ctx context.Context, host string) error {
	if host == "" {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if Blocked(ip) {
			return fmt.Errorf("connections to %s are not allowed (link-local or cloud metadata address)", ip.Unmap().WithZone(""))
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if Blocked(ip) {
			return fmt.Errorf("connections to %s (%s) are not allowed (link-local or cloud metadata address)", host, ip.Unmap().WithZone(""))
		}
	}
	return nil
}
