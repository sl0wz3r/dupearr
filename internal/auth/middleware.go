package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// Via values: how a request was authenticated (Info.Via).
const (
	ViaAPIKey   = "apiKey"       // X-Api-Key header
	ViaCookie   = "cookie"       // Forms session cookie
	ViaLocal    = "localAddress" // AuthenticationRequired = DisabledForLocalAddresses
	ViaExternal = "external"     // AuthenticationMethod = External (reverse proxy)
	ViaNoAuth   = "none"         // AuthenticationMethod = None
)

// Info describes how the current request was authenticated. The middleware stores it in the
// request context of protected and webhook routes (see FromContext).
type Info struct {
	Authenticated bool   `json:"authenticated"`
	Via           string `json:"via,omitempty"`
	UserID        int64  `json:"-"` // Via == ViaCookie
	Remember      bool   `json:"-"` // Via == ViaCookie: persistent session
}

type ctxKey struct{}

// FromContext returns the authentication info of a request that passed through Middleware
// (zero Info — unauthenticated — otherwise).
func FromContext(ctx context.Context) Info {
	if v, ok := ctx.Value(ctxKey{}).(Info); ok {
		return v
	}
	return Info{}
}

// WithInfo returns a copy of ctx carrying info (for tests and internal callers).
func WithInfo(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// routeClass is the authentication policy of a path (relative to UrlBase).
type routeClass int

const (
	routePublic    routeClass = iota // no authentication (SPA shell, assets, ping, login, auth status)
	routeSetup                       // first-run setup (the handler enforces local + setupRequired)
	routeWebhook                     // webhook token (or, deprecated, the API key) required
	routeProtected                   // API key, session cookie, or what the method/requirement allows
)

// classify returns the policy of a request path (after UrlBase stripping). The path is cleaned
// first so "//api/…" or "/x/../api/…" cannot dodge the /api prefix check.
func classify(p string) routeClass {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	switch p {
	case "/ping", "/login", "/logout", "/api/v1/auth/status":
		return routePublic
	case "/api/v1/auth/setup":
		return routeSetup
	case "/initialize.json", "/api":
		return routeProtected
	}
	switch {
	case strings.HasPrefix(p, "/api/v1/webhook/"):
		return routeWebhook
	case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/backup/"):
		return routeProtected
	}
	return routePublic
}

// routeClassOf returns the policy of a request: the stricter of the policies of its decoded path
// (URL.Path) and of the path the router matches (routedPath of URL.EscapedPath).
//
// net/http.ServeMux matches the escaped path segment by segment, unescaping each segment on its
// own, so "/api/v1/log/file/..%2F..%2F..%2F..%2Fx" reaches the {filename} handler with
// "../../../../x", while the decoded path "/api/v1/log/file/../../../../x" cleans to "/x", a
// public path. Classifying only the decoded path would let such a request into a protected
// handler without credentials.
func routeClassOf(u *url.URL) routeClass {
	c := classify(u.Path)
	if rc := classify(routedPath(u.EscapedPath())); rc > c {
		c = rc
	}
	return c
}

// routedPath returns the path as the router sees it: every segment of the escaped path is
// unescaped, except a segment that would then contain "/" or become a "." or ".." segment,
// which keeps its escaped form (for the router it is one ordinary segment, so classify's
// path.Clean must neither split it nor apply it).
func routedPath(escaped string) string {
	if !strings.Contains(escaped, "%") {
		return escaped
	}
	segs := strings.Split(escaped, "/")
	for i, seg := range segs {
		u, err := url.PathUnescape(seg)
		if err != nil || strings.Contains(u, "/") || (u != seg && (u == "." || u == "..")) {
			continue
		}
		segs[i] = u
	}
	return strings.Join(segs, "/")
}

// keyState is the outcome of the API-key check.
type keyState int

const (
	keyAbsent keyState = iota
	keyValid
	keyInvalid
)

// apiKey checks the X-Api-Key header (constant-time compare). A present but wrong key is
// keyInvalid (never rescued by another credential). The master key is never accepted in the URL:
// a key in a query string leaks into reverse-proxy and CDN access logs, browser history and
// download lists, and a key-authenticated request skips the cross-site check. A request carrying
// an apikey query parameter is therefore keyInvalid (401: send the X-Api-Key header), whatever
// its value. Only the webhook routes take a credential in the query (the webhook token; see
// checkWebhook), because Radarr, Sonarr and Plex cannot send headers.
func (s *Service) apiKey(r *http.Request) keyState {
	if r.URL.Query().Has("apikey") {
		return keyInvalid
	}
	provided := r.Header.Get("X-Api-Key")
	if provided == "" {
		return keyAbsent
	}
	want := s.cfg.Get().ApiKey
	if want == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(want)) != 1 {
		return keyInvalid
	}
	return keyValid
}

