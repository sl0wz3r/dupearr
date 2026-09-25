package jellyfin

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
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/netguard"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	// maxJSONBody caps one answer (a listing page of 200 rows with their streams is a few MiB).
	maxJSONBody = 16 << 20
	// maxJSONValues caps the JSON values of one answer before it is decoded ("{}," is three bytes
	// but decodes into a struct of hundreds of bytes, so the body cap alone does not bound memory).
	maxJSONValues = 400_000
	// drainLimit bounds what is read of a body before closing it (connection reuse).
	drainLimit = 64 << 10
	// dialTimeout bounds establishing a TCP connection.
	dialTimeout = 30 * time.Second
	// maxNotifyPaths caps the paths of one change notification.
	maxNotifyPaths = 500
)

// ---------------------------------------------------------------------------
// Base URL
// ---------------------------------------------------------------------------

// parseBaseURL validates the connection URL: http/https with a host. Userinfo, query and fragment
// are dropped (a stale ?ApiKey= must never be sent); a path prefix is kept (a reverse proxy's
// /jellyfin, or Jellyfin's own "Base URL").
func parseBaseURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("%w: the server URL is empty", ErrInvalidArgument)
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		// The parse error echoes the input, which may carry credentials: keep it out.
		return nil, fmt.Errorf("%w: the server URL is not a valid URL", ErrInvalidArgument)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: the server URL must use http or https", ErrInvalidArgument)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: the server URL has no host", ErrInvalidArgument)
	}
	u.User = nil
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

// Shared transports, one per TLS mode, built on first use and never modified afterwards: clients
// are built per operation, so a transport per client would leave idle connections behind.
var (
	sharedVerifyingTransport = sync.OnceValue(func() *http.Transport { return newTransport(true) })
	sharedInsecureTransport  = sync.OnceValue(func() *http.Transport { return newTransport(false) })
)

// newHTTPClient builds the HTTP client: opts.HTTPClient (copied) or one on the shared transport.
// Redirects are never followed.
func newHTTPClient(o Options) *http.Client {
	if o.HTTPClient != nil {
		c := *o.HTTPClient
		if c.Timeout <= 0 {
			c.Timeout = o.Timeout
		}
		c.CheckRedirect = checkRedirect
		return &c
	}
	tr := sharedInsecureTransport()
	if o.VerifyTLS {
		tr = sharedVerifyingTransport()
	}
	return &http.Client{Timeout: o.Timeout, Transport: tr, CheckRedirect: checkRedirect}
}

// newTransport returns a transport with Go's defaults, TLS 1.2+, and certificate verification
// unless verifyTLS is false.
func newTransport(verifyTLS bool) *http.Transport {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	}
	// Link-local and cloud-metadata addresses are refused, also behind a proxy (netguard).
	tr.DialContext = netguard.NewDialer(dialTimeout).DialContext
	tr.Proxy = netguard.Proxy(tr.Proxy)
	tr.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Opt-in for self-signed certificates (MediaServer.VerifyTLS=false).
		InsecureSkipVerify: !verifyTLS, //nolint:gosec // user-controlled per connection
	}
	tr.MaxIdleConnsPerHost = 8
	return tr
}

// checkRedirect refuses every redirect: a compromised server (or a MITM on a plain-http LAN
// connection) could otherwise point Dupearr at an internal service. The error names only the
// target's scheme and host. A redirect to the same scheme and host is what Jellyfin answers when it
// runs with a Base URL the server URL lacks, so the error says how to fix that.
func checkRedirect(req *http.Request, via []*http.Request) error {
	target := req.URL.Scheme + "://" + req.URL.Host
	if len(target) > 300 {
		target = target[:300] + "…"
	}
	same := len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, req.URL.Scheme) && strings.EqualFold(via[0].URL.Host, req.URL.Host)
	return &redirectError{target: strings.ToValidUTF8(target, ""), sameHost: same}
}

// redirectError is a refused redirect (only the target's scheme and host). Its text has no package
// prefix: do() wraps it with the request.
type redirectError struct {
	target   string
	sameHost bool
}

