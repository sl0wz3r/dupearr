// Package tautulli is the Tautulli (API v2) client Dupearr reads play history with
// (docs/DECISIONS.md D10, docs/research/watch-history.md). Dupearr only ever reads: the history
// ranks versions (the played / last_played criteria) and never decides a removal on its own.
//
// Transport rules (like the *arr client, docs/DECISIONS.md D3, docs/SECURITY.md):
//   - the API key travels only in the X-Api-Key header, never in a URL: Tautulli prefers the
//     ?apikey= query parameter over the header, and a URL ends up in logs and proxies. Tautulli
//     accepts the header since 2.18.0; older versions are refused (ErrTooOld);
//   - redirects are never followed (ErrRedirect), link-local and cloud-metadata addresses are
//     refused (netguard), every response is bounded, and error texts never carry the URL or the
//     server's response body (a connection URL is free-form, SEC-031);
//   - every answer is checked: an HTTP 200 whose envelope says "error", or data of an unexpected
//     shape, is an error — never an empty history. A history read is complete or an error: a short
//     page, a row for a rating key that was not asked for, a history that shrank while it was read
//     or more rows than the cap all fail the read (ErrIncomplete).
//
// The package does no logging and keeps no user names: History returns user ids only.
package tautulli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// DefaultTimeout is the per-request timeout used when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// MinVersion is the oldest Tautulli that reads the API key from the X-Api-Key header.
const MinVersion = "2.18.0"

var minVersion = [3]int{2, 18, 0}

const (
	// DefaultPageSize is the number of history rows requested per page.
	DefaultPageSize = 1000
	// DefaultMaxRows caps the rows one History call reads; beyond it the read fails (the history
	// is never truncated, docs/DECISIONS.md D10).
	DefaultMaxRows = 250_000
	// maxConcurrency caps concurrent requests to one Tautulli.
	maxConcurrency = 4
)

// Errors. Every error of the client wraps one of these (or a context / transport error).
var (
	ErrUnauthorized = errors.New("tautulli: unauthorized (check the API key)")
	// ErrTooOld: Tautulli before 2.18.0 only accepts the API key as a URL parameter, which
	// Dupearr never sends.
	ErrTooOld = errors.New("tautulli: version " + MinVersion + " or later is required (older versions only accept the API key in the URL)")
	// ErrKeyHeaderMissing: Tautulli 2.18.0 or later received no API key although Dupearr sent the
	// X-Api-Key header — something in between (a reverse proxy) dropped it.
	ErrKeyHeaderMissing = errors.New("tautulli: Tautulli did not receive the X-Api-Key header (check a reverse proxy in front of it; it must pass the header on)")
	// ErrWrongApp: the server at the URL does not answer like Tautulli's API v2.
	ErrWrongApp = errors.New("tautulli: the server at this URL does not answer like Tautulli (check the URL and HTTP root)")
	// ErrNotFound: /api/v2 is missing or the API is disabled (Tautulli → Settings → Web Interface).
	ErrNotFound = errors.New("tautulli: the API was not found (enable it in Tautulli → Settings → Web Interface, and check the URL and HTTP root)")
	// ErrRedirect: Tautulli redirects requests that lack its HTTP root; redirects are never followed.
	ErrRedirect = errors.New("tautulli: unexpected redirect (include Tautulli's HTTP root in the URL, e.g. http://host:8181/tautulli, and check http vs https)")
	// ErrInvalidArgument is returned, before any request, for unusable input.
	ErrInvalidArgument = errors.New("tautulli: invalid argument")
	// ErrCommandFailed: Tautulli answered, but reported that the command failed.
	ErrCommandFailed = errors.New("tautulli: the command failed")
	// ErrIncomplete: an answer was cut short, inconsistent or of an unexpected shape, so the
	// history cannot be trusted to be complete.
	ErrIncomplete = errors.New("tautulli: the play history could not be read completely")
)

// Options configures a Client.
type Options struct {
	// VerifyTLS enables TLS certificate verification (models.TautulliInstance.VerifyTLS).
	VerifyTLS bool
	// Timeout bounds each request (default 30s).
	Timeout time.Duration
	// HTTPClient overrides the HTTP client (tests). A copy is used whose redirect policy never
	// follows redirects; VerifyTLS and the address checks are then the caller's responsibility.
	HTTPClient *http.Client
}

// Client talks to one Tautulli. It is safe for concurrent use.
type Client struct {
	inst    models.TautulliInstance
	base    *url.URL // no query, fragment or trailing slash
	baseErr error    // non-nil when inst.URL is unusable; returned by every call
	hc      *http.Client
	timeout time.Duration
	limits  responseLimits
}

