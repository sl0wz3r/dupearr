package arr

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/netguard"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// responseLimits bounds what one success response may make Dupearr read and allocate, so a
// compromised or misconfigured *arr (or anyone answering on its URL) cannot exhaust memory: JSON
// decoding amplifies ("{}," is three bytes but a struct of hundreds; "1," eight bytes in a []int64),
// so JSON arrays are decoded one element at a time with a per-element byte cap, an element count
// cap and a budget for the estimated memory of the decoded result.
type responseLimits struct {
	body       int64 // bytes of a response other than a library listing
	listBody   int64 // bytes of a library listing (request.long)
	element    int64 // bytes of one element of a JSON array response (approximate: read-ahead)
	elements   int   // elements of a JSON array response
	listMemory int64 // estimated memory of a decoded JSON array response
}

// defaultLimits: GET /movie of a 30k-movie library is ~150 MB of JSON of which ~50 MB is kept once
// decoded; single resources are kilobytes.
var defaultLimits = responseLimits{
	body:       8 << 20,
	listBody:   256 << 20,
	element:    2 << 20,
	elements:   500_000,
	listMemory: 256 << 20,
}

const (
	// maxErrorBodyBytes caps how much of an error body is read to extract a message.
	maxErrorBodyBytes = 64 << 10
	// maxMessageRunes caps an error message taken from a response body.
	maxMessageRunes = 300
	// maxGetAttempts is the number of attempts for idempotent GETs on transient failures
	// (connection refused/reset, HTTP 502/504). Mutating requests are never retried.
	maxGetAttempts = 2
	// deleteTimeoutFloor is the minimum timeout for a file DELETE. The *arr answers only after it
	// has moved the file into its recycle bin, which is a copy + delete when the bin is on another
	// filesystem (research §6) and takes minutes for a large remux. A client-side timeout would
	// leave the outcome unknown while the *arr keeps going.
	deleteTimeoutFloor = 15 * time.Minute
)

// HTTPError is a non-2xx answer from an *arr. It unwraps to ErrUnauthorized (401), ErrNotFound
// (404), ErrConflict (409), ErrUnavailable (503) or ErrRedirect (3xx) so callers can use errors.Is;
// errors.As gives access to the status code and the *arr's own message.
type HTTPError struct {
	Method     string
	Path       string // API path without host or query, e.g. "/api/v3/moviefile/118"
	StatusCode int
	// Message is the *arr's error text ({"message"} / [{"errorMessage"}] / plain text) or, for a
	// redirect, its target. Never contains the exception description (stack trace) or HTML.
	Message string
	// Err is the sentinel for the status code, or nil for other statuses.
	Err error
	// fromArr reports that the error body was JSON, i.e. the *arr itself answered (a reverse proxy
	// or a wrong URL answers with HTML or nothing). Used to tell "this item no longer exists" apart
	// from "this endpoint is not reachable" before skipping an item.
	fromArr bool
}

// isArrNotFound reports whether err is a 404 the *arr itself returned (its JSON error body), as
// opposed to a 404 from a reverse proxy or a wrong URL.
func isArrNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.StatusCode == http.StatusNotFound && he.fromArr
}