func (e *redirectError) Error() string {
	msg := "the server redirected to " + e.target + "; redirects are never followed"
	if e.sameHost {
		return msg + " (if Jellyfin runs with a Base URL, add it to the server URL, for example " + e.target + "/jellyfin)"
	}
	return msg + " (update the server URL)"
}

func (e *redirectError) Unwrap() error { return ErrRedirect }

// ---------------------------------------------------------------------------
// Allowlist (research §5.3.3; the control for S1–S3, S15–S17, S24)
// ---------------------------------------------------------------------------

// routeSpec is one allowlisted request: method, path segments ("{id}" = a 32-hex id), whether the
// credential is sent, and the query keys it may carry.
type routeSpec struct {
	method string
	segs   []string
	auth   bool
	query  map[string]func(string) bool
	body   bool
}

// Query value checks.
var (
	reDigits = regexp.MustCompile(`^[0-9]{1,9}$`)
	// idList accepts 1–200 comma-separated ids.
	idList = func(s string) bool {
		return strings.Count(s, ",") < 200 && listOf(reID.MatchString)(s)
	}
	allowedFld = map[string]bool{
		"MediaSources": true, "Path": true, "ProviderIds": true, "MediaSourceCount": true,
		"PartCount": true, "DateCreated": true, "ParentId": true, "MediaStreams": true,
	}
)

func oneOf(vals ...string) func(string) bool {
	return func(s string) bool {
		for _, v := range vals {
			if s == v {
				return true
			}
		}
		return false
	}
}

func listOf(ok func(string) bool) func(string) bool {
	return func(s string) bool {
		if s == "" {
			return false
		}
		for _, p := range strings.Split(s, ",") {
			if !ok(p) {
				return false
			}
		}
		return true
	}
}

// routes is the complete list of requests the client can send. Nothing else is ever built.
var routes = []routeSpec{
	{method: http.MethodGet, segs: []string{"System", "Info", "Public"}},
	{method: http.MethodGet, segs: []string{"System", "Info"}, auth: true},
	{method: http.MethodGet, segs: []string{"System", "Configuration"}, auth: true},
	{method: http.MethodGet, segs: []string{"Library", "VirtualFolders"}, auth: true},
	{method: http.MethodGet, segs: []string{"Items"}, auth: true, query: map[string]func(string) bool{
		"ParentId":               reID.MatchString,
		"Recursive":              oneOf("true"),
		"IncludeItemTypes":       listOf(oneOf("Movie", "Episode", "Series")),
		"Fields":                 listOf(func(s string) bool { return allowedFld[s] }),
		"StartIndex":             reDigits.MatchString,
		"Limit":                  reDigits.MatchString,
		"EnableTotalRecordCount": oneOf("true"),
		"SortBy":                 listOf(oneOf("SortName", "DateCreated")),
		"SortOrder":              oneOf("Ascending"),
		"Ids":                    idList,
	}},
	{method: http.MethodGet, segs: []string{"Videos", "{id}", "AdditionalParts"}, auth: true},
	{method: http.MethodGet, segs: []string{"Sessions"}, auth: true},
	{method: http.MethodGet, segs: []string{"ScheduledTasks"}, auth: true},
	{method: http.MethodPost, segs: []string{"Library", "Media", "Updated"}, auth: true, body: true},
}

// forbiddenQueryKeys are credential parameters a request never carries, whatever the route.
var forbiddenQueryKeys = []string{"apikey", "api_key", "token", "x-emby-token", "x-mediabrowser-token"}

