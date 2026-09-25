// Package executor executes approved removals (docs/ARCHITECTURE.md §6, docs/DECISIONS.md D3/D6):
// re-verify against live data, choose a deletion method (arr → plex → filesystem, per settings),
// post-process, record history, and enforce the circuit breakers and safety invariants.
//
// This package deletes user media. Every decision errs on the side of NOT deleting: anything that
// cannot be verified leaves the removal pending (transient problems) or skips it and sends the
// group back to review (stale or suspicious data). A removal is never retried with another
// method after a failed attempt, and Plex's section-wide "empty trash" is never called.
//
// Identity guards (guards.go): a media server must still answer with the machineIdentifier
// Dupearr stored, before its group is re-verified and right before every Plex delete; an *arr file
// id must still name the version being removed (same mapped path, size and item) when the removal
// is planned and right before DeleteFile. A connection re-pointed at another server therefore
// never deletes unrelated media. Per-run limits of 0 or less allow nothing, and removals a crash
// left "running" are failed, never retried (RecoverInterrupted).
//
// Changes after the approval win (process.go, guards.go): an exclusion created, a library
// disabled or a profile protection added since skips the group; the *arr file of a tracked keeper
// must still name the keeper (else the "untracked" loser may be the *arr's file now); a version
// kept by a group that removed files earlier in the same run is never removed; dry run switched on
// during a run applies from the next removal. A keeper must be confirmed present (on disk, or
// Plex's exists=true) and must not lie in a recycle bin (Dupearr's or an *arr's), which is purged
// automatically; with a keep-per profile (one per resolution or dynamic range) every partition a
// version is removed from needs such a keeper of its own. Approve only queues the state it checked
// (ApproveReviewed). Restore ignores the group of the restored file, so it is not removed again.
//
// Several media servers (crossserver.go, docs/DECISIONS.md D11): with two or more enabled servers
// a group is only acted on with a complete cross-server record that names every enabled server;
// another server's libraries must not have changed since the scan; every other server's item that
// lists a file to remove must keep a version Dupearr finds on disk and proves to be a different
// file (on files open at the same time, allowlisted filesystem types only), and no other server's
// live group may keep that file. An *arr file is never confirmed by raw path for a server the
// instance is not confirmed to feed, nor for a part the server has no mapping for while the *arr's
// path maps.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// PlexClient is the subset of *plex.Client the executor uses (fakes in tests).
type PlexClient interface {
	// Identity is read before a group is re-verified and right before every Plex delete: the
	// server must still be the one Dupearr stored (models.MediaServer.MachineIdentifier).
	Identity(ctx context.Context) (*plex.Identity, error)
	Item(ctx context.Context, ratingKey string) (*models.MediaItem, error)
	DeleteMedia(ctx context.Context, ratingKey string, mediaID int64) error
	RefreshItem(ctx context.Context, ratingKey string) error
	ScanPath(ctx context.Context, sectionKey, dir string) error
	MediaDeletionAllowed(ctx context.Context) (bool, error)
	ActiveSessions(ctx context.Context) (map[string]bool, error)
	// Sections is read, with two or more enabled media servers, from the other servers right before
	// a removal: a library that changed since the scan may list the file (docs/DECISIONS.md D11).
	Sections(ctx context.Context) ([]plex.Section, error)
}

// ArrClient is the subset of *arr.Client the executor uses (fakes in tests).
type ArrClient interface {
	// File is read when an *arr removal is planned and again right before DeleteFile: the file id
	// must still name the version being removed (same file, same size, same item).
	File(ctx context.Context, fileID int64) (*arr.TrackedFileRef, error)
	DeleteFile(ctx context.Context, fileID int64) error
	Rescan(ctx context.Context, itemID int64) error
	Unmonitor(ctx context.Context, info models.ArrFileInfo) error
	AddExclusion(ctx context.Context, t arr.ExclusionTarget) error
	MediaManagement(ctx context.Context) (*arr.MediaManagement, error)
}

