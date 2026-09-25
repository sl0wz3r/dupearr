package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Several Plex servers (docs/DECISIONS.md D11, docs/research/multi-server.md §5.4.3). The scanner
// records, per version, the items of other servers that list its file (OtherServers). A version
// whose file another server's item lists as the same file is never removed unless that item keeps
// another version that could be proven to be a different file from every version this group
// removes, and that is not (or may not be) the file of any of them: removing it would otherwise
// leave that server's item without a copy. Proof itself happens at run time (the executor opens
// the files); here the scan's evidence only rules out what can never be proven. With one server
// OtherServers is nil and nothing here changes a decision.

// survivor reports whether the other item of e keeps a version that could be proven different
// from every version in removed and is not (or may not be) the file of any of them.
func survivor(e *models.OtherListing, removed map[string]bool) bool {
	for i := range e.Others {
		o := &e.Others[i]
		ok := true
		for r := range removed {
			if containsString(o.Same, r) || !containsString(o.Distinct, r) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// removedKeys returns the version keys keep does not keep.
func (ev *evaluation) removedKeys(keep []bool) map[string]bool {
	out := map[string]bool{}
	for i, v := range ev.vs {
		if !keep[i] {
			out[v.Key] = true
		}
	}
	return out
}

// protectOtherServers keeps (and protects) every would-be-removed version with a same-file
// listing on another server whose item keeps no survivor (see survivor). Keeping a version can
// only make the removal set smaller, so callers repeat it (with protectSameFile) until nothing
// changes; it reports whether it kept anything. After the user's overrides (afterOverrides) an
// override that asked for the removal is ignored with a note.
func (ev *evaluation) protectOtherServers(keep []bool, afterOverrides bool) bool {
	changed := false
	removed := ev.removedKeys(keep)
	for i, v := range ev.vs {
		if keep[i] || len(v.OtherServers) == 0 {
			continue
		}
		for k := range v.OtherServers {
			e := &v.OtherServers[k]
			if e.Match != models.OtherSameFile || survivor(e, removed) {
				continue
			}
			if afterOverrides && strings.EqualFold(strings.TrimSpace(string(ev.g.Files[i].Override)), string(models.DecisionRemove)) {
				ev.overrideApplied[i] = false
				ev.note(i, fmt.Sprintf("Override ignored — removing it would leave %s without a copy", serverLabel(e)))
			}
			keep[i] = true
			ev.addProtection(i, onlyCopyReason(e))
			changed = true
			break
		}
	}
	return changed
}

// serverLabel names the other server of a listing.
func serverLabel(e *models.OtherListing) string {
	if s := strings.TrimSpace(e.ServerName); s != "" {
		return s
	}
	return fmt.Sprintf("media server #%d", e.ServerID)
}

// onlyCopyReason is the protection reason of a version another server's item needs.
func onlyCopyReason(e *models.OtherListing) string {
	title := strings.TrimSpace(e.ItemTitle)
	if title == "" {
		title = "item " + e.RatingKey
	}
	lib := strings.TrimSpace(e.LibraryTitle)
	if lib == "" {
		lib = fmt.Sprintf("library #%d", e.LibraryID)
	}
	r := fmt.Sprintf("the only copy of %q on %s (%s)", title, serverLabel(e), lib)
	if len(e.Others) > 0 {
		r += ": its other versions cannot be proven a different file"
	}
	if h := strings.TrimSpace(e.Hint); h != "" {
		r += " — " + h
	}
	return r
}

// crossServerFlags derives the engine-owned cross-server flags and fills ItemKeepsAnother on the
// listings of the versions this group removes (nil on kept versions).
func (ev *evaluation) crossServerFlags(flags []string, removals int) []string {
	removed := ev.removedKeys(ev.keep)
	for i, v := range ev.vs {
		for k := range v.OtherServers {
			e := &v.OtherServers[k]
			if ev.keep[i] {
				e.ItemKeepsAnother = nil
				continue
			}
			keeps := survivor(e, removed)
			e.ItemKeepsAnother = &keeps
			switch e.Match {
			case models.OtherSameFile:
				flags = addFlag(flags, models.FlagOtherServerListing)
			case models.OtherPossiblySame:
				flags = addFlag(flags, models.FlagOtherServerPossible)
			}
			if e.KeptByGroup != nil && *e.KeptByGroup {
				flags = addFlag(flags, models.FlagOtherServerKeeps)
			}
		}
	}
	if cs := ev.g.CrossServer; cs != nil && !cs.Complete && removals > 0 {
		flags = addFlag(flags, models.FlagOtherServerUnread)
	}
	return flags
}

// crossServerReview lists the review reasons of the cross-server flags.
func (ev *evaluation) crossServerReview() []string {
	g := ev.g
	var out []string
	if g.HasFlag(models.FlagOtherServerPossible) {
		if e := ev.firstRemovedListing(models.OtherPossiblySame, false); e != nil {
			out = append(out, fmt.Sprintf("%s lists a file with the same name and size (%s in %s); if it is the same file, removing it takes %s's copy",
				serverLabel(e), e.Path, e.LibraryTitle, serverLabel(e)))
		} else {
			out = append(out, "another media server lists a file with the same name and size; if it is the same file, removing it takes that server's copy")
		}
	}
	if g.HasFlag(models.FlagOtherServerKeeps) {
		if e := ev.firstRemovedListing("", true); e != nil {
			out = append(out, fmt.Sprintf("a duplicate group of %s keeps this file (%s in %s); change one of the two decisions",
				serverLabel(e), e.Path, e.LibraryTitle))
		} else {
			out = append(out, "a duplicate group of another media server keeps this file; change one of the two decisions")
		}
	}
	if g.HasFlag(models.FlagOtherServerUnread) {
		var names []string
		if cs := g.CrossServer; cs != nil {
			for _, s := range cs.Servers {
				if s.Unread != "" {
					names = append(names, s.Unread)
				}
			}
		}
		sort.Strings(names)
		msg := "a media server that may list these files could not be read"
		if len(names) > 0 {
			msg += " (" + strings.Join(names, "; ") + ")"
		}
		out = append(out, msg+"; make it reachable, disable it or declare it separate storage, then re-scan")
	}
	return out
}

// firstRemovedListing returns the first listing of a removed version with the given match (any
// when ""), or kept by another server's group (keptByGroup).
func (ev *evaluation) firstRemovedListing(match string, keptByGroup bool) *models.OtherListing {
	for i, v := range ev.vs {
		if ev.keep[i] {
			continue
		}
		for k := range v.OtherServers {
			e := &v.OtherServers[k]
			if match != "" && e.Match != match {
				continue
			}
			if keptByGroup && (e.KeptByGroup == nil || !*e.KeptByGroup) {
				continue
			}
			return e
		}
	}
	return nil
}

// otherServerProblems lists, for ValidateDecisions, the removed versions another server's item
// needs (a same-file listing without a survivor under the effective removals).
func otherServerProblems(g *models.DuplicateGroup, removed []int) []string {
	keys := map[string]bool{}
	for _, r := range removed {
		keys[g.Files[r].Version.Key] = true
	}
	var out []string
	for _, r := range removed {
		v := &g.Files[r].Version
		for k := range v.OtherServers {
			e := &v.OtherServers[k]
			if e.Match == models.OtherSameFile && !survivor(e, keys) {
				out = append(out, fmt.Sprintf("version %s is %s", v.Key, onlyCopyReason(e)))
				break
			}
		}
	}
	return out
}
