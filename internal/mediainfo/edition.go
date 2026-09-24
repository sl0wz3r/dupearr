package mediainfo

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxEditionLen caps display editions taken verbatim from Plex / *arr / {edition-…} tags.
const maxEditionLen = 100

// apostrophes normalizes typographic apostrophes to ASCII.
var apostrophes = strings.NewReplacer("’", "'", "‘", "'", "`", "'", "´", "'")

// reEditionTag matches Plex's naming tag "{edition-Director's Cut}" (file or folder name).
var reEditionTag = regexp.MustCompile(`(?i)\{edition-([^{}]+)\}`)

// editionPattern is one recognized edition token (applied to lower-cased text).
type editionPattern struct {
	re      *regexp.Regexp
	display func(sub []string) string
}

func staticEdition(s string) func([]string) string { return func([]string) string { return s } }

// editionToken wraps alt in token boundaries; group 1 is the whole token.
func editionToken(alt string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^a-z0-9])(` + alt + `)(?:$|[^a-z0-9])`)
}

// editionPatterns follow Radarr's EditionRegex (Parser.cs) restricted to the editions Dupearr
// distinguishes. Each is matched on its own so adjacent tokens ("Extended.Directors.Cut") are
// all found.
var editionPatterns = []editionPattern{
	{editionToken(`director'?s?[ ._-]*(?:cut|edition|version)`), staticEdition("Director's Cut")},
	{editionToken(`collector'?s?[ ._-]*(?:edition|cut|version)`), staticEdition("Collector's Edition")},
	{editionToken(`extended(?:[ ._-]*(?:cut|edition|version))?`), staticEdition("Extended")},
	{editionToken(`theatrical(?:[ ._-]*(?:cut|edition|version|release))?`), staticEdition("Theatrical")},
	{editionToken(`unrated`), staticEdition("Unrated")},
	{editionToken(`uncut`), staticEdition("Uncut")},
	{editionToken(`uncensored`), staticEdition("Uncensored")},
	{editionToken(`imax(?:[ ._-]*(?:edition|version|enhanced))?`), staticEdition("IMAX")},
	{editionToken(`remastered`), staticEdition("Remastered")},
	{editionToken(`criterion(?:[ ._-]*(?:collection|edition))?`), staticEdition("Criterion")},
	{editionToken(`special[ ._-]*(?:edition|cut|version)`), staticEdition("Special Edition")},
	{editionToken(`ultimate(?:[ ._-]*(cut|edition|version))?`), func(sub []string) string {
		if len(sub) > 2 && sub[2] == "cut" {
			return "Ultimate Cut"
		}
		return "Ultimate Edition"
	}},
	{editionToken(`final[ ._-]*cut`), staticEdition("Final Cut")},
	{editionToken(`([0-9]{1,3})(?:st|nd|rd|th)?[ ._-]*anniversary(?:[ ._-]*(?:edition|cut|version))?`), func(sub []string) string {
		if len(sub) > 2 && sub[2] != "" {
			if n, err := strconv.Atoi(sub[2]); err == nil && n > 0 {
				return ordinal(n) + " Anniversary Edition"
			}
		}
		return "Anniversary Edition"
	}},
	{editionToken(`anniversary[ ._-]*edition`), staticEdition("Anniversary Edition")},
	{editionToken(`redux`), staticEdition("Redux")},
	{editionToken(`open[ ._-]?matte`), staticEdition("Open Matte")},
}

