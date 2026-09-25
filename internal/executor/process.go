package executor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	// maxConsecutiveFailures aborts a run after this many failures in a row (docs/DECISIONS.md D6).
	maxConsecutiveFailures = 3
	// bytesPerGB converts settings.MaxBytesPerRunGB (decimal GB: the stricter reading).
	bytesPerGB = 1_000_000_000
)

// interruptedMessage is recorded on removals a crash or restart left "running", and as the status
// reason of their groups (see RecoverInterrupted).
const interruptedMessage = "Interrupted by a restart while running — verify the files, then re-approve"

// run is the state of one ProcessQueue invocation. It is used by a single goroutine.
type run struct {
	s        *Service
	ctx      context.Context
	st       models.Settings
	progress func(string)

	servers  map[int64]models.MediaServer
	arrs     map[int64]models.ArrInstance
	mapper   *pathmap.Mapper
	mappings []models.PathMapping
	roots    []string // resolved local mapping roots (confinement)
	libDirs  []string // local folders of the Plex libraries (the recycle bin must not be one)
	// serverLibDirs are the local library folders per media server: the filesystem method only
	// removes a part inside one of its own server's (confinement beyond the mapping roots).
	serverLibDirs map[int64][]string
	// libraries (by id) and exclusions as they are now: a library disabled or an exclusion added
	// after an approval stops its removals.
	libraries  map[int64]models.Library
	exclusions []models.Exclusion
	// profiles (by id) and the default profile: a protection added after an approval wins.
	profiles       map[int64]models.Profile
	defaultProfile models.Profile
	// keptInRun maps the version keys kept by groups that removed files in this run to the
	// group's label: a later (stale, overlapping) group must not remove them.
	keptInRun map[string]string

	serverClients map[int64]MediaServerClient
	arrClients    map[int64]ArrClient
	deletionOK    map[int64]cachedBool   // Plex "Allow media deletion" per server
	arrBins       map[int64]cachedString // *arr recycle bin per instance
	bin           *cachedString          // validated local recycle bin
	ids           *fileid.Prober         // file identities (several media servers; fileIDs)
	// rescans collects, with several media servers, the re-scans of skipped groups (server →
	// rating keys): one targeted scan per server at the end of the run (flushRescans), since each
	// one lists every movie and TV library of the other servers.
	rescans map[int64][]string

	maxCount    int
	maxBytes    int64
	attempts    int   // removals started (or simulated) in this run
	bytes       int64 // bytes removed (or simulated) in this run
	consecutive int   // consecutive failures

	stopped    string // a limit was reached: no new removals, the rest stays queued
	aborted    string // a safety breaker stopped the run
	abortCause error

	sum Summary
}

type cachedBool struct {
	v   bool
	err error
}

type cachedString struct {
	v   string
	err error
}

// target is one queued removal and the group file it removes.
type target struct {
	a    *models.Action
	file *models.GroupFile
}

// outcome is the result of one started removal.
type outcome struct {
	t      *target
	status models.ActionStatus
	choice *methodChoice
	stale  string // non-empty: the data was stale, the group goes to review
	// noRescan: the stale data is not fixed by a re-scan (the media server's identity changed).
	noRescan bool
}

// ProcessQueue executes pending actions (docs/ARCHITECTURE.md §6) respecting dry run,
// maxDeletionsPerRun and the 3-consecutive-failures breaker.
//
// Runs are serialized. Per group, every involved Plex item is re-fetched and every version
// re-verified (including the minimum age) before anything is removed, and the method of every
// removal is chosen before the first one runs, so a group is either removed as approved or left
// untouched; removals of versions no *arr tracks run first and the *arr-tracked ones last
// (docs/DECISIONS.md D3). Group statuses changed meanwhile by someone else (ignored, cancelled,
// resolved) are kept. New removals stop when the per-run count or byte
// limit is reached, after 3 consecutive failures, when an *arr reports a missing root folder
// (the run is aborted: returned error wraps ErrAborted) and when ctx is cancelled; whatever was not
// started stays queued. A limit of 0 or less is never "unlimited": nothing is removed and the
// summary says why. Dry run (settings or action) verifies and selects a method but changes
// nothing outside the database.
//
// Removals a previous process left "running" are failed first (see RecoverInterrupted; main calls
// it at startup, this is the safety net).
func (s *Service) ProcessQueue(ctx context.Context, progress func(string)) (Summary, error) {
	if s.d.Store == nil {
		return Summary{}, errors.New("process queue: executor has no store")
	}
	if err := s.lock(ctx); err != nil {
		return Summary{}, err
	}
	defer s.unlock()

	r, err := s.newRun(ctx, progress)
	if err != nil {
		return Summary{}, err
	}
	if _, err := s.recoverInterrupted(ctx); err != nil {
		s.d.Log.Error("Could not recover removals interrupted by a restart", "error", err)
	}

	pending, err := s.d.Store.Actions().ListPending(ctx, 0)
	if err != nil {
		return Summary{}, fmt.Errorf("process queue: %w", err)
	}
	if len(pending) == 0 {
		r.sum.Message = "No queued removals"
		return r.sum, nil
	}
	if r.stopped != "" {
		s.d.Log.Warn("Queued removals are not processed", "reason", r.stopped, "queued", len(pending))
	}
	r.report("Processing %d queued removal(s)%s", len(pending), dryRunSuffix(r.st.DryRun))
	for _, b := range batchByGroup(pending) {
		if r.halted() || !r.budgetAllows(0) {
			break
		}
		r.processGroup(b.groupID, b.actions)
	}
	return r.finish()
}

