# Dupearr — Package Contracts (exported Go API)

Companion to `docs/ARCHITECTURE.md`. **`docs/DECISIONS.md` (research-driven) overrides this file
where they conflict.** The compiling skeleton in the repo is the ground truth for signatures. Every package MUST expose exactly these exported names and
signatures (extra unexported helpers are fine; extra exported helpers are fine if additive).
Additions made during implementation and review are recorded in each package's block (marked
`// added`); `go doc ./internal/<pkg>` has the full documentation.
Domain types live in `internal/models` (already written) and persistence interfaces in
`internal/store` (already written). Module path: `github.com/sl0wz3r/dupearr`.

Conventions for all packages:
- `ctx context.Context` first; return `error` last; wrap errors with `fmt.Errorf("...: %w", err)`.
- Logging via `*slog.Logger` passed in deps (never the global logger in library code).
- HTTP clients: timeouts always set (default 30s), `User-Agent: Dupearr/<version>`.
- No package-level mutable state except in `version`.
- Tests: table-driven, stdlib `testing` + `net/http/httptest`; no network access in tests.

---

## internal/version
```go
var (
    Version   = "0.1.0-dev" // -ldflags "-X github.com/sl0wz3r/dupearr/internal/version.Version=..."
    Commit    = "unknown"
    BuildDate = ""
)
func UserAgent() string // "Dupearr/<Version>"
```

## internal/config
```go
const (
    AuthNone = "None"; AuthBasic = "Basic" /* legacy: migrated to Forms on load */; AuthForms = "Forms"; AuthExternal = "External"
    AuthRequiredEnabled = "Enabled"; AuthRequiredDisabledForLocal = "DisabledForLocalAddresses"
    DefaultPort = 3873; DefaultSslPort = 9873
    MaxLogSizeLimit = 10                        // added: LogSizeLimit range 1–10 MB
    EnvPrefix = "DUPEARR__"; FileName = "config.xml" // added
)
// Config mirrors config.xml (<Config> root). Booleans serialize as "True"/"False" like *arr.
type Config struct {
    BindAddress            string // "*"
    Port                   int
    UrlBase                string // normalized: "" or "/something" (leading slash, no trailing)
    EnableSsl              bool
    SslPort                int
    SslCertPath            string
    SslKeyPath             string
    ApiKey                 string // 32 lowercase hex
    AuthenticationMethod   string // Auth*
    AuthenticationRequired string // AuthRequired*
    TrustedProxies         string // added (issue #1): reverse proxies (IPs / CIDR ranges), "a, b"; checked by internal/auth
    AllowedHosts           string // added (issue #1): host names ("*.example.com": sub-domains), "a, b"
    LogLevel               string // trace|debug|info|warn|error
    LogSizeLimit           int    // MB per log file
    InstanceName           string
    LaunchBrowser          bool
    Branch                 string
}
func Default() Config
func GenerateAPIKey() string
// Manager owns config.xml in dataDir. Load creates the file with defaults (new ApiKey) when
// missing, fills missing elements with defaults, then applies DUPEARR__SECTION__KEY env overrides
// in memory (overrides are NOT written back). Unknown XML elements are preserved on save.
type Manager struct{ /* unexported */ }
func Load(dataDir string) (*Manager, error)
func (m *Manager) Get() Config                         // copy, overrides applied
func (m *Manager) Update(fn func(c *Config)) (Config, error) // validate + persist + notify subscribers
func (m *Manager) Path() string
func (m *Manager) DataDir() string
func (m *Manager) OnChange(fn func(old, new Config))
func (m *Manager) EnvOverrides() []string // names of keys currently overridden by env (UI shows them read-only)
func Validate(c Config) []ValidationError
type ValidationError struct{ PropertyName, ErrorMessage string }
type ValidationErrors []ValidationError                 // added: error type of a failed Update
func Normalize(c Config) Config                         // added: canonical form (Basic → Forms, UrlBase "/x", …)
func NormalizeURLBase(s string) string                  // added
func NormalizeList(s string) string                     // added: a list setting as "a, b" (TrustedProxies, AllowedHosts)
func SplitList(s string) []string                       // added: its entries (commas, semicolons, white space separate)
func LookupEnv(name string) (string, bool)              // added: env override lookup exactly as Load does it (healthcheck)
```

## internal/logging
```go
// Setup configures a slog logger writing to stdout and to dataDir/logs/dupearr.txt with *arr-style
// rotation (dupearr.txt → dupearr.0.txt … dupearr.N.txt, N = 5) at sizeLimitMB. Also keeps an
// in-memory ring buffer (1000 entries) for System → Events.
func Setup(logDir, level string, sizeLimitMB int) (*slog.Logger, *Manager, error)
type Manager struct{ /* unexported */ }
func (m *Manager) SetLevel(level string) error
func (m *Manager) SetSizeLimit(sizeLimitMB int)             // added: applied on config change
func (m *Manager) Level() string
func (m *Manager) Files() ([]LogFile, error)                // newest first
func (m *Manager) FilePath(name string) (string, error)     // validates name (no traversal)
func (m *Manager) Recent(p store.Paging, level string) store.Page[Entry]
func (m *Manager) Close() error
type LogFile struct { Filename string `json:"filename"`; LastWriteTime time.Time `json:"lastWriteTime"`; Size int64 `json:"size"` }
type Entry struct { Time time.Time `json:"time"`; Level string `json:"level"`; Logger string `json:"logger"`; Message string `json:"message"`; Exception string `json:"exception,omitempty"` }
// Redact masks secrets (api keys, tokens, passwords) in URLs and strings before logging.
func Redact(s string) string
const LevelTrace = slog.Level(-8)                           // added
func ParseLevel(s string) (slog.Level, error); func LevelName(l slog.Level) string // added
var ErrFileNotFound = errors.New("log file not found")     // added
```

## internal/events
```go
type Event struct {
    Name     string `json:"name"`     // resource: "duplicate","queue","history","command","health","task","scan","settings"
    Action   string `json:"action"`   // "updated","deleted","sync","progress"
    Resource any    `json:"resource,omitempty"`
}
type Bus struct{ /* unexported */ }
func New() *Bus
func (b *Bus) Publish(e Event)                              // non-blocking; drops for slow subscribers
func (b *Bus) Subscribe(buffer int) (<-chan Event, func())  // returns channel + unsubscribe
func (b *Bus) Subscribers() int                             // added
const NameDuplicate … NameSettings = "duplicate" … "settings"; ActionUpdated … ActionProgress // added
const DefaultBuffer = 64                                    // added: buffer ≤ 0
```

## internal/mediainfo (pure normalizers; shared by plex, arr, engine)
```go
func ResolutionTier(width, height int, plexVideoResolution string) string          // models.Res*
func VideoCodec(raw string) string                                                   // models.VCodec*
func AudioFormat(codec, profile, title string, channels int) (format string, atmos bool) // models.Audio*
func DynamicRange(colorTrc, colorPrimaries string, doviPresent bool, doviProfile int, doviBLCompatID int, hdr10Plus bool, displayTitle string) (models.DynamicRange, int /*dvProfile*/)
func DynamicRangeFromArr(videoDynamicRangeType string) models.DynamicRange          // "DV HDR10" → dv_hdr10 …
func SourceFromArr(qualitySource, qualityModifier string) string                     // models.Source*
func SourceFromPath(path string) string                                              // filename tokens; a path inside a disc structure or a disc image → models.SourceDisc (D9)
func Edition(path, plexEditionTitle, arrEdition string) string                       // normalized display edition, "" = none
func EditionKey(edition string) string                                               // lowercase slug for grouping, "" = none
func ContainerFromPath(path, plexContainer string) string                             // mkv|mp4|m4v|m2ts|avi|ts|other (.m2ts/.mts/.m2t → "m2ts", D9)
func ResolutionLabel(tier string) string                                             // "2160" → "2160p (4K)", "sd" → "SD"
func EditionWithTitles(path, plexEditionTitle, arrEdition string, titles ...string) string // added: also reads editions before the year
func Is3D(path string) bool                                                          // added: the one 3D detector (engine "#3d" split)
func IsSamplePath(path string) bool                                                  // added
func LanguageCode(s string) string                                                   // added: ISO 639 normalisation
```