// Edition returns the normalized display edition, "" = none.
//
// Priority: Plex editionTitle > *arr edition > "{edition-X}" tag in the file name, then the
// folder name > edition tokens of the file and folder names (Director's Cut, Extended,
// Theatrical, Unrated, Uncut, Uncensored, IMAX, Remastered, Criterion, Special Edition,
// Ultimate, Final Cut, Collector's Edition, [NNth] Anniversary Edition, Redux, Open Matte).
// Tokens are joined in order of appearance, file name first ("Extended Director's Cut").
// "Theatrical" is returned as such: compare editions with EditionKey, which treats Theatrical as
// no edition.
//
// Without the item title, a token counts when it follows the release year / episode marker, or
// when it sits in the title part of the file (or folder) name but not in the other name
// ("Apocalypse Now Redux (1979).mkv" inside "Apocalypse Now (1979)/" is Redux), so a title such
// as "The Final Cut (2004)" in "The Final Cut (2004)/" is not an edition. Prefer
// EditionWithTitles when the title is known. Errors lean towards reporting an edition: a false
// edition only keeps two copies apart, a missed one could merge (and delete) a different cut.
func Edition(p, plexEditionTitle, arrEdition string) string {
	return EditionWithTitles(p, plexEditionTitle, arrEdition)
}

// EditionWithTitles is Edition for an item whose title(s) are known: the Plex or *arr title and,
// for episodes, the episode and show titles. The titles' own words are removed from the file and
// folder names (case, punctuation and separators ignored) and every remaining edition token
// counts wherever it is: "Blade Runner Director's Cut (1982).mkv" of "Blade Runner" is Director's
// Cut and "Apocalypse Now Redux (1979)/" of "Apocalypse Now" is Redux, while "The Final Cut
// (2004).mkv" of "The Final Cut" or the episode "S01E05 - The Director's Cut" have no edition.
// With no non-empty title it behaves exactly like Edition.
func EditionWithTitles(p, plexEditionTitle, arrEdition string, titles ...string) string {
	if e := cleanEdition(plexEditionTitle); e != "" {
		return e
	}
	if e := cleanEdition(arrEdition); e != "" {
		return e
	}
	base, parent := baseAndParent(p)
	for _, name := range []string{base, parent} {
		if e := editionTag(name); e != "" {
			return e
		}
	}
	var norm []string
	for _, t := range titles {
		if n := normWords(t); n != "" {
			norm = append(norm, n)
		}
	}
	if len(norm) > 0 {
		return editionOutsideTitles(base, parent, norm)
	}
	return editionFromNames(base, parent)
}

// cleanEdition trims and collapses whitespace, strips wrapping brackets/quotes and caps the length.
func cleanEdition(s string) string {
	s = strings.Join(strings.Fields(apostrophes.Replace(s)), " ")
	s = strings.TrimSpace(strings.Trim(s, `"[](){}<>`))
	if utf8.RuneCountInString(s) > maxEditionLen {
		s = strings.TrimSpace(string([]rune(s)[:maxEditionLen]))
	}
	return s
}

func editionTag(name string) string {
	m := reEditionTag.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	tag := m[1]
	if !strings.Contains(tag, " ") { // scene-style "{edition-Directors.Cut}"
		tag = strings.NewReplacer(".", " ", "_", " ").Replace(tag)
	}
	return cleanEdition(tag)
}

// displaySet accumulates display editions in order, each once.
type displaySet struct {
	parts []string
	seen  map[string]bool
}

func (d *displaySet) add(ds ...string) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	for _, x := range ds {
		if !d.seen[x] {
			d.seen[x] = true
			d.parts = append(d.parts, x)
		}
	}
}

func (d *displaySet) String() string { return strings.Join(d.parts, " ") }

// editionOutsideTitles returns the edition tokens of the file and folder names once every
// occurrence of the (normalized) titles has been removed from them.
func editionOutsideTitles(base, parent string, normTitles []string) string {
	var out displaySet
	for _, name := range []string{base, parent} {
		out.add(editionDisplays(stripWords(normWords(name), normTitles))...)
	}
	return out.String()
}

