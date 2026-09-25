package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Listing bounds (defence against a broken or hostile server that never stops paginating or
// inflates its rows): a section listing holds at most maxListedItems rows and about
// defaultMaxListingBytes of row data, and fails once it has seen far more rows than the server
// reported (overrunTotal). A very large home library (~200k episodes) needs about 150 MiB.
const (
	maxListedItems         = 500_000
	defaultMaxListingBytes = 512 << 20
	seenEntryBytes         = 48 // one entry of the rating-key de-duplication set (plus the key)
)

// overrunTotal reports whether seen rows exceed the largest total the server reported so badly that
// the listing cannot be a library that grew while it was paged (total < 0: never reported).
func overrunTotal(seen, total, pageSize int) bool {
	return total >= 0 && seen > 2*total+10*pageSize
}

// Identity fetches the server identity from GET / (authenticated, so it also validates the
// token, and carries friendlyName), falling back to GET /identity for a missing
// machineIdentifier/version.
func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	mc, _, err := c.getContainer(ctx, "/", nil, nil)
	if err != nil {
		return nil, err
	}
	id := &Identity{
		MachineIdentifier: mc.MachineIdentifier.String(),
		Version:           mc.Version.String(),
		FriendlyName:      mc.FriendlyName.String(),
	}
	if id.MachineIdentifier == "" || id.Version == "" {
		if ident, _, err := c.getContainer(ctx, "/identity", nil, nil); err == nil {
			if id.MachineIdentifier == "" {
				id.MachineIdentifier = ident.MachineIdentifier.String()
			}
			if id.Version == "" {
				id.Version = ident.Version.String()
			}
		} else if ctx.Err() != nil {
			return nil, err
		}
	}
	if id.MachineIdentifier == "" {
		return nil, errors.New("plex: server did not report a machineIdentifier (is this URL a Plex Media Server?)")
	}
	return id, nil
}

