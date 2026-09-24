package tautulli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/netguard"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// responseLimits bounds what one answer may make Dupearr read, so anyone answering on the URL
// cannot exhaust memory.
type responseLimits struct {
	body     int64 // bytes of one answer
	errBody  int64 // bytes of an error answer read to classify it
	elements int   // entries of a list answer (users) or history page beyond the page size
}

// defaultLimits: a history page of 1000 rows is ~2 MB of JSON; a user list a few kilobytes each.
var defaultLimits = responseLimits{
	body:     32 << 20,
	errBody:  64 << 10,
	elements: 100_000,
}

const apiPath = "/api/v2"

// HTTPError is a non-2xx answer. It unwraps to ErrUnauthorized, ErrKeyHeaderMissing, ErrTooOld,
// ErrNotFound or ErrRedirect when the status tells, and never carries the response body (SEC-031).
type HTTPError struct {
	Cmd        string
	StatusCode int
	Err        error // the sentinel, or nil
}

// Error implements error.
func (e *HTTPError) Error() string {
	prefix := "tautulli: request failed"
	if e.Err != nil {
		prefix = e.Err.Error()
	}
	return fmt.Sprintf("%s: GET %s (%s) returned HTTP %d", prefix, apiPath, e.Cmd, e.StatusCode)
}

// Unwrap returns the sentinel (may be nil).
func (e *HTTPError) Unwrap() error { return e.Err }

// Transient reports a status worth one more attempt later (Tautulli restarting, a proxy timeout).
func (e *HTTPError) Transient() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// parseBaseURL validates the connection URL: http/https with a host, the HTTP root kept, trailing
// slashes, query and fragment dropped (a stale ?apikey= must never be sent).
func parseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: the URL is empty", ErrInvalidArgument)
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse errors echo the input, which may carry credentials: report the cause only.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: invalid URL: %v", ErrInvalidArgument, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: the URL must start with http:// or https://", ErrInvalidArgument)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: the URL has no host", ErrInvalidArgument)
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}

// dialTimeout bounds establishing a TCP connection.
const dialTimeout = 30 * time.Second

// newHTTPClient returns the HTTP client for opts; redirects are never followed.
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
		// Link-local and cloud-metadata addresses are refused, also behind a proxy (netguard).
		tr.DialContext = netguard.NewDialer(dialTimeout).DialContext
		tr.Proxy = netguard.Proxy(tr.Proxy)
		// Self-signed certificates are common on home servers; verification is the user's choice.
		tr.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: !opts.VerifyTLS, //nolint:gosec // user-controlled per connection
		}
		tr.MaxConnsPerHost = maxConcurrency
		tr.MaxIdleConnsPerHost = maxConcurrency
		tr.IdleConnTimeout = 30 * time.Second
		hc.Transport = tr
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

// endpoint builds the URL of an API command. The API key is never part of it.
func (c *Client) endpoint(cmd string, params url.Values) string {
	q := url.Values{}
	for k, v := range params {
		if strings.EqualFold(k, "apikey") {
			continue // defensive: the key only ever travels in the header
		}
		q[k] = v
	}
	q.Set("cmd", cmd)
	u := *c.base
	u.Path = c.base.Path + apiPath
	if c.base.RawPath != "" {
		u.RawPath = c.base.RawPath + apiPath
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// call runs an API command and decodes the envelope's data into out.
func (c *Client) call(ctx context.Context, cmd string, params url.Values, out any) error {
	data, err := c.fetch(ctx, cmd, params)
	if err != nil {
		return err
	}
	if err := decodeData(data, out); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrIncomplete, cmd, err)
	}
	return nil
}

