// Package store defines Dupearr's persistence interfaces. internal/database implements them on
// SQLite; tests may use fakes. All methods are safe for concurrent use.
//
// Conventions:
//   - Get* return ErrNotFound (wrapped or bare) when a row does not exist.
//   - Create* set the ID (and CreatedAt/UpdatedAt where present) on the passed pointer.
//   - Times are stored as UTC RFC3339Nano text.
//   - JSON-ish fields (slices, maps, nested structs) are stored as JSON text columns.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Paging describes a page request (1-based page).
type Paging struct {
	Page          int    // ≥1
	PageSize      int    // 1..1000
	SortKey       string // repository-specific whitelist; unknown keys fall back to the default
	SortDirection string // "ascending" | "descending"
}

// Page is a page of results (mirrors the *arr paging resource).
type Page[T any] struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey"`
	SortDirection string `json:"sortDirection"`
	TotalRecords  int    `json:"totalRecords"`
	Records       []T    `json:"records"`
}

// GroupFilter filters duplicate group listings. Zero values mean "any".
type GroupFilter struct {
	Statuses  []models.GroupStatus
	MediaType models.MediaType
	LibraryID int64
	ServerID  int64
	Flag      string
	Search    string // case-insensitive substring of title/showTitle
	Keys      []string
}

// GroupStats are aggregate counters for the Duplicates page header.
type GroupStats struct {
	Total            int                        `json:"total"`
	ByStatus         map[models.GroupStatus]int `json:"byStatus"`
	ReclaimableBytes int64                      `json:"reclaimableBytes"` // over pending+review+queued groups
	ReclaimedBytes   int64                      `json:"reclaimedBytes"`   // sum of succeeded (non-dry-run) action sizes
}

// Store aggregates all repositories.
type Store interface {
	Settings() SettingsRepo
	Users() UserRepo
	MediaServers() MediaServerRepo
	Libraries() LibraryRepo
	ArrInstances() ArrInstanceRepo
	PathMappings() PathMappingRepo
	Profiles() ProfileRepo
	Groups() GroupRepo
	Actions() ActionRepo
	History() HistoryRepo
	Exclusions() ExclusionRepo
	Notifications() NotificationRepo
	ScanRuns() ScanRunRepo
	Commands() CommandRepo
	Tasks() TaskRepo

	// Ping verifies the DB is reachable.
	Ping(ctx context.Context) error
	// Vacuum compacts the database (Housekeeping).
	Vacuum(ctx context.Context) error
	// Path returns the SQLite file path (for backups/status).
	Path() string
	// BackupTo writes a consistent snapshot of the DB to dst (VACUUM INTO).
	BackupTo(ctx context.Context, dst string) error
	Close() error
}

// SettingsRepo stores the single models.Settings document (missing keys → defaults) and
// arbitrary internal key/value secrets (e.g. session signing key, plex client identifier).
type SettingsRepo interface {
	Get(ctx context.Context) (models.Settings, error)
	Save(ctx context.Context, s models.Settings) error
	GetValue(ctx context.Context, key string) (string, bool, error)
	SetValue(ctx context.Context, key, value string) error
}

// UserRepo stores the local account (at most one row in v1).
type UserRepo interface {
	Count(ctx context.Context) (int, error)
	GetByUsername(ctx context.Context, username string) (*models.User, error)
	GetByID(ctx context.Context, id int64) (*models.User, error)
	// Upsert creates the single user or replaces username/password of the existing one.
	Upsert(ctx context.Context, username, passwordHash string) (*models.User, error)
	DeleteAll(ctx context.Context) error
}

type MediaServerRepo interface {
	List(ctx context.Context) ([]models.MediaServer, error)
	Get(ctx context.Context, id int64) (*models.MediaServer, error)
	Create(ctx context.Context, s *models.MediaServer) error
	Update(ctx context.Context, s *models.MediaServer) error
	Delete(ctx context.Context, id int64) error // cascades libraries + path mappings of the server
}

type LibraryRepo interface {
	List(ctx context.Context) ([]models.Library, error)
	ListByServer(ctx context.Context, serverID int64) ([]models.Library, error)
	Get(ctx context.Context, id int64) (*models.Library, error)
	// Sync upserts the given sections for a server (matched by SectionKey), preserving user fields
	// (Enabled, ProfileID, ScopeGroup) of existing rows, and deletes rows not present.
	// New libraries default to Enabled=true.
	Sync(ctx context.Context, serverID int64, sections []models.Library) ([]models.Library, error)
	Update(ctx context.Context, l *models.Library) error
}

