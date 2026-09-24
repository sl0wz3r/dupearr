package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// incompletePrefix starts the StatusReason of a group the scanner sent to review because data
// was missing (*arr unreachable, a version's item could not be loaded). Re-evaluations keep such
// a group in review until the next scan with complete data.
const incompletePrefix = "Incomplete data: "

// openStatuses are the statuses of groups a scan may resolve (everything but ignored/resolved).
var openStatuses = []models.GroupStatus{
	models.GroupPending, models.GroupReview, models.GroupDeferred,
	models.GroupProtected, models.GroupQueued, models.GroupFailed,
}

// groupPageSize is the page size used when walking stored groups.
const groupPageSize = 500

// ---------------------------------------------------------------------------
// Evaluate + persist
// ---------------------------------------------------------------------------

// persistGroups flags queue-busy groups, then evaluates and stores every group.
func (p *pipeline) persistGroups(groups []*models.DuplicateGroup, busy queueState) {
	if len(groups) == 0 {
		p.progress("No duplicates found")
		return
	}
	p.progress(fmt.Sprintf("Evaluating %d duplicate groups", len(groups)))
	p.loadIgnored()
	for _, g := range groups {
		if p.ctx.Err() != nil {
			return
		}
		for i := range g.Files {
			v := &g.Files[i].Version
			if busy.busy(v.Arr) || p.busyItems[refKey{server: v.ServerID, rk: v.RatingKey}] {
				// Before Evaluate, so the group is deferred (docs/DECISIONS.md D3 A4).
				g.Flags = append(g.Flags, models.FlagArrQueueBusy)
				break
			}
		}
		for i := range g.Files {
			v := &g.Files[i].Version
			if p.discNearby[refKey{server: v.ServerID, rk: v.RatingKey}] && !slices.Contains(g.Flags, models.FlagFullDisc) {
				// A TV episode whose folder holds a full-disc backup (never a version in v1): the
				// group is only approved by a person (docs/DECISIONS.md D9).
				g.Flags = append(g.Flags, models.FlagFullDisc)
				break
			}
		}
		p.persistGroup(g)
	}
}

// persistGroup evaluates one group against its stored state (overrides, ignored/queued status,
// stability) and upserts it.
func (p *pipeline) persistGroup(g *models.DuplicateGroup) {
	s := p.s
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	repo := s.d.Store.Groups()

	existing, inh, skip, err := p.lookupStored(g)
	if err != nil {
		p.groupFailed(g, existing, true, fmt.Errorf("load stored group: %w", err))
		return
	}
	if skip {
		return
	}
	carryOver(g, existing)
	inh.apply(g)
	if err := engine.Evaluate(g, p.cfg.profileFor(g), p.cfg.evalEnv(p.start)); err != nil {
		p.groupFailed(g, existing, false, fmt.Errorf("evaluate: %w", err))
		return
	}
	incomplete := p.incompleteReasons(g)
	if len(incomplete) > 0 {
		forceReview(g, incompletePrefix+strings.Join(incomplete, "; ")+" — re-scan once this is fixed")
	} else if reason := p.ignored.overlap(g); reason != "" && g.Status != models.GroupQueued {
		// A queued group was approved by the user after seeing this; the approval stands.
		forceReview(g, reason)
	}
	if existing != nil && existing.Status == models.GroupQueued {
		dropStaleApproval(g, keptKeys(existing))
	}
	g.StableCount = stableCount(existing, g.Signature, p.run.Targeted)
	if len(incomplete) > 0 || g.HasFlag(models.FlagWatchUnreadable) {
		// Decisions made on incomplete data — or ranked on a play history that could not be read —
		// never count as a confirming scan (auto mode).
		g.StableCount = 0
	}
	g.LastScanID = p.run.ID
	g.LastSeenAt = s.now()

	created, err := repo.Upsert(p.ctx, g)
	if err != nil {
		p.groupFailed(g, existing, false, fmt.Errorf("save: %w", err))
		return
	}
	p.countGroup(g)
	if created {
		p.mu.Lock()
		p.run.Stats.NewGroups++
		p.mu.Unlock()
		s.addHistory(p.ctx, detectedEvent(g))
	}
	s.publish(events.NameDuplicate, events.ActionUpdated, *g)
	if p.autoEligible(g) {
		p.autoCands = append(p.autoCands, g)
	}
}