// newRun snapshots settings and connections for one run.
func (s *Service) newRun(ctx context.Context, progress func(string)) (*run, error) {
	st, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load settings: %w", err)
	}
	servers, err := s.d.Store.MediaServers().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load media servers: %w", err)
	}
	arrs, err := s.d.Store.ArrInstances().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load *arr instances: %w", err)
	}
	mappings, err := s.d.Store.PathMappings().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load path mappings: %w", err)
	}
	libs, err := s.d.Store.Libraries().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load libraries: %w", err)
	}
	exclusions, err := s.d.Store.Exclusions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load exclusions: %w", err)
	}
	profiles, err := s.d.Store.Profiles().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("process queue: load profiles: %w", err)
	}
	mapper := pathmap.New(mappings)
	r := &run{
		s:             s,
		ctx:           ctx,
		st:            st,
		progress:      progress,
		servers:       make(map[int64]models.MediaServer, len(servers)),
		arrs:          make(map[int64]models.ArrInstance, len(arrs)),
		mapper:        mapper,
		mappings:      mappings,
		roots:         resolvedRoots(mappings),
		libDirs:       libraryFolders(libs, mapper),
		serverLibDirs: serverLibraryFolders(libs, mapper),
		libraries:     make(map[int64]models.Library, len(libs)),
		exclusions:    exclusions,
		keptInRun:     map[string]string{},
		serverClients: map[int64]MediaServerClient{},
		arrClients:    map[int64]ArrClient{},
		deletionOK:    map[int64]cachedBool{},
		arrBins:       map[int64]cachedString{},
		maxCount:      st.MaxDeletionsPerRun,
		maxBytes:      gbToBytes(st.MaxBytesPerRunGB),
	}
	for _, m := range servers {
		r.servers[m.ID] = m
	}
	for _, l := range libs {
		r.libraries[l.ID] = l
	}
	r.profiles = make(map[int64]models.Profile, len(profiles))
	foundDefault := false
	for _, p := range profiles {
		r.profiles[p.ID] = p
		if p.IsDefault && !foundDefault {
			r.defaultProfile, foundDefault = p, true
		}
	}
	if !foundDefault {
		// Like the scanner: without a stored default the built-in template (with its keep-tag
		// protection) decides.
		if tpl := engine.ProfileTemplates(); len(tpl) > 0 {
			r.defaultProfile = tpl[0]
		}
	}
	for _, a := range arrs {
		r.arrs[a.ID] = a
	}
	// The breakers can never be switched off: a limit of 0 or less (a hand-edited or restored
	// database; the API refuses it) means nothing may be deleted, never "unlimited".
	switch {
	case st.MaxDeletionsPerRun <= 0:
		r.stopped = fmt.Sprintf("the limit of deletions per run is %d, so nothing may be deleted; set it to at least 1 (Settings → Media Management)", st.MaxDeletionsPerRun)
	case st.MaxBytesPerRunGB <= 0:
		r.stopped = fmt.Sprintf("the limit of GB per run is %d, so nothing may be deleted; set it to at least 1 (Settings → Media Management)", st.MaxBytesPerRunGB)
	}
	return r, nil
}

// gbToBytes converts the per-run GB limit (decimal GB), saturating instead of overflowing.
func gbToBytes(gb int) int64 {
	if gb <= 0 {
		return 0
	}
	if int64(gb) > math.MaxInt64/bytesPerGB {
		return math.MaxInt64
	}
	return int64(gb) * bytesPerGB
}

// setRunStatus records the status a queue run decided for a group. The group may have changed
// while the run was verifying or removing (the user ignored or cancelled it, a scan resolved it or
// sent it to review): such later decisions win, so the status is only written while the group is
// still "queued", or already has the decided status — one atomic compare-and-set in the store.
// Reports whether it was written.
func (s *Service) setRunStatus(ctx context.Context, groupID int64, status models.GroupStatus, reason string) bool {
	ctx = context.WithoutCancel(ctx)
	ok, err := s.d.Store.Groups().UpdateStatusIf(ctx, groupID, []models.GroupStatus{models.GroupQueued, status}, status, reason)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false
	case err != nil:
		s.d.Log.Error("Could not update a group's status", "group", groupID, "status", status, "error", err)
		return false
	case !ok:
		s.d.Log.Info("The group changed while the queue ran; keeping its new status", "group", groupID, "runStatus", status)
	}
	return ok
}

type groupBatch struct {
	groupID int64
	actions []models.Action
}

// batchByGroup groups pending actions by group, in the order the groups first appear (oldest
// first).
func batchByGroup(pending []models.Action) []groupBatch {
	idx := map[int64]int{}
	var out []groupBatch
	for _, a := range pending {
		i, ok := idx[a.GroupID]
		if !ok {
			i = len(out)
			idx[a.GroupID] = i
			out = append(out, groupBatch{groupID: a.GroupID})
		}
		out[i].actions = append(out[i].actions, a)
	}
	return out
}