type ArrInstanceRepo interface {
	List(ctx context.Context) ([]models.ArrInstance, error)
	Get(ctx context.Context, id int64) (*models.ArrInstance, error)
	Create(ctx context.Context, a *models.ArrInstance) error
	Update(ctx context.Context, a *models.ArrInstance) error
	Delete(ctx context.Context, id int64) error // cascades path mappings of the instance
}

type PathMappingRepo interface {
	List(ctx context.Context) ([]models.PathMapping, error)
	Get(ctx context.Context, id int64) (*models.PathMapping, error)
	Create(ctx context.Context, m *models.PathMapping) error
	Update(ctx context.Context, m *models.PathMapping) error
	Delete(ctx context.Context, id int64) error
}

type ProfileRepo interface {
	List(ctx context.Context) ([]models.Profile, error)
	Get(ctx context.Context, id int64) (*models.Profile, error)
	GetDefault(ctx context.Context) (*models.Profile, error)
	Create(ctx context.Context, p *models.Profile) error
	Update(ctx context.Context, p *models.Profile) error // setting IsDefault clears it on others
	Delete(ctx context.Context, id int64) error          // refuses (error) to delete the default profile
}

// GroupRepo persists duplicate groups together with their files.
type GroupRepo interface {
	List(ctx context.Context, f GroupFilter, p Paging) (Page[models.DuplicateGroup], error) // Files populated
	Get(ctx context.Context, id int64) (*models.DuplicateGroup, error)                      // Files populated
	GetByKey(ctx context.Context, key string) (*models.DuplicateGroup, error)
	// ListByRatingKeys returns groups (Files populated) that contain a file whose version belongs to
	// one of the given media-server rating keys on serverID (used by targeted scans to resolve
	// groups that no longer have duplicates).
	ListByRatingKeys(ctx context.Context, serverID int64, ratingKeys []string) ([]models.DuplicateGroup, error)
	// Upsert inserts or updates a group by Key (and replaces its files, matched by Version.Key),
	// preserving: FirstSeenAt, Status=ignored, and each file's Override (by Version.Key).
	// Sets g.ID and file IDs. Returns created=true when the group did not exist before.
	//
	// Safety (internal/database): overrides of versions that left the group are retained and
	// restored when the version returns; a group with files but no keeper is stored as review; a
	// status that blocks removals (ignored, resolved, review, deferred, protected) cancels the
	// group's pending actions in the same transaction, and pending actions whose version is no
	// longer decided "remove" are cancelled.
	Upsert(ctx context.Context, g *models.DuplicateGroup) (created bool, err error)
	// UpdateStatus sets a group's status and reason. Moving it into a status that blocks removals
	// (ignored, resolved, review, deferred, protected) cancels its pending actions in the same
	// transaction; running actions are left to finish.
	UpdateStatus(ctx context.Context, id int64, status models.GroupStatus, reason string) error
	// UpdateStatusIf is UpdateStatus as an atomic compare-and-set: the status (and reason) is only
	// written while the group's current status is one of from, with the same side effects as
	// UpdateStatus. Reports whether it was written: false when the group has another status (a
	// concurrent change wins); an error wrapping ErrNotFound when the group does not exist.
	UpdateStatusIf(ctx context.Context, id int64, from []models.GroupStatus, to models.GroupStatus, reason string) (bool, error)
	// SetFlag atomically adds (on=true) or removes a flag (e.g. models.FlagPlaying) of a group,
	// leaving its status, decisions and files untouched. Idempotent; an error wrapping ErrNotFound
	// when the group does not exist.
	SetFlag(ctx context.Context, id int64, flag string, on bool) error
	// SetOverride sets/clears ("") a file's user override. Does not re-evaluate.
	SetOverride(ctx context.Context, groupID, fileID int64, d models.Decision) error
	// MarkUnseenResolved sets status=resolved for groups (not ignored/resolved) whose LastScanID
	// != scanID and that belong to one of libraryIDs. Returns the affected group IDs.
	MarkUnseenResolved(ctx context.Context, scanID int64, libraryIDs []int64) ([]int64, error)
	Stats(ctx context.Context) (GroupStats, error)
	Delete(ctx context.Context, id int64) error
}