## internal/pathmap
```go
type Mapper struct{ /* unexported */ }
func New(mappings []models.PathMapping) *Mapper
// ToLocal translates a remote path using the longest matching RemotePath prefix for that source
// (path-segment aware: "/data/movies" must not match "/data/movies2"). ok=false when no mapping.
func (m *Mapper) ToLocal(sourceType string, sourceID int64, remote string) (local string, ok bool)
func (m *Mapper) ToRemote(sourceType string, sourceID int64, local string) (remote string, ok bool)
// Normalize cleans a path for comparison (forward slashes, no trailing slash, Clean; Windows
// drive letters lower-cased; UNC preserved).
func Normalize(p string) string
// Stat returns size and hardlink count of a local file (linkCount 0 when unknown/unsupported).
func Stat(local string) (size int64, linkCount int, err error)
```

## internal/integrations/plex
```go
type Options struct {
    ClientIdentifier string        // stable per install (store.SettingsRepo value "plex.clientIdentifier")
    Product          string        // "Dupearr"
    Version          string
    VerifyTLS        bool
    Timeout          time.Duration // default 30s
    HTTPClient       *http.Client  // optional (tests)
}
type Client struct{ /* unexported */ }
func New(baseURL, token string, opts Options) *Client

type Identity struct { MachineIdentifier, Version, FriendlyName string }
type Section  struct { Key, Type, Title, UUID string; Locations []string }
// ItemRef is a lightweight listing row.
type ItemRef struct {
    RatingKey  string
    MediaType  models.MediaType
    Title      string
    Year       int
    ShowTitle  string
    Season     int
    Episode    int
    GUID       string            // plex://movie/… or legacy
    ExternalIDs map[string]string // from Guid[] (tmdb/imdb/tvdb)
    MediaCount int        // NON-optimized media count
    Media      []MediaRef // {ID, Optimized, Width, Height, DurationMs, Parts []PartRef{ID, File, Size}}
    AddedAt    time.Time
}

// Identity: GET / (machineIdentifier). The executor reads it before re-verifying a group and
// right before every Plex delete (a re-pointed URL must never delete on another server).
func (c *Client) Identity(ctx context.Context) (*Identity, error)
func (c *Client) Sections(ctx context.Context) ([]Section, error)
// DuplicateItems lists items with ≥2 Media in a section (duplicate=1, includeGuids=1, paginated).
func (c *Client) DuplicateItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]ItemRef, error)
// AllItems lists every movie (mt=movie) or episode (mt=episode) with guids (paginated).
// DuplicateItems/AllItems: a listing that ends early (an empty page before the reported total) is
// an error, never a silently truncated result.
func (c *Client) AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]ItemRef, error)
// Item fetches one item's detail (checkFiles=1, includeGuids=1) and maps it to a MediaItem with
// all Versions (streams → normalized attributes via mediainfo). For episodes it also fills ShowIDs
// (grandparent show guids, cached per client). Optimized versions are returned with
// OptimizedVersion=true (callers skip them). ServerID/LibraryID are left 0 for the caller to set.
func (c *Client) Item(ctx context.Context, ratingKey string) (*models.MediaItem, error)
func (c *Client) DeleteMedia(ctx context.Context, ratingKey string, mediaID int64) error
func (c *Client) RefreshItem(ctx context.Context, ratingKey string) error
func (c *Client) ScanPath(ctx context.Context, sectionKey, dir string) error
// EmptyTrash: removed — section-wide "empty trash" is never called (D6).
func (c *Client) MediaDeletionAllowed(ctx context.Context) (bool, error) // GET / allowMediaDeletion; absent ⇒ false
func (c *Client) ActiveSessions(ctx context.Context) (map[string]bool, error) // GET /status/sessions rating keys
// Photo proxies a thumb (transcoded to w×h via /photo/:/transcode) for the UI poster endpoint.
func (c *Client) Photo(ctx context.Context, thumbPath string, w, h int) (body io.ReadCloser, contentType string, err error)

// Errors
var ErrUnauthorized = errors.New("plex: unauthorized (check token)")
var ErrNotFound     = errors.New("plex: not found")
var ErrDeletionNotAllowed = errors.New("plex: media deletion is disabled in Plex server settings")
var ErrForbidden, ErrInvalidArgument error // added: 403 (not the owner's token); input refused before any request
type StatusError struct { Method, Path string; StatusCode int; Body string } // added: other unexpected statuses

// plex.tv
type Pin      struct { ID int64; Code string; AuthToken string; ExpiresAt time.Time }
type Resource struct { Name, ClientIdentifier, ProductVersion, Platform string; Owned bool; AccessToken string; Connections []Connection }
type Connection struct { URI, Address string; Port int; Protocol string; Local, Relay bool }
func CreatePin(ctx context.Context, opts Options) (*Pin, error)
func CheckPin(ctx context.Context, opts Options, id int64) (*Pin, error)   // AuthToken "" until the user signs in
func AuthURL(opts Options, code string) string                             // https://app.plex.tv/auth#?...
func Servers(ctx context.Context, opts Options, token string) ([]Resource, error) // only "server" resources
// added: is token the owner's for server machineID? known=false when plex.tv does not list it;
// callers bound ctx (5 s) and treat errors as unknown (docs/API.md `owned`).
func OwnerOf(ctx context.Context, opts Options, token, machineID string) (owned, known bool, err error)
func (c *Client) Ownership(ctx context.Context, machineID string) (owned, known bool, err error)
// Webhook payload (Plex Pass webhooks, multipart field "payload").
type WebhookPayload struct { Event string; Server struct{ UUID, Title string }; Metadata struct{ RatingKey, Type, Title, GrandparentTitle, LibrarySectionID string /* … */ } }
func ParseWebhook(r *http.Request) (*WebhookPayload, error)
```

