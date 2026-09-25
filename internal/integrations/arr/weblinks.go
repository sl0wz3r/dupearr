package arr

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Links into the Radarr/Sonarr web UI ("Open in Radarr"). Dupearr never requests these URLs; the
// browser opens them. Routes as in Radarr v5.28.0.10274 (identical in v6.4) and Sonarr v4.0.20.3014,
// frontend/src/App/AppRoutes.tsx: every route is prefixed with the *arr's URL base
// (Components/Router/Switch.tsx → getPathWithUrlBase), the same base its API is served under, so
// the instance URL (which includes it) is the web base too.
const (
	radarrItemRoute = "/movie/"         // /movie/:titleSlug (MovieDetailsPage looks the movie up by titleSlug only)
	sonarrItemRoute = "/series/"        // /series/:titleSlug (SeriesDetailsPageConnector, by titleSlug)
	queueRoute      = "/activity/queue" // Activity → Queue
)

// slugPattern is the page slug a link is built from. The *arr serves its web page for a deep link
// only when the path contains no '.' (Http/Frontend/Mappers/IndexHtmlMapper.cs CanHandle), and a
// slug is one path segment: anything else gets no item link.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_~-]{1,200}$`)

// pagePath matches a path ending in a page of the *arr's web UI or in its API (…/movie/603,
// …/series/the-expanse, …/activity/queue, …/settings/general, …/system/status, …/wanted/missing,
// …/add/new, …/calendar, …/api/v3). An address copied from the browser on such a page is not the
// web base: every link built on it would append its route to the page's (…/movie/603/movie/603).
var pagePath = regexp.MustCompile(`(?i)/(?:api(?:/v\d+)?|(?:movie|series)/[^/]+|(?:activity|settings|system|wanted|add|calendar)(?:/[^/]+)?)/?$`)

// IsWebPagePath reports whether the path of an External URL names a page of the *arr's web UI or
// its API rather than the start page (with the URL base) the links are built on.
func IsWebPagePath(path string) bool {
	return pagePath.MatchString(path)
}

// WebBase returns the address a browser opens inst at, with its URL base and without a trailing
// slash: ExternalURL when set, else URL. ok is false when that value is not an absolute http(s)
// URL with a host or carries credentials; an External URL that is set but unusable (only possible
// through a restored backup) gives no links rather than falling back to URL, so a link never leads
// somewhere the person did not choose. The query and fragment are dropped. The API key is never
// part of a link.
func WebBase(inst models.ArrInstance) (base string, ok bool) {
	raw := strings.TrimSpace(inst.ExternalURL)
	if raw == "" {
		raw = strings.TrimSpace(inst.URL)
	}
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Opaque != "",
		u.Host == "", u.Hostname() == "", u.User != nil:
		return "", false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), true
}

// ItemWebURL returns the page of an item in the *arr's web UI (base from WebBase): Radarr
// base/movie/<slug> (Radarr's titleSlug is the TMDB id), Sonarr base/series/<slug>. It returns ""
// for an unknown kind or a slug that is not 1–200 letters, digits, '-', '_' or '~'.
func ItemWebURL(base string, kind models.ArrKind, slug string) string {
	if base == "" || !slugPattern.MatchString(slug) {
		return ""
	}
	route := ""
	switch kind {
	case models.ArrRadarr:
		route = radarrItemRoute
	case models.ArrSonarr:
		route = sonarrItemRoute
	default:
		return ""
	}
	return base + route + url.PathEscape(slug)
}

// QueueWebURL returns the *arr's Activity → Queue page (base from WebBase), or "" without a base.
func QueueWebURL(base string) string {
	if base == "" {
		return ""
	}
	return base + queueRoute
}
