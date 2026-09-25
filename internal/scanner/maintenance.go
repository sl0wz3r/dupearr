package scanner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// maxJoinedErrors bounds the individual errors ReevaluateAll / SyncLibraries report.
const maxJoinedErrors = 5

// ---------------------------------------------------------------------------
// Libraries
// ---------------------------------------------------------------------------

// SyncLibraries refreshes the library list of one server (serverID 0 = all enabled servers).
//
// The server identity is read first: an empty stored machine identifier is filled in; a
// different one is an error (another Plex server answers at the configured URL — its sections
// must not inherit this server's library settings), resolved by re-saving the media server.
// Only movie and show sections are kept; libraries that disappeared from the server are removed
// by the store, user settings (enabled, profile, scope group) of the others are preserved.
func (s *Service) SyncLibraries(ctx context.Context, serverID int64) error {
	var servers []models.MediaServer
	if serverID != 0 {
		srv, err := s.d.Store.MediaServers().Get(ctx, serverID)
		if err != nil {
			return fmt.Errorf("sync libraries: load media server %d: %w", serverID, err)
		}
		servers = append(servers, *srv)
	} else {
		all, err := s.d.Store.MediaServers().List(ctx)
		if err != nil {
			return fmt.Errorf("sync libraries: load media servers: %w", err)
		}
		for _, srv := range all {
			if srv.Enabled {
				servers = append(servers, srv)
			}
		}
	}
	var errs []error
	for _, srv := range servers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.syncServer(ctx, srv); err != nil {
			s.d.Log.Warn("Library sync failed", "server", srv.Name, "serverId", srv.ID, "error", upstreamerr.Message(err))
			errs = append(errs, fmt.Errorf("media server %q: %w", srv.Name, err))
		}
	}
	return joinBounded(errs)
}

// syncServer syncs the libraries of one server.
func (s *Service) syncServer(ctx context.Context, srv models.MediaServer) error {
	if !srv.Kind.Supported() {
		return fmt.Errorf("unsupported media server kind %q", srv.Kind)
	}
	if !s.hasServerFactory() {
		return errors.New("no media server client factory configured")
	}
	client := s.serverClient(srv)
	if client == nil {
		return errors.New("no client available")
	}
	id, err := client.Identity(ctx)
	if err != nil {
		return fmt.Errorf("read server identity: %w", err)
	}
	if id == nil || strings.TrimSpace(id.MachineIdentifier) == "" {
		return errors.New("the server did not report a machine identifier")
	}
	mid := strings.TrimSpace(id.MachineIdentifier)
	switch stored := strings.TrimSpace(srv.MachineIdentifier); {
	case stored == "":
		if err := s.adoptIdentity(ctx, srv, mid); err != nil {
			return err
		}
	case !strings.EqualFold(stored, mid):
		return fmt.Errorf("the server at %s reports machine identifier %q, but %q is configured; re-save the media server to confirm the change",
			srv.Name, mid, stored)
	}
	sections, err := client.Sections(ctx)
	if err != nil {
		return fmt.Errorf("list library sections: %w", err)
	}
	libs := make([]models.Library, 0, len(sections))
	seen := map[string]bool{}
	for _, sec := range sections {
		key := strings.TrimSpace(sec.Key)
		typ := strings.ToLower(strings.TrimSpace(sec.Type))
		if key == "" || seen[key] || mediaTypeOf(typ) == "" {
			continue
		}
		seen[key] = true
		locations := append([]string{}, sec.Locations...)
		libs = append(libs, models.Library{
			ServerID:   srv.ID,
			SectionKey: key,
			Title:      sec.Title,
			Type:       typ,
			Locations:  locations,
		})
	}
	if len(libs) == 0 {
		// Sync deletes the libraries a server no longer reports, and with them their settings
		// (enabled, decision profile, scope group); re-created later they would come back
		// enabled with the default profile. An empty answer is far more likely a server that is
		// starting up or has its storage unmounted than a server without any library.
		known, err := s.d.Store.Libraries().ListByServer(ctx, srv.ID)
		if err != nil {
			return fmt.Errorf("load libraries: %w", err)
		}
		if len(known) > 0 {
			return fmt.Errorf("the server reported no movie or TV libraries; the %d known libraries and their settings were kept (remove them by deleting the media server if this is intended)",
				len(known))
		}
	}
	synced, err := s.d.Store.Libraries().Sync(ctx, srv.ID, libs)
	if err != nil {
		return fmt.Errorf("save libraries: %w", err)
	}
	s.d.Log.Info("Libraries synced", "server", srv.Name, "libraries", len(synced))
	return nil
}

