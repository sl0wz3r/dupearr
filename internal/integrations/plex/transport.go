package plex

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/netguard"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	// maxJSONBody caps one PMS response (a listing page of 100 rows, an item's detail, the
	// sessions): legitimate ones are well under 1 MiB, so a larger body is a broken or hostile
	// server rather than a big library.
	maxJSONBody = 16 << 20
	// maxJSONValues caps the JSON values (objects, arrays, members) of one PMS response before it
	// is decoded: "{}," is three bytes but decodes into a struct of hundreds of bytes, so the body
	// cap alone does not bound memory. A page of 100 rows has ~10k.
	maxJSONValues   = 200_000
	maxPlexTVBody   = 8 << 20  // plex.tv responses
	maxErrorExcerpt = 200      // runes of an error body kept in StatusError
	drainLimit      = 64 << 10 // bytes drained before closing a body (connection reuse)
)

// withDefaults fills unset options.
func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	o.Product = strings.TrimSpace(o.Product)
	if o.Product == "" {
		o.Product = defaultProduct
	}
	if strings.TrimSpace(o.Version) == "" {
		o.Version = version.Version
	}
	if strings.TrimSpace(o.Platform) == "" {
		o.Platform = platformName()
	}
	if strings.TrimSpace(o.Device) == "" {
		o.Device = o.Product
	}
	if strings.TrimSpace(o.DeviceName) == "" {
		o.DeviceName = o.Product
	}
	o.ClientIdentifier = strings.TrimSpace(o.ClientIdentifier)
	o.PlexTVURL = strings.TrimRight(strings.TrimSpace(o.PlexTVURL), "/")
	if o.PlexTVURL == "" {
		o.PlexTVURL = defaultPlexTVURL
	}
	o.ClientsPlexTVURL = strings.TrimRight(strings.TrimSpace(o.ClientsPlexTVURL), "/")
	if o.ClientsPlexTVURL == "" {
		o.ClientsPlexTVURL = defaultClientsPlexTVURL
	}
	return o
}

func platformName() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "freebsd":
		return "FreeBSD"
	default:
		return runtime.GOOS
	}
}

// setHeaders sets the standard Plex headers (docs/research/plex-api.md §2.2). The token, when
// non-empty, is only ever sent as a header.
func (o Options) setHeaders(h http.Header, token string) {
	h.Set("Accept", "application/json")
	h.Set("User-Agent", version.UserAgent())
	if o.ClientIdentifier != "" {
		h.Set("X-Plex-Client-Identifier", o.ClientIdentifier)
	}
	h.Set("X-Plex-Product", o.Product)
	h.Set("X-Plex-Version", o.Version)
	h.Set("X-Plex-Platform", o.Platform)
	h.Set("X-Plex-Device", o.Device)
	h.Set("X-Plex-Device-Name", o.DeviceName)
	if token != "" {
		h.Set("X-Plex-Token", token)
	}
}

func sanitizeToken(t string) string { return strings.TrimSpace(t) }

// ---------------------------------------------------------------------------
// Base URL
// ---------------------------------------------------------------------------

// baseURL is a validated server base URL (scheme, host, optional path prefix).
type baseURL struct{ u url.URL }

func parseBaseURL(raw string) (*baseURL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("%w: server URL is empty", ErrInvalidArgument)
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		// The parse error echoes the input, which may carry credentials: keep it out.
		return nil, fmt.Errorf("%w: server URL is not a valid URL", ErrInvalidArgument)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: server URL must use http or https", ErrInvalidArgument)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: server URL has no host", ErrInvalidArgument)
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return &baseURL{u: *u}, nil
}