// Sections lists all library sections (every type; callers use "movie" and "show") with their
// root locations as the server sees them. Never returns a nil slice on success.
func (c *Client) Sections(ctx context.Context) ([]Section, error) {
	mc, _, err := c.getContainer(ctx, "/library/sections", nil, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Section, 0, len(mc.Directory))
	for _, d := range mc.Directory {
		key := d.Key.String()
		if key == "" {
			continue
		}
		s := Section{
			Key:              key,
			Type:             d.Type.String(),
			Title:            d.Title.String(),
			UUID:             d.UUID.String(),
			Locations:        make([]string, 0, len(d.Location)),
			Refreshing:       bool(d.Refreshing),
			ScannedAt:        max(int64(d.ScannedAt), 0),
			ContentChangedAt: max(int64(d.ContentChangedAt), 0),
		}
		for _, l := range d.Location {
			if p := string(l.Path); strings.TrimSpace(p) != "" {
				s.Locations = append(s.Locations, p)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// DuplicateItems lists the items of a section that have ≥2 non-optimized Media. Per
// docs/DECISIONS.md D2 it does not rely on Plex's duplicate=1 filter: it is AllItems filtered
// locally on MediaCount.
func (c *Client) DuplicateItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]ItemRef, error) {
	all, err := c.AllItems(ctx, sectionKey, mt)
	if err != nil {
		return nil, err
	}
	out := make([]ItemRef, 0)
	for _, it := range all {
		if it.MediaCount >= 2 {
			out = append(out, it)
		}
	}
	return out, nil
}

// AllItems lists every movie (mt=movie, type=1) or episode (mt=episode, type=4) of a section
// with guids (includeGuids=1), paginated with X-Plex-Container-Start/Size (page size 100). The
// offset advances by the number of rows actually returned; paging stops on an empty page or
// once totalSize rows were seen. An empty page before the reported total is an error (a
// truncated listing), never the end. Rows are de-duplicated by rating key (the library can change
// while paging). The listing fails beyond maxListedItems rows, ~defaultMaxListingBytes of row data
// or far more rows than the server reported (see overrunTotal). Never returns a nil slice on
// success.
func (c *Client) AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]ItemRef, error) {
	typ, err := listingType(mt)
	if err != nil {
		return nil, err
	}
	if !validKey(sectionKey) {
		return nil, fmt.Errorf("%w: section key %q", ErrInvalidArgument, sectionKey)
	}
	escPath := "/library/sections/" + url.PathEscape(sectionKey) + "/all"
	pageSize := c.pageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	maxItems, maxBytes := c.maxItems, c.maxListingBytes
	if maxItems <= 0 {
		maxItems = maxListedItems
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxListingBytes
	}

	items := make([]ItemRef, 0)
	seen := make(map[string]struct{})
	start := 0
	maxTotal := -1 // largest total the server reported
	var held int64 // approximate bytes of the rows kept so far
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plex: GET %s: %w", escPath, err)
		}
		startS, sizeS := strconv.Itoa(start), strconv.Itoa(pageSize)
		q := url.Values{
			"type":                   {typ},
			"includeGuids":           {"1"},
			"X-Plex-Container-Start": {startS},
			"X-Plex-Container-Size":  {sizeS},
		}
		hdr := http.Header{"X-Plex-Container-Start": {startS}, "X-Plex-Container-Size": {sizeS}}
		mc, h, err := c.getContainer(ctx, escPath, q, hdr)
		if err != nil {
			return nil, err
		}
		md := mc.Metadata
		n := len(md)
		if n == 0 {
			// An empty page ends the listing only when the (current) total confirms it: fewer rows
			// than the total means the listing was cut short, and a silently short listing
			// leaves the shared-file (multi-episode) index incomplete.
			if total := containerTotal(mc, h); total > start {
				return nil, fmt.Errorf("plex: GET %s: the listing ended early (an empty page at offset %d of %d items)",
					escPath, start, total)
			}
			break
		}
		fresh := 0
		for i := range md {
			rk := md[i].RatingKey.String()
			if rk == "" {
				continue
			}
			if _, dup := seen[rk]; dup {
				continue
			}
			seen[rk] = struct{}{}
			fresh++
			// The de-duplication set keeps the key of every row, including rows toItemRef rejects
			// (another type, …): counted too, or a server could grow it without bound with
			// rejected rows carrying huge rating keys.
			held += seenEntryBytes + int64(len(rk))
			if row, ok := toItemRef(&md[i], mt); ok {
				held += row.approxBytes()
				items = append(items, ItemRef(row))
			}
			if held > maxBytes {
				return nil, fmt.Errorf("plex: GET %s: the listing holds more than %d MiB of item data; aborting listing",
					escPath, maxBytes>>20)
			}
		}
		start += n
		total := containerTotal(mc, h)
		maxTotal = max(maxTotal, total)
		if overrunTotal(start, maxTotal, pageSize) {
			return nil, fmt.Errorf("plex: GET %s: the server returned %d rows but reported only %d items; aborting listing",
				escPath, start, maxTotal)
		}
		if total >= 0 && total < start {
			total = -1 // inconsistent (or the library shrank): page until an empty page instead
		}
		if total >= 0 && start >= total {
			break
		}
		if fresh == 0 {
			if total < 0 {
				break // the server ignores the offset and returned everything already
			}
			return nil, fmt.Errorf("plex: GET %s: pagination is not advancing (page at offset %d of %d held only items already listed)",
				escPath, start-n, total)
		}
		if start > maxItems {
			return nil, fmt.Errorf("plex: GET %s: more than %d items; aborting listing", escPath, maxItems)
		}
	}
	return items, nil
}

// listingRow is a mapped listing row while the listing is built: ItemRef is an alias of the
// kind-neutral mediaserver.ItemRef, so the listing budget's method lives on this local type.
type listingRow ItemRef