// adoptIdentity stores the machine identifier a server answered with while none is stored (a
// media server force-saved while it was unreachable), so the identity checks of scans, syncs and
// the executor apply to it from now on. The stored row is read again first and only written while
// it still has no identifier and the URL and token the answer came from (an edit made meanwhile
// wins; nothing is stored). A different stored identifier is never replaced. When another
// configured server already has this identifier the connection is a second one to the same Plex
// server — scanned and acted on twice — so an error is returned and nothing is stored.
func (s *Service) adoptIdentity(ctx context.Context, srv models.MediaServer, mid string) error {
	mid = strings.TrimSpace(mid)
	if mid == "" {
		return nil
	}
	repo := s.d.Store.MediaServers()
	all, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("check the server identity: %w", err)
	}
	var cur *models.MediaServer
	for i := range all {
		o := &all[i]
		if o.ID == srv.ID {
			cur = o
			continue
		}
		if strings.EqualFold(strings.TrimSpace(o.MachineIdentifier), mid) {
			return fmt.Errorf("the server at %s's URL is the Plex server already configured as %q; remove one of the two media servers",
				srv.Name, o.Name)
		}
	}
	switch {
	case cur == nil:
		return nil // deleted meanwhile
	case strings.TrimSpace(cur.MachineIdentifier) != "":
		if !strings.EqualFold(strings.TrimSpace(cur.MachineIdentifier), mid) {
			return fmt.Errorf("the server at %s reports machine identifier %q, but %q is configured; re-save the media server to confirm the change",
				srv.Name, mid, cur.MachineIdentifier)
		}
		return nil
	case cur.URL != srv.URL || cur.Token != srv.Token:
		s.d.Log.Debug("Media server changed while its identity was read; not storing it", "server", srv.Name)
		return nil
	}
	cur.MachineIdentifier = mid
	if err := repo.Update(ctx, cur); err != nil {
		return fmt.Errorf("store machine identifier: %w", err)
	}
	s.d.Log.Info("Stored the media server's identity", "server", srv.Name, "machineIdentifier", mid)
	return nil
}

// ---------------------------------------------------------------------------
// Re-evaluation
// ---------------------------------------------------------------------------

// Reevaluate re-runs engine.Evaluate for one group (current profile + overrides), persists,
// publishes. Resolved groups are returned unchanged: their data is stale and must not reopen
// them (the next scan does if the duplicate is back). StableCount restarts at 0 when the
// decisions change (no scan has confirmed them yet); the group's LastScanID is kept.
func (s *Service) Reevaluate(ctx context.Context, groupID int64) (*models.DuplicateGroup, error) {
	cfg, err := s.loadEvalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("re-evaluate group %d: %w", groupID, err)
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	g, err := s.d.Store.Groups().Get(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("re-evaluate group %d: %w", groupID, err)
	}
	if g.Status == models.GroupResolved {
		return g, nil
	}
	related, err := s.d.Store.Groups().ListByRatingKeys(ctx, g.ServerID, groupRatingKeysOf(g))
	if err != nil {
		return nil, fmt.Errorf("re-evaluate group %d: load related groups: %w", groupID, err)
	}
	if err := s.reevaluateLocked(ctx, cfg, newIgnoredIndex(related), g); err != nil {
		return nil, fmt.Errorf("re-evaluate group %d: %w", groupID, err)
	}
	return g, nil
}