## internal/integrations/arr
```go
type Options struct { VerifyTLS bool; Timeout time.Duration; HTTPClient *http.Client }
type Client struct{ /* unexported */ }
func New(inst models.ArrInstance, opts Options) *Client

type SystemStatus struct { AppName, InstanceName, Version string }
func (c *Client) Status(ctx context.Context) (*SystemStatus, error) // also validates kind matches AppName

// TrackedFile is an *arr-tracked file normalized for matching against media-server versions.
type TrackedFile struct {
    Path        string              // as the *arr sees it
    Size        int64
    Info        models.ArrFileInfo  // InstanceID/Name/Kind filled
    TmdbID      int
    ImdbID      string
    TvdbID      int                 // series tvdb id for Sonarr
    Season      int
    Episodes    []int               // episode numbers covered (multi-episode files → several)
    MediaInfo   *MediaInfo          // added: the *arr's ffprobe summary (nil when not analysed)
}
type TrackedFilter struct { TmdbIDs map[int]bool; ImdbIDs map[string]bool; TvdbIDs map[int]bool } // nil maps = no filter
// TrackedFiles returns every file the instance tracks (Radarr: from /movie embedded movieFile;
// Sonarr: /series filtered by TvdbIDs/ImdbIDs, then /episodefile + /episode per series).
func (c *Client) TrackedFiles(ctx context.Context, f TrackedFilter) ([]TrackedFile, error)
func (c *Client) DeleteFile(ctx context.Context, fileID int64) error        // moviefile / episodefile
// added: what a file id names right now (GET moviefile/{id} | episodefile/{id}); the path is
// rebuilt from the item folder + relativePath when the *arr omits it, never guessed. A 404 wraps
// ErrNotFound. The executor calls it when planning an *arr removal and right before DeleteFile.
type TrackedFileRef struct { Path string; Size int64; ItemID int64 /* movieId | seriesId */ }
func (c *Client) File(ctx context.Context, fileID int64) (*TrackedFileRef, error)
// added: FileByID re-reads one file and returns it only while the *arr still tracks it.
func (c *Client) FileByID(ctx context.Context, fileID int64) (*TrackedFile, error)
func (c *Client) Rescan(ctx context.Context, itemID int64) error            // RescanMovie / RescanSeries
func (c *Client) Unmonitor(ctx context.Context, info models.ArrFileInfo) error // Radarr: movie; Sonarr: info.EpisodeIDs
type ExclusionTarget struct { TmdbID int; TvdbID int; Title string; Year int }
func (c *Client) AddExclusion(ctx context.Context, t ExclusionTarget) error
type MediaManagement struct { RecycleBin string; RecycleBinCleanupDays int }
func (c *Client) MediaManagement(ctx context.Context) (*MediaManagement, error)
func (c *Client) QueueItemIDs(ctx context.Context) (map[int64]bool, error) // movie/series ids with queue entries

var ErrUnauthorized = errors.New("arr: unauthorized (check API key)")
var ErrNotFound     = errors.New("arr: not found")
// added: ErrConflict (409 on delete: root folder missing — abort the run), ErrUnavailable (503),
// ErrRedirect (URL base missing; redirects are never followed), ErrWrongApp, ErrInvalidArgument;
// *HTTPError{Method, Path, StatusCode, Message, Err} for other statuses.

// Webhooks (Settings → Connect → Webhook in Radarr/Sonarr).
type WebhookPayload struct {
    EventType string; InstanceName string; IsUpgrade bool
    Movie  *struct{ ID int64; Title string; Year int; TmdbID int; ImdbID string; FolderPath string }
    Series *struct{ ID int64; Title string; TvdbID int; ImdbID string; Path string }
    Episodes []struct{ ID int64; SeasonNumber, EpisodeNumber int }
}
func ParseWebhook(body io.Reader) (*WebhookPayload, error)
func (p *WebhookPayload) Kind() models.ArrKind // added: "radarr" (movie) | "sonarr" (series) | ""
// ToTargetedScan converts a webhook into a TargetedScan body; ok=false for irrelevant events (Test, Grab…).
func (p *WebhookPayload) ToTargetedScan() (models.TargetedScanBody, bool)
```

## internal/integrations/tautulli (added, DECISIONS D10 — read only)
```go
type Options struct { VerifyTLS bool; Timeout time.Duration; HTTPClient *http.Client }
type Client struct{ /* unexported */ }
func New(inst models.TautulliInstance, opts Options) *Client // never fails; an unusable URL is reported by every call
const MinVersion = "2.18.0"; const DefaultPageSize = 1000; const DefaultMaxRows = 250_000
type Info struct { Version, PMSIdentifier, PMSName string }
func (c *Client) Info(ctx context.Context) (*Info, error) // get_tautulli_info (ErrTooOld < 2.18.0) + get_server_info
type User struct { Active bool; KeepHistory *bool }           // flags only: no names, no e-mail
func (c *Client) Users(ctx context.Context) ([]User, error)
type Library struct { SectionID string; KeepHistory *bool }   // nil: not reported / another section answered
func (c *Client) Library(ctx context.Context, sectionID string) (*Library, error)
type HistoryRow struct { RowID int64; RatingKey, GUID string; UserID int64; Started, Stopped time.Time }
func (r HistoryRow) PlayedAt() time.Time
func (c *Client) FirstPlay(ctx context.Context, sectionID string) (*HistoryRow, error) // earliest play; nil = none
type HistoryFilter struct { RatingKeys []string; GUID, SectionID string; PageSize, MaxRows int }
// History returns every recorded play (grouping and live activity off, oldest first, deduplicated
// by row id) or an error: short page, shrinking history, a row for a key not asked for, more rows
// than MaxRows, an "error" result or data of an unexpected shape → ErrIncomplete / ErrCommandFailed.
func (c *Client) History(ctx context.Context, f HistoryFilter) ([]HistoryRow, error)
func Transient(err error) bool // worth one retry later (timeout, dropped connection, 5xx)
var ErrUnauthorized, ErrTooOld, ErrWrongApp, ErrNotFound, ErrRedirect, ErrInvalidArgument, ErrCommandFailed, ErrIncomplete error
type HTTPError struct { Cmd string; StatusCode int; Err error } // never carries the response body
```
The API key travels in the `X-Api-Key` header only; redirects are never followed; netguard refuses
link-local/metadata addresses; answers are bounded; errors never carry the URL or a response body.

