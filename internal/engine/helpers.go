package engine

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Compiled regular expressions (immutable after init, safe for concurrent use).
var (
	reSeasonDir  = regexp.MustCompile(`(?i)^(?:(?:season|series|staffel|saison|temporada|stagione)[ ._-]*\d+|s\d{1,4}|specials?)$`)
	reDiscDir    = regexp.MustCompile(`(?i)^(?:cd|dis[ck]|dvd|part|pt)[ ._-]*\d+$`)
	reParenYear  = regexp.MustCompile(`\(((?:19|20)\d{2})\)`)
	reBracketTag = regexp.MustCompile(`\{[^{}]*\}|\[[^\[\]]*\]`)
	reIDTag      = regexp.MustCompile(`(?i)[{\[](tmdb|imdb|tvdb)(?:id)?[-=]([^}\]\s]+)[}\]]`)
	reEditionTag = regexp.MustCompile(`(?i)\{edition-([^{}]+)\}`)
	reOrdinal    = regexp.MustCompile(`^([0-9]+)(?:st|nd|rd|th)$`)
	// reMultiEpisode matches multi-episode file names in every Sonarr multi-episode style
	// (S01E01E02, S01E01-E02, S01E01-02, S01E01.S01E02, 1x01-1x02, 1x01-02). A resolution such as
	// "S01E01-1080p" is not a range.
	reMultiEpisode = regexp.MustCompile(`(?i)(?:\bs\d{1,4}[ ._-]?e\d{1,4}(?:[ ._-]*(?:s\d{1,4}[ ._-]?)?e\d{1,4}|-\d{1,4}(?:[^0-9pi]|$))|\b\d{1,2}x\d{2,3}-(?:\d{1,2}x)?\d{2,3}(?:[^0-9pi]|$))`)
)

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// normPath cleans a media path for comparison: forward slashes, path.Clean (no trailing slash),
// Windows drive letter lower-cased, UNC prefix ("//server/share") preserved. "" stays "".
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, `\`, "/")
	unc := strings.HasPrefix(p, "//")
	p = path.Clean(p)
	if unc && !strings.HasPrefix(p, "//") {
		p = "/" + p
	}
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return p
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// isDrivePath reports whether p starts with a Windows drive ("c:/…").
func isDrivePath(p string) bool { return len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) }

// fileKey is the comparison key of a path for same-file detection. Comparison is
// case-insensitive on purpose: treating two paths as the same file is the conservative choice.
func fileKey(p string) string { return strings.ToLower(normPath(p)) }

// hasPathPrefix reports whether the normalized path p equals prefix or lies below it
// (segment-aware: "/data/movies" does not match "/data/movies2"). Both must be normalized.
func hasPathPrefix(p, prefix string) bool {
	if p == "" || prefix == "" {
		return false
	}
	if p == prefix {
		return true
	}
	if strings.HasSuffix(prefix, "/") || strings.HasSuffix(prefix, ":") {
		return strings.HasPrefix(p, prefix) && (strings.HasSuffix(prefix, "/") || strings.HasPrefix(p[len(prefix):], "/"))
	}
	return strings.HasPrefix(p, prefix+"/")
}

// pathUnder is a case-insensitive hasPathPrefix on raw paths (used for exclusions, where matching
// more is the safe direction).
func pathUnder(p, prefix string) bool {
	return hasPathPrefix(strings.ToLower(normPath(p)), strings.ToLower(normPath(prefix)))
}

// versionPaths returns the non-empty Path and LocalPath of every part.
func versionPaths(v *models.MediaVersion) []string {
	out := make([]string, 0, 2*len(v.Parts))
	for _, p := range v.Parts {
		if strings.TrimSpace(p.Path) != "" {
			out = append(out, p.Path)
		}
		if strings.TrimSpace(p.LocalPath) != "" {
			out = append(out, p.LocalPath)
		}
	}
	return out
}

// primaryPath is the first part's server path (or local path when the server path is empty).
func primaryPath(v *models.MediaVersion) string {
	for _, p := range v.Parts {
		if strings.TrimSpace(p.Path) != "" {
			return p.Path
		}
		if strings.TrimSpace(p.LocalPath) != "" {
			return p.LocalPath
		}
	}
	return ""
}

func stripExt(name string) string {
	if ext := path.Ext(name); ext != "" && len(ext) <= 6 && !strings.Contains(ext, " ") {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

// hasGlobMeta reports whether a pattern contains doublestar metacharacters.
func hasGlobMeta(p string) bool { return strings.ContainsAny(p, "*?[{") }

// globMatch matches a doublestar pattern against paths (Path and LocalPath of a version).
// A pattern without "/" matches the file name only (e.g. "*Remux*"); a pattern with "/" is
// matched against the full normalized path, a relative one (e.g. "movies4k/**") also at any
// depth, and a pattern without metacharacters is a segment-aware path prefix. Invalid patterns
// never match.
func globMatch(pattern string, paths []string, caseSensitive bool) bool {
	pat := strings.ReplaceAll(strings.TrimSpace(pattern), `\`, "/")
	if pat == "" {
		return false
	}
	if !caseSensitive {
		pat = strings.ToLower(pat)
	}
	if !doublestar.ValidatePattern(pat) {
		return false
	}
	literal := !hasGlobMeta(pat)
	if literal {
		pat = normPath(pat)
		if !caseSensitive {
			pat = strings.ToLower(pat)
		}
	}
	for _, p := range paths {
		np := normPath(p)
		if np == "" {
			continue
		}
		if !caseSensitive {
			np = strings.ToLower(np)
		}
		switch {
		case !strings.Contains(pat, "/"):
			if doublestar.MatchUnvalidated(pat, path.Base(np)) {
				return true
			}
		case literal:
			if hasPathPrefix(np, pat) {
				return true
			}
		default:
			if doublestar.MatchUnvalidated(pat, np) {
				return true
			}
			if !strings.HasPrefix(pat, "/") && !strings.HasPrefix(pat, "*") && !isDrivePath(pat) &&
				doublestar.MatchUnvalidated("**/"+pat, np) {
				return true
			}
		}
	}
	return false
}

// protectionGlobMatch matches a path_glob protection. Protections are a safety feature, so on top
// of globMatch (case-insensitive) a pattern also protects everything below the directories it
// matches, and a relative pattern matches at any depth: "movies4k", "Movies4K/", "*4K*",
// "media/movies4k" and "/data/movies4k*" all protect "/data/movies4k/Dune (2021)/Dune.mkv"
// (globMatch alone would test a slash-less pattern against the file name only, and a relative
// literal against the path root only). Invalid patterns never match (ValidateProfile rejects them).
func protectionGlobMatch(pattern string, paths []string) bool {
	if globMatch(pattern, paths, false) {
		return true
	}
	pat := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pattern), `\`, "/"))
	pat = strings.TrimRight(pat, "/")
	if pat == "" || !doublestar.ValidatePattern(pat) {
		return false
	}
	pats := []string{pat, pat + "/**"}
	if !strings.HasPrefix(pat, "/") && !isDrivePath(pat) && !strings.HasPrefix(pat, "**") {
		pats = append(pats, "**/"+pat, "**/"+pat+"/**")
	}
	for _, p := range paths {
		np := strings.ToLower(normPath(p))
		if np == "" {
			continue
		}
		for _, pt := range pats {
			if doublestar.MatchUnvalidated(pt, np) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tokens, titles, years
// ---------------------------------------------------------------------------

// tokenize lower-cases s and splits it on everything that is not a letter or a digit.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isYearToken(t string) bool {
	if len(t) != 4 || !(strings.HasPrefix(t, "19") || strings.HasPrefix(t, "20")) {
		return false
	}
	_, err := strconv.Atoi(t)
	return err == nil
}

// titlePath is the path whose folders and file name name the title of a version: the first part's
// path, except for a full disc — its parts are the disc roots (folders) or, from a custom Plex
// scanner, clips deep inside BDMV/STREAM — where it is a path directly inside the disc root (or the
// image file), so that the root's parent folders name the title.
func titlePath(v *models.MediaVersion) string {
	if d := v.Disc; d != nil {
		root := strings.TrimSpace(d.Root)
		if root == "" {
			root = strings.TrimSpace(d.LocalRoot)
		}
		if root == "" {
			if r, _, ok := disc.RootOf(primaryPath(v)); ok {
				root = r
			}
		}
		switch {
		case root == "":
		case d.IsImage():
			return root
		default:
			return strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/") + "/BDMV"
		}
	}
	p := primaryPath(v)
	if r, _, ok := disc.RootOf(p); ok && r != "" {
		return strings.TrimRight(strings.ReplaceAll(r, `\`, "/"), "/") + "/BDMV"
	}
	return p
}

// titleFolder returns the name of the folder that names the title a version lives in: the
// parent folder of the file (of a disc's root), skipping season ("Season 01", "Specials") and disc
// ("CD1", "Disc 2") folders. "" when the path has no usable folder.
func titleFolder(v *models.MediaVersion) string {
	np := normPath(titlePath(v))
	if np == "" {
		return ""
	}
	dir := path.Dir(np)
	for i := 0; i < 4; i++ {
		if dir == "/" || dir == "." || dir == "" || dir == "//" || (len(dir) == 2 && isDrivePath(dir)) {
			return ""
		}
		b := path.Base(dir)
		if reSeasonDir.MatchString(b) || reDiscDir.MatchString(b) {
			dir = path.Dir(dir)
			continue
		}
		return b
	}
	return ""
}

// qualityTokens are dropped from folder titles before comparing them.
func isQualityToken(t string) bool {
	switch t {
	case "4k", "uhd", "hdr", "hdr10", "sdr", "dv", "remux", "bluray", "web", "webdl", "3d", "imax":
		return true
	}
	if len(t) >= 4 && strings.HasSuffix(t, "p") {
		if _, err := strconv.Atoi(t[:len(t)-1]); err == nil {
			return true
		}
	}
	return false
}

// normalizeTitle turns a folder name into a comparable title: bracket/brace tags
// ({edition-…}, {tmdb-…}, [imdb-…], [1080p] …) and parenthesized years removed, everything from
// the first bare year token on dropped (scene names: "Dune.2021.2160p.UHD" → "dune"; a leading
// year such as "1917" is part of the title), lower-cased, punctuation, articles and quality
// words dropped.
func normalizeTitle(folder string) string {
	s := reBracketTag.ReplaceAllString(folder, " ")
	s = reParenYear.ReplaceAllString(s, " ")
	s = strings.NewReplacer("'", "", "’", "", "`", "", "&", " and ").Replace(s)
	toks := tokenize(s)
	for i := 1; i < len(toks); i++ {
		if isYearToken(toks[i]) {
			toks = toks[:i]
			break
		}
	}
	out := toks[:0]
	for _, t := range toks {
		if t == "the" || t == "a" || t == "an" || isQualityToken(t) {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// folderYear returns the release year encoded in a folder name ("Dune (2021)" → "2021"),
// preferring a parenthesized year, else the last standalone year token. "" when none.
func folderYear(folder string) string {
	if m := reParenYear.FindAllStringSubmatch(folder, -1); len(m) > 0 {
		return m[len(m)-1][1]
	}
	toks := tokenize(reBracketTag.ReplaceAllString(folder, " "))
	for i := len(toks) - 1; i >= 0; i-- {
		if isYearToken(toks[i]) {
			return toks[i]
		}
	}
	return ""
}

// fileNameYear returns the release year a version's file name states: the first parenthesized
// year ("The Thing (1982) - Remastered (2023).mkv" → 1982: Plex naming puts it right after the
// title), else the last bare year token that is neither the first token nor the last one — scene
// names put the year between the title and the quality tokens ("The.Thing.1982.1080p.BluRay"),
// while a leading or trailing year-like word is usually part of the title ("1917.mkv",
// "Blade Runner 2049.mkv"). "" when the name states no year.
func fileNameYear(v *models.MediaVersion) string {
	if v.Disc != nil && !v.Disc.IsImage() {
		return "" // a disc folder's files (BDMV, 00800.m2ts …) never state the year
	}
	base := path.Base(normPath(titlePath(v)))
	if base == "." || base == "/" || base == "" {
		return ""
	}
	name := strings.TrimSuffix(base, path.Ext(base))
	if m := reParenYear.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	toks := tokenize(reBracketTag.ReplaceAllString(name, " "))
	for i := len(toks) - 2; i >= 1; i-- {
		if isYearToken(toks[i]) {
			return toks[i]
		}
	}
	return ""
}

// idTags returns the {tmdb-…}/{imdb-…}/{tvdb-…} tags (and bracket variants) found in the title
// folder and file name of a version.
func idTags(v *models.MediaVersion) map[string]string {
	out := map[string]string{}
	np := normPath(titlePath(v))
	for _, s := range []string{titleFolder(v), path.Base(np)} {
		for _, m := range reIDTag.FindAllStringSubmatch(s, -1) {
			space, val := strings.ToLower(m[1]), strings.ToLower(strings.TrimSpace(m[2]))
			if val != "" {
				if _, ok := out[space]; !ok {
					out[space] = val
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Version properties
// ---------------------------------------------------------------------------

// isOptimized reports Plex Optimized Versions (never duplicates, never keepers, never deleted):
// the mapper flag, or any part stored under a "Plex Versions" folder.
func isOptimized(v *models.MediaVersion) bool {
	if v.OptimizedVersion {
		return true
	}
	for _, p := range versionPaths(v) {
		np := "/" + strings.Trim(fileKey(p), "/") + "/"
		if strings.Contains(np, "/plex versions/") {
			return true
		}
	}
	return false
}

// isUnavailable reports a version whose file is explicitly missing (any part exists==false) or
// that has no parts at all. Absent exists is unknown, never "missing" (docs/DECISIONS.md D2).
func isUnavailable(v *models.MediaVersion) bool {
	if len(v.Parts) == 0 {
		return true
	}
	for _, p := range v.Parts {
		if p.Exists != nil && !*p.Exists {
			return true
		}
	}
	return false
}

// isInaccessible reports a version with a part explicitly not accessible to the media server.
func isInaccessible(v *models.MediaVersion) bool {
	for _, p := range v.Parts {
		if p.Accessible != nil && !*p.Accessible {
			return true
		}
	}
	return false
}

// sharedWith returns the sorted, de-duplicated rating keys other items share this version's
// files with (multi-episode files). Empty when not shared.
func sharedWith(v *models.MediaVersion) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range v.Parts {
		for _, rk := range p.SharedWith {
			rk = strings.TrimSpace(rk)
			if rk == "" || seen[rk] {
				continue
			}
			seen[rk] = true
			out = append(out, rk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i], out[j]) })
	return out
}

// multiEpisodeReasons explains why a version's file may cover several episodes (prior-art V7,
// docs/ARCHITECTURE.md §6 invariant 4). The engine never removes such a file: removing it would
// also delete the other episodes it contains. Any one signal suffices (when in doubt the file is
// treated as shared):
//   - a part carries SharedWith rating keys (the scanner's path → rating keys index; even a blank
//     entry counts);
//   - the *arr file is referenced by several episodes (a Sonarr episodefile with ≥ 2 episode ids,
//     docs/DECISIONS.md D3) — this still works when the path index is incomplete, e.g. in a
//     targeted scan that did not list the whole section;
//   - a file name follows a multi-episode naming style (S01E01E02, S01E01-E02, S01E01-02,
//     S01E01.S01E02, 1x01-1x02).
//
// Empty when there is no sign of a multi-episode file.
func multiEpisodeReasons(v *models.MediaVersion) []string {
	var out []string
	shared := false
	for _, p := range v.Parts {
		shared = shared || len(p.SharedWith) > 0
	}
	if shared {
		if rks := sharedWith(v); len(rks) > 0 {
			out = append(out, "file is shared with other episodes ("+strings.Join(rks, ", ")+")")
		} else {
			out = append(out, "file is shared with other episodes")
		}
	}
	if v.Arr != nil {
		eps := map[int64]bool{}
		for _, id := range v.Arr.EpisodeIDs {
			eps[id] = true
		}
		if len(eps) > 1 {
			out = append(out, fmt.Sprintf("the %s file covers %d episodes", arrName(v.Arr), len(eps)))
		}
	}
	for _, p := range versionPaths(v) {
		if np := normPath(p); np != "" && reMultiEpisode.MatchString(stripExt(path.Base(np))) {
			out = append(out, "file name indicates a multi-episode file")
			break
		}
	}
	return out
}

// isShared reports whether a version's file may cover several episodes (see multiEpisodeReasons).
func isShared(v *models.MediaVersion) bool { return len(multiEpisodeReasons(v)) > 0 }

func isStacked(v *models.MediaVersion) bool { return len(v.Parts) > 1 }

func isHardlinked(v *models.MediaVersion) bool {
	if v.Disc != nil && v.Disc.HardlinkedFiles > 0 {
		return true // moving the disc frees nothing for its hardlinked files (a seeding copy)
	}
	for _, p := range v.Parts {
		if p.LinkCount > 1 {
			return true
		}
	}
	return false
}

// unanalyzedProblems lists why a version looks unanalyzed/corrupt (no video codec, width 0 or
// bitrate 0); empty when it looks analyzed. A full disc whose metadata could not be read, or a disc
// image (whose attributes Dupearr cannot read), is reported as such.
func unanalyzedProblems(v *models.MediaVersion) []string {
	if d := v.Disc; d != nil && (strings.TrimSpace(v.VideoCodec) == "" || v.Width <= 0 || (v.BitrateKbps <= 0 && v.VideoBitrate <= 0)) {
		switch {
		case strings.TrimSpace(d.Problem) != "":
			return []string{"disc metadata unreadable"}
		case d.IsImage():
			return []string{"disc image: quality unknown"}
		default:
			return []string{"disc metadata incomplete"}
		}
	}
	var out []string
	if strings.TrimSpace(v.VideoCodec) == "" {
		out = append(out, "no video codec")
	}
	if v.Width <= 0 {
		out = append(out, "width unknown")
	}
	if v.BitrateKbps <= 0 && v.VideoBitrate <= 0 {
		out = append(out, "bitrate unknown")
	}
	return out
}

// versionDuration is the version duration in ms (media duration, else sum of part durations).
func versionDuration(v *models.MediaVersion) int64 {
	if v.DurationMs > 0 {
		return v.DurationMs
	}
	var n int64
	for _, p := range v.Parts {
		if p.Duration > 0 {
			n += p.Duration
		}
	}
	return n
}

// isSamplePath reports sample-like file names: a "sample" token in the file name, or a parent
// folder named "sample"/"samples".
func isSamplePath(p string) bool {
	np := normPath(p)
	if np == "" {
		return false
	}
	for _, t := range tokenize(stripExt(path.Base(np))) {
		if t == "sample" {
			return true
		}
	}
	dir := strings.ToLower(path.Base(path.Dir(np)))
	return dir == "sample" || dir == "samples"
}

func hasSampleName(v *models.MediaVersion) bool {
	for _, p := range versionPaths(v) {
		if isSamplePath(p) {
			return true
		}
	}
	return false
}

// dateAdded is the *arr dateAdded when known, else the media-server addedAt.
func dateAdded(v *models.MediaVersion) time.Time {
	if v.Arr != nil && !v.Arr.DateAdded.IsZero() {
		return v.Arr.DateAdded
	}
	return v.AddedAt
}

// newestAdded is the later of addedAt and *arr dateAdded (used for min-age protection).
func newestAdded(v *models.MediaVersion) time.Time {
	t := v.AddedAt
	if v.Arr != nil && v.Arr.DateAdded.After(t) {
		t = v.Arr.DateAdded
	}
	return t
}

// validInode filters inode strings that cannot identify a file.
func validInode(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && s != "0" && !strings.HasSuffix(s, ":0") && !strings.HasSuffix(s, ":")
}

// sameFileKeys returns the identity keys of a version's files: normalized paths (server and
// local, case-insensitive), "<device>:<inode>", and the *arr file it was matched to (two versions
// matched to one *arr file must never be split into keep/remove: an *arr-side delete of the
// "loser" would delete the keeper's file).
func sameFileKeys(v *models.MediaVersion) []string {
	var out []string
	if v.Arr != nil && v.Arr.FileID > 0 {
		out = append(out, fmt.Sprintf("a:%d:%d", v.Arr.InstanceID, v.Arr.FileID))
	}
	for _, p := range v.Parts {
		if k := fileKey(p.Path); k != "" {
			out = append(out, "p:"+k)
		}
		if k := fileKey(p.LocalPath); k != "" {
			out = append(out, "l:"+k)
		}
		if validInode(p.Inode) {
			out = append(out, "i:"+strings.TrimSpace(p.Inode))
		}
	}
	return append(out, discRootKeys(v)...)
}

// discRootKeys are the same-file keys of the disc a version is or lies in: two versions of one disc
// (a disc found on disk and the same disc listed by a custom Plex scanner, or a clip of it) share
// its files and must never be split into keep and remove.
func discRootKeys(v *models.MediaVersion) []string {
	var out []string
	add := func(prefix, p string) {
		if k := fileKey(p); k != "" {
			out = append(out, prefix+k)
		}
	}
	if d := v.Disc; d != nil {
		add("dr:", d.Root)
		for _, r := range d.Roots {
			add("dr:", r)
		}
		add("dl:", d.LocalRoot)
		for _, r := range d.LocalRoots {
			add("dl:", r)
		}
		return out
	}
	for _, p := range v.Parts {
		if r, _, ok := disc.RootOf(p.Path); ok {
			add("dr:", r)
		}
		if r, _, ok := disc.RootOf(p.LocalPath); ok {
			add("dl:", r)
		}
	}
	return out
}

// sameFileComponents groups versions that share a file (same normalized path or same inode).
// comp[i] is the component id of version i; members lists the components with ≥ 2 versions.
func sameFileComponents(vs []*models.MediaVersion) (comp []int, members map[int][]int) {
	uf := newUnionFind(len(vs))
	first := map[string]int{}
	for i, v := range vs {
		for _, k := range sameFileKeys(v) {
			if j, ok := first[k]; ok {
				uf.union(i, j)
			} else {
				first[k] = i
			}
		}
	}
	comp = make([]int, len(vs))
	all := map[int][]int{}
	for i := range vs {
		comp[i] = uf.find(i)
		all[comp[i]] = append(all[comp[i]], i)
	}
	members = map[int][]int{}
	for c, m := range all {
		if len(m) > 1 {
			members[c] = m
		}
	}
	return comp, members
}

// likelySameFilePairs returns the pairs of versions (i < j, not already known to share a file)
// whose files have the same names and sizes in different folders. Such versions may be ONE file
// reached through two paths (e.g. /movies and /data/movies mounted from the same host folder), and
// only a device+inode comparison can rule that out: removing one of them through Plex would delete
// the other's data (prior-art G8). A pair is ruled out when some part has known, differing inodes
// on both sides (provably different files). Pairs are ordered by version key (the first element
// has the smaller key), independent of the order of vs.
func likelySameFilePairs(vs []*models.MediaVersion, comp []int) [][2]int {
	idx := make([]int, len(vs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vs[idx[a]].Key < vs[idx[b]].Key })
	var out [][2]int
	for x, i := range idx {
		for _, j := range idx[x+1:] {
			if comp[i] != comp[j] && sameNamesAndSizes(vs[i], vs[j]) {
				out = append(out, [2]int{i, j})
			}
		}
	}
	return out
}

// sameNamesAndSizes reports whether a and b have the same number of parts and, part by part, the
// same file name (case-insensitive) and the same known size, without provably different inodes.
func sameNamesAndSizes(a, b *models.MediaVersion) bool {
	if len(a.Parts) == 0 || len(a.Parts) != len(b.Parts) {
		return false
	}
	for k := range a.Parts {
		pa, pb := &a.Parts[k], &b.Parts[k]
		if pa.Size <= 0 || pa.Size != pb.Size {
			return false
		}
		na, nb := partName(pa), partName(pb)
		if na == "" || na != nb {
			return false
		}
		if validInode(pa.Inode) && validInode(pb.Inode) && strings.TrimSpace(pa.Inode) != strings.TrimSpace(pb.Inode) {
			return false
		}
	}
	return true
}

// partName is the lower-cased file name of a part (server path, else local path).
func partName(p *models.MediaPart) string {
	np := fileKey(p.Path)
	if np == "" {
		np = fileKey(p.LocalPath)
	}
	if np == "" {
		return ""
	}
	return path.Base(np)
}

// libraryIDsOf returns the sorted distinct non-zero library ids of versions.
func libraryIDsOf(vs []*models.MediaVersion) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, v := range vs {
		if v.LibraryID != 0 && !seen[v.LibraryID] {
			seen[v.LibraryID] = true
			out = append(out, v.LibraryID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// arrInstanceNames returns the distinct *arr instances tracking versions (id → display name),
// plus the sorted ids.
func arrInstanceNames(vs []*models.MediaVersion) (map[int64]string, []int64) {
	names := map[int64]string{}
	var ids []int64
	for _, v := range vs {
		if v.Arr == nil || v.Arr.InstanceID == 0 {
			continue
		}
		if _, ok := names[v.Arr.InstanceID]; !ok {
			ids = append(ids, v.Arr.InstanceID)
			names[v.Arr.InstanceID] = arrName(v.Arr)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return names, ids
}

// arrName is a display name for the *arr instance tracking a file.
func arrName(a *models.ArrFileInfo) string {
	if a == nil {
		return ""
	}
	if n := strings.TrimSpace(a.InstanceName); n != "" {
		return n
	}
	kind := "*arr"
	switch a.Kind {
	case models.ArrRadarr:
		kind = "Radarr"
	case models.ArrSonarr:
		kind = "Sonarr"
	}
	if a.InstanceID != 0 {
		return fmt.Sprintf("%s #%d", kind, a.InstanceID)
	}
	return kind
}

// ---------------------------------------------------------------------------
// 3D, editions
// ---------------------------------------------------------------------------

// EpisodeLabel renders a season/episode pair as "S01E02"; an unknown season (-1, Plex did not
// report one) is shown as "S??E02" rather than a misleading "S-1E02" or "S00E02" (specials), and
// an unknown episode number (0) as "E??".
func EpisodeLabel(season, episode int) string {
	s, e := "??", "??"
	if season >= 0 {
		s = fmt.Sprintf("%02d", season)
	}
	if episode > 0 {
		e = fmt.Sprintf("%02d", episode)
	}
	return "S" + s + "E" + e
}

// isVersion3D reports 3D versions: a "3D" edition, or 3D release tokens in a part's file or
// folder name (mediainfo.Is3D, the TRaSH "3D" custom format tokens — BluRay3D, BD3D, (H/Half/F/Full)
// SBS, OU/TAB, and "3D" after the release year or before it when the folder name lacks it, e.g.
// "Avatar 3D (2009).mkv" inside "Avatar (2009)/"). Errors lean towards 3D: a false positive only
// keeps two copies in separate groups.
func isVersion3D(v *models.MediaVersion, editionKey string) bool {
	if v.Disc != nil && v.Disc.Is3D {
		return true
	}
	for _, t := range strings.Split(editionKey, "-") {
		if t == "3d" {
			return true
		}
	}
	for _, p := range versionPaths(v) {
		if mediainfo.Is3D(p) {
			return true
		}
	}
	return false
}

// editionFromPath extracts a Plex "{edition-Name}" tag from a path (file name wins over folder).
func editionFromPath(p string) string {
	m := reEditionTag.FindAllStringSubmatch(normPath(p), -1)
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSpace(m[len(m)-1][1])
}

// editionKey normalizes an edition to a grouping slug (same rules as mediainfo.EditionKey, kept
// local so group keys never change because another package evolves): apostrophes and punctuation
// dropped, filler words (edition, cut, version, the, collection) removed, "director"/"collector"
// pluralized, ordinals reduced to their number. "Director's Cut" → "directors", "Extended
// Edition" → "extended", "25th Anniversary Edition" → "25-anniversary"; "Theatrical", "Original
// Theatrical", "Standard" and "" → "" (the base cut).
func editionKey(e string) string {
	s := strings.NewReplacer("'", "", "’", "", "`", "").Replace(strings.TrimSpace(e))
	toks := tokenize(s)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		switch t {
		case "edition", "cut", "version", "the", "collection":
			continue
		case "director":
			t = "directors"
		case "collector":
			t = "collectors"
		}
		if m := reOrdinal.FindStringSubmatch(t); m != nil {
			t = m[1]
		}
		out = append(out, t)
	}
	key := strings.Join(out, "-")
	switch key {
	case "theatrical", "original-theatrical", "theatrical-release", "original-theatrical-release", "standard":
		return ""
	}
	return key
}

// versionEditionKey returns the edition key of a version from (first non-empty) the normalized
// version edition, the *arr edition, the Plex item edition title, or a {edition-…} path tag.
func versionEditionKey(v *models.MediaVersion, itemEdition string) string {
	cands := []string{v.Edition}
	if v.Arr != nil {
		cands = append(cands, v.Arr.Edition)
	}
	cands = append(cands, itemEdition)
	for _, p := range versionPaths(v) {
		cands = append(cands, editionFromPath(p))
	}
	for _, c := range cands {
		if strings.TrimSpace(c) != "" {
			return editionKey(c)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Languages
// ---------------------------------------------------------------------------

// normLang returns a normalized ISO 639-2/B code for a track language code (639-1, 639-2/B or
// 639-2/T, optionally with a region suffix) or, failing that, its display name. "" = unknown.
func normLang(code, name string) string {
	if l := langFromCode(code); l != "" {
		return l
	}
	return langFromName(name)
}

func langFromCode(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if i := strings.IndexAny(c, "-_"); i > 0 {
		c = c[:i]
	}
	switch c {
	case "", "und", "unk", "mis", "mul", "zxx", "xx", "qaa", "none":
		return ""
	}
	switch len(c) {
	case 2:
		if l := iso6391To6392B(c); l != "" {
			return l
		}
		return c
	case 3:
		return iso6392TToB(c)
	}
	return langFromName(c)
}

func iso6391To6392B(c string) string {
	switch c {
	case "en":
		return "eng"
	case "de":
		return "ger"
	case "fr":
		return "fre"
	case "es":
		return "spa"
	case "it":
		return "ita"
	case "pt":
		return "por"
	case "nl":
		return "dut"
	case "sv":
		return "swe"
	case "no":
		return "nor"
	case "nb":
		return "nob"
	case "nn":
		return "nno"
	case "da":
		return "dan"
	case "fi":
		return "fin"
	case "is":
		return "ice"
	case "pl":
		return "pol"
	case "cs":
		return "cze"
	case "sk":
		return "slo"
	case "hu":
		return "hun"
	case "ro":
		return "rum"
	case "bg":
		return "bul"
	case "hr":
		return "hrv"
	case "sr":
		return "srp"
	case "sl":
		return "slv"
	case "el":
		return "gre"
	case "tr":
		return "tur"
	case "ru":
		return "rus"
	case "uk":
		return "ukr"
	case "he", "iw":
		return "heb"
	case "ar":
		return "ara"
	case "fa":
		return "per"
	case "hi":
		return "hin"
	case "bn":
		return "ben"
	case "ta":
		return "tam"
	case "te":
		return "tel"
	case "ml":
		return "mal"
	case "kn":
		return "kan"
	case "mr":
		return "mar"
	case "ur":
		return "urd"
	case "th":
		return "tha"
	case "vi":
		return "vie"
	case "id":
		return "ind"
	case "ms":
		return "may"
	case "zh":
		return "chi"
	case "ja":
		return "jpn"
	case "ko":
		return "kor"
	case "et":
		return "est"
	case "lv":
		return "lav"
	case "lt":
		return "lit"
	case "ca":
		return "cat"
	case "eu":
		return "baq"
	case "gl":
		return "glg"
	case "ga":
		return "gle"
	case "cy":
		return "wel"
	case "sq":
		return "alb"
	case "mk":
		return "mac"
	case "hy":
		return "arm"
	case "ka":
		return "geo"
	case "af":
		return "afr"
	case "sw":
		return "swa"
	case "tl":
		return "tgl"
	}
	return ""
}

func iso6392TToB(c string) string {
	switch c {
	case "deu":
		return "ger"
	case "fra":
		return "fre"
	case "nld":
		return "dut"
	case "ces":
		return "cze"
	case "slk":
		return "slo"
	case "ron":
		return "rum"
	case "ell":
		return "gre"
	case "fas":
		return "per"
	case "zho":
		return "chi"
	case "msa":
		return "may"
	case "isl":
		return "ice"
	case "eus":
		return "baq"
	case "cym":
		return "wel"
	case "sqi":
		return "alb"
	case "mkd":
		return "mac"
	case "hye":
		return "arm"
	case "kat":
		return "geo"
	case "bod":
		return "tib"
	case "mya":
		return "bur"
	case "mri":
		return "mao"
	}
	return c
}

func langFromName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if i := strings.IndexAny(n, "(,;/"); i > 0 {
		n = strings.TrimSpace(n[:i])
	}
	switch n {
	case "english":
		return "eng"
	case "german", "deutsch":
		return "ger"
	case "french", "français", "francais":
		return "fre"
	case "spanish", "español", "espanol", "castilian":
		return "spa"
	case "italian", "italiano":
		return "ita"
	case "portuguese", "português", "portugues", "brazilian":
		return "por"
	case "dutch", "flemish":
		return "dut"
	case "swedish":
		return "swe"
	case "norwegian":
		return "nor"
	case "danish":
		return "dan"
	case "finnish":
		return "fin"
	case "icelandic":
		return "ice"
	case "polish":
		return "pol"
	case "czech":
		return "cze"
	case "slovak":
		return "slo"
	case "hungarian":
		return "hun"
	case "romanian":
		return "rum"
	case "bulgarian":
		return "bul"
	case "croatian":
		return "hrv"
	case "serbian":
		return "srp"
	case "slovenian":
		return "slv"
	case "greek":
		return "gre"
	case "turkish":
		return "tur"
	case "russian":
		return "rus"
	case "ukrainian":
		return "ukr"
	case "hebrew":
		return "heb"
	case "arabic":
		return "ara"
	case "persian", "farsi":
		return "per"
	case "hindi":
		return "hin"
	case "tamil":
		return "tam"
	case "telugu":
		return "tel"
	case "thai":
		return "tha"
	case "vietnamese":
		return "vie"
	case "indonesian":
		return "ind"
	case "malay":
		return "may"
	case "chinese", "mandarin", "cantonese":
		return "chi"
	case "japanese":
		return "jpn"
	case "korean":
		return "kor"
	}
	return ""
}

// audioLangs returns the sorted distinct normalized audio languages of a version.
func audioLangs(v *models.MediaVersion) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range v.AudioTracks {
		if l := normLang(t.LanguageCode, t.Language); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------------

// naturalLess orders numeric strings numerically and everything else lexically.
func naturalLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		if ai != bi {
			return ai < bi
		}
		return a < b
	}
	if (aerr == nil) != (berr == nil) {
		return aerr == nil
	}
	return a < b
}

type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	uf := &unionFind{parent: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

// union merges the sets of i and j, keeping the smaller root (deterministic).
func (u *unionFind) union(i, j int) {
	ri, rj := u.find(i), u.find(j)
	switch {
	case ri < rj:
		u.parent[rj] = ri
	case rj < ri:
		u.parent[ri] = rj
	}
}

// addFlag appends f to flags unless present.
func addFlag(flags []string, f string) []string {
	for _, x := range flags {
		if x == f {
			return flags
		}
	}
	return append(flags, f)
}

// sortedUnique returns a sorted, de-duplicated copy (never nil).
func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// humanBytes renders a byte count with binary units ("45.2 GiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// msDuration converts milliseconds to a time.Duration.
func msDuration(ms int64) time.Duration { return time.Duration(ms) * time.Millisecond }

// humanDuration renders a duration compactly ("2h 1m", "45m", "30s", "3d 4h").
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int((d%time.Hour)/time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		days, h := int(d/(24*time.Hour)), int((d%(24*time.Hour))/time.Hour)
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
}

// ---------------------------------------------------------------------------
// Full-disc backups (docs/research/disc-structures.md §6, docs/DECISIONS.md D9)
// ---------------------------------------------------------------------------

// discMemberPath returns the first path (server or local) of a regular version that lies inside a
// disc structure (BDMV/STREAM/00800.m2ts, VIDEO_TS/VTS_01_1.VOB …) or is a loose disc clip
// ("…/Elemental (2023)/00174.m2ts": disc.IsDiscPath), "" when there is none or v is a disc
// version. Such a file is never removed on its own: that would corrupt the disc.
func discMemberPath(v *models.MediaVersion) string {
	if v.Disc != nil {
		return ""
	}
	for _, p := range versionPaths(v) {
		if disc.IsDiscPath(p) {
			return p
		}
	}
	return ""
}

// discProtectOnly reports disc kinds that are only ever shown and protected (home video, recorder
// and legacy formats no media server plays).
func discProtectOnly(kind string) bool {
	switch kind {
	case models.DiscBluray, models.DiscUHDBluray, models.DiscDVD, models.DiscISO,
		// A loose clip set is removed only as a whole, like a BDMV folder (docs/DECISIONS.md D9
		// "Loose clip sets"); never one clip.
		models.DiscBlurayClips, models.DiscDVDClips:
		return false
	}
	return true
}

// discLabel names a disc kind for messages ("UHD Blu-ray").
func discLabel(kind string) string {
	switch kind {
	case models.DiscBluray:
		return "Blu-ray"
	case models.DiscUHDBluray:
		return "UHD Blu-ray"
	case models.DiscDVD:
		return "DVD"
	case models.DiscHDDVD:
		return "HD DVD"
	case models.DiscAVCHD:
		return "AVCHD"
	case models.DiscBDAV:
		return "Blu-ray recording (BDAV)"
	case models.DiscISO:
		return "disc image"
	case models.DiscBlurayClips:
		return "Blu-ray clip set (loose .m2ts)"
	case models.DiscDVDClips:
		return "DVD file set (loose VOB)"
	}
	return "disc"
}

// discProtectionReasons explains why a disc version must be kept (empty when a person may approve
// its removal): discs in TV libraries, disc removal switched off, protect-only kinds, a disc that
// could not be verified (unreadable, incomplete, symlinks …), that Dupearr cannot reach (no path
// mapping) or whose files other Plex items expose.
func discProtectionReasons(mt models.MediaType, v *models.MediaVersion, env EvalEnv) []string {
	d := v.Disc
	if d == nil {
		return nil
	}
	var out []string
	switch {
	case mt == models.MediaTypeEpisode:
		out = append(out, "full-disc backup in a TV library (always kept)")
	case !env.AllowDiscRemoval:
		out = append(out, "full-disc backup (disc removal is off — Settings → Media Management → Allow Removing Full Discs)")
	}
	if discProtectOnly(d.Type) {
		out = append(out, discLabel(d.Type)+" backups are never removed")
	} else if env.AllowDiscRemoval && mt != models.MediaTypeEpisode {
		switch {
		case !d.Removable:
			why := strings.TrimSpace(d.Problem)
			if why == "" {
				why = "it could not be verified completely"
			}
			out = append(out, "full-disc backup that cannot be removed safely: "+why)
		case strings.TrimSpace(d.LocalRoot) == "" || len(d.OwnedEntries) == 0:
			out = append(out, "Dupearr cannot reach this disc (no path mapping covers it), so it cannot move it to the recycle bin")
		}
	}
	if len(d.PlexItems) > 0 {
		out = append(out, "other Plex items ("+strings.Join(d.PlexItems, ", ")+") use files of this disc")
	}
	return out
}
