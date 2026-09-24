package plex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultPhotoWidth  = 300
	defaultPhotoHeight = 450
	maxPhotoDimension  = 2000
	maxPhotoBytes      = 20 << 20
	maxThumbPathLen    = 512
)

// DeleteMedia deletes ONE Media (version) of an item: DELETE /library/metadata/{rk}/media/{id}.
// Plex deletes the version's file(s) on disk; this is irreversible.
//
// Guards (docs/DECISIONS.md D2), all checked before any request is sent: ratingKey must match
// ^[0-9A-Za-z]+$ (no empty, comma-list, slash or encoded segment could reach another handler)
// and mediaID must be > 0; both path segments are escaped; no "proxy" parameter is sent; the
// request is never retried and never follows a redirect. Callers remain responsible for the
// safe-delete procedure (fresh re-verification, keeper present, not optimized, no shared file).
//
// Errors: 401 → ErrUnauthorized; 404 → ErrNotFound (already gone: re-sync); 400/403 → an error
// wrapping ErrDeletionNotAllowed that names the usual causes ("Allow media deletion" disabled,
// token not the server owner's, PMS lacking file-system permission); invalid arguments →
// ErrInvalidArgument.
func (c *Client) DeleteMedia(ctx context.Context, ratingKey string, mediaID int64) error {
	if !validKey(ratingKey) {
		return fmt.Errorf("%w: refusing to delete: rating key %q is not a plain alphanumeric id", ErrInvalidArgument, ratingKey)
	}
	if mediaID <= 0 {
		return fmt.Errorf("%w: refusing to delete: media id %d is not a positive id", ErrInvalidArgument, mediaID)
	}
	escPath := "/library/metadata/" + url.PathEscape(ratingKey) + "/media/" + url.PathEscape(strconv.FormatInt(mediaID, 10))
	err := c.action(ctx, http.MethodDelete, escPath, nil)
	if err == nil {
		return nil
	}
	var se *StatusError
	if (errors.As(err, &se) && se.StatusCode == http.StatusBadRequest) || errors.Is(err, ErrForbidden) {
		return fmt.Errorf("plex: Plex refused to delete media %d of item %s: check that \"Allow media deletion\" "+
			"(allowMediaDeletion, Settings > Library) is enabled, that the token belongs to the server owner, and "+
			"that the Plex server process has permission to delete the file (Docker PUID/PGID): %w (%w)",
			mediaID, ratingKey, ErrDeletionNotAllowed, err)
	}
	return err
}

// RefreshItem refreshes an item's metadata (PUT /library/metadata/{rk}/refresh). To pick up file
// changes use ScanPath.
func (c *Client) RefreshItem(ctx context.Context, ratingKey string) error {
	if !validKey(ratingKey) {
		return fmt.Errorf("%w: rating key %q", ErrInvalidArgument, ratingKey)
	}
	return c.action(ctx, http.MethodPut, "/library/metadata/"+url.PathEscape(ratingKey)+"/refresh", nil)
}

// ScanPath triggers a partial scan of dir (a path as the Plex server sees it, inside one of the
// section's locations): GET /library/sections/{id}/refresh?path=<url-encoded dir> — no "force"
// (that is a full metadata refresh). Servers that reject GET (404/405) are retried once with POST,
// the method of the official spec. Trailing separators are trimmed like Plex's own locations.
func (c *Client) ScanPath(ctx context.Context, sectionKey, dir string) error {
	if !validKey(sectionKey) {
		return fmt.Errorf("%w: section key %q", ErrInvalidArgument, sectionKey)
	}
	d, ok := cleanScanDir(dir)
	if !ok {
		return fmt.Errorf("%w: scan directory must be a non-empty path without control characters", ErrInvalidArgument)
	}
	escPath := "/library/sections/" + url.PathEscape(sectionKey) + "/refresh"
	q := url.Values{"path": {d}}
	err := c.action(ctx, http.MethodGet, escPath, q)
	var se *StatusError
	if errors.Is(err, ErrNotFound) || (errors.As(err, &se) && se.StatusCode == http.StatusMethodNotAllowed) {
		return c.action(ctx, http.MethodPost, escPath, q)
	}
	return err
}

