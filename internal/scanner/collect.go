package scanner

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// idSpaces are the external id spaces used for cross-library matching, in the engine's order
// (conventions: "tmdb", "imdb", "tvdb" and "plex" = the plex:// GUID).
var idSpaces = [...]string{"tmdb", "imdb", "tvdb", "plex"}

// normID normalizes an external id exactly like the engine does (scheme and query removed, plex
// GUIDs reduced to their last segment, lower-cased); "" and "0" mean absent.
func normID(space, raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if space == "plex" {
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "0" {
		return ""
	}
	return s
}

// sharedKey normalizes a media-server path for the shared-file (multi-episode) index. It is
// case-insensitive on purpose: a false "shared" only protects a file from removal.
func sharedKey(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return strings.ToLower(pathmap.Normalize(p))
}

// ---------------------------------------------------------------------------
// Listing index
// ---------------------------------------------------------------------------

// indexedRef is one listed item with the library and server it was listed from.
type indexedRef struct {
	ref    mediaserver.ItemRef
	lib    models.Library
	server models.MediaServer
	// indexOnly: listed only for the shared-file index (never a candidate or partner).
	indexOnly bool
}

// libListing is the listing of one library.
type libListing struct {
	lib       models.Library
	server    models.MediaServer
	refs      []mediaserver.ItemRef
	indexOnly bool
}

// itemIndex indexes every item listed in a run: by rating key, the shared-file index
// (path → rating keys, for multi-episode files) and the cross-library identity components
// (items of scope-grouped libraries sharing an external id).
type itemIndex struct {
	refs      map[refKey]*indexedRef
	order     []refKey // listing order (libraries sorted by server, id)
	byLibrary map[int64][]refKey
	paths     map[int64]map[string][]string // server → shared path key → rating keys
	comp      map[refKey]int                // item → component id (scoped items with ids)
	// shortcuts maps, per server, a file (sharedKey) to the names of the .strm shortcuts that point
	// to it (read-only servers, readonly.go).
	shortcuts map[int64]map[string][]string
	members   map[int][]refKey
	compLibs  map[int]map[int64]bool
}

// buildIndex indexes the listings (in the given order).
func buildIndex(listings []libListing) *itemIndex {
	idx := &itemIndex{
		refs:      map[refKey]*indexedRef{},
		byLibrary: map[int64][]refKey{},
		paths:     map[int64]map[string][]string{},
		comp:      map[refKey]int{},
		members:   map[int][]refKey{},
		compLibs:  map[int]map[int64]bool{},
	}
	for _, l := range listings {
		for _, ref := range l.refs {
			rk := strings.TrimSpace(ref.RatingKey)
			if rk == "" {
				continue
			}
			k := refKey{server: l.server.ID, rk: rk}
			if _, dup := idx.refs[k]; dup {
				continue // the library changed while paging, or the item is listed twice
			}
			ref.RatingKey = rk
			idx.refs[k] = &indexedRef{ref: ref, lib: l.lib, server: l.server, indexOnly: l.indexOnly}
			idx.order = append(idx.order, k)
			idx.byLibrary[l.lib.ID] = append(idx.byLibrary[l.lib.ID], k)
			for _, m := range ref.Media {
				for _, part := range m.Parts {
					idx.addPath(l.server.ID, part.File, rk)
				}
			}
		}
	}
	idx.buildComponents()
	return idx
}

// addPath records that rating key rk references file p on server.
func (idx *itemIndex) addPath(server int64, p, rk string) {
	key := sharedKey(p)
	if key == "" {
		return
	}
	m := idx.paths[server]
	if m == nil {
		m = map[string][]string{}
		idx.paths[server] = m
	}
	for _, x := range m[key] {
		if x == rk {
			return
		}
	}
	m[key] = append(m[key], rk)
}

// buildComponents unions items of libraries sharing a non-empty scope group (same server and
// media type) that share any external id value, mirroring the engine's cross-library matching.
func (idx *itemIndex) buildComponents() {
	uf := newUnionFind(len(idx.order))
	first := map[string]int{}
	participating := make([]bool, len(idx.order))
	for i, k := range idx.order {
		ir := idx.refs[k]
		scope := normScope(ir.lib.ScopeGroup)
		if scope == "" || ir.ref.MediaCount < 1 || ir.indexOnly {
			continue
		}
		bucket := fmt.Sprintf("%d|%s|%s", k.server, scope, mediaTypeOf(ir.lib.Type))
		for _, sp := range idSpaces {
			v := normID(sp, ir.ref.ExternalIDs[sp])
			if v == "" {
				continue
			}
			participating[i] = true
			tok := bucket + "|" + sp + ":" + v
			if j, ok := first[tok]; ok {
				uf.union(i, j)
			} else {
				first[tok] = i
			}
		}
	}
	for i, k := range idx.order {
		if !participating[i] {
			continue
		}
		c := uf.find(i)
		idx.comp[k] = c
		idx.members[c] = append(idx.members[c], k)
		if idx.compLibs[c] == nil {
			idx.compLibs[c] = map[int64]bool{}
		}
		idx.compLibs[c][idx.refs[k].lib.ID] = true
	}
}

// crossLibrary reports whether item k shares an external id with an item of another library of
// its scope group.
func (idx *itemIndex) crossLibrary(k refKey) bool {
	c, ok := idx.comp[k]
	return ok && len(idx.compLibs[c]) >= 2
}

// partners returns the items of k's cross-library component (k included), or nil.
func (idx *itemIndex) partners(k refKey) []refKey {
	if !idx.crossLibrary(k) {
		return nil
	}
	return idx.members[idx.comp[k]]
}

// fullCandidates returns the items a full scan fetches in detail: items with ≥2 non-optimized
// versions, plus every item of a cross-library identity spanning ≥2 libraries. Items of
// libraries skipLib reports are left out.
func (idx *itemIndex) fullCandidates(skipLib func(int64) bool) []refKey {
	var out []refKey
	for _, k := range idx.order {
		ir := idx.refs[k]
		if ir.indexOnly || (skipLib != nil && skipLib(ir.lib.ID)) {
			continue
		}
		if ir.ref.MediaCount >= 2 || idx.crossLibrary(k) {
			out = append(out, k)
		}
	}
	return out
}

// sharedWith returns the other rating keys referencing file p on server (sorted), or nil.
func (idx *itemIndex) sharedWith(server int64, p, ownRK string) []string {
	if idx == nil {
		return nil
	}
	var out []string
	for _, rk := range idx.paths[server][sharedKey(p)] {
		if rk != ownRK {
			out = append(out, rk)
		}
	}
	sort.Strings(out)
	return out
}

// addShortcut records that the .strm shortcut name points to file p on server.
func (idx *itemIndex) addShortcut(server int64, p, name string) {
	key := sharedKey(p)
	if key == "" {
		return
	}
	if idx.shortcuts == nil {
		idx.shortcuts = map[int64]map[string][]string{}
	}
	m := idx.shortcuts[server]
	if m == nil {
		m = map[string][]string{}
		idx.shortcuts[server] = m
	}
	if !slices.Contains(m[key], name) {
		m[key] = append(m[key], name)
	}
}

// shortcutsOf returns the sorted names of the .strm shortcuts that point to file p on server.
func (idx *itemIndex) shortcutsOf(server int64, p string) []string {
	if idx == nil {
		return nil
	}
	out := slices.Clone(idx.shortcuts[server][sharedKey(p)])
	sort.Strings(out)
	return out
}

// unionFind is a minimal disjoint-set forest.
type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	u := &unionFind{parent: make([]int, n)}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

// union merges the sets of i and j; the smaller root wins (deterministic component ids).
func (u *unionFind) union(i, j int) {
	ri, rj := u.find(i), u.find(j)
	switch {
	case ri < rj:
		u.parent[rj] = ri
	case rj < ri:
		u.parent[ri] = rj
	}
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

// listLibraries lists every library (sequentially, to spare the media server) and builds the
// run's index from the successful listings. A failed library is recorded (its groups are never
// resolved in this run) and the scan continues. indexOnly libraries (see
// scanConfig.overlapPartners) are listed for the shared-file index only. A scanned library whose
// folders overlap a library that could not be listed has an incomplete shared-file index: its
// groups go to review.
func (p *pipeline) listLibraries(libs []models.Library, indexOnly map[int64]models.Library) {
	type job struct {
		lib       models.Library
		indexOnly bool
	}
	var jobs []job
	for _, l := range libs {
		jobs = append(jobs, job{lib: l})
	}
	for _, l := range sortedLibraries(indexOnly) {
		jobs = append(jobs, job{lib: l, indexOnly: true})
		p.indexOnly[l.ID] = true
	}
	scannedServers := map[int64]bool{}
	for _, l := range libs {
		scannedServers[l.ServerID] = true
	}
	var listings []libListing
	for _, j := range jobs {
		if p.ctx.Err() != nil {
			break
		}
		lib := j.lib
		srv, ok := p.cfg.servers[lib.ServerID]
		if !ok {
			p.libFailed(lib, errors.New("its media server is not enabled"))
			continue
		}
		client, err := p.serverClient(srv)
		if err != nil {
			p.libFailed(lib, err)
			continue
		}
		switch {
		case j.indexOnly && !scannedServers[lib.ServerID]:
			p.progress(fmt.Sprintf("Listing library %q on %s (another media server may list the same files)", lib.Title, srv.Name))
		case j.indexOnly:
			p.progress(fmt.Sprintf("Listing library %q on %s (its folders overlap a scanned library)", lib.Title, srv.Name))
		default:
			p.progress(fmt.Sprintf("Listing library %q on %s", lib.Title, srv.Name))
		}
		refs, err := client.AllItems(p.ctx, lib.SectionKey, mediaTypeOf(lib.Type))
		if err != nil {
			if p.ctx.Err() != nil {
				break
			}
			p.libFailed(lib, err)
			continue
		}
		listings = append(listings, libListing{lib: lib, server: srv, refs: refs, indexOnly: j.indexOnly})
		p.indexed[lib.ID] = true
		if j.indexOnly {
			continue
		}
		p.listed[lib.ID] = true
		p.run.Stats.Libraries++
		if len(refs) == 0 {
			// An empty answer is more likely a media server hiccup (library being refreshed,
			// unmounted storage) than a library that lost everything: never resolve on it.
			p.unsafeLibs[lib.ID] = true
			p.log.Warn("Library listing returned no items; its groups are not resolved in this scan",
				"library", lib.Title, "libraryId", lib.ID)
		}
	}
	p.index = buildIndex(listings)
	p.indexShortcuts(listings)
	for _, l := range listings {
		if !l.server.Kind.IsPlex() {
			// A server without library scan times (Jellyfin): the listing itself is the change
			// signal the executor re-checks before a removal (docs/DECISIONS.md D11).
			p.fingerprints[l.lib.ID] = mediaserver.ListingFingerprint(l.refs)
		}
	}
	if p.cfg.multi {
		p.buildCrossIndex()
	}
	for _, j := range jobs {
		err, failed := p.failedLibs[j.lib.ID]
		if !failed {
			continue
		}
		if p.cfg.multi {
			// Another server may list any file: a library it could not list leaves its files unknown.
			p.markUnread(j.lib.ServerID, fmt.Sprintf("its library %q could not be listed: %s", j.lib.Title, upstreamerr.Message(err)))
		}
		for _, l := range libs {
			if l.ID != j.lib.ID && p.listed[l.ID] && l.ServerID == j.lib.ServerID && locationsOverlap(l, j.lib) {
				if _, ok := p.sharedIncomplete[l.ID]; !ok {
					p.sharedIncomplete[l.ID] = j.lib.Title
				}
			}
		}
	}
}

// libFailed records a library whose listing failed.
func (p *pipeline) libFailed(lib models.Library, err error) {
	p.mu.Lock()
	p.failedLibs[lib.ID] = err
	p.run.Stats.Errors++
	p.mu.Unlock()
	p.log.Warn("Could not list library; its groups are left untouched", "library", lib.Title, "libraryId", lib.ID, "error", upstreamerr.Message(err))
}

// itemFailed records an item whose details could not be fetched.
func (p *pipeline) itemFailed(k refKey, err error) {
	p.mu.Lock()
	p.failedRKs[k] = err
	p.run.Stats.Errors++
	p.mu.Unlock()
	p.log.Warn("Could not load item details; its groups are left untouched", "serverId", k.server, "ratingKey", k.rk, "error", upstreamerr.Message(err))
}

// markIncomplete flags the items that share a cross-library identity with an item whose details
// failed: groups containing them may miss versions and are sent to review.
func (p *pipeline) markIncomplete() {
	for k := range p.failedRKs {
		p.incomplete[k] = true
		if p.index == nil {
			continue
		}
		for _, m := range p.index.partners(k) {
			p.incomplete[m] = true
		}
	}
}

// ---------------------------------------------------------------------------
// Item details
// ---------------------------------------------------------------------------

// fetchDetails fetches (with bounded concurrency) and decorates the items; failures are recorded
// per item. The result keeps the order of keys (deterministic grouping).
func (p *pipeline) fetchDetails(keys []refKey) []models.MediaItem {
	results := p.fetchDetailsSlice(keys)
	items := make([]models.MediaItem, 0, len(keys))
	for _, it := range results {
		if it != nil {
			items = append(items, *it)
		}
	}
	return items
}

// fetchDetailsKeyed is fetchDetails returning the fetched items by key (failures are absent).
func (p *pipeline) fetchDetailsKeyed(keys []refKey) map[refKey]*models.MediaItem {
	out := make(map[refKey]*models.MediaItem, len(keys))
	for i, it := range p.fetchDetailsSlice(keys) {
		if it != nil {
			out[keys[i]] = it
		}
	}
	return out
}

// fetchDetailsSlice fetches the items with at most Deps.Concurrency requests in flight;
// results[i] is nil when keys[i] failed (recorded) or the scan was cancelled.
func (p *pipeline) fetchDetailsSlice(keys []refKey) []*models.MediaItem {
	results := make([]*models.MediaItem, len(keys))
	if len(keys) == 0 {
		return results
	}
	p.progress(fmt.Sprintf("Fetching details for %d %s", len(keys), plural(len(keys), "item", "items")))
	step := int64(max(len(keys)/10, 25))
	var done atomic.Int64
	sem := make(chan struct{}, p.s.d.Concurrency)
	var wg sync.WaitGroup
dispatch:
	for i, k := range keys {
		select {
		case sem <- struct{}{}:
		case <-p.ctx.Done():
			break dispatch
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			item, err := p.fetchItem(k)
			switch {
			case err != nil && p.ctx.Err() != nil:
				// Cancelled: the scan is aborted; nothing is recorded.
			case err != nil:
				p.itemFailed(k, err)
			default:
				results[i] = item
			}
			if n := done.Add(1); n%step == 0 && int(n) < len(keys) {
				p.progress(fmt.Sprintf("Fetched details for %d of %d items", n, len(keys)))
			}
		}()
	}
	wg.Wait()
	return results
}

// fetchItem fetches and decorates one listed item. A panicking client is converted into an
// error so one bad item cannot crash the process.
func (p *pipeline) fetchItem(k refKey) (item *models.MediaItem, err error) {
	defer func() {
		if r := recover(); r != nil {
			item, err = nil, fmt.Errorf("item %s: unexpected panic: %v", k.rk, r)
		}
	}()
	ir := p.index.refs[k]
	if ir == nil {
		return nil, fmt.Errorf("item %s is not in the library listing", k.rk)
	}
	client, err := p.serverClient(ir.server)
	if err != nil {
		return nil, err
	}
	item, err = client.Item(p.ctx, k.rk)
	if err != nil {
		return nil, fmt.Errorf("item %s: %w", k.rk, err)
	}
	if item == nil {
		return nil, fmt.Errorf("item %s: empty response", k.rk)
	}
	p.decorate(item, ir)
	return item, nil
}

// decorate completes a fetched item: identity fields of the item and its versions (server and its
// kind, Dupearr library, version key "<kind>:<serverID>:<versionID>", for Plex
// "plex:<serverID>:<mediaID>"), listing data the detail lacks, SharedWith from the shared-file
// index, local paths (media-server path mappings) and, when the local file exists, its hardlink
// count and "<device>:<inode>".
func (p *pipeline) decorate(item *models.MediaItem, ir *indexedRef) {
	srv, lib, ref := ir.server, ir.lib, &ir.ref
	item.ServerID = srv.ID
	item.ServerKind = srv.Kind
	item.LibraryID = lib.ID
	item.LibraryTitle = lib.Title
	item.SectionKey = lib.SectionKey
	if strings.TrimSpace(item.RatingKey) == "" {
		item.RatingKey = ref.RatingKey
	}
	if item.MediaType == "" {
		item.MediaType = mediaTypeOf(lib.Type)
	}
	if item.ExternalIDs == nil {
		item.ExternalIDs = map[string]string{}
	}
	for k, v := range ref.ExternalIDs {
		if strings.TrimSpace(item.ExternalIDs[k]) == "" && strings.TrimSpace(v) != "" {
			item.ExternalIDs[k] = v
		}
	}
	if item.ShowIDs == nil {
		item.ShowIDs = map[string]string{}
	}
	if item.Title == "" {
		item.Title = ref.Title
	}
	if item.Year == 0 {
		item.Year = ref.Year
	}
	if item.MediaType == models.MediaTypeEpisode {
		if item.ShowTitle == "" {
			item.ShowTitle = ref.ShowTitle
		}
		// The detail reports -1 for an unknown season and 0 for specials: only fill the unknown.
		if item.Season < 0 && ref.Season >= 0 {
			item.Season = ref.Season
		}
		if item.Episode == 0 {
			item.Episode = ref.Episode
		}
	}
	if item.AddedAt.IsZero() {
		item.AddedAt = ref.AddedAt
	}
	if item.Versions == nil {
		item.Versions = []models.MediaVersion{}
	}
	for i := range item.Versions {
		v := &item.Versions[i]
		v.ServerID = srv.ID
		v.LibraryID = lib.ID
		v.LibraryTitle = lib.Title
		v.SectionKey = lib.SectionKey
		if strings.TrimSpace(v.RatingKey) == "" {
			v.RatingKey = item.RatingKey
		}
		if v.ItemTitle == "" {
			v.ItemTitle = item.Title
		}
		if v.AddedAt.IsZero() {
			v.AddedAt = item.AddedAt
		}
		v.Key = ""
		// ServerVersionID is the one place a kind's version id is defined (for Plex: set exactly
		// when MediaID > 0); a version without one is not identifiable and never grouped.
		if id := v.ServerVersionID(); id != "" {
			v.Key = models.VersionKey(srv.Kind, srv.ID, id)
		}
		var placed time.Time // when the version's newest file was put in place (see fileAges)
		unavailable := false // Plex reports a file missing that is not confirmed deleted
		for j := range v.Parts {
			part := &v.Parts[j]
			part.SharedWith = p.index.sharedWith(srv.ID, part.Path, v.RatingKey)
			part.ShortcutOf = p.index.shortcutsOf(srv.ID, part.Path)
			part.LocalPath, part.LinkCount, part.Inode = "", 0, ""
			missing := part.Exists != nil && !*part.Exists
			deleted := false
			local, mapped := p.cfg.mapper.ToLocal(models.PathSourceServer, srv.ID, part.Path)
			if srv.Kind.ReadOnly() && decorateReadOnly(srv, v, part, local, mapped) {
				// The server still lists a file that is gone on disk (readonly.go).
				missing = true
			}
			if mapped {
				part.LocalPath = local
				st := statLocal(local)
				part.LinkCount, part.Inode = st.links, st.inode
				if st.exists && part.Size > 0 && st.size != part.Size && v.Key != "" {
					p.markStale(v.Key, fmt.Sprintf("the file on disk %q is %s but the media server reports %s (it has not rescanned it, or the path mapping points elsewhere)",
						path.Base(filepath.ToSlash(local)), humanBytes(st.size), humanBytes(part.Size)))
				}
				if st.exists && st.changed.After(placed) {
					placed = st.changed
				}
				deleted = missing && !st.exists && dirExists(filepath.Dir(local))
			}
			unavailable = unavailable || (missing && !deleted)
		}
		if srv.Kind.ReadOnly() {
			if reason := p.gateReason(srv); reason != "" {
				v.ReportOnly = appendOnce(v.ReportOnly, reason)
			}
		}
		if v.Key != "" && (!placed.IsZero() || unavailable) {
			p.mu.Lock()
			if !placed.IsZero() {
				p.placedAt[v.Key] = placed
			}
			if unavailable {
				p.unavailableVersions[v.Key] = true
			}
			p.mu.Unlock()
		}
	}
}

// dirExists reports whether dir is an existing directory.
func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// localStat is what the scanner reads from a local file.
type localStat struct {
	size    int64
	links   int
	inode   string    // "<device>:<inode>" ("" when unknown)
	changed time.Time // fileChangeTime: when the file was put in place
	exists  bool
}

// statLocal stats a local regular file; exists is false (and the rest zero) when it does not
// exist or cannot be stat'ed.
func statLocal(local string) localStat {
	fi, err := os.Stat(local)
	if err != nil || !fi.Mode().IsRegular() {
		return localStat{}
	}
	st := localStat{size: fi.Size(), inode: fileIdentity(fi), changed: fileChangeTime(fi), exists: true}
	if _, n, err := pathmap.Stat(local); err == nil {
		st.links = n
	}
	return st
}

// applyFileAges gives versions a per-file date added (docs/DECISIONS.md D3, minimum age). Plex
// only has an item-level addedAt, so a NEW copy of an OLD title would look as old as the title
// and could be removed at once; an *arr's dateAdded is per file, but only for the files it tracks.
// A version that no *arr dates (untracked, or tracked without a dateAdded) whose file could be
// stat'ed therefore gets AddedAt = max(Plex addedAt, the file's change time — see
// fileChangeTime). A later date only ever delays a removal (and the executor re-checks the stored
// date), never allows one. Call after enrich (which attaches the *arr data).
func (p *pipeline) applyFileAges(items []models.MediaItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.placedAt) == 0 {
		return
	}
	for i := range items {
		for j := range items[i].Versions {
			v := &items[i].Versions[j]
			if v.Arr != nil && !v.Arr.DateAdded.IsZero() {
				continue
			}
			if t, ok := p.placedAt[v.Key]; ok && t.After(v.AddedAt) {
				v.AddedAt = t.UTC()
			}
		}
	}
}

// markStale records that the data of a version is inconsistent between the systems that
// describe it; its group goes to review.
func (p *pipeline) markStale(versionKey, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.stale[versionKey]; !ok {
		p.stale[versionKey] = reason
	}
}

// plural returns one or many depending on n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