// Error implements error.
func (e *HTTPError) Error() string {
	var b strings.Builder
	if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString("arr: request failed")
	}
	fmt.Fprintf(&b, ": %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap returns the sentinel error for the status code (may be nil).
func (e *HTTPError) Unwrap() error { return e.Err }

// request describes one API call.
type request struct {
	method string
	path   string     // relative to /api/v3/, e.g. "moviefile/118"
	query  url.Values // never carries the API key
	body   any        // JSON-encoded when non-nil
	long   bool       // library-sized listing: timeout is at least listTimeoutFloor
}

func (r request) apiPath() string { return "/api/v3/" + r.path }

// timeout returns the effective timeout of r given the client's default.
func (r request) timeout(def time.Duration) time.Duration {
	floor := time.Duration(0)
	switch {
	case r.method == http.MethodDelete:
		floor = deleteTimeoutFloor
	case r.long:
		floor = listTimeoutFloor
	}
	if def < floor {
		return floor
	}
	return def
}

// mutating reports whether r can change state on the *arr (its outcome matters after a failure).
func (r request) mutating() bool { return r.method != http.MethodGet && r.method != http.MethodHead }

// outcomeUnknown is appended to transport failures of mutating requests: the request may have been
// applied even though no answer arrived, so callers must re-check state instead of retrying.
const outcomeUnknown = "; the outcome is unknown (the *arr may still apply it), re-check before retrying"

// parseBaseURL validates an instance URL and normalizes it: scheme http/https, a host, the URL
// base kept, trailing slashes, query and fragment dropped. The query is dropped on purpose: a stale
// ?apikey= would take precedence over the X-Api-Key header on the *arr side.
func parseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: instance URL is empty", ErrInvalidArgument)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse errors echo the input, which may carry credentials: report the cause only.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: invalid instance URL: %v", ErrInvalidArgument, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: instance URL must start with http:// or https://", ErrInvalidArgument)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: instance URL has no host", ErrInvalidArgument)
	}
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}

// dialTimeout bounds establishing a TCP connection (http.DefaultTransport's value).
const dialTimeout = 30 * time.Second

// newHTTPClient returns the HTTP client for opts. Redirects are never followed: an *arr redirects
// requests that lack its URL base (307), and Go would replay a redirected DELETE as a GET on
// 301/302/303, reporting success for a delete that never happened.
func newHTTPClient(opts Options) *http.Client {
	var hc http.Client
	if opts.HTTPClient != nil {
		hc = *opts.HTTPClient
	} else {
		var tr *http.Transport
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			tr = dt.Clone()
		} else {
			tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
		}
		// Link-local and cloud-metadata addresses are refused, also behind an HTTP(S) proxy
		// (netguard): no *arr lives there, but a cloud metadata service hands out credentials.
		tr.DialContext = netguard.NewDialer(dialTimeout).DialContext
		tr.Proxy = netguard.Proxy(tr.Proxy)
		// Self-signed certificates are common on home servers; verification is the user's choice
		// (ArrInstance.VerifyTLS).
		tr.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: !opts.VerifyTLS, //nolint:gosec // user-controlled per instance
		}
		// At most maxConcurrency requests reach the instance at once, whatever the callers do
		// (SQLite contention on the *arr side; research §9). Waiting for a connection counts
		// against the request's timeout and honours its context.
		tr.MaxConnsPerHost = maxConcurrency
		tr.MaxIdleConnsPerHost = maxConcurrency
		// Clients are cheap and often short-lived; do not keep idle connections around for long.
		tr.IdleConnTimeout = 30 * time.Second
		hc.Transport = tr
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

// endpoint builds the absolute URL of an API path, keeping the instance URL base.
func (c *Client) endpoint(r request) string {
	u := *c.base
	u.Path = c.base.Path + r.apiPath()
	if c.base.RawPath != "" {
		u.RawPath = c.base.RawPath + r.apiPath()
	}
	if len(r.query) > 0 {
		u.RawQuery = r.query.Encode()
	}
	return u.String()
}

// do performs r and decodes a JSON success body into out (nil = discard the body). Idempotent GETs
// get one retry on transient failures; nothing else is ever retried.
func (c *Client) do(ctx context.Context, r request, out any) error {
	if err := c.check(); err != nil {
		return err
	}
	var payload []byte
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("arr: %s %s: encoding request body: %w", r.method, r.apiPath(), err)
		}
		payload = b
	}
	attempts := 1
	if r.method == http.MethodGet {
		attempts = maxGetAttempts
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if serr := sleepCtx(ctx, c.retryDelay*time.Duration(attempt-1)); serr != nil {
				return err // keep the original failure; the caller's context ended while waiting
			}
		}
		var retry bool
		retry, err = c.once(ctx, r, payload, out)
		if err == nil || !retry {
			return err
		}
	}
	return err
}