## internal/engine (pure — no I/O)
```go
type GroupOptions struct {
    TreatEditionsAsDistinct  bool
    Treat3DAsDistinct, LanguageVariantsAsDistinct, DifferentArrInstancesIntentional bool // added
    DurationTolerancePercent float64
    DurationToleranceMinutes int
    MaxGroupSize             int                      // added: > this ⇒ suspect_merge (≤ 0 = 4)
    Exclusions               []models.Exclusion
    Libraries                map[int64]models.Library // for scope groups / exclusion by library
}
func GroupOptionsFromSettings(s models.Settings, exclusions []models.Exclusion, libraries map[int64]models.Library) GroupOptions // added
// BuildGroups turns candidate items into duplicate groups: versions of the same item, plus
// cross-library matches between libraries sharing a ScopeGroup (by external ids, then plex guid).
// Skips optimized versions, removes exact duplicate paths, splits editions, applies exclusions,
// sets Key/Title/…/Flags (cross_library, duration_mismatch, multi_episode, stacked, hardlinked,
// edition_split). Returns only groups with ≥2 versions. Files have Version set, no decisions.
// Also flags suspect merges: different folder titles/years, and — when a folder has no year —
// different years in the file names ("The Thing (1982).mkv" vs "The Thing (2011).mkv").
func BuildGroups(items []models.MediaItem, opts GroupOptions) []*models.DuplicateGroup
func GroupKey(item models.MediaItem, serverID int64) string

type EvalEnv struct {
    Now       time.Time
    MinAge    time.Duration
    Libraries map[int64]models.Library
    DifferentArrInstancesIntentional bool // added
    MaxGroupSize int                      // added (explains suspect_merge)
    AllowDiscRemoval, KeepPlayableCopy bool // added (D9): disc versions protected unless allowed; a disc is never the only keeper
}
func EvalEnvFromSettings(s models.Settings, now time.Time, libraries map[int64]models.Library) EvalEnv // added
// Evaluate ranks the group's files with the profile, sets EngineDecision/Decision (respecting
// existing Overrides), Rank, Reasons, DecidingCriterion, Protected/ProtectedReason, Values,
// ReclaimableBytes, adds min_age / arr_untracked_keeper / missing_keeper_file flags, and sets Status
// to pending|review|deferred|protected — except groups currently ignored/queued keep their status.
// Enforces the safety invariants of docs/ARCHITECTURE.md §6 (never 0 keepers, shared-file rule…).
func Evaluate(g *models.DuplicateGroup, p models.Profile, env EvalEnv) error
// ValidateDecisions re-checks invariants on the effective decisions (used by API before approval).
func ValidateDecisions(g *models.DuplicateGroup) error
var ErrInvalidGroup, ErrInvariant error // added: Evaluate input refused; ValidateDecisions (wrapped)

// added — shared by the scanner, the API and the executor so run-time checks match the engine:
func BlocksAutoApproval(flag string) bool                   // flags that keep a group out of auto mode
func ExclusionReason(ex []models.Exclusion, g *models.DuplicateGroup) string // "" = not excluded
func ProtectionReason(p models.Profile, v *models.MediaVersion, libs map[int64]models.Library) string
func KeepPartition(keepPer string, v *models.MediaVersion) (key, label string) // keep-per partition of a version
func EpisodeLabel(season, episode int) string               // "S01E02"; unknown season (-1) → "S??E02"

type CriterionSchema struct {
    Type        models.CriterionType `json:"type"`
    Label       string               `json:"label"`
    Description string               `json:"description"`
    Kind        string               `json:"kind"` // ordered|numeric|boolean|patterns
    Options     []SchemaOption       `json:"options,omitempty"` // ordered: all possible values
    DefaultOrder     []string        `json:"defaultOrder,omitempty"`
    DefaultDirection string          `json:"defaultDirection,omitempty"`
    SupportsTolerance bool           `json:"supportsTolerance"`
    RequiresArr bool                 `json:"requiresArr"`
    RequiresWatchHistory bool        `json:"requiresWatchHistory"`   // added (D10): played, last_played
    MinDeltaUnit string              `json:"minDeltaUnit,omitempty"` // added (D10): "days" for last_played
}
type SchemaOption struct { Value string `json:"value"`; Label string `json:"label"` }
func CriteriaSchema() []CriterionSchema
func ProfileTemplates() []models.Profile     // [0] = "Keep Highest Quality" (IsDefault=true)
func ValidateProfile(p models.Profile) []config.ValidationError
// Explain renders a one-line reason for version b losing to a (used in Reasons).
func DisplayValue(t models.CriterionType, v *models.MediaVersion) string
```

Full-disc backups (added, DECISIONS D9): versions with `MediaVersion.Disc` rank with `source`
`disc`, tie on `container` (and on unknown multichannel counts / unrecorded Atmos–DTS:X),
compare `DiscInfo.FeatureBytes` on `file_size` and count `FreedBytes` as reclaimable; flags
`full_disc`, `disc_unreadable`, `disc_tracked_clip` (all `BlocksAutoApproval`). Evaluate protects
disc versions (setting off, TV, protect-only kinds, not removable/reachable, shared with other
Plex items) and regular versions with a file inside a disc structure, and applies
`KeepPlayableCopy`; ValidateDecisions refuses removing such a file, a TV disc or an unremovable disc.

Play history (added, DECISIONS D10): `played` and `last_played` read `MediaVersion.Watch` and compare
two versions only when both histories are known (an unknown or failed history is class-less: a tie,
never eliminated); "no plays recorded" loses only to a play on or after its `since` (the item's
date added; `metric.floor`); `last_played` ignores `direction` (always newer) and takes `minDelta`
in days. The engine-owned flag `watch_unreadable` (`BlocksAutoApproval`) is set when an enabled
play-history criterion, a removal and a `failed` history coincide in a group with regular copies of
at least two Plex items, which makes the group review. Values and
reasons never say "never watched" / "unwatched". Without those criteria, watch data changes
neither decisions nor signatures.

## internal/disc (added, DECISIONS D9 — read-only, never follows symlinks, bounded)
```go
type Type string // Bluray "bluray", UHDBluray "uhd_bluray", DVD "dvd", HDDVD "hddvd", AVCHD "avchd", ISO "iso", BDAV "bdav"
type Options struct{ MaxFiles, MaxDepth, MaxDirEntries, MaxPlaylists int; MaxMetadataBytes int64; FollowSymlinks bool; KnownPlaylists []string }
type Feature struct{ Playlist string; DurationMs int64; Width, Height int; VideoCodec, FrameRate string; BitDepth int
    DynamicRange models.DynamicRange; DVProfile int; AudioTracks []models.AudioTrack; SubtitleTracks []models.SubtitleTrack
    Chapters, Clips int; ClipIDs []string; Bytes int64 }
type Disc struct{ Type Type; Root string; Roots, OwnedEntries []string; FileCount int; TotalSize int64; Main *Feature; Err error
    Flat, Is3D bool; Alternates, DiscFeatures []Feature; FreedBytes int64; HardlinkedFiles, Irregular int
    NewestModTime time.Time; Fingerprint string }
func (d *Disc) Discs() int; func (d *Disc) Readable() bool; func (d *Disc) FeatureBytes() int64
func (d *Disc) Owns(p string) bool; func (d *Disc) Removable() (bool, string)
func IsDiscPath(p string) bool                       // the per-file guard (Tier 1): p lies inside a disc structure
func RootOf(p string) (root string, t Type, ok bool) // disc root of such a path
func IsImagePath(p string) bool; func IsDiscEntryName(name string) bool; func HintsDisc(name string) bool
func IsSetFolderName(name string) (int, bool); func IsExtrasFolderName(name string) bool
func SetFolderPrefix(name string) (string, bool) // members of one set share it (else ErrSetUnclear)
func HasExtrasWord(s string) bool                 // extras keyword anywhere (scanner: after the title)
func DetectDir(root string) string; func RootHash(root string) string
func Detect(ctx context.Context, dir string, opts Options) ([]Disc, error) // discs rooted in dir ("Disc N" sets, images)
func Inspect(ctx context.Context, d *Disc) error                           // sizes, fingerprint, main feature
func Measure(ctx context.Context, entries []string, opts Options) (Stats, error) // re-check right before a move
var ErrIncomplete, ErrUnreadable, ErrSetUnclear, ErrMixedLayout, ErrSymlink, ErrLimit, ErrInsideDisc, ErrChanged, ErrFollowSymlinks, ErrNotAbsolute error
```