// lookupStored finds the stored state of a freshly built group (groupMu held). Group keys are
// content identities, but the engine appends "@plex:<server>:<ratingKey>" to a key several
// titles share only while they collide, so the same content can come back under another key
// (and a targeted scan, which sees fewer titles, disambiguates differently than a full scan).
// Therefore:
//
//   - a stored open or ignored group of the key that shares no item with g belongs to other
//     content: g is disambiguated instead of overwriting (or silently inheriting the ignored
//     status of) that group;
//   - when the key is new (or only resolved), a stored group of the same content under the other
//     form of the key (with/without the disambiguation) is adopted, keeping the group, its
//     approval and its stability — unless its key is excluded (the group is then skipped);
//   - otherwise the user state of stored groups sharing g's versions is inherited (see
//     inheritance).
//
// err is set when the store could not be read; existing is then the group found so far.
func (p *pipeline) lookupStored(g *models.DuplicateGroup) (existing *models.DuplicateGroup, inh inheritance, skip bool, err error) {
	repo := p.s.d.Store.Groups()
	get := func(key string) (*models.DuplicateGroup, error) {
		e, err := repo.GetByKey(p.ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return e, err
	}
	if existing, err = get(g.Key); err != nil {
		return nil, inh, false, err
	}
	if existing != nil && existing.Status != models.GroupResolved && !sameContent(existing, g) {
		alt := disambiguatedKey(g)
		if alt != g.Key {
			p.log.Info("Another title's group uses this key; the group is keyed by its media server item",
				"key", g.Key, "newKey", alt, "otherGroupId", existing.ID)
			g.Key = alt
			if existing, err = get(g.Key); err != nil {
				return nil, inh, false, err
			}
		}
		if existing != nil && existing.Status != models.GroupResolved && !sameContent(existing, g) {
			// Still another title's group: storing g would overwrite it (and take over its
			// ignored status). Leave both alone; the stored group is not resolved in this run.
			return existing, inh, false, fmt.Errorf("group key %q is used by another title's group (id %d)", g.Key, existing.ID)
		}
	}
	if existing != nil && existing.Status != models.GroupResolved {
		return existing, inh, false, nil
	}
	preds, err := repo.ListByRatingKeys(p.ctx, g.ServerID, groupRatingKeysOf(g))
	if err != nil {
		return existing, inh, false, err
	}
	if adopted := adoptable(g, preds); adopted != nil {
		if p.cfg.keyExcluded(adopted.Key) {
			p.log.Info("Duplicate group skipped: its stored key is excluded", "key", g.Key, "storedKey", adopted.Key)
			return nil, inh, true, nil
		}
		g.Key = adopted.Key
		return adopted, inh, false, nil
	}
	return existing, inherit(g, preds), false, nil
}

// inheritance is the user state a new group takes over from stored groups that contained its
// versions under another key.
type inheritance struct {
	overrides map[string]models.Decision // version key → override
	ignored   *models.DuplicateGroup     // an ignored group with exactly g's versions
}

// inherit collects the inheritance of g from preds (stored groups sharing g's items). Overrides
// are taken by version key ("keep" wins when groups disagree). A group ignored by the user keeps
// being ignored under its new key when it holds exactly the same versions; when the versions
// differ (a new copy appeared), ignoredIndex.overlap sends g to review on every scan instead.
func inherit(g *models.DuplicateGroup, preds []models.DuplicateGroup) inheritance {
	var inh inheritance
	mine := versionKeySet(g)
	for i := range preds {
		pr := &preds[i]
		if pr.Key == g.Key || pr.Status == models.GroupResolved {
			continue
		}
		shared := false
		for _, f := range pr.Files {
			if !mine[f.Version.Key] {
				continue
			}
			shared = true
			if f.Override == "" {
				continue
			}
			if inh.overrides == nil {
				inh.overrides = map[string]models.Decision{}
			}
			if cur, ok := inh.overrides[f.Version.Key]; !ok || f.Override == models.DecisionKeep || cur == "" {
				inh.overrides[f.Version.Key] = f.Override
			}
		}
		if shared && pr.Status == models.GroupIgnored && inh.ignored == nil && sameKeySet(mine, versionKeySet(pr)) {
			inh.ignored = pr
		}
	}
	return inh
}

// ignoredIndex maps version keys to the ignored groups containing them.
type ignoredIndex struct {
	byVersion map[string][]ignoredRef
	err       error // the ignored groups could not be read
}

type ignoredRef struct {
	id    int64
	title string
}

// newIgnoredIndex indexes the ignored groups among gs.
func newIgnoredIndex(gs []models.DuplicateGroup) *ignoredIndex {
	idx := &ignoredIndex{byVersion: map[string][]ignoredRef{}}
	for i := range gs {
		g := &gs[i]
		if g.Status != models.GroupIgnored {
			continue
		}
		for _, f := range g.Files {
			idx.byVersion[f.Version.Key] = append(idx.byVersion[f.Version.Key], ignoredRef{id: g.ID, title: displayTitle(g)})
		}
	}
	return idx
}

// overlap explains why g must be reviewed because the user ignored another group holding some
// of its versions (a group whose key changed, e.g. after the media server re-matched the title,
// or that gained a version): "" when there is no such group. The user's "leave these alone" is
// never overridden automatically.
func (idx *ignoredIndex) overlap(g *models.DuplicateGroup) string {
	if idx == nil || idx.err != nil {
		return "" // an unreadable index is an incomplete-data reason (incompleteReasons)
	}
	for i := range g.Files {
		for _, ref := range idx.byVersion[g.Files[i].Version.Key] {
			if ref.id != g.ID {
				return fmt.Sprintf("Some of these versions belong to %q, which is ignored — review this group before acting on it", ref.title)
			}
		}
	}
	return ""
}

// loadIgnored reads the ignored groups once per scan.
func (p *pipeline) loadIgnored() {
	gs, err := listGroups(p.ctx, p.s.d.Store, store.GroupFilter{}, []models.GroupStatus{models.GroupIgnored})
	if err != nil {
		p.addError()
		p.log.Error("Could not read the ignored duplicate groups; groups are sent to review", "error", err)
		p.ignored = &ignoredIndex{err: err}
		return
	}
	p.ignored = newIgnoredIndex(gs)
}

// apply copies the inherited state onto g (before evaluation; existing overrides win).
func (inh inheritance) apply(g *models.DuplicateGroup) {
	for i := range g.Files {
		f := &g.Files[i]
		if d, ok := inh.overrides[f.Version.Key]; ok && f.Override == "" {
			f.Override = d
		}
	}
	if inh.ignored != nil {
		g.Status, g.StatusReason = models.GroupIgnored, inh.ignored.StatusReason
	}
}

// adoptable returns the stored group of the same content as g under the other form of its key
// (with or without the engine's "@plex:…" disambiguation), or nil when there is none or several.
func adoptable(g *models.DuplicateGroup, preds []models.DuplicateGroup) *models.DuplicateGroup {
	want := stripDisambiguation(g.Key)
	var found *models.DuplicateGroup
	for i := range preds {
		pr := &preds[i]
		if pr.Key == g.Key || pr.Status == models.GroupResolved || stripDisambiguation(pr.Key) != want || !sameContent(pr, g) {
			continue
		}
		if found != nil {
			return nil // ambiguous
		}
		found = pr
	}
	return found
}

// sameContent reports whether a stored group describes the same content as g: same media
// server and at least one shared media-server item.
func sameContent(stored, g *models.DuplicateGroup) bool {
	if stored.ServerID != 0 && g.ServerID != 0 && stored.ServerID != g.ServerID {
		return false
	}
	items := map[refKey]bool{}
	for i := range g.Files {
		v := &g.Files[i].Version
		items[refKey{server: v.ServerID, rk: strings.TrimSpace(v.RatingKey)}] = true
	}
	for i := range stored.Files {
		v := &stored.Files[i].Version
		if items[refKey{server: v.ServerID, rk: strings.TrimSpace(v.RatingKey)}] {
			return true
		}
	}
	return false
}

// splitGroupKey splits a group key into its identity part and its variant suffix ("#ed-…",
// "#3d", "#lang-…", "~2").
func splitGroupKey(key string) (base, suffix string) {
	if i := strings.IndexAny(key, "#~"); i >= 0 {
		return key[:i], key[i:]
	}
	return key, ""
}

// stripDisambiguation removes the engine's "@plex:<server>:<ratingKey>" collision suffix.
func stripDisambiguation(key string) string {
	base, suffix := splitGroupKey(key)
	if i := strings.Index(base, "@plex:"); i >= 0 {
		base = base[:i]
	}
	return base + suffix
}

// disambiguatedKey returns g's key with the engine's collision suffix
// ("<base>@plex:<server>:<ratingKey><variant>", rating key of the primary item: lowest library,
// server, then rating key), or the key unchanged when it already carries one.
func disambiguatedKey(g *models.DuplicateGroup) string {
	base, suffix := splitGroupKey(g.Key)
	if strings.Contains(base, "@plex:") {
		return g.Key
	}
	var primary *models.MediaVersion
	for i := range g.Files {
		v := &g.Files[i].Version
		if strings.TrimSpace(v.RatingKey) == "" {
			continue
		}
		if primary == nil || versionItemLess(v, primary) {
			primary = v
		}
	}
	if primary == nil {
		return g.Key
	}
	return fmt.Sprintf("%s@plex:%d:%s%s", base, primary.ServerID, strings.TrimSpace(primary.RatingKey), suffix)
}

// versionItemLess orders versions by their item like the engine orders a unit's items.
func versionItemLess(a, b *models.MediaVersion) bool {
	if a.LibraryID != b.LibraryID {
		return a.LibraryID < b.LibraryID
	}
	if a.ServerID != b.ServerID {
		return a.ServerID < b.ServerID
	}
	return naturalLess(strings.TrimSpace(a.RatingKey), strings.TrimSpace(b.RatingKey))
}

// naturalLess orders numeric strings numerically and everything else lexically.
func naturalLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	switch {
	case aerr == nil && berr == nil && ai != bi:
		return ai < bi
	case aerr == nil && berr == nil:
		return a < b
	case (aerr == nil) != (berr == nil):
		return aerr == nil
	}
	return a < b
}

