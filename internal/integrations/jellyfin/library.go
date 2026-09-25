package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Listing bounds (defence against a broken or hostile server): a library listing holds at most
// maxListedRows rows and about defaultMaxListingBytes of row data.
const (
	maxListedRows          = 500_000
	defaultMaxListingBytes = 512 << 20
)

// listFields are the fields every listing and re-read asks for (research §3.2): the sources, the
// paths, the provider ids and the counts the completeness checks need.
const listFields = "MediaSources,Path,ProviderIds,MediaSourceCount,PartCount,DateCreated,ParentId"

// Report-only reasons (MediaVersion.ReportOnly; docs/DECISIONS.md D12).
const (
	reasonParts = "its stack parts could not be read from Jellyfin"
)

// ---------------------------------------------------------------------------
// Identity and libraries
// ---------------------------------------------------------------------------

// Identity reads GET /System/Info with the key: the server id (MachineIdentifier, 32 hex,
// lower-case), version and name. Another product (ErrWrongApp) or a version before MinVersion
// (ErrTooOld) is an error, so a sync, scan or queue run against such a server fails closed (S26).
// The first call of a client reads GET /System/Info/Public without the key first: the key (an
// administrator's, S15) is only ever sent to a server that says it is Jellyfin ≥ 12.1, also after a
// forced save of an address the connection test never confirmed.
func (c *Client) Identity(ctx context.Context) (*mediaserver.Identity, error) {
	if c.keyErr != nil {
		return nil, c.keyErr
	}
	c.mu.Lock()
	public := c.public
	c.mu.Unlock()
	if !public {
		if _, err := c.PublicInfo(ctx); err != nil {
			return nil, err
		}
	}
	var si systemInfoDTO
	if err := c.getJSON(ctx, "/System/Info", nil, &si); err != nil {
		return nil, err
	}
	return identityOf(si)
}

// PublicInfo reads GET /System/Info/Public WITHOUT the credential: the connection test and the
// forced save's probe use it to confirm Jellyfin ≥ 12.1 before the key is sent anywhere, and a
// success lets this client's Identity send the key.
func (c *Client) PublicInfo(ctx context.Context) (*mediaserver.Identity, error) {
	var si systemInfoDTO
	if err := c.getJSON(ctx, "/System/Info/Public", nil, &si); err != nil {
		return nil, err
	}
	id, err := identityOf(si)
	if err == nil {
		c.mu.Lock()
		c.public = true
		c.mu.Unlock()
	}
	return id, err
}

func identityOf(si systemInfoDTO) (*mediaserver.Identity, error) {
	if p := strings.TrimSpace(si.ProductName); p != ProductName {
		if p == "" {
			return nil, fmt.Errorf("%w (it reports no product name)", ErrWrongApp)
		}
		return nil, fmt.Errorf("%w (it reports %q)", ErrWrongApp, bounded(p, 60))
	}
	id := normID(si.ID)
	if id == "" {
		return nil, fmt.Errorf("%w (it reports no server id)", ErrWrongApp)
	}
	if err := checkVersion(si.Version); err != nil {
		return nil, err
	}
	return &mediaserver.Identity{MachineIdentifier: id, Version: strings.TrimSpace(si.Version), FriendlyName: bounded(si.ServerName, 200)}, nil
}