// Deps are the executor's dependencies.
type Deps struct {
	Store       store.Store
	Bus         *events.Bus
	Log         *slog.Logger
	Notifier    *notifications.Service // may be nil
	PlexFactory func(s models.MediaServer) PlexClient
	ArrFactory  func(a models.ArrInstance) ArrClient
	Now         func() time.Time
	// Enqueue schedules a command (used to queue a TargetedScan after a stale-data skip). May be nil.
	Enqueue func(ctx context.Context, name string, body any, trigger string) error
	// DataDir is Dupearr's data folder (config.xml, the database, backups): the recycle bin must
	// never be, contain or lie inside it (validateRecycleBin). Empty skips that check (tests).
	DataDir string
	// GroupLock, when set, is held while an approval reads, checks and queues a group.
	// cmd/dupearr passes scanner.Service.GroupLock, the lock of the scanner's read → evaluate →
	// upsert cycle: a scan or re-evaluation that read the group before the approval would
	// otherwise store the status it read ("pending") over the approval afterwards, which cancels
	// the approved removals although the approval succeeded. nil = no such lock (tests).
	GroupLock sync.Locker
	// FileIdentity proves, with two or more media servers, that another server's remaining copy is
	// a different file from the one being removed (docs/DECISIONS.md D11). nil = fileid.Default()
	// per queue run.
	FileIdentity *fileid.Prober
}

// Summary is the outcome of one ProcessQueue run.
type Summary struct {
	Processed, Succeeded, DryRun, Skipped, Failed int
	BytesFreed                                    int64
	Aborted                                       bool
	Message                                       string
	// Deferred counts groups whose removals were left pending for a later run (a version is
	// playing, or Plex could not be reached to re-verify the group).
	Deferred int
}

// Service executes removal actions. It is safe for concurrent use: ProcessQueue, Restore and
// CleanRecycleBin are serialized (one at a time); Approve and CancelGroup only touch the database
// (whose repositories guard the action lifecycle themselves) and are serialized with each other.
type Service struct {
	d Deps

	// runSem (capacity 1) serializes everything that touches media files.
	runSem chan struct{}
	// approveMu serializes Approve and CancelGroup, so two approvals of one group (a double click,
	// a bulk approval racing a single one, auto mode racing the user) never queue two batches.
	approveMu sync.Mutex

	// rename is renameEntry: a rename relative to two open folders (replaced in tests to simulate
	// cross-device moves).
	rename func(src, dst entry) error
	// goneWait / goneStep bound how long a Plex delete is given to remove the part files from disk
	// before it is reported as incomplete.
	goneWait, goneStep time.Duration
}

// Errors.
var (
	// ErrNothingToRemove is returned by Approve when no file of the group is decided "remove".
	ErrNothingToRemove = errors.New("nothing to remove in this group")
	// ErrNotApprovable is returned (wrapped, with the reason) by Approve when the group's status or
	// the approval trigger does not allow queueing removals.
	ErrNotApprovable = errors.New("group cannot be approved")
	// ErrNotRestorable is returned (wrapped, with the reason) by Restore when an action cannot be
	// undone.
	ErrNotRestorable = errors.New("action cannot be restored")
	// ErrAborted is returned (wrapped, with the reason) by ProcessQueue when a safety breaker stopped
	// the whole run (an *arr reported a missing root folder, or too many consecutive failures). The
	// Summary is returned alongside it.
	ErrAborted = errors.New("queue run aborted")
)

// New returns a Service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		d:        d,
		runSem:   make(chan struct{}, 1),
		rename:   renameEntry,
		goneWait: 5 * time.Second,
		goneStep: 500 * time.Millisecond,
	}
}

func (s *Service) now() time.Time { return s.d.Now() }

// lock acquires the run lock, waiting until it is free or ctx is done.
func (s *Service) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	select {
	case s.runSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("executor: waiting for the running queue job: %w", ctx.Err())
	}
}

func (s *Service) unlock() { <-s.runSem }

