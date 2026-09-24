package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// keeperRelation describes where the kept copy lives relative to a removed *arr-tracked file.
type keeperRelation int

const (
	// keeperElsewhere: no keeper is in the *arr item (other instance, other item, untracked
	// elsewhere) — the *arr would re-download the title.
	keeperElsewhere keeperRelation = iota
	// keeperInFolder: an untracked keeper lies inside the *arr item's folder — a rescan adopts it.
	keeperInFolder
	// keeperTracked: a keeper is already tracked by the same *arr item.
	keeperTracked
)

// postActions runs after the group's real removals: *arr rescan / unmonitor / exclusion
// (docs/ARCHITECTURE.md §6 step 2) and Plex stale-entry cleanup + refresh (docs/DECISIONS.md D6).
// Failures are recorded on the affected actions; they never change the removals' status.
func (r *run) postActions(g *models.DuplicateGroup, done []*outcome, vr *verification) {
	var removed []*outcome
	for _, oc := range done {
		if oc.status == models.ActionSucceeded {
			removed = append(removed, oc)
		}
	}
	if len(removed) == 0 {
		return
	}
	ctx, cancel := r.postCtx()
	defer cancel()
	r.arrPostActions(ctx, g, removed, vr)
	r.plexPostActions(ctx, g, removed, vr)
}

// postGrace bounds post-processing when the run was cancelled right after a removal.
const postGrace = 15 * time.Second

// postCtx is the run's context or, when the run was cancelled after a removal, a short detached
// grace period: the *arr should still be told to adopt the kept file or to stop monitoring.
func (r *run) postCtx() (context.Context, context.CancelFunc) {
	if r.ctx.Err() == nil {
		return r.ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(r.ctx), postGrace)
}

type arrItemKey struct{ instanceID, itemID int64 }

func (r *run) arrPostActions(ctx context.Context, g *models.DuplicateGroup, removed []*outcome, vr *verification) {
	type rescan struct {
		inst    models.ArrInstance
		client  ArrClient
		actions []*models.Action
	}
	rescans := map[arrItemKey]*rescan{}
	var order []arrItemKey
	unmonitored := map[arrItemKey]bool{}
	excluded := map[arrItemKey]bool{}

	for _, oc := range removed {
		info := oc.t.file.Version.Arr
		if info == nil {
			continue
		}
		a := oc.t.a
		inst, ok := r.arrs[info.InstanceID]
		if !ok || !inst.Enabled {
			r.appendNote(a, fmt.Sprintf("the *arr instance #%d that tracked this file is missing or disabled; it was not updated", info.InstanceID))
			continue
		}
		c := r.arrClient(inst)
		if c == nil {
			continue
		}
		key := arrItemKey{info.InstanceID, info.ItemID}
		// A removed disc the *arr tracked a clip of: the *arr still lists that file, so it is always
		// rescanned (it drops the file and, when the kept copy is in its folder, adopts it).
		isDisc := oc.t.file.Version.Disc != nil
		addRescan := func() {
			if info.ItemID <= 0 {
				return
			}
			if rescans[key] == nil {
				rescans[key] = &rescan{inst: inst, client: c}
				order = append(order, key)
			}
			rescans[key].actions = append(rescans[key].actions, a)
		}
		switch r.relationToKeepers(info, vr.keepers) {
		case keeperTracked:
		case keeperInFolder:
			if !r.st.ArrRescanAfterDelete && !isDisc {
				continue
			}
			addRescan()
		case keeperElsewhere:
			if isDisc {
				addRescan()
				if !r.st.UnmonitorWhenKeeperElsewhere {
					r.appendNote(a, fmt.Sprintf("%s tracked a file of this disc: the movie is now missing there and, if monitored, it may be downloaded again", inst.Name))
				}
			}
			if r.st.UnmonitorWhenKeeperElsewhere {
				// Radarr unmonitors the movie (once per item); Sonarr the file's episodes.
				if inst.Kind == models.ArrSonarr || !unmonitored[key] {
					unmonitored[key] = true
					if err := c.Unmonitor(ctx, *info); err != nil {
						r.appendNote(a, fmt.Sprintf("could not unmonitor it in %s: %v", inst.Name, err))
					} else {
						r.appendNote(a, fmt.Sprintf("unmonitored in %s (the kept copy lives elsewhere)", inst.Name))
					}
				}
			}
			if r.st.AddExclusionWhenKeeperElsewhere && !excluded[key] {
				excluded[key] = true
				r.appendNote(a, r.addExclusion(ctx, g, inst, c))
			}
		}
	}
	// One rescan per *arr item, after every removal of the group (D3 re-link ordering).
	for _, key := range order {
		rs := rescans[key]
		note := fmt.Sprintf("rescanned in %s so it adopts the kept file", rs.inst.Name)
		if err := rs.client.Rescan(ctx, key.itemID); err != nil {
			note = fmt.Sprintf("could not rescan in %s: %v", rs.inst.Name, err)
		}
		for _, a := range rs.actions {
			r.appendNote(a, note)
		}
	}
}