// endpoint appends an already-escaped path (starting with "/") to the base URL's path prefix —
// never a URL-reference resolution that would drop the prefix — and sets the query.
func (b *baseURL) endpoint(escPath string, q url.Values) string {
	u := b.u
	raw := strings.TrimRight(b.u.EscapedPath(), "/") + escPath
	if p, err := url.PathUnescape(raw); err == nil {
		u.Path, u.RawPath = p, raw
	} else {
		u.Path, u.RawPath = raw, ""
	}
	u.RawQuery = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

// Shared transports, one per TLS mode, built on first use and never modified afterwards (an
// *http.Transport is safe for concurrent use). Callers build a Client per operation (the
// factories in cmd/dupearr create one per call, the poster proxy one per image), so a transport
// per Client would leave an idle keep-alive connection (file descriptor + goroutines) behind for
// every call; sharing lets connections to the same server be reused instead.
var (
	sharedVerifyingTransport = sync.OnceValue(func() *http.Transport { return newTransport(true) })
	sharedInsecureTransport  = sync.OnceValue(func() *http.Transport { return newTransport(false) })
)

// newHTTPClient builds the HTTP client for o: o.HTTPClient (copied, with this package's redirect
// policy) when set, else a client on the shared transport for verifyTLS.
func newHTTPClient(o Options, verifyTLS bool) *http.Client {
	if o.HTTPClient != nil {
		c := *o.HTTPClient
		if c.Timeout <= 0 {
			c.Timeout = o.Timeout
		}
		c.CheckRedirect = checkRedirect
		return &c
	}
	tr := sharedInsecureTransport()
	if verifyTLS {
		tr = sharedVerifyingTransport()
	}
	return &http.Client{Timeout: o.Timeout, Transport: tr, CheckRedirect: checkRedirect}
}

// dialTimeout bounds establishing a TCP connection (http.DefaultTransport's value).
const dialTimeout = 30 * time.Second

// newTransport returns a transport with Go's defaults (proxy from environment, HTTP/2, pooled
// keep-alives), TLS 1.2+, and certificate verification unless verifyTLS is false.
func newTransport(verifyTLS bool) *http.Transport {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		tr = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     true,
		}
	}
	// Link-local and cloud-metadata addresses are refused, also behind an HTTP(S) proxy
	// (netguard): no Plex server lives there, but a cloud metadata service hands out credentials.
	tr.DialContext = netguard.NewDialer(dialTimeout).DialContext
	tr.Proxy = netguard.Proxy(tr.Proxy)
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Opt-in for self-signed PMS certificates (MediaServer.VerifyTLS=false).
		InsecureSkipVerify: !verifyTLS, //nolint:gosec // user-controlled toggle, see above
	}
	tr.MaxIdleConnsPerHost = 8 // listing + concurrent item-detail fetches against one server
	return tr
}

// checkRedirect follows at most 5 redirects for GET/HEAD only, and only on the same origin
// (scheme, host and port — a URL-base fix-up by a reverse proxy) or, for plex.tv, between https
// plex.tv hosts. Anything else fails with ErrRedirect naming the target host and is never
// requested: a compromised PMS (or a MITM on a plain-http LAN connection) could otherwise point
// Dupearr at an internal service and have its answer reflected in errors, logs and notifications.
// Mutating requests (DELETE, PUT, POST) are never redirected — a redirected DELETE could land on a
// different endpoint — and X-Plex-Token is dropped whenever a redirect changes the host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	orig := via[0]
	if orig.Method != http.MethodGet && orig.Method != http.MethodHead {
		return http.ErrUseLastResponse
	}
	if len(via) >= 5 {
		return errors.New("stopped after 5 redirects")
	}
	if !sameOrigin(orig.URL, req.URL) && !(isPlexTVURL(orig.URL) && isPlexTVURL(req.URL)) {
		target := req.URL.Scheme + "://" + req.URL.Host
		if len(target) > 300 {
			target = target[:300] + "…"
		}
		return &redirectError{target: strings.ToValidUTF8(target, "")}
	}
	if !strings.EqualFold(req.URL.Hostname(), orig.URL.Hostname()) {
		req.Header.Del("X-Plex-Token")
	}
	return nil
}

// redirectError is a refused redirect; it names only the target's scheme and host (never its path
// or query).
type redirectError struct{ target string }