// groupRatingKeysOf returns the sorted, distinct rating keys of g's versions on g's server.
func groupRatingKeysOf(g *models.DuplicateGroup) []string {
	seen := map[string]bool{}
	var out []string
	for i := range g.Files {
		v := &g.Files[i].Version
		rk := strings.TrimSpace(v.RatingKey)
		if rk == "" || seen[rk] || (g.ServerID != 0 && v.ServerID != 0 && v.ServerID != g.ServerID) {
			continue
		}
		seen[rk] = true
		out = append(out, rk)
	}
	sort.Strings(out)
	return out
}

// versionKeySet returns the version keys of a group's files.
func versionKeySet(g *models.DuplicateGroup) map[string]bool {
	out := make(map[string]bool, len(g.Files))
	for i := range g.Files {
		out[g.Files[i].Version.Key] = true
	}
	return out
}

// sameKeySet reports whether two key sets are equal.
func sameKeySet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// carryOver copies the user state of the stored group onto a freshly built one: overrides by
// version key, and the ignored/queued status (which Evaluate keeps).
func carryOver(g, existing *models.DuplicateGroup) {
	if existing == nil {
		return
	}
	g.ID = existing.ID
	g.FirstSeenAt = existing.FirstSeenAt
	overrides := map[string]models.Decision{}
	for _, f := range existing.Files {
		if f.Override != "" {
			overrides[f.Version.Key] = f.Override
		}
	}
	for i := range g.Files {
		if d, ok := overrides[g.Files[i].Version.Key]; ok {
			g.Files[i].Override = d
		}
	}
	if existing.Status == models.GroupIgnored || existing.Status == models.GroupQueued {
		g.Status, g.StatusReason = existing.Status, existing.StatusReason
	}
}