// approxBytes estimates the memory a row holds (struct headers plus string and slice data), for
// the listing budget.
func (r *listingRow) approxBytes() int64 {
	const (
		itemOverhead  = 256 // ItemRef fields and slice/map headers
		entryOverhead = 48  // one map entry (hash bucket share)
		mediaOverhead = 64  // one MediaRef
		partOverhead  = 48  // one PartRef
	)
	n := int64(itemOverhead + len(r.RatingKey) + len(r.MediaType) + len(r.Title) + len(r.ShowTitle) + len(r.GUID))
	for k, v := range r.ExternalIDs {
		n += int64(entryOverhead + len(k) + len(v))
	}
	for i := range r.Media {
		n += mediaOverhead
		for _, p := range r.Media[i].Parts {
			n += int64(partOverhead + len(p.File))
		}
	}
	return n
}

// seasonNumber returns an episode's season (parentIndex), or -1 when Plex did not report it
// (hidden seasons can omit it). -1 is never a valid season, so an unknown season cannot pass for
// Season 0 ("Specials") and merge an episode with an unrelated special.
func seasonNumber(m *metadataDTO) int {
	if m.ParentIndex == nil {
		return -1
	}
	return m.ParentIndex.Int()
}

// episodeNumber returns an episode's number (index), 0 when not reported (never a real episode).
func episodeNumber(m *metadataDTO) int {
	if m.Index == nil {
		return 0
	}
	return m.Index.Int()
}

func listingType(mt models.MediaType) (string, error) {
	switch mt {
	case models.MediaTypeMovie:
		return "1", nil
	case models.MediaTypeEpisode:
		return "4", nil
	default:
		return "", fmt.Errorf("%w: unsupported media type %q (want movie or episode)", ErrInvalidArgument, mt)
	}
}