func (e *redirectError) Error() string {
	return "plex: the server redirected to " + e.target + "; redirects are only followed on the same server (update the server URL)"
}

func (e *redirectError) Unwrap() error { return ErrRedirect }

// sameOrigin reports whether a and b have the same scheme, host (case-insensitive) and port
// (default ports made explicit).
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// isPlexTVURL reports an https URL on plex.tv or one of its subdomains (clients.plex.tv, …).
func isPlexTVURL(u *url.URL) bool {
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return strings.EqualFold(u.Scheme, "https") && (h == "plex.tv" || strings.HasSuffix(h, ".plex.tv"))
}

// asRedirectError unwraps a refused redirect from a transport error (dropping the *url.Error
// wrapper, whose text repeats the redirect target's full URL).
func asRedirectError(err error) (*redirectError, bool) {
	var re *redirectError
	ok := errors.As(err, &re)
	return re, ok
}

// ---------------------------------------------------------------------------
// PMS requests
// ---------------------------------------------------------------------------

// do sends one request to the server. escPath must already be escaped.
func (c *Client) do(ctx context.Context, method, escPath string, q url.Values, extra http.Header) (*http.Response, error) {
	if c.baseErr != nil {
		return nil, c.baseErr
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.endpoint(escPath, q), nil)
	if err != nil {
		return nil, fmt.Errorf("plex: %s %s: build request: %w", method, escPath, err)
	}
	c.opts.setHeaders(req.Header, c.token)
	for k, vs := range extra {
		req.Header[http.CanonicalHeaderKey(k)] = vs
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if re, ok := asRedirectError(err); ok {
			return nil, fmt.Errorf("plex: %s %s: %w", method, escPath, re)
		}
		if quotesPeerData(err) {
			return nil, fmt.Errorf("plex: %s %s: %w", method, escPath, errNotHTTP)
		}
		return nil, fmt.Errorf("plex: %s %s: %w", method, escPath, redactURLError(err))
	}
	return resp, nil
}

// getJSON GETs escPath and decodes the JSON body into out. Idempotent, so it is retried once on
// transport errors and 502/503/504 (never when ctx is done).
func (c *Client) getJSON(ctx context.Context, escPath string, q url.Values, extra http.Header, out any) (http.Header, error) {
	var lastErr error
	for attempt := 0; attempt < getAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryDelay); err != nil {
				return nil, fmt.Errorf("plex: GET %s: %w", escPath, err)
			}
		}
		resp, err := c.do(ctx, http.MethodGet, escPath, q, extra)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrRedirect) {
				return nil, err
			}
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			lastErr = checkStatus(resp, http.MethodGet, escPath, true)
			drainClose(resp.Body)
			continue
		}
		return decodeJSONResponse(resp, http.MethodGet, escPath, out)
	}
	return nil, lastErr
}

// errNoContainer is returned for a JSON body without a "MediaContainer" object.
var errNoContainer = errors.New(`response has no "MediaContainer" object (unsupported PMS version or not a Plex Media Server)`)

// getContainer GETs escPath (see getJSON) and returns its MediaContainer, never nil on success:
// a body without one is an error rather than an empty result.
func (c *Client) getContainer(ctx context.Context, escPath string, q url.Values, extra http.Header) (*containerDTO, http.Header, error) {
	var resp containerResponse
	h, err := c.getJSON(ctx, escPath, q, extra, &resp)
	if err != nil {
		return nil, nil, err
	}
	if resp.MediaContainer == nil {
		return nil, nil, fmt.Errorf("plex: GET %s: %w", escPath, errNoContainer)
	}
	return resp.MediaContainer, h, nil
}

// action sends a request whose success body is empty (DELETE/PUT/refresh). The body is never
// decoded; any 2xx is success.
func (c *Client) action(ctx context.Context, method, escPath string, q url.Values) error {
	resp, err := c.do(ctx, method, escPath, q, nil)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	return checkStatus(resp, method, escPath, true)
}