// fetch runs an API command and returns the envelope's data after checking the result.
func (c *Client) fetch(parent context.Context, cmd string, params url.Values) (json.RawMessage, error) {
	if c.baseErr != nil {
		return nil, c.baseErr
	}
	if strings.TrimSpace(c.inst.APIKey) == "" {
		return nil, fmt.Errorf("%w: no API key", ErrInvalidArgument)
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(cmd, params), nil)
	if err != nil {
		return nil, fmt.Errorf("tautulli: %s: building request: %w", cmd, unwrapURLError(err))
	}
	// X-Api-Key only (Tautulli 2.18.0+): a key in the URL ends up in logs and proxies, and Tautulli
	// would prefer a stale ?apikey= over the header.
	req.Header.Set("X-Api-Key", strings.TrimSpace(c.inst.APIKey))
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		if perr := parent.Err(); perr != nil {
			return nil, fmt.Errorf("tautulli: %s: %w", cmd, perr)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("tautulli: %s: no response within %s: %w", cmd, c.timeout, context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("tautulli: %s: %w", cmd, transportCause(err))
	}
	defer func() { _ = resp.Body.Close() }()

	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.limits.errBody))
		return nil, &HTTPError{Cmd: cmd, StatusCode: code, Err: ErrRedirect}
	case code < 200 || code >= 300:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, c.limits.errBody))
		return nil, &HTTPError{Cmd: cmd, StatusCode: code, Err: classifyStatus(code, body)}
	}
	if isHTML(resp) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.limits.errBody))
		return nil, fmt.Errorf("%w: %s answered with an HTML page", ErrWrongApp, cmd)
	}
	body, err := readCapped(resp.Body, c.limits.body)
	if err != nil {
		if perr := parent.Err(); perr != nil {
			return nil, fmt.Errorf("tautulli: %s: %w", cmd, perr)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrIncomplete, cmd, err)
	}
	var env envelopeDTO
	if err := json.Unmarshal(body, &env); err != nil || env.Response == nil {
		return nil, fmt.Errorf("%w: %s", ErrWrongApp, cmd)
	}
	switch strings.ToLower(strings.TrimSpace(env.Response.Result)) {
	case "success":
		return env.Response.Data, nil
	case "error":
		// The message is matched, never repeated (it comes from whatever answers at the URL).
		return nil, fmt.Errorf("%w: %s", classifyMessage(env.Response.Message), cmd)
	}
	return nil, fmt.Errorf("%w: %s: unexpected result", ErrWrongApp, cmd)
}

// classifyStatus maps an error status (and, to tell the causes apart, its body) to a sentinel.
func classifyStatus(code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		switch {
		case headerMissingMessage(body):
			return ErrKeyHeaderMissing
		case tooOldMessage(body):
			return ErrTooOld
		}
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// classifyMessage maps the message of an "error" result to a sentinel.
func classifyMessage(raw json.RawMessage) error {
	msg := strings.ToLower(string(raw))
	switch {
	case headerMissingMessage(raw):
		return ErrKeyHeaderMissing
	case tooOldMessage(raw):
		return ErrTooOld
	case strings.Contains(msg, "apikey") || strings.Contains(msg, "api key"):
		return ErrUnauthorized
	case strings.Contains(msg, "api not enabled"):
		return ErrNotFound
	}
	return ErrCommandFailed
}

// tooOldMessage reports Tautulli's answer to a request without the ?apikey= parameter: before
// 2.18.0 the X-Api-Key header is not read. (Checked after headerMissingMessage, whose text
// contains it.)
func tooOldMessage(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("parameter apikey is required"))
}

// headerMissingMessage reports the answer of Tautulli 2.18.0 or later to a request that carried
// neither the parameter nor the X-Api-Key header ("Parameter apikey is required or X-Api-Key header
// is required"): Dupearr sent the header, so something in between dropped it.
func headerMissingMessage(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("x-api-key header"))
}

// decodeData decodes the envelope's data into out: a list into a slice (an object is an error), an
// object into a struct (a list, e.g. Tautulli's [] for "nothing", is an error).
func decodeData(data json.RawMessage, out any) error {
	d := bytes.TrimSpace(data)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return errors.New("no data")
	}
	switch out.(type) {
	case *[]userDTO:
		if d[0] != '[' {
			return errors.New("expected a list")
		}
	default:
		if d[0] != '{' {
			return errors.New("expected an object")
		}
	}
	return json.Unmarshal(d, out)
}

// historyPage is one decoded get_history answer.
type historyPage struct {
	total int
	rows  []pageRow
}