## internal/notifications
```go
type Field   struct { Name, Value string; Inline bool; Paths bool /* lists server file paths */ }
type Message struct {
    Event    string // models.On*
    Title    string
    Body     string
    // BodyWithoutPaths replaces Body (and Paths fields are dropped) for connections without
    // SettingIncludePaths ("includePaths", every provider, default false; GAP-14).
    BodyWithoutPaths string
    Fields   []Field
    URL      string // deep link into the Dupearr UI when known
    Severity string // info|warning|error
}
type FieldSchema struct { Name, Label, Type, HelpText string; Required, Advanced, Secret bool; Options []string; Default any } // Type: text|password|url|number|checkbox|select|textarea
type ProviderSchema struct { Kind, Name, InfoURL string; Fields []FieldSchema }
func Schema() []ProviderSchema // discord, slack, telegram, pushover, gotify, ntfy, apprise, webhook, email
type Service struct{ /* unexported */ }
func New(st store.Store, log *slog.Logger) *Service
// Notify sends asynchronously to every enabled config whose Triggers include msg.Event. Never blocks
// the caller for network I/O; failures are logged.
func (s *Service) Notify(ctx context.Context, msg Message)
// Test sends a test message synchronously with an unsaved config and returns the provider error.
func (s *Service) Test(ctx context.Context, cfg models.NotificationConfig) error
func ValidateConfig(cfg models.NotificationConfig) []config.ValidationError
// MaskSecrets returns a copy of cfg with secret settings replaced by "********";
// MergeSecrets restores masked values from the stored config on update.
func MaskSecrets(cfg models.NotificationConfig) models.NotificationConfig
func MergeSecrets(incoming, stored models.NotificationConfig) models.NotificationConfig
// added:
func (s *Service) SetInstanceName(name string)          // config InstanceName in messages
func (s *Service) Shutdown(ctx context.Context) error   // stop accepting, wait for in-flight sends (main: 5 s)
type TriggerOption struct { Value, Label string }; func TriggerOptions() []TriggerOption
const EventTest = "test"; const MaskedValue = "********"
```

## internal/scanner
```go
// PlexClient / ArrClient are the subsets of the integration clients the scanner uses (fakes in tests).
type PlexClient interface {
    Identity(ctx context.Context) (*plex.Identity, error)
    Sections(ctx context.Context) ([]plex.Section, error)
    DuplicateItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error)
    AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]plex.ItemRef, error)
    Item(ctx context.Context, ratingKey string) (*models.MediaItem, error)
}
type ArrClient interface {
    TrackedFiles(ctx context.Context, f arr.TrackedFilter) ([]arr.TrackedFile, error)
    QueueItemIDs(ctx context.Context) (map[int64]bool, error)
}
// WatchClient (added, D10) is the subset of *tautulli.Client the scan reads play history with.
type WatchClient interface {
    Info(ctx context.Context) (*tautulli.Info, error)
    Users(ctx context.Context) ([]tautulli.User, error)
    Library(ctx context.Context, sectionID string) (*tautulli.Library, error)
    FirstPlay(ctx context.Context, sectionID string) (*tautulli.HistoryRow, error)
    History(ctx context.Context, f tautulli.HistoryFilter) ([]tautulli.HistoryRow, error)
}
type Deps struct {
    Store         store.Store
    Bus           *events.Bus
    Log           *slog.Logger
    Notifier      *notifications.Service // may be nil in tests
    PlexFactory   func(s models.MediaServer) PlexClient
    ArrFactory    func(a models.ArrInstance) ArrClient
    // TautulliFactory (added, D10): the play-history client of a Tautulli connection. nil: an
    // enabled connection cannot be read and its server's versions get a "failed" history.
    TautulliFactory func(t models.TautulliInstance) WatchClient
    Now           func() time.Time // nil = time.Now
    Concurrency   int              // parallel item-detail fetches (default 4)
    // AutoApprove approves a group in auto mode. May be nil. Changed: it takes the group's
    // Signature as stored and checked by this scan; cmd/dupearr wires it to
    // executor.ApproveReviewed, so an approval of a group something changed since is refused.
    // Within the per-scan budget (maxDeletionsPerRun / maxBytesPerRunGb; ≤ 0 approves nothing).
    AutoApprove   func(ctx context.Context, groupID int64, trigger, signature string) error
}
const DefaultConcurrency = 4; const DefaultArrRetryDelay = 3 * time.Second // added: a failed *arr read is retried once
var ErrNothingToScan = errors.New("targeted scan: no rating keys or external ids given") // added
// ArrDataMissing (added): the group went to review because an *arr could not be read in its last
// scan; the API refuses to approve it (409) until a scan read every instance.
func ArrDataMissing(g *models.DuplicateGroup) bool
type Service struct{ /* unexported */ }
func New(d Deps) *Service
// FullScan scans the given (or all enabled) libraries; progress receives human messages.
func (s *Service) FullScan(ctx context.Context, body models.DuplicateScanBody, trigger string, progress func(string)) (*models.ScanRun, error)
func (s *Service) TargetedScan(ctx context.Context, body models.TargetedScanBody, trigger string) (*models.ScanRun, error)
// SyncLibraries refreshes the library list of one server (serverID 0 = all enabled servers).
func (s *Service) SyncLibraries(ctx context.Context, serverID int64) error
// Reevaluate re-runs engine.Evaluate for one group (current profile + overrides), persists, publishes.
func (s *Service) Reevaluate(ctx context.Context, groupID int64) (*models.DuplicateGroup, error)
// ReevaluateAll re-evaluates every non-resolved group (after profile/settings change).
func (s *Service) ReevaluateAll(ctx context.Context) error
// ReevaluateMatching (added) re-evaluates at once the open groups match selects — the groups a
// configuration change affects (exclusion created/deleted, library disabled/enabled, other
// profile or scope group): a group the change blocks goes to review (cancelling its queued
// removals), one it no longer blocks is re-opened. Returns how many groups were re-evaluated.
func (s *Service) ReevaluateMatching(ctx context.Context, match func(*models.DuplicateGroup) bool) (int, error)
func GroupUsesLibrary(g *models.DuplicateGroup, id int64) bool // added (match helper)
// Scans never keep an approval for a keeper nobody reviewed: a queued group whose kept copy changed
// (vanished, or beaten by a new download) goes to review, which cancels its queued removals.
// A group whose file Plex reports missing (exists=false: typically an unmounted share) is not
// resolved; it keeps its status with flag unavailable_version. A media server stored without a
// machineIdentifier (force-saved while unreachable) adopts the one it answers with on the next
// sync or scan (never replacing a stored one, never one another server has).
// Full-disc backups (added, D9): with settings.DetectDiscs a scan detects and inspects the discs in
// the mapped folders of the listed movies (bounded, cached per folder per scan) and adds each as a
// version (key "disc:<server>:<RootHash>", origin filesystem) — so "mkv + disc" is a candidate; Plex
// versions whose parts are a disc's clips/IFO/VOB or an image become disc versions (origin plex); an
// *arr file inside a disc attaches to the disc (disc_tracked_clip); TV discs only flag their
// episodes' groups. Auto mode never proposes a disc removal.
// Play history (added, D10): after the *arr data, full and targeted scans read each server's enabled
// Tautulli and set MediaVersion.Watch on every version (known / unknown with a reason / failed); a
// failed read counts in Stats.Errors, never yields "no plays", and a group with watch_unreadable
// never counts as a stable scan (scans and re-evaluations set StableCount 0).
// Matcher (exported for tests): matches versions to *arr files.
type Matcher struct{ /* unexported */ }
func NewMatcher(m *pathmap.Mapper) *Matcher
func (mt *Matcher) Add(instanceID int64, files []arr.TrackedFile)
func (mt *Matcher) Match(serverID int64, v *models.MediaVersion) *arr.TrackedFile
```