// authenticate resolves the request's credentials. A valid session cookie is renewed when stale
// (sliding expiration). ok=false means the request is not authenticated.
func (s *Service) authenticate(w http.ResponseWriter, r *http.Request) (Info, bool) {
	if s.LockedDown() {
		return Info{}, false
	}
	switch s.apiKey(r) {
	case keyValid:
		return Info{Authenticated: true, Via: ViaAPIKey}, true
	case keyInvalid:
		return Info{}, false
	}
	method := s.method()
	switch method {
	case config.AuthNone:
		// No credentials, but only for requests that cannot come from a DNS-rebinding page
		// (see privateHosts): with None, such a page would otherwise own the whole API.
		if !s.privateHosts(r) {
			s.warnNoneHost(r)
			return Info{}, false
		}
		return Info{Authenticated: true, Via: ViaNoAuth}, true
	case config.AuthExternal:
		if s.externalTrusted(r) {
			return Info{Authenticated: true, Via: ViaExternal}, true
		}
		// Not relayed by the trusted proxy: a session of a local account (if one exists) or the
		// API key still work.
	}
	if sess, ok := s.verifySession(r); ok {
		if w != nil {
			s.renewIfStale(w, r, sess)
		}
		return Info{Authenticated: true, Via: ViaCookie, UserID: sess.userID, Remember: sess.remember}, true
	}
	if method == config.AuthForms && s.cfg.Get().AuthenticationRequired == config.AuthRequiredDisabledForLocal && s.LocalAccessAllowed(r) {
		return Info{Authenticated: true, Via: ViaLocal}, true
	}
	return Info{}, false
}

// externalTrusted reports whether External authentication may trust the request, i.e. whether
// it was relayed (and therefore authenticated) by the reverse proxy:
//   - with TrustedProxies set, the TCP peer must be one of them;
//   - with AllowedHosts set, the Host header and every X-Forwarded-Host entry must be allowed;
//   - with neither, the request must name this server by a private host (the DNS-rebinding guard
//     of None). That stops a rebinding page, but not a client on the network that reaches the
//     port directly — set TrustedProxies.
func (s *Service) externalTrusted(r *http.Request) bool {
	t := s.NetworkTrust()
	if !t.Configured() {
		if !s.privateHosts(r) {
			s.warnExternal(r, "public host name (set the trusted proxies / allowed hosts)")
			return false
		}
		return true
	}
	if len(t.TrustedProxies) > 0 && !s.trustedPeer(r) {
		s.warnExternal(r, "not relayed by a trusted proxy")
		return false
	}
	if len(t.AllowedHosts) > 0 && !requestHostsAll(r, s.hostAllowed) {
		s.warnExternal(r, "host name not allowed")
		return false
	}
	return true
}

// Authenticated reports whether the request carries valid credentials (or needs none: method
// None/External for requests they trust, or a local client with DisabledForLocalAddresses). It
// does not renew cookies.
func (s *Service) Authenticated(r *http.Request) bool {
	if info := FromContext(r.Context()); info.Authenticated {
		return true
	}
	_, ok := s.authenticate(nil, r)
	return ok
}