// processGroup re-verifies one group and executes its queued removals.
func (r *run) processGroup(groupID int64, actions []models.Action) {
	ctx := r.ctx
	g, err := r.s.d.Store.Groups().Get(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		r.cancelActions(actions, "Cancelled: the duplicate group no longer exists")
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.s.d.Log.Error("Could not load a duplicate group", "group", groupID, "error", err)
		r.noteFailure()
		return
	}
	// Versions carry their media server; older rows may not (the group's server then applies).
	for i := range g.Files {
		if g.Files[i].Version.ServerID == 0 {
			g.Files[i].Version.ServerID = g.ServerID
		}
	}
	title := displayTitle(g)
	switch g.Status {
	case models.GroupQueued:
	case models.GroupFailed:
		r.cancelActions(actions, "Cancelled: another removal of this group failed; approve the group again to retry")
		r.s.publishGroup(ctx, g.ID)
		return
	default:
		r.cancelActions(actions, fmt.Sprintf("Cancelled: the group is no longer queued (status %q)", g.Status))
		r.s.publishGroup(ctx, g.ID)
		return
	}
	r.report("Verifying %s", title)

	// Every queued removal must still target a file of the group decided "remove".
	targets := make([]*target, 0, len(actions))
	var problems []string
	for i := range actions {
		a := &actions[i]
		f := findTargetFile(g, a)
		switch {
		case f == nil:
			problems = append(problems, fmt.Sprintf("the version to remove (%s) is no longer part of the group", a.VersionKey))
		case f.Decision != models.DecisionRemove || f.Protected:
			problems = append(problems, fmt.Sprintf("the version %s is no longer marked for removal", describeVersion(&f.Version)))
		case !samePathList(a.Paths, partPaths(&f.Version)):
			problems = append(problems, fmt.Sprintf("the files of %s changed since the group was approved", describeVersion(&f.Version)))
		case f.Version.Disc != nil && a.Size != f.Version.TotalSize():
			// A scan after the approval re-measured the disc (GAP-03): the approval covered the
			// disc as it was then, not what it holds now.
			problems = append(problems, fmt.Sprintf("the full disc %s changed since the group was approved (%d bytes then, %d now)",
				describeVersion(&f.Version), a.Size, f.Version.TotalSize()))
		case isShared(&f.Version):
			problems = append(problems, fmt.Sprintf("%s is a multi-episode file shared with other episodes", describeVersion(&f.Version)))
		case hasShortcut(&f.Version):
			problems = append(problems, fmt.Sprintf("a .strm shortcut points to %s", describeVersion(&f.Version)))
		case isOptimized(&f.Version):
			problems = append(problems, fmt.Sprintf("%s is a Plex optimized version", describeVersion(&f.Version)))
		case r.discMember(&f.Version) != "":
			problems = append(problems, r.discMemberProblem(&f.Version))
		}
		targets = append(targets, &target{a: a, file: f})
	}
	if err := engine.ValidateDecisions(g); err != nil {
		problems = append(problems, err.Error())
	}
	// An exclusion added or a library disabled after the approval wins over it.
	if reason := r.notAllowedNow(g, targets); reason != "" {
		r.skipGroup(g, targets, "Not removed: "+reason, true)
		return
	}
	// Several media servers (M25): the scan must have compared the group with every server.
	if reason := r.crossServerRecordProblem(g); reason != "" {
		r.skipGroup(g, targets, "Not removed: "+reason, true)
		return
	}
	if len(problems) == 0 {
		switch conflict, err := r.keptElsewhere(g, targets); {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			r.deferGroup(g, fmt.Sprintf("could not check the other duplicate groups (%v)", err), true, false)
			return
		case conflict != "":
			problems = append(problems, conflict)
		}
	}
	if len(problems) > 0 {
		r.skipGroup(g, targets, "Stale or unsafe decisions: "+strings.Join(problems, "; "), true)
		return
	}
	// docs/DECISIONS.md D3: versions no *arr tracks first, *arr-tracked ones last.
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].file.Version.Arr == nil && targets[j].file.Version.Arr != nil
	})

	byServer := involvedRatingKeys(g)
	for _, sid := range sortedKeys(byServer) {
		if _, reason := r.serverClient(sid); reason != "" {
			r.skipGroup(g, targets, "Cannot re-verify the group: "+reason, false)
			return
		}
	}
	// Every involved media server must still be the server the group was scanned from: after its
	// URL was pointed at another server, the stored rating keys and media ids name unrelated items.
	for _, sid := range sortedKeys(byServer) {
		c, _ := r.serverClient(sid)
		problem, err := r.serverIdentityProblem(ctx, sid, c)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			r.deferGroup(g, fmt.Sprintf("could not confirm the identity of %s (%v)", r.servers[sid].Name, err), true, false)
			return
		case problem != "":
			r.skipGroup(g, targets, problem, false)
			return
		}
	}
	// Read-only media servers (Jellyfin, readonly.go): stored identity, removal gate, mappings.
	if ro, _ := readOnlyGroup(g); ro {
		problem, rescan, wait, failure := r.readOnlyRunProblem(g)
		switch {
		case problem != "":
			r.skipGroup(g, targets, "Not removed: "+problem, rescan)
			return
		case wait != "":
			if ctx.Err() != nil {
				return
			}
			r.deferGroup(g, wait, failure, false)
			return
		}
	}

	// F10: never remove anything while a version of the group is playing.
	for _, sid := range sortedKeys(byServer) {
		c, _ := r.serverClient(sid)
		sessions, err := c.ActiveSessions(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.deferGroup(g, fmt.Sprintf("could not check whether a version is playing on %s (%v)", r.servers[sid].Name, err), true, false)
			return
		}
		for _, id := range playingKeys(g, sid) {
			if sessions[id] {
				r.deferGroup(g, "a version is currently playing", false, true)
				return
			}
		}
	}

	// Fresh state of every involved item (checkFiles).
	fresh := map[itemRef]*models.MediaItem{}
	for _, sid := range sortedKeys(byServer) {
		c, _ := r.serverClient(sid)
		for _, rk := range byServer[sid] {
			it, err := c.Item(ctx, rk)
			switch {
			case err == nil && it != nil:
				fresh[itemRef{sid, rk}] = it
			case err == nil || errors.Is(err, mediaserver.ErrNotFound):
				fresh[itemRef{sid, rk}] = nil // the item is gone
			case ctx.Err() != nil:
				return
			default:
				r.deferGroup(g, fmt.Sprintf("could not re-check item %s with %s (%v)", rk, r.servers[sid].Name, err), true, false)
				return
			}
		}
	}
	vr := r.verify(g, targets, fresh)
	if vr.problem != "" {
		r.skipGroup(g, targets, vr.problem, true)
		return
	}
	if vr.wait != "" {
		r.deferGroup(g, vr.wait, false, false)
		return
	}
	// Several media servers: the other servers that list a file to remove, and their libraries.
	if r.multiServer() {
		cc := r.otherServerProblem(g, targets, vr)
		switch {
		case cc.problem != "":
			r.skipGroupRescan(g, targets, "Not removed: "+cc.problem, cc.rescan)
			return
		case cc.wait != "":
			if ctx.Err() != nil {
				return
			}
			r.deferGroup(g, cc.wait, cc.failure, cc.playing)
			return
		}
	}

	stamps := map[time.Time]bool{}
	for _, t := range targets {
		stamps[t.a.CreatedAt] = true
	}

	// Choose the method of every removal before the first one runs: a group is removed as
	// approved or not touched at all (a removal without a usable method must not leave the group
	// half processed, e.g. the untracked copies gone but the *arr-tracked one still there).
	plans := make(map[int64]*methodChoice, len(targets))
	for _, t := range targets {
		choice, reasons := r.selectMethod(g, t, vr)
		if choice == nil {
			if ctx.Err() != nil {
				return // cancelled: everything stays queued
			}
			r.refuseGroup(g, targets, t, "No deletion method is available: "+strings.Join(reasons, "; "), stamps)
			return
		}
		if choice.arr != nil {
			// The *arr file id must still name this version before anything of the group is
			// touched (checked again right before DeleteFile).
			stale, err := r.arrFileProblem(ctx, choice.arr, &t.file.Version, vr.losers[t.a.ID])
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return
				}
				r.deferGroup(g, fmt.Sprintf("could not re-check file %d with %s (%v)", choice.arr.info.FileID, choice.arr.inst.Name, err), true, false)
				return
			case stale != "":
				r.skipGroup(g, targets, "Stale *arr data: "+stale, true)
				return
			}
		}
		plans[t.a.ID] = choice
	}
	// The *arr files of the kept versions must still be the kept files: when an *arr now tracks
	// another copy (it imported the loser, or upgraded the keeper), removing the "untracked" loser
	// would delete the *arr's file behind its back.
	switch stale, err := r.keeperArrProblem(ctx, vr); {
	case err != nil:
		if ctx.Err() != nil {
			return
		}
		r.deferGroup(g, fmt.Sprintf("could not re-check the kept version with its *arr (%v)", err), true, false)
		return
	case stale != "":
		r.skipGroup(g, targets, "Stale *arr data of the kept version: "+stale, true)
		return
	}

	var (
		done     []*outcome
		stale    string
		noRescan bool
		fail     bool
	)
	for _, t := range targets {
		if r.halted() || !r.budgetAllows(t.a.Size) {
			break
		}
		oc := r.execute(g, t, vr, plans[t.a.ID])
		if oc == nil {
			continue
		}
		done = append(done, oc)
		if oc.stale != "" {
			stale, noRescan = oc.stale, oc.noRescan
			break
		}
		if oc.status == models.ActionFailed {
			fail = true
			break
		}
	}
	rest := notStarted(targets, done)
	if r.anyRemoved(done) {
		r.postActions(g, done, vr)
		for _, k := range vr.keepers {
			r.keptInRun[k.file.Version.Key] = fmt.Sprintf("%s, #%d", title, g.ID)
			// The other servers' listings of a kept file are that file too (docs/DECISIONS.md D11).
			for _, e := range k.file.Version.OtherServers {
				r.keptInRun[e.VersionKey] = fmt.Sprintf("%s, #%d", title, g.ID)
			}
		}
	}
	switch {
	case stale != "":
		r.skipGroup(g, rest, stale, !noRescan)
		return
	case fail:
		// The group is marked failed before its other removals are cancelled: cancelling the last
		// queued removal of a "queued" group makes the store reopen it as "pending".
		r.s.setRunStatus(ctx, g.ID, models.GroupFailed, "A removal failed; the other removals of this group were not attempted")
		r.cancelActions(actionsOf(rest), "Not attempted: another removal of this group failed; approve the group again to retry")
	}
	r.finalizeGroup(g, stamps)
}