func cleanScanDir(dir string) (string, bool) {
	if strings.TrimSpace(dir) == "" || strings.IndexFunc(dir, unicode.IsControl) >= 0 {
		return "", false
	}
	d := strings.TrimRight(dir, `/\`)
	switch {
	case d == "": // "/" itself
		return dir[:1], true
	case strings.TrimSpace(d) == "":
		return "", false
	case strings.HasSuffix(d, ":") && len(dir) > len(d): // Windows drive root "C:\"
		return d + dir[len(d):len(d)+1], true
	}
	return d, true
}

// ActiveSessions returns the rating keys currently being played (GET /status/sessions). The map
// is empty (not nil) when nothing is playing; any response that cannot be read as a session list
// is an error (never "nothing is playing"), so callers defer deletions.
func (c *Client) ActiveSessions(ctx context.Context) (map[string]bool, error) {
	mc, _, err := c.getContainer(ctx, "/status/sessions", nil, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(mc.Metadata))
	for _, m := range mc.Metadata {
		if rk := m.RatingKey.String(); rk != "" {
			out[rk] = true
		}
	}
	return out, nil
}

// MediaDeletionAllowed reports the server's "Allow media deletion" preference (attribute
// allowMediaDeletion on GET /; absent ⇒ false — PMS omits it when the setting is off).
func (c *Client) MediaDeletionAllowed(ctx context.Context) (bool, error) {
	mc, _, err := c.getContainer(ctx, "/", nil, nil)
	if err != nil {
		return false, err
	}
	a := mc.AllowMediaDeletion
	return a != nil && bool(*a), nil
}

// reThumbPath restricts Photo to library artwork paths (e.g. /library/metadata/1049/thumb/1612345678):
// the path is handed to Plex's transcoder as a URL, so absolute URLs, traversal, encoded or
// query characters are rejected (the transcoder must never fetch an arbitrary URL).
var reThumbPath = regexp.MustCompile(`^/library/[A-Za-z0-9_.\-/]+$`)

func validThumbPath(p string) bool {
	return len(p) <= maxThumbPathLen && reThumbPath.MatchString(p) &&
		!strings.Contains(p, "..") && !strings.Contains(p, "//")
}

// Photo proxies a thumb (transcoded to w×h via /photo/:/transcode) for the UI poster endpoint.
// thumbPath must be a Plex library path starting with "/library/" (ErrInvalidArgument
// otherwise). w/h default to 300×450 when ≤ 0 and are capped at 2000. The caller closes body;
// reading more than 20 MiB fails.
func (c *Client) Photo(ctx context.Context, thumbPath string, w, h int) (body io.ReadCloser, contentType string, err error) {
	if !validThumbPath(thumbPath) {
		return nil, "", fmt.Errorf("%w: thumb path must be a Plex /library/ path", ErrInvalidArgument)
	}
	w, h = clampDim(w, defaultPhotoWidth), clampDim(h, defaultPhotoHeight)
	const escPath = "/photo/:/transcode"
	q := url.Values{
		"width":   {strconv.Itoa(w)},
		"height":  {strconv.Itoa(h)},
		"minSize": {"1"},
		"upscale": {"1"},
		"url":     {thumbPath},
	}
	resp, err := c.do(ctx, http.MethodGet, escPath, q, http.Header{"Accept": {"image/*"}})
	if err != nil {
		return nil, "", err
	}
	if err := checkStatus(resp, http.MethodGet, escPath, true); err != nil {
		drainClose(resp.Body)
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	mt, _, perr := mime.ParseMediaType(ct)
	if perr != nil || !strings.HasPrefix(mt, "image/") {
		drainClose(resp.Body)
		return nil, "", fmt.Errorf("plex: GET %s: response is %q, not an image", escPath, ct)
	}
	return &cappedBody{rc: resp.Body, limit: maxPhotoBytes}, mt, nil
}

func clampDim(v, def int) int {
	switch {
	case v <= 0:
		return def
	case v > maxPhotoDimension:
		return maxPhotoDimension
	default:
		return v
	}
}

var errPhotoTooLarge = fmt.Errorf("plex: photo exceeds %d bytes", maxPhotoBytes)

// cappedBody fails reads once more than limit bytes were read.
type cappedBody struct {
	rc    io.ReadCloser
	limit int64
	read  int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errPhotoTooLarge
	}
	return n, err
}

func (b *cappedBody) Close() error { return b.rc.Close() }