// stableCount returns the new StableCount: consecutive full scans that produced the same
// decision signature (docs/DECISIONS.md D4). Targeted scans (webhooks) do not count as a
// confirming scan — two webhooks a minute apart must not make a group "stable" — but a changed
// signature still resets the count. A resolved group that reappears starts over.
func stableCount(existing *models.DuplicateGroup, sig string, targeted bool) int {
	if existing == nil || existing.Status == models.GroupResolved || existing.Signature == "" || existing.Signature != sig {
		return 1
	}
	if targeted {
		return max(existing.StableCount, 1)
	}
	return existing.StableCount + 1
}

// incompleteReasons explains why the data behind g may be incomplete (empty when complete).
func (p *pipeline) incompleteReasons(g *models.DuplicateGroup) []string {
	var out []string
	add := func(reason string) {
		if !slices.Contains(out, reason) {
			out = append(out, reason)
		}
	}
	if p.ignored != nil && p.ignored.err != nil {
		add("could not check the ignored duplicate groups")
	}
	if names := p.arrFailed[g.MediaType]; len(names) > 0 {
		add(fmt.Sprintf("could not read %s %s", strings.Join(names, ", "), arrUnreadableReason))
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		if p.incomplete[refKey{server: v.ServerID, rk: v.RatingKey}] {
			add("some versions of this title could not be loaded from the media server")
			break
		}
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		if reason, ok := p.arrUnlookable[refKey{server: v.ServerID, rk: v.RatingKey}]; ok {
			add(reason)
		}
	}
	for _, id := range g.LibraryIDs {
		if other, ok := p.sharedIncomplete[id]; ok {
			add(fmt.Sprintf("the library %q, whose folders overlap this one, could not be listed (files shared with it cannot be detected)", other))
		}
	}
	for i := range g.Files {
		if reason, ok := p.stale[g.Files[i].Version.Key]; ok {
			add(reason)
		}
	}
	return out
}