// containerTotal returns MediaContainer.totalSize, else the X-Plex-Container-Total-Size header,
// else -1 (unknown).
func containerTotal(mc *containerDTO, h http.Header) int {
	if mc.TotalSize != nil && *mc.TotalSize >= 0 {
		return mc.TotalSize.Int()
	}
	if v := strings.TrimSpace(h.Get("X-Plex-Container-Total-Size")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// toItemRef maps a listing row. Rows without rating key or of another type are skipped.
func toItemRef(m *metadataDTO, mt models.MediaType) (listingRow, bool) {
	rk := m.RatingKey.String()
	if rk == "" {
		return listingRow{}, false
	}
	if t := strings.ToLower(m.Type.String()); t != "" && t != string(mt) {
		return listingRow{}, false
	}
	ref := listingRow{
		RatingKey:   rk,
		MediaType:   mt,
		Title:       m.Title.String(),
		Year:        m.Year.Int(),
		GUID:        m.GUID.String(),
		ExternalIDs: externalIDs(m.GUID.String(), m.Guids),
		Media:       make([]MediaRef, 0, len(m.Media)),
		AddedAt:     unixTime(m.AddedAt),
	}
	if mt == models.MediaTypeEpisode {
		ref.ShowTitle = m.GrandparentTitle.String()
		ref.Season = seasonNumber(m)
		ref.Episode = episodeNumber(m)
	}
	for i := range m.Media {
		md := &m.Media[i]
		mr := MediaRef{
			ID:         int64(md.ID),
			Optimized:  isOptimized(md),
			Width:      md.Width.Int(),
			Height:     md.Height.Int(),
			DurationMs: int64(md.Duration),
			Parts:      make([]PartRef, 0, len(md.Parts)),
		}
		for _, p := range md.Parts {
			mr.Parts = append(mr.Parts, PartRef{ID: int64(p.ID), File: string(p.File), Size: int64(p.Size)})
		}
		if !mr.Optimized {
			ref.MediaCount++
		}
		ref.Media = append(ref.Media, mr)
	}
	return ref, true
}

// isOptimized reports whether a Media is a Plex Optimized Version: proxyType 42, a non-empty
// optimizer "target", or any part under a "Plex Versions" folder (separators normalized,
// case-insensitive). Optimized versions are never duplicates, keepers or deletion candidates.
func isOptimized(md *mediaDTO) bool {
	if int64(md.ProxyType) == optimizedProxyType || md.Target.String() != "" {
		return true
	}
	for _, p := range md.Parts {
		if isPlexVersionsPath(string(p.File)) {
			return true
		}
	}
	return false
}

func isPlexVersionsPath(p string) bool {
	return strings.Contains(strings.ToLower(strings.ReplaceAll(p, `\`, "/")), "/plex versions/")
}

// unixTime converts an epoch (seconds; milliseconds tolerated) to UTC; zero when unset.
func unixTime(v flexInt) time.Time {
	switch n := int64(v); {
	case n <= 0:
		return time.Time{}
	case n > 100_000_000_000: // milliseconds
		return time.UnixMilli(n).UTC()
	default:
		return time.Unix(n, 0).UTC()
	}
}

// ---------------------------------------------------------------------------
// GUIDs → external ids
// ---------------------------------------------------------------------------

var (
	reIMDbID    = regexp.MustCompile(`^tt[0-9]+$`)
	reNumericID = regexp.MustCompile(`^[0-9]+$`)
)

func isPlexGUID(g string) bool { return strings.HasPrefix(strings.ToLower(g), "plex://") }

// externalIDs builds the "tmdb"/"imdb"/"tvdb"/"plex" map of an item from its Guid[] array
// (authoritative) and its primary guid: the full plex:// GUID is stored under "plex"; a legacy
// agent guid (com.plexapp.agents.imdb://tt…, themoviedb://…, thetvdb://…) fills a missing id
// unless it carries a /season/episode path (then the id is the show's, see showIDsFor).
func externalIDs(primary string, guids list[guidDTO]) map[string]string {
	ids := make(map[string]string)
	for _, g := range guids {
		if k, v, hasPath, ok := parseGUID(g.ID); ok && !hasPath {
			if _, exists := ids[k]; !exists {
				ids[k] = v
			}
		}
	}
	p := strings.TrimSpace(primary)
	if isPlexGUID(p) {
		ids["plex"] = p
	} else if k, v, hasPath, ok := parseGUID(p); ok && !hasPath {
		if _, exists := ids[k]; !exists {
			ids[k] = v
		}
	}
	return ids
}

// parseGUID parses "imdb://tt0088763", "tmdb://105", "tvdb://5664724" and legacy agent guids
// such as "com.plexapp.agents.imdb://tt0088763?lang=en", "com.plexapp.agents.themoviedb://105",
// "com.plexapp.agents.thetvdb://75159/3/6?lang=en" (hasPath: the id is the show's, followed by
// season/episode) or "com.plexapp.agents.xbmcnfo://tt0088763". Ids are validated (imdb tt+digits,
// tmdb/tvdb digits); anything else returns ok=false.
func parseGUID(g string) (key, id string, hasPath, ok bool) {
	g = strings.TrimSpace(g)
	i := strings.Index(g, "://")
	if i <= 0 {
		return "", "", false, false
	}
	scheme := strings.ToLower(g[:i])
	rest := g[i+3:]
	if q := strings.IndexAny(rest, "?#"); q >= 0 {
		rest = rest[:q]
	}
	agent := scheme
	if j := strings.LastIndex(scheme, "."); j >= 0 {
		agent = scheme[j+1:]
	}
	segs := strings.Split(strings.Trim(rest, "/"), "/")
	id = strings.TrimSpace(segs[0])
	hasPath = len(segs) > 1
	switch agent {
	case "imdb":
		key = "imdb"
	case "tmdb", "themoviedb":
		key = "tmdb"
	case "tvdb", "thetvdb", "thetvdbdvdorder":
		key = "tvdb"
	case "plex", "local", "none", "file":
		return "", "", false, false
	default:
		key = "imdb" // other agents (xbmcnfo, …) are accepted only for an IMDb-shaped id
	}
	if key == "imdb" {
		id = strings.ToLower(id)
		if !reIMDbID.MatchString(id) {
			return "", "", false, false
		}
	} else if !reNumericID.MatchString(id) {
		return "", "", false, false
	}
	return key, id, hasPath, true
}