## internal/executor
```go
type PlexClient interface {
    Identity(ctx context.Context) (*plex.Identity, error) // added: server identity guard (before re-verify and every Plex delete)
    Item(ctx context.Context, ratingKey string) (*models.MediaItem, error)
    DeleteMedia(ctx context.Context, ratingKey string, mediaID int64) error
    RefreshItem(ctx context.Context, ratingKey string) error
    ScanPath(ctx context.Context, sectionKey, dir string) error
    MediaDeletionAllowed(ctx context.Context) (bool, error)
    ActiveSessions(ctx context.Context) (map[string]bool, error)
}
type ArrClient interface {
    File(ctx context.Context, fileID int64) (*arr.TrackedFileRef, error) // added: file identity guard (planning + right before DeleteFile)
    DeleteFile(ctx context.Context, fileID int64) error
    Rescan(ctx context.Context, itemID int64) error
    Unmonitor(ctx context.Context, info models.ArrFileInfo) error
    AddExclusion(ctx context.Context, t arr.ExclusionTarget) error
    MediaManagement(ctx context.Context) (*arr.MediaManagement, error)
}
type Deps struct {
    Store       store.Store
    Bus         *events.Bus
    Log         *slog.Logger
    Notifier    *notifications.Service // may be nil
    PlexFactory func(s models.MediaServer) PlexClient
    ArrFactory  func(a models.ArrInstance) ArrClient
    Now         func() time.Time
    // Enqueue schedules a command (used to queue a TargetedScan after a stale-data skip). May be nil.
    Enqueue     func(ctx context.Context, name string, body any, trigger string) error
}
type Summary struct { Processed, Succeeded, DryRun, Skipped, Failed int; BytesFreed int64; Aborted bool; Message string
    Deferred int } // added: groups left pending (playing, Plex unreachable, *arr unreachable, …)
type Service struct{ /* unexported */ }
func New(d Deps) *Service
// Approve validates the group's effective decisions and creates pending actions for every file
// decided "remove" (not protected); sets the group to queued; history event groupApproved.
// Returns ErrNothingToRemove / validation errors.
func (s *Service) Approve(ctx context.Context, groupID int64, trigger string) ([]models.Action, error)
// ApproveReviewed (added) is Approve for the state that was reviewed: a non-empty signature must
// still be the group's Signature (else ErrNotApprovable). The status is written with a
// compare-and-set (store UpdateStatusIf) and the group is re-read before and after; when a scan
// stored new results meanwhile, the queued removals are cancelled and ErrNotApprovable is returned
// (the group goes back to review with a re-scan). Approve = ApproveReviewed with signature "".
func (s *Service) ApproveReviewed(ctx context.Context, groupID int64, trigger, signature string) ([]models.Action, error)
// ProcessQueue executes pending actions (docs/ARCHITECTURE.md §6) respecting dry run,
// maxDeletionsPerRun and the 3-consecutive-failures breaker.
func (s *Service) ProcessQueue(ctx context.Context, progress func(string)) (Summary, error)
// RecoverInterrupted (added) fails the removals a previous process left "running" (never retried:
// "Interrupted by a restart while running — verify the files, then re-approve"), fails their groups
// and cancels the groups' other queued removals. main calls it at startup before commands run;
// ProcessQueue calls it as a safety net. Returns how many removals were recovered.
func (s *Service) RecoverInterrupted(ctx context.Context) (int, error)
func (s *Service) CancelGroup(ctx context.Context, groupID int64) error
// Restore moves a filesystem recycle-bin removal back to its original path and refreshes Plex.
// The group is set to ignored first, so the restored copy is not removed again (un-ignore to
// re-evaluate it).
func (s *Service) Restore(ctx context.Context, actionID int64) error
// CleanRecycleBin deletes recycle-bin entries older than settings.RecycleBinCleanupDays.
func (s *Service) CleanRecycleBin(ctx context.Context) (int, error)
var ErrNothingToRemove = errors.New("nothing to remove in this group")
var ErrNotApprovable, ErrNotRestorable, ErrAborted error // added (wrapped with the reason)
```
Full-disc backups (added, D9): ApproveReviewed refuses (ErrNotApprovable) a disc removal that is not
a person's, with `AllowDiscRemoval` off, without a recycle bin or the filesystem method, in a TV
group or of an unremovable disc, and any group keeping only discs while `KeepPlayableCopy`. A disc
is removed only by the filesystem method as a whole: re-detected, re-inspected and re-measured
(fingerprint) right before its owned entries are renamed (no copy, no replace) into the recycle
bin; Restore moves them back. Every method refuses a regular version with a file inside a disc
structure; Plex refuses media with more than 8 parts. After a disc removal Plex scans the movie
folder and an *arr that tracked a clip of it is rescanned.

