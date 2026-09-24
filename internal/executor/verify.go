package executor

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// verifiedPart is one part of a version as re-read right before acting.
type verifiedPart struct {
	path  string      // path as the media server sees it (fresh)
	size  int64       // size reported by the media server (fresh)
	local string      // mapped local path ("" when no mapping covers it)
	fi    fs.FileInfo // os.Stat(local) when local != ""
}

// verifiedVersion is a stored version confirmed against live data.
type verifiedVersion struct {
	file  *models.GroupFile
	fresh *models.MediaVersion
	parts []verifiedPart
	disc  *verifiedDisc // a full disc found again on disk (nil for a regular version)
}

// verification is the result of re-verifying a group (docs/DECISIONS.md D6, F17).
type verification struct {
	keepers []*verifiedVersion
	losers  map[int64]*verifiedVersion // by action ID
	// items are the freshly fetched Plex items of the group (nil value: the item is gone).
	items map[itemRef]*models.MediaItem
	// removing holds every media of the group decided "remove" (queued in this run or not).
	removing map[mediaRef]bool
	problem  string // non-empty: do not act; the group goes to review
	wait     string // non-empty: do not act yet; the removals stay queued (minimum age)
}

// mediaRef identifies a Plex media (version) on a media server.
type mediaRef struct {
	serverID int64
	mediaID  int64
}

