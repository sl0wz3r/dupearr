// Package plex is the Plex Media Server client (library listing, item detail → models.MediaItem,
// media deletion, refresh) plus the plex.tv PIN sign-in flow and server discovery.
//
// Every request sends X-Plex-Token, X-Plex-Client-Identifier, X-Plex-Product, X-Plex-Version,
// X-Plex-Platform, X-Plex-Device, X-Plex-Device-Name, Accept: application/json and User-Agent:
// Dupearr/<version>. Timeouts are always set (default 30s). The token is only ever sent as a
// header (never in a URL). GET redirects are followed only on the same server (scheme, host and
// port) or between https plex.tv hosts (without the token when the host changes); a redirect to
// any other address fails with ErrRedirect and is never requested.
//
// Wire format (docs/DECISIONS.md D2, docs/research/plex-api.md): PMS mixes numbers and numeric
// strings ("size": "1" vs 1) and "1"/true booleans, so every numeric/boolean field is decoded
// leniently; ratingKey is an opaque string. Action endpoints (DELETE, refresh) answer 200 with an
// empty text/html body which is never decoded, and error bodies may be HTML.
//
// Safety (this client can delete media): DeleteMedia validates its arguments before any request,
// only ever calls DELETE /library/metadata/{rk}/media/{mediaId} (no proxy parameter) and never
// follows redirects for non-GET requests. The client never calls whole-item delete, library
// delete, merge/split or bulk edit endpoints. A Client is safe for concurrent use.
package plex

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Options configures clients (PMS and plex.tv).
type Options struct {
	ClientIdentifier string // stable per install (store.SettingsRepo value "plex.clientIdentifier")
	Product          string // "Dupearr"
	Version          string
	// VerifyTLS enables certificate verification for PMS connections (disable for self-signed
	// certificates). plex.tv / clients.plex.tv requests always verify certificates.
	VerifyTLS bool
	Timeout   time.Duration // default 30s
	// HTTPClient is an optional client (tests). It is copied: its Transport, Jar and Timeout are
	// kept (Timeout defaults to the Timeout above when zero) but CheckRedirect is replaced by the
	// package's token-safe redirect policy. VerifyTLS does not apply to a caller-supplied client.
	HTTPClient *http.Client

	// Platform, Device and DeviceName fill X-Plex-Platform / X-Plex-Device /
	// X-Plex-Device-Name (shown in plex.tv "Authorized Devices"). Defaults: the OS name, Product,
	// Product.
	Platform   string
	Device     string
	DeviceName string

	// PlexTVURL overrides https://plex.tv (PIN endpoints) and ClientsPlexTVURL overrides
	// https://clients.plex.tv (resources); for tests. No trailing slash needed.
	PlexTVURL        string
	ClientsPlexTVURL string
}

// Client talks to one Plex Media Server.
type Client struct {
	base    *baseURL
	baseErr error
	token   string
	opts    Options
	hc      *http.Client

	pageSize   int           // listing page size (X-Plex-Container-Size)
	retryDelay time.Duration // delay before retrying an idempotent GET

	// Listing bounds (0 = maxListedItems / defaultMaxListingBytes); overridden only by tests.
	maxItems        int
	maxListingBytes int64

	// showIDs caches grandparent (show) external ids by show rating key for episode detail.
	showIDs sync.Map // map[string]map[string]string
}

// Identity is the server's identity (GET /, falling back to /identity).
type Identity struct {
	MachineIdentifier, Version, FriendlyName string
}

// Section is a library section (/library/sections).
type Section struct {
	Key, Type, Title, UUID string
	Locations              []string
}

// ItemRef is a lightweight listing row (from /library/sections/{id}/all). Listing rows include
// Media/Part (ids, files, sizes) but not stream details; see docs/DECISIONS.md D2.
type ItemRef struct {
	RatingKey string
	MediaType models.MediaType
	Title     string
	Year      int
	ShowTitle string
	Season    int    // episodes: -1 when Plex did not report the season (never confused with 0 = specials)
	Episode   int    // episodes: 0 when not reported
	GUID      string // plex://movie/… or legacy
	// ExternalIDs holds "tmdb"/"imdb"/"tvdb" from Guid[] (or a legacy agent guid) and "plex"
	// (the full plex:// GUID) when present. For episodes these are episode-level ids. Never nil.
	ExternalIDs map[string]string
	MediaCount  int // number of NON-optimized media
	Media       []MediaRef
	AddedAt     time.Time
}