// once performs a single attempt. retry reports whether the failure is transient (GET only).
func (c *Client) once(parent context.Context, r request, payload []byte, out any) (retry bool, err error) {
	timeout := r.timeout(c.timeout)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.endpoint(r), body)
	if err != nil {
		// The URL may carry credentials (http://user:pass@host): never echo it.
		return false, fmt.Errorf("arr: %s %s: building request: %w", r.method, r.apiPath(), unwrapURLError(err))
	}
	// X-Api-Key only: the *arr checks ?apikey= first, so the key must never be in the URL.
	// Surrounding whitespace (a pasted key) would make every call fail with 401.
	req.Header.Set("X-Api-Key", strings.TrimSpace(c.inst.APIKey))
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		unknown := ""
		if r.mutating() {
			unknown = outcomeUnknown
		}
		if perr := parent.Err(); perr != nil {
			return false, fmt.Errorf("arr: %s %s: %w%s", r.method, r.apiPath(), perr, unknown)
		}
		if ctx.Err() != nil {
			return false, fmt.Errorf("arr: %s %s: no response within %s: %w%s", r.method, r.apiPath(), timeout, context.DeadlineExceeded, unknown)
		}
		return r.method == http.MethodGet && retryableTransport(err),
			fmt.Errorf("arr: %s %s: %w%s", r.method, r.apiPath(), transportCause(err), unknown)
	}
	defer func() { _ = resp.Body.Close() }()

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		if isHTML(resp) {
			// A reverse-proxy login page or a wrong URL answering 200: nothing reached the *arr, so
			// a DELETE/command/PUT must not be reported as done.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			return false, fmt.Errorf("arr: %s %s: %w", r.method, r.apiPath(), errHTMLResponse)
		}
		if out == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			return false, nil
		}
		if err := c.decodeJSON(resp, out, r.long); err != nil {
			if perr := parent.Err(); perr != nil {
				return false, fmt.Errorf("arr: %s %s: %w", r.method, r.apiPath(), perr)
			}
			return false, fmt.Errorf("arr: %s %s: %w", r.method, r.apiPath(), err)
		}
		return false, nil
	case code >= 300 && code < 400:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		msg := "redirect without a Location header"
		if loc := sanitizeLocation(resp.Header.Get("Location")); loc != "" {
			msg = "redirected to " + loc
		}
		return false, &HTTPError{Method: r.method, Path: r.apiPath(), StatusCode: code, Message: msg, Err: ErrRedirect}
	default:
		raw := readLimited(resp.Body, maxErrorBodyBytes)
		herr := &HTTPError{Method: r.method, Path: r.apiPath(), StatusCode: code, Message: errorMessage(raw),
			Err: sentinelFor(code), fromArr: !isHTML(resp) && looksLikeJSON(raw)}
		transient := code == http.StatusBadGateway || code == http.StatusGatewayTimeout
		return r.method == http.MethodGet && transient, herr
	}
}

// errHTMLResponse is returned for a 2xx answer carrying an HTML page instead of the *arr's JSON.
var errHTMLResponse = errors.New("expected JSON but received an HTML page (check the URL, the *arr URL base and any reverse-proxy authentication)")