// verify re-checks every version to remove and the kept versions against the freshly fetched
// Plex items (and the local filesystem when a path mapping covers them):
//   - each version to remove still exists with the same media id, part paths and sizes, is not an
//     optimized version and no part is reported missing; on disk (when mapped) its size matches;
//   - at least one kept version is present and fully verified the same way (and readable by Plex),
//     every part confirmed present — on disk when mapped, else by Plex's file check (exists=true;
//     an answer without "exists" confirms nothing); with a keep-per setting (one per resolution or
//     dynamic range) one such keeper in every partition a version is removed from
//     (partitionKeeperProblem);
//   - no version to remove shares a path or an inode with any kept version, nor a path with any
//     other media Plex lists for the group's items (docs/research/plex-api.md §7.10);
//   - every version to remove is older than settings.MinAgeHours (otherwise vr.wait is set:
//     docs/ARCHITECTURE.md §6 invariant 6, re-checked here).
func (r *run) verify(g *models.DuplicateGroup, targets []*target, fresh map[itemRef]*models.MediaItem) *verification {
	vr := &verification{
		losers:   make(map[int64]*verifiedVersion, len(targets)),
		items:    fresh,
		removing: map[mediaRef]bool{},
	}
	for i := range g.Files {
		if f := &g.Files[i]; f.Decision == models.DecisionRemove {
			vr.removing[mediaRef{serverOf(g, &f.Version), f.Version.MediaID}] = true
		}
	}
	for _, t := range targets {
		vv, problem := r.checkVersion(g, t.file, fresh, false)
		if problem != "" {
			vr.problem = fmt.Sprintf("Stale data for the version to remove (%s): %s", describeVersion(&t.file.Version), problem)
			return vr
		}
		vr.losers[t.a.ID] = vv
	}
	if problem := sharedWithOtherMedia(g, targets, vr); problem != "" {
		vr.problem = problem
		return vr
	}
	if problem := r.discSharedWithOtherMedia(g, targets, vr); problem != "" {
		vr.problem = problem
		return vr
	}

	var keeperProblems []string
	keeperProblem := map[*models.GroupFile]string{} // why a kept version could not be verified
	var keepFiles []*models.GroupFile
	for i := range g.Files {
		f := &g.Files[i]
		if f.Decision != models.DecisionKeep || isOptimized(&f.Version) {
			continue
		}
		keepFiles = append(keepFiles, f)
		vv, problem := r.checkVersion(g, f, fresh, true)
		if problem == "" {
			if bin := r.recycledKeeper(vv, g.MediaType); bin != "" {
				// Present today, purged automatically in a few days: never the kept copy.
				problem = "it lies in " + bin + ", which is emptied automatically"
			}
		}
		if problem != "" {
			keeperProblems = append(keeperProblems, fmt.Sprintf("%s: %s", describeVersion(&f.Version), problem))
			keeperProblem[f] = problem
			continue
		}
		vr.keepers = append(vr.keepers, vv)
	}

	// Invariant 2: a removed version never shares a file with a kept one (path or inode).
	for _, t := range targets {
		lv := vr.losers[t.a.ID]
		for _, kf := range keepFiles {
			var kv *verifiedVersion
			for _, k := range vr.keepers {
				if k.file == kf {
					kv = k
				}
			}
			if sharesFile(lv, kf, kv) {
				vr.problem = fmt.Sprintf("The version to remove (%s) is the same file as the kept version (%s)",
					describeVersion(&t.file.Version), describeVersion(&kf.Version))
				return vr
			}
			if lv.disc != nil {
				if inside := r.discKeeperInside(lv.disc, kf, kv); inside != "" {
					vr.problem = fmt.Sprintf("The kept version (%s) lies in the full disc to remove (%s): %s",
						describeVersion(&kf.Version), describeVersion(&t.file.Version), inside)
					return vr
				}
			}
		}
		if v := t.file.Version.Arr; v != nil {
			for _, kf := range keepFiles {
				if ka := kf.Version.Arr; ka != nil && ka.InstanceID == v.InstanceID && ka.FileID == v.FileID {
					vr.problem = fmt.Sprintf("The *arr file %d of the version to remove (%s) is also the kept version's file",
						v.FileID, describeVersion(&t.file.Version))
					return vr
				}
			}
		}
	}

	// Invariant 3: a keeper must be verified present right before any removal.
	if len(vr.keepers) == 0 {
		msg := "No kept version could be verified, so nothing was removed"
		if len(keeperProblems) > 0 {
			msg += ": " + strings.Join(keeperProblems, "; ")
		}
		vr.problem = msg
		return vr
	}
	// … and with "keep one per resolution / dynamic range", in every partition removed from.
	if problem := r.partitionKeeperProblem(g, targets, vr, keeperProblem); problem != "" {
		vr.problem = problem
		return vr
	}
	// … and, next to a full disc, a kept copy Plex can play (settings.KeepPlayableCopy).
	if problem := r.playableKeeperProblem(g, targets, vr); problem != "" {
		vr.problem = problem
		return vr
	}

	// Invariant 6: files younger than the minimum age are never removed (the setting may have been
	// raised since the approval). Like the engine, an unknown date counts as too young.
	if minAge := time.Duration(r.st.MinAgeHours) * time.Hour; minAge > 0 {
		now := r.s.now()
		for _, t := range targets {
			lv := vr.losers[t.a.ID]
			added := newestAdded(&t.file.Version, lv.fresh)
			if lv.disc != nil && lv.disc.d.NewestModTime.After(added) {
				added = lv.disc.d.NewestModTime // a disc changed since the scan is as new as its newest file
			}
			switch {
			case added.IsZero():
				vr.wait = fmt.Sprintf("the date %s was added is unknown and the minimum age is %s", describeVersion(&t.file.Version), minAge)
			case now.Sub(added) < minAge:
				vr.wait = fmt.Sprintf("%s was added %s and the minimum age is %s", describeVersion(&t.file.Version),
					added.UTC().Format(time.RFC3339), minAge)
			}
			if vr.wait != "" {
				break
			}
		}
	}
	return vr
}

