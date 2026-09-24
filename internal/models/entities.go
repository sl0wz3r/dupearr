package models

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Duplicate groups
// ---------------------------------------------------------------------------

// GroupStatus is the lifecycle state of a duplicate group (docs/ARCHITECTURE.md §4.2).
type GroupStatus string

const (
	GroupPending   GroupStatus = "pending"
	GroupReview    GroupStatus = "review"
	GroupDeferred  GroupStatus = "deferred"
	GroupProtected GroupStatus = "protected"
	GroupQueued    GroupStatus = "queued"
	GroupResolved  GroupStatus = "resolved"
	GroupIgnored   GroupStatus = "ignored"
	GroupFailed    GroupStatus = "failed"
)

// Group flags.
const (
	FlagCrossLibrary       = "cross_library"
	FlagDurationMismatch   = "duration_mismatch"
	FlagMultiEpisode       = "multi_episode"
	FlagStacked            = "stacked"
	FlagHardlinked         = "hardlinked"
	FlagMinAge             = "min_age"
	FlagMissingKeeperFile  = "missing_keeper_file"
	FlagEditionSplit       = "edition_split"
	FlagArrUntrackedKeeper = "arr_untracked_keeper"
	// docs/DECISIONS.md D4
	FlagUnanalyzed         = "unanalyzed"
	FlagUnavailableVersion = "unavailable_version"
	FlagSuspectMerge       = "suspect_merge"
	FlagVariant3D          = "variant_3d"
	FlagLanguageVariant    = "language_variant"
	FlagIntentionalArr     = "intentional_arr_instances"
	FlagArrQueueBusy       = "arr_queue_busy"
	FlagArrCutoffUnmet     = "arr_cutoff_unmet"
	FlagPlaying            = "playing"
	FlagSample             = "sample"
	FlagSameFile           = "same_file"
	// Full-disc backups (docs/research/disc-structures.md; docs/DECISIONS.md D9).
	FlagFullDisc       = "full_disc"         // the group contains a disc version (manual approval only)
	FlagDiscUnreadable = "disc_unreadable"   // a disc's metadata could not be read or verified (review)
	FlagDiscTracked    = "disc_tracked_clip" // an *arr tracks a single clip inside a disc
)

// Decision is what happens to a version.
type Decision string

const (
	DecisionKeep   Decision = "keep"
	DecisionRemove Decision = "remove"
)

