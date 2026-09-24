package notifications

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/netguard"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	httpClientTimeout = 30 * time.Second
	maxResponseBody   = 64 << 10
	maxRetryAfter     = 5 * time.Second
	maxErrorSnippet   = 200
	// maxRetryAfterHeader clamps parsed Retry-After values (anything above maxRetryAfter is not
	// retried anyway).
	maxRetryAfterHeader = 24 * time.Hour
)

// newHTTPClient returns the client used by every HTTP provider. Redirects are never followed:
// following one could replay a body (and credentials) to another URL, and a POST→GET downgrade
// would silently drop the notification. Link-local and cloud-metadata addresses are refused at
// connect time (checkDialAddress), after name resolution, so DNS cannot point around the check,
// and before a request is handed to an HTTP(S) proxy (netguard.Proxy), which would otherwise
// connect to them on Dupearr's behalf.
func newHTTPClient() *http.Client {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	}
	tr.DialContext = newDialer().DialContext
	tr.Proxy = netguard.Proxy(tr.Proxy)
	return &http.Client{
		Timeout:   httpClientTimeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// newDialer returns the dialer of every provider connection (HTTP and SMTP).
func newDialer() *net.Dialer { return netguard.NewDialer(httpClientTimeout) }

// checkDialAddress refuses connections to link-local and cloud-metadata addresses (see netguard).
var checkDialAddress = netguard.CheckDialAddress

// httpRequest describes one outbound provider request.
type httpRequest struct {
	method      string
	url         string
	body        []byte
	contentType string
	header      http.Header
	// fixedEndpoint marks requests to a provider's own API (Telegram, Pushover), whose address is
	// not configurable: their error details are always shown (see httpResult.withhold).
	fixedEndpoint bool
}

// httpResult is a completed HTTP exchange (any status).
type httpResult struct {
	status int
	header http.Header
	body   []byte
	// withhold hides body-derived error details from the returned error (statusError): set for
	// every request (test or delivery) to an address that is neither a fixed provider endpoint nor
	// a public provider host. The error of a test is returned to the API client and the error of
	// a delivery is logged (GET /api/v1/log, the log files), so neither may carry what an
	// arbitrary internal service answered: the URL is free-form, and a notification connection
	// must not become a way to read internal services' responses. The details are not logged
	// either, at any level (the log level is an ordinary setting and the log files are served by
	// the API).
	withhold bool
}

// providerHost is publicProviderHost (a variable so tests can treat their local servers as a
// provider's endpoint to check how its error messages are parsed).
var providerHost = publicProviderHost

// publicProviderHost reports the public endpoints of the chat providers (Discord, Slack, ntfy.sh),
// whose error messages are safe and useful to show in a test result.
func publicProviderHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	switch {
	case h == "discord.com", h == "discordapp.com", h == "hooks.slack.com", h == "ntfy.sh":
		return true
	case strings.HasSuffix(h, ".discord.com"), strings.HasSuffix(h, ".discordapp.com"):
		return true
	}
	return false
}

// withheldError is a non-2xx result whose body-derived detail was withheld (see
// httpResult.withhold). Only the status is kept: the detail is dropped, not logged.
type withheldError struct {
	status string
}

func (e *withheldError) Error() string {
	return e.status + " (the server's response is not shown for this address)"
}

// jsonRequest builds a JSON POST request.
func jsonRequest(rawURL string, payload any) (httpRequest, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return httpRequest{}, fmt.Errorf("encode payload: %w", err)
	}
	return httpRequest{method: http.MethodPost, url: rawURL, body: body, contentType: "application/json", header: http.Header{}}, nil
}

// urlHost returns the host[:port] of rawURL for error messages (never path or query, which may
// carry tokens).
func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "the configured server"
	}
	return u.Host
}

// do performs req, retrying once on 429 when the server asks for a short Retry-After. The returned
// result may carry any status; transport failures are returned as errors that name only the host.
func (s *Service) do(ctx context.Context, req httpRequest) (*httpResult, error) {
	host := urlHost(req.url)
	for attempt := 0; ; attempt++ {
		hr, err := http.NewRequestWithContext(ctx, req.method, req.url, bytes.NewReader(req.body))
		if err != nil {
			// url.Parse errors quote the whole URL; do not wrap them.
			return nil, fmt.Errorf("invalid request URL for %s", host)
		}
		for k, vs := range req.header {
			hr.Header[k] = slices.Clone(vs)
		}
		hr.Header.Set("User-Agent", version.UserAgent())
		if req.contentType != "" {
			hr.Header.Set("Content-Type", req.contentType)
		}
		resp, err := s.client.Do(hr)
		if err != nil {
			return nil, transportError(ctx, req.method, host, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, transportError(ctx, req.method, host, fmt.Errorf("read response: %w", readErr))
		}
		res := &httpResult{status: resp.StatusCode, header: resp.Header, body: body,
			withhold: !req.fixedEndpoint && !providerHost(host)}
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			if wait, ok := retryAfter(resp.Header); ok && wait <= maxRetryAfter {
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					return nil, transportError(ctx, req.method, host, ctx.Err())
				case <-t.C:
				}
				continue
			}
		}
		return res, nil
	}
}