// keptKeys returns the version keys a stored group decides to keep.
func keptKeys(g *models.DuplicateGroup) []string {
	var out []string
	for i := range g.Files {
		if g.Files[i].Decision == models.DecisionKeep {
			out = append(out, g.Files[i].Version.Key)
		}
	}
	return out
}

// dropStaleApproval sends a queued group to review when a version its approval kept is no longer
// kept (it left the group, or the fresh decisions remove it): the approved removals relied on
// that copy staying, and the executor would otherwise remove them while keeping a copy nobody
// reviewed. Review cancels the queued removals in the store; the next scan re-derives the status
// (and auto mode needs stable scans again). Groups that are not queued any more are left alone.
func dropStaleApproval(g *models.DuplicateGroup, approvedKeepers []string) {
	if g.Status != models.GroupQueued || len(approvedKeepers) == 0 {
		return
	}
	kept := map[string]bool{}
	for i := range g.Files {
		if g.Files[i].Decision == models.DecisionKeep {
			kept[g.Files[i].Version.Key] = true
		}
	}
	for _, k := range approvedKeepers {
		if !kept[k] {
			g.Status = models.GroupReview
			g.StatusReason = fmt.Sprintf("The approved removals no longer hold: version %s, which the approval kept, is no longer kept; review the duplicate and approve it again", k)
			return
		}
	}
}

// forceReview sends a group that would otherwise be acted on (pending, deferred, or queued —
// whose approved removals were decided on data this scan cannot confirm) to review; the store
// cancels the queued removals of a group moved to review. Protected (nothing to remove) and
// ignored groups are left alone.
func forceReview(g *models.DuplicateGroup, reason string) {
	switch g.Status {
	case models.GroupPending, models.GroupDeferred, models.GroupQueued:
		g.Status, g.StatusReason = models.GroupReview, reason
	}
}

// groupFailed records a group that could not be evaluated or stored. Its stored version (when
// known) must not be resolved in this run; when even the lookup failed, nothing in its
// libraries may be resolved.
func (p *pipeline) groupFailed(g *models.DuplicateGroup, existing *models.DuplicateGroup, lookupFailed bool, err error) {
	p.mu.Lock()
	p.run.Stats.Errors++
	if existing != nil && existing.ID > 0 {
		p.protect[existing.ID] = true
	}
	if lookupFailed {
		for _, id := range g.LibraryIDs {
			p.unsafeLibs[id] = true
		}
	}
	p.mu.Unlock()
	p.log.Error("Could not process duplicate group", "key", g.Key, "error", err)
}