// keptElsewhere reports a version to remove that another live duplicate group (not resolved or
// ignored) decides to keep. A version belongs to one group per scan, but when content is regrouped
// (e.g. a targeted scan files it under a new key) an older group can linger until the next full
// scan; acting on its approval could remove the newer group's keeper.
func (r *run) keptElsewhere(g *models.DuplicateGroup, targets []*target) (string, error) {
	// Another server's listing of a file to remove is the same file (or may be): its groups'
	// decisions count too (docs/DECISIONS.md D11).
	if conflict, err := r.crossKept(targets); conflict != "" || err != nil {
		return conflict, err
	}
	byServer := map[int64][]string{}
	removing := map[string]bool{}
	for _, t := range targets {
		v := &t.file.Version
		// A group that already removed files in this run kept this version: it may be resolved
		// by now (and so skipped below), but its removals relied on this copy staying.
		if label, ok := r.keptInRun[v.Key]; ok {
			return fmt.Sprintf("the version %s was kept by another duplicate group (%s) whose copies were removed in this run",
				describeVersion(v), label), nil
		}
		removing[v.Key] = true
		sid, rk := serverOf(g, v), strings.TrimSpace(v.RatingKey)
		if rk != "" && !slices.Contains(byServer[sid], rk) {
			byServer[sid] = append(byServer[sid], rk)
		}
	}
	for _, sid := range sortedKeys(byServer) {
		others, err := r.s.d.Store.Groups().ListByRatingKeys(r.ctx, sid, byServer[sid])
		if err != nil {
			return "", err
		}
		for i := range others {
			o := &others[i]
			if o.ID == g.ID || o.Status == models.GroupResolved || o.Status == models.GroupIgnored {
				continue
			}
			for j := range o.Files {
				if f := &o.Files[j]; removing[f.Version.Key] && f.Decision != models.DecisionRemove {
					return fmt.Sprintf("the version %s is kept by another duplicate group (%s, #%d)",
						describeVersion(&f.Version), displayTitle(o), o.ID), nil
				}
			}
		}
	}
	return "", nil
}

// refuseGroup fails the removal that cannot be done (msg says why) and cancels the other queued
// removals of the group, before anything of the group was removed.
func (r *run) refuseGroup(g *models.DuplicateGroup, targets []*target, bad *target, msg string, stamps map[time.Time]bool) {
	r.finishAction(bad.a, models.ActionFailed, msg)
	r.noteFailure()
	r.s.setRunStatus(r.ctx, g.ID, models.GroupFailed, msg)
	var others []models.Action
	for _, t := range targets {
		if t != bad {
			others = append(others, *t.a)
		}
	}
	r.cancelActions(others, "Not attempted: another removal of this group cannot be done ("+describeVersion(&bad.file.Version)+"); fix the cause and approve the group again")
	r.finalizeGroup(g, stamps)
}

