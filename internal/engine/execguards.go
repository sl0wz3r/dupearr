package engine

import (
	"fmt"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Checks the executor repeats right before acting on an approved group: what was true when the
// group was approved (no exclusion covered it, auto mode could take it) may have changed since.

// autoBlockingFlags keep a group out of automatic approval: automation only acts on unambiguous
// groups (docs/DECISIONS.md D4); these need a person's look even when removals are proposed.
var autoBlockingFlags = map[string]bool{
	models.FlagSuspectMerge:       true,
	models.FlagSameFile:           true,
	models.FlagUnanalyzed:         true,
	models.FlagDurationMismatch:   true,
	models.FlagMissingKeeperFile:  true,
	models.FlagUnavailableVersion: true,
	models.FlagSample:             true,
	models.FlagArrUntrackedKeeper: true,
	models.FlagMultiEpisode:       true,
	models.FlagStacked:            true,
	models.FlagMinAge:             true,
	models.FlagArrQueueBusy:       true,
	models.FlagPlaying:            true,
	// docs/DECISIONS.md D9: disc removals are manual-approval only.
	models.FlagFullDisc:       true,
	models.FlagDiscUnreadable: true,
	models.FlagDiscTracked:    true,
}

// BlocksAutoApproval reports whether a group flag keeps the group out of automatic approval (a
// person may still approve it).
func BlocksAutoApproval(flag string) bool { return autoBlockingFlags[flag] }

// ExclusionReason reports why the exclusions cover a stored group — its key (with or without the
// "@plex:<server>:<ratingKey>" disambiguation BuildGroups appends to colliding keys, on either
// side), its title or show title (title_regex), or any version's library, item title or path
// (path_prefix, same rules as BuildGroups). "" when none applies. An exclusion created after a
// group was approved must stop its removals right away, not only once a full scan rebuilt it.
func ExclusionReason(ex []models.Exclusion, g *models.DuplicateGroup) string {
	if g == nil || len(ex) == 0 {
		return ""
	}
	set := compileExclusions(ex)
	for k := range set.groupKeys {
		for _, a := range keyForms(k) {
			for _, b := range keyForms(g.Key) {
				if a == b {
					return fmt.Sprintf("the exclusion of group %q covers it", k)
				}
			}
		}
	}
	for _, id := range g.LibraryIDs {
		if set.libraries[id] {
			return fmt.Sprintf("the exclusion of library %d covers it", id)
		}
	}
	for _, re := range set.titles {
		for _, t := range []string{g.Title, g.ShowTitle} {
			if t != "" && re.MatchString(t) {
				return fmt.Sprintf("the title exclusion %q covers %q", re.String()[len("(?i)"):], t)
			}
		}
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		if set.versionExcluded(v) {
			if set.libraries[v.LibraryID] {
				return fmt.Sprintf("the exclusion of library %d covers %s", v.LibraryID, primaryPath(v))
			}
			return fmt.Sprintf("a path exclusion covers %s", primaryPath(v))
		}
		for _, re := range set.titles {
			if v.ItemTitle != "" && re.MatchString(v.ItemTitle) {
				return fmt.Sprintf("the title exclusion %q covers %q", re.String()[len("(?i)"):], v.ItemTitle)
			}
		}
	}
	return ""
}

// keyForms returns a group key and, when it carries the "@plex:<server>:<ratingKey>"
// disambiguation, the key without it (variant suffixes kept).
func keyForms(key string) []string {
	key = strings.TrimSpace(key)
	i := strings.Index(key, "@plex:")
	if i < 0 {
		return []string{key}
	}
	rest := key[i+len("@plex:"):]
	j := strings.IndexAny(rest, "#~")
	if j < 0 {
		return []string{key, key[:i]}
	}
	return []string{key, key[:i] + rest[j:]}
}

// ProtectionReason reports the first protection of profile p that applies to v (path_glob,
// library, arr_instance, arr_tag — exactly the rules Evaluate applies), "" when none. A protection
// added after a group was approved must stop its removal right away, before the background
// re-evaluation of every group reaches it.
func ProtectionReason(p models.Profile, v *models.MediaVersion, libs map[int64]models.Library) string {
	if v == nil {
		return ""
	}
	for _, pr := range p.Protections {
		if r, ok := matchProtection(pr, v, libs); ok {
			return r
		}
	}
	return ""
}