## internal/health
```go
type Deps struct {
    Store       store.Store
    Config      *config.Manager
    Bus         *events.Bus
    Notifier    *notifications.Service
    Log         *slog.Logger
    PlexFactory func(s models.MediaServer) interface{ Identity(context.Context) (*plex.Identity, error); MediaDeletionAllowed(context.Context) (bool, error) }
    ArrFactory  func(a models.ArrInstance) interface{ Status(context.Context) (*arr.SystemStatus, error) }
    TautulliFactory func(t models.TautulliInstance) TautulliClient // added (D10); nil skips the Tautulli probes
    StartTime       time.Time     // added: OnHealthIssue notifications wait until StartTime + BootGracePeriod
    BootGracePeriod time.Duration // added: > 0 overrides the default (15 min); < 0 disables
}
// added: optional capabilities of the factory results (type-asserted)
type PlexOwnership interface { Ownership(ctx context.Context, machineID string) (owned, known bool, err error) }
type ArrMediaManagement interface { MediaManagement(context.Context) (*arr.MediaManagement, error) }
type TautulliClient interface { Info(context.Context) (*tautulli.Info, error); Users(context.Context) ([]tautulli.User, error); Library(ctx context.Context, sectionID string) (*tautulli.Library, error) } // added
const BootGracePeriod = 15 * time.Minute; const CheckTimeout = 10 * time.Second; const PlexTVTimeout = 5 * time.Second // added
func (c *Checker) GracePeriodEnd() time.Time // added
type Checker struct{ /* unexported */ }
func New(d Deps) *Checker
func (c *Checker) Run(ctx context.Context) []models.HealthCheck // runs all checks, caches, publishes, notifies on change
func (c *Checker) Results() []models.HealthCheck                // cached
```
Checks (`HealthCheck.source`, constants `Source*`): NoMediaServerCheck, MediaServerConnectivityCheck
(also: the URL answers as a different Plex server), PlexMediaDeletionCheck (only when "plex" is in
deletionMethods), PlexOwnerCheck (added: the token is known not to be the owner's),
ArrConnectivityCheck, ArrRecycleBinCheck (added, notice), PathMappingCheck (filesystem method
enabled but a library location has no Plex mapping / local path not found), RecycleBinCheck (a
missing bin with a writable parent is fine), DryRunCheck (notice), AuthenticationCheck (warning:
None), ExternalAuthCheck (added: External — warning without trusted proxies, else a notice that
the port must only be reachable through the authenticating reverse proxy; `Deps.ProxyTrust`),
WebhookApiKeyCheck (added, warning; `Deps.WebhookMasterKeyUsed`), ReverseProxyCheck (added, warning:
a local peer that is not a trusted proxy sent forwarding headers; `Deps.UntrustedProxySeen`),
DiscDetectionUnavailable (added, notice: `detectDiscs` on but no enabled movie library folder is
mapped), TautulliConnectivityCheck (added, error: a Tautulli connection is unreachable, rejects the
key, is older than 2.18.0 or monitors another Plex server), WatchHistoryCheck (added: warning when
a profile ranks by play history for a server without an enabled Tautulli; notice when Tautulli
keeps no history for some of those libraries or users), LastScanCheck, DatabaseCheck. Details:
`docs/API.md` → Health.

## internal/backup
```go
type Backup struct { ID int64 `json:"id"`; Name string `json:"name"`; Path string `json:"path"`; Type string `json:"type"`; Size int64 `json:"size"`; Time time.Time `json:"time"` } // Type: scheduled|manual|update; Path = "/backup/<type>/<name>"
type Service struct{ /* unexported */ }
func New(dataDir string, cfg *config.Manager, st store.Store, log *slog.Logger) *Service
func (s *Service) Create(ctx context.Context, kind string) (*Backup, error) // zip: config.xml + dupearr.db (VACUUM INTO snapshot)
func (s *Service) List() ([]Backup, error)                                  // newest first; ID = stable hash of type/name
func (s *Service) File(kind, name string) (string, error)                   // validated absolute path for download
func (s *Service) Delete(id int64) error
// Restore validates the zip and stages it to dataDir/.restore; ApplyPendingRestore (called by main
// at startup before opening the DB) swaps the files in. Caller then restarts the process.
func (s *Service) Restore(ctx context.Context, id int64) error
func (s *Service) RestoreUpload(ctx context.Context, r io.Reader, size int64) error
func ApplyPendingRestore(dataDir string) (applied bool, err error)
func (s *Service) Cleanup(retentionDays int) (int, error)                  // scheduled backups only; ≤ 0 keeps all
var ErrInvalidBackup = errors.New("invalid backup"); var ErrNotFound error  // added
```

## internal/commands
```go
type Handler func(ctx context.Context, cmd *models.Command, progress func(msg string)) (message string, err error)
type TaskDef struct { Name, TaskName string; DefaultInterval int; IntervalFunc func() int /* optional dynamic interval from settings */ }
type Manager struct{ /* unexported */ }
func New(st store.Store, bus *events.Bus, log *slog.Logger) *Manager
func (m *Manager) Register(name string, h Handler, exclusive bool)
func (m *Manager) RegisterTask(t TaskDef)
// Enqueue persists and queues a command; if an identical exclusive command is already queued, it
// returns that one instead of queueing a duplicate.
func (m *Manager) Enqueue(ctx context.Context, name string, body any, trigger string) (*models.Command, error)
// EnqueueFresh (added) is Enqueue for a command whose result must reflect a change the caller just
// made (CheckHealth after a configuration change): an identical *queued* command absorbs the
// request, but one that is already *running* does not (it may have read the old state) — a new
// command is queued behind it.
func (m *Manager) EnqueueFresh(ctx context.Context, name string, body any, trigger string) (*models.Command, error)
func (m *Manager) CountQueued(name, trigger string) int // queued (not running) commands; bounds webhook scans
func (m *Manager) Get(ctx context.Context, id int64) (*models.Command, error)
func (m *Manager) Recent(ctx context.Context, limit int) ([]models.Command, error)
func (m *Manager) Tasks(ctx context.Context) ([]models.ScheduledTask, error)
func (m *Manager) Start(ctx context.Context) // runs the worker(s) + scheduler until ctx done
func (m *Manager) Wait()                      // waits for running commands after ctx cancel
var ErrUnknownCommand = errors.New("unknown command")
var ErrInvalidBody, ErrStopped error                        // added
const Workers = 3; const SchedulerTick = 30 * time.Second; const StartupGrace = 5 * time.Minute // added
```

## internal/auth
```go
const CookieName = "DupearrAuth"
type Service struct{ /* unexported */ }
func New(cfg *config.Manager, st store.Store, log *slog.Logger) (*Service, error) // loads/creates signing key (settings value "auth.sessionKey", "v2:<hex>"; an older key is replaced; RevokeSessions replaces it)
func HashPassword(pw string) (string, error)
func CheckPassword(hash, pw string) bool
// Middleware enforces auth for everything except the allow-list (/ping, /login, /logout,
// static assets, /favicon*, /initialize.json when a valid session exists is still auth'd).
// API key (X-Api-Key header only; an apikey query parameter is a 401) always works for /api/*. Forms → session cookie (HMAC-signed,
// 7-day sliding); External → trust; None → allow only requests whose Host and every
// X-Forwarded-Host is private (IsPrivateHost: IP literal, localhost, single-label name, private-use
// suffix such as .local/.lan/.home.arpa/.internal) — a DNS-rebinding guard; other hosts need the
// API key (401, logged once as a warning). Basic is NOT supported (DECISIONS D1): a
// stored "Basic" is treated as Forms.
// DisabledForLocalAddresses bypasses auth for RFC1918/loopback/link-local/ULA clients (using the
// TCP peer address; X-Forwarded-For only honoured when the peer itself is local) that also name the
// server by a private host, where an IP literal must itself be a local address (LocalAccessAllowed).
// A request's policy is the stricter of the policies of its decoded path and of the path the
// router matches (encoded "/" or "." cannot reach a protected handler as a public path).
// First-run: when method is Forms (a stored Basic counts as Forms) and no user exists,
// /api/v1/auth/setup is reachable without auth (local clients addressing the server by IP or a
// private host name only) to create the user.
func (s *Service) Middleware(next http.Handler) http.Handler
func (s *Service) Login(w http.ResponseWriter, r *http.Request, username, password string, remember bool) error
func (s *Service) Logout(w http.ResponseWriter, r *http.Request)
func (s *Service) SetupRequired(ctx context.Context) bool
func (s *Service) Setup(ctx context.Context, method, required, username, password string) error
func IsLocalAddress(ip net.IP) bool
var ErrInvalidCredentials = errors.New("invalid username or password")
// added:
func IsPrivateHost(host string) bool                         // Host/X-Forwarded-Host that DNS rebinding cannot produce
func (s *Service) LocalAccessAllowed(r *http.Request) bool   // local client + local host (bypass, setup)
func (s *Service) IsLocalRequest(r *http.Request) bool; func (s *Service) ClientIP(r *http.Request) net.IP
func (s *Service) Authenticated(r *http.Request) bool; func (s *Service) Method() string
func (s *Service) User(ctx context.Context) (*models.User, error)
func (s *Service) UpdateUser(ctx context.Context, username, password string) (*models.User, error)
func (s *Service) ReissueSession(w http.ResponseWriter, r *http.Request) error
func ValidateCredentials(username, password string) []config.ValidationError
type Info struct { Authenticated bool; Via string /* Via* */; UserID int64; Remember bool }
func WithInfo(ctx context.Context, info Info) context.Context; func FromContext(ctx context.Context) Info
var ErrTooManyAttempts, ErrSetupNotRequired, ErrNoUser error; type ThrottledError struct{ RetryAfter time.Duration }
// added by the GAP review (docs/SECURITY.md):
func (s *Service) EnvForcedAuth() map[string]string // env-forced authenticationMethod/authenticationRequired
func (s *Service) Audit(ctx context.Context, r *http.Request, kind, title string, attrs ...any) // internal/audit record
func (s *Service) Lockdown(); func (s *Service) LockedDown() bool // refuse every credential until exit (reset-auth)
var ErrLockedDown error
const EnvAuthMethod, EnvAuthRequired = "DUPEARR__AUTH__METHOD", "DUPEARR__AUTH__REQUIRED"
// Reverse-proxy trust (network.go; DECISIONS "Reverse-proxy trust"). The lists are
// config.Config.TrustedProxies / AllowedHosts (Settings → General, config.xml), overridden by
// EnvTrustedProxies / EnvAllowedHosts through internal/config, and read on every use (no restart).
const EnvTrustedProxies, EnvAllowedHosts = "DUPEARR__AUTH__TRUSTEDPROXIES", "DUPEARR__AUTH__ALLOWEDHOSTS"
type NetworkTrust struct { TrustedProxies []netip.Prefix; AllowedHosts []string }
func (t NetworkTrust) Configured() bool
func ParseNetworkTrust(proxies, hosts string) (NetworkTrust, error) // skips invalid entries, returns them as the error
func ValidateNetworkTrust(proxies, hosts string) []config.ValidationError // added (issue #1): what the API refuses, per entry
func (s *Service) NetworkTrust() NetworkTrust                        // effective lists (cached parse, invalid entries logged)
func (s *Service) SetNetworkTrust(fn func() (trustedProxies, allowedHosts string)) // tests; nil = the configuration
// added (issue #1): why r, as it is authenticated now (Via external / none / localAddress), would
// stop being trusted with these lists under method External or None ("" when it stays trusted,
// keeps a login form (Forms) or a credential (session, API key)).
func (s *Service) TrustChangeProblem(r *http.Request, method, proxies, hosts string) string
func (s *Service) UntrustedProxySeen() (time.Time, string) // ReverseProxyCheck; zero once that peer is trusted
```

## internal/audit
```go
// Record writes a security event (models.EventSecurity) into the history: independent of the log
// level, kept ≥ models.SecurityHistoryRetentionDays; attrs are key/value pairs, never secrets.
func Record(ctx context.Context, st store.Store, log *slog.Logger, kind, title string, attrs ...any)
const KindSetup, KindAuthReset, KindLogin, KindLoginFailed, KindCredentialsChanged, KindAPIKeyChanged,
    KindAPIKeyRevealed, KindWebhookTokenChanged, KindSessionsRevoked, KindHostSettings, KindLogLevelLowered,
    KindRemovalSettings, KindConnectionChanged, KindBackupDownloaded, KindRestoreConfirmed = …
```

## internal/api
```go
type Deps struct {
    Config    *config.Manager
    Store     store.Store
    Bus       *events.Bus
    Log       *slog.Logger
    Logs      *logging.Manager
    Auth      *auth.Service
    Commands  *commands.Manager
    Scanner   *scanner.Service
    Executor  *executor.Service
    Health    *health.Checker
    Backups   *backup.Service
    Notifier  *notifications.Service
    PlexOpts  plex.Options
    PlexFactory func(s models.MediaServer) *plex.Client
    ArrFactory  func(a models.ArrInstance) *arr.Client
    TautulliFactory func(t models.TautulliInstance) *tautulli.Client // added (D10): connection tests
    WebFS     fs.FS            // embedded SPA (web/dist); may lack index.html in dev
    StartTime time.Time
    Restart   func()           // graceful restart (re-exec)
}
type Server struct{ /* unexported */ }
func New(d Deps) *Server
func (s *Server) Handler() http.Handler   // full mux incl. UrlBase handling, middleware, SPA fallback
func (s *Server) Close()                  // added: stops/waits for handler background work (after HTTP shutdown, before the store closes)
func RunningInDocker() bool               // added: DUPEARR_DOCKER=1 (set by the image) or a container marker file
```
Approvals go through `executor.Service.ApproveReviewed` with the signature the client reviewed
(`Deps.Executor` stays `*executor.Service`; handlers use unexported interfaces of it and the
scanner, which tests fake). Configuration changes that
affect open groups (exclusions, library enable/profile/scope group) call
`scanner.ReevaluateMatching` before the response is written.
Endpoints: see `docs/API.md` (authoritative).

## web (Go package `web`, file `web/embed.go`)
```go
//go:embed all:dist
var dist embed.FS
func FS() fs.FS // sub-FS rooted at dist
```

## cmd/dupearr
Flags `--data`, `--nobrowser`; subcommands `version`, `healthcheck`, `reset-auth`. The server
first takes the data directory's lock (`<data>/.dupearr-app.lock`: flock where available, else an
exclusive PID file; a second server on the same directory exits with a message; subcommands never
take it; an in-place restart hands the lock over). The Docker entrypoint also locks
`/config/.dupearr.lock` (a different file, inherited on fd 9). Then it wires everything:
config → logging → backup.ApplyPendingRestore → database.Open(dataDir/dupearr.db) (+migrations,
seed default profile templates + settings) → events → notifications → factories → scanner
(AutoApprove → executor.ApproveReviewed) → executor → health → backup → commands (register
handlers + tasks) → auth → api → `executor.RecoverInterrupted` (before any command runs) →
commands start → http.Server(s) (HTTP + optional HTTPS) → graceful shutdown on SIGINT/SIGTERM
(HTTP, then `api.Close`, then the commands, then `notifier.Shutdown` (≤ 5 s), then the database)
→ restart via syscall.Exec.

