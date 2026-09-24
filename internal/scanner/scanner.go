// Package scanner orchestrates the scan pipeline (docs/ARCHITECTURE.md §5, docs/DECISIONS.md
// D2–D4): collect (Plex listings + item details) → enrich (*arr tracked files, queue state) →
// group (engine.BuildGroups) → evaluate (engine.Evaluate) → persist (store) → resolve groups that
// disappeared → publish events / history / notifications → auto-approve in auto mode.
//
// Safety rules implemented here (this application deletes media):
//   - A group is only marked resolved when the listing of its libraries completed in this scan,
//     every item detail it depends on was fetched and it could be persisted; a transient Plex
//     error never resolves (or silently shrinks) a group. Targeted scans never resolve groups
//     other than those of the items they targeted.
//   - Shared files (multi-episode files, or files another library's item also uses) are detected
//     from full listings (path → rating keys), never guessed: targeted scans list the library
//     too, and libraries whose folders overlap a scanned library — even ones disabled in Dupearr
//     — are listed for this index. When such a library cannot be listed, the groups of the
//     libraries it overlaps go to review.
//   - When an *arr instance cannot be read (tracked files or queue) — also after one retry, e.g.
//     when a title changed while it was read — every group of that media type is sent to review
//     and its files are never treated as untracked: missing *arr data could hide a keep tag or
//     tracked state, so such a group cannot be approved (ArrDataMissing) until a scan read every
//     instance. So is a
//     candidate the *arrs cannot be asked about (no TMDB/IMDb/TVDB id), and a queued (approved)
//     group whose fresh data is incomplete (its queued removals are cancelled). Such a scan never
//     counts towards a group's stability.
//   - The file name + size fallback of the *arr matcher only applies when it is unambiguous on
//     both sides: one tracked file, one candidate version, no other version claiming that file.
//   - User state follows the content, not only the key: a group whose key changed (collision
//     suffixes, re-matched titles) keeps its stored group or inherits its overrides and ignored
//     status, and a group sharing versions with an ignored group is never acted on without review.
//     A stored group of other content is never overwritten because the engine reused its key.
//   - Auto mode approves only pending groups that are stable (settings.StableScansRequired
//     consecutive full scans with the same decisions), carry no review-ish flag and fit in the
//     per-scan deletion budget.
//   - Missing files are not a resolved duplicate: a stored group holding a version whose file the
//     media server reports missing (exists == false) — unless its local folder is there and only
//     the file is gone — is kept with its status, flag unavailable_version and the reason "Some
//     files are unavailable — check your mounts" instead of being resolved (unavailable.go).
//   - Plex has no per-version date added: a version no *arr dates whose local file can be stat'ed
//     is dated by the later of Plex's addedAt and the file's change time, so a new copy of an old
//     title waits for the minimum age (applyFileAges).
//   - Re-evaluations apply configuration changes at once: a group an exclusion now covers, with a
//     version in a disabled library, or merged across libraries that no longer share a scope group
//     goes to review, which cancels its queued removals (blockedReason, ReevaluateMatching).
//   - A media server stored without a machine identifier (force-saved while unreachable) gets the
//     one it answers with on the next sync or scan, never replacing a stored one and never one
//     another configured server has (adoptIdentity).
//   - Scans are serialized inside the Service; group read-evaluate-write cycles are serialized
//     with re-evaluations.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// PlexClient is the subset of *plex.Client the scanner uses (fakes in tests).
type PlexClient interface {
	Identity(ctx context.Context) (*plex.Identity, error)
	Sections(ctx context.Context) ([]plex.Section, error)
	DuplicateItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error)
	AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error)
	Item(ctx context.Context, ratingKey string) (*models.MediaItem, error)
}

// ArrClient is the subset of *arr.Client the scanner uses (fakes in tests).
type ArrClient interface {
	TrackedFiles(ctx context.Context, f arr.TrackedFilter) ([]arr.TrackedFile, error)
	QueueItemIDs(ctx context.Context) (map[int64]bool, error)
}

// Deps are the scanner's dependencies.
type Deps struct {
	Store       store.Store
	Bus         *events.Bus
	Log         *slog.Logger
	Notifier    *notifications.Service // may be nil in tests
	PlexFactory func(s models.MediaServer) PlexClient
	ArrFactory  func(a models.ArrInstance) ArrClient
	Now         func() time.Time // nil = time.Now
	Concurrency int              // parallel item-detail fetches (default 4)

	// AutoApprove (additive to docs/CONTRACTS.md) approves one group in auto mode (§5 step 6).
	// cmd/dupearr wires it to executor.Service.ApproveReviewed; signature is the
	// DuplicateGroup.Signature this scan stored and checked, so the executor refuses the approval
	// when anything changed the group's decisions since. The scanner calls it for groups left in
	// "pending" when settings.Mode == "auto" (never for review/deferred/protected), within the
	// per-scan budget (maxDeletionsPerRun files, maxBytesPerRunGb; a limit ≤ 0 approves nothing,
	// like the executor), and counts successes in ScanStats.AutoApproved. The caller queues
	// ProcessQueue afterwards when AutoApproved > 0. nil = auto approval disabled (tests).
	AutoApprove func(ctx context.Context, groupID int64, trigger, signature string) error
}

