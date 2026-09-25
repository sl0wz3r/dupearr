package engine

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Identity keys
// ---------------------------------------------------------------------------

// ID spaces in key priority order (docs/ARCHITECTURE.md §4.2; conventions: "tmdb", "imdb",
// "tvdb" and "plex" = the plex:// GUID). "plex" is Plex's metadata-agent namespace, an external id
// like the others, not a media-server kind: it stays whichever server listed the item.
func movieIDSpaces() []string   { return []string{"tmdb", "imdb", "tvdb", "plex"} }
func episodeIDSpaces() []string { return []string{"tvdb", "tmdb", "imdb", "plex"} }

// normID normalizes an external id value: scheme ("tmdb://", "plex://movie/") and legacy agent
// query strings removed, lower-cased. "" and "0" mean absent.
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

// firstID returns the first id space (in priority order) with a usable value.
func firstID(ids map[string]string, spaces []string) (space, id string) {
	for _, sp := range spaces {
		if v := normID(sp, ids[sp]); v != "" {
			return sp, v
		}
	}
	return "", ""
}

// GroupKey returns the stable identity key of an item's group (docs/ARCHITECTURE.md §4.2):
//   - movie:   "movie:<space>:<id>" with the first available of tmdb, imdb, tvdb, plex;
//   - episode: "episode:<space>:<id>" from the episode's own ids (tvdb, tmdb, imdb, plex), else
//     "episode:<showSpace>:<showId>:s<season>e<episode>" from the show ids;
//   - fallback "<kind>:<serverID>:<itemID>" (models.ServerItemKey with the item's ServerKind and
//     KeyItemID; Plex: "plex:<serverID>:<ratingKey>").
//
// Variant suffixes ("#ed-…", "#3d", "#lang-…") are appended by BuildGroups.
func GroupKey(item models.MediaItem, serverID int64) string {
	if item.MediaType == models.MediaTypeEpisode {
		if sp, id := firstID(item.ExternalIDs, episodeIDSpaces()); id != "" {
			return "episode:" + sp + ":" + id
		}
		if item.Episode > 0 && item.Season >= 0 {
			if sp, id := firstID(item.ShowIDs, episodeIDSpaces()); id != "" {
				return fmt.Sprintf("episode:%s:%s:s%de%d", sp, id, item.Season, item.Episode)
			}
		}
	} else if sp, id := firstID(item.ExternalIDs, movieIDSpaces()); id != "" {
		return "movie:" + sp + ":" + id
	}
	return models.ServerItemKey(item.ServerKind, serverID, item.KeyItemID())
}

// ---------------------------------------------------------------------------
// Exclusions
// ---------------------------------------------------------------------------

// exclusionSet is the compiled form of models.Exclusion rules. Empty values and invalid regexes
// are ignored.
type exclusionSet struct {
	groupKeys    map[string]bool
	pathPrefixes []string
	libraries    map[int64]bool
	titles       []*regexp.Regexp
}

func compileExclusions(ex []models.Exclusion) exclusionSet {
	s := exclusionSet{groupKeys: map[string]bool{}, libraries: map[int64]bool{}}
	for _, e := range ex {
		val := strings.TrimSpace(e.Value)
		if val == "" {
			continue
		}
		switch e.Kind {
		case models.ExcludeGroupKey:
			s.groupKeys[val] = true
		case models.ExcludePathPrefix:
			if p := strings.ToLower(normPath(val)); p != "" {
				s.pathPrefixes = append(s.pathPrefixes, p)
			}
		case models.ExcludeLibrary:
			if id, err := strconv.ParseInt(val, 10, 64); err == nil {
				s.libraries[id] = true
			}
		case models.ExcludeRegex:
			// Case-insensitive: an exclusion matching more is the safe direction.
			if re, err := regexp.Compile("(?i)" + val); err == nil {
				s.titles = append(s.titles, re)
			}
		}
	}
	return s
}

// itemExcluded reports items excluded by library or title regex (title or show title).
func (s exclusionSet) itemExcluded(it *models.MediaItem) bool {
	if s.libraries[it.LibraryID] {
		return true
	}
	for _, re := range s.titles {
		if (it.Title != "" && re.MatchString(it.Title)) || (it.ShowTitle != "" && re.MatchString(it.ShowTitle)) {
			return true
		}
	}
	return false
}

