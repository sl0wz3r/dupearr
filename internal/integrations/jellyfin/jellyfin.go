// Package jellyfin is the Jellyfin client (12.1 or later): a READ-ONLY media-server source that
// implements the kind-neutral internal/mediaserver contract (Client, plus the ChangeNotifier and
// RemovalGate capabilities) for docs/DECISIONS.md D12 and docs/research/jellyfin-emby.md §5.3.
//
// Safety (research §4, P1): Dupearr never removes, merges, unlinks or edits anything through
// Jellyfin. Its item delete removes a movie's whole folder (every version, subtitles, other movies
// in a parent folder: S1, S2) and sidecars of other items by name prefix (S3); a bulk delete fails
// half-way (S17); an API key is an administrator and Jellyfin performs no deletion check for it
// (S15). The client therefore does not implement mediaserver.VersionDeleter, ItemRefresher or
// FolderScanner, and its transport builds requests only from a closed allowlist of reads plus the
// change notification (transport.go, S1–S3, S15–S17): every request passes one choke point that
// re-checks the final method, escaped path and query against the allowlist before a connection is
// opened, and refuses anything else with ErrRefused.
//
// Credential (S24): the API key travels only in the "Authorization: MediaBrowser Token=…" header,
// never in a URL or query (no ApiKey/api_key, no X-Emby-Token), and the public identity read of a
// connection test sends none. Redirects are never followed, link-local and cloud-metadata
// addresses are refused (netguard), TLS is 1.2 or later and verified unless the user turned that
// off, bodies are bounded, and error texts never carry the server's response body (SEC-031).
//
// Completeness: a library listing is complete or an error (ErrIncomplete, S10); the stack parts
// of every version are read with that version's own AdditionalParts (S4); a .strm source is never
// a version (S19); an unknown value is never read as a positive fact.
package jellyfin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
)

// MinVersion is the oldest Jellyfin Dupearr supports: versions were reworked for 12.x (episode
// versions, research §3.1), and 12.1 is the version the research confirmed live (S26).
const MinVersion = "12.1.0"

// minVersion / testedMinor: 12.1 is the tested minor; a newer one is accepted with a health notice.
var (
	minVersion  = [3]int{12, 1, 0}
	testedMinor = [2]int{12, 1}
)

// ProductName is what /System/Info reports for Jellyfin (Emby answers "Emby Server").
const ProductName = "Jellyfin Server"

const (
	defaultTimeout    = 30 * time.Second
	defaultPageSize   = 200
	defaultRetryDelay = 500 * time.Millisecond
	getAttempts       = 2 // one retry for a GET on transport errors and 502/503/504; POST never
	clientName        = "Dupearr"
)

// Options configures a Client.
type Options struct {
	// DeviceID identifies this install in the Authorization header (the install's stable id).
	DeviceID string
	// Version is sent in the Authorization header ("" = Dupearr's version).
	Version string
	// VerifyTLS enables certificate verification (models.MediaServer.VerifyTLS).
	VerifyTLS bool
	Timeout   time.Duration // default 30s
	// HTTPClient is an optional client (tests). It is copied; its CheckRedirect is replaced by the
	// package's (redirects are never followed). VerifyTLS does not apply to it.
	HTTPClient *http.Client
}

// Client talks to one Jellyfin server. It is safe for concurrent use. Callers build one per
// operation (a scan, a queue run, a health run): the library list is cached for its life.
type Client struct {
	base    *url.URL
	baseErr error
	key     string
	keyErr  error
	opts    Options
	hc      *http.Client

	pageSize   int
	retryDelay time.Duration
	// Listing bounds (0 = maxListedRows / defaultMaxListingBytes); lowered only by tests.
	maxRows         int
	maxListingBytes int64

	mu      sync.Mutex
	folders []virtualFolderDTO // GET /Library/VirtualFolders, cached for the client's life
	showIDs sync.Map           // series id → map[string]string (provider ids)
	// public: GET /System/Info/Public identified Jellyfin ≥ MinVersion (Identity reads it once per
	// client before it sends the key anywhere).
	public bool
	// substKnown/substSet: whether PathSubstitutions are set (read once per client by the listings
	// and re-reads, which refuse to work on rewritten paths).
	substKnown, substSet bool
}