// execute runs (or simulates) one removal with the chosen method. It returns nil when the action
// could not be started (it was cancelled or its version stopped being marked for removal in the
// meantime).
func (r *run) execute(g *models.DuplicateGroup, t *target, vr *verification, choice *methodChoice) *outcome {
	ctx := r.ctx
	a := t.a
	// Dry run switched on while the run is going applies from the next removal on.
	if !r.st.DryRun && !a.DryRun {
		switch st, err := r.s.d.Store.Settings().Get(ctx); {
		case err != nil:
			if ctx.Err() == nil {
				r.stopped = fmt.Sprintf("the settings could not be read again before a removal (%v)", err)
			}
			return nil
		case st.DryRun:
			r.st.DryRun = true
			r.s.d.Log.Warn("Dry run was switched on during a queue run; the remaining removals are only simulated")
		}
	}
	start := r.s.now().UTC()
	a.Status = models.ActionRunning
	a.StartedAt = &start
	if err := r.s.d.Store.Actions().Update(ctx, a); err != nil {
		r.s.d.Log.Info("A queued removal can no longer run; skipping it", "action", a.ID, "group", a.GroupID, "reason", err)
		return nil
	}
	r.s.publish(events.NameQueue, events.ActionUpdated, *a)

	oc := &outcome{t: t, choice: choice}
	a.Method, a.Permanent = choice.method, choice.permanent
	r.attempts++

	if r.st.DryRun || a.DryRun {
		msg := fmt.Sprintf("Would delete via %s (%s): %s", choice.method, choice.target, joinLimited(a.Paths, 5))
		if dp := choice.disc; dp != nil {
			msg = fmt.Sprintf("Would move the full disc (%d files, %s) to the %s via filesystem: %s", dp.files, humanBytes(dp.bytes),
				choice.target, joinLimited(discEntryPaths(dp), 5))
		}
		if choice.permanent {
			msg += " [permanent]"
		}
		oc.status = models.ActionDryRun
		r.bytes += a.Size
		r.consecutive = 0
		r.finishAction(a, models.ActionDryRun, msg)
		return oc
	}

	r.report("Deleting %s via %s (%s)", joinLimited(a.Paths, 3), choice.method, choice.target)
	res := r.perform(t, choice, vr)
	switch {
	case res.stale != "":
		oc.status, oc.stale, oc.noRescan = models.ActionSkipped, res.stale, res.noRescan
		r.finishAction(a, models.ActionSkipped, "Skipped: "+res.stale)
	case res.err != nil:
		oc.status = models.ActionFailed
		a.RecyclePath = res.recyclePath
		r.finishAction(a, models.ActionFailed, res.err.Error())
		if res.abort {
			r.abort(res.err.Error(), res.cause)
		}
		r.noteFailure()
	default:
		oc.status = models.ActionSucceeded
		a.RecyclePath = strings.Join(res.recyclePaths, "\n")
		r.bytes += a.Size
		if choice.disc != nil {
			r.sum.BytesFreed += choice.disc.freed // hardlinked disc files free nothing
		} else {
			r.sum.BytesFreed += a.Size
		}
		r.consecutive = 0
		r.finishAction(a, models.ActionSucceeded, res.message)
		r.s.d.Log.Info("Removed a duplicate", "group", g.ID, "action", a.ID, "method", a.Method,
			"target", choice.target, "paths", a.Paths, "permanent", a.Permanent)
	}
	return oc
}

// finishAction records the final status of an action. Bookkeeping must survive a cancelled run
// context: the removal already happened (or definitely did not).
func (r *run) finishAction(a *models.Action, status models.ActionStatus, message string) {
	ctx := context.WithoutCancel(r.ctx)
	now := r.s.now().UTC()
	a.Status, a.Message, a.FinishedAt = status, message, &now
	if err := r.s.d.Store.Actions().Update(ctx, a); err != nil {
		r.s.d.Log.Error("Could not record the result of a removal", "action", a.ID, "status", status, "error", err)
	}
	r.sum.Processed++
	switch status {
	case models.ActionSucceeded:
		r.sum.Succeeded++
	case models.ActionDryRun:
		r.sum.DryRun++
	case models.ActionSkipped:
		r.sum.Skipped++
	case models.ActionFailed:
		r.sum.Failed++
	}
	r.s.recordAction(ctx, a)
	r.s.publish(events.NameQueue, events.ActionUpdated, *a)
	if status == models.ActionFailed {
		r.s.d.Log.Warn("A duplicate removal failed", "action", a.ID, "group", a.GroupID, "message", message)
		r.report("Failed: %s", message)
	} else {
		r.report("%s", message)
	}
}

// appendNote adds a post-processing note to a finished action's message.
func (r *run) appendNote(a *models.Action, note string) {
	if strings.TrimSpace(note) == "" {
		return
	}
	a.Message = strings.TrimSpace(a.Message + " | " + note)
	if err := r.s.d.Store.Actions().Update(context.WithoutCancel(r.ctx), a); err != nil {
		r.s.d.Log.Error("Could not update a removal's message", "action", a.ID, "error", err)
		return
	}
	r.s.publish(events.NameQueue, events.ActionUpdated, *a)
}

// cancelActions cancels queued actions that must not run (their group changed).
func (r *run) cancelActions(actions []models.Action, message string) {
	r.s.cancelActions(r.ctx, actions, message)
}

// cancelActions cancels the given actions that are still pending (the store refuses to cancel
// one that started or finished meanwhile).
func (s *Service) cancelActions(ctx context.Context, actions []models.Action, message string) {
	ctx = context.WithoutCancel(ctx)
	for i := range actions {
		a := &actions[i]
		if a.Status != models.ActionPending {
			continue
		}
		now := s.now().UTC()
		a.Status, a.Message, a.FinishedAt = models.ActionCancelled, message, &now
		if err := s.d.Store.Actions().Update(ctx, a); err != nil {
			s.d.Log.Info("Could not cancel a queued removal", "action", a.ID, "error", err)
			continue
		}
		s.publish(events.NameQueue, events.ActionUpdated, *a)
	}
}

// skipGroup marks the given not-yet-started removals skipped, sends the group to review and
// (for stale data) queues a targeted re-scan of its items.
func (r *run) skipGroup(g *models.DuplicateGroup, targets []*target, reason string, rescan bool) {
	r.skipGroupAndScan(g, targets, reason, rescan, nil)
}

// skipGroupRescan is skipGroup with a re-scan of the group's items plus targeted scans of other
// servers' items (server → rating keys) whose data changed.
func (r *run) skipGroupRescan(g *models.DuplicateGroup, targets []*target, reason string, extra map[int64][]string) {
	r.skipGroupAndScan(g, targets, reason, true, extra)
}