// New returns a client for inst. It never fails: an unusable URL is reported by every call.
func New(inst models.TautulliInstance, opts Options) *Client {
	c := &Client{inst: inst, timeout: opts.Timeout, limits: defaultLimits}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	c.base, c.baseErr = parseBaseURL(inst.URL)
	c.hc = newHTTPClient(opts)
	return c
}

// Info is what Tautulli reports about itself and the Plex server it monitors.
type Info struct {
	Version string // e.g. "v2.18.1"
	// PMSIdentifier is the machine identifier of the monitored Plex server ("" when not reported).
	PMSIdentifier string
	PMSName       string
}

// User is the part of a Tautulli user Dupearr keeps: no name, no e-mail.
type User struct {
	Active bool
	// KeepHistory is nil when Tautulli does not report it.
	KeepHistory *bool
}

// Library is the part of a Tautulli library Dupearr uses.
type Library struct {
	SectionID string
	// KeepHistory is nil when Tautulli does not report it, or reports another section (Tautulli
	// answers an unknown section with defaults).
	KeepHistory *bool
}

// HistoryRow is one recorded play (grouping off): who (an id only), what and when.
type HistoryRow struct {
	RowID     int64
	RatingKey string
	GUID      string
	UserID    int64
	Started   time.Time
	Stopped   time.Time
}

// PlayedAt is when the play was last active: its stop time, else its start time.
func (r HistoryRow) PlayedAt() time.Time {
	if r.Stopped.After(r.Started) {
		return r.Stopped
	}
	return r.Started
}

// HistoryFilter selects the rows of History. At least one of RatingKeys, GUID or SectionID must be
// set; every row of the answer must match RatingKeys when it is set.
type HistoryFilter struct {
	RatingKeys []string // Plex rating keys (sent as one comma-separated list)
	GUID       string   // Plex guid: Tautulli matches it as a prefix, so callers compare rows exactly
	SectionID  string   // Plex library section key
	PageSize   int      // rows per request (default DefaultPageSize)
	MaxRows    int      // the read fails beyond this many rows (default DefaultMaxRows)
}

var reKey = regexp.MustCompile(`^[0-9A-Za-z]{1,32}$`)

// Info reads Tautulli's version (ErrTooOld before 2.18.0) and the identity of the Plex server it
// monitors.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var ti tautulliInfoDTO
	if err := c.call(ctx, "get_tautulli_info", nil, &ti); err != nil {
		return nil, err
	}
	v := html.UnescapeString(strings.TrimSpace(ti.Version.String()))
	parsed, ok := parseVersion(v)
	if !ok {
		return nil, fmt.Errorf("%w: it reports no Tautulli version", ErrWrongApp)
	}
	if versionLess(parsed, minVersion) {
		return nil, fmt.Errorf("%w (found %s)", ErrTooOld, cleanText(v))
	}
	var si serverInfoDTO
	if err := c.call(ctx, "get_server_info", nil, &si); err != nil {
		return nil, err
	}
	return &Info{
		Version: cleanText(v),
		// Bounded and without control characters like every text taken from the answer: it is
		// compared, but also shown in health messages and logs (a machine identifier is 40 hex
		// characters, which cleaning leaves unchanged).
		PMSIdentifier: cleanText(strings.TrimSpace(si.PMSIdentifier.String())),
		PMSName:       cleanText(html.UnescapeString(si.PMSName.String())),
	}, nil
}

// Users returns every Tautulli user's active and keep-history flags (nothing else is kept).
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var list []userDTO
	if err := c.call(ctx, "get_users", nil, &list); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(list))
	for _, u := range list {
		active := true // an older Tautulli without the flag: every user counts
		if u.IsActive.set {
			active = u.IsActive.n != 0
		}
		out = append(out, User{Active: active, KeepHistory: u.KeepHistory.boolPtr()})
	}
	return out, nil
}

// Library returns the keep-history flag of a library section.
func (c *Client) Library(ctx context.Context, sectionID string) (*Library, error) {
	if !reKey.MatchString(sectionID) {
		return nil, fmt.Errorf("%w: section id %q", ErrInvalidArgument, sectionID)
	}
	var l libraryDTO
	// An "error" result fails the read like any other command (docs/DECISIONS.md D10): Tautulli
	// answers a section it does not know with its defaults (handled below), so an error is a
	// failure, not an unknown section.
	if err := c.call(ctx, "get_library", url.Values{"section_id": {sectionID}}, &l); err != nil {
		return nil, err
	}
	out := &Library{SectionID: sectionID}
	if strings.TrimSpace(l.SectionID.String()) == sectionID {
		out.KeepHistory = l.KeepHistory.boolPtr()
	}
	return out, nil
}