// bounded cuts s to n runes (text of the server shown in messages).
func bounded(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Sections lists the libraries (GET /Library/VirtualFolders): Key = the library's ItemId, Type
// "movie" (CollectionType movies) or "show" (tvshows), anything else "" (not synced). A library of
// another kind that may hold video files (mixed content, home videos, music videos, or a kind this
// client does not know) is OtherVideo: Dupearr never lists it, so the several-servers checks count
// it as unknown (docs/DECISIONS.md D11, D12). A 401 or 403 is ErrForbidden: the answer needs an API
// key or an administrator (S14), which is what proves that Dupearr sees every playback session.
// Every call reads the libraries again (the several-servers re-check before a removal compares
// them with the scan's record), and the answer is kept for the listings and re-reads that need the
// library folders. Jellyfin reports no scan times (ScannedAt/ContentChangedAt 0): the D11 change
// signal of its libraries is the listing fingerprint (docs/DECISIONS.md D11).
func (c *Client) Sections(ctx context.Context) ([]mediaserver.Section, error) {
	folders, err := c.virtualFolders(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]mediaserver.Section, 0, len(folders))
	for _, f := range folders {
		key := normID(f.ItemID)
		if key == "" {
			continue
		}
		s := mediaserver.Section{
			Key:       key,
			Type:      sectionType(f.CollectionType),
			Title:     bounded(f.Name, 200),
			Locations: make([]string, 0, len(f.Locations)),
			// UNVERIFIED (research §8): "Active" while a library scan runs. Only ever makes the D11
			// re-check stricter.
			Refreshing: strings.EqualFold(strings.TrimSpace(f.RefreshStatus), "Active"),
		}
		s.OtherVideo = s.Type == "" && mayHoldVideo(f.CollectionType)
		for _, l := range f.Locations {
			if strings.TrimSpace(l) != "" {
				s.Locations = append(s.Locations, l)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// sectionType maps a CollectionType to the section types Dupearr syncs.
func sectionType(collectionType string) string {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "movies":
		return "movie"
	case "tvshows":
		return "show"
	}
	return ""
}

// mayHoldVideo reports a library kind Dupearr does not sync that may list video files. Only the
// kinds known to hold no video files are left out: an unknown kind counts (fail closed).
func mayHoldVideo(collectionType string) bool {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "music", "books", "boxsets", "playlists":
		return false
	}
	return true
}

// virtualFolders reads (fresh) or returns the cached library list.
func (c *Client) virtualFolders(ctx context.Context, fresh bool) ([]virtualFolderDTO, error) {
	if !fresh {
		c.mu.Lock()
		cached := c.folders
		c.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
	}
	var out []virtualFolderDTO
	if err := c.getJSON(ctx, "/Library/VirtualFolders", nil, &out); err != nil {
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrForbidden) {
			return nil, fmt.Errorf("jellyfin: GET /Library/VirtualFolders: %w", ErrForbidden)
		}
		return nil, err
	}
	if out == nil {
		out = []virtualFolderDTO{}
	}
	c.mu.Lock()
	c.folders = out
	c.mu.Unlock()
	return out, nil
}

// libraryOf returns the library whose ItemId is key.
func (c *Client) libraryOf(ctx context.Context, key string) (*virtualFolderDTO, error) {
	folders, err := c.virtualFolders(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range folders {
		if normID(folders[i].ItemID) == key {
			return &folders[i], nil
		}
	}
	return nil, fmt.Errorf("jellyfin: library %s: %w", key, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

// includeType maps the media type to Jellyfin's item type.
func includeType(mt models.MediaType) (string, error) {
	switch mt {
	case models.MediaTypeMovie:
		return "Movie", nil
	case models.MediaTypeEpisode:
		return "Episode", nil
	}
	return "", fmt.Errorf("%w: unsupported media type %q (want movie or episode)", ErrInvalidArgument, mt)
}

// AllItems lists every movie or episode of one library (GET /Items?ParentId=<ItemId>&Recursive=
// true, pages of 200 with the total record count). The listing is complete or an error
// (ErrIncomplete, research S10): the distinct row ids must equal TotalRecordCount, the total must
// not change from the first to the last page, no page may be short or empty before the total, no
// row may repeat, every row's MediaSourceCount (absent = 1) must equal its sources, no source may be
// a row's own (Default) source under two rows, every file version must report its size, and every
// row's own file must lie inside the library's folders. While Jellyfin rewrites the paths it
// reports (path substitutions, S12) nothing is listed (ErrPathSubstitutions): the rewritten paths
// lie outside the library folders, so every copy would look like another library's and the groups
// would be resolved. Extras and rows that are not files (LocationType other than FileSystem) are
// skipped. A row's versions are its file sources inside this library's folders (Default or
// Grouping, Protocol File, not remote, with a path; a source of another library is left to that
// library's row, S11); a .strm source is a shortcut, never a version (S19). Rows of one library
// merged into one title (MergeVersions with a primary in another library lists each of them with
// the others as Grouping sources, live on 12.1) are one ref, keyed by their smallest row id, so each
// file is listed once. Parts: the source's own file plus GET /Videos/{sourceID}/AdditionalParts for
// every alternate, and for the ref's own source when its PartCount says it is stacked (the row's
// PartCount describes only its own item, S4); a failed parts read fails the listing.
func (c *Client) AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]mediaserver.ItemRef, error) {
	typ, err := includeType(mt)
	if err != nil {
		return nil, err
	}
	key := normID(sectionKey)
	if key == "" {
		return nil, fmt.Errorf("%w: library id %q", ErrInvalidArgument, bounded(sectionKey, 80))
	}
	if err := c.pathsRewritten(ctx); err != nil {
		return nil, err
	}
	lib, err := c.libraryOf(ctx, key)
	if err != nil {
		return nil, err
	}
	all, err := c.listRows(ctx, key, typ)
	if err != nil {
		return nil, err
	}
	var rows []*itemDTO
	for i := range all {
		row := &all[i]
		if skipRow(row, typ) {
			continue
		}
		if err := sourceCountProblem(row); err != nil {
			return nil, err
		}
		if err := ownFileProblem(row, lib.Locations); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	comps, err := mergedRows(key, rows, lib.Locations)
	if err != nil {
		return nil, err
	}
	out := make([]mediaserver.ItemRef, 0, len(comps))
	for _, comp := range comps {
		ref, err := c.itemRef(ctx, comp, mt, lib.Locations)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// mergedRows returns the listed rows as refs to build, in listing order: one entry per row, except
// that rows linked through Grouping sources of this library (one merged title listed as several
// rows) are one entry, led by the smallest row id (what a re-read by that id lists too). A row's
// own sources (Default) belong to exactly one row: one under two rows is an incomplete listing.
func mergedRows(libKey string, rows []*itemDTO, locations []string) ([][]*itemDTO, error) {
	owner := map[string]int{} // a row's own (Default) source id → its row
	for i, row := range rows {
		for _, s := range row.MediaSources {
			sid := normID(s.ID)
			if sid == "" || strings.EqualFold(strings.TrimSpace(s.Type), "Grouping") {
				continue
			}
			if o, dup := owner[sid]; dup && o != i {
				return nil, fmt.Errorf("jellyfin: library %s: %w: source %s is listed under two items", libKey, ErrIncomplete, sid)
			}
			owner[sid] = i
		}
	}
	parent := make([]int, len(rows))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	grouped := map[string]int{} // a Grouping source of this library no listed row owns → its first row
	for i, row := range rows {
		for j := range row.MediaSources {
			s := &row.MediaSources[j]
			sid := normID(s.ID)
			// A merged copy of another library is that library's (its row lists it there).
			if sid == "" || !strings.EqualFold(strings.TrimSpace(s.Type), "Grouping") || classify(s, locations) == sourceIgnored {
				continue
			}
			if o, ok := owner[sid]; ok {
				union(i, o)
				continue
			}
			if o, ok := grouped[sid]; ok {
				union(i, o)
				continue
			}
			grouped[sid] = i
		}
	}
	byRoot := map[int][]*itemDTO{}
	var roots []int
	for i, row := range rows {
		r := find(i)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], row)
	}
	out := make([][]*itemDTO, 0, len(roots))
	for _, r := range roots {
		comp := byRoot[r]
		lead := 0
		for i := range comp {
			if comp[i].ID < comp[lead].ID {
				lead = i
			}
		}
		comp[0], comp[lead] = comp[lead], comp[0]
		out = append(out, comp)
	}
	return out, nil
}

// ownFileProblem refuses a row whose own file lies outside the library's folders: Jellyfin then
// reports paths it rewrites (path substitutions, or another rewrite Dupearr does not know), and
// every version would look like another library's (S12).
func ownFileProblem(row *itemDTO, locations []string) error {
	for i := range row.MediaSources {
		s := &row.MediaSources[i]
		if normID(s.ID) != row.ID || !strings.EqualFold(strings.TrimSpace(s.Protocol), "File") || s.IsRemote {
			continue
		}
		if p := strings.TrimSpace(s.Path); p != "" && !inLocations(p, locations) {
			return fmt.Errorf("jellyfin: item %s: %w: its file %s lies outside the library's folders (the server may rewrite the paths it reports)",
				row.ID, ErrIncomplete, bounded(p, 200))
		}
	}
	return nil
}

// pathsRewritten fails while Jellyfin rewrites the paths it reports (PathSubstitutions of GET
// /System/Configuration, research S12; the library folders are not rewritten, so no listed file
// would lie inside its library). The answer is kept for the client's life once it could be read.
func (c *Client) pathsRewritten(ctx context.Context) error {
	c.mu.Lock()
	known, set := c.substKnown, c.substSet
	c.mu.Unlock()
	if !known {
		cfg, err := c.configuration(ctx)
		if err != nil {
			return err
		}
		set = len(cfg.PathSubstitutions) > 0
		c.mu.Lock()
		c.substKnown, c.substSet = true, set
		c.mu.Unlock()
	}
	if set {
		return ErrPathSubstitutions
	}
	return nil
}

// listRows pages GET /Items over one library and checks the completeness of the listing.
func (c *Client) listRows(ctx context.Context, parentID, typ string) ([]itemDTO, error) {
	pageSize := c.pageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxRows, maxBytes := c.maxRows, c.maxListingBytes
	if maxRows <= 0 {
		maxRows = maxListedRows
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxListingBytes
	}
	fail := func(format string, args ...any) ([]itemDTO, error) {
		return nil, fmt.Errorf("jellyfin: GET /Items (library %s): %w: %s", parentID, ErrIncomplete, fmt.Sprintf(format, args...))
	}
	var rows []itemDTO
	seen := map[string]bool{}
	total := -1
	var held int64
	for start := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{
			"ParentId":               {parentID},
			"Recursive":              {"true"},
			"IncludeItemTypes":       {typ},
			"Fields":                 {listFields},
			"SortBy":                 {"SortName,DateCreated"},
			"SortOrder":              {"Ascending"},
			"StartIndex":             {strconv.Itoa(start)},
			"Limit":                  {strconv.Itoa(pageSize)},
			"EnableTotalRecordCount": {"true"},
		}
		var page itemsDTO
		if err := c.getJSON(ctx, "/Items", q, &page); err != nil {
			return nil, err
		}
		if page.TotalRecordCount == nil || *page.TotalRecordCount < 0 {
			return fail("the answer has no total record count")
		}
		switch {
		case total < 0:
			total = *page.TotalRecordCount
		case *page.TotalRecordCount != total:
			return fail("the library changed while it was listed (%d items, then %d)", total, *page.TotalRecordCount)
		}
		n := len(page.Items)
		if start >= total {
			if n != 0 {
				return fail("more items than the total of %d", total)
			}
			break
		}
		if n == 0 {
			return fail("an empty page at offset %d of %d items", start, total)
		}
		if n < pageSize && start+n < total {
			return fail("a short page at offset %d (%d items of %d)", start, n, total)
		}
		for i := range page.Items {
			it := &page.Items[i]
			id := normID(it.ID)
			if id == "" {
				return fail("an item without a valid id")
			}
			if seen[id] {
				return fail("item %s is listed twice", id)
			}
			seen[id] = true
			it.ID = id
			held += rowBytes(it)
			rows = append(rows, *it)
		}
		if held > maxBytes {
			return fail("more than %d MiB of item data", maxBytes>>20)
		}
		start += n
		if start > maxRows {
			return fail("more than %d items", maxRows)
		}
		if start >= total {
			break
		}
	}
	if len(seen) != total {
		return fail("%d distinct items but a total of %d", len(seen), total)
	}
	if rows == nil {
		rows = []itemDTO{}
	}
	return rows, nil
}

// rowBytes estimates the memory a row holds (for the listing budget).
func rowBytes(it *itemDTO) int64 {
	n := int64(512 + len(it.Name) + len(it.Path) + len(it.SeriesName))
	for _, s := range it.MediaSources {
		n += int64(256 + len(s.Path) + len(s.Name) + 128*len(s.MediaStreams))
	}
	return n
}

// skipRow reports rows that are never items: another type, extras and rows that are not files.
func skipRow(row *itemDTO, typ string) bool {
	return !strings.EqualFold(strings.TrimSpace(row.Type), typ) ||
		strings.TrimSpace(row.ExtraType) != "" ||
		!strings.EqualFold(strings.TrimSpace(row.LocationType), "FileSystem")
}

// sourceCountProblem checks a row's MediaSourceCount (absent when 1) against its sources: a row
// that hides a version from the listing would make a group look smaller than it is (S10).
func sourceCountProblem(row *itemDTO) error {
	want := 1
	if row.MediaSourceCount != nil {
		want = *row.MediaSourceCount
	}
	if want != len(row.MediaSources) {
		return fmt.Errorf("jellyfin: item %s: %w: it reports %d versions but lists %d", row.ID, ErrIncomplete, want, len(row.MediaSources))
	}
	return nil
}

// sourceKind classifies a media source of a row.
type sourceKind int

const (
	sourceIgnored  sourceKind = iota // placeholder, remote, no path, another library
	sourceFile                       // a file version
	sourceShortcut                   // a local .strm shortcut (never a version, S19)
)

// classify tells what a source is for a row of the library with the given folders.
func classify(s *mediaSourceDTO, locations []string) sourceKind {
	typ := strings.TrimSpace(s.Type)
	p := strings.TrimSpace(s.Path)
	if (!strings.EqualFold(typ, "Default") && !strings.EqualFold(typ, "Grouping")) ||
		!strings.EqualFold(strings.TrimSpace(s.Protocol), "File") || s.IsRemote || p == "" || normID(s.ID) == "" {
		return sourceIgnored
	}
	if !inLocations(p, locations) {
		return sourceIgnored
	}
	if isShortcut(s) {
		return sourceShortcut
	}
	return sourceFile
}

// isShortcut reports a .strm source (by its path's extension or its container).
func isShortcut(s *mediaSourceDTO) bool {
	return strings.EqualFold(path.Ext(strings.ReplaceAll(strings.TrimSpace(s.Path), `\`, "/")), ".strm") ||
		strings.EqualFold(strings.TrimSpace(s.Container), "strm")
}

// inLocations reports whether p lies inside one of the library folders.
func inLocations(p string, locations []string) bool {
	np := pathmap.Normalize(p)
	for _, l := range locations {
		if withinDir(np, pathmap.Normalize(l)) {
			return true
		}
	}
	return false
}

// withinDir reports whether normalized p lies strictly inside normalized dir.
func withinDir(p, dir string) bool {
	if dir == "" || p == dir {
		return false
	}
	return strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// itemRef maps listed rows (one row, or the rows of one merged title led by the smallest id) to
// the neutral listing row: the lead row's metadata and every file source of the rows inside the
// library, each once. A file version without a size is an incomplete listing: its size is what the
// several-servers index and the keeper check compare.
func (c *Client) itemRef(ctx context.Context, rows []*itemDTO, mt models.MediaType, locations []string) (mediaserver.ItemRef, error) {
	row := rows[0]
	ref := mediaserver.ItemRef{
		RatingKey:   row.ID,
		MediaType:   mt,
		Title:       row.Name,
		Year:        row.ProductionYear,
		ExternalIDs: providerIDs(row.ProviderIDs),
		Media:       []mediaserver.MediaRef{},
		AddedAt:     parseTime(row.DateCreated),
	}
	if mt == models.MediaTypeEpisode {
		ref.ShowTitle = row.SeriesName
		ref.Season = -1
		if row.ParentIndexNumber != nil {
			ref.Season = *row.ParentIndexNumber
		}
		if row.IndexNumber != nil {
			ref.Episode = *row.IndexNumber
		}
	}
	stackedRow := row.PartCount != nil && *row.PartCount > 1
	seen := map[string]bool{}
	for _, r := range rows {
		for i := range r.MediaSources {
			s := &r.MediaSources[i]
			sid := normID(s.ID)
			kind := classify(s, locations)
			if kind == sourceIgnored || seen[sid] {
				continue
			}
			seen[sid] = true
			if kind == sourceShortcut {
				ref.Shortcuts = append(ref.Shortcuts, mediaserver.PartRef{File: s.Path, Size: sizeOf(s.Size), ItemID: sid})
				continue
			}
			if s.Size == nil || *s.Size <= 0 {
				return mediaserver.ItemRef{}, fmt.Errorf("jellyfin: item %s: %w: version %s reports no size", row.ID, ErrIncomplete, sid)
			}
			m := mediaserver.MediaRef{VersionID: sid, DurationMs: ticksToMs(ptrInt64(s.RunTimeTicks))}
			if vs := videoStream(s.MediaStreams); vs != nil {
				m.Width, m.Height = vs.Width, vs.Height
			}
			m.Parts = []mediaserver.PartRef{{File: s.Path, Size: *s.Size, ItemID: sid}}
			// The row's PartCount describes its own item only: an alternate may be a stack the row
			// does not show (Kappa, research §3.1), so every alternate's parts are read.
			if sid != row.ID || stackedRow {
				parts, err := c.additionalParts(ctx, sid)
				if err != nil {
					return mediaserver.ItemRef{}, fmt.Errorf("jellyfin: item %s: the stack parts of version %s: %w", row.ID, sid, err)
				}
				m.Parts = append(m.Parts, parts...)
			}
			ref.Media = append(ref.Media, m)
			ref.MediaCount++
		}
	}
	return ref, nil
}

// additionalParts reads GET /Videos/{id}/AdditionalParts: the other parts of a stacked version,
// each with its path, size and item id. An answer that is not a complete list of parts with paths
// and sizes is an error (never "one part").
func (c *Client) additionalParts(ctx context.Context, sourceID string) ([]mediaserver.PartRef, error) {
	id := normID(sourceID)
	if id == "" {
		return nil, fmt.Errorf("%w: version id %q", ErrInvalidArgument, bounded(sourceID, 80))
	}
	var res itemsDTO
	if err := c.getJSON(ctx, "/Videos/"+id+"/AdditionalParts", nil, &res); err != nil {
		return nil, err
	}
	if res.TotalRecordCount != nil && *res.TotalRecordCount != len(res.Items) {
		return nil, fmt.Errorf("%w: the answer lists %d parts of %d", ErrIncomplete, len(res.Items), *res.TotalRecordCount)
	}
	out := make([]mediaserver.PartRef, 0, len(res.Items))
	for i := range res.Items {
		it := &res.Items[i]
		pid := normID(it.ID)
		p := strings.TrimSpace(it.Path)
		var size *int64
		if len(it.MediaSources) > 0 {
			size = it.MediaSources[0].Size
			if p == "" {
				p = strings.TrimSpace(it.MediaSources[0].Path)
			}
		}
		switch {
		case pid == "":
			return nil, fmt.Errorf("%w: a part without an id", ErrIncomplete)
		case p == "":
			return nil, fmt.Errorf("%w: part %s has no path", ErrIncomplete, pid)
		case size == nil || *size <= 0:
			// UNVERIFIED (research Appendix B): a part whose size is unknown cannot be confirmed on
			// disk or compared, so the version is not trusted.
			return nil, fmt.Errorf("%w: part %s has no size", ErrIncomplete, pid)
		}
		out = append(out, mediaserver.PartRef{File: p, Size: *size, ItemID: pid})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Item detail
// ---------------------------------------------------------------------------

// Item re-reads one row (GET /Items?Ids=<id>, no user) with every version. A row Jellyfin no longer
// lists (its primary left: the row id changes, S9; or a hidden alternate's id) is ErrNotFound.
// SectionKey is the library whose folders contain the row's path (the longest match; "" when two
// libraries tie). Versions are the row's file sources inside that library, keyed by their source id
// (RatingKey = the row id, MediaID 0); every version's parts are its own file plus its
// AdditionalParts, read for every version whatever PartCount says (S4). Instead of failing, a
// version is made report-only (ReportOnly) when its parts cannot be read, when the title has a
// .strm source (S19), or when it is a disc folder or image (S20). EpisodeEnd is set from the row's
// IndexNumberEnd only on the source whose path is the row's path (S21). KeyID is the smallest
// source id of the row's file versions (stable when the primary changes, research Q11). Exists and
// Accessible stay nil: Jellyfin never reports whether a file exists. While path substitutions are
// set nothing is re-read (ErrPathSubstitutions, S12).
func (c *Client) Item(ctx context.Context, itemID string) (*models.MediaItem, error) {
	id := normID(itemID)
	if id == "" {
		return nil, fmt.Errorf("%w: item id %q", ErrInvalidArgument, bounded(itemID, 80))
	}
	if err := c.pathsRewritten(ctx); err != nil {
		return nil, err
	}
	row, err := c.readRow(ctx, id, listFields)
	if err != nil {
		return nil, err
	}
	var mt models.MediaType
	switch strings.ToLower(strings.TrimSpace(row.Type)) {
	case "movie":
		mt = models.MediaTypeMovie
	case "episode":
		mt = models.MediaTypeEpisode
	default:
		return nil, fmt.Errorf("jellyfin: item %s has unsupported type %q (want Movie or Episode)", id, bounded(row.Type, 40))
	}
	if strings.TrimSpace(row.ExtraType) != "" || !strings.EqualFold(strings.TrimSpace(row.LocationType), "FileSystem") {
		return nil, fmt.Errorf("jellyfin: item %s is not a movie or episode file (extra or %q)", id, bounded(row.LocationType, 40))
	}
	if err := sourceCountProblem(row); err != nil {
		return nil, err
	}
	folders, err := c.virtualFolders(ctx, false)
	if err != nil {
		return nil, err
	}
	sectionKey, sectionTitle, locations := sectionOf(row.Path, folders)
	if len(locations) == 0 {
		return nil, fmt.Errorf("jellyfin: item %s: its path lies in no library folder", id)
	}

	item := &models.MediaItem{
		SectionKey:   sectionKey,
		LibraryTitle: sectionTitle,
		RatingKey:    id,
		MediaType:    mt,
		Title:        row.Name,
		Year:         row.ProductionYear,
		ExternalIDs:  providerIDs(row.ProviderIDs),
		ShowIDs:      map[string]string{},
		AddedAt:      parseTime(row.DateCreated),
		Versions:     []models.MediaVersion{},
		ServerKind:   models.MediaServerJellyfin,
	}
	titles := []string{row.Name}
	if mt == models.MediaTypeEpisode {
		item.ShowTitle = row.SeriesName
		item.Season = -1
		if row.ParentIndexNumber != nil {
			item.Season = *row.ParentIndexNumber
		}
		if row.IndexNumber != nil {
			item.Episode = *row.IndexNumber
		}
		titles = append(titles, row.SeriesName)
		if sid := normID(row.SeriesID); sid != "" {
			ids, err := c.seriesIDs(ctx, sid)
			if err != nil {
				return nil, fmt.Errorf("jellyfin: series %s of episode %s: %w", sid, id, err)
			}
			item.ShowIDs = ids
		}
	}

	var shortcuts []string
	for i := range row.MediaSources {
		if s := &row.MediaSources[i]; classify(s, locations) == sourceShortcut {
			shortcuts = append(shortcuts, path.Base(strings.ReplaceAll(s.Path, `\`, "/")))
		}
	}
	rowPath := pathmap.Normalize(strings.TrimSpace(row.Path))
	for i := range row.MediaSources {
		s := &row.MediaSources[i]
		if classify(s, locations) != sourceFile {
			continue
		}
		v := c.mapVersion(ctx, item, row, s, titles)
		for _, name := range shortcuts {
			v.ReportOnly = append(v.ReportOnly, fmt.Sprintf("a .strm shortcut %q is part of this title (it is never a copy)", name))
		}
		if row.IndexNumberEnd != nil && row.IndexNumber != nil && *row.IndexNumberEnd > *row.IndexNumber &&
			pathmap.Normalize(strings.TrimSpace(s.Path)) == rowPath {
			v.EpisodeEnd = *row.IndexNumberEnd
		}
		if item.KeyID == "" || v.SourceID < item.KeyID {
			item.KeyID = v.SourceID
		}
		item.Versions = append(item.Versions, v)
	}
	return item, nil
}

// readRow reads one row by id; zero rows is ErrNotFound (hidden alternates and rows whose primary
// left are not listed, S9).
func (c *Client) readRow(ctx context.Context, id, fields string) (*itemDTO, error) {
	var res itemsDTO
	q := url.Values{"Ids": {id}, "Fields": {fields}}
	if err := c.getJSON(ctx, "/Items", q, &res); err != nil {
		return nil, err
	}
	switch {
	case len(res.Items) == 0:
		return nil, fmt.Errorf("jellyfin: item %s: %w", id, ErrNotFound)
	case len(res.Items) > 1:
		return nil, fmt.Errorf("jellyfin: item %s: %w: %d rows for one id", id, ErrIncomplete, len(res.Items))
	case normID(res.Items[0].ID) != id:
		return nil, fmt.Errorf("jellyfin: item %s: %w: the answer names item %q", id, ErrIncomplete, bounded(res.Items[0].ID, 40))
	}
	row := &res.Items[0]
	row.ID = id
	return row, nil
}

// sectionOf returns the library whose folder contains p most specifically: its key, title and
// folders ("" key, with the tied libraries' folders, when two libraries tie).
func sectionOf(p string, folders []virtualFolderDTO) (key, title string, locations []string) {
	np := pathmap.Normalize(strings.TrimSpace(p))
	best := -1
	var tied []int
	for i := range folders {
		for _, l := range folders[i].Locations {
			nl := pathmap.Normalize(l)
			if !withinDir(np, nl) {
				continue
			}
			switch {
			case len(nl) > best:
				best, tied = len(nl), []int{i}
			case len(nl) == best && tied[len(tied)-1] != i:
				tied = append(tied, i)
			}
		}
	}
	if len(tied) == 0 {
		return "", "", nil
	}
	for _, i := range tied {
		locations = append(locations, folders[i].Locations...)
	}
	if len(tied) > 1 {
		return "", "", locations
	}
	return normID(folders[tied[0]].ItemID), bounded(folders[tied[0]].Name, 200), locations
}

// seriesIDs returns the provider ids of a series (fetched once per client).
func (c *Client) seriesIDs(ctx context.Context, seriesID string) (map[string]string, error) {
	if v, ok := c.showIDs.Load(seriesID); ok {
		if m, ok := v.(map[string]string); ok {
			return copyMap(m), nil
		}
	}
	row, err := c.readRow(ctx, seriesID, "ProviderIds")
	if err != nil {
		return nil, err
	}
	ids := providerIDs(row.ProviderIDs)
	c.showIDs.Store(seriesID, copyMap(ids))
	return ids, nil
}

// mapVersion maps one file source of a row (Key, ServerID and LibraryID are left to the caller).
func (c *Client) mapVersion(ctx context.Context, item *models.MediaItem, row *itemDTO, s *mediaSourceDTO, titles []string) models.MediaVersion {
	sid := normID(s.ID)
	v := models.MediaVersion{
		LibraryTitle:   item.LibraryTitle,
		SectionKey:     item.SectionKey,
		RatingKey:      item.RatingKey,
		SourceID:       sid,
		ItemTitle:      item.Title,
		Parts:          []models.MediaPart{{Path: s.Path, Size: sizeOf(s.Size), ItemID: sid}},
		DurationMs:     ticksToMs(ptrInt64(s.RunTimeTicks)),
		AudioTracks:    []models.AudioTrack{},
		SubtitleTracks: []models.SubtitleTrack{},
		AddedAt:        item.AddedAt,
	}
	if v.DurationMs <= 0 {
		v.DurationMs = ticksToMs(row.RunTimeTicks)
	}
	if s.Bitrate != nil && *s.Bitrate > 0 {
		v.BitrateKbps = int(*s.Bitrate / 1000)
	}
	if s.Size == nil || *s.Size <= 0 {
		v.ReportOnly = append(v.ReportOnly, "Jellyfin does not report the size of "+path.Base(strings.ReplaceAll(s.Path, `\`, "/")))
	}
	// Every version's parts, whatever PartCount says (S4): a failure makes the version report-only
	// rather than failing the whole item.
	if parts, err := c.additionalParts(ctx, sid); err != nil {
		v.ReportOnly = append(v.ReportOnly, reasonParts)
	} else {
		for _, p := range parts {
			v.Parts = append(v.Parts, models.MediaPart{Path: p.File, Size: p.Size, ItemID: p.ItemID})
		}
	}
	vt := strings.TrimSpace(s.VideoType)
	if vt == "" && sid == row.ID {
		vt = strings.TrimSpace(row.VideoType)
	}
	if vt != "" && !strings.EqualFold(vt, "VideoFile") {
		v.ReportOnly = append(v.ReportOnly, fmt.Sprintf("Jellyfin lists it as a disc (%s); discs on Jellyfin are only reported", bounded(vt, 20)))
	}
	applyStreams(&v, s, item.EditionTitle, titles)
	return v
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

var (
	reIMDb    = regexp.MustCompile(`^tt[0-9]+$`)
	reNumeric = regexp.MustCompile(`^[0-9]+$`)
)

// providerIDs normalizes ProviderIds to "tmdb"/"imdb"/"tvdb" (keys lower-cased, other providers
// ignored; an empty, "0" or malformed value is dropped). Never nil.
func providerIDs(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "tmdb", "tvdb":
			if reNumeric.MatchString(val) && strings.TrimLeft(val, "0") != "" {
				out[key] = strings.TrimLeft(val, "0")
			}
		case "imdb":
			if val = strings.ToLower(val); reIMDb.MatchString(val) {
				out[key] = val
			}
		}
	}
	return out
}

// parseTime parses Jellyfin's DateCreated (RFC 3339 with up to 7 fractional digits); zero when
// unset or unreadable.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if t, err = time.Parse("2006-01-02T15:04:05.9999999", s); err != nil {
			return time.Time{}
		}
	}
	if t.Year() < 1971 {
		return time.Time{}
	}
	return t.UTC()
}

// ticksToMs converts .NET ticks (100 ns) to milliseconds.
func ticksToMs(ticks int64) int64 {
	if ticks <= 0 {
		return 0
	}
	return ticks / 10_000
}

func ptrInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func sizeOf(p *int64) int64 {
	if p == nil || *p < 0 {
		return 0
	}
	return *p
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