func (r *run) skipGroupAndScan(g *models.DuplicateGroup, targets []*target, reason string, rescan bool, extra map[int64][]string) {
	ctx := context.WithoutCancel(r.ctx)
	for _, t := range targets {
		if t.a.Status != models.ActionPending {
			continue
		}
		r.finishAction(t.a, models.ActionSkipped, "Skipped: "+reason)
	}
	r.s.setRunStatus(ctx, g.ID, models.GroupReview, reason)
	r.s.d.Log.Warn("Duplicate group skipped and sent to review", "group", g.ID, "title", displayTitle(g), "reason", reason)
	if rescan && r.s.d.Enqueue != nil {
		byServer := involvedRatingKeys(g)
		for sid, rks := range extra {
			for _, rk := range rks {
				if !slices.Contains(byServer[sid], rk) {
					byServer[sid] = append(byServer[sid], rk)
				}
			}
			slices.Sort(byServer[sid])
		}
		if r.multiServer() {
			// Merged per server and queued at the end of the run (see run.rescans).
			if r.rescans == nil {
				r.rescans = map[int64][]string{}
			}
			for sid, rks := range byServer {
				for _, rk := range rks {
					if !slices.Contains(r.rescans[sid], rk) {
						r.rescans[sid] = append(r.rescans[sid], rk)
					}
				}
			}
			byServer = nil
		}
		for _, sid := range sortedKeys(byServer) {
			body := models.TargetedScanBody{ServerID: sid, RatingKeys: byServer[sid]}
			if err := r.s.d.Enqueue(ctx, models.CmdTargetedScan, body, models.TriggerScheduled); err != nil {
				r.s.d.Log.Error("Could not queue a re-scan of a stale group", "group", g.ID, "error", err)
			}
		}
	}
	r.s.publishGroup(ctx, g.ID)
	r.report("Skipped %s: %s", displayTitle(g), reason)
}

// flushRescans queues the re-scans collected in this run, one targeted scan per server.
func (r *run) flushRescans() {
	if len(r.rescans) == 0 || r.s.d.Enqueue == nil {
		return
	}
	ctx := context.WithoutCancel(r.ctx)
	for _, sid := range sortedKeys(r.rescans) {
		rks := r.rescans[sid]
		slices.Sort(rks)
		body := models.TargetedScanBody{ServerID: sid, RatingKeys: rks}
		if err := r.s.d.Enqueue(ctx, models.CmdTargetedScan, body, models.TriggerScheduled); err != nil {
			r.s.d.Log.Error("Could not queue a re-scan of skipped groups", "serverId", sid, "error", err)
		}
	}
	r.rescans = nil
}

// deferGroup leaves a group's removals queued for a later run.
func (r *run) deferGroup(g *models.DuplicateGroup, reason string, failure, playing bool) {
	ctx := context.WithoutCancel(r.ctx)
	r.sum.Deferred++
	msg := "Waiting: " + reason + "; the removals stay queued for the next run"
	if playing {
		if err := r.s.d.Store.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, true); err != nil && !errors.Is(err, store.ErrNotFound) {
			r.s.d.Log.Error("Could not flag a deferred group as playing", "group", g.ID, "error", err)
		}
	}
	r.s.setRunStatus(ctx, g.ID, models.GroupQueued, msg)
	r.s.publishGroup(ctx, g.ID)
	r.s.d.Log.Info("Duplicate group deferred", "group", g.ID, "title", displayTitle(g), "reason", reason)
	r.report("Deferred %s: %s", displayTitle(g), reason)
	if failure {
		r.noteFailure()
	}
}

// finalizeGroup sets the group status from the results of its approval batch.
func (r *run) finalizeGroup(g *models.DuplicateGroup, stamps map[time.Time]bool) {
	ctx := context.WithoutCancel(r.ctx)
	all, err := r.s.d.Store.Actions().ListByGroup(ctx, g.ID)
	if err != nil {
		r.s.d.Log.Error("Could not load a group's removals", "group", g.ID, "error", err)
		return
	}
	var (
		batch     []models.Action
		n         = map[models.ActionStatus]int{}
		firstFail string
	)
	for _, a := range all {
		if !stamps[a.CreatedAt] {
			continue
		}
		if a.Status == models.ActionCancelled {
			n[a.Status]++
			continue
		}
		batch = append(batch, a)
		n[a.Status]++
		if a.Status == models.ActionFailed && firstFail == "" {
			firstFail = a.Message
		}
	}
	total := len(batch)
	if total == 0 {
		return
	}
	var (
		status   models.GroupStatus
		reason   string
		resolved bool
	)
	switch {
	case n[models.ActionFailed] > 0:
		status = models.GroupFailed
		reason = fmt.Sprintf("%d of %d removal(s) failed: %s", n[models.ActionFailed], total, firstFail)
	case n[models.ActionSkipped] > 0:
		status, reason = models.GroupReview, "Some removals were skipped; check the group"
	case n[models.ActionPending]+n[models.ActionRunning] > 0:
		why := "they run with the next queue run"
		if r.stopped != "" {
			why = r.stopped
		} else if r.aborted != "" {
			why = "the run was stopped"
		}
		status = models.GroupQueued
		reason = fmt.Sprintf("%d of %d removal(s) done; the rest stay queued (%s)", total-n[models.ActionPending]-n[models.ActionRunning], total, why)
	case n[models.ActionDryRun] == total:
		status, reason = models.GroupPending, "Dry run: no files were deleted"
	case n[models.ActionSucceeded] == total && n[models.ActionCancelled] > 0:
		// A cancelled removal leaves its duplicate in place: the group is not resolved.
		status = models.GroupPending
		reason = fmt.Sprintf("Removed %d file(s); %d removal(s) were cancelled", total, n[models.ActionCancelled])
	case n[models.ActionSucceeded] == total:
		status, reason, resolved = models.GroupResolved, fmt.Sprintf("Removed %d duplicate file(s)", total), true
	default:
		status = models.GroupPending
		reason = fmt.Sprintf("Partially simulated: %d file(s) removed, %d only in a dry run", n[models.ActionSucceeded], n[models.ActionDryRun])
	}
	if g.HasFlag(models.FlagPlaying) {
		if err := r.s.d.Store.Groups().SetFlag(ctx, g.ID, models.FlagPlaying, false); err != nil && !errors.Is(err, store.ErrNotFound) {
			r.s.d.Log.Error("Could not clear the playing flag", "group", g.ID, "error", err)
		}
	}
	if !r.s.setRunStatus(ctx, g.ID, status, reason) {
		resolved = false
	}
	if resolved {
		var bytes int64
		for _, a := range batch {
			bytes += a.Size
		}
		gid := g.ID
		r.s.addHistory(ctx, models.EventGroupResolved, &gid, nil, displayTitle(g),
			fmt.Sprintf("Removed %d duplicate file(s), %s", total, humanBytes(bytes)),
			map[string]any{"removed": total, "size": bytes})
	}
	r.s.publishGroup(ctx, g.ID)
}