// addExclusion adds an import-list exclusion for a Radarr movie (Sonarr exclusions are per
// series, which a single episode's duplicate must not decide).
func (r *run) addExclusion(ctx context.Context, g *models.DuplicateGroup, inst models.ArrInstance, c ArrClient) string {
	if inst.Kind != models.ArrRadarr || g.MediaType != models.MediaTypeMovie {
		return "import-list exclusions are only added for Radarr movies"
	}
	tmdb, err := strconv.Atoi(strings.TrimSpace(g.ExternalIDs["tmdb"]))
	if err != nil || tmdb <= 0 {
		return "no import-list exclusion added: the TMDb id is unknown"
	}
	if err := c.AddExclusion(ctx, arr.ExclusionTarget{TmdbID: tmdb, Title: g.Title, Year: g.Year}); err != nil {
		return fmt.Sprintf("could not add an import-list exclusion in %s: %v", inst.Name, err)
	}
	return fmt.Sprintf("added an import-list exclusion in %s", inst.Name)
}

// relationToKeepers classifies where the kept copies live relative to a removed tracked file.
func (r *run) relationToKeepers(info *models.ArrFileInfo, keepers []*verifiedVersion) keeperRelation {
	rel := keeperElsewhere
	for _, k := range keepers {
		kv := &k.file.Version
		if ka := kv.Arr; ka != nil {
			if ka.InstanceID == info.InstanceID && ka.ItemID == info.ItemID {
				return keeperTracked
			}
			continue
		}
		if r.inArrItem(kv, info) {
			rel = keeperInFolder
		}
	}
	return rel
}

// inArrItem reports whether every part of v lies inside the *arr item's folder (compared on the
// local side when both sides are mapped, else on the remote paths, which Plex and the *arrs
// usually share).
func (r *run) inArrItem(v *models.MediaVersion, info *models.ArrFileInfo) bool {
	itemPath := strings.TrimSpace(info.ItemPath)
	if itemPath == "" || len(v.Parts) == 0 {
		return false
	}
	itemLocal, itemMapped := r.mapper.ToLocal(models.PathSourceArr, info.InstanceID, itemPath)
	for _, p := range v.Parts {
		in := false
		if itemMapped {
			if kl, ok := r.mapper.ToLocal(models.PathSourceServer, v.ServerID, p.Path); ok {
				in = isWithin(filepath.Clean(kl), filepath.Clean(itemLocal))
			}
		}
		if !in {
			in = withinSlash(pathmap.Normalize(p.Path), pathmap.Normalize(itemPath))
		}
		if !in {
			return false
		}
	}
	return true
}

// withinSlash reports whether the normalized (forward-slash) path p lies strictly inside root.
func withinSlash(p, root string) bool {
	if root == "" || p == root {
		return false
	}
	if strings.HasSuffix(root, "/") {
		return strings.HasPrefix(p, root)
	}
	return strings.HasPrefix(p, root+"/")
}