// Errors. Every error of the client wraps one of these where it applies (errors.Is).
var (
	ErrUnauthorized = errors.New("jellyfin: unauthorized (check the API key)")
	// ErrForbidden: the credential is valid but is neither an API key nor an administrator (GET
	// /Library/VirtualFolders needs elevation, research S14): Dupearr cannot see every playback
	// session with it, so removals from this server are disabled.
	ErrForbidden = errors.New("jellyfin: the credential is not an API key or an administrator (create an API key in Jellyfin → Dashboard → API Keys)")
	// ErrNotFound (HTTP 404, or no row for an id) also matches mediaserver.ErrNotFound.
	ErrNotFound error = &notFoundError{}
	// ErrInvalidArgument is returned, before any request, for unusable input.
	ErrInvalidArgument = errors.New("jellyfin: invalid argument")
	// ErrRedirect: the server redirected; redirects are never followed (fix the URL instead).
	ErrRedirect = errors.New("jellyfin: the server redirected to another address; redirects are never followed (update the server URL)")
	// ErrRefused: the request is not on the client's allowlist (research §5.3.3); it was never sent.
	ErrRefused = errors.New("jellyfin: request refused by Dupearr's allowlist (Dupearr only reads from Jellyfin)")
	// ErrWrongApp: the server at the URL is not Jellyfin (another product, an HTML page).
	ErrWrongApp = errors.New("jellyfin: the server at this URL is not Jellyfin")
	// ErrTooOld: the server is older than MinVersion.
	ErrTooOld = errors.New("jellyfin: version " + MinVersion + " or later is required")
	// ErrIncomplete: a listing (or another answer) cannot be proven complete, or has an unexpected
	// shape; it is never used as a partial result.
	ErrIncomplete = errors.New("jellyfin: the answer could not be read completely")
	// ErrPathSubstitutions: Jellyfin rewrites the paths it reports (path substitutions in its
	// configuration, research S12), so its libraries are not listed or re-read until they are
	// removed.
	ErrPathSubstitutions = errors.New("jellyfin: path substitutions are set in Jellyfin's configuration, so the paths it reports cannot be compared with its library folders; Dupearr does not read its libraries until they are removed")
)

// notFoundError is ErrNotFound: it also matches mediaserver.ErrNotFound (like the Plex client's).
type notFoundError struct{ _ byte }

func (*notFoundError) Error() string { return "jellyfin: not found" }

// Is lets errors.Is(err, mediaserver.ErrNotFound) match a Jellyfin "not found".
func (*notFoundError) Is(target error) bool { return target == mediaserver.ErrNotFound }

// StatusError is an unexpected HTTP status. It never carries the response body (SEC-031: the URL
// is free-form, so the body may come from any internal service).
type StatusError struct {
	Method     string
	Path       string // escaped request path without the query (never carries the key)
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("jellyfin: %s %s: unexpected HTTP %d", e.Method, e.Path, e.StatusCode)
}

// Temporary reports a status worth retrying later (5xx, 429).
func (e *StatusError) Temporary() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// reKey is what an API key may look like: it is placed in a quoted header parameter, so quotes,
// backslashes, commas, whitespace and control characters are refused (Jellyfin's keys are 32 hex).
var reKey = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{1,256}$`)

// New returns a client for the server at baseURL (e.g. http://jellyfin:8096 or, behind a reverse
// proxy or with Jellyfin's "Base URL", https://host/jellyfin). An invalid URL or key is reported by
// every call.
func New(baseURL, apiKey string, opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	opts.DeviceID = strings.TrimSpace(opts.DeviceID)
	c := &Client{
		opts:       opts,
		hc:         newHTTPClient(opts),
		pageSize:   defaultPageSize,
		retryDelay: defaultRetryDelay,
	}
	c.base, c.baseErr = parseBaseURL(baseURL)
	c.key = strings.TrimSpace(apiKey)
	if !reKey.MatchString(c.key) {
		c.keyErr = fmt.Errorf("%w: the API key is empty or has characters an API key cannot have", ErrInvalidArgument)
	}
	return c
}

// parseVersion parses "12.1.0" (a fourth or pre-release part is ignored).
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "-+ "); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return v, false
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// versionLess reports a < b.
func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// checkVersion refuses a version older than MinVersion (or one that cannot be read).
func checkVersion(raw string) error {
	v, ok := parseVersion(raw)
	if !ok {
		return fmt.Errorf("%w (the server reports version %q)", ErrTooOld, raw)
	}
	if versionLess(v, minVersion) {
		return fmt.Errorf("%w (the server runs %s)", ErrTooOld, strings.TrimSpace(raw))
	}
	return nil
}

// UntestedVersion reports a version newer than the tested minor (12.1): accepted, with a health
// notice until it has been confirmed.
func UntestedVersion(raw string) bool {
	v, ok := parseVersion(raw)
	if !ok {
		return false
	}
	return v[0] > testedMinor[0] || (v[0] == testedMinor[0] && v[1] > testedMinor[1])
}

// reID is a Jellyfin item, source or library id in "N" format.
var reID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// normID normalizes an id as Jellyfin writes it in different places (N format, or D format with
// dashes; either case) to the N format; "" when it is not an id.
func normID(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	if !reID.MatchString(s) {
		return ""
	}
	return s
}