// ---------------------------------------------------------------------------
// Breakers and reporting
// ---------------------------------------------------------------------------

// halted reports whether no new removal may start.
func (r *run) halted() bool {
	return r.ctx.Err() != nil || r.aborted != "" || r.stopped != ""
}

// budgetAllows checks the per-run count and size limits before a removal starts. The size limit
// is never exceeded, except by a single removal larger than the whole limit when it is the first
// of the run. A limit of 0 or less allows nothing (newRun recorded why).
func (r *run) budgetAllows(size int64) bool {
	switch {
	case r.stopped != "":
		return false
	case r.maxCount <= 0 || r.maxBytes <= 0:
		r.stopped = "a per-run deletion limit is not set, so nothing may be deleted"
		return false
	}
	if r.attempts >= r.maxCount {
		r.stopped = fmt.Sprintf("reached the limit of %d deletions per run", r.maxCount)
		return false
	}
	if r.bytes >= r.maxBytes || (r.bytes > 0 && r.bytes+size > r.maxBytes) {
		r.stopped = fmt.Sprintf("reached the limit of %d GB per run", r.maxBytes/bytesPerGB)
		return false
	}
	return true
}

// noteFailure counts a failure and aborts the run after maxConsecutiveFailures in a row.
func (r *run) noteFailure() {
	r.consecutive++
	if r.consecutive >= maxConsecutiveFailures && r.aborted == "" {
		r.abort(fmt.Sprintf("Stopped after %d consecutive failures; the remaining removals stay queued", r.consecutive), nil)
	}
}

func (r *run) abort(msg string, cause error) {
	if r.aborted != "" {
		return
	}
	r.aborted, r.abortCause = msg, cause
	r.sum.Aborted = true
	r.s.d.Log.Error("Queue run aborted", "reason", msg)
}

// report sends a progress message.
func (r *run) report(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.s.d.Log.Debug(msg)
	if r.progress != nil {
		r.progress(msg)
	}
}

// finish renders the summary and the run's error.
func (r *run) finish() (Summary, error) {
	r.flushRescans()
	var parts []string
	parts = append(parts, fmt.Sprintf("%d removal(s) processed", r.sum.Processed))
	if r.sum.Succeeded > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted (%s)", r.sum.Succeeded, humanBytes(r.sum.BytesFreed)))
	}
	if r.sum.DryRun > 0 {
		parts = append(parts, fmt.Sprintf("%d dry run", r.sum.DryRun))
	}
	if r.sum.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", r.sum.Skipped))
	}
	if r.sum.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.sum.Failed))
	}
	if r.sum.Deferred > 0 {
		parts = append(parts, fmt.Sprintf("%d group(s) deferred", r.sum.Deferred))
	}
	msg := strings.Join(parts, ", ")
	var err error
	switch {
	case r.aborted != "":
		msg += ". Aborted: " + r.aborted
		if r.abortCause != nil {
			err = fmt.Errorf("%w: %s: %w", ErrAborted, r.aborted, r.abortCause)
		} else {
			err = fmt.Errorf("%w: %s", ErrAborted, r.aborted)
		}
	case r.ctx.Err() != nil:
		msg += ". Cancelled; the remaining removals stay queued"
		err = fmt.Errorf("process queue: %w", r.ctx.Err())
	case r.stopped != "":
		msg += ". Stopped: " + r.stopped + "; the remaining removals stay queued"
	}
	r.sum.Message = msg
	r.report("%s", msg)
	return r.sum, err
}

// ---------------------------------------------------------------------------
// Clients and cached lookups
// ---------------------------------------------------------------------------

// serverClient returns the client of an enabled media server, or why there is none.
func (r *run) serverClient(serverID int64) (MediaServerClient, string) {
	if c, ok := r.serverClients[serverID]; ok {
		return c, ""
	}
	srv, ok := r.servers[serverID]
	switch {
	case !ok:
		return nil, fmt.Sprintf("media server #%d no longer exists", serverID)
	case !srv.Enabled:
		return nil, fmt.Sprintf("media server %s is disabled", srv.Name)
	case !r.s.hasServerFactory():
		return nil, "no Plex client is configured"
	}
	c := r.s.newServerClient(srv)
	if c == nil {
		return nil, fmt.Sprintf("no client for media server %s", srv.Name)
	}
	r.serverClients[serverID] = c
	return c, ""
}

// newServerClient returns a new client for srv: from MediaServerFactory when it is set, else from
// PlexFactory, else nil. A factory's nil stays a nil interface.
func (s *Service) newServerClient(srv models.MediaServer) MediaServerClient {
	switch {
	case s.d.MediaServerFactory != nil:
		if c := s.d.MediaServerFactory(srv); c != nil {
			return c
		}
	case s.d.PlexFactory != nil:
		if c := s.d.PlexFactory(srv); c != nil {
			return c
		}
	}
	return nil
}

// hasServerFactory reports whether a media server client factory is configured.
func (s *Service) hasServerFactory() bool {
	return s.d.MediaServerFactory != nil || s.d.PlexFactory != nil
}

// arrClient returns the client of an *arr instance (nil when none can be built).
func (r *run) arrClient(inst models.ArrInstance) ArrClient {
	if c, ok := r.arrClients[inst.ID]; ok {
		return c
	}
	if r.s.d.ArrFactory == nil {
		return nil
	}
	c := r.s.d.ArrFactory(inst)
	if c != nil {
		r.arrClients[inst.ID] = c
	}
	return c
}

// plexDeletionAllowed caches the server's "Allow media deletion" setting for the run.
func (r *run) plexDeletionAllowed(serverID int64, c mediaserver.VersionDeleter) (bool, error) {
	if v, ok := r.deletionOK[serverID]; ok {
		return v.v, v.err
	}
	ok, err := c.MediaDeletionAllowed(r.ctx)
	r.deletionOK[serverID] = cachedBool{ok, err}
	return ok, err
}