// ---------------------------------------------------------------------------
// Approve
// ---------------------------------------------------------------------------

// Approve validates the group's effective decisions and creates pending actions for every file
// decided "remove" (not protected); sets the group to queued; history event groupApproved.
// Returns ErrNothingToRemove / validation errors. It is ApproveReviewed without a signature.
//
// Only an explicit user approval (trigger "manual") may approve a group in review, deferred or
// failed status; automatic approvals (any other trigger) require auto mode, status pending, no
// flag that keeps a group out of auto mode (engine.BlocksAutoApproval) and a decision that was
// stable for settings.StableScansRequired scans. Resolved, ignored, protected and already queued
// groups are never approved (errors wrap ErrNotApprovable). Decisions that violate a safety
// invariant are rejected with engine.ErrInvariant.
func (s *Service) Approve(ctx context.Context, groupID int64, trigger string) ([]models.Action, error) {
	return s.ApproveReviewed(ctx, groupID, trigger, "")
}

// ApproveReviewed is Approve for the state a person (or the caller's checks) reviewed: a non-empty
// signature must still be the group's DuplicateGroup.Signature, else the approval is refused
// (ErrNotApprovable) — the decisions changed since they were shown.
//
// The group is only queued with the data the approval was checked against: the status is written
// with a compare-and-set, and the group is read again before and after that write. When a scan (or
// a re-evaluation) stored new results for the group meanwhile — another status, other decisions,
// flags, files or *arr tracking — the queued removals are cancelled and ErrNotApprovable is
// returned; the scan's results stand (after the write, the group is sent to review and re-scanned,
// because the write replaced the scan's status).
func (s *Service) ApproveReviewed(ctx context.Context, groupID int64, trigger, signature string) ([]models.Action, error) {
	if s.d.Store == nil {
		return nil, errors.New("approve: executor has no store")
	}
	s.approveMu.Lock()
	defer s.approveMu.Unlock()
	// The scanner's group lock (Deps.GroupLock): no scan or re-evaluation is between reading this
	// group and storing it while the approval checks and queues it, so none can store the status
	// it read before the approval over it afterwards.
	if s.d.GroupLock != nil {
		s.d.GroupLock.Lock()
		defer s.d.GroupLock.Unlock()
	}
	settings, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("approve group %d: load settings: %w", groupID, err)
	}
	g, err := s.d.Store.Groups().Get(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("approve group %d: %w", groupID, err)
	}
	manual := trigger == models.TriggerManual
	if signature = strings.TrimSpace(signature); signature != "" && signature != g.Signature {
		return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, displayTitle(g), ErrNotApprovable, errChangedSinceReview)
	}
	if err := checkApprovable(g, manual, settings); err != nil {
		return nil, fmt.Errorf("approve group %d (%s): %w", g.ID, displayTitle(g), err)
	}
	if err := engine.ValidateDecisions(g); err != nil {
		return nil, fmt.Errorf("approve group %d (%s): %w", g.ID, displayTitle(g), err)
	}
	// Full-disc backups (docs/DECISIONS.md D9): a disc removal needs a person's approval, the
	// setting, a recycle bin and the filesystem method; a disc is never the only kept copy while
	// "keep a Plex-playable copy" is on.
	if problem := discRemovalProblem(g, settings, manual); problem != "" {
		return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, displayTitle(g), ErrNotApprovable, problem)
	}

	var targets []*models.GroupFile
	for i := range g.Files {
		f := &g.Files[i]
		if f.Decision == models.DecisionRemove && !f.Protected && !f.Version.OptimizedVersion {
			targets = append(targets, f)
		}
	}
	if len(targets) == 0 {
		return nil, ErrNothingToRemove
	}

	existing, err := s.d.Store.Actions().ListByGroup(ctx, g.ID)
	if err != nil {
		return nil, fmt.Errorf("approve group %d: %w", g.ID, err)
	}
	// Leftovers of an earlier approval (e.g. a run aborted by a breaker left removals of a failed
	// group queued) are replaced by one fresh batch, so every removal of the group is approved
	// against the same, current decisions.
	stamp := s.now().UTC()
	leftovers := false
	for _, a := range existing {
		if a.Status == models.ActionRunning {
			return nil, fmt.Errorf("approve group %d: %w: a removal of this group is running right now", g.ID, ErrNotApprovable)
		}
		if a.Status == models.ActionPending {
			leftovers = true
		}
		if !stamp.After(a.CreatedAt) {
			stamp = a.CreatedAt.Add(time.Millisecond)
		}
	}
	if leftovers {
		if _, err := s.d.Store.Actions().CancelPendingForGroup(ctx, g.ID); err != nil {
			return nil, fmt.Errorf("approve group %d: cancel earlier queued removals: %w", g.ID, err)
		}
	}

	title := displayTitle(g)
	created := make([]models.Action, 0, len(targets))
	for _, f := range targets {
		a := models.Action{
			GroupID:     g.ID,
			GroupFileID: f.ID,
			VersionKey:  f.Version.Key,
			Title:       title,
			Paths:       partPaths(&f.Version),
			Size:        f.Version.TotalSize(),
			Status:      models.ActionPending,
			DryRun:      settings.DryRun,
			CreatedAt:   stamp,
		}
		if err := s.d.Store.Actions().Create(ctx, &a); err != nil {
			s.rollbackApproval(ctx, g.ID)
			if changed, _ := s.groupChanged(ctx, g, true); changed {
				// The store refused the removal because a scan changed the group meanwhile.
				return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, title, ErrNotApprovable, errChangedWhileApproving)
			}
			return nil, fmt.Errorf("approve group %d: queue removal of %s: %w", g.ID, f.Version.Key, err)
		}
		created = append(created, a)
	}
	// Before the status write: the group must still be what the approval was checked against
	// (a scan may have stored new results while the removals were queued).
	switch changed, err := s.groupChanged(ctx, g, true); {
	case err != nil:
		s.rollbackApproval(ctx, g.ID)
		return nil, fmt.Errorf("approve group %d: %w", g.ID, err)
	case changed:
		s.rollbackApproval(ctx, g.ID)
		return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, title, ErrNotApprovable, errChangedWhileApproving)
	}
	reason := fmt.Sprintf("Approved (%s): %d removal(s) queued", triggerLabel(trigger), len(created))
	if settings.DryRun {
		reason += " (dry run)"
	}
	// Compare-and-set: a status stored meanwhile (review, ignored, resolved…) is never overwritten.
	ok, err := s.d.Store.Groups().UpdateStatusIf(ctx, g.ID, []models.GroupStatus{g.Status}, models.GroupQueued, reason)
	switch {
	case err != nil:
		s.rollbackApproval(ctx, g.ID)
		return nil, fmt.Errorf("approve group %d: %w", g.ID, err)
	case !ok:
		s.rollbackApproval(ctx, g.ID)
		return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, title, ErrNotApprovable, errChangedWhileApproving)
	}
	// After the write: a scan that stored the same status with other data right before it lost
	// its status and reason to "queued" — undo the approval and let a re-scan decide.
	if changed, err := s.groupChanged(ctx, g, false); err != nil || changed {
		s.revertApproval(ctx, g)
		if err != nil {
			return nil, fmt.Errorf("approve group %d: %w", g.ID, err)
		}
		return nil, fmt.Errorf("approve group %d (%s): %w: %s", g.ID, title, ErrNotApprovable, errChangedWhileApproving)
	}

	var bytes int64
	for _, a := range created {
		bytes += a.Size
		s.publish(events.NameQueue, events.ActionUpdated, a)
	}
	gid := g.ID
	s.addHistory(ctx, models.EventGroupApproved, &gid, nil, title,
		fmt.Sprintf("Approved (%s): %d file(s), %s queued for removal%s", triggerLabel(trigger), len(created), humanBytes(bytes), dryRunSuffix(settings.DryRun)),
		map[string]any{"trigger": trigger, "actions": len(created), "size": bytes, "dryRun": settings.DryRun})
	s.publishGroup(ctx, g.ID)
	s.d.Log.Info("Duplicate group approved", "group", g.ID, "title", title, "trigger", trigger,
		"removals", len(created), "dryRun", settings.DryRun)
	return created, nil
}