// partitionKeeperProblem applies invariant 3 to every keepPer partition of the profile that applies
// to the group now (profileFor, partitions as engine.KeepPartition): a partition a version is
// removed from must still have one of its own kept versions verified present. With "keep one per
// resolution", removing a 1080p copy relies on the kept 1080p copy — a verified 4K keeper does not
// stand in for it. A partition the stored decisions keep nothing in is accepted only when a person
// overrode its engine-chosen keeper to "remove" (the one-keeper-overall check above still holds);
// otherwise the decisions were made under another keep-per setting (the profile changed after
// the approval) and the group needs a re-scan. "" when every partition is covered, and always
// without a keep-per setting.
func (r *run) partitionKeeperProblem(g *models.DuplicateGroup, targets []*target, vr *verification, keeperProblem map[*models.GroupFile]string) string {
	prof := r.profileFor(g)
	partOf := func(v *models.MediaVersion) (string, string) { return engine.KeepPartition(prof.KeepPer, v) }
	verified := map[string]bool{}
	for _, k := range vr.keepers {
		key, _ := partOf(&k.file.Version)
		verified[key] = true
	}
	checked := map[string]bool{}
	for _, t := range targets {
		key, label := partOf(&t.file.Version)
		if key == "" {
			return "" // no keep-per setting: one partition, covered by the check above
		}
		if checked[key] || verified[key] {
			continue
		}
		checked[key] = true
		var problems []string
		kept, overridden := false, false
		for i := range g.Files {
			f := &g.Files[i]
			if k, _ := partOf(&f.Version); k != key {
				continue
			}
			switch {
			case f.Decision == models.DecisionKeep && !isOptimized(&f.Version):
				kept = true
				if p := keeperProblem[f]; p != "" {
					problems = append(problems, fmt.Sprintf("%s: %s", describeVersion(&f.Version), p))
				}
			case f.Decision == models.DecisionRemove && f.EngineDecision == models.DecisionKeep &&
				models.Decision(strings.ToLower(strings.TrimSpace(string(f.Override)))) == models.DecisionRemove:
				overridden = true
			}
		}
		per := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(prof.KeepPer)), "_", " ")
		switch {
		case kept:
			msg := fmt.Sprintf("No kept %s version could be verified, so nothing was removed (the profile %q keeps one per %s; the other kept versions do not replace it)",
				label, prof.Name, per)
			if len(problems) > 0 {
				msg += ": " + strings.Join(problems, "; ")
			}
			return msg
		case !overridden:
			return fmt.Sprintf("The decisions keep no %s version although the profile %q now keeps one per %s (the profile changed after the approval?); nothing was removed — the group is re-scanned",
				label, prof.Name, per)
		}
	}
	return ""
}

// playable reports a version Plex can play whole for settings.KeepPlayableCopy: plexPlayable, with
// the mapped local paths checked too (discMember).
func (r *run) playable(v *models.MediaVersion) bool { return plexPlayable(v) && r.discMember(v) == "" }

// playableKeeperProblem applies settings.KeepPlayableCopy right before acting (docs/DECISIONS.md
// D9): in a group with a full disc (or a clip of one kept as a regular version: a group stored
// before loose clips were merged into sets), a regular version is only removed while a kept
// regular version of its keep-per partition — a copy Plex can play — was verified just now. A
// verified disc (or clip) alone does not count: when the kept regular copy vanished after the
// approval, removing the other one would leave only the disc.
func (r *run) playableKeeperProblem(g *models.DuplicateGroup, targets []*target, vr *verification) string {
	if !r.st.KeepPlayableCopy || !slices.ContainsFunc(g.Files, func(f models.GroupFile) bool { return !r.playable(&f.Version) }) {
		return ""
	}
	prof := r.profileFor(g)
	partOf := func(v *models.MediaVersion) string { k, _ := engine.KeepPartition(prof.KeepPer, v); return k }
	playable := map[string]bool{}
	for _, k := range vr.keepers {
		if v := &k.file.Version; r.playable(v) && !isOptimized(v) {
			playable[partOf(v)] = true
		}
	}
	for _, t := range targets {
		if v := &t.file.Version; r.playable(v) && !playable[partOf(v)] {
			return fmt.Sprintf("No kept copy Plex can play could be verified, so %s was not removed: only a full disc would be left "+
				"(Settings → Media Management → Always Keep a Plex-Playable Copy)", describeVersion(v))
		}
	}
	return ""
}