func decodeJSONResponse(resp *http.Response, method, escPath string, out any) (http.Header, error) {
	defer drainClose(resp.Body)
	if err := checkStatus(resp, method, escPath, true); err != nil {
		return nil, err
	}
	body, err := readLimited(resp.Body, maxJSONBody)
	if err != nil {
		return nil, fmt.Errorf("plex: %s %s: read response: %w", method, escPath, err)
	}
	if err := decodeJSONBody(body, out); err != nil {
		return nil, fmt.Errorf("plex: %s %s: %w", method, escPath, err)
	}
	return resp.Header, nil
}

var errNotJSON = errors.New("response is XML/HTML, not JSON (is this URL a Plex Media Server?)")

// errNotHTTP replaces transport errors that quote a non-HTTP peer's bytes (quotesPeerData).
var errNotHTTP = errors.New("the server's answer is not HTTP (not a Plex Media Server, or the wrong port or scheme?)")

// quotesPeerData reports a net/http transport error whose text quotes what the peer sent ("malformed
// HTTP status code \"Ubuntu-…\"", "malformed MIME header …"). The server URL is free-form, so such
// text would echo the banner of whatever service answers there (connection tests).
func quotesPeerData(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "malformed HTTP") || strings.Contains(msg, "malformed MIME header")
}

// decodeJSONBody decodes a JSON document, rejecting empty and XML/HTML bodies with a clear error.
func decodeJSONBody(body []byte, out any) error {
	b := trimBody(body)
	if len(b) == 0 {
		return errors.New("empty response body")
	}
	if b[0] == '<' {
		return errNotJSON
	}
	if tooManyJSONValues(b, maxJSONValues) {
		return fmt.Errorf("response has more than %d JSON values (not a plausible Plex response)", maxJSONValues)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// tooManyJSONValues reports whether b holds more than limit JSON values, counted without decoding:
// every '{', '[' and ',' outside strings (an upper bound of the objects, arrays and elements that
// decoding would allocate).
func tooManyJSONValues(b []byte, limit int) bool {
	n := 0
	inString, escaped := false, false
	for _, c := range b {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[', ',':
			if n++; n > limit {
				return true
			}
		}
	}
	return false
}

func trimBody(b []byte) []byte {
	b = bytes.TrimPrefix(bytes.TrimSpace(b), []byte("\xef\xbb\xbf")) // UTF-8 BOM
	return bytes.TrimSpace(b)
}

// checkStatus maps an HTTP status to an error: 2xx → nil, 401 → ErrUnauthorized, 403 →
// ErrForbidden, 404 → ErrNotFound, anything else → *StatusError (with a sanitized body excerpt
// when withBody).
func checkStatus(resp *http.Response, method, escPath string, withBody bool) error {
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return fmt.Errorf("plex: %s %s: %w", method, escPath, ErrUnauthorized)
	case code == http.StatusForbidden:
		return fmt.Errorf("plex: %s %s: %w", method, escPath, ErrForbidden)
	case code == http.StatusNotFound:
		return fmt.Errorf("plex: %s %s: %w", method, escPath, ErrNotFound)
	default:
		se := &StatusError{Method: method, Path: escPath, StatusCode: code}
		if withBody {
			se.Body = errorExcerpt(resp.Body)
		}
		return se
	}
}

var (
	reHTMLTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reHTMLBlock = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
)

// errorExcerpt returns a short single-line, tag-free excerpt of an error body.
func errorExcerpt(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	s := string(b)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = reHTMLBlock.ReplaceAllString(s, " ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > maxErrorExcerpt {
		s = string(rs[:maxErrorExcerpt]) + "…"
	}
	return s
}

// readLimited reads at most limit bytes and fails when the body is larger.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return b, nil
}

// drainClose drains (bounded) and closes a response body so the connection can be reused.
func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, drainLimit))
	_ = rc.Close()
}

// redactURLError strips credentials from a *url.Error's URL (userinfo in the base URL).
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			ue.URL = u.Redacted()
		} else {
			ue.URL = "(invalid url)"
		}
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
