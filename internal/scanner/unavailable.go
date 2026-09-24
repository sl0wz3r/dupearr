package scanner

import (
	"errors"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// unavailableReason is the StatusReason of a stored group kept although its duplicate was not
// built again because the media server reports some of its files missing.
const unavailableReason = "Some files are unavailable — check your mounts"

// noteUnavailable records the items with a version whose file the media server reports missing
// (part exists == false) without it being confirmed deleted (see decorate: its local folder is
// there, the file is not). BuildGroups leaves such versions out, so a duplicate may not be built
// again although nothing was removed — typically an unmounted share or a NAS that is still
// starting. keepUnavailable keeps their stored groups instead of resolving them.
func (p *pipeline) noteUnavailable(items []models.MediaItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range items {
		it := &items[i]
		if strings.TrimSpace(it.RatingKey) == "" {
			continue
		}
		for j := range it.Versions {
			v := &it.Versions[j]
			if v.Key == "" && partMissing(v) {
				p.unavailableVersions["rk:"+strings.TrimSpace(it.RatingKey)] = true
			}
			if (v.Key != "" && p.unavailableVersions[v.Key]) || (v.Key == "" && partMissing(v)) {
				p.unavailable[refKey{server: it.ServerID, rk: strings.TrimSpace(it.RatingKey)}] = true
			}
		}
	}
}

// partMissing reports a version with a part the media server explicitly reports missing.
func partMissing(v *models.MediaVersion) bool {
	for _, pt := range v.Parts {
		if pt.Exists != nil && !*pt.Exists {
			return true
		}
	}
	return false
}

// keepUnavailable keeps the stored open groups that hold a version noteUnavailable found
// unavailable and that this run did not build again: missing files are not a resolved duplicate.
// Such a group keeps its status and decisions, gets the flag unavailable_version and the reason
// "Some files are unavailable — check your mounts", and is not resolved in this run (it comes back
// as a normal group once the files are there again). A group in review because data was
// incomplete keeps its reason (it blocks approvals, see ArrDataMissing). Call after persistGroups,
// before the resolution.
func (p *pipeline) keepUnavailable() {
	p.mu.Lock()
	keys := groupRatingKeys(p.unavailable)
	p.mu.Unlock()
	if len(keys) == 0 {
		return
	}
	repo := p.s.d.Store.Groups()
	for _, server := range sortedIDs(keys) {
		if p.ctx.Err() != nil {
			return
		}
		gs, err := repo.ListByRatingKeys(p.ctx, server, keys[server])
		if err != nil {
			// Nothing may be resolved that could depend on these items.
			p.addError()
			p.log.Error("Could not load the groups of items with unavailable files; their libraries are not resolved in this scan", "error", err)
			for _, rk := range keys[server] {
				p.mu.Lock()
				p.failedRKs[refKey{server: server, rk: rk}] = err
				p.mu.Unlock()
			}
			continue
		}
		for i := range gs {
			g := &gs[i]
			if g.Status == models.GroupIgnored || g.Status == models.GroupResolved || g.LastScanID == p.run.ID || !p.holdsUnavailable(g) {
				continue
			}
			p.markUnavailable(g.ID)
		}
	}
}

// holdsUnavailable reports whether a stored group has a version noteUnavailable found unavailable.
func (p *pipeline) holdsUnavailable(g *models.DuplicateGroup) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range g.Files {
		v := &g.Files[i].Version
		if p.unavailableVersions[v.Key] || (v.ServerID == g.ServerID && p.unavailableVersions["rk:"+strings.TrimSpace(v.RatingKey)]) {
			return true
		}
	}
	return false
}

// markUnavailable stamps a stored group with this run (so it is not resolved), the flag
// unavailable_version and unavailableReason, keeping its status.
func (p *pipeline) markUnavailable(id int64) {
	p.s.groupMu.Lock()
	defer p.s.groupMu.Unlock()
	repo := p.s.d.Store.Groups()
	g, err := repo.Get(p.ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		p.addError()
		p.log.Error("Could not load a group with unavailable files; it is not resolved in this scan", "groupId", id, "error", err)
		p.mu.Lock()
		p.protect[id] = true
		p.mu.Unlock()
		return
	}
	if g.Status == models.GroupIgnored || g.Status == models.GroupResolved || g.LastScanID == p.run.ID {
		return
	}
	g.LastScanID = p.run.ID
	if !g.HasFlag(models.FlagUnavailableVersion) {
		g.Flags = append(g.Flags, models.FlagUnavailableVersion)
	}
	if !(g.Status == models.GroupReview && strings.HasPrefix(g.StatusReason, incompletePrefix)) {
		g.StatusReason = unavailableReason
	}
	p.mu.Lock()
	p.protect[id] = true
	p.mu.Unlock()
	if _, err := repo.Upsert(p.ctx, g); err != nil {
		p.addError()
		p.log.Error("Could not keep a group with unavailable files; its libraries are not resolved in this scan", "groupId", id, "error", err)
		p.mu.Lock()
		for _, l := range g.LibraryIDs {
			p.unsafeLibs[l] = true
		}
		p.mu.Unlock()
		return
	}
	p.log.Info("Duplicate kept: the media server reports some of its files missing", "groupId", id, "title", displayTitle(g))
	p.s.publish(events.NameDuplicate, events.ActionUpdated, *g)
}