// newestAdded is the latest of the stored and fresh media-server addedAt and the *arr dateAdded
// (the same rule the engine applies for its min-age protection).
func newestAdded(stored, fresh *models.MediaVersion) time.Time {
	t := stored.AddedAt
	if fresh != nil && fresh.AddedAt.After(t) {
		t = fresh.AddedAt
	}
	if stored.Arr != nil && stored.Arr.DateAdded.After(t) {
		t = stored.Arr.DateAdded
	}
	return t
}

// sharedWithOtherMedia reports a version to remove whose file is also a part of another media Plex
// lists for the group's items — a version the user chose to keep, a media of another group (e.g.
// another edition) or the same file listed twice (a Plex database glitch). Removing it would take
// that media's file too. Other versions removed in the same run may share it.
func sharedWithOtherMedia(g *models.DuplicateGroup, targets []*target, vr *verification) string {
	queued := targetMedia(g, targets)
	for _, ref := range sortedItemRefs(vr.items) {
		it := vr.items[ref]
		if it == nil {
			continue
		}
		for i := range it.Versions {
			other := &it.Versions[i]
			if queued[mediaRef{ref.serverID, other.MediaID}] {
				continue
			}
			otherPaths := map[string]bool{}
			for _, p := range other.Parts {
				otherPaths[pathmap.Normalize(p.Path)] = true
			}
			for _, t := range targets {
				lv := vr.losers[t.a.ID]
				for _, p := range lv.parts {
					if otherPaths[pathmap.Normalize(p.path)] {
						return fmt.Sprintf("The file %s of the version to remove is also used by Plex media %d of item %s, which is not being removed",
							p.path, other.MediaID, ref.ratingKey)
					}
				}
			}
		}
	}
	return ""
}

// targetMedia returns the Plex media the queued removals take away: each target's media and, for
// a loose clip set, the Plex media of its clips (Plex lists one per clip; docs/DECISIONS.md D9
// "Loose clip sets"). Any other media sharing a file with a target still refuses the removal.
func targetMedia(g *models.DuplicateGroup, targets []*target) map[mediaRef]bool {
	queued := make(map[mediaRef]bool, len(targets))
	for _, t := range targets {
		sid := serverOf(g, &t.file.Version)
		if t.file.Version.MediaID > 0 {
			queued[mediaRef{sid, t.file.Version.MediaID}] = true
		}
		if d := t.file.Version.Disc; d.IsLooseClips() {
			for _, id := range d.PlexMediaIDs {
				if id > 0 {
					queued[mediaRef{sid, id}] = true
				}
			}
		}
	}
	return queued
}

// sortedItemRefs returns the keys of m in a stable order (server, then rating key).
func sortedItemRefs(m map[itemRef]*models.MediaItem) []itemRef {
	refs := make([]itemRef, 0, len(m))
	for ref := range m {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b itemRef) int {
		if a.serverID != b.serverID {
			return cmp.Compare(a.serverID, b.serverID)
		}
		return strings.Compare(a.ratingKey, b.ratingKey)
	})
	return refs
}

// remainsInItem reports whether the fresh Plex item of v keeps at least one other non-optimized
// media that is not decided "remove" once v is deleted. Deleting an item's last media through
// DELETE …/media/{id} has an unverified outcome (docs/research/plex-api.md §9.4, §9.5 step 7c).
func (vr *verification) remainsInItem(serverID int64, v *models.MediaVersion) bool {
	it := vr.items[itemRef{serverID, strings.TrimSpace(v.RatingKey)}]
	if it == nil {
		return false
	}
	for i := range it.Versions {
		o := &it.Versions[i]
		if o.MediaID != v.MediaID && o.MediaID > 0 && !isOptimized(o) && !vr.removing[mediaRef{serverID, o.MediaID}] {
			return true
		}
	}
	return false
}