// DefaultConcurrency is the number of parallel item-detail fetches when Deps.Concurrency ≤ 0.
const DefaultConcurrency = 4

// maxConcurrency bounds Deps.Concurrency (a Plex server is easily overwhelmed).
const maxConcurrency = 32

// Scan run statuses (models.ScanRun.Status).
const (
	runRunning   = "running"
	runCompleted = "completed"
	runFailed    = "failed"
)

// ErrNothingToScan is returned by TargetedScan for a body without rating keys or external ids.
var ErrNothingToScan = errors.New("targeted scan: no rating keys or external ids given")

// Service runs scans. It is safe for concurrent use: DuplicateScan and TargetedScan are separate
// commands (the command queue only keeps a command from running concurrently with itself), so
// scans that write groups are serialized inside the Service (a second scan waits for the first).
type Service struct {
	d Deps

	// scanMu serializes whole scans (full and targeted).
	scanMu sync.Mutex
	// groupMu serializes the read → evaluate → upsert cycle of a group between scans and
	// re-evaluations, so neither overwrites the other with stale data. Approvals take it too
	// (GroupLock). Held only around one group's cycle, never while calling Deps.AutoApprove.
	groupMu sync.Mutex

	// arrRetryDelay is the pause before an *arr read that failed is tried once more.
	arrRetryDelay time.Duration
}

// DefaultArrRetryDelay is the pause before retrying a failed *arr read: the *arr may have been
// changing the title while it was read ("changed during the scan") or restarting.
const DefaultArrRetryDelay = 3 * time.Second

// New returns a Service.
func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Concurrency <= 0 {
		d.Concurrency = DefaultConcurrency
	}
	if d.Concurrency > maxConcurrency {
		d.Concurrency = maxConcurrency
	}
	return &Service{d: d, arrRetryDelay: DefaultArrRetryDelay}
}

// GroupLock returns the lock of the read → evaluate → upsert cycle of a group (scans and
// re-evaluations). cmd/dupearr hands it to the executor (executor.Deps.GroupLock), which holds it
// while it approves a group: a scan or re-evaluation that read the group before an approval can
// then never store the status it read over the approval (a lost update that reopened the group
// as "pending" and cancelled the approved removals). The lock is only ever held around one
// group's cycle, and never while the scanner calls Deps.AutoApprove, so an approval waits at
// most for one group to be stored.
func (s *Service) GroupLock() sync.Locker { return &s.groupMu }

// now returns the current time in UTC.
func (s *Service) now() time.Time {
	if s.d.Now != nil {
		return s.d.Now().UTC()
	}
	return time.Now().UTC()
}

// publish sends an event on the bus (no-op without a bus).
func (s *Service) publish(name, action string, resource any) {
	if s.d.Bus == nil {
		return
	}
	s.d.Bus.Publish(events.Event{Name: name, Action: action, Resource: resource})
}

// FullScan scans the given (or all enabled) libraries; progress receives human messages.
//
// The returned run is completed when at least one library could be listed (per-library and
// per-item failures are counted in Stats.Errors) and failed — with a non-nil error — when every
// selected library failed or the scan could not run at all. A cancelled context aborts the scan
// without resolving anything.
func (s *Service) FullScan(ctx context.Context, body models.DuplicateScanBody, trigger string, progress func(string)) (*models.ScanRun, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	p, err := s.startRun(ctx, trigger, false, progress)
	if err != nil {
		return nil, err
	}
	return p.finish(p.safely(func() error { return p.runFull(body) }))
}

// TargetedScan runs the pipeline limited to specific rating keys / external ids (plus the
// cross-library partners of those items); it never marks other groups resolved. Groups that
// contained a targeted item and are no longer produced are resolved.
func (s *Service) TargetedScan(ctx context.Context, body models.TargetedScanBody, trigger string) (*models.ScanRun, error) {
	body = normalizeTargetBody(body)
	if len(body.RatingKeys) == 0 && body.TmdbID <= 0 && body.TvdbID <= 0 && body.ImdbID == "" {
		return nil, ErrNothingToScan
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	p, err := s.startRun(ctx, trigger, true, nil)
	if err != nil {
		return nil, err
	}
	return p.finish(p.safely(func() error { return p.runTargeted(body) }))
}

// startRun creates the ScanRun row (status running) and the pipeline state.
func (s *Service) startRun(ctx context.Context, trigger string, targeted bool, progress func(string)) (*pipeline, error) {
	if trigger == "" {
		trigger = models.TriggerManual
	}
	run := &models.ScanRun{
		Trigger:   trigger,
		Targeted:  targeted,
		Status:    runRunning,
		StartedAt: s.now(),
	}
	if err := s.d.Store.ScanRuns().Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create scan run: %w", err)
	}
	s.publish(events.NameScan, events.ActionUpdated, *run)
	return newPipeline(ctx, s, run, progress), nil
}