// allowedRoute checks a request against the allowlist: the exact method, the escaped path (every
// segment literal or a 32-hex id; no "..", ".", empty segment, escaped slash or dot, backslash),
// the query keys and values of the route, and a body only where one is expected. It returns the
// route (for the credential) or ErrRefused. It runs on the final request, right before it is sent.
func allowedRoute(method, escPath string, q url.Values, hasBody bool) (*routeSpec, error) {
	refuse := func(why string) (*routeSpec, error) {
		return nil, fmt.Errorf("%w: %s %s (%s)", ErrRefused, method, sanitizePath(escPath), why)
	}
	for k := range q {
		for _, f := range forbiddenQueryKeys {
			if strings.EqualFold(strings.TrimSpace(k), f) {
				return refuse("a credential never goes in a URL")
			}
		}
	}
	lower := strings.ToLower(escPath)
	switch {
	case !strings.HasPrefix(escPath, "/") || escPath == "/":
		return refuse("not an allowed path")
	case strings.Contains(escPath, "//"), strings.ContainsAny(escPath, "\\?#;\x00"),
		strings.Contains(lower, "%2f"), strings.Contains(lower, "%2e"), strings.Contains(lower, "%5c"),
		strings.Contains(lower, "%00"):
		return refuse("the path is not canonical")
	}
	segs := strings.Split(escPath[1:], "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return refuse("the path is not canonical")
		}
	}
	for i := range routes {
		rt := &routes[i]
		if rt.method != method || len(rt.segs) != len(segs) {
			continue
		}
		match := true
		for j, want := range rt.segs {
			if want == "{id}" {
				match = match && reID.MatchString(segs[j])
			} else {
				match = match && segs[j] == want
			}
		}
		if !match {
			continue
		}
		for k, vs := range q {
			check, ok := rt.query[k]
			if !ok || len(vs) != 1 || !check(vs[0]) {
				return refuse("query parameter " + strconvQuote(k) + " is not allowed here")
			}
		}
		if hasBody != rt.body {
			return refuse("unexpected request body")
		}
		return rt, nil
	}
	return refuse("not on the allowlist")
}

// sanitizePath bounds a path for an error message.
func sanitizePath(p string) string {
	if len(p) > 200 {
		p = p[:200] + "…"
	}
	return strings.ToValidUTF8(p, "")
}

func strconvQuote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// authorization is the credential header: the key only ever travels here.
func (c *Client) authorization() string {
	ver := strings.TrimSpace(c.opts.Version)
	if ver == "" {
		ver = version.Version
	}
	device := c.opts.DeviceID
	if device == "" {
		device = "dupearr"
	}
	return fmt.Sprintf(`MediaBrowser Token="%s", Client="%s", Device="%s", DeviceId="%s", Version="%s"`,
		c.key, clientName, clientName, headerValue(device), headerValue(ver))
}

// headerValue strips what cannot appear inside a quoted header parameter.
func headerValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r > 0x7e {
			return -1
		}
		return r
	}, s)
}

// do is the one choke point every request passes: it checks the request against the allowlist
// (allowedRoute) and only then opens a connection. escPath must be escaped already.
func (c *Client) do(ctx context.Context, method, escPath string, q url.Values, body []byte) (*http.Response, error) {
	rt, err := allowedRoute(method, escPath, q, body != nil)
	if err != nil {
		return nil, err
	}
	if c.baseErr != nil {
		return nil, c.baseErr
	}
	if rt.auth && c.keyErr != nil {
		return nil, c.keyErr
	}
	u := *c.base
	rawPrefix := c.base.EscapedPath()
	u.RawPath = rawPrefix + escPath
	p, perr := url.PathUnescape(u.RawPath)
	if perr != nil {
		return nil, fmt.Errorf("%w: %s %s (the path is not canonical)", ErrRefused, method, sanitizePath(escPath))
	}
	u.Path = p
	u.RawQuery = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	// The URL as it will be sent must still be the checked path under the base's prefix.
	if u.EscapedPath() != rawPrefix+escPath {
		return nil, fmt.Errorf("%w: %s %s (the path changed when the URL was built)", ErrRefused, method, sanitizePath(escPath))
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: %s %s: build request: %w", method, escPath, redactURLError(err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.auth {
		req.Header.Set("Authorization", c.authorization())
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var re *redirectError
		switch {
		case errors.As(err, &re):
			return nil, fmt.Errorf("jellyfin: %s %s: %w", method, escPath, re)
		case quotesPeerData(err):
			return nil, fmt.Errorf("jellyfin: %s %s: %w", method, escPath, errNotHTTP)
		}
		return nil, fmt.Errorf("jellyfin: %s %s: %w", method, escPath, redactURLError(err))
	}
	return resp, nil
}