// checkVersion confirms a stored version against the fresh Plex item and the local disk. A full
// disc found on disk (Plex does not list it) is confirmed on disk only; a disc a custom Plex
// scanner lists is confirmed with Plex and, when Dupearr can read it, on disk too (a disc to remove
// must be: its owned entries are what moves).
func (r *run) checkVersion(g *models.DuplicateGroup, f *models.GroupFile, fresh map[itemRef]*models.MediaItem, keeper bool) (*verifiedVersion, string) {
	stored := &f.Version
	if stored.Disc != nil && (stored.MediaID <= 0 || strings.HasPrefix(stored.Key, models.DiscKeyPrefix)) {
		return r.checkDiscVersion(g, f, fresh, keeper)
	}
	vv, problem := r.checkPlexVersion(g, f, fresh, keeper)
	if problem != "" || stored.Disc == nil {
		return vv, problem
	}
	if keeper && strings.TrimSpace(stored.Disc.LocalRoot) == "" {
		return vv, "" // confirmed by Plex's file check (a kept disc is never moved)
	}
	vd, problem := r.verifyDisc(stored, keeper)
	if problem != "" {
		return nil, problem
	}
	vv.disc = vd
	return vv, ""
}

// checkPlexVersion confirms a stored Plex version against the fresh Plex item and the local disk.
func (r *run) checkPlexVersion(g *models.DuplicateGroup, f *models.GroupFile, fresh map[itemRef]*models.MediaItem, keeper bool) (*verifiedVersion, string) {
	stored := &f.Version
	sid := serverOf(g, stored)
	item, ok := fresh[itemRef{sid, strings.TrimSpace(stored.RatingKey)}]
	switch {
	case !ok:
		return nil, "it was not re-checked with Plex"
	case item == nil:
		return nil, fmt.Sprintf("Plex item %s no longer exists", stored.RatingKey)
	case stored.MediaID <= 0:
		return nil, "its Plex media id is unknown"
	case len(stored.Parts) == 0:
		return nil, "it has no files"
	}
	var fv *models.MediaVersion
	for i := range item.Versions {
		if item.Versions[i].MediaID == stored.MediaID {
			fv = &item.Versions[i]
			break
		}
	}
	if fv == nil {
		return nil, fmt.Sprintf("Plex no longer lists media %d of item %s", stored.MediaID, stored.RatingKey)
	}
	if isOptimized(fv) {
		return nil, "Plex now reports it as an optimized version"
	}
	if len(fv.Parts) != len(stored.Parts) {
		return nil, fmt.Sprintf("the number of files changed (%d → %d)", len(stored.Parts), len(fv.Parts))
	}
	vv := &verifiedVersion{file: f, fresh: fv}
	for i, fp := range fv.Parts {
		sp := stored.Parts[i]
		switch {
		case pathmap.Normalize(sp.Path) != pathmap.Normalize(fp.Path):
			return nil, fmt.Sprintf("its file changed (%s → %s)", sp.Path, fp.Path)
		case sp.Size != fp.Size:
			return nil, fmt.Sprintf("the size of %s changed (%d → %d bytes)", fp.Path, sp.Size, fp.Size)
		case fp.Exists != nil && !*fp.Exists:
			return nil, fmt.Sprintf("Plex reports %s as missing", fp.Path)
		case keeper && fp.Accessible != nil && !*fp.Accessible:
			return nil, fmt.Sprintf("Plex cannot access %s", fp.Path)
		}
		vp := verifiedPart{path: fp.Path, size: fp.Size}
		if local, ok := r.mapper.ToLocal(models.PathSourceServer, sid, fp.Path); ok {
			fi, err := os.Stat(local)
			switch {
			case err != nil:
				return nil, fmt.Sprintf("cannot read %s on disk (%v)", local, err)
			case !fi.Mode().IsRegular():
				return nil, fmt.Sprintf("%s is not a regular file", local)
			case fi.Size() != fp.Size:
				return nil, fmt.Sprintf("%s is %d bytes on disk but Plex reports %d", local, fi.Size(), fp.Size)
			}
			vp.local, vp.fi = local, fi
		} else if keeper && fp.Exists == nil {
			// Invariant 3 needs a keeper confirmed present: on disk (above) or by Plex's file check.
			// An answer without "exists" (partial data) confirms nothing — the other copy may be
			// the last one.
			return nil, fmt.Sprintf("could not confirm that %s exists: Plex did not report it and no path mapping lets Dupearr check the file", fp.Path)
		}
		vv.parts = append(vv.parts, vp)
	}
	return vv, ""
}

