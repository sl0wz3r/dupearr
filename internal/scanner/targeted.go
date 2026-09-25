package scanner

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// targetedResolvedReason is the status reason of groups a targeted scan resolves.
const targetedResolvedReason = "No longer detected as a duplicate (targeted scan)"

// maxShowProbes bounds the episodes tried per show title when identifying a series' episodes.
const maxShowProbes = 3

// normalizeTargetBody trims and de-duplicates rating keys and normalizes the IMDb id ("" when it
// is not a "tt<digits>" id).
func normalizeTargetBody(b models.TargetedScanBody) models.TargetedScanBody {
	seen := map[string]bool{}
	var rks []string
	for _, rk := range b.RatingKeys {
		rk = strings.TrimSpace(rk)
		if rk == "" || seen[rk] {
			continue
		}
		seen[rk] = true
		rks = append(rks, rk)
	}
	b.RatingKeys = rks
	b.ImdbID = normID("imdb", b.ImdbID)
	if !validIMDb(b.ImdbID) {
		b.ImdbID = ""
	}
	return b
}

// validIMDb reports a normalized "tt<digits>" id.
func validIMDb(s string) bool {
	if len(s) < 3 || !strings.HasPrefix(s, "tt") {
		return false
	}
	for _, r := range s[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// wantsMovies / wantsShows tell which library types external ids address: tmdb → movies,
// tvdb → shows (Sonarr sends the *series* tvdb id), imdb → movies and/or shows (only one kind when
// the other id says which).
func wantsMovies(b models.TargetedScanBody) bool {
	return b.TmdbID > 0 || (b.ImdbID != "" && b.TvdbID <= 0)
}

func wantsShows(b models.TargetedScanBody) bool {
	return b.TvdbID > 0 || (b.ImdbID != "" && b.TmdbID <= 0)
}

// movieMatches reports a movie whose ids match the body.
func movieMatches(ids map[string]string, b models.TargetedScanBody) bool {
	if b.TmdbID > 0 && normID("tmdb", ids["tmdb"]) == strconv.Itoa(b.TmdbID) {
		return true
	}
	return b.ImdbID != "" && normID("imdb", ids["imdb"]) == b.ImdbID
}

// showMatches reports an episode whose show-level ids match the body.
func showMatches(showIDs map[string]string, b models.TargetedScanBody) bool {
	if b.TvdbID > 0 && normID("tvdb", showIDs["tvdb"]) == strconv.Itoa(b.TvdbID) {
		return true
	}
	return b.ImdbID != "" && normID("imdb", showIDs["imdb"]) == b.ImdbID
}

// targetServers returns the enabled servers a targeted scan covers.
func (c *scanConfig) targetServers(serverID int64) ([]models.MediaServer, error) {
	if serverID != 0 {
		srv, ok := c.servers[serverID]
		if !ok {
			return nil, fmt.Errorf("media server %d is not configured or not enabled", serverID)
		}
		return []models.MediaServer{srv}, nil
	}
	out := make([]models.MediaServer, 0, len(c.servers))
	for _, srv := range c.servers {
		out = append(out, srv)
	}
	if len(out) == 0 {
		return nil, errors.New("no enabled media server")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// libraryForItem returns the scannable library of server serverID holding item (by section key).
func (c *scanConfig) libraryForItem(serverID int64, item *models.MediaItem) (models.Library, bool) {
	key := strings.TrimSpace(item.SectionKey)
	if key == "" {
		return models.Library{}, false
	}
	for _, l := range c.libraries {
		if l.ServerID == serverID && l.SectionKey == key && c.scannable(l) {
			return l, true
		}
	}
	return models.Library{}, false
}

// markFailed records an item whose data is unusable in this run without counting a new error
// (the cause was counted already, e.g. its library listing failed).
func (p *pipeline) markFailed(k refKey, err error) {
	p.mu.Lock()
	p.failedRKs[k] = err
	p.mu.Unlock()
}

// runTargeted executes a targeted scan. Rating keys are fetched directly (to learn their
// library); external ids select items from the listings of the enabled libraries of the matching
// type. Every library involved is listed in full — the shared-file (multi-episode) index must be
// complete before any version can be judged removable — together with its scope-group partners,
// whose matching items join the scan.
func (p *pipeline) runTargeted(body models.TargetedScanBody) error {
	cfg, err := p.s.loadScanConfig(p.ctx)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	p.cfg = cfg
	servers, err := cfg.targetServers(body.ServerID)
	if err != nil {
		return err
	}
	inScope := map[int64]bool{}
	for _, srv := range servers {
		inScope[srv.ID] = true
	}

	targets := map[refKey]bool{}
	gone := map[refKey]bool{}
	pre := map[refKey]*models.MediaItem{}
	libs := map[int64]models.Library{}

	for _, srv := range servers {
		if len(body.RatingKeys) == 0 {
			break
		}
		client, err := p.serverClient(srv)
		if err != nil {
			for _, rk := range body.RatingKeys {
				p.itemFailed(refKey{server: srv.ID, rk: rk}, err)
			}
			continue
		}
		for _, rk := range body.RatingKeys {
			k := refKey{server: srv.ID, rk: rk}
			item, err := client.Item(p.ctx, rk)
			switch {
			case p.ctx.Err() != nil:
				return canceled(p.ctx.Err())
			case errors.Is(err, mediaserver.ErrNotFound) && srv.Kind.ReadOnly():
				// A Jellyfin row id also disappears when only its primary version left (the row is
				// re-keyed to the next version, research S9): never "gone", the next full scan
				// rebuilds the groups (docs/DECISIONS.md D12).
				p.itemFailed(k, fmt.Errorf("item %s: %w", rk, err))
				continue
			case errors.Is(err, mediaserver.ErrNotFound):
				gone[k] = true // deleted from the server: its groups may be resolved
				continue
			case err != nil:
				p.itemFailed(k, fmt.Errorf("item %s: %w", rk, err))
				continue
			case item == nil:
				p.itemFailed(k, fmt.Errorf("item %s: empty response", rk))
				continue
			}
			lib, ok := cfg.libraryForItem(srv.ID, item)
			if !ok {
				p.log.Info("Targeted item is not in an enabled movie or TV library; skipped",
					"serverId", srv.ID, "ratingKey", rk, "section", item.SectionKey)
				continue
			}
			targets[k], pre[k], libs[lib.ID] = true, item, lib
		}
	}
	byIDs := wantsMovies(body) || wantsShows(body)
	if len(body.RatingKeys) > 0 && !byIDs && len(targets) == 0 && len(gone) == 0 && len(p.failedRKs) > 0 {
		return fmt.Errorf("could not load any of the %d targeted items: %w", len(body.RatingKeys), p.firstItemError())
	}
	if byIDs {
		for _, l := range cfg.libraries {
			if !cfg.scannable(l) || !inScope[l.ServerID] {
				continue
			}
			switch mediaTypeOf(l.Type) {
			case models.MediaTypeMovie:
				if wantsMovies(body) {
					libs[l.ID] = l
				}
			case models.MediaTypeEpisode:
				if wantsShows(body) {
					libs[l.ID] = l
				}
			}
		}
	}
	cfg.withScopePartners(libs)

	partners := cfg.overlapPartners(libs)
	if cfg.multi {
		// Every movie and TV library of the other servers, whatever the targeted media type
		// (docs/DECISIONS.md D11): another server may list a movie file in a TV library.
		p.readSections()
		for id, l := range cfg.crossServerPartners(libs, p.sections) {
			partners[id] = l
		}
	}
	p.listLibraries(sortedLibraries(libs), partners)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	if len(libs) > 0 && len(p.listed) == 0 {
		return fmt.Errorf("could not list any of the %d libraries: %s", len(libs), p.libraryErrorSummary())
	}

	// Rating-key targets need their library's listing (shared-file index, partners).
	detailed := map[refKey]*models.MediaItem{}
	for k, item := range pre {
		ir := p.index.refs[k]
		if ir == nil || ir.indexOnly {
			delete(targets, k)
			if err, failed := p.failedLibs[p.libraryIDOf(k, item)]; failed {
				p.markFailed(k, fmt.Errorf("its library could not be listed: %w", err))
			} else {
				p.itemFailed(k, fmt.Errorf("item %s is missing from its library listing", k.rk))
			}
			continue
		}
		p.decorate(item, ir)
		detailed[k] = item
	}

	if wantsMovies(body) {
		for _, k := range p.index.order {
			ir := p.index.refs[k]
			if !ir.indexOnly && mediaTypeOf(ir.lib.Type) == models.MediaTypeMovie && movieMatches(ir.ref.ExternalIDs, body) {
				targets[k] = true
			}
		}
	}
	if wantsShows(body) {
		p.selectSeriesEpisodes(body, targets, detailed)
	}
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}

	// Targets plus their cross-library partners.
	all := map[refKey]bool{}
	for k := range targets {
		all[k] = true
		for _, m := range p.index.partners(k) {
			all[m] = true
		}
	}
	var fetch []refKey
	for _, k := range p.index.order {
		if all[k] && detailed[k] == nil {
			if _, failed := p.failedRKs[k]; !failed {
				fetch = append(fetch, k)
			}
		}
	}
	for k, it := range p.fetchDetailsKeyed(fetch) {
		detailed[k] = it
	}
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	var items []models.MediaItem
	for _, k := range p.index.order {
		if it := detailed[k]; all[k] && it != nil {
			items = append(items, *it)
		}
	}
	p.run.Stats.ItemsExamined = len(items)
	if len(targets) > 0 {
		p.progress(fmt.Sprintf("Scanning %d targeted items (%d with cross-library partners)", len(targets), len(items)))
	}
	p.markIncomplete()
	p.attachDiscs(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}

	busy := p.enrich(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.watch(items)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.applyFileAges(items)
	p.noteUnavailable(items)
	groups := engine.BuildGroups(items, engine.GroupOptionsFromSettings(cfg.settings, cfg.exclusions, cfg.libraries))
	p.annotateCrossServer(groups, false)
	p.persistGroups(groups, busy)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.keepUnavailable()
	for k := range gone {
		targets[k] = true
		all[k] = true
	}
	p.resolveTargeted(targets, all)
	if err := p.ctx.Err(); err != nil {
		return canceled(err)
	}
	p.markOtherServerKeeps()
	p.autoApprove()
	return nil
}

// firstItemError returns the item failure with the smallest key (deterministic), or nil.
func (p *pipeline) firstItemError() error {
	var first *refKey
	for k := range p.failedRKs {
		if first == nil || k.server < first.server || (k.server == first.server && k.rk < first.rk) {
			kk := k
			first = &kk
		}
	}
	if first == nil {
		return nil
	}
	return p.failedRKs[*first]
}

// libraryIDOf returns the id of the library holding a pre-fetched item (0 when unknown).
func (p *pipeline) libraryIDOf(k refKey, item *models.MediaItem) int64 {
	if l, ok := p.cfg.libraryForItem(k.server, item); ok {
		return l.ID
	}
	return 0
}

// selectSeriesEpisodes adds to targets the episodes of the series identified by the body's
// show-level ids (Sonarr sends series ids; episode listings only carry episode-level ids). Only
// "interesting" episodes are considered — ≥2 versions, a cross-library identity, or an open
// stored group — clustered by show title: one episode per title is fetched to learn the show's
// ids, and only the clusters that match are fetched in full.
func (p *pipeline) selectSeriesEpisodes(b models.TargetedScanBody, targets map[refKey]bool, detailed map[refKey]*models.MediaItem) {
	open := map[refKey]bool{}
	for _, libID := range sortedIDs(p.listed) {
		lib := p.cfg.libraries[libID]
		if mediaTypeOf(lib.Type) != models.MediaTypeEpisode {
			continue
		}
		gs, err := p.listOpenGroups(store.GroupFilter{LibraryID: libID, MediaType: models.MediaTypeEpisode})
		if err != nil {
			p.addError()
			p.log.Warn("Could not list stored groups of a TV library; only current duplicates are targeted",
				"library", lib.Title, "error", upstreamerr.Message(err))
			continue
		}
		for _, g := range gs {
			for _, f := range g.Files {
				open[refKey{server: f.Version.ServerID, rk: f.Version.RatingKey}] = true
			}
		}
	}

	type cluster struct{ keys []refKey }
	clusters := map[string]*cluster{}
	var order []string
	for _, k := range p.index.order {
		ir := p.index.refs[k]
		if ir.indexOnly || mediaTypeOf(ir.lib.Type) != models.MediaTypeEpisode {
			continue
		}
		if ir.ref.MediaCount < 2 && !p.index.crossLibrary(k) && !open[k] && detailed[k] == nil {
			continue
		}
		ck := fmt.Sprintf("%d|%s", k.server, strings.ToLower(strings.TrimSpace(ir.ref.ShowTitle)))
		c := clusters[ck]
		if c == nil {
			c = &cluster{}
			clusters[ck] = c
			order = append(order, ck)
		}
		c.keys = append(c.keys, k)
	}

	// Probe rounds: one episode per undecided cluster per round, in parallel.
	matched := map[string]bool{}
	decided := map[string]bool{}
	for round := 0; round < maxShowProbes; round++ {
		var probes []refKey
		probeOf := map[refKey]string{}
		for _, ck := range order {
			if decided[ck] {
				continue
			}
			c := clusters[ck]
			// An already fetched episode decides without a request.
			for _, k := range c.keys {
				if it := detailed[k]; it != nil {
					decided[ck], matched[ck] = true, showMatches(it.ShowIDs, b)
					break
				}
			}
			if decided[ck] || round >= len(c.keys) {
				continue
			}
			probes = append(probes, c.keys[round])
			probeOf[c.keys[round]] = ck
		}
		if len(probes) == 0 || p.ctx.Err() != nil {
			break
		}
		for k, it := range p.fetchDetailsKeyed(probes) {
			detailed[k] = it
			ck := probeOf[k]
			decided[ck], matched[ck] = true, showMatches(it.ShowIDs, b)
		}
	}

	var rest []refKey
	for _, ck := range order {
		if !matched[ck] {
			continue
		}
		for _, k := range clusters[ck].keys {
			if _, failed := p.failedRKs[k]; detailed[k] == nil && !failed {
				rest = append(rest, k)
			}
		}
	}
	for k, it := range p.fetchDetailsKeyed(rest) {
		detailed[k] = it
	}
	for _, ck := range order {
		if !matched[ck] {
			continue
		}
		for _, k := range clusters[ck].keys {
			if it := detailed[k]; it != nil && showMatches(it.ShowIDs, b) {
				targets[k] = true
			}
		}
	}
}

// resolveTargeted resolves stored groups containing a targeted item (or one that no longer
// exists) that this run did not produce again — unless they depend on data that failed to load
// or contain an item this run did not examine (e.g. the other library's copy of a cross-library
// group whose targeted copy was deleted: that copy may still be a duplicate, the next full scan
// decides).
func (p *pipeline) resolveTargeted(keys, examined map[refKey]bool) {
	repo := p.s.d.Store.Groups()
	byServer := groupRatingKeys(keys)
	for _, server := range sortedIDs(byServer) {
		gs, err := repo.ListByRatingKeys(p.ctx, server, byServer[server])
		if err != nil {
			p.skipResolution(err)
			return
		}
		for i := range gs {
			g := &gs[i]
			if g.Status == models.GroupIgnored || g.Status == models.GroupResolved ||
				g.LastScanID == p.run.ID || p.protect[g.ID] || p.dependsOnFailure(g) || !allExamined(g, examined) {
				continue
			}
			if p.resolveOne(g.ID) {
				p.afterResolved(g.ID, targetedResolvedReason)
			}
		}
	}
}

// allExamined reports whether every item of g was part of this run.
func allExamined(g *models.DuplicateGroup, examined map[refKey]bool) bool {
	for i := range g.Files {
		v := &g.Files[i].Version
		if !examined[refKey{server: v.ServerID, rk: v.RatingKey}] {
			return false
		}
	}
	return true
}

// dependsOnFailure reports a stored group touching a library or item that failed in this run.
func (p *pipeline) dependsOnFailure(g *models.DuplicateGroup) bool {
	for _, id := range g.LibraryIDs {
		if _, failed := p.failedLibs[id]; failed || p.unsafeLibs[id] {
			return true
		}
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		k := refKey{server: v.ServerID, rk: v.RatingKey}
		if _, failed := p.failedRKs[k]; failed || p.incomplete[k] {
			return true
		}
	}
	return false
}

// resolveOne marks one group resolved after re-checking its stored state.
func (p *pipeline) resolveOne(id int64) bool {
	p.s.groupMu.Lock()
	defer p.s.groupMu.Unlock()
	repo := p.s.d.Store.Groups()
	g, err := repo.Get(p.ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		p.addError()
		p.log.Error("Could not load a group to resolve", "groupId", id, "error", err)
		return false
	}
	if g.Status == models.GroupIgnored || g.Status == models.GroupResolved || g.LastScanID == p.run.ID {
		return false
	}
	if err := repo.UpdateStatus(p.ctx, id, models.GroupResolved, targetedResolvedReason); err != nil {
		p.addError()
		p.log.Error("Could not resolve group", "groupId", id, "error", err)
		return false
	}
	return true
}