## internal/database
```go
func Open(ctx context.Context, path string, log *slog.Logger) (*DB, error) // WAL, foreign_keys=ON, busy_timeout, migrations, data upgrades
type DB struct{ /* unexported */ } // implements store.Store
// Seed inserts ProfileTemplates when no profile exists and default Settings when missing.
func (d *DB) Seed(ctx context.Context, templates []models.Profile) error
// added (store.Store): Ping, BackupTo (VACUUM INTO), Vacuum, Close, Path
var ErrActionNotPending, ErrConstraint, ErrDefaultProfile, ErrNotRemovable error // added
```
Tables: settings(key TEXT PK, value TEXT), users, media_servers, libraries, arr_instances,
tautulli_instances (added, migration 0003: one per media server, deleted with it), path_mappings, profiles(criteria/protections JSON), duplicate_groups, group_files(version JSON,
decision columns), actions, history, exclusions, notifications, scan_runs, commands, tasks,
schema_migrations. Indexes on duplicate_groups(key UNIQUE, status, last_scan_id),
group_files(group_id), actions(status, group_id), history(created_at, event_type).
Data upgrades (added): once per database, recorded by a settings entry (`upgrade.discProfiles`) and
only when profiles exist (a backup restore copies its rows — and its entry — into an empty
database, so an older backup is upgraded when opened): the D9 profile upgrade adds `disc` after
`remux` to non-empty source orders and `m2ts` after the common containers.

## internal/store (persistence interfaces; additions)
The interfaces live in `internal/store/store.go` (their comments carry the database layer's safety
rules). `Store.Tautullis() TautulliRepo` (added, D10: List/Get/Create/Update/Delete of
`models.TautulliInstance`). Added during implementation — atomic operations so concurrent writers
(a scan, the executor, the API) can never overwrite each other's status changes:
```go
type GroupRepo interface {
    // … Upsert, UpdateStatus, SetOverride, MarkUnseenResolved, … (see store.go)
    // UpdateStatusIf is UpdateStatus as a compare-and-set: written only while the current status is
    // one of from (same side effects: a blocking status cancels pending actions). false = another
    // status won; ErrNotFound when the group does not exist.
    UpdateStatusIf(ctx context.Context, id int64, from []models.GroupStatus, to models.GroupStatus, reason string) (bool, error)
    // SetFlag adds (on) or removes one flag (e.g. models.FlagPlaying) without touching status,
    // decisions or files. Idempotent.
    SetFlag(ctx context.Context, id int64, flag string, on bool) error
}
type ActionRepo interface {
    // … Create, Get, Update, ListPending, CancelPendingForGroup, …
    // CancelIfPending atomically moves one action pending → cancelled ("Cancelled by user"),
    // reopening an emptied queued group. false when it is running, finished or already cancelled
    // (a running removal is never marked cancelled); ErrNotFound when it does not exist.
    CancelIfPending(ctx context.Context, id int64) (bool, error)
}
```