// sharesFile reports whether the version to remove shares a path (server-side or resolved local)
// or an inode with a kept file. kv (the verified keeper) may be nil when the keeper could not be
// verified; its stored paths are still compared.
func sharesFile(lv *verifiedVersion, kf *models.GroupFile, kv *verifiedVersion) bool {
	keepPaths := map[string]bool{}
	for _, p := range kf.Version.Parts {
		keepPaths[pathmap.Normalize(p.Path)] = true
	}
	if kv != nil {
		for _, p := range kv.parts {
			keepPaths[pathmap.Normalize(p.path)] = true
		}
	}
	for _, sp := range lv.file.Version.Parts {
		if keepPaths[pathmap.Normalize(sp.Path)] {
			return true
		}
	}
	for _, lp := range lv.parts {
		if keepPaths[pathmap.Normalize(lp.path)] {
			return true
		}
		if kv == nil || lp.fi == nil {
			continue
		}
		for _, kp := range kv.parts {
			if kp.fi == nil {
				continue
			}
			if os.SameFile(lp.fi, kp.fi) || sameResolved(lp.local, kp.local) {
				return true
			}
		}
	}
	return false
}

// sameResolved reports whether two local paths resolve to the same path.
func sameResolved(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// recycledKeeper names the recycle bin a verified kept version lies in ("" when none): Dupearr's
// own bin, or the recycle bin of an enabled *arr instance of the media type's kind (Radarr for
// movies, Sonarr for episodes; an *arr bin inside a Plex library is indexed as another version of
// the title). Files there are deleted permanently by the bins' cleanup, so such a copy must never
// be what the removal of the other copies relies on. An *arr whose settings cannot be read is not
// checked (its tracked files are not removed then anyway).
func (r *run) recycledKeeper(vv *verifiedVersion, mt models.MediaType) string {
	kind := models.ArrRadarr
	if mt == models.MediaTypeEpisode {
		kind = models.ArrSonarr
	}
	own := ""
	if strings.TrimSpace(r.st.RecycleBinPath) != "" {
		if b, err := r.recycleBin(); err == nil {
			own = b
		}
	}
	for _, p := range vv.parts {
		if own != "" && p.local != "" {
			res := filepath.Clean(p.local)
			if rp, err := filepath.EvalSymlinks(p.local); err == nil {
				res = rp
			}
			if res == own || isWithin(res, own) {
				return "Dupearr's recycle bin " + own
			}
		}
		for _, id := range sortedKeys(r.arrs) {
			inst := r.arrs[id]
			if !inst.Enabled || inst.Kind != kind {
				continue
			}
			c := r.arrClient(inst)
			if c == nil {
				continue
			}
			bin, err := r.arrRecycleBin(inst, c)
			if err != nil || bin == "" {
				continue
			}
			label := fmt.Sprintf("the recycle bin of %s (%s)", inst.Name, bin)
			if kb := pathKey(bin); kb != "" && withinSlash(pathKey(p.path), kb) {
				return label
			}
			if p.local == "" {
				continue
			}
			if lb, ok := r.mapper.ToLocal(models.PathSourceArr, inst.ID, bin); ok && filepath.IsAbs(lb) {
				if lp := filepath.Clean(p.local); isWithin(lp, filepath.Clean(lb)) {
					return label
				}
			}
		}
	}
	return ""
}