func (r *run) plexPostActions(ctx context.Context, g *models.DuplicateGroup, removed []*outcome, vr *verification) {
	if r.st.CleanupPlexStaleEntries {
		for _, oc := range removed {
			if oc.choice == nil || oc.choice.method == models.MethodPlex || oc.t.file.Version.MediaID <= 0 {
				continue // a disc found on disk has no Plex entry
			}
			r.appendNote(oc.t.a, r.cleanupStaleEntry(ctx, &oc.t.file.Version, vr))
		}
	}
	if !r.st.RefreshPlexAfterDelete {
		return
	}
	r.scanDiscFolders(ctx, g, removed)
	byServer := involvedRatingKeys(g)
	for _, sid := range sortedKeys(byServer) {
		c, reason := r.plexClient(sid)
		if c == nil {
			r.s.d.Log.Warn("Cannot refresh Plex after a removal", "group", g.ID, "reason", reason)
			continue
		}
		for _, rk := range byServer[sid] {
			if err := c.RefreshItem(ctx, rk); err != nil && !errors.Is(err, plex.ErrNotFound) {
				r.s.d.Log.Warn("Could not refresh a Plex item after a removal", "group", g.ID, "ratingKey", rk, "error", err)
			}
		}
	}
}

// cleanupStaleEntry removes the Plex entry of a version whose file was removed through the *arr
// or the filesystem, but only when Plex itself reports every part missing, none of its paths is a
// kept path, the entry still points at the removed files and (when mapped) the files are gone.
// Returns a note for the action ("" when nothing was worth reporting).
func (r *run) cleanupStaleEntry(ctx context.Context, v *models.MediaVersion, vr *verification) string {
	c, _ := r.plexClient(v.ServerID)
	if c == nil {
		return ""
	}
	for _, k := range vr.keepers {
		if k.file.Version.MediaID == v.MediaID && k.file.Version.RatingKey == v.RatingKey {
			return ""
		}
	}
	item, err := c.Item(ctx, v.RatingKey)
	if errors.Is(err, plex.ErrNotFound) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("could not re-check Plex for a stale entry: %v", err)
	}
	if item == nil {
		return ""
	}
	var (
		fv     *models.MediaVersion
		others int // other non-optimized media of the item
	)
	for i := range item.Versions {
		switch iv := &item.Versions[i]; {
		case iv.MediaID == v.MediaID:
			fv = iv
		case iv.MediaID > 0 && !isOptimized(iv):
			others++
		}
	}
	if fv == nil || len(fv.Parts) == 0 {
		return ""
	}
	if others == 0 {
		// Deleting an item's last media has an unverified outcome (docs/research/plex-api.md §9.4).
		return "the stale Plex entry was left in place (it is the item's only version; Plex shows it as unavailable until its trash is emptied)"
	}
	keepPaths := map[string]bool{}
	for _, k := range vr.keepers {
		for _, p := range k.file.Version.Parts {
			keepPaths[pathmap.Normalize(p.Path)] = true
		}
		for _, p := range k.parts {
			keepPaths[pathmap.Normalize(p.path)] = true
		}
	}
	removedPaths := map[string]bool{}
	for _, p := range v.Parts {
		removedPaths[pathmap.Normalize(p.Path)] = true
	}
	for _, p := range fv.Parts {
		np := pathmap.Normalize(p.Path)
		switch {
		case p.Exists == nil || *p.Exists:
			return "" // Plex does not report the file missing: leave the entry to Plex's own scan
		case keepPaths[np], !removedPaths[np]:
			return ""
		}
		if local, ok := r.mapper.ToLocal(models.PathSourceServer, v.ServerID, p.Path); ok {
			if _, err := os.Lstat(local); err == nil {
				return ""
			}
		}
	}
	if ok, err := r.plexDeletionAllowed(v.ServerID, c); err != nil || !ok {
		return "the stale Plex entry was left in place (media deletion is not allowed on the server)"
	}
	if problem, err := r.serverIdentityProblem(ctx, v.ServerID, c); err != nil || problem != "" {
		return "the stale Plex entry was left in place (the media server's identity could not be confirmed)"
	}
	if err := c.DeleteMedia(ctx, v.RatingKey, v.MediaID); err != nil && !errors.Is(err, plex.ErrNotFound) {
		return fmt.Sprintf("could not remove the stale Plex entry: %v", err)
	}
	return "removed the stale Plex entry"
}