// ActionRepo persists removal actions (the queue + their results).
//
// Safety (internal/database): a pending or running action is only created, re-queued or started
// while its target (GroupFileID and/or VersionKey) is stored with decision "remove", not
// protected, in a group that is neither ignored nor resolved (database.ErrNotRemovable). Update
// refuses status changes that would undo a cancellation or repeat a finished removal
// (database.ErrActionNotPending): running only from pending, pending never from cancelled or
// succeeded, cancelled only from pending or running. Cancelling the last queued action of a
// "queued" group reopens the group as "pending".
type ActionRepo interface {
	Create(ctx context.Context, a *models.Action) error
	Get(ctx context.Context, id int64) (*models.Action, error)
	// Update replaces every field of a except ID and CreatedAt (see the safety rules above).
	Update(ctx context.Context, a *models.Action) error
	// CancelIfPending atomically moves one action from pending to cancelled (message "Cancelled by
	// user", FinishedAt now), reopening its group like any cancellation. Reports false when the
	// action exists but is not pending (running, finished or already cancelled): a running removal
	// is never marked cancelled. An error wrapping ErrNotFound when the action does not exist.
	CancelIfPending(ctx context.Context, id int64) (bool, error)
	ListPending(ctx context.Context, limit int) ([]models.Action, error) // oldest first
	ListByGroup(ctx context.Context, groupID int64) ([]models.Action, error)
	// List returns actions filtered by statuses (empty = all), newest first.
	List(ctx context.Context, statuses []models.ActionStatus, p Paging) (Page[models.Action], error)
	// CancelPendingForGroup marks pending actions of a group cancelled.
	CancelPendingForGroup(ctx context.Context, groupID int64) (int, error)
	// ReclaimedBytes sums Size over succeeded, non-dry-run actions.
	ReclaimedBytes(ctx context.Context) (int64, error)
}

type HistoryRepo interface {
	Add(ctx context.Context, e *models.HistoryEvent) error
	List(ctx context.Context, eventTypes []string, groupID int64, p Paging) (Page[models.HistoryEvent], error)
	// DeleteOlderThan removes the events created before t, except security events
	// (models.EventSecurity), which only DeleteSecurityOlderThan removes.
	DeleteOlderThan(ctx context.Context, t time.Time) (int, error)
	// DeleteSecurityOlderThan removes the security events created before t.
	DeleteSecurityOlderThan(ctx context.Context, t time.Time) (int, error)
}

type ExclusionRepo interface {
	List(ctx context.Context) ([]models.Exclusion, error)
	Create(ctx context.Context, e *models.Exclusion) error
	Delete(ctx context.Context, id int64) error
}

type NotificationRepo interface {
	List(ctx context.Context) ([]models.NotificationConfig, error)
	Get(ctx context.Context, id int64) (*models.NotificationConfig, error)
	Create(ctx context.Context, n *models.NotificationConfig) error
	Update(ctx context.Context, n *models.NotificationConfig) error
	Delete(ctx context.Context, id int64) error
}

type ScanRunRepo interface {
	Create(ctx context.Context, r *models.ScanRun) error
	Update(ctx context.Context, r *models.ScanRun) error
	Latest(ctx context.Context) (*models.ScanRun, error) // ErrNotFound when none
	List(ctx context.Context, limit int) ([]models.ScanRun, error)
}

type CommandRepo interface {
	Create(ctx context.Context, c *models.Command) error
	Update(ctx context.Context, c *models.Command) error
	Get(ctx context.Context, id int64) (*models.Command, error)
	ListRecent(ctx context.Context, limit int) ([]models.Command, error) // newest first
	// FailRunning marks queued/started commands as aborted (called at startup after a crash).
	FailRunning(ctx context.Context) error
	DeleteOlderThan(ctx context.Context, t time.Time) (int, error)
}

type TaskRepo interface {
	List(ctx context.Context) ([]models.ScheduledTask, error)
	Get(ctx context.Context, taskName string) (*models.ScheduledTask, error)
	// Upsert saves task state keyed by TaskName.
	Upsert(ctx context.Context, t *models.ScheduledTask) error
}