// transportError describes a failed exchange without the request URL.
func transportError(ctx context.Context, method, host string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%s %s: timed out: %w", method, host, ctxErr)
		}
		return fmt.Errorf("%s %s: %w", method, host, ctxErr)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if quotesPeerData(err) {
		// net/http quotes what a non-HTTP peer sent ("malformed HTTP status code \"Ubuntu-…\"");
		// the URL is free-form, so that would echo the banner of whatever service answers there.
		return fmt.Errorf("%s %s: the server's answer is not HTTP (not a web server, or the wrong port or scheme?)", method, host)
	}
	return fmt.Errorf("%s %s: %w", method, host, err)
}

// quotesPeerData reports a net/http transport error whose text quotes the peer's bytes: a response
// line or header that is not HTTP ("malformed HTTP response/version/status code …", "malformed
// MIME header …"). It also reports "readLoopPeekFailLocked": the peer sent bytes before the request
// was on its way (a banner of an SMTP, FTP, SSH … service; an HTTP server never speaks first), which
// net/http reports that way when the banner wins the race against the request.
func quotesPeerData(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "malformed HTTP") || strings.Contains(msg, "malformed MIME header") ||
		strings.Contains(msg, "readLoopPeekFailLocked")
}

// retryAfter parses a Retry-After header (delta seconds or HTTP date).
func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 { // NaN fails >= 0
		// Clamp before converting: a huge value (or +Inf) would overflow time.Duration —
		// possibly to a negative wait, i.e. an immediate retry.
		secs = min(secs, maxRetryAfterHeader.Seconds())
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// ok2xx reports a 2xx status.
func (r *httpResult) ok2xx() bool { return r.status >= 200 && r.status < 300 }

// statusError builds an error for a non-2xx response. detail (a provider-specific message parsed
// from the body) wins over the raw body snippet.
func (r *httpResult) statusError(detail string) error {
	status := fmt.Sprintf("HTTP %d", r.status)
	if t := http.StatusText(r.status); t != "" {
		status += " " + t
	}
	if r.status >= 300 && r.status < 400 {
		loc := "another location"
		if l := r.header.Get("Location"); l != "" {
			loc = urlHost(l)
		}
		return fmt.Errorf("%s: server redirected to %s; check the URL (redirects are not followed)", status, loc)
	}
	if detail = cleanLine(detail); detail == "" {
		detail = r.snippet()
	}
	if detail == "" {
		return errors.New(status)
	}
	if r.withhold {
		return &withheldError{status: status}
	}
	return fmt.Errorf("%s: %s", status, truncate(detail, maxErrorSnippet))
}

// snippet returns a short single-line excerpt of a non-HTML response body.
func (r *httpResult) snippet() string {
	b := bytes.TrimSpace(r.body)
	if len(b) == 0 {
		return ""
	}
	lower := strings.ToLower(string(b[:min(len(b), 64)]))
	if strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") {
		return ""
	}
	return truncate(cleanLine(string(b)), maxErrorSnippet)
}

// jsonField extracts a string property from a JSON object body ("" when absent).
func (r *httpResult) jsonField(names ...string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(r.body, &obj); err != nil {
		return ""
	}
	for _, n := range names {
		if v, k := decodeScalar(obj[n]); k == valueString && v != "" {
			return v
		}
	}
	return ""
}

// redact replaces every sensitive value in s with MaskedValue (longest first, including
// URL-escaped forms). Values shorter than 3 characters are ignored to keep messages readable.
func redact(s string, sensitive []string) string {
	var vals []string
	for _, v := range sensitive {
		if len(v) < 3 {
			continue
		}
		vals = append(vals, v, url.QueryEscape(v), url.PathEscape(v))
	}
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	for _, v := range vals {
		if len(v) >= 3 {
			s = strings.ReplaceAll(s, v, MaskedValue)
		}
	}
	return s
}

// redactedError carries a redacted message while keeping the cause for errors.Is/As.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// basicAuth returns an HTTP Basic Authorization header value.
func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// withTrailingSlash returns a copy of u whose path ends with "/" (query preserved).
func withTrailingSlash(u *url.URL) *url.URL {
	out := *u
	if !strings.HasSuffix(out.Path, "/") {
		out.Path += "/"
		if out.RawPath != "" {
			out.RawPath += "/"
		}
	}
	return &out
}