// getJSON GETs escPath and decodes the JSON answer into out, retried once on transport errors and
// 502/503/504 (never when ctx is done, never after a refusal).
func (c *Client) getJSON(ctx context.Context, escPath string, q url.Values, out any) error {
	var lastErr error
	for attempt := 0; attempt < getAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryDelay); err != nil {
				return fmt.Errorf("jellyfin: GET %s: %w", escPath, err)
			}
		}
		resp, err := c.do(ctx, http.MethodGet, escPath, q, nil)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrRefused) || errors.Is(err, ErrInvalidArgument) || errors.Is(err, ErrRedirect) {
				return err
			}
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			lastErr = checkStatus(resp, http.MethodGet, escPath)
			drainClose(resp.Body)
			continue
		}
		return decodeResponse(resp, http.MethodGet, escPath, out)
	}
	return lastErr
}

// post sends a JSON body once (never retried: a POST is not idempotent in general) and expects a
// 2xx without reading the answer.
func (c *Client) post(ctx context.Context, escPath string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("jellyfin: POST %s: encode body: %w", escPath, err)
	}
	resp, err := c.do(ctx, http.MethodPost, escPath, nil, body)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	return checkStatus(resp, http.MethodPost, escPath)
}

func decodeResponse(resp *http.Response, method, escPath string, out any) error {
	defer drainClose(resp.Body)
	if err := checkStatus(resp, method, escPath); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return fmt.Errorf("jellyfin: %s %s: read response: %w", method, escPath, err)
	}
	if len(b) > maxJSONBody {
		return fmt.Errorf("jellyfin: %s %s: %w: the answer exceeds %d MiB", method, escPath, ErrIncomplete, maxJSONBody>>20)
	}
	b = bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(b), []byte("\xef\xbb\xbf")))
	switch {
	case len(b) == 0:
		return fmt.Errorf("jellyfin: %s %s: %w: empty answer", method, escPath, ErrIncomplete)
	case b[0] == '<':
		return fmt.Errorf("jellyfin: %s %s: %w (the answer is an HTML/XML page)", method, escPath, ErrWrongApp)
	case tooManyJSONValues(b, maxJSONValues):
		return fmt.Errorf("jellyfin: %s %s: %w: the answer has more than %d JSON values", method, escPath, ErrIncomplete, maxJSONValues)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("jellyfin: %s %s: %w: %v", method, escPath, ErrIncomplete, err)
	}
	return nil
}

// tooManyJSONValues counts '{', '[' and ',' outside strings (an upper bound of the values decoding
// would allocate).
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

// checkStatus maps a status: 2xx → nil, 401 → ErrUnauthorized, 403 → ErrForbidden, 404 →
// ErrNotFound, anything else a StatusError (never with the body).
func checkStatus(resp *http.Response, method, escPath string) error {
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return fmt.Errorf("jellyfin: %s %s: %w", method, escPath, ErrUnauthorized)
	case code == http.StatusForbidden:
		return fmt.Errorf("jellyfin: %s %s: %w", method, escPath, ErrForbidden)
	case code == http.StatusNotFound:
		return fmt.Errorf("jellyfin: %s %s: %w", method, escPath, ErrNotFound)
	case code >= 300 && code < 400:
		return fmt.Errorf("jellyfin: %s %s: %w", method, escPath, ErrRedirect)
	default:
		return &StatusError{Method: method, Path: escPath, StatusCode: code}
	}
}

// errNotHTTP replaces transport errors that quote a non-HTTP peer's bytes.
var errNotHTTP = errors.New("the server's answer is not HTTP (not Jellyfin, or the wrong port or scheme?)")

// quotesPeerData reports a transport error whose text quotes what the peer sent (the URL is
// free-form, so such text would echo the banner of whatever service answers there).
func quotesPeerData(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "malformed HTTP") || strings.Contains(msg, "malformed MIME header")
}

// redactURLError strips credentials from a *url.Error's URL (userinfo cannot be in it, but a
// defensive redaction costs nothing).
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil {
			u.RawQuery = ""
			ue.URL = u.Redacted()
		} else {
			ue.URL = "(invalid url)"
		}
	}
	return err
}

// drainClose drains (bounded) and closes a body so the connection can be reused.
func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, drainLimit))
	_ = rc.Close()
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