// Middleware enforces auth for everything except the allow-list (/ping, /login, /logout,
// static assets, /favicon*, /initialize.json when a valid session exists is still auth'd).
// The API key works for /api/* (X-Api-Key header only; an apikey query parameter is refused).
// Forms → session cookie (HMAC-signed, 7-day sliding, revocable); External → trust requests
// relayed by the trusted reverse proxy (see externalTrusted); None → allow requests that name
// this server by an IP or a private host name (a DNS-rebinding guard; others need the API key).
// Basic is not supported (a stored Basic is treated as Forms).
// DisabledForLocalAddresses bypasses auth for RFC1918/loopback/link-local/ULA clients (the TCP
// peer; forwarding headers only from trusted proxies, see IsLocalRequest) that address this
// server by a private host name or IP (LocalAccessAllowed — a DNS-rebinding guard).
// First-run: when method is Forms and no user exists, /api/v1/auth/setup is reachable
// without auth (the API handler restricts it to LocalAccessAllowed and the setup code).
//
// A request's policy is the stricter of the policies of its decoded path and of the path the
// router matches (routeClassOf), so encoded slashes or dots cannot move it to a public path.
//
// Paths are expected relative to UrlBase (strip it before this middleware). Public paths: /ping,
// /login, /logout, /api/v1/auth/status, and every non-API path except /initialize.json and
// /backup/… (the SPA shell and static assets; the SPA redirects to /login itself).
// /api/v1/webhook/* requires the webhook token (or, deprecated, the API key); the webhook token
// is refused everywhere else. Unauthenticated protected requests get 401
// {"message":"Unauthorized"}. State-changing requests that were not authenticated by the API key
// must be same-origin (Origin, else Referer) or get 403 — this covers cookie sessions, the
// local-address bypass and the None/External methods against cross-site request forgery.
// Rejected requests that announced a body are answered with "Connection: close", so the server
// does not wait for (or drain) a body nobody will read.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.noteUntrustedForwarding(r)
		class := routeClassOf(r.URL)
		if class != routePublic && s.LockedDown() {
			// `dupearr reset-auth` is being applied: no credential counts until the restart.
			w.Header().Set("Retry-After", "5")
			reject(w, r, http.StatusServiceUnavailable, "Authentication is being reset; try again in a moment")
			return
		}
		switch class {
		case routePublic:
			// Credentials are not resolved here (static assets would each cost a user lookup);
			// handlers that care call Authenticated.
			next.ServeHTTP(w, r)

		case routeSetup:
			if stateChanging(r.Method) && !s.sameOrigin(r, false) {
				reject(w, r, http.StatusForbidden, "Cross-site request rejected")
				return
			}
			next.ServeHTTP(w, r)

		case routeWebhook:
			var info Info
			switch s.checkWebhook(r) {
			case webhookByToken:
				info = Info{Authenticated: true, Via: ViaWebhookToken}
			case webhookByMasterKey:
				s.warnWebhookMasterKey(r)
				info = Info{Authenticated: true, Via: ViaAPIKey}
			default:
				s.log.Debug("Auth-Unauthorized", append(s.clientAttrs(r), "path", r.URL.Path)...)
				reject(w, r, http.StatusUnauthorized, "Unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithInfo(r.Context(), info)))

		default:
			info, ok := s.authenticate(w, r)
			if !ok {
				s.log.Debug("Auth-Unauthorized", append(s.clientAttrs(r), "path", r.URL.Path)...)
				reject(w, r, http.StatusUnauthorized, "Unauthorized")
				return
			}
			if stateChanging(r.Method) && info.Via != ViaAPIKey && !s.sameOrigin(r, true) {
				// Sampled and bounded: a malicious page can loop such requests through a visitor's
				// browser (e.g. under DisabledForLocalAddresses) with ~64 KiB paths.
				s.warnSampled("crossSite", "Rejected a cross-site request",
					append(s.clientAttrs(r), "method", r.Method, "path", truncatePath(r.URL.Path))...)
				reject(w, r, http.StatusForbidden, "Cross-site request rejected: send the X-Api-Key header or use the Dupearr web UI")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithInfo(r.Context(), info)))
		}
	})
}

// reject writes a JSON error without reading the request body; when the request announced one,
// the connection is closed after the response instead of waiting for the body.
func reject(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.ContentLength != 0 {
		w.Header().Set("Connection", "close")
	}
	writeJSONError(w, status, msg)
}

// stateChanging reports whether a method may change server state.
func stateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// sameOrigin reports whether the request's Origin (else Referer) names this server. When neither
// header is present the result is !required.
func (s *Service) sameOrigin(r *http.Request, required bool) bool {
	src := strings.TrimSpace(r.Header.Get("Origin"))
	if src == "" {
		src = strings.TrimSpace(r.Header.Get("Referer"))
		if src == "" {
			return !required
		}
	}
	if src == "null" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	for _, h := range s.requestHosts(r) {
		if sameHostPort(u, h) {
			return true
		}
	}
	return false
}

// requestHosts returns the host names this request was addressed to: the Host header, plus
// X-Forwarded-Host when a local or trusted reverse proxy forwarded the request. (A browser cannot
// send X-Forwarded-Host cross-site without a CORS preflight, which Dupearr never grants.)
func (s *Service) requestHosts(r *http.Request) []string {
	hosts := []string{r.Host}
	if peer := peerIP(r); IsLocalAddress(peer) || s.isTrustedProxy(peer) {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			if i := strings.IndexByte(fh, ','); i >= 0 {
				fh = fh[:i]
			}
			if fh = strings.TrimSpace(fh); fh != "" {
				hosts = append(hosts, fh)
			}
		}
	}
	return hosts
}

// sameHostPort compares the origin URL with a Host header value. Missing ports default to the
// origin scheme's port on both sides (a TLS-terminating proxy may change the scheme).
func sameHostPort(origin *url.URL, host string) bool {
	if host == "" {
		return false
	}
	oh, op := origin.Hostname(), origin.Port()
	hh, hp := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		hh, hp = h, p
	}
	hh = strings.TrimSuffix(strings.TrimPrefix(hh, "["), "]")
	def := "80"
	if origin.Scheme == "https" {
		def = "443"
	}
	if op == "" {
		op = def
	}
	if hp == "" {
		hp = def
	}
	return strings.EqualFold(oh, hh) && op == hp
}

// writeJSONError writes {"message": msg}.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"message":` + quoteJSON(msg) + "}\n"))
}

// quoteJSON quotes s as a JSON string.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
