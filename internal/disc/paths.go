package disc

import (
	"crypto/sha1" //nolint:gosec // RootHash is a stable identifier (version keys), not a security boundary.
	"encoding/hex"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Path rules (docs/research/disc-structures.md §6.2). Everything here is pure string work that
// accepts server, *arr and local paths alike: "\" and "/" are both separators and names are
// compared case-insensitively (a disc copied from a Windows or macOS machine keeps whatever
// case the ripper used).

// structureDirs maps the upper-cased folder names that belong to a disc structure ("Tier 1"
// components) to the disc kind they indicate. A path with any of these components lies inside
// a disc. The list mirrors the research's reDiscDir and web/src/components/duplicates/disc.ts.
var structureDirs = map[string]Type{
	"BDMV":        Bluray,
	"AACS":        Bluray,
	"CERTIFICATE": Bluray,
	"MAKEMKV":     Bluray,
	"BDSVM":       Bluray,
	"SLYVM":       Bluray,
	"ANYVM":       Bluray,
	"BDAV":        BDAV,
	"VIDEO_TS":    DVD,
	"HVDVD_TS":    HDDVD,
	"ADV_OBJ":     HDDVD,
	"AVCHD":       AVCHD,
}

// discFileNames are upper-cased marker file names that only exist in a disc structure
// (reDiscFile, together with reFlatDVD).
var discFileNames = map[string]Type{
	"INDEX.BDMV":       Bluray,
	"MOVIEOBJECT.BDMV": Bluray,
	"DISCATT.DAT":      Bluray,
	"INDEX.BDM":        AVCHD,
	"MOVIEOBJ.BDM":     AVCHD,
}

// discExtensions are upper-cased extensions that only exist inside a disc structure
// (reDiscExt). A standalone .m2ts/.mts/.ts/.vob is an ordinary video and is not listed.
var discExtensions = map[string]Type{
	".MPLS": Bluray,
	".CLPI": Bluray,
	".BDMV": Bluray,
	".SSIF": Bluray,
	".BDM":  AVCHD,
	".MPL":  AVCHD,
	".CPI":  AVCHD,
	".IFO":  DVD,
	".BUP":  DVD,
	".EVO":  HDDVD,
}

var (
	// reFlatDVD matches the file names of a DVD-Video title (also used for flat DVDs, whose
	// files sit directly in the movie folder), also with the copy markers a file manager adds to a
	// clashing name when two DVDs are flattened into one folder ("VTS_01_1 (2).VOB": copyMarkers;
	// docs/DECISIONS.md D9 "Loose clip sets").
	reFlatDVD = regexp.MustCompile(`(?i)^(?:VIDEO_TS` + copyMarkers + `\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]` + copyMarkers + `\.(?:IFO|BUP|VOB))$`)
	// reTitleVOB matches the title-set VOBs that carry the video (VTS_NN_1..9.VOB; _0 is the
	// title set's menu).
	reTitleVOB = regexp.MustCompile(`(?i)^VTS_([0-9]{2})_([1-9])\.VOB$`)
	// reSetFolder matches a multi-disc folder: "Disc 1", "CD2", "Movie - Disc 1", "Part 1",
	// "DVD1", "pt 2" (research §6.2: bare "Disc 1" accepted).
	reSetFolder = regexp.MustCompile(`(?i)^(?:.*?[ ._-])?(?:cd|dvd|dis[ck]|part|pt)[ ._-]*([0-9]{1,2})$`)
	// reStackedImage splits a stacked image name (without extension): "Movie (2010) - Disc 1",
	// "Movie.cd2" → prefix + number.
	reStackedImage = regexp.MustCompile(`(?i)^(.*?)[ ._-]*(?:cd|dvd|dis[ck]|part|pt)[ ._-]*([0-9]{1,2})$`)
	// reExtrasFolder matches extras/bonus folders (Plex legacy ignore_dirs, Radarr extras
	// names, Kodi "Bonus Disc"): a disc in there is an extra, never a version.
	reExtrasFolder = regexp.MustCompile(`(?i)^(?:extras?|bonus.*|.*bonus dis[ck].*|special[ ._-]?features|featurettes|behind the scenes|deleted scenes|interviews|scenes|shorts|trailers|other|samples?)$`)
	// reClipName matches the numbered clip names of a Blu-ray/AVCHD STREAM folder ("00800.m2ts",
	// "00001.MTS", "00003.m2t") wherever they lie, including the copies a second disc flattened
	// into the same folder leaves (clipStem: "00004.1.m2ts" as in the real library, and the names
	// a file manager gives a clashing copy — "00800 (1)", "00800 - Copy (2)", "00800 2",
	// "00800 copy", "00800_1"; docs/DECISIONS.md D9 "Loose clip sets"). Four digits ("1917.m2ts"),
	// six or more, a named file ("Movie.m2ts") or a ".ts" never match.
	reClipName = regexp.MustCompile(`(?i)^` + clipStem + `\.(?:m2ts|mts|m2t)$`)
	// reDVDClipName matches the loose DVD-Video files that carry video or the disc's navigation
	// (VTS_01_1.VOB, VIDEO_TS.VOB/IFO/BUP): the "DVD clips".
	reDVDClipName = regexp.MustCompile(`(?i)^(?:VTS_[0-9]{2}_[0-9]` + copyMarkers + `\.VOB|VIDEO_TS` + copyMarkers + `\.(?:VOB|IFO|BUP))$`)
	// reLooseBDMeta matches the Blu-ray files a flattened backup keeps next to its clips and that
	// a loose clip set owns: playlists, clip information, index/MovieObject/id .bdmv files and the
	// numbered 3D interleaved files (.ssif).
	reLooseBDMeta = regexp.MustCompile(`(?i)^(?:[^/\\]+\.(?:mpls|clpi|bdmv)|` + clipStem + `\.ssif)$`)
)

// copyMarkers matches what is appended to a file name when a copy clashes with an existing name:
// any number of markers, each after at least one separator (" ", ".", "_", "-"): a number of up to
// three digits ("00004.1", macOS "00800 2", "00800_1"), one in parentheses (Windows "00800 (2)") or
// "copy" ("00800 - Copy", "00800 copy 2"). It fails closed — a clip renamed by a file manager stays
// a clip — without reaching titles: a year in parentheses ("00800 (2019)"), a fourth digit
// ("00800.1234") or words ("00800 Movie") never match.
const copyMarkers = `(?:[ ._-]+(?:copy|\([0-9]{1,3}\)|[0-9]{1,3}))*`

// clipStem is the name of a numbered STREAM clip without its extension: exactly five digits, then
// copyMarkers.
const clipStem = `[0-9]{5}` + copyMarkers

// IsClipName reports whether a file name (no folder) is a numbered Blu-ray/AVCHD STREAM clip
// name: five digits, any copy markers (copyMarkers: "00004.1", "00800 (2)", "00800 - Copy") and
// .m2ts, .mts or .m2t (any case). Such a
// file is part of a disc wherever it lies — inside BDMV/STREAM or loose in a movie folder, where
// Plex lists every clip as a separate version — and is never removed on its own.
func IsClipName(name string) bool { return reClipName.MatchString(name) }

// IsDVDClipName reports whether a file name is a loose DVD-Video file carrying video or the
// disc's navigation: VTS_NN_N.VOB or VIDEO_TS.VOB/IFO/BUP (any case).
func IsDVDClipName(name string) bool { return reDVDClipName.MatchString(name) }

// IsLooseSetFileName reports whether a file lying directly in a folder next to loose clips
// belongs to that loose clip set: a Blu-ray clip name (IsClipName), a loose Blu-ray metadata file
// (.mpls, .clpi, .bdmv, a numbered .ssif) or a flat DVD file (VIDEO_TS.* / VTS_NN_N.VOB/IFO/BUP).
// A whole-set removal moves exactly these files of the folder, never an .mkv, NFO, artwork or
// subtitle next to them.
func IsLooseSetFileName(name string) bool {
	return reClipName.MatchString(name) || reLooseBDMeta.MatchString(name) || reFlatDVD.MatchString(name)
}

// LooseClipKind returns the kind of loose clip set a path's file belongs to — BlurayClips for a
// clip name (IsClipName), DVDClips for a DVD clip name (IsDVDClipName) — when no folder of the
// path is a disc structure folder (BDMV, VIDEO_TS …: such a file belongs to that structure).
func LooseClipKind(p string) (Type, bool) {
	name := baseName(p)
	var kind Type
	switch {
	case name == "":
		return "", false
	case reClipName.MatchString(name):
		kind = BlurayClips
	case reDVDClipName.MatchString(name):
		kind = DVDClips
	default:
		return "", false
	}
	if inStructure(p) {
		return "", false
	}
	return kind, true
}

// IsLooseClipPath reports whether p names a loose disc clip: a clip-named file (IsClipName or
// IsDVDClipName) that does not lie inside a disc structure folder. It is a disc path
// (IsDiscPath): the per-file guard refuses removing it on its own.
func IsLooseClipPath(p string) bool {
	_, ok := LooseClipKind(p)
	return ok
}

// inStructure reports whether a folder of p (not its last element) is a disc structure folder.
func inStructure(p string) bool {
	n := strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	i := strings.LastIndexByte(n, '/')
	if i < 0 {
		return false
	}
	for _, comp := range strings.Split(n[:i], "/") {
		if comp == "" {
			continue
		}
		if _, found := lookupFold(structureDirs, comp); found {
			return true
		}
	}
	return false
}

// ClipSetHash is the stable identifier of the loose clip set of a folder (the version key
// "disc:<serverID>:<ClipSetHash(folder)>"): the hex SHA-1 of the normalized folder (RootHash's
// normalization) followed by ":clips", so it never collides with a disc structure rooted in the
// same folder. A Windows folder (drive letter or UNC share: case-insensitive) is also case-folded,
// so the two spellings a Windows media server may report for one folder are one set with one key.
func ClipSetHash(folder string) string {
	n := normalizeRoot(folder)
	if strings.HasPrefix(n, "//") || (len(n) >= 2 && n[1] == ':' && isASCIILetter(n[0])) {
		n = strings.ToLower(n)
	}
	sum := sha1.Sum([]byte(n + ":clips")) //nolint:gosec // identifier, not security
	return hex.EncodeToString(sum[:])
}

// lookupFold looks s up in a table with upper-case ASCII keys, case-insensitively with the same
// Unicode simple folding as strings.EqualFold and Go's (?i) regexps (so "K", the Kelvin sign,
// matches "k"): never narrower than the research's regular expressions.
func lookupFold(m map[string]Type, s string) (Type, bool) {
	if t, ok := m[strings.ToUpper(s)]; ok {
		return t, true
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 { // only non-ASCII input can fold onto an ASCII key differently
			for k, t := range m {
				if strings.EqualFold(k, s) {
					return t, true
				}
			}
			break
		}
	}
	return "", false
}

// locate finds the disc structure a path belongs to: its kind and the byte offset in p where
// the disc root ends (the start of the component that begins the structure, or of the file
// name for a loose disc file). The outermost structure component wins, so BDMV/BACKUP and
// STREAM/SSIF never count as nested discs.
func locate(p string) (t Type, rootEnd int, ok bool) {
	n := strings.ReplaceAll(p, `\`, "/") // same length and offsets as p
	prevStart, prevComp := -1, ""
	for start := 0; start <= len(n); {
		end := strings.IndexByte(n[start:], '/')
		if end < 0 {
			end = len(n)
		} else {
			end += start
		}
		if comp := n[start:end]; comp != "" {
			if kind, found := lookupFold(structureDirs, comp); found {
				rs := start
				// PRIVATE/AVCHD (camcorder card layout): the root is the folder holding PRIVATE.
				if kind == AVCHD && prevStart >= 0 && strings.EqualFold(prevComp, "PRIVATE") {
					rs = prevStart
				}
				return kind, rs, true
			}
			prevStart, prevComp = start, comp
		}
		if end == len(n) {
			break
		}
		start = end + 1
	}
	if prevStart < 0 {
		return "", 0, false
	}
	name := prevComp // the last non-empty component: a loose disc file?
	if kind, found := lookupFold(discFileNames, name); found {
		return kind, prevStart, true
	}
	if reFlatDVD.MatchString(name) {
		return DVD, prevStart, true
	}
	if kind, found := lookupFold(discExtensions, path.Ext(name)); found {
		return kind, prevStart, true
	}
	if reClipName.MatchString(name) {
		// A numbered STREAM clip outside BDMV/: a flattened Blu-ray backup (loose clip set).
		return BlurayClips, prevStart, true
	}
	return "", 0, false
}

// IsDiscPath reports whether p (any separators, any case) lies inside a disc structure: a
// component BDMV, BDAV, VIDEO_TS, HVDVD_TS, AVCHD, AACS, CERTIFICATE, MAKEMKV, BDSVM, SLYVM,
// ANYVM or ADV_OBJ (the path may also name that folder itself), a disc marker or flat-DVD file
// name (index.bdmv, VIDEO_TS.IFO, VTS_01_1.VOB …), a disc-only extension (.mpls .clpi
// .bdmv .bdm .mpl .cpi .ssif .ifo .bup .evo) or a numbered STREAM clip name wherever it lies
// ("00800.m2ts", "00004.1.m2ts", "00001.MTS": IsClipName — the loose clips of a flattened
// Blu-ray backup, docs/DECISIONS.md D9 "Loose clip sets").
//
// This is the fail-closed per-file guard: removing such a path on its own (through Plex, an
// *arr or the filesystem) would corrupt a disc, so every method must refuse it unless the
// removal is the whole-disc removal of a disc version. A standalone "Movie.m2ts" or "Movie.ts"
// is not a disc path; neither is an .iso/.img image (one file; see IsImagePath). It is
// conservative by design: a movie folder literally named "Certificate" is treated as a disc
// path, and so is a lone "00800.m2ts" copied out of a disc.
func IsDiscPath(p string) bool {
	_, _, ok := locate(p)
	return ok
}

// RootOf returns the disc root of a path inside a disc structure, the kind the path indicates
// and ok=false when IsDiscPath(p) is false. The root keeps p's separators and case:
//
//	/movies/M (2010)/BDMV/STREAM/00800.m2ts  → /movies/M (2010), Bluray
//	D:\Movies\M\Disc 2\VIDEO_TS\VTS_01_1.VOB → D:\Movies\M\Disc 2, DVD
//	/movies/M/VTS_01_1.VOB (flat DVD)         → /movies/M, DVD
//	/cam/PRIVATE/AVCHD/BDMV/STREAM/00001.MTS  → /cam, AVCHD
//	BDMV/STREAM/00800.m2ts (a relative path)  → "", Bluray (the base it is relative to)
//	/BDMV/index.bdmv                          → /, Bluray
//	/movies/M (2010)/00800.m2ts (loose clip)  → /movies/M (2010), BlurayClips
//
// The kind comes from the path alone: a BDMV path reports Bluray even for a UHD disc, and a
// loose disc file (e.g. an .mpls outside any BDMV, or a loose clip) reports its containing folder.
func RootOf(p string) (root string, t Type, ok bool) {
	t, end, ok := locate(p)
	if !ok {
		return "", "", false
	}
	return rootBefore(p, end), t, true
}

// rootBefore returns p[:end] without its trailing separators, keeping a lone "/" or "\" and a
// drive root "C:\".
func rootBefore(p string, end int) string {
	if end <= 0 {
		return ""
	}
	r := p[:end]
	t := strings.TrimRight(r, `/\`)
	switch {
	case t == "":
		return r[:1]
	case len(t) == 2 && t[1] == ':' && isASCIILetter(t[0]) && len(r) > 2:
		return r[:3]
	}
	return t
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// baseName is the last non-empty element of p, with either separator.
func baseName(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// IsImagePath reports whether p names a disc image file (.iso or .img, any case). An image is
// one file: it is a disc version but not a "disc path" (IsDiscPath).
func IsImagePath(p string) bool {
	ext := strings.ToUpper(path.Ext(baseName(p)))
	return ext == ".ISO" || ext == ".IMG"
}

// ownedNames lists the upper-cased entry names a disc owns at its root besides its structure
// folders (research §6.7.3), by family.
var (
	bdCompanions  = []string{"CERTIFICATE", "AACS", "MAKEMKV", "BDSVM", "SLYVM", "ANYVM", "SNP", "FILMINDEX.XML", "DISCATT.DAT"}
	dvdCompanions = []string{"AUDIO_TS", "JACKET_P"}
	// HD DVD owns ADV_OBJ and AACS next to HVDVD_TS (see detect.go classify).
)

func containsFold(list []string, name string) bool {
	for _, s := range list {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// IsDiscEntryName reports whether an entry with this name, directly inside a folder, belongs
// to a disc structure rooted in that folder: a structure folder (BDMV, VIDEO_TS, HVDVD_TS,
// AVCHD, BDAV …), a disc companion (CERTIFICATE, AACS, MAKEMKV, AUDIO_TS, JACKET_P, SNP,
// FilmIndex.xml, discatt.dat …), a flat-DVD file (VIDEO_TS.IFO, VTS_01_1.VOB …), a file of a
// loose clip set (IsLooseSetFileName: "00800.m2ts", "00800.mpls", "index.bdmv" …) or a macOS
// .dvdmedia bundle. Such an entry is never an independent version. Disc images are separate
// (IsImagePath).
func IsDiscEntryName(name string) bool {
	if name == "" {
		return false
	}
	if _, ok := lookupFold(structureDirs, name); ok {
		return true
	}
	if containsFold(bdCompanions, name) || containsFold(dvdCompanions, name) {
		return true
	}
	return reFlatDVD.MatchString(name) || IsLooseSetFileName(name) || isDVDMedia(name)
}

// HintsDisc reports whether an entry with this name in a folder is a reason to run Detect on
// that folder: IsDiscEntryName, a disc image, or a multi-disc folder name ("Disc 1", "CD2").
// It is a cheap pre-filter for folder inventories; Detect makes the decision.
func HintsDisc(name string) bool {
	if IsDiscEntryName(name) || IsImagePath(name) {
		return true
	}
	_, ok := IsSetFolderName(name)
	return ok
}

func isDVDMedia(name string) bool {
	return len(name) > len(".dvdmedia") && strings.EqualFold(name[len(name)-len(".dvdmedia"):], ".dvdmedia")
}

// IsSetFolderName reports whether name is a multi-disc folder name ("Disc 1", "CD2",
// "Movie - Disc 1", "Part 1", "DVD1") and returns its number. Extras folders ("Bonus Disc 1")
// are never set folders.
func IsSetFolderName(name string) (n int, ok bool) {
	if IsExtrasFolderName(name) {
		return 0, false
	}
	m := reSetFolder.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// reExtrasWord finds an extras keyword anywhere in a name, as a whole word: "Special Features - Disc
// 2", "Extras Disc 2", "Deleted Scenes Disc 2", "Heat - Bonus" (GAP-02). Unlike reExtrasFolder it
// can match a title ("The Interview"), so callers only apply it where a title is not expected or has
// been removed (set folder names, image names after the title).
var reExtrasWord = regexp.MustCompile(`(?i)(?:^|[^\pL\pN])(?:extras?|bonus|special[ ._-]*features?|featurettes?|behind[ ._-]*the[ ._-]*scenes|deleted[ ._-]*scenes|interviews?|trailers?|samples?)(?:$|[^\pL\pN])`)

// HasExtrasWord reports whether s holds an extras keyword as a whole word (extra(s), bonus,
// special features, featurette(s), behind the scenes, deleted scenes, interview(s), trailer(s),
// sample(s)).
func HasExtrasWord(s string) bool { return reExtrasWord.MatchString(s) }

// SetFolderPrefix returns what a multi-disc folder name (or the name of a .dvdmedia bundle) puts
// before its disc number, normalised to lower-case words separated by single spaces: "Movie -
// Disc 1" and "movie.disc.2" → "movie", "Disc 1" → "". ok is false when name is not a set folder
// name (IsSetFolderName). The members of one set share their prefix (GAP-02): a set mixing "Disc
// 1" with "Special Features - Disc 2", or "Heat Part 1" with "Ronin Part 2", is unclear.
func SetFolderPrefix(name string) (prefix string, ok bool) {
	if isDVDMedia(name) {
		name = name[:len(name)-len(".dvdmedia")]
	}
	if _, ok := IsSetFolderName(name); !ok {
		return "", false
	}
	m := reStackedImage.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	words := strings.FieldsFunc(strings.ToLower(m[1]), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.Join(words, " "), true
}

// IsExtrasFolderName reports whether name is an extras/bonus folder ("Extras", "Bonus Disc",
// "Featurettes", "Behind the Scenes", "Special Features" …). A disc inside one is an extra:
// never a version and never removed together with the feature.
func IsExtrasFolderName(name string) bool { return reExtrasFolder.MatchString(name) }

// stackedImage splits an image file name into its stack prefix (lower-cased, trimmed) and
// number: "Movie (2010) - Disc 1.iso" → "movie (2010)", 1.
func stackedImage(name string) (prefix string, n int, ok bool) {
	stem := strings.TrimSuffix(name, path.Ext(name))
	m := reStackedImage.FindStringSubmatch(stem)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return strings.ToLower(strings.TrimSpace(m[1])), n, true
}

// DetectDir returns the folder to pass to Detect for a disc root (e.g. from RootOf mapped to a
// local path): the parent for an image file, a "Disc N" set folder or a .dvdmedia bundle (so
// the whole set is found) and for an extras folder ("Bonus Disc", "Extras" …: Detect on the
// movie folder then correctly reports no version for that extras disc), otherwise the root
// itself. It works from the name alone: a movie folder that is itself named like a set folder
// ("Deathly Hallows Part 1", no year) would lead to its parent, so callers that know the media
// item's folder should pass that folder to Detect directly and use DetectDir only for roots
// derived from file paths (RootOf).
func DetectDir(root string) string {
	if root == "" {
		return ""
	}
	c := filepath.Clean(root)
	base := filepath.Base(c)
	if IsImagePath(c) || isDVDMedia(base) || IsExtrasFolderName(base) {
		return filepath.Dir(c)
	}
	if _, ok := IsSetFolderName(base); ok {
		return filepath.Dir(c)
	}
	return c
}

// RootHash is the hex SHA-1 of a normalized disc root, for stable version keys
// ("disc:<serverID>:<RootHash(localRoot)>"). Normalization: "\" → "/", path.Clean (UNC
// "//host/share" kept), no trailing separator, Windows drive letter lower-cased; the rest of
// the path keeps its case.
func RootHash(root string) string {
	sum := sha1.Sum([]byte(normalizeRoot(root))) //nolint:gosec // identifier, not security
	return hex.EncodeToString(sum[:])
}

func normalizeRoot(p string) string {
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