// editionFromNames combines the edition tokens of a file name and its folder name when the
// title is unknown (see Edition for which tokens count), file first, each display once.
func editionFromNames(base, parent string) string {
	var out displaySet
	for _, n := range [][2]string{{base, parent}, {parent, base}} {
		name, other := n[0], n[1]
		if strings.TrimSpace(name) == "" {
			continue
		}
		titlePart, rest, ok := splitAtReleaseMarker(name)
		if !ok { // no year/episode marker: the whole name is searched
			out.add(editionDisplays(name)...)
			continue
		}
		// A token before the year is part of the title unless the other name (folder vs file)
		// lacks it.
		var otherHits map[string]bool
		for _, d := range editionDisplays(titlePart) {
			if otherHits == nil {
				otherHits = map[string]bool{}
				for _, x := range editionDisplays(other) {
					otherHits[x] = true
				}
			}
			if !otherHits[d] {
				out.add(d)
			}
		}
		out.add(editionDisplays(rest)...)
	}
	return out.String()
}

// normWords lower-cases s, drops apostrophes and turns every other non-letter/digit into a single
// space: "Director’s.Cut" → "directors cut", "Star Wars: Episode IV" → "star wars episode iv".
func normWords(s string) string {
	s = strings.ReplaceAll(strings.ToLower(apostrophes.Replace(s)), "'", "")
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// stripWords removes every whole-word occurrence of each phrase from the normalized text.
func stripWords(text string, phrases []string) string {
	t := " " + text + " "
	for _, p := range phrases {
		needle := " " + p + " "
		for strings.Contains(t, needle) {
			t = strings.ReplaceAll(t, needle, " ")
		}
	}
	return strings.TrimSpace(t)
}

// editionDisplays returns the display editions of every edition token in text, in order of
// appearance (overlapping shorter tokens and repeated displays dropped).
func editionDisplays(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	s := strings.ToLower(apostrophes.Replace(text))
	type hit struct {
		start, end int
		display    string
	}
	var hits []hit
	for _, pat := range editionPatterns {
		idx := pat.re.FindStringSubmatchIndex(s)
		if idx == nil {
			continue
		}
		sub := make([]string, len(idx)/2)
		for i := range sub {
			if idx[2*i] >= 0 {
				sub[i] = s[idx[2*i]:idx[2*i+1]]
			}
		}
		hits = append(hits, hit{start: idx[2], end: idx[3], display: pat.display(sub)})
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].start != hits[j].start {
			return hits[i].start < hits[j].start
		}
		return hits[i].end > hits[j].end // longest first at the same position
	})
	var parts []string
	seen := map[string]bool{}
	lastEnd := -1
	for _, h := range hits {
		if h.start < lastEnd || seen[h.display] {
			continue // overlaps a longer token already taken, or duplicate
		}
		seen[h.display] = true
		parts = append(parts, h.display)
		lastEnd = h.end
	}
	return parts
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// editionFiller words do not distinguish editions ("Extended Cut" == "Extended Edition").
var editionFiller = map[string]bool{
	"edition": true, "cut": true, "version": true, "the": true, "collection": true,
}

var reOrdinalNumber = regexp.MustCompile(`^([0-9]+)(?:st|nd|rd|th)$`)

// EditionKey returns a lowercase slug for grouping, "" = none.
//
// Punctuation and apostrophes are dropped, filler words (edition, cut, version, the,
// collection) removed, "director"/"collector" pluralized and ordinals reduced to their number,
// so "Director's Cut" == "Directors.Cut" == "Director's Edition" → "directors",
// "Extended Cut" == "Extended Edition" → "extended", "25th Anniversary Edition" →
// "25-anniversary". "Theatrical" (and "Original Theatrical …") normalizes to "" so a theatrical
// cut groups with a copy that has no edition. Unknown editions keep their words, so two
// different unknown editions never collide.
func EditionKey(edition string) string {
	s := strings.ToLower(apostrophes.Replace(edition))
	s = strings.ReplaceAll(s, "'", "")
	fields := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	toks := make([]string, 0, len(fields))
	for _, f := range fields {
		if editionFiller[f] {
			continue
		}
		switch f {
		case "director":
			f = "directors"
		case "collector":
			f = "collectors"
		}
		if m := reOrdinalNumber.FindStringSubmatch(f); m != nil {
			f = m[1]
		}
		toks = append(toks, f)
	}
	if len(toks) == 0 && len(fields) > 0 {
		// Only filler words ("Cut", "The Collection"): still a named edition, never the base cut.
		toks = fields
	}
	key := strings.Join(toks, "-")
	switch key {
	case "theatrical", "original-theatrical", "theatrical-release", "original-theatrical-release", "standard":
		return ""
	}
	if key == "" {
		// No letters or digits at all (e.g. only symbols): keep the edition distinct from "none".
		key = strings.Join(strings.Fields(s), "-")
	}
	return key
}