// arrRecycleBin caches an instance's recycle bin ("" = its deletes are permanent) for the run.
func (r *run) arrRecycleBin(inst models.ArrInstance, c ArrClient) (string, error) {
	if v, ok := r.arrBins[inst.ID]; ok {
		return v.v, v.err
	}
	var v cachedString
	mm, err := c.MediaManagement(r.ctx)
	switch {
	case err != nil:
		v.err = err
	case mm == nil:
		v.err = errors.New("empty answer")
	default:
		v.v = strings.TrimSpace(mm.RecycleBin)
	}
	r.arrBins[inst.ID] = v
	return v.v, v.err
}

// recycleBin validates the configured local recycle bin once per run.
func (r *run) recycleBin() (string, error) {
	if r.bin == nil {
		v, err := validateRecycleBin(r.st.RecycleBinPath, r.roots, r.mappings, r.libDirs, r.s.d.DataDir)
		r.bin = &cachedString{v, err}
	}
	return r.bin.v, r.bin.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type itemRef struct {
	serverID  int64
	ratingKey string
}

// serverOf returns the media server of a version (the group's when the version has none).
func serverOf(g *models.DuplicateGroup, v *models.MediaVersion) int64 {
	if v.ServerID != 0 {
		return v.ServerID
	}
	return g.ServerID
}

// playingKeys returns the ids a session of server sid reports while it plays a version of g: the
// rating keys of the group's items on that server (Plex sessions name the item, and only the
// item). A version of another kind (Jellyfin, research S13) adds its own version id and the item id
// of every stack part: a session names the version playing (PlayState.MediaSourceId) and, while
// part 2 or later plays, the part's own item. listingPlayingIDs is the counterpart for another
// server's listing of a file to remove (both feed the F10 "never while playing" checks).
func playingKeys(g *models.DuplicateGroup, sid int64) []string {
	out := involvedRatingKeys(g)[sid]
	for i := range g.Files {
		v := &g.Files[i].Version
		if serverOf(g, v) != sid {
			continue
		}
		if k, ok := models.KindOfVersionKey(v.Key); !ok || k.IsPlex() {
			continue
		}
		ids := []string{v.SourceID}
		for _, p := range v.Parts {
			ids = append(ids, p.ItemID)
		}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// listingPlayingIDs returns the ids a session of another server reports while it plays the
// version listed by e: the listing's item (Plex sessions name the item) and, for a listing of
// another kind (Jellyfin), its version id and the item ids of its stack parts. See playingKeys.
func listingPlayingIDs(e *models.OtherListing) []string {
	out := []string{strings.TrimSpace(e.RatingKey)}
	k, ok := models.KindOfVersionKey(e.VersionKey)
	if !ok || k.IsPlex() {
		return out
	}
	if i := strings.LastIndexByte(e.VersionKey, ':'); i >= 0 {
		out = append(out, e.VersionKey[i+1:])
	}
	for _, id := range e.PartItemIDs {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// involvedRatingKeys returns the sorted rating keys of every file of the group, per server.
func involvedRatingKeys(g *models.DuplicateGroup) map[int64][]string {
	out := map[int64][]string{}
	for i := range g.Files {
		v := &g.Files[i].Version
		rk := strings.TrimSpace(v.RatingKey)
		if rk == "" {
			continue
		}
		sid := serverOf(g, v)
		if !slices.Contains(out[sid], rk) {
			out[sid] = append(out[sid], rk)
		}
	}
	for sid := range out {
		slices.Sort(out[sid])
	}
	return out
}

func sortedKeys[V any](m map[int64]V) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// findTargetFile returns the group file an action removes (matched like the store does: the
// version key, and the file id when both are set).
func findTargetFile(g *models.DuplicateGroup, a *models.Action) *models.GroupFile {
	if a.VersionKey == "" && a.GroupFileID == 0 {
		return nil
	}
	for i := range g.Files {
		f := &g.Files[i]
		if (a.VersionKey == "" || f.Version.Key == a.VersionKey) && (a.GroupFileID == 0 || f.ID == a.GroupFileID) {
			return f
		}
	}
	return nil
}

// samePathList compares two path lists element-wise after normalization.
func samePathList(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if pathmap.Normalize(a[i]) != pathmap.Normalize(b[i]) {
			return false
		}
	}
	return true
}

// isShared reports a version whose file is also referenced by other items (multi-episode file),
// or that the media server lists as a multi-episode file (EpisodeEnd, Jellyfin).
func isShared(v *models.MediaVersion) bool {
	for _, p := range v.Parts {
		if len(p.SharedWith) > 0 {
			return true
		}
	}
	return v.EpisodeEnd > 0 || (v.Arr != nil && v.Arr.Kind == models.ArrSonarr && len(v.Arr.EpisodeIDs) > 1)
}

// hasShortcut reports a version whose file a .strm shortcut the server lists points to (Jellyfin,
// MediaPart.ShortcutOf): it is in use and never removed.
func hasShortcut(v *models.MediaVersion) bool {
	for _, p := range v.Parts {
		if len(p.ShortcutOf) > 0 {
			return true
		}
	}
	return false
}

// isOptimized reports a Plex optimized version (never a duplicate, never removed).
func isOptimized(v *models.MediaVersion) bool {
	if v.OptimizedVersion {
		return true
	}
	for _, p := range v.Parts {
		if strings.Contains("/"+strings.ToLower(strings.Trim(pathmap.Normalize(p.Path), "/"))+"/", "/plex versions/") {
			return true
		}
	}
	return false
}

// describeVersion names a version in messages.
func describeVersion(v *models.MediaVersion) string {
	if len(v.Parts) > 0 {
		return v.Parts[0].Path
	}
	return v.Key
}

// notStarted returns the targets that have no outcome.
func notStarted(targets []*target, done []*outcome) []*target {
	started := map[int64]bool{}
	for _, oc := range done {
		started[oc.t.a.ID] = true
	}
	var out []*target
	for _, t := range targets {
		if !started[t.a.ID] {
			out = append(out, t)
		}
	}
	return out
}

func actionsOf(ts []*target) []models.Action {
	out := make([]models.Action, 0, len(ts))
	for _, t := range ts {
		out = append(out, *t.a)
	}
	return out
}

// anyRemoved reports whether a real (non-simulated) removal succeeded.
func (r *run) anyRemoved(done []*outcome) bool {
	for _, oc := range done {
		if oc.status == models.ActionSucceeded {
			return true
		}
	}
	return false
}