type pageRow struct {
	row  HistoryRow
	live bool // row_id null: a session in progress
}

// historyPage reads one page of get_history; more than max rows, a missing total or rows of an
// unexpected shape are errors.
func (c *Client) historyPage(ctx context.Context, q url.Values, max int) (*historyPage, error) {
	data, err := c.fetch(ctx, "get_history", q)
	if err != nil {
		return nil, err
	}
	var h historyDTO
	if err := decodeData(data, &h); err != nil {
		return nil, fmt.Errorf("%w: get_history: %v", ErrIncomplete, err)
	}
	if h.RecordsFiltered == nil || !h.RecordsFiltered.set || h.RecordsFiltered.n < 0 || h.Data == nil {
		return nil, fmt.Errorf("%w: get_history: no play count or rows in the answer", ErrIncomplete)
	}
	var raw []historyRowDTO
	if err := json.Unmarshal(*h.Data, &raw); err != nil {
		return nil, fmt.Errorf("%w: get_history: rows: %v", ErrIncomplete, err)
	}
	if len(raw) > max || len(raw) > c.limits.elements {
		return nil, fmt.Errorf("%w: get_history returned %d rows for a page of %d", ErrIncomplete, len(raw), max)
	}
	if h.RecordsFiltered.n > int64(^uint32(0)) {
		return nil, fmt.Errorf("%w: get_history reports %d plays", ErrIncomplete, h.RecordsFiltered.n)
	}
	p := &historyPage{total: int(h.RecordsFiltered.n), rows: make([]pageRow, 0, len(raw))}
	for i, r := range raw {
		if !r.RowID.set {
			p.rows = append(p.rows, pageRow{live: true})
			continue
		}
		rk := strings.TrimSpace(r.RatingKey.String())
		if !reKey.MatchString(rk) {
			return nil, fmt.Errorf("%w: get_history row %d has no usable rating key", ErrIncomplete, i)
		}
		started := r.Started.unixTime()
		if started.IsZero() {
			started = r.Date.unixTime()
		}
		p.rows = append(p.rows, pageRow{row: HistoryRow{
			RowID:     r.RowID.n,
			RatingKey: rk,
			GUID:      html.UnescapeString(strings.TrimSpace(r.GUID.String())),
			UserID:    r.UserID.n,
			Started:   started,
			Stopped:   r.Stopped.unixTime(),
		}})
	}
	return p, nil
}

// readCapped reads at most limit bytes and fails on more.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("the answer exceeds %d MiB", limit>>20)
	}
	return b, nil
}

// isHTML reports whether resp declares an HTML body (a login page, a wrong URL).
func isHTML(resp *http.Response) bool {
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// errNotHTTP replaces transport errors that quote a non-HTTP peer's bytes.
var errNotHTTP = errors.New("the server's answer is not HTTP (not a Tautulli, or the wrong port or scheme?)")

// transportCause returns a transport error without its *url.Error wrapper (which repeats the
// URL), and without the bytes a non-HTTP peer sent.
func transportCause(err error) error {
	err = unwrapURLError(err)
	if msg := err.Error(); strings.Contains(msg, "malformed HTTP") || strings.Contains(msg, "malformed MIME header") ||
		strings.Contains(msg, "readLoopPeekFailLocked") {
		return errNotHTTP
	}
	return err
}

// unwrapURLError strips *url.Error, whose text repeats the full URL.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// Transient reports whether err may go away when the read is tried again later: a timeout, a
// dropped connection, a 5xx. Configuration errors (credentials, version, wrong application,
// redirects, bad input), an incomplete read and cancellation are not.
func Transient(err error) bool {
	switch {
	case err == nil, errors.Is(err, context.Canceled),
		errors.Is(err, ErrUnauthorized), errors.Is(err, ErrKeyHeaderMissing), errors.Is(err, ErrTooOld), errors.Is(err, ErrWrongApp),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrRedirect), errors.Is(err, ErrInvalidArgument),
		errors.Is(err, ErrIncomplete), errors.Is(err, ErrCommandFailed):
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Transient()
	}
	return true
}