// Messages of approvals refused because the group changed (wrapped with ErrNotApprovable).
const (
	errChangedSinceReview    = "the duplicate changed since it was reviewed (a scan or another user updated its decisions); review it again"
	errChangedWhileApproving = "the duplicate changed while it was being approved (a scan stored new results); review it again"
)

// groupChanged re-reads a group and reports whether it differs from the state an approval was
// checked against (see sameApprovalState; withStatus also compares status and reason). A group
// that no longer exists counts as changed.
func (s *Service) groupChanged(ctx context.Context, approved *models.DuplicateGroup, withStatus bool) (bool, error) {
	now, err := s.d.Store.Groups().Get(context.WithoutCancel(ctx), approved.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	if withStatus && (now.Status != approved.Status || now.StatusReason != approved.StatusReason) {
		return true, nil
	}
	return !sameApprovalState(approved, now), nil
}

// sameApprovalState compares what an approval depends on: the decision signature, the flags and,
// per version, the decision, protection, parts and *arr tracking (which decides how a file is
// removed and whether a keep tag applies).
func sameApprovalState(a, b *models.DuplicateGroup) bool {
	if a.Signature != b.Signature || len(a.Files) != len(b.Files) {
		return false
	}
	fa, fb := slices.Clone(a.Flags), slices.Clone(b.Flags)
	slices.Sort(fa)
	slices.Sort(fb)
	if !slices.Equal(slices.Compact(fa), slices.Compact(fb)) {
		return false
	}
	byKey := make(map[string]*models.GroupFile, len(b.Files))
	for i := range b.Files {
		byKey[b.Files[i].Version.Key] = &b.Files[i]
	}
	for i := range a.Files {
		x := &a.Files[i]
		y := byKey[x.Version.Key]
		if y == nil || x.Decision != y.Decision || x.Protected != y.Protected || !samePathList(partPaths(&x.Version), partPaths(&y.Version)) ||
			x.Version.TotalSize() != y.Version.TotalSize() || !sameArr(x.Version.Arr, y.Version.Arr) {
			return false
		}
	}
	return true
}

// sameArr compares the identity of two *arr trackings (nil = untracked).
func sameArr(a, b *models.ArrFileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.InstanceID == b.InstanceID && a.FileID == b.FileID && a.ItemID == b.ItemID &&
		slices.Equal(a.EpisodeIDs, b.EpisodeIDs) && slices.Equal(a.Tags, b.Tags)
}

// revertApproval undoes an approval whose status write replaced a scan's result: the queued
// removals are cancelled (which reopens the group), the group goes to review and a targeted
// re-scan recomputes its real state.
func (s *Service) revertApproval(ctx context.Context, g *models.DuplicateGroup) {
	bg := context.WithoutCancel(ctx)
	s.rollbackApproval(bg, g.ID)
	msg := "This duplicate changed while it was being approved (a scan stored new results); it is re-scanned — review it again"
	if _, err := s.d.Store.Groups().UpdateStatusIf(bg, g.ID, []models.GroupStatus{models.GroupQueued, models.GroupPending}, models.GroupReview, msg); err != nil {
		s.d.Log.Error("Could not send a changed group back to review", "group", g.ID, "error", err)
	}
	if s.d.Enqueue != nil {
		byServer := involvedRatingKeys(g)
		for _, sid := range sortedKeys(byServer) {
			if err := s.d.Enqueue(bg, models.CmdTargetedScan, models.TargetedScanBody{ServerID: sid, RatingKeys: byServer[sid]}, models.TriggerScheduled); err != nil {
				s.d.Log.Error("Could not queue a re-scan of a changed group", "group", g.ID, "error", err)
			}
		}
	}
	s.publishGroup(bg, g.ID)
	s.d.Log.Warn("An approval was undone: the group changed while it was being approved", "group", g.ID, "title", displayTitle(g))
}

// checkApprovable enforces which statuses and triggers may queue removals.
func checkApprovable(g *models.DuplicateGroup, manual bool, st models.Settings) error {
	withReason := func(msg string) error {
		if r := strings.TrimSpace(g.StatusReason); r != "" {
			msg += ": " + r
		}
		return fmt.Errorf("%w: %s", ErrNotApprovable, msg)
	}
	switch g.Status {
	case models.GroupPending:
	case models.GroupReview, models.GroupDeferred, models.GroupFailed:
		if !manual {
			return withReason(fmt.Sprintf("a group in %q status can only be approved manually", g.Status))
		}
	case models.GroupQueued:
		return fmt.Errorf("%w: its removals are already queued", ErrNotApprovable)
	case models.GroupResolved:
		return fmt.Errorf("%w: it is resolved", ErrNotApprovable)
	case models.GroupIgnored:
		return fmt.Errorf("%w: it is ignored", ErrNotApprovable)
	case models.GroupProtected:
		return withReason("it is protected")
	default:
		return fmt.Errorf("%w: unknown status %q", ErrNotApprovable, g.Status)
	}
	if manual {
		return nil
	}
	if st.Mode != models.ModeAuto {
		return fmt.Errorf("%w: automatic approval requires auto mode", ErrNotApprovable)
	}
	if need := max(1, st.StableScansRequired); g.StableCount < need {
		return fmt.Errorf("%w: the decision has been stable for %d scan(s); automatic approval needs %d", ErrNotApprovable, g.StableCount, need)
	}
	// The scanner only proposes groups without these flags, but the group may have changed since.
	for _, f := range g.Flags {
		if engine.BlocksAutoApproval(f) {
			return fmt.Errorf("%w: its flag %q needs a person to approve it", ErrNotApprovable, f)
		}
	}
	return nil
}

// rollbackApproval cancels the removals a failed approval already queued.
func (s *Service) rollbackApproval(ctx context.Context, groupID int64) {
	if _, err := s.d.Store.Actions().CancelPendingForGroup(context.WithoutCancel(ctx), groupID); err != nil {
		s.d.Log.Error("Could not cancel the removals of a failed approval", "group", groupID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// CancelGroup
// ---------------------------------------------------------------------------

// CancelGroup cancels the pending actions of a group and re-opens it (status pending; the caller
// re-evaluates it). Removals that are already running are not interrupted. Groups that are not
// queued or failed keep their status.
func (s *Service) CancelGroup(ctx context.Context, groupID int64) error {
	if s.d.Store == nil {
		return errors.New("cancel group: executor has no store")
	}
	s.approveMu.Lock()
	defer s.approveMu.Unlock()
	g, err := s.d.Store.Groups().Get(ctx, groupID)
	if err != nil {
		return fmt.Errorf("cancel group %d: %w", groupID, err)
	}
	n, err := s.d.Store.Actions().CancelPendingForGroup(ctx, groupID)
	if err != nil {
		return fmt.Errorf("cancel group %d: %w", groupID, err)
	}
	if g.Status == models.GroupQueued || g.Status == models.GroupFailed {
		reason := "Queued removals were cancelled"
		if n == 0 {
			reason = "Approval was cancelled"
		}
		if err := s.d.Store.Groups().UpdateStatus(ctx, groupID, models.GroupPending, reason); err != nil {
			return fmt.Errorf("cancel group %d: %w", groupID, err)
		}
	}
	if acts, err := s.d.Store.Actions().ListByGroup(ctx, groupID); err == nil {
		for _, a := range acts {
			if a.Status == models.ActionCancelled {
				s.publish(events.NameQueue, events.ActionUpdated, a)
			}
		}
	}
	s.publishGroup(ctx, groupID)
	s.d.Log.Info("Cancelled queued removals", "group", groupID, "cancelled", n)
	return nil
}