// isHTML reports whether resp declares an HTML body.
func isHTML(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// looksLikeJSON reports whether body starts like a JSON object or array (the body may have been
// truncated to maxErrorBodyBytes, so it is not fully validated).
func looksLikeJSON(body []byte) bool {
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	return len(body) > 0 && (body[0] == '{' || body[0] == '[')
}

// decodeJSON decodes a success body (HTML bodies are rejected before this is called) within
// c.limits: a JSON array into a slice is streamed element by element (decodeList), anything else
// is decoded whole from at most limits.body bytes (limits.listBody for a library listing).
func (c *Client) decodeJSON(resp *http.Response, out any, long bool) error {
	limit := c.limits.body
	if long {
		limit = c.limits.listBody
	}
	body := &capReader{r: resp.Body, left: limit, limit: limit}
	if rv := reflect.ValueOf(out); rv.Kind() == reflect.Pointer && !rv.IsNil() && rv.Elem().Kind() == reflect.Slice {
		return c.decodeList(body, rv.Elem())
	}
	dec := json.NewDecoder(body)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("empty response body")
		}
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// decodeList decodes a JSON array (or null) from r into slice one element at a time, so the body
// is never buffered whole and every limit is enforced while decoding rather than afterwards.
func (c *Client) decodeList(r io.Reader, slice reflect.Value) error {
	er := &elementReader{r: r, limit: c.limits.element}
	dec := json.NewDecoder(er)
	tok, err := dec.Token()
	switch {
	case errors.Is(err, io.EOF):
		return errors.New("empty response body")
	case err != nil:
		return fmt.Errorf("decoding response: %w", err)
	case tok == nil: // JSON null
		slice.SetZero()
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("decoding response: expected a JSON array")
	}
	elemType := slice.Type().Elem()
	out := reflect.MakeSlice(slice.Type(), 0, 16)
	var held int64
	for dec.More() {
		if out.Len() >= c.limits.elements {
			return fmt.Errorf("decoding response: the listing has more than %d entries", c.limits.elements)
		}
		er.reset()
		ptr := reflect.New(elemType)
		if err := dec.Decode(ptr.Interface()); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
		if held += approxSize(ptr.Interface()); held > c.limits.listMemory {
			return fmt.Errorf("decoding response: the listing holds more than %d MiB of data", c.limits.listMemory>>20)
		}
		out = reflect.Append(out, ptr.Elem())
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		return fmt.Errorf("decoding response: %w", err)
	}
	slice.Set(out)
	return nil
}

// capReader fails once more than limit bytes are read (a body of exactly limit bytes is fine).
type capReader struct {
	r           io.Reader
	left, limit int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("response body exceeds %s", formatLimit(c.limit))
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// elementReader fails once more than limit bytes are read since the last reset (one array
// element; approximate because the JSON decoder reads ahead).
type elementReader struct {
	r        io.Reader
	n, limit int64
}

func (e *elementReader) Read(p []byte) (int, error) {
	if e.n >= e.limit {
		return 0, fmt.Errorf("a listed element exceeds %s", formatLimit(e.limit))
	}
	if room := e.limit - e.n; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := e.r.Read(p)
	e.n += int64(n)
	return n, err
}

func (e *elementReader) reset() { e.n = 0 }

func formatLimit(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

var timeType = reflect.TypeFor[time.Time]()

// approxSize estimates the memory held by a decoded value: its inline size plus the strings,
// slices, maps and pointees it references (time.Time counted inline only).
func approxSize(v any) int64 {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return 0
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return 0
	}
	return typeSize(rv.Type()) + indirectSize(rv, 0)
}

// typeSize is t.Size() as int64 (Go type sizes are far below the int64 range).
func typeSize(t reflect.Type) int64 {
	return int64(t.Size()) // #nosec G115 -- a type's size always fits in int64
}

// indirectSize returns the bytes v references outside its own inline storage.
func indirectSize(v reflect.Value, depth int) int64 {
	if depth > 32 || v.Type() == timeType {
		return 0
	}
	var n int64
	switch v.Kind() {
	case reflect.String:
		n = int64(v.Len())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			e := v.Elem()
			n = typeSize(e.Type()) + indirectSize(e, depth+1)
		}
	case reflect.Slice:
		if v.IsNil() {
			break
		}
		n = int64(v.Cap()) * typeSize(v.Type().Elem())
		if !isScalarKind(v.Type().Elem().Kind()) {
			for i := 0; i < v.Len(); i++ {
				n += indirectSize(v.Index(i), depth+1)
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			n += indirectSize(v.Index(i), depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			n += indirectSize(v.Field(i), depth+1)
		}
	case reflect.Map:
		entry := typeSize(v.Type().Key()) + typeSize(v.Type().Elem()) + 16
		it := v.MapRange()
		for it.Next() {
			n += entry + indirectSize(it.Key(), depth+1) + indirectSize(it.Value(), depth+1)
		}
	}
	return n
}

func isScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	}
	return false
}

// sentinelFor maps a status code to its sentinel error.
func sentinelFor(code int) error {
	switch code {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	case http.StatusServiceUnavailable:
		return ErrUnavailable
	}
	return nil
}

// retryableTransport reports whether a transport error is worth one retry (the *arr restarting or
// a dropped keep-alive connection). Timeouts, TLS and DNS errors are not.
func retryableTransport(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// errNotHTTP replaces transport errors that quote a non-HTTP peer's bytes (see transportCause).
var errNotHTTP = errors.New("the server's answer is not HTTP (not a Radarr/Sonarr, or the wrong port or scheme?)")

// transportCause returns a transport error for messages: without its *url.Error wrapper
// (unwrapURLError), and replaced by errNotHTTP when net/http quotes what a non-HTTP peer sent
// ("malformed HTTP status code \"Ubuntu-…\"", "malformed MIME header …"). The URL is free-form, so
// such text would echo the banner of whatever service answers there (connection tests). A peer that
// sent bytes before the request was on its way ("readLoopPeekFailLocked": a service banner that won
// the race against the request; an HTTP server never speaks first) is not HTTP either.
func transportCause(err error) error {
	err = unwrapURLError(err)
	if msg := err.Error(); strings.Contains(msg, "malformed HTTP") || strings.Contains(msg, "malformed MIME header") ||
		strings.Contains(msg, "readLoopPeekFailLocked") {
		return errNotHTTP
	}
	return err
}

// unwrapURLError strips *url.Error, whose text repeats the full URL (the method and API path are
// already part of the wrapping message).
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func readLimited(r io.Reader, n int64) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return b
}

// errorMessage extracts a human message from an *arr error body:
//   - {"message": "...", "description": "<stack trace>"} → message (description is never used)
//   - {"errorMessage": "..."} (503 while starting up) → errorMessage
//   - [{"propertyName": "...", "errorMessage": "..."}] (400 validation) → errorMessages joined
//   - plain text → the text; HTML → "" (reverse-proxy error pages are noise)
func errorMessage(body []byte) string {
	body = bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	if len(body) == 0 {
		return ""
	}
	switch body[0] {
	case '{':
		var obj struct {
			Message      string `json:"message"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(body, &obj) == nil {
			if obj.Message != "" {
				return cleanMessage(obj.Message)
			}
			return cleanMessage(obj.ErrorMessage)
		}
	case '[':
		var list []struct {
			PropertyName string `json:"propertyName"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(body, &list) == nil {
			seen := make(map[string]bool, len(list))
			parts := make([]string, 0, len(list))
			for _, f := range list {
				m := strings.TrimSpace(f.ErrorMessage)
				if m == "" || seen[m] {
					continue
				}
				seen[m] = true
				parts = append(parts, m)
			}
			return cleanMessage(strings.Join(parts, "; "))
		}
	case '<':
		return ""
	}
	if !utf8.Valid(body) {
		return ""
	}
	return cleanMessage(string(body))
}

// cleanMessage collapses whitespace, drops control characters and truncates.
func cleanMessage(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= maxMessageRunes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// sanitizeLocation renders a redirect target for an error message: no credentials, no query
// (only the scheme/host/path matter to the user), bounded length.
func sanitizeLocation(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	return cleanMessage(u.String())
}

// sleepCtx waits for d or until ctx is done.
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