// DuplicateGroup is a set of ≥2 versions representing the same content.
type DuplicateGroup struct {
	ID               int64             `json:"id"`
	Key              string            `json:"key"`
	MediaType        MediaType         `json:"mediaType"`
	Title            string            `json:"title"` // movie title or episode title
	Year             int               `json:"year"`
	ShowTitle        string            `json:"showTitle,omitempty"`
	Season           int               `json:"season,omitempty"`
	Episode          int               `json:"episode,omitempty"`
	ServerID         int64             `json:"serverId"`
	LibraryIDs       []int64           `json:"libraryIds"`
	ExternalIDs      map[string]string `json:"externalIds"`
	Thumb            string            `json:"thumb,omitempty"`
	Status           GroupStatus       `json:"status"`
	StatusReason     string            `json:"statusReason,omitempty"`
	Flags            []string          `json:"flags"`
	ProfileID        int64             `json:"profileId"`
	Files            []GroupFile       `json:"files"`
	ReclaimableBytes int64             `json:"reclaimableBytes"` // sum of sizes of versions decided "remove"
	FirstSeenAt      time.Time         `json:"firstSeenAt"`
	LastSeenAt       time.Time         `json:"lastSeenAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	LastScanID       int64             `json:"lastScanId"`
	// Signature is a hash of the sorted version keys + effective decisions; StableCount is the
	// number of consecutive scans that produced the same signature (auto mode requires
	// settings.StableScansRequired). docs/DECISIONS.md D4.
	Signature   string `json:"signature"`
	StableCount int    `json:"stableCount"`
}

// HasFlag reports whether the group carries flag f.
func (g *DuplicateGroup) HasFlag(f string) bool {
	for _, x := range g.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// GroupFile is a version inside a group plus its decision.
type GroupFile struct {
	ID      int64        `json:"id"`
	GroupID int64        `json:"groupId"`
	Version MediaVersion `json:"version"`

	// Decision is the effective decision (Override when set, else the engine's decision).
	Decision Decision `json:"decision"`
	// EngineDecision is what the profile decided, before user overrides.
	EngineDecision Decision `json:"engineDecision"`
	// Override is the user's manual decision ("" = none). Survives re-scans.
	Override Decision `json:"override,omitempty"`

	Rank              int      `json:"rank"`              // 1 = best
	Reasons           []string `json:"reasons"`           // human readable explanation lines
	DecidingCriterion string   `json:"decidingCriterion"` // criterion type that decided vs the best keeper
	Protected         bool     `json:"protected"`
	ProtectedReason   string   `json:"protectedReason,omitempty"`
	// Values maps criterion type → display value for the comparison table.
	Values map[string]string `json:"values"`
}

// ---------------------------------------------------------------------------
// Decision profiles
// ---------------------------------------------------------------------------

// CriterionType identifies a comparison criterion (docs/ARCHITECTURE.md §4.3).
type CriterionType string

const (
	CritResolution         CriterionType = "resolution"
	CritDynamicRange       CriterionType = "dynamic_range"
	CritSource             CriterionType = "source"
	CritVideoCodec         CriterionType = "video_codec"
	CritAudioFormat        CriterionType = "audio_format"
	CritContainer          CriterionType = "container"
	CritLibrary            CriterionType = "library"
	CritAudioChannels      CriterionType = "audio_channels"
	CritVideoBitrate       CriterionType = "video_bitrate"
	CritFileSize           CriterionType = "file_size"
	CritBitDepth           CriterionType = "bit_depth"
	CritCustomFormatScore  CriterionType = "custom_format_score"
	CritDateAdded          CriterionType = "date_added"
	CritAudioTrackCount    CriterionType = "audio_track_count"
	CritSubtitleTrackCount CriterionType = "subtitle_track_count"
	CritArrManaged         CriterionType = "arr_managed"
	CritAudioLanguage      CriterionType = "audio_language"
	CritFilenameScore      CriterionType = "filename_score"
	CritHealth             CriterionType = "health" // docs/DECISIONS.md D4: always first in templates
)

// Direction for numeric criteria.
const (
	DirectionHigher = "higher"
	DirectionLower  = "lower"
)

// PatternScore is one filename/path pattern with the score it adds.
type PatternScore struct {
	Pattern       string `json:"pattern"`
	Score         int    `json:"score"`
	Regex         bool   `json:"regex"`         // false = glob (doublestar) against the full path
	CaseSensitive bool   `json:"caseSensitive"` // default false
}

// Criterion is one step of a profile's tiebreaker chain.
type Criterion struct {
	Type             CriterionType  `json:"type"`
	Enabled          bool           `json:"enabled"`
	Direction        string         `json:"direction,omitempty"`        // numeric: higher|lower
	Order            []string       `json:"order,omitempty"`            // ordered: best first
	TolerancePercent float64        `json:"tolerancePercent,omitempty"` // numeric equality tolerance (relative)
	MinDelta         float64        `json:"minDelta,omitempty"`         // numeric equality tolerance (absolute), e.g. CF score 10
	Patterns         []PatternScore `json:"patterns,omitempty"`         // filename_score
	Value            string         `json:"value,omitempty"`            // arr_managed instance id / audio_language code
}

// Protection types.
const (
	ProtectPathGlob    = "path_glob"
	ProtectLibrary     = "library"
	ProtectArrInstance = "arr_instance"
	ProtectArrTag      = "arr_tag" // *arr movie/series tag label, default "dupearr-keep"
)

// Protection marks versions that must never be removed.
type Protection struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// KeepPer values.
const (
	KeepPerNone         = ""
	KeepPerResolution   = "resolution"
	KeepPerDynamicRange = "dynamic_range"
)

// Profile is a decision profile.
type Profile struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	IsDefault   bool         `json:"isDefault"`
	Criteria    []Criterion  `json:"criteria"`
	KeepCount   int          `json:"keepCount"` // ≥1
	KeepPer     string       `json:"keepPer"`
	Protections []Protection `json:"protections"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

// MediaServerKind identifies a media server implementation.
type MediaServerKind string

const (
	MediaServerPlex MediaServerKind = "plex"
)

// MediaServer is a configured media server connection.
type MediaServer struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Kind              MediaServerKind `json:"kind"`
	URL               string          `json:"url"`   // e.g. http://192.168.1.10:32400
	Token             string          `json:"token"` // X-Plex-Token (masked in API responses)
	MachineIdentifier string          `json:"machineIdentifier"`
	VerifyTLS         bool            `json:"verifyTls"`
	Enabled           bool            `json:"enabled"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

// Library is a media-server library section known to Dupearr.
type Library struct {
	ID         int64     `json:"id"`
	ServerID   int64     `json:"serverId"`
	SectionKey string    `json:"sectionKey"`
	Title      string    `json:"title"`
	Type       string    `json:"type"`      // plex section type: "movie" | "show"
	Locations  []string  `json:"locations"` // library root paths as the server sees them
	Enabled    bool      `json:"enabled"`
	ProfileID  *int64    `json:"profileId"`  // nil = default profile
	ScopeGroup string    `json:"scopeGroup"` // libraries sharing a non-empty scope group are cross-matched
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ArrKind identifies an *arr application.
type ArrKind string

const (
	ArrRadarr ArrKind = "radarr"
	ArrSonarr ArrKind = "sonarr"
)

// ArrInstance is a configured Radarr/Sonarr instance.
type ArrInstance struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      ArrKind   `json:"kind"`
	URL       string    `json:"url"`    // base url incl. url base, e.g. http://radarr:7878
	APIKey    string    `json:"apiKey"` // masked in API responses
	VerifyTLS bool      `json:"verifyTls"`
	Enabled   bool      `json:"enabled"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PathMapping source types.
const (
	PathSourceServer = "server"
	PathSourceArr    = "arr"
)

// PathMapping translates a remote path prefix (as a server/*arr sees it) to a local one.
type PathMapping struct {
	ID         int64  `json:"id"`
	SourceType string `json:"sourceType"` // PathSourceServer | PathSourceArr
	SourceID   int64  `json:"sourceId"`   // media server id or arr instance id
	RemotePath string `json:"remotePath"`
	LocalPath  string `json:"localPath"`
}

// Exclusion kinds.
const (
	ExcludeGroupKey   = "group_key"
	ExcludePathPrefix = "path_prefix"
	ExcludeLibrary    = "library"
	ExcludeRegex      = "title_regex"
)

// Exclusion removes content from duplicate detection entirely.
type Exclusion struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	Title     string    `json:"title,omitempty"` // display (e.g. the group title)
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// Actions, history, scans, commands, notifications
// ---------------------------------------------------------------------------

// ActionStatus is the state of a removal action.
type ActionStatus string

const (
	ActionPending   ActionStatus = "pending"
	ActionRunning   ActionStatus = "running"
	ActionSucceeded ActionStatus = "succeeded"
	ActionDryRun    ActionStatus = "dry_run"
	ActionSkipped   ActionStatus = "skipped"
	ActionFailed    ActionStatus = "failed"
	ActionCancelled ActionStatus = "cancelled"
)

// Deletion methods.
const (
	MethodArr        = "arr"
	MethodPlex       = "plex"
	MethodFilesystem = "filesystem"
)

// Action is one queued/executed removal of a version.
type Action struct {
	ID          int64        `json:"id"`
	GroupID     int64        `json:"groupId"`
	GroupFileID int64        `json:"groupFileId"`
	VersionKey  string       `json:"versionKey"`
	Title       string       `json:"title"` // display title of the group
	Paths       []string     `json:"paths"` // server-side paths of the version's parts
	Size        int64        `json:"size"`
	Method      string       `json:"method,omitempty"` // chosen method once executed
	Status      ActionStatus `json:"status"`
	DryRun      bool         `json:"dryRun"`
	Message     string       `json:"message,omitempty"`
	RecyclePath string       `json:"recyclePath,omitempty"` // where the file went (filesystem + recycle bin)
	Permanent   bool         `json:"permanent"`             // true when the removal cannot be undone (no recycle bin)
	CreatedAt   time.Time    `json:"createdAt"`
	StartedAt   *time.Time   `json:"startedAt,omitempty"`
	FinishedAt  *time.Time   `json:"finishedAt,omitempty"`
}

// History event types.
const (
	EventScanCompleted    = "scanCompleted"
	EventScanFailed       = "scanFailed"
	EventGroupDetected    = "groupDetected"
	EventGroupApproved    = "groupApproved"
	EventGroupIgnored     = "groupIgnored"
	EventGroupUnignored   = "groupUnignored"
	EventGroupResolved    = "groupResolved"
	EventFileDeleted      = "fileDeleted"
	EventFileDeleteDryRun = "fileDeleteDryRun"
	EventFileDeleteFailed = "fileDeleteFailed"
	EventFileSkipped      = "fileSkipped"
	EventFileRestored     = "fileRestored"
	EventOverrideChanged  = "overrideChanged"
	// EventSecurity is a security-relevant event (sign-ins, credential, authentication, removal
	// and connection setting changes, backup downloads; internal/audit). It is independent of the
	// log level and kept for at least SecurityHistoryRetentionDays.
	EventSecurity = "security"
)

// SecurityHistoryRetentionDays is the minimum number of days security events stay in the history,
// whatever Settings.HistoryRetentionDays says (a restored backup or a session must not be able to
// erase the record of what it did by shortening the retention).
const SecurityHistoryRetentionDays = 365

// HistoryEvent is an audit-log entry.
type HistoryEvent struct {
	ID        int64           `json:"id"`
	EventType string          `json:"eventType"`
	GroupID   *int64          `json:"groupId,omitempty"`
	ActionID  *int64          `json:"actionId,omitempty"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ScanStats summarizes a scan run.
type ScanStats struct {
	Libraries        int   `json:"libraries"`
	ItemsExamined    int   `json:"itemsExamined"`
	GroupsFound      int   `json:"groupsFound"`
	NewGroups        int   `json:"newGroups"`
	ResolvedGroups   int   `json:"resolvedGroups"`
	PendingGroups    int   `json:"pendingGroups"`
	ReviewGroups     int   `json:"reviewGroups"`
	ReclaimableBytes int64 `json:"reclaimableBytes"`
	AutoApproved     int   `json:"autoApproved"`
	Errors           int   `json:"errors"`
}

// ScanRun is one execution of the scan pipeline.
type ScanRun struct {
	ID         int64      `json:"id"`
	Trigger    string     `json:"trigger"`  // manual | scheduled | webhook
	Targeted   bool       `json:"targeted"` // targeted scans never resolve unseen groups
	Status     string     `json:"status"`   // running | completed | failed
	Stats      ScanStats  `json:"stats"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Command status / trigger values.
const (
	CommandQueued    = "queued"
	CommandStarted   = "started"
	CommandCompleted = "completed"
	CommandFailed    = "failed"
	CommandAborted   = "aborted"

	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
	TriggerWebhook   = "webhook"
)

// Command names.
const (
	CmdDuplicateScan   = "DuplicateScan"
	CmdTargetedScan    = "TargetedScan"
	CmdProcessQueue    = "ProcessQueue"
	CmdSyncLibraries   = "SyncLibraries"
	CmdCheckHealth     = "CheckHealth"
	CmdBackup          = "Backup"
	CmdHousekeeping    = "Housekeeping"
	CmdCleanRecycleBin = "CleanRecycleBin"
)

// Command is a queued/executed background command (like *arr /api/v3/command).
type Command struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Body      json.RawMessage `json:"body"`
	Status    string          `json:"status"`
	Trigger   string          `json:"trigger"`
	Message   string          `json:"message,omitempty"`
	QueuedAt  time.Time       `json:"queued"`
	StartedAt *time.Time      `json:"started,omitempty"`
	EndedAt   *time.Time      `json:"ended,omitempty"`
	Duration  string          `json:"duration,omitempty"` // "00:00:12.345"
}

// TargetedScanBody is the body of a TargetedScan command.
type TargetedScanBody struct {
	ServerID   int64    `json:"serverId,omitempty"`
	RatingKeys []string `json:"ratingKeys,omitempty"`
	TmdbID     int      `json:"tmdbId,omitempty"`
	TvdbID     int      `json:"tvdbId,omitempty"`
	ImdbID     string   `json:"imdbId,omitempty"`
}

// DuplicateScanBody is the body of a DuplicateScan command.
type DuplicateScanBody struct {
	LibraryIDs []int64 `json:"libraryIds,omitempty"`
}

// ScheduledTask is the persisted state of a recurring task.
type ScheduledTask struct {
	Name            string     `json:"name"`     // human name, e.g. "Duplicate Scan"
	TaskName        string     `json:"taskName"` // command name
	IntervalMinutes int        `json:"interval"` // 0 = disabled
	LastExecution   *time.Time `json:"lastExecution,omitempty"`
	LastStartTime   *time.Time `json:"lastStartTime,omitempty"`
	LastDuration    string     `json:"lastDuration,omitempty"`
	NextExecution   *time.Time `json:"nextExecution,omitempty"`
}

// Notification event triggers.
const (
	OnDuplicatesFound = "onDuplicatesFound"
	OnFileDeleted     = "onFileDeleted"
	OnDeleteFailed    = "onDeleteFailed"
	OnScanCompleted   = "onScanCompleted"
	OnHealthIssue     = "onHealthIssue"
	OnHealthRestored  = "onHealthRestored"
)

// NotificationConfig is a configured notification connection (Settings → Connect).
type NotificationConfig struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`     // discord|slack|telegram|pushover|gotify|ntfy|apprise|webhook|email
	Settings json.RawMessage `json:"settings"` // provider-specific fields (see notifications schema)
	Triggers []string        `json:"triggers"` // On* values
	Enabled  bool            `json:"enabled"`
}

// HealthType mirrors *arr health levels.
type HealthType string

const (
	HealthOK      HealthType = "ok"
	HealthNotice  HealthType = "notice"
	HealthWarning HealthType = "warning"
	HealthError   HealthType = "error"
)

// HealthCheck is one health issue.
type HealthCheck struct {
	Source  string     `json:"source"` // check name, e.g. "PlexConnectivityCheck"
	Type    HealthType `json:"type"`
	Message string     `json:"message"`
	WikiURL string     `json:"wikiUrl,omitempty"`
}

// User is the single local account for Forms/Basic auth.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// Settings (DB)
// ---------------------------------------------------------------------------

// Mode values.
const (
	ModeManual = "manual"
	ModeAuto   = "auto"
)

// Settings are the operational settings stored in the DB (docs/ARCHITECTURE.md §4.4).
type Settings struct {
	DryRun                           bool     `json:"dryRun"`
	Mode                             string   `json:"mode"`
	ScanIntervalMinutes              int      `json:"scanIntervalMinutes"`
	MinAgeHours                      int      `json:"minAgeHours"`
	MaxDeletionsPerRun               int      `json:"maxDeletionsPerRun"`
	DeletionMethods                  []string `json:"deletionMethods"`
	ArrRescanAfterDelete             bool     `json:"arrRescanAfterDelete"`
	UnmonitorWhenKeeperElsewhere     bool     `json:"unmonitorWhenKeeperElsewhere"`
	AddExclusionWhenKeeperElsewhere  bool     `json:"addExclusionWhenKeeperElsewhere"`
	RecycleBinPath                   string   `json:"recycleBinPath"`
	RecycleBinCleanupDays            int      `json:"recycleBinCleanupDays"`
	RefreshPlexAfterDelete           bool     `json:"refreshPlexAfterDelete"`
	CleanupPlexStaleEntries          bool     `json:"cleanupPlexStaleEntries"`
	TreatEditionsAsDistinct          bool     `json:"treatEditionsAsDistinct"`
	Treat3DAsDistinct                bool     `json:"treat3DAsDistinct"`
	LanguageVariantsAsDistinct       bool     `json:"languageVariantsAsDistinct"`
	DifferentArrInstancesIntentional bool     `json:"differentArrInstancesIntentional"`
	MaxGroupSize                     int      `json:"maxGroupSize"`
	StableScansRequired              int      `json:"stableScansRequired"`
	MaxBytesPerRunGB                 int      `json:"maxBytesPerRunGb"`
	DurationTolerancePercent         float64  `json:"durationTolerancePercent"`
	DurationToleranceMinutes         int      `json:"durationToleranceMinutes"`
	HistoryRetentionDays             int      `json:"historyRetentionDays"`
	BackupIntervalDays               int      `json:"backupIntervalDays"`
	BackupRetentionDays              int      `json:"backupRetentionDays"`
	// Full-disc backups (docs/DECISIONS.md D9). DetectDiscs looks for BDMV / VIDEO_TS / ISO backups
	// in the (mapped) folders of the scanned movies; AllowDiscRemoval lets a person approve the
	// removal of a whole disc (manual approval, filesystem method, recycle bin only);
	// KeepPlayableCopy never lets a disc be the only kept copy (Plex cannot play discs).
	DetectDiscs      bool `json:"detectDiscs"`
	AllowDiscRemoval bool `json:"allowDiscRemoval"`
	KeepPlayableCopy bool `json:"keepPlayableCopy"`
}

// DefaultSettings returns the safe defaults (dry run ON, manual mode).
func DefaultSettings() Settings {
	return Settings{
		DryRun:                           true,
		Mode:                             ModeManual,
		ScanIntervalMinutes:              360,
		MinAgeHours:                      168,
		MaxDeletionsPerRun:               25,
		DeletionMethods:                  []string{MethodArr, MethodPlex, MethodFilesystem},
		ArrRescanAfterDelete:             true,
		UnmonitorWhenKeeperElsewhere:     true,
		AddExclusionWhenKeeperElsewhere:  false,
		RecycleBinPath:                   "",
		RecycleBinCleanupDays:            7,
		RefreshPlexAfterDelete:           true,
		CleanupPlexStaleEntries:          true,
		TreatEditionsAsDistinct:          true,
		Treat3DAsDistinct:                true,
		LanguageVariantsAsDistinct:       true,
		DifferentArrInstancesIntentional: true,
		MaxGroupSize:                     4,
		StableScansRequired:              2,
		MaxBytesPerRunGB:                 500,
		DurationTolerancePercent:         10,
		DurationToleranceMinutes:         5,
		HistoryRetentionDays:             90,
		BackupIntervalDays:               7,
		BackupRetentionDays:              28,
		DetectDiscs:                      true,
		AllowDiscRemoval:                 false,
		KeepPlayableCopy:                 true,
	}
}