// ---------------------------------------------------------------------------
// Variants: 3D and samples
// ---------------------------------------------------------------------------

var (
	// Unambiguous TRaSH "3D" custom-format tokens, matched anywhere: BluRay3D, BD3D, SBS,
	// Half-SBS, HSBS, Full-SBS, Half-OU.
	re3DAnywhere = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:blu[ ._-]?ray[ ._-]?3d|bd3d|3dbd|sbs|[hf][ ._-]?sbs|h[ ._-]?tab|(?:half|full)[ ._-]?(?:sbs|ou|tab))(?:$|[^a-z0-9])`)
	// "3D", "HOU" and "FOU" also occur in titles ("Step Up 3D (2010)", "Pierrot le Fou"): they
	// only count after the release year, or anywhere in a name that has no year at all.
	re3DTitleSafe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:3d|[hf][ ._-]?ou)(?:$|[^a-z0-9])`)
	reSample      = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])sample(?:$|[^a-z0-9])`)
)

// Is3D reports whether a file (or its folder) is a 3D release, using the TRaSH Guides "3D"
// custom-format tokens. BluRay3D, BD3D, SBS, HSBS/FSBS, HTAB and Half-/Full-SBS/OU/TAB match
// anywhere; "3D", "HOU" and "FOU" match after the release year (so "Step Up 3D (2010)" is not
// 3D), anywhere in a name without a year ("Avatar.3D.mkv" inside "Avatar (2009)/"), or before
// the year when the folder name lacks them ("Avatar 3D (2009).mkv" inside "Avatar (2009)/").
// Matching is token-boundary aware: "3DO", "2013D" or "x3d" do not match. The grouping engine
// uses this function, so its "#3d" group split and this detection never disagree.
//
// Errors lean towards "3D": a false positive only keeps two copies apart (no deletion), while a
// false negative could let a 3D copy be removed as a duplicate of a 2D one.
func Is3D(p string) bool {
	base, parent := baseAndParent(p)
	for _, name := range []string{base, parent} {
		if name == "" {
			continue
		}
		if re3DAnywhere.MatchString(name) || re3DTitleSafe.MatchString(afterTitle(name)) {
			return true
		}
	}
	// "3D" in the title part of the file but not in its folder name (or the other way round) is
	// a variant marker, not part of the title: "Avatar 3D (2009).mkv" inside "Avatar (2009)/".
	if parent != "" {
		bt, _, bok := splitAtReleaseMarker(base)
		pt, _, pok := splitAtReleaseMarker(parent)
		if (bok && re3DTitleSafe.MatchString(bt) && !re3DTitleSafe.MatchString(parent)) ||
			(pok && re3DTitleSafe.MatchString(pt) && !re3DTitleSafe.MatchString(base)) {
			return true
		}
	}
	return false
}

// IsSamplePath reports whether a path looks like a release sample: the file sits in a
// "Sample"/"Samples" folder, or its name contains the token "sample" (after the release year
// when there is one, so a film titled "Sample" is not flagged).
func IsSamplePath(p string) bool {
	base, parent := baseAndParent(p)
	if base == "" {
		return false
	}
	switch strings.ToLower(parent) {
	case "sample", "samples":
		return true
	}
	return reSample.MatchString(afterTitle(base))
}