// FirstPlay returns the earliest recorded play of a library section: the history covers that
// library since then. nil (and no error) when the section has no recorded play.
func (c *Client) FirstPlay(ctx context.Context, sectionID string) (*HistoryRow, error) {
	if !reKey.MatchString(sectionID) {
		return nil, fmt.Errorf("%w: section id %q", ErrInvalidArgument, sectionID)
	}
	q := historyQuery(1)
	q.Set("section_id", sectionID)
	q.Set("start", "0")
	page, err := c.historyPage(ctx, q, 1)
	if err != nil {
		return nil, err
	}
	if page.total == 0 {
		return nil, nil
	}
	for _, r := range page.rows {
		if r.live {
			continue
		}
		row := r.row
		return &row, nil
	}
	return nil, fmt.Errorf("%w: get_history reports %d plays of section %s but returned none", ErrIncomplete, page.total, sectionID)
}

// History returns every recorded play matching f (grouping off, live sessions excluded, oldest
// first), deduplicated by row id — or an error: the read is never partial.
func (c *Client) History(ctx context.Context, f HistoryFilter) ([]HistoryRow, error) {
	pageSize, maxRows := f.PageSize, f.MaxRows
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	q := historyQuery(pageSize)
	var want map[string]bool
	if len(f.RatingKeys) > 0 {
		want = make(map[string]bool, len(f.RatingKeys))
		for _, k := range f.RatingKeys {
			if !reKey.MatchString(k) {
				return nil, fmt.Errorf("%w: rating key %q", ErrInvalidArgument, k)
			}
			want[k] = true
		}
		q.Set("rating_key", strings.Join(f.RatingKeys, ","))
	}
	if g := strings.TrimSpace(f.GUID); g != "" {
		if len(g) > 512 || strings.ContainsFunc(g, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("%w: guid", ErrInvalidArgument)
		}
		q.Set("guid", g)
	}
	if f.SectionID != "" {
		if !reKey.MatchString(f.SectionID) {
			return nil, fmt.Errorf("%w: section id %q", ErrInvalidArgument, f.SectionID)
		}
		q.Set("section_id", f.SectionID)
	}
	if want == nil && q.Get("guid") == "" && q.Get("section_id") == "" {
		return nil, fmt.Errorf("%w: an unfiltered history read", ErrInvalidArgument)
	}
	seen := map[int64]bool{}
	var out []HistoryRow
	start, total, live := 0, -1, 0
	for pages := 0; ; pages++ {
		if pages > maxRows/pageSize+16 {
			return nil, fmt.Errorf("%w: the history does not end", ErrIncomplete)
		}
		q.Set("start", strconv.Itoa(start))
		page, err := c.historyPage(ctx, q, pageSize)
		if err != nil {
			return nil, err
		}
		if total >= 0 && page.total < total {
			// Rows were deleted while the history was read: later pages shifted, a row may have
			// been skipped. (Rows added meanwhile only shift rows already read onto the next page.)
			return nil, fmt.Errorf("%w: the history changed while it was read", ErrIncomplete)
		}
		total = page.total
		if total > maxRows {
			return nil, fmt.Errorf("%w: more than %d recorded plays match", ErrIncomplete, maxRows)
		}
		for _, r := range page.rows {
			if r.live {
				live++
				continue // a session in progress, not a recorded play
			}
			if want != nil && !want[r.row.RatingKey] {
				return nil, fmt.Errorf("%w: get_history returned a play of rating key %q, which was not asked for",
					ErrIncomplete, cleanText(r.row.RatingKey))
			}
			if seen[r.row.RowID] {
				continue
			}
			seen[r.row.RowID] = true
			out = append(out, r.row)
		}
		if len(out) > maxRows {
			return nil, fmt.Errorf("%w: more than %d recorded plays match", ErrIncomplete, maxRows)
		}
		start += len(page.rows)
		if start >= total {
			if len(out)+live < total {
				// A page repeated rows of another (rows shifted between pages, e.g. a play recorded
				// meanwhile with an earlier start): as many rows were skipped as were repeated.
				return nil, fmt.Errorf("%w: get_history returned %d distinct of %d plays", ErrIncomplete, len(out)+live, total)
			}
			return out, nil
		}
		if len(page.rows) < pageSize {
			return nil, fmt.Errorf("%w: get_history returned %d of %d plays", ErrIncomplete, start, total)
		}
	}
}

// historyQuery returns the fixed parameters of every history read: grouping and live activity
// off (Tautulli's defaults merge consecutive plays and add sessions in progress), oldest first.
func historyQuery(length int) url.Values {
	return url.Values{
		"grouping":         {"0"},
		"include_activity": {"0"},
		"order_column":     {"date"},
		"order_dir":        {"asc"},
		"length":           {strconv.Itoa(length)},
	}
}

// parseVersion parses "v2.18.1" / "2.18.1-beta" into its numbers.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "v")
	if i := strings.IndexAny(s, "-+ "); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return out, false
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