// versionExcluded reports versions excluded by library or by a path prefix matching any part's
// server or local path. An absolute prefix ("/data/movies4k", "d:/movies") matches segment-aware
// from the root; a relative one ("movies4k", "media/movies4k") matches that segment sequence at
// any depth — an exclusion that silently matched nothing would expose content the user meant to
// keep away from deletion.
func (s exclusionSet) versionExcluded(v *models.MediaVersion) bool {
	if s.libraries[v.LibraryID] {
		return true
	}
	for _, p := range versionPaths(v) {
		np := strings.ToLower(normPath(p))
		for _, pre := range s.pathPrefixes {
			if hasPathPrefix(np, pre) {
				return true
			}
			if !strings.HasPrefix(pre, "/") && !isDrivePath(pre) && strings.Contains("/"+np+"/", "/"+pre+"/") {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Candidate collection
// ---------------------------------------------------------------------------

// candidate is a version eligible for grouping, with its variant attributes.
type candidate struct {
	v       *models.MediaVersion // engine-owned deep copy
	edition string               // edition key ("" = base cut)
	is3D    bool
	langs   []string // normalized audio languages
}

// itemEntry is one (de-duplicated) media-server item with its candidate versions.
type itemEntry struct {
	item        models.MediaItem // metadata only (Versions nil)
	ids         map[string]string
	cands       []candidate
	seen        map[string]bool
	unavailable bool
	scope       string
}

func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBoolPtr(b *bool) *bool {
	if b == nil {
		return nil
	}
	x := *b
	return &x
}

// cloneVersion deep-copies a version so groups never alias caller data. Slices are non-nil so
// they serialize as [] rather than null.
func cloneVersion(src *models.MediaVersion) *models.MediaVersion {
	v := *src
	v.Parts = make([]models.MediaPart, len(src.Parts))
	for i, p := range src.Parts {
		p.Exists = cloneBoolPtr(p.Exists)
		p.Accessible = cloneBoolPtr(p.Accessible)
		if p.SharedWith != nil {
			p.SharedWith = append([]string{}, p.SharedWith...)
		}
		if p.ShortcutOf != nil {
			p.ShortcutOf = append([]string{}, p.ShortcutOf...)
		}
		v.Parts[i] = p
	}
	v.AudioTracks = append([]models.AudioTrack{}, src.AudioTracks...)
	v.SubtitleTracks = append([]models.SubtitleTrack{}, src.SubtitleTracks...)
	if src.Arr != nil {
		a := *src.Arr
		a.EpisodeIDs = append([]int64{}, src.Arr.EpisodeIDs...)
		a.CustomFormats = append([]string{}, src.Arr.CustomFormats...)
		a.Languages = append([]string{}, src.Arr.Languages...)
		a.Tags = append([]string{}, src.Arr.Tags...)
		if src.Arr.CustomFormatScore != nil {
			s := *src.Arr.CustomFormatScore
			a.CustomFormatScore = &s
		}
		v.Arr = &a
	}
	v.OtherServers = CloneOtherListings(src.OtherServers)
	return &v
}

// CloneOtherListings deep-copies a version's OtherServers (nil stays nil).
func CloneOtherListings(in []models.OtherListing) []models.OtherListing {
	if in == nil {
		return nil
	}
	out := make([]models.OtherListing, len(in))
	for i, o := range in {
		o.ItemKeepsAnother = cloneBoolPtr(o.ItemKeepsAnother)
		o.KeptByGroup = cloneBoolPtr(o.KeptByGroup)
		if o.Others != nil {
			others := make([]models.OtherMedia, len(o.Others))
			for j, m := range o.Others {
				m.Same = append([]string(nil), m.Same...)
				m.Distinct = append([]string(nil), m.Distinct...)
				others[j] = m
			}
			o.Others = others
		}
		out[i] = o
	}
	return out
}

// fillVersionIdentity completes version identity fields the mapper leaves to the caller. The key is
// built with the item's server kind ("plex:<serverID>:<mediaID>" for Plex); an item with a stable
// KeyID passes it on to its versions (ItemKeyID), so the scanner's key helpers use it too.
func fillVersionIdentity(v *models.MediaVersion, it *models.MediaItem) {
	if v.ServerID == 0 {
		v.ServerID = it.ServerID
	}
	if v.LibraryID == 0 {
		v.LibraryID = it.LibraryID
	}
	if v.LibraryTitle == "" {
		v.LibraryTitle = it.LibraryTitle
	}
	if v.SectionKey == "" {
		v.SectionKey = it.SectionKey
	}
	if v.RatingKey == "" {
		v.RatingKey = it.RatingKey
	}
	if v.ItemTitle == "" {
		v.ItemTitle = it.Title
	}
	if v.ItemKeyID == "" && it.KeyID != "" {
		v.ItemKeyID = it.KeyID
	}
	v.Key = strings.TrimSpace(v.Key)
	if id := v.ServerVersionID(); v.Key == "" && id != "" {
		v.Key = models.VersionKey(it.ServerKind, v.ServerID, id)
	}
}

// collectItems de-duplicates items (same server + rating key), applies item/version exclusions and
// keeps candidate versions: not optimized, not explicitly unavailable, identifiable by Key.
func collectItems(items []models.MediaItem, opts GroupOptions, ex exclusionSet) []*itemEntry {
	byIdentity := map[string]*itemEntry{}
	var entries []*itemEntry
	for idx := range items {
		it := &items[idx]
		if ex.itemExcluded(it) {
			continue
		}
		identity := ""
		if rk := strings.TrimSpace(it.RatingKey); rk != "" {
			identity = fmt.Sprintf("%d|%s", it.ServerID, rk)
		}
		e := byIdentity[identity]
		if identity == "" || e == nil {
			e = &itemEntry{item: *it, seen: map[string]bool{}, ids: map[string]string{}}
			e.item.Versions = nil
			e.item.ExternalIDs = copyStringMap(it.ExternalIDs)
			e.item.ShowIDs = copyStringMap(it.ShowIDs)
			if lib, ok := opts.Libraries[it.LibraryID]; ok {
				e.scope = strings.ToLower(strings.TrimSpace(lib.ScopeGroup))
			}
			entries = append(entries, e)
			if identity != "" {
				byIdentity[identity] = e
			}
		} else {
			for k, v := range it.ExternalIDs {
				if strings.TrimSpace(e.item.ExternalIDs[k]) == "" {
					e.item.ExternalIDs[k] = v
				}
			}
		}
		for _, sp := range movieIDSpaces() {
			if v := normID(sp, e.item.ExternalIDs[sp]); v != "" {
				e.ids[sp] = v
			}
		}
		for vi := range it.Versions {
			v := cloneVersion(&it.Versions[vi])
			fillVersionIdentity(v, it)
			if v.Key == "" || e.seen[v.Key] || isOptimized(v) {
				continue
			}
			if isUnavailable(v) {
				e.unavailable = true
				continue
			}
			if ex.versionExcluded(v) {
				continue
			}
			e.seen[v.Key] = true
			c := candidate{v: v, edition: versionEditionKey(v, it.EditionTitle), langs: audioLangs(v)}
			c.is3D = isVersion3D(v, c.edition)
			e.cands = append(e.cands, c)
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// Units: the content identities (one item, or items matched across scoped libraries)
// ---------------------------------------------------------------------------

type unit struct {
	entries     []*itemEntry
	cands       []candidate
	meta        models.MediaItem // merged metadata of the entries (primary first)
	unavailable bool
	key         string
	// idConflicts describes id spaces in which the merged items disagree (e.g. same tmdb id but
	// different imdb ids or plex GUIDs): a sign of a mismatched merge (docs/DECISIONS.md D4 G1).
	idConflicts []string
}

func entryLess(a, b *itemEntry) bool {
	if a.item.LibraryID != b.item.LibraryID {
		return a.item.LibraryID < b.item.LibraryID
	}
	if a.item.ServerID != b.item.ServerID {
		return a.item.ServerID < b.item.ServerID
	}
	return naturalLess(a.item.RatingKey, b.item.RatingKey)
}

func candLess(a, b candidate) bool {
	if a.v.ServerID != b.v.ServerID {
		return a.v.ServerID < b.v.ServerID
	}
	if a.v.MediaID != b.v.MediaID {
		return a.v.MediaID < b.v.MediaID
	}
	return a.v.Key < b.v.Key
}

// newUnit merges entries into one content identity: candidates de-duplicated by version key,
// metadata from the primary (first) entry completed by the others.
func newUnit(entries []*itemEntry) *unit {
	sorted := append([]*itemEntry{}, entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return entryLess(sorted[i], sorted[j]) })
	u := &unit{entries: sorted}
	u.meta = sorted[0].item
	u.meta.ExternalIDs = map[string]string{}
	u.meta.ShowIDs = map[string]string{}
	seen := map[string]bool{}
	for _, e := range sorted {
		u.unavailable = u.unavailable || e.unavailable
		for _, c := range e.cands {
			if !seen[c.v.Key] {
				seen[c.v.Key] = true
				u.cands = append(u.cands, c)
			}
		}
		mergeIDs(u.meta.ExternalIDs, e.item.ExternalIDs)
		mergeIDs(u.meta.ShowIDs, e.item.ShowIDs)
		if u.meta.Title == "" {
			u.meta.Title = e.item.Title
		}
		if u.meta.Year == 0 {
			u.meta.Year = e.item.Year
		}
		if u.meta.ShowTitle == "" {
			u.meta.ShowTitle = e.item.ShowTitle
		}
		// Plex reports a missing season as -1 (never confused with 0 = specials) and a missing
		// episode number as 0: take them from another entry that knows them.
		if u.meta.Season < 0 && e.item.Season >= 0 {
			u.meta.Season = e.item.Season
		}
		if u.meta.Episode <= 0 && e.item.Episode > 0 {
			u.meta.Episode = e.item.Episode
		}
		if u.meta.Thumb == "" {
			u.meta.Thumb = e.item.Thumb
		}
		if u.meta.MediaType == "" {
			u.meta.MediaType = e.item.MediaType
		}
	}
	sort.SliceStable(u.cands, func(i, j int) bool { return candLess(u.cands[i], u.cands[j]) })
	u.key = GroupKey(u.meta, u.meta.ServerID)
	u.idConflicts = idConflicts(sorted)
	return u
}

// idConflicts lists what the merged entries disagree on: per id space, differing external ids
// ("imdb ids of the matched items differ (tt1 vs tt2)"), differing known years, and — for
// episodes — differing season/episode numbers (a show-level id mistaken for an episode id would
// otherwise merge different episodes into one group). An unknown season (-1) only conflicts
// through its episode number. Empty for a single entry or consistent data.
func idConflicts(entries []*itemEntry) []string {
	if len(entries) < 2 {
		return nil
	}
	values := map[string]map[string]bool{}
	years := map[string]bool{}
	episodes := map[string]bool{} // labels of the episodes, for the message
	seasons := map[int]bool{}     // known seasons
	numbers := map[int]bool{}     // episode numbers
	for _, e := range entries {
		for sp, v := range e.ids {
			if values[sp] == nil {
				values[sp] = map[string]bool{}
			}
			values[sp][v] = true
		}
		if e.item.Year > 0 {
			years[strconv.Itoa(e.item.Year)] = true
		}
		if e.item.MediaType == models.MediaTypeEpisode && e.item.Episode > 0 {
			episodes[EpisodeLabel(e.item.Season, e.item.Episode)] = true
			numbers[e.item.Episode] = true
			if e.item.Season >= 0 {
				seasons[e.item.Season] = true
			}
		}
	}
	if len(seasons) <= 1 && len(numbers) <= 1 {
		episodes = nil // the same episode, some entries without a season
	}
	var out []string
	for _, sp := range movieIDSpaces() {
		if len(values[sp]) > 1 {
			out = append(out, fmt.Sprintf("%s ids of the matched items differ (%s)", sp, strings.Join(setKeys(values[sp]), " vs ")))
		}
	}
	if len(years) > 1 {
		out = append(out, "years of the matched items differ ("+strings.Join(setKeys(years), " vs ")+")")
	}
	if len(episodes) > 1 {
		out = append(out, "the matched items are different episodes ("+strings.Join(setKeys(episodes), " vs ")+")")
	}
	return out
}

// mergeIDs copies ids missing in dst (sorted keys for determinism).
func mergeIDs(dst, src map[string]string) {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.TrimSpace(dst[k]) == "" && strings.TrimSpace(src[k]) != "" {
			dst[k] = src[k]
		}
	}
}

// buildUnits returns one unit per item, except that items of libraries sharing a non-empty scope
// group (same server and media type) whose external ids match across ≥2 libraries are merged into
// one unit (one group per content identity, never a second group for the same versions).
func buildUnits(entries []*itemEntry) []*unit {
	var units []*unit
	buckets := map[string][]*itemEntry{}
	var bucketKeys []string
	for _, e := range entries {
		if len(e.cands) == 0 {
			continue
		}
		if e.scope == "" || len(e.ids) == 0 {
			units = append(units, newUnit([]*itemEntry{e}))
			continue
		}
		bk := fmt.Sprintf("%d|%s|%s", e.item.ServerID, e.scope, e.item.MediaType)
		if _, ok := buckets[bk]; !ok {
			bucketKeys = append(bucketKeys, bk)
		}
		buckets[bk] = append(buckets[bk], e)
	}
	sort.Strings(bucketKeys)
	for _, bk := range bucketKeys {
		for _, comp := range idComponents(buckets[bk]) {
			for _, cluster := range splitConflicting(comp) {
				if distinctLibraries(cluster) >= 2 && (!readOnlyCluster(cluster) || distinctLibraries(cluster) == len(cluster)) {
					units = append(units, newUnit(cluster))
					continue
				}
				for _, e := range cluster {
					units = append(units, newUnit([]*itemEntry{e}))
				}
			}
		}
	}
	return units
}

// idComponents groups entries sharing any external id value (same id space).
func idComponents(es []*itemEntry) [][]*itemEntry {
	sort.SliceStable(es, func(i, j int) bool { return entryLess(es[i], es[j]) })
	uf := newUnionFind(len(es))
	first := map[string]int{}
	for i, e := range es {
		for _, sp := range movieIDSpaces() {
			if v := e.ids[sp]; v != "" {
				tok := sp + ":" + v
				if j, ok := first[tok]; ok {
					uf.union(i, j)
				} else {
					first[tok] = i
				}
			}
		}
	}
	return componentsOf(es, uf)
}

func componentsOf(es []*itemEntry, uf *unionFind) [][]*itemEntry {
	byRoot := map[int][]*itemEntry{}
	var roots []int
	for i, e := range es {
		r := uf.find(i)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], e)
	}
	sort.Ints(roots)
	out := make([][]*itemEntry, 0, len(roots))
	for _, r := range roots {
		out = append(out, byRoot[r])
	}
	return out
}

// splitConflicting returns comp unchanged when no id space carries two different values;
// otherwise the chained match is not trusted and entries are re-clustered by their primary id
// only (the group key id).
func splitConflicting(comp []*itemEntry) [][]*itemEntry {
	values := map[string]map[string]bool{}
	conflict := false
	for _, e := range comp {
		for sp, v := range e.ids {
			if values[sp] == nil {
				values[sp] = map[string]bool{}
			}
			values[sp][v] = true
			if len(values[sp]) > 1 {
				conflict = true
			}
		}
	}
	if !conflict {
		return [][]*itemEntry{comp}
	}
	byKey := map[string][]*itemEntry{}
	var keys []string
	for _, e := range comp {
		k := GroupKey(e.item, e.item.ServerID)
		if _, ok := byKey[k]; !ok {
			keys = append(keys, k)
		}
		byKey[k] = append(byKey[k], e)
	}
	sort.Strings(keys)
	out := make([][]*itemEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

// readOnlyCluster reports entries of a read-only media server (Jellyfin). Such a server's items of
// one library are separate on purpose (Jellyfin groups the copies of a title into one item itself):
// two of them sharing an external id are copies in separate folders or a stray release, which
// Phase 1 never groups (docs/DECISIONS.md D12, research §5.1, S22). A cross-library unit is only
// formed when every library contributes one item; otherwise each item stays its own unit.
func readOnlyCluster(es []*itemEntry) bool {
	for _, e := range es {
		if e.item.ServerKind.ReadOnly() {
			return true
		}
	}
	return false
}

func distinctLibraries(es []*itemEntry) int {
	seen := map[int64]bool{}
	for _, e := range es {
		seen[e.item.LibraryID] = true
	}
	return len(seen)
}

// disambiguateKeys makes unit keys unique: when several units that can form a group (≥ 2
// candidates) share a key (the same title as separate items in unrelated libraries, on several
// servers, or as separate Plex edition items), each colliding key gets the server identity of the
// unit's primary item appended ("@<kind>:<s>:<itemID>", models.ServerItemKey; Plex:
// "@plex:<s>:<rk>"). Units with a single candidate never produce a group, so they do not force a
// suffix on the others (keeps keys stable across scans).
func disambiguateKeys(units []*unit) {
	count := map[string]int{}
	for _, u := range units {
		if len(u.cands) >= 2 {
			count[u.key]++
		}
	}
	for _, u := range units {
		if len(u.cands) >= 2 && count[u.key] > 1 {
			p := u.entries[0].item
			u.key += "@" + models.ServerItemKey(p.ServerKind, p.ServerID, p.KeyItemID())
		}
	}
	sort.SliceStable(units, func(i, j int) bool {
		if units[i].key != units[j].key {
			return units[i].key < units[j].key
		}
		return entryLess(units[i].entries[0], units[j].entries[0])
	})
}

// ---------------------------------------------------------------------------
// Variant splitting (docs/DECISIONS.md D4: variants are not duplicates)
// ---------------------------------------------------------------------------

type variantGroup struct {
	suffix string
	cands  []candidate
	flags  []string
}

// splitVariants splits a unit's candidates into edition, 3D and language variants (each only when
// enabled). Suffixes: "#ed-<editionKey>" for non-base editions, "#3d" for 3D versions (always,
// so keys stay stable), "#lang-<codes>" when language variants were split.
func splitVariants(cands []candidate, opts GroupOptions) []variantGroup {
	groups := []variantGroup{{cands: cands}}
	if opts.TreatEditionsAsDistinct {
		groups = splitBy(groups, func(c candidate) string { return c.edition },
			func(k string) string { return "#ed-" + k }, models.FlagEditionSplit)
	}
	if opts.Treat3DAsDistinct {
		groups = splitBy(groups, func(c candidate) string {
			if c.is3D {
				return "3d"
			}
			return ""
		}, func(string) string { return "#3d" }, models.FlagVariant3D)
	}
	if opts.LanguageVariantsAsDistinct {
		groups = splitLanguages(groups)
	}
	return groups
}

// splitBy partitions every group by keyOf. Non-empty keys append suffixOf(key); when a group is
// actually split, every part gets flag.
func splitBy(groups []variantGroup, keyOf func(candidate) string, suffixOf func(string) string, flag string) []variantGroup {
	var out []variantGroup
	for _, g := range groups {
		parts := map[string][]candidate{}
		var keys []string
		for _, c := range g.cands {
			k := keyOf(c)
			if _, ok := parts[k]; !ok {
				keys = append(keys, k)
			}
			parts[k] = append(parts[k], c)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ng := variantGroup{suffix: g.suffix, cands: parts[k], flags: append([]string{}, g.flags...)}
			if k != "" {
				ng.suffix += suffixOf(k)
			}
			if len(keys) > 1 {
				ng.flags = addFlag(ng.flags, flag)
			}
			out = append(out, ng)
		}
	}
	return out
}

// splitLanguages splits a group into language variants (docs/DECISIONS.md D4 G5): versions are
// clustered by overlapping, non-empty audio-language sets; ≥ 2 clusters are split into
// "#lang-<codes>" parts. A cluster that contains two versions with disjoint sets — bridged by a
// multi-language release (eng, ger and eng+ger) — is partitioned by exact language set instead,
// so no version can be removed in favour of one lacking its language. When a split happens,
// versions with unknown languages form their own "#lang-und" part (they cannot be assigned
// safely).
func splitLanguages(groups []variantGroup) []variantGroup {
	var out []variantGroup
	for _, g := range groups {
		partOf := languageParts(g.cands)
		distinct := map[string]bool{}
		for _, p := range partOf {
			if p != "" {
				distinct[p] = true
			}
		}
		if len(distinct) < 2 {
			out = append(out, g)
			continue
		}
		parts := map[string][]candidate{}
		for i, c := range g.cands {
			suffix := "#lang-und"
			if partOf[i] != "" {
				suffix = "#lang-" + partOf[i]
			}
			parts[suffix] = append(parts[suffix], c)
		}
		suffixes := make([]string, 0, len(parts))
		for s := range parts {
			suffixes = append(suffixes, s)
		}
		sort.Strings(suffixes)
		for _, s := range suffixes {
			out = append(out, variantGroup{
				suffix: g.suffix + s,
				cands:  parts[s],
				flags:  addFlag(append([]string{}, g.flags...), models.FlagLanguageVariant),
			})
		}
	}
	return out
}

// languageParts returns, per candidate, its language part code ("eng+ger"; "" = unknown
// languages): the sorted union of its overlap cluster, or its exact language set when its cluster
// contains two versions with disjoint sets.
func languageParts(cands []candidate) []string {
	uf := newUnionFind(len(cands))
	first := map[string]int{}
	for i, c := range cands {
		for _, l := range c.langs {
			if j, ok := first[l]; ok {
				uf.union(i, j)
			} else {
				first[l] = i
			}
		}
	}
	members := map[int][]int{}
	for i, c := range cands {
		if len(c.langs) > 0 {
			r := uf.find(i)
			members[r] = append(members[r], i)
		}
	}
	out := make([]string, len(cands))
	for _, ms := range members {
		bridged := false
		var union []string
		for x, i := range ms {
			union = append(union, cands[i].langs...)
			for _, j := range ms[x+1:] {
				if !stringsIntersect(cands[i].langs, cands[j].langs) {
					bridged = true
				}
			}
		}
		code := strings.Join(sortedUnique(union), "+")
		for _, i := range ms {
			if bridged {
				out[i] = strings.Join(sortedUnique(cands[i].langs), "+")
			} else {
				out[i] = code
			}
		}
	}
	return out
}

func stringsIntersect(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Flags and guards
// ---------------------------------------------------------------------------

// episodeCount returns how many episodes the file of version v covers, for comparing durations
// (the sample and duration-mismatch checks): 1 for movies and ordinary episode files; for a
// multi-episode file (isShared) the largest count its signals report — distinct Sonarr episode
// ids, or 1 + the other rating keys sharing the file — and 0 when it covers several episodes but
// how many is unknown (only its file name says so, or the path index entry is blank).
func episodeCount(mt models.MediaType, v *models.MediaVersion) int {
	if mt != models.MediaTypeEpisode || !isShared(v) {
		return 1
	}
	n := 0
	if v.Arr != nil {
		eps := map[int64]bool{}
		for _, id := range v.Arr.EpisodeIDs {
			eps[id] = true
		}
		n = len(eps)
	}
	if rks := sharedWith(v); len(rks)+1 > n {
		n = len(rks) + 1
	}
	if n < 2 {
		return 0
	}
	return n
}

// durationPeers returns, for every version, the longest known duration (ms) among the versions
// that cover the same number of episodes (episodeCount, the version itself included): the
// reference of the "possible sample" check. A single-episode copy is never measured against a
// multi-episode file covering more episodes — a 45-minute episode is not a sample of a 90-minute
// double episode. 0 (no comparison) for a version whose episode count is unknown. Movies are all
// compared with each other.
func durationPeers(mt models.MediaType, vs []*models.MediaVersion) []int64 {
	counts := make([]int, len(vs))
	longest := map[int]int64{}
	for i, v := range vs {
		counts[i] = episodeCount(mt, v)
		if d := versionDuration(v); counts[i] > 0 && d > longest[counts[i]] {
			longest[counts[i]] = d
		}
	}
	out := make([]int64, len(vs))
	for i := range vs {
		if counts[i] > 0 {
			out[i] = longest[counts[i]]
		}
	}
	return out
}

// versionFlags returns the flags derivable from the versions alone: same_file, unanalyzed,
// sample, multi_episode, hardlinked, stacked, unavailable_version, full_disc (a disc version, or a
// file inside a disc structure), disc_unreadable, disc_tracked_clip. mt is the group's media type
// (durations are compared among versions covering the same number of episodes, durationPeers).
func versionFlags(mt models.MediaType, vs []*models.MediaVersion) []string {
	var flags []string
	peers := durationPeers(mt, vs)
	for i, v := range vs {
		if len(unanalyzedProblems(v)) > 0 {
			flags = addFlag(flags, models.FlagUnanalyzed)
		}
		d := versionDuration(v)
		if hasSampleName(v) || (d > 0 && float64(d) < 0.9*float64(peers[i]) && !durationLowerBound(v)) {
			flags = addFlag(flags, models.FlagSample)
		}
		if isShared(v) {
			flags = addFlag(flags, models.FlagMultiEpisode)
		}
		if isHardlinked(v) {
			flags = addFlag(flags, models.FlagHardlinked)
		}
		if isStacked(v) {
			flags = addFlag(flags, models.FlagStacked)
		}
		if isUnavailable(v) {
			flags = addFlag(flags, models.FlagUnavailableVersion)
		}
		if v.Disc != nil || discMemberPath(v) != "" {
			flags = addFlag(flags, models.FlagFullDisc)
		}
		if v.Disc != nil && strings.TrimSpace(v.Disc.Problem) != "" {
			flags = addFlag(flags, models.FlagDiscUnreadable)
		}
		if v.Disc != nil && strings.TrimSpace(v.Disc.TrackedClip) != "" {
			flags = addFlag(flags, models.FlagDiscTracked)
		}
		// Read-only media servers (Jellyfin, docs/DECISIONS.md D12): a person approves each such
		// group on its own, and a report-only reason protects the whole group.
		if readOnlyVersion(v) {
			flags = addFlag(flags, models.FlagManualOnly)
		}
		if len(v.ReportOnly) > 0 {
			flags = addFlag(flags, models.FlagReportOnly)
		}
	}
	if comp, members := sameFileComponents(vs); len(members) > 0 || len(likelySameFilePairs(vs, comp)) > 0 {
		flags = addFlag(flags, models.FlagSameFile)
	}
	return flags
}

// durationSpread compares the durations of non-stacked versions with a known duration that cover
// the same number of episodes (episodeCount: a multi-episode file is not compared with single
// episodes, one whose count is unknown with nothing; movies are all compared). mismatch reports
// whether the spread of any such set exceeds max(pct % of its longest, minutes); lo/hi are the
// shortest and longest duration of the set with the widest spread (a mismatching set first), 0
// when no set has two versions.
func durationSpread(mt models.MediaType, vs []*models.MediaVersion, pct float64, minutes int) (lo, hi int64, mismatch bool) {
	type durations struct {
		lo, hi int64
		n      int
	}
	sets := map[int]*durations{}
	var counts []int
	for _, v := range vs {
		if isStacked(v) {
			continue
		}
		d := versionDuration(v)
		c := episodeCount(mt, v)
		if d <= 0 || c == 0 {
			continue
		}
		s := sets[c]
		if s == nil {
			s = &durations{lo: d, hi: d}
			sets[c] = s
			counts = append(counts, c)
		}
		s.lo, s.hi, s.n = min(s.lo, d), max(s.hi, d), s.n+1
	}
	sort.Ints(counts)
	found := false
	var widest int64
	for _, c := range counts {
		s := sets[c]
		if s.n < 2 {
			continue
		}
		tol := math.Max(float64(s.hi)*pct/100, float64(minutes)*60000)
		mm := float64(s.hi-s.lo) > tol
		if !found || (mm && !mismatch) || (mm == mismatch && s.hi-s.lo > widest) {
			found, lo, hi, mismatch, widest = true, s.lo, s.hi, mm, s.hi-s.lo
		}
	}
	return lo, hi, mismatch
}

// suspectReasons applies the mismatched-merge checks (docs/DECISIONS.md D4 G1): group size,
// differing title folders (tags and years stripped), differing years (of the title folder, else of
// the file name), differing {tmdb/imdb/tvdb} folder tags, different *arr items within one *arr
// instance, and one *arr file matched to two versions. Empty when nothing looks suspicious.
func suspectReasons(vs []*models.MediaVersion, maxGroupSize int) []string {
	if maxGroupSize <= 0 {
		maxGroupSize = DefaultMaxGroupSize
	}
	var out []string
	if len(vs) > maxGroupSize {
		out = append(out, fmt.Sprintf("%d versions in one group (more than %d)", len(vs), maxGroupSize))
	}
	titles := map[string]string{}
	years := map[string]bool{}
	tags := map[string]map[string]bool{}
	for _, v := range vs {
		f := titleFolder(v)
		// The year of the title folder, else of the file name (G1 b): in a flat library or a
		// title folder without a year, the folder does not say which film a file is.
		y := ""
		if f != "" {
			y = folderYear(f)
		}
		if y == "" {
			y = fileNameYear(v)
		}
		if y != "" {
			years[y] = true
		}
		if f == "" {
			continue
		}
		if t := normalizeTitle(f); t != "" {
			if _, ok := titles[t]; !ok {
				titles[t] = f
			}
		}
		for sp, val := range idTags(v) {
			if tags[sp] == nil {
				tags[sp] = map[string]bool{}
			}
			tags[sp][val] = true
		}
	}
	if len(titles) > 1 {
		names := make([]string, 0, len(titles))
		for _, f := range titles {
			names = append(names, "\""+f+"\"")
		}
		sort.Strings(names)
		out = append(out, "folder titles differ ("+strings.Join(names, " vs ")+")")
	}
	if len(years) > 1 {
		out = append(out, "years differ ("+strings.Join(setKeys(years), " vs ")+")")
	}
	for _, sp := range []string{"tmdb", "imdb", "tvdb"} {
		if len(tags[sp]) > 1 {
			out = append(out, sp+" ids in folder names differ ("+strings.Join(setKeys(tags[sp]), " vs ")+")")
		}
	}
	names, ids := arrInstanceNames(vs)
	for _, inst := range ids {
		items := map[string]bool{}
		files := map[int64]int{}
		for _, v := range vs {
			if v.Arr == nil || v.Arr.InstanceID != inst {
				continue
			}
			if v.Arr.ItemID > 0 {
				items[strconv.FormatInt(v.Arr.ItemID, 10)] = true
			}
			if v.Arr.FileID > 0 {
				files[v.Arr.FileID]++
			}
		}
		if len(items) > 1 {
			out = append(out, fmt.Sprintf("tracked as different items by %s (ids %s)", names[inst],
				strings.Join(setKeys(items), ", ")))
		}
		fileIDs := make([]int64, 0, len(files))
		for id := range files {
			fileIDs = append(fileIDs, id)
		}
		sort.Slice(fileIDs, func(i, j int) bool { return fileIDs[i] < fileIDs[j] })
		for _, id := range fileIDs {
			if n := files[id]; n > 1 {
				out = append(out, fmt.Sprintf("the same %s file (id %d) is matched to %d versions", names[inst], id, n))
			}
		}
		if disjointEpisodes(vs, inst) {
			out = append(out, fmt.Sprintf("tracked as different episodes by %s", names[inst]))
		}
	}
	return out
}

// disjointEpisodes reports two versions tracked by the same *arr instance and item whose episode
// id sets are both non-empty and disjoint (different episodes merged into one item).
func disjointEpisodes(vs []*models.MediaVersion, inst int64) bool {
	for i, a := range vs {
		if a.Arr == nil || a.Arr.InstanceID != inst || len(a.Arr.EpisodeIDs) == 0 {
			continue
		}
		for _, b := range vs[i+1:] {
			if b.Arr == nil || b.Arr.InstanceID != inst || b.Arr.ItemID != a.Arr.ItemID || len(b.Arr.EpisodeIDs) == 0 {
				continue
			}
			if !intersects(a.Arr.EpisodeIDs, b.Arr.EpisodeIDs) {
				return true
			}
		}
	}
	return false
}

func intersects(a, b []int64) bool {
	set := make(map[int64]bool, len(a))
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}

// setKeys returns the sorted keys of a set.
func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i], out[j]) })
	return out
}

// ---------------------------------------------------------------------------
// BuildGroups
// ---------------------------------------------------------------------------

// BuildGroups turns candidate items into duplicate groups (docs/DECISIONS.md D4,
// docs/ARCHITECTURE.md §4.2/§5):
//   - candidates exclude Plex optimized versions and versions whose files are explicitly missing
//     (exists == false; the group gets flag unavailable_version);
//   - every item with ≥2 candidate versions is a group; items of libraries sharing a non-empty
//     ScopeGroup (same server and media type) whose external ids match (tmdb, imdb, tvdb, plex)
//     across ≥2 libraries are merged into ONE group containing all their versions;
//   - variants are split into separate groups when enabled: editions ("#ed-<key>"), 3D ("#3d"),
//     disjoint audio-language sets ("#lang-<codes>");
//   - exclusions (group_key exact, path_prefix, library id, title_regex) are applied;
//   - flags: cross_library, duration_mismatch, multi_episode, stacked, hardlinked, edition_split,
//     variant_3d, language_variant, unanalyzed, sample, same_file, suspect_merge,
//     intentional_arr_instances, unavailable_version.
//
// Only groups with ≥2 versions are returned, sorted by Key; files carry their Version only (no
// decisions — see Evaluate). The input is never modified.
func BuildGroups(items []models.MediaItem, opts GroupOptions) []*models.DuplicateGroup {
	opts = opts.normalized()
	ex := compileExclusions(opts.Exclusions)
	units := buildUnits(collectItems(items, opts, ex))
	disambiguateKeys(units)

	groups := []*models.DuplicateGroup{}
	used := map[string]bool{}
	for _, u := range units {
		for _, vg := range splitVariants(u.cands, opts) {
			if len(vg.cands) < 2 {
				continue
			}
			key := u.key + vg.suffix
			if ex.groupKeys[key] {
				continue
			}
			for n := 2; used[key]; n++ {
				key = fmt.Sprintf("%s%s~%d", u.key, vg.suffix, n)
			}
			used[key] = true
			groups = append(groups, newGroup(u, vg, key, opts))
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	return groups
}

// newGroup assembles a DuplicateGroup for one variant group of a unit.
func newGroup(u *unit, vg variantGroup, key string, opts GroupOptions) *models.DuplicateGroup {
	files := make([]models.GroupFile, len(vg.cands))
	for i, c := range vg.cands {
		files[i] = models.GroupFile{Version: *c.v, Reasons: []string{}, Values: map[string]string{}}
	}
	vs := make([]*models.MediaVersion, len(files))
	for i := range files {
		vs[i] = &files[i].Version
	}

	mt := u.meta.MediaType
	if mt == "" {
		mt = models.MediaTypeMovie
	}
	flags := append([]string{}, vg.flags...)
	for _, f := range versionFlags(mt, vs) {
		flags = addFlag(flags, f)
	}
	if u.unavailable {
		flags = addFlag(flags, models.FlagUnavailableVersion)
	}
	libs := libraryIDsOf(vs)
	if len(libs) >= 2 {
		flags = addFlag(flags, models.FlagCrossLibrary)
	}
	if _, _, mismatch := durationSpread(mt, vs, opts.DurationTolerancePercent, opts.DurationToleranceMinutes); mismatch {
		flags = addFlag(flags, models.FlagDurationMismatch)
	}
	if len(u.idConflicts) > 0 || len(suspectReasons(vs, opts.MaxGroupSize)) > 0 {
		flags = addFlag(flags, models.FlagSuspectMerge)
	}
	if _, ids := arrInstanceNames(vs); opts.DifferentArrInstancesIntentional && len(ids) >= 2 {
		flags = addFlag(flags, models.FlagIntentionalArr)
	}

	serverID := u.meta.ServerID
	if serverID == 0 && len(vs) > 0 {
		serverID = vs[0].ServerID
	}
	g := &models.DuplicateGroup{
		Key:         key,
		MediaType:   mt,
		Title:       u.meta.Title,
		Year:        u.meta.Year,
		ServerID:    serverID,
		LibraryIDs:  libs,
		ExternalIDs: copyStringMap(u.meta.ExternalIDs),
		Thumb:       u.meta.Thumb,
		Flags:       sortedUnique(flags),
		Files:       files,
	}
	if mt == models.MediaTypeEpisode {
		g.ShowTitle = u.meta.ShowTitle
		g.Season = u.meta.Season
		g.Episode = u.meta.Episode
	}
	return g
}