// ReevaluateAll re-evaluates every non-resolved group (after profile/settings change). It
// continues past individual failures and reports them together; a cancelled context stops it.
func (s *Service) ReevaluateAll(ctx context.Context) error {
	cfg, err := s.loadEvalConfig(ctx)
	if err != nil {
		return fmt.Errorf("re-evaluate groups: %w", err)
	}
	statuses := make([]models.GroupStatus, 0, len(openStatuses)+1)
	statuses = append(statuses, openStatuses...)
	statuses = append(statuses, models.GroupIgnored)
	groups, err := listGroups(ctx, s.d.Store, store.GroupFilter{}, statuses)
	if err != nil {
		return fmt.Errorf("re-evaluate groups: %w", err)
	}
	ignored := newIgnoredIndex(groups)
	var errs []error
	failed := 0
	for i := range groups {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("re-evaluate groups: %w", err)
		}
		if err := s.reevaluateID(ctx, cfg, ignored, groups[i].ID); err != nil {
			failed++
			s.d.Log.Warn("Re-evaluation failed", "groupId", groups[i].ID, "error", err)
			errs = append(errs, fmt.Errorf("group %d: %w", groups[i].ID, err))
		}
	}
	if failed > 0 {
		return fmt.Errorf("re-evaluate groups: %d of %d failed: %w", failed, len(groups), joinBounded(errs))
	}
	return nil
}

// reevaluateID re-evaluates one group from its current stored state.
func (s *Service) reevaluateID(ctx context.Context, cfg *evalConfig, ignored *ignoredIndex, id int64) error {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	g, err := s.d.Store.Groups().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return err
	}
	if g.Status == models.GroupResolved {
		return nil
	}
	return s.reevaluateLocked(ctx, cfg, ignored, g)
}

// reevaluateLocked evaluates and stores g (groupMu held). Stored overrides are on the files; the
// ignored/queued status is kept by Evaluate; an "incomplete data" review set by the last scan is
// kept until a scan with complete data, and a group overlapping another, ignored group stays in
// review (ignoredIndex.overlap).
func (s *Service) reevaluateLocked(ctx context.Context, cfg *evalConfig, ignored *ignoredIndex, g *models.DuplicateGroup) error {
	prevSig, prevStable := g.Signature, g.StableCount
	wasQueued := g.Status == models.GroupQueued
	var approvedKeepers []string
	if wasQueued {
		approvedKeepers = keptKeys(g)
	}
	incomplete := ""
	if g.Status == models.GroupReview && strings.HasPrefix(g.StatusReason, incompletePrefix) {
		incomplete = g.StatusReason
	}
	if err := engine.Evaluate(g, cfg.profileFor(g), cfg.evalEnv(s.now())); err != nil {
		return fmt.Errorf("evaluate: %w", err)
	}
	if incomplete != "" {
		forceReview(g, incomplete)
		if g.Status == models.GroupReview && crossIncomplete(incomplete) {
			g.StatusReason = incomplete // see crossIncomplete
		}
	} else if reason := cfg.blockedReason(g); reason != "" {
		holdBack(g, reason)
	} else if reason := ignored.overlap(g); reason != "" && g.Status != models.GroupQueued {
		forceReview(g, reason)
	}
	dropStaleApproval(g, approvedKeepers)
	if g.Signature == prevSig && !g.HasFlag(models.FlagWatchUnreadable) {
		g.StableCount = prevStable
	} else {
		// Changed decisions, or decisions ranked on a play history the last scan could not read
		// (a profile change can make the history matter): no scan has confirmed them.
		g.StableCount = 0
	}
	if _, err := s.d.Store.Groups().Upsert(ctx, g); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	s.publish(events.NameDuplicate, events.ActionUpdated, *g)
	if wasQueued && g.Status != models.GroupQueued {
		// The store cancelled (some of) its queued removals: the queue changed.
		s.publish(events.NameQueue, events.ActionSync, struct{}{})
	}
	return nil
}