// MediaRef is a Media element of a listing row.
type MediaRef struct {
	ID         int64
	Optimized  bool // proxyType == 42, a "target" (optimizer profile) or a part under "/Plex Versions/"
	Width      int
	Height     int
	DurationMs int64
	Parts      []PartRef
}

// PartRef is a Part element of a listing row.
type PartRef struct {
	ID   int64
	File string // path as Plex sees it
	Size int64
}

// Errors. Every error returned by this package wraps one of these where applicable, so callers
// can test with errors.Is.
var (
	ErrUnauthorized       = errors.New("plex: unauthorized (check token)")
	ErrNotFound           = errors.New("plex: not found")
	ErrDeletionNotAllowed = errors.New("plex: media deletion is disabled in Plex server settings")

	// ErrForbidden is returned for HTTP 403 (the token is valid but lacks access, e.g. a shared
	// user's token on an owner-only endpoint).
	ErrForbidden = errors.New("plex: forbidden (the token lacks permission; use the server owner's token)")
	// ErrInvalidArgument is returned (before any request is sent) for rating keys, media ids,
	// section keys or paths that fail validation.
	ErrInvalidArgument = errors.New("plex: invalid argument")
	// ErrRedirect is returned when the server redirects a request to another address (scheme,
	// host or port). Such redirects are never followed: the server URL must be corrected instead.
	ErrRedirect = errors.New("plex: the server redirected to another address; redirects are only followed on the same server (update the server URL)")
)

// StatusError is returned for unexpected HTTP statuses (other than 401/403/404, which map to
// the sentinel errors above).
type StatusError struct {
	Method     string
	Path       string // request path relative to the server/base URL (never includes the token)
	StatusCode int
	Body       string // short, sanitized excerpt of the response body ("" when omitted)
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("plex: %s %s: unexpected HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Temporary reports whether the status is worth retrying later (5xx, 429).
func (e *StatusError) Temporary() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

const (
	defaultTimeout    = 30 * time.Second
	defaultPageSize   = 100
	defaultRetryDelay = 500 * time.Millisecond
	getAttempts       = 2 // one retry for idempotent GETs on 502/503/504 or transport errors

	defaultProduct          = "Dupearr"
	defaultPlexTVURL        = "https://plex.tv"
	defaultClientsPlexTVURL = "https://clients.plex.tv"
	authAppURL              = "https://app.plex.tv/auth"

	// optimizedProxyType is Media.proxyType of a Plex Optimized Version.
	optimizedProxyType = 42
)

// reSafeKey validates rating keys, section keys and other ids placed in URL paths: PMS treats a
// comma list in {ids} as several items and an empty segment lands on an unknown handler, so only
// plain alphanumerics are ever accepted (docs/DECISIONS.md D2).
var reSafeKey = regexp.MustCompile(`^[0-9A-Za-z]+$`)

// validKey reports whether s is safe to use as a rating/section key path segment.
func validKey(s string) bool { return len(s) <= 64 && reSafeKey.MatchString(s) }

// New returns a client for the server at baseURL (e.g. http://192.168.1.10:32400). A base URL may
// carry a path prefix (reverse proxy: https://host/plex); it is kept and request paths are
// appended to it. A URL without scheme gets "http://". An invalid URL is reported by every call.
func New(baseURL, token string, opts Options) *Client {
	opts = opts.withDefaults()
	c := &Client{
		token:      sanitizeToken(token),
		opts:       opts,
		hc:         newHTTPClient(opts, opts.VerifyTLS),
		pageSize:   defaultPageSize,
		retryDelay: defaultRetryDelay,
	}
	c.base, c.baseErr = parseBaseURL(baseURL)
	return c
}