// countGroup adds a stored group to the run statistics.
func (p *pipeline) countGroup(g *models.DuplicateGroup) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := &p.run.Stats
	st.GroupsFound++
	switch g.Status {
	case models.GroupPending:
		st.PendingGroups++
	case models.GroupReview:
		st.ReviewGroups++
	}
	switch g.Status {
	case models.GroupPending, models.GroupReview, models.GroupDeferred, models.GroupQueued, models.GroupFailed:
		st.ReclaimableBytes += g.ReclaimableBytes
	}
}

// removalCount is the number of files an approval would remove.
func removalCount(g *models.DuplicateGroup) int {
	n := 0
	for i := range g.Files {
		if g.Files[i].Decision == models.DecisionRemove && !g.Files[i].Protected {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Auto mode
// ---------------------------------------------------------------------------

// autoEligible reports whether auto mode may approve g: pending, stable for
// settings.StableScansRequired consecutive scans, no review-ish flag, something to remove.
func (p *pipeline) autoEligible(g *models.DuplicateGroup) bool {
	st := p.cfg.settings
	if st.Mode != models.ModeAuto || p.s.d.AutoApprove == nil || g.ID <= 0 || g.Status != models.GroupPending {
		return false
	}
	required := st.StableScansRequired
	if required <= 0 {
		required = models.DefaultSettings().StableScansRequired
	}
	if g.StableCount < required {
		return false
	}
	for _, f := range g.Flags {
		// docs/DECISIONS.md D4: automation only acts on unambiguous groups (the executor
		// re-checks the same flags when it approves).
		if engine.BlocksAutoApproval(f) {
			return false
		}
	}
	if removesDisc(g) {
		return false // docs/DECISIONS.md D9: a disc is only ever removed by a person's approval
	}
	return removalCount(g) > 0
}

// autoApprove approves the eligible groups (by key) within the per-scan budget
// (settings.MaxDeletionsPerRun files and MaxBytesPerRunGB). A limit of 0 or less approves nothing
// — the executor reads it as "nothing may be deleted" too (the API refuses such values; only a
// hand-edited database has them). Groups beyond the budget stay pending for the next scan. Each
// approval names the signature this scan stored, so the executor refuses a group whose decisions
// changed meanwhile.
func (p *pipeline) autoApprove() {
	if len(p.autoCands) == 0 {
		return
	}
	st := p.cfg.settings
	maxFiles, maxGB := st.MaxDeletionsPerRun, st.MaxBytesPerRunGB
	if maxFiles <= 0 || maxGB <= 0 {
		p.log.Warn("Auto approval skipped: a per-run deletion limit is 0 or less, so nothing may be deleted",
			"maxDeletions", maxFiles, "maxGb", maxGB, "eligible", len(p.autoCands))
		return
	}
	const maxWholeGB = (1<<63 - 1) / 1_000_000_000
	maxBytes := int64(1<<63 - 1)
	if int64(maxGB) <= maxWholeGB {
		maxBytes = int64(maxGB) * 1_000_000_000 // decimal GB, like the executor (the stricter reading)
	}
	cands := append([]*models.DuplicateGroup(nil), p.autoCands...)
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Key < cands[j].Key })

	files, skipped := 0, 0
	var bytes int64
	for _, g := range cands {
		if p.ctx.Err() != nil {
			return
		}
		n := removalCount(g)
		if files+n > maxFiles || bytes+g.ReclaimableBytes > maxBytes {
			skipped++
			continue
		}
		if err := p.s.d.AutoApprove(p.ctx, g.ID, models.TriggerScheduled, g.Signature); err != nil {
			if p.ctx.Err() != nil {
				return
			}
			p.addError()
			p.log.Warn("Auto approval failed", "groupId", g.ID, "key", g.Key, "error", err)
			continue
		}
		files += n
		bytes += g.ReclaimableBytes
		p.mu.Lock()
		p.run.Stats.AutoApproved++
		p.run.Stats.PendingGroups--
		p.mu.Unlock()
	}
	if skipped > 0 {
		p.log.Info("Auto approval budget reached; the remaining groups wait for the next scan",
			"skipped", skipped, "maxDeletions", maxFiles, "maxGb", maxGB)
	}
	if p.run.Stats.AutoApproved > 0 {
		p.progress(fmt.Sprintf("Auto-approved %d groups", p.run.Stats.AutoApproved))
	}
}

// ---------------------------------------------------------------------------
// Resolution (full scans)
// ---------------------------------------------------------------------------

// resolveFull marks groups that were not seen again as resolved — only in libraries whose
// listing completed in this run. Stored groups that depend on anything that failed (a library
// listing, an item's details, their own evaluation) are "touched" first (stamped with this run
// id, content unchanged) so the store does not resolve them. When the set of such groups cannot
// be determined, nothing is resolved.
func (p *pipeline) resolveFull() {
	resolveSet := func() map[int64]bool {
		out := map[int64]bool{}
		for id := range p.listed {
			if !p.unsafeLibs[id] {
				out[id] = true
			}
		}
		return out
	}
	if len(resolveSet()) == 0 {
		return
	}
	repo := p.s.d.Store.Groups()
	keep := map[int64]models.DuplicateGroup{}
	add := func(gs []models.DuplicateGroup) {
		for _, g := range gs {
			if g.Status != models.GroupIgnored && g.Status != models.GroupResolved {
				keep[g.ID] = g
			}
		}
	}
	for _, libID := range sortedIDs(p.failedLibs) {
		gs, err := p.listOpenGroups(store.GroupFilter{LibraryID: libID})
		if err != nil {
			p.skipResolution(err)
			return
		}
		add(gs)
	}
	for server, rks := range groupRatingKeys(p.failedRKs) {
		gs, err := repo.ListByRatingKeys(p.ctx, server, rks)
		if err != nil {
			p.skipResolution(err)
			return
		}
		add(gs)
	}
	for _, id := range sortedIDs(p.protect) {
		g, err := repo.Get(p.ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			p.skipResolution(err)
			return
		}
		add([]models.DuplicateGroup{*g})
	}

	set := resolveSet()
	for _, id := range sortedIDs(keep) {
		g := keep[id]
		if g.LastScanID == p.run.ID || !intersectsIDs(g.LibraryIDs, set) {
			continue
		}
		if err := p.touch(id); err != nil {
			p.addError()
			p.log.Error("Could not protect a group from resolution; its libraries are not resolved in this scan",
				"groupId", id, "error", err)
			for _, l := range g.LibraryIDs {
				p.unsafeLibs[l] = true
			}
		}
	}

	libs := sortedIDs(resolveSet())
	if len(libs) == 0 || p.ctx.Err() != nil {
		return
	}
	ids, err := repo.MarkUnseenResolved(p.ctx, p.run.ID, libs)
	if err != nil {
		p.addError()
		p.log.Error("Could not resolve groups that disappeared", "error", err)
		return
	}
	for _, id := range ids {
		p.afterResolved(id, "")
	}
	if len(ids) > 0 {
		p.progress(fmt.Sprintf("%d duplicate groups resolved", len(ids)))
	}
}

// skipResolution logs why no group is resolved in this run.
func (p *pipeline) skipResolution(err error) {
	p.addError()
	p.log.Error("Could not determine which groups depend on failed data; no group is resolved in this scan", "error", err)
}

// touch stamps a stored group with this run's id without changing its content, so
// MarkUnseenResolved leaves it alone.
func (p *pipeline) touch(id int64) error {
	p.s.groupMu.Lock()
	defer p.s.groupMu.Unlock()
	repo := p.s.d.Store.Groups()
	g, err := repo.Get(p.ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if g.LastScanID == p.run.ID || g.Status == models.GroupResolved || g.Status == models.GroupIgnored {
		return nil
	}
	g.LastScanID = p.run.ID
	_, err = repo.Upsert(p.ctx, g)
	return err
}

// afterResolved records a group the run resolved: statistics, history, event.
func (p *pipeline) afterResolved(id int64, reason string) {
	p.mu.Lock()
	p.run.Stats.ResolvedGroups++
	p.mu.Unlock()
	g, err := p.s.d.Store.Groups().Get(p.ctx, id)
	if err != nil {
		p.log.Warn("Resolved group could not be reloaded", "groupId", id, "error", err)
		p.s.publish(events.NameDuplicate, events.ActionUpdated, struct {
			ID     int64              `json:"id"`
			Status models.GroupStatus `json:"status"`
		}{id, models.GroupResolved})
		return
	}
	if reason == "" {
		reason = g.StatusReason
	}
	if reason == "" {
		reason = "No longer detected as a duplicate"
	}
	gid := g.ID
	p.s.addHistory(p.ctx, &models.HistoryEvent{
		EventType: models.EventGroupResolved,
		GroupID:   &gid,
		Title:     displayTitle(g),
		Message:   reason,
		Data:      groupData(g),
	})
	p.s.publish(events.NameDuplicate, events.ActionUpdated, *g)
}

// listOpenGroups returns every stored group matching f whose status is open.
func (p *pipeline) listOpenGroups(f store.GroupFilter) ([]models.DuplicateGroup, error) {
	return listGroups(p.ctx, p.s.d.Store, f, openStatuses)
}

// listGroups walks all pages of groups matching f with one of statuses (ordered by first seen,
// which never changes, so concurrent updates cannot shift the pages).
func listGroups(ctx context.Context, st store.Store, f store.GroupFilter, statuses []models.GroupStatus) ([]models.DuplicateGroup, error) {
	f.Statuses = statuses
	var out []models.DuplicateGroup
	for page := 1; ; page++ {
		pg, err := st.Groups().List(ctx, f, store.Paging{
			Page: page, PageSize: groupPageSize, SortKey: "firstSeenAt", SortDirection: "ascending",
		})
		if err != nil {
			return nil, fmt.Errorf("list groups: %w", err)
		}
		out = append(out, pg.Records...)
		if len(pg.Records) == 0 || len(out) >= pg.TotalRecords {
			return out, nil
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// groupRatingKeys groups item keys by server (rating keys sorted).
func groupRatingKeys[V any](m map[refKey]V) map[int64][]string {
	out := map[int64][]string{}
	for k := range m {
		out[k.server] = append(out[k.server], k.rk)
	}
	for _, rks := range out {
		sort.Strings(rks)
	}
	return out
}

// sortedIDs returns the keys of an id-keyed map in ascending order.
func sortedIDs[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// intersectsIDs reports whether any id is in set.
func intersectsIDs(ids []int64, set map[int64]bool) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}

// displayTitle is the human title of a group ("Movie (2020)", "Show - S01E02 - Episode").
func displayTitle(g *models.DuplicateGroup) string {
	switch {
	case g.MediaType == models.MediaTypeEpisode && g.ShowTitle != "":
		s := g.ShowTitle + " - " + engine.EpisodeLabel(g.Season, g.Episode)
		if g.Title != "" {
			s += " - " + g.Title
		}
		return s
	case g.Title != "" && g.Year > 0:
		return fmt.Sprintf("%s (%d)", g.Title, g.Year)
	case g.Title != "":
		return g.Title
	}
	return g.Key
}

// groupData is the JSON payload of group history events.
func groupData(g *models.DuplicateGroup) json.RawMessage {
	data, err := json.Marshal(struct {
		Key              string             `json:"key"`
		Status           models.GroupStatus `json:"status"`
		Files            int                `json:"fileCount"`
		ReclaimableBytes int64              `json:"reclaimableBytes"`
		LibraryIDs       []int64            `json:"libraryIds"`
	}{g.Key, g.Status, len(g.Files), g.ReclaimableBytes, append([]int64{}, g.LibraryIDs...)})
	if err != nil {
		return nil
	}
	return data
}

// detectedEvent is the history event of a newly detected group.
func detectedEvent(g *models.DuplicateGroup) *models.HistoryEvent {
	id := g.ID
	msg := fmt.Sprintf("Duplicate detected: %d versions, status %s", len(g.Files), g.Status)
	if g.StatusReason != "" {
		msg += " (" + g.StatusReason + ")"
	}
	return &models.HistoryEvent{
		EventType: models.EventGroupDetected,
		GroupID:   &id,
		Title:     displayTitle(g),
		Message:   msg,
		Data:      groupData(g),
	}
}