// ReevaluateMatching re-evaluates, right away, the stored open groups (neither resolved nor
// ignored) for which match reports true: the groups a configuration change affects — an exclusion
// created or deleted, a library disabled or enabled, given another profile or scope group. Their
// status and queued removals follow the change at once (a group the change blocks goes to review,
// which cancels its queued removals, see blockedReason) instead of at the next scan or queue run.
// It returns how many groups were re-evaluated; failures are reported together, a cancelled
// context stops it.
func (s *Service) ReevaluateMatching(ctx context.Context, match func(*models.DuplicateGroup) bool) (int, error) {
	if match == nil {
		return 0, nil
	}
	cfg, err := s.loadEvalConfig(ctx)
	if err != nil {
		return 0, fmt.Errorf("re-evaluate groups: %w", err)
	}
	statuses := make([]models.GroupStatus, 0, len(openStatuses)+1)
	statuses = append(statuses, openStatuses...)
	statuses = append(statuses, models.GroupIgnored)
	groups, err := listGroups(ctx, s.d.Store, store.GroupFilter{}, statuses)
	if err != nil {
		return 0, fmt.Errorf("re-evaluate groups: %w", err)
	}
	ignored := newIgnoredIndex(groups)
	var errs []error
	n := 0
	for i := range groups {
		g := &groups[i]
		if g.Status == models.GroupIgnored || !match(g) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return n, fmt.Errorf("re-evaluate groups: %w", err)
		}
		n++
		if err := s.reevaluateID(ctx, cfg, ignored, g.ID); err != nil {
			s.d.Log.Warn("Re-evaluation failed", "groupId", g.ID, "error", err)
			errs = append(errs, fmt.Errorf("group %d: %w", g.ID, err))
		}
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("re-evaluate groups: %d of %d failed: %w", len(errs), n, joinBounded(errs))
	}
	return n, nil
}

// GroupUsesLibrary reports whether a group has a version in library id (or lists it).
func GroupUsesLibrary(g *models.DuplicateGroup, id int64) bool {
	if slices.Contains(g.LibraryIDs, id) {
		return true
	}
	for i := range g.Files {
		if g.Files[i].Version.LibraryID == id {
			return true
		}
	}
	return false
}

// blockedReason explains why a stored group must not be acted on under the current
// configuration although its data did not change: an exclusion covers it
// (engine.ExclusionReason), a library of its versions is disabled, or its versions come from
// libraries that no longer share the scope group that merged them. Scans never build such groups
// (BuildGroups applies the exclusions, disabled libraries are not scanned and only libraries of
// one scope group are merged); re-evaluations of stored groups hold them in review until a scan
// rebuilds (or resolves) them, or the change is undone. "" when none applies.
func (c *evalConfig) blockedReason(g *models.DuplicateGroup) string {
	if reason := engine.ExclusionReason(c.exclusions, g); reason != "" {
		return "Excluded from duplicate detection (" + reason + "); nothing is removed — the next scan updates this duplicate"
	}
	var ids []int64
	add := func(id int64) {
		if id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range g.LibraryIDs {
		add(id)
	}
	for i := range g.Files {
		add(g.Files[i].Version.LibraryID)
	}
	slices.Sort(ids)
	known := true
	for _, id := range ids {
		l, ok := c.libraries[id]
		if !ok {
			known = false
			continue
		}
		if !l.Enabled {
			return fmt.Sprintf("The library %q is disabled; nothing is removed from it until it is enabled and scanned again", l.Title)
		}
	}
	if known && len(ids) >= 2 {
		scope := normScope(c.libraries[ids[0]].ScopeGroup)
		for _, id := range ids {
			if s := normScope(c.libraries[id].ScopeGroup); s == "" || s != scope {
				return "The libraries of this duplicate no longer share a scope group; nothing is removed — the next scan regroups it"
			}
		}
	}
	return ""
}

// holdBack sends a group that a configuration change blocks (blockedReason) to review — the store
// cancels the queued removals of a group moved to review. Ignored and protected groups (nothing
// to remove) are left alone.
func holdBack(g *models.DuplicateGroup, reason string) {
	switch g.Status {
	case models.GroupPending, models.GroupDeferred, models.GroupQueued, models.GroupFailed, models.GroupReview:
		g.Status, g.StatusReason = models.GroupReview, reason
	}
}

// joinBounded joins at most maxJoinedErrors errors (nil when there are none).
func joinBounded(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) > maxJoinedErrors {
		extra := len(errs) - maxJoinedErrors
		errs = append(errs[:maxJoinedErrors:maxJoinedErrors], fmt.Errorf("and %d more", extra))
	}
	return errors.Join(errs...)
}
