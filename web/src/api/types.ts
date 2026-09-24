/**
 * TypeScript mirror of the Dupearr HTTP contract (docs/API.md) and the JSON of the Go domain types
 * (internal/models, internal/store, internal/engine, internal/notifications, internal/logging,
 * internal/backup). Field names are exact — do not rename.
 *
 * Conventions:
 * - Times are RFC3339 strings (UTC) → `IsoDateTime`.
 * - Go `omitempty` fields are optional (`?`); Go pointers are `| null` (and optional when omitempty).
 * - Go int64 ids are plain `number`.
 */

export type IsoDateTime = string;
export type Id = number;

// ---------------------------------------------------------------------------
// Common
// ---------------------------------------------------------------------------

export type SortDirection = 'ascending' | 'descending';

/** Query parameters accepted by every *paged* endpoint. */
export interface PagingParams {
  page?: number;
  pageSize?: number;
  sortKey?: string;
  sortDirection?: SortDirection;
}

/** Response of every *paged* endpoint (store.Page[T]). */
export interface PagedResponse<T> {
  page: number;
  pageSize: number;
  sortKey: string;
  sortDirection: SortDirection;
  totalRecords: number;
  records: T[];
}

/** 400 validation error item: `[{"propertyName":"url","errorMessage":"URL is required"}]`. */
export interface ValidationFailure {
  propertyName: string;
  errorMessage: string;
  /** Optional (not part of the contract, tolerated). */
  severity?: string;
}

/** Non-validation error body: `{"message":"…","description":"…"}`. */
export interface ErrorResponse {
  message: string;
  description?: string;
}

/** `{value,label}` pairs (notification triggers, schema options). */
export interface SelectOptionDto {
  value: string;
  label: string;
}

// ---------------------------------------------------------------------------
// Enums (string unions) — labels live in src/lib/constants.ts
// ---------------------------------------------------------------------------

export type MediaType = 'movie' | 'episode';
export type ResolutionTier = '2160' | '1440' | '1080' | '720' | '576' | '480' | 'sd';
export type DynamicRange = 'dv_hdr10' | 'dv' | 'hdr10plus' | 'hdr10' | 'hlg' | 'sdr';
export type VideoCodec = 'av1' | 'hevc' | 'h264' | 'vc1' | 'mpeg2' | 'mpeg4' | 'vp9' | 'other';
export type AudioFormat =
  | 'truehd_atmos'
  | 'truehd'
  | 'dtsx'
  | 'dts_hd_ma'
  | 'dts_hd_hra'
  | 'eac3_atmos'
  | 'flac'
  | 'pcm'
  | 'dts'
  | 'eac3'
  | 'ac3'
  | 'aac'
  | 'opus'
  | 'mp3'
  | 'other';
/** `disc` = a full-disc backup (BDMV / VIDEO_TS / ISO …), see {@link DiscInfo}. */
export type ReleaseSource =
  | 'remux'
  | 'disc'
  | 'bluray'
  | 'webdl'
  | 'webrip'
  | 'hdtv'
  | 'dvd'
  | 'sdtv'
  | 'unknown';
/**
 * `m2ts` = a standalone .m2ts/.mts file (outside a disc structure); distinct from `ts`. `disc` is the
 * container of a full-disc version (it has none; not a criterion option).
 */
export type ContainerFormat = 'mkv' | 'mp4' | 'm4v' | 'm2ts' | 'avi' | 'ts' | 'other' | 'disc';

/**
 * Kind of a full-disc backup (models.DiscInfo.Type). `hddvd`, `avchd` and `bdav` (a Blu-ray recorder's
 * BDAV/ folder) are only ever shown and kept.
 *
 * `bluray_clips` / `dvd_clips` are a flattened backup: the numbered STREAM clips of a Blu-ray
 * (`00800.m2ts` …) or the VOB files of a DVD (`VTS_01_1.VOB` …) lying loose in the movie folder,
 * without a BDMV/ or VIDEO_TS/ folder. Plex lists each loose clip as its own version; the server
 * merges every clip of one folder into ONE disc version (a feature can span clips).
 */
export type DiscType =
  | 'bluray'
  | 'uhd_bluray'
  | 'dvd'
  | 'hddvd'
  | 'avchd'
  | 'bdav'
  | 'iso'
  | 'bluray_clips'
  | 'dvd_clips';
/**
 * How a disc was found: `filesystem` = detected on disk next to the Plex item (Plex's default
 * scanners ignore disc folders, so Plex does not show it); `plex` = a custom Plex scanner exposes
 * the disc's files as parts of one version — or, for a loose clip set, Plex lists each loose clip
 * as its own version and the server merged them into this one.
 */
export type DiscOrigin = 'filesystem' | 'plex';

export type GroupStatus =
  | 'pending'
  | 'review'
  | 'deferred'
  | 'protected'
  | 'queued'
  | 'resolved'
  | 'ignored'
  | 'failed';

export type GroupFlag =
  | 'cross_library'
  | 'duration_mismatch'
  | 'multi_episode'
  | 'stacked'
  | 'hardlinked'
  | 'min_age'
  | 'missing_keeper_file'
  | 'edition_split'
  | 'arr_untracked_keeper'
  | 'unanalyzed'
  | 'unavailable_version'
  | 'suspect_merge'
  | 'variant_3d'
  | 'language_variant'
  | 'intentional_arr_instances'
  | 'arr_queue_busy'
  | 'arr_cutoff_unmet'
  | 'playing'
  | 'sample'
  | 'same_file'
  | 'full_disc'
  | 'disc_unreadable'
  | 'disc_tracked_clip'
  | 'watch_unreadable';

export type Decision = 'keep' | 'remove';

export type CriterionType =
  | 'resolution'
  | 'dynamic_range'
  | 'source'
  | 'video_codec'
  | 'audio_format'
  | 'container'
  | 'library'
  | 'audio_channels'
  | 'video_bitrate'
  | 'file_size'
  | 'bit_depth'
  | 'custom_format_score'
  | 'date_added'
  | 'audio_track_count'
  | 'subtitle_track_count'
  | 'arr_managed'
  | 'audio_language'
  | 'filename_score'
  | 'health'
  | 'played'
  | 'last_played';

export type CriterionKind = 'ordered' | 'numeric' | 'boolean' | 'patterns';
export type Direction = 'higher' | 'lower';
export type ProtectionType = 'path_glob' | 'library' | 'arr_instance' | 'arr_tag';
export type KeepPer = '' | 'resolution' | 'dynamic_range';

export type MediaServerKind = 'plex';
export type ArrKind = 'radarr' | 'sonarr';
export type PathSourceType = 'server' | 'arr';
export type ExclusionKind = 'group_key' | 'path_prefix' | 'library' | 'title_regex';

export type ActionStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'dry_run'
  | 'skipped'
  | 'failed'
  | 'cancelled';
export type DeletionMethod = 'arr' | 'plex' | 'filesystem';

export type HistoryEventType =
  | 'scanCompleted'
  | 'scanFailed'
  | 'groupDetected'
  | 'groupApproved'
  | 'groupIgnored'
  | 'groupUnignored'
  | 'groupResolved'
  | 'fileDeleted'
  | 'fileDeleteDryRun'
  | 'fileDeleteFailed'
  | 'fileSkipped'
  | 'fileRestored'
  | 'overrideChanged'
  /** A security-relevant event: sign-ins, credential/authentication/settings/connection changes, backup downloads. */
  | 'security';

export type ScanStatus = 'running' | 'completed' | 'failed';
export type CommandStatus = 'queued' | 'started' | 'completed' | 'failed' | 'aborted';
export type CommandTrigger = 'manual' | 'scheduled' | 'webhook';
export type CommandName =
  | 'DuplicateScan'
  | 'TargetedScan'
  | 'ProcessQueue'
  | 'SyncLibraries'
  | 'CheckHealth'
  | 'Backup'
  | 'Housekeeping'
  | 'CleanRecycleBin';

export type NotificationTrigger =
  | 'onDuplicatesFound'
  | 'onFileDeleted'
  | 'onDeleteFailed'
  | 'onScanCompleted'
  | 'onHealthIssue'
  | 'onHealthRestored';
export type NotificationKind =
  | 'discord'
  | 'slack'
  | 'telegram'
  | 'pushover'
  | 'gotify'
  | 'ntfy'
  | 'apprise'
  | 'webhook'
  | 'email';

export type HealthType = 'ok' | 'notice' | 'warning' | 'error';
export type Mode = 'manual' | 'auto';
/** Basic auth is not supported (docs/DECISIONS.md D1). */
export type AuthenticationMethod = 'None' | 'Forms' | 'External';
export type AuthenticationRequired = 'Enabled' | 'DisabledForLocalAddresses';
export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error';
export type BackupType = 'scheduled' | 'manual' | 'update';

// ---------------------------------------------------------------------------
// Media (internal/models/media.go)
// ---------------------------------------------------------------------------

export interface AudioTrack {
  /** Normalized, see AudioFormat. */
  format: AudioFormat;
  /** Raw codec from the media server (e.g. "truehd", "dca"). */
  codec: string;
  /** Raw profile (e.g. "ma", "dts-hd ma"). */
  profile: string;
  /** e.g. 8 for 7.1 */
  channels: number;
  /** Display language, e.g. "English". */
  language: string;
  /** ISO 639-2/B when available, e.g. "eng". */
  languageCode: string;
  title: string;
  default: boolean;
  atmos: boolean;
}

export interface SubtitleTrack {
  codec: string;
  language: string;
  languageCode: string;
  forced: boolean;
  /** Sidecar file. */
  external: boolean;
}

export interface MediaPart {
  /** Plex Part id. */
  id: Id;
  /** Path as the media server sees it. */
  path: string;
  /** Path as Dupearr sees it ("" when unmapped/not mounted). */
  localPath: string;
  /** Bytes. */
  size: number;
  /** Milliseconds. */
  duration: number;
  /** From Plex checkFiles=1; absent when unknown. */
  exists?: boolean;
  accessible?: boolean;
  /** Other rating keys referencing the same file (multi-episode files). */
  sharedWith?: string[];
  /** Hardlink count when the local path could be stat'ed (absent/0 = unknown). */
  linkCount?: number;
  /** "<device>:<inode>" when stat'ed. */
  inode?: string;
}

export interface ArrFileInfo {
  instanceId: Id;
  instanceName: string;
  kind: ArrKind;
  /** moviefile / episodefile id */
  fileId: Id;
  /** movieId / seriesId */
  itemId: Id;
  episodeIds: Id[] | null;
  itemPath: string;
  monitored: boolean;
  /** e.g. "Bluray-2160p" */
  qualityName: string;
  qualitySource: string;
  qualityResolution: number;
  /** e.g. "remux" */
  qualityModifier: string;
  customFormats: string[] | null;
  customFormatScore?: number | null;
  releaseGroup: string;
  edition: string;
  languages: string[] | null;
  /** Raw mediaInfo.videoDynamicRangeType */
  dynamicRangeType: string;
  /** *arr movie/series tag labels (e.g. "dupearr-keep"). */
  tags: string[] | null;
  qualityCutoffNotMet: boolean;
  sceneName: string;
  dateAdded: IsoDateTime;
}

/**
 * A full-disc backup (models.DiscInfo): hundreds of files that make up ONE version. A disc is only
 * ever removed as a whole (its disc-owned entries moved to the recycle bin), never file by file.
 */
export interface DiscInfo {
  type: DiscType;
  /** Disc root as the media server / *arr sees it (folder containing BDMV/ etc., or the .iso path). */
  root: string;
  /** The same as Dupearr sees it ("" when not mapped). */
  localRoot: string;
  /** Number of discs in a multi-disc set (1 = single disc). */
  discs: number;
  /** Files under the disc root(s). */
  fileCount: number;
  /** e.g. "BDMV/PLAYLIST/00800.mpls" or "VIDEO_TS/VTS_01_0.IFO". */
  mainFeature?: string;
  /** The main feature's metadata was parsed (false = quality attributes unknown). */
  readable: boolean;
  origin: DiscOrigin;

  // Additive fields of models.DiscInfo (absent in older answers).
  /** Every disc of a multi-disc set, as the server sees them (roots[0] == root). */
  roots?: string[];
  /** The same as Dupearr sees them. */
  localRoots?: string[];
  /** Local paths a removal moves as a whole (BDMV/, CERTIFICATE/ …, a "Disc N" folder, the image). */
  ownedEntries?: string[];
  /** Bytes of every file of the disc (what a removal moves). */
  totalBytes?: number;
  /** Bytes of the main feature's clips (what ranking compares). */
  featureBytes?: number;
  /** Bytes a removal frees (hardlinked files free nothing). */
  freedBytes?: number;
  /** Files of the disc with more than one hard link. */
  hardlinkedFiles?: number;
  fingerprint?: string;
  newestModTime?: IsoDateTime;
  /** A Blu-ray 3D disc (BDMV/STREAM/SSIF holds files). */
  is3d?: boolean;
  /** Other feature-length cuts on the disc. */
  alternates?: number;
  /** The disc itself allows a whole-disc removal (the settings apply on top). */
  removable: boolean;
  /** Why the disc could not be read or verified completely. */
  problem?: string;
  /** The file inside the disc an *arr tracks (its path as the *arr sees it). */
  trackedClip?: string;
  /** Other Plex items (rating keys) exposing files of this disc: it is protected. */
  plexItems?: string[];
  /**
   * Loose clip sets (`bluray_clips` / `dvd_clips`) only: the number of clip files in the set (the
   * UI falls back to counting the clip-named parts when absent).
   */
  clipCount?: number;
  /** Loose clip sets only: the Plex media ids (one per loose clip) merged into this one version. */
  mediaIds?: Id[];
}

export interface MediaVersion {
  /**
   * "plex:<serverID>:<mediaID>"; a disc detected on disk is
   * "disc:<serverID>:<sha1 hex of its normalized root>".
   */
  key: string;
  serverId: Id;
  /** Dupearr library id. */
  libraryId: Id;
  libraryTitle: string;
  sectionKey: string;
  ratingKey: string;
  mediaId: Id;
  itemTitle: string;
  /** e.g. "4K DoVi/HDR10 (HEVC Main 10)" */
  displayTitle: string;
  optimizedVersion: boolean;

  parts: MediaPart[];

  container: string;
  durationMs: number;
  /** Overall bitrate. */
  bitrateKbps: number;
  /** Video stream bitrate, 0 if unknown. */
  videoBitrateKbps: number;
  width: number;
  height: number;
  resolution: ResolutionTier;
  videoCodec: VideoCodec;
  videoProfile: string;
  bitDepth: number;
  frameRate: string;
  dynamicRange: DynamicRange;
  dvProfile?: number;
  audioTracks: AudioTrack[] | null;
  subtitleTracks: SubtitleTrack[] | null;
  source: ReleaseSource;
  /** Normalized edition, "" = none/theatrical. */
  edition: string;
  addedAt: IsoDateTime;

  arr?: ArrFileInfo | null;
  /** Set when this version is a full-disc backup (absent/null = a regular file). */
  disc?: DiscInfo | null;
  /**
   * Play history of the version's Plex item (docs/DECISIONS.md D10); absent/null = no
   * play-history source (Tautulli) for its media server.
   */
  watch?: WatchInfo | null;
}

/** WatchInfo.status: an unknown or failed history is never "no plays". */
export type WatchStatus = 'known' | 'unknown' | 'failed';

/** Play history of a Plex item (shared by all its versions), read during the last scan. */
export interface WatchInfo {
  /** "tautulli" */
  source: string;
  /** The connection's name. */
  sourceName: string;
  status: WatchStatus;
  /** Why the status is unknown or failed. */
  reason?: string;
  /** Recorded plays (status known; 0 = "no plays recorded" since `since`). */
  plays: number;
  /** Distinct users among them. */
  users: number;
  lastPlayed?: IsoDateTime;
  /**
   * With plays: the earliest recorded play of the item's library (the history covers plays since).
   * Without plays: the item's date added — the copy only loses to a play from then on.
   */
  since?: IsoDateTime;
  readAt?: IsoDateTime;
}

export interface MediaItem {
  serverId: Id;
  libraryId: Id;
  libraryTitle: string;
  sectionKey: string;
  ratingKey: string;
  mediaType: MediaType;
  title: string;
  year: number;
  showTitle: string;
  season: number;
  episode: number;
  externalIds: Record<string, string> | null;
  showIds: Record<string, string> | null;
  editionTitle: string;
  thumb: string;
  addedAt: IsoDateTime;
  versions: MediaVersion[];
}

// ---------------------------------------------------------------------------
// Duplicate groups
// ---------------------------------------------------------------------------

export interface GroupFile {
  id: Id;
  groupId: Id;
  version: MediaVersion;
  /** Effective decision (override when set, else engine decision). */
  decision: Decision;
  /** What the profile decided, before user overrides. */
  engineDecision: Decision;
  /** The user's manual decision (absent/"" = none). */
  override?: Decision | '';
  /** 1 = best */
  rank: number;
  reasons: string[] | null;
  /** Criterion type that decided vs the best keeper. */
  decidingCriterion: CriterionType | '';
  protected: boolean;
  protectedReason?: string;
  /** Criterion type → display value for the comparison table. */
  values: Partial<Record<CriterionType, string>> | null;
}

export interface DuplicateGroup {
  id: Id;
  key: string;
  mediaType: MediaType;
  /** Movie title or episode title. */
  title: string;
  year: number;
  showTitle?: string;
  season?: number;
  episode?: number;
  serverId: Id;
  libraryIds: Id[];
  externalIds: Record<string, string> | null;
  thumb?: string;
  status: GroupStatus;
  statusReason?: string;
  flags: GroupFlag[];
  profileId: Id;
  files: GroupFile[];
  /** Sum of sizes of versions decided "remove". */
  reclaimableBytes: number;
  firstSeenAt: IsoDateTime;
  lastSeenAt: IsoDateTime;
  updatedAt: IsoDateTime;
  lastScanId: Id;
  /** Hash of version keys + decisions; stableCount = consecutive scans with the same signature. */
  signature: string;
  stableCount: number;
}

/** GET /api/v1/duplicate/{id} — full group plus its actions. */
export interface DuplicateGroupDetail extends DuplicateGroup {
  actions: Action[];
}

/** Disc facts of a DuplicateGroupSummaryFile (the list shows no paths). */
export interface DuplicateGroupSummaryDisc {
  type: DiscType;
  fileCount: number;
  discs: number;
}

/** File row of a DuplicateGroupSummary. */
export interface DuplicateGroupSummaryFile {
  id: Id;
  decision: Decision;
  resolution: ResolutionTier;
  dynamicRange: DynamicRange;
  videoCodec: VideoCodec;
  size: number;
  libraryTitle: string;
  arrInstanceName?: string;
  /** Full-disc backup facts; null/absent = a regular file. */
  disc?: DuplicateGroupSummaryDisc | null;
  /**
   * Optional (sent by newer servers): this copy is a single file of a disc — a loose clip
   * (`00800.m2ts`, `VTS_01_1.VOB`) or a file inside BDMV/ / VIDEO_TS/. Such a copy is never
   * removed on its own, so the list never approves its group.
   */
  discClip?: boolean;
}

/** Row of GET /api/v1/duplicate (paged). */
export interface DuplicateGroupSummary {
  id: Id;
  key: string;
  mediaType: MediaType;
  title: string;
  year: number;
  showTitle?: string;
  season?: number;
  episode?: number;
  serverId: Id;
  libraryIds: Id[];
  thumb?: string;
  status: GroupStatus;
  statusReason?: string;
  flags: GroupFlag[];
  profileId: Id;
  fileCount: number;
  keepCount: number;
  removeCount: number;
  reclaimableBytes: number;
  bestResolution: ResolutionTier;
  firstSeenAt: IsoDateTime;
  lastSeenAt: IsoDateTime;
  files: DuplicateGroupSummaryFile[];
  /**
   * The group's current versions + decisions (same value as DuplicateGroup.signature). Sent back
   * with an approval so the server refuses it (409) when the decisions changed since they were
   * shown.
   */
  signature: string;
}

export type DuplicateSortKey = 'title' | 'lastSeenAt' | 'firstSeenAt' | 'reclaimableBytes' | 'status';

/** Query of GET /api/v1/duplicate. */
export interface DuplicateListParams extends PagingParams {
  sortKey?: DuplicateSortKey;
  /** Sent as a comma list. */
  status?: GroupStatus[];
  mediaType?: MediaType;
  libraryId?: Id;
  serverId?: Id;
  flag?: GroupFlag;
  search?: string;
}

/** GET /api/v1/duplicate/stats */
export interface DuplicateStats {
  total: number;
  byStatus: Partial<Record<GroupStatus, number>>;
  /** Over pending + review + queued groups. */
  reclaimableBytes: number;
  /** Sum of succeeded (non-dry-run) action sizes. */
  reclaimedBytes: number;
  lastScan: ScanRun | null;
}

/** POST /api/v1/duplicate/{id}/ignore body. */
export interface IgnoreDuplicateRequest {
  addExclusion: boolean;
}

/** PUT /api/v1/duplicate/{id}/file/{fileId}/override body. */
export interface FileOverrideRequest {
  decision: Decision | null;
}

/**
 * POST /api/v1/duplicate/{id}/approve body (optional). `signature` is the group's signature as
 * the user reviewed it: a group whose decisions changed since is refused with 409.
 */
export interface ApproveDuplicateRequest {
  signature?: string;
}

export type BulkDuplicateAction = 'approve' | 'ignore' | 'unignore';

/** POST /api/v1/duplicate/bulk body. */
export interface BulkDuplicateRequest {
  ids: Id[];
  action: BulkDuplicateAction;
  /**
   * Approve only: group id → signature of the row the user reviewed. A group whose decisions
   * changed since is not approved (it is reported in `failed` with the server's message).
   */
  signatures?: Record<Id, string>;
}

/** POST /api/v1/duplicate/bulk response. */
export interface BulkDuplicateResponse {
  succeeded: Id[];
  failed: { id: Id; message: string }[];
}

// ---------------------------------------------------------------------------
// Decision profiles
// ---------------------------------------------------------------------------

export interface PatternScore {
  pattern: string;
  score: number;
  /** false = glob (doublestar) against the full path */
  regex: boolean;
  caseSensitive: boolean;
}

export interface Criterion {
  type: CriterionType;
  enabled: boolean;
  /** Numeric criteria. */
  direction?: Direction;
  /** Ordered criteria: best first. */
  order?: string[];
  /** Numeric equality tolerance (relative %). */
  tolerancePercent?: number;
  /** Numeric equality tolerance (absolute, e.g. custom format score 10). */
  minDelta?: number;
  /** filename_score */
  patterns?: PatternScore[];
  /** arr_managed instance id / audio_language code */
  value?: string;
}

export interface Protection {
  type: ProtectionType;
  value: string;
}

export interface Profile {
  id: Id;
  name: string;
  isDefault: boolean;
  criteria: Criterion[];
  /** ≥ 1 */
  keepCount: number;
  keepPer: KeepPer;
  protections: Protection[];
  createdAt: IsoDateTime;
  updatedAt: IsoDateTime;
}

/** Body for POST/PUT /api/v1/profile (server fills id/timestamps). */
export type ProfileInput = Omit<Profile, 'id' | 'createdAt' | 'updatedAt'> & { id?: Id };

export interface SchemaOption {
  value: string;
  label: string;
}

/** engine.CriterionSchema */
export interface CriterionSchema {
  type: CriterionType;
  label: string;
  description: string;
  kind: CriterionKind;
  /** Ordered: all possible values. */
  options?: SchemaOption[];
  defaultOrder?: string[];
  defaultDirection?: Direction;
  supportsTolerance: boolean;
  requiresArr: boolean;
  /** Ranks by play history: needs a Tautulli connection (unknown histories tie). */
  requiresWatchHistory?: boolean;
  /** Set when the criterion takes a minimum difference without a tolerance: its unit ("days"). */
  minDeltaUnit?: string;
}

/** GET /api/v1/profile/schema */
export interface ProfileSchema {
  criteria: CriterionSchema[];
  templates: Profile[];
  protectionTypes: ProtectionType[];
  keepPer: KeepPer[];
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

export interface MediaServer {
  id: Id;
  name: string;
  kind: MediaServerKind;
  /** e.g. http://192.168.1.10:32400 */
  url: string;
  /** X-Plex-Token (masked "********" in responses; send the mask back to keep it). */
  token: string;
  machineIdentifier: string;
  verifyTls: boolean;
  enabled: boolean;
  createdAt: IsoDateTime;
  updatedAt: IsoDateTime;
}

export type MediaServerInput = Omit<MediaServer, 'id' | 'createdAt' | 'updatedAt' | 'machineIdentifier'> & {
  id?: Id;
  machineIdentifier?: string;
};

/** POST /api/v1/mediaserver/test response. */
export interface MediaServerTestResult {
  version: string;
  machineIdentifier: string;
  friendlyName: string;
  mediaDeletionAllowed: boolean;
  /**
   * Whether the token belongs to the server owner (only the owner's token can delete media via
   * Plex). null/absent = could not be determined.
   */
  owned?: boolean | null;
}

export interface Library {
  id: Id;
  serverId: Id;
  sectionKey: string;
  title: string;
  /** Plex section type: "movie" | "show" */
  type: 'movie' | 'show' | (string & {});
  locations: string[] | null;
  enabled: boolean;
  /** null = default profile */
  profileId: Id | null;
  /** Libraries sharing a non-empty scope group are cross-matched. */
  scopeGroup: string;
  updatedAt: IsoDateTime;
}

/** POST /api/v1/plex/pin */
export interface PlexPin {
  id: Id;
  code: string;
  authUrl: string;
}

/** GET /api/v1/plex/pin/{id} */
export interface PlexPinStatus {
  authenticated: boolean;
  authToken: string;
}

export interface PlexConnection {
  uri: string;
  address: string;
  port: number;
  protocol: string;
  local: boolean;
  relay: boolean;
}

/** GET /api/v1/plex/servers (plex.tv account token in the X-Plex-Token header) */
export interface PlexServer {
  name: string;
  clientIdentifier: string;
  productVersion: string;
  owned: boolean;
  accessToken: string;
  connections: PlexConnection[];
}

export interface ArrInstance {
  id: Id;
  name: string;
  kind: ArrKind;
  /** Base url incl. url base, e.g. http://radarr:7878 */
  url: string;
  /** Masked in responses. */
  apiKey: string;
  verifyTls: boolean;
  enabled: boolean;
  tags: string[] | null;
  createdAt: IsoDateTime;
  updatedAt: IsoDateTime;
}

export type ArrInstanceInput = Omit<ArrInstance, 'id' | 'createdAt' | 'updatedAt'> & { id?: Id };

/** POST /api/v1/arr/test response. */
export interface ArrTestResult {
  appName: string;
  version: string;
  instanceName: string;
  recycleBin: string;
  recycleBinCleanupDays: number;
}

/** Tautulli connection: the play history of one Plex server (docs/API.md → Watch history). */
export interface TautulliInstance {
  id: Id;
  name: string;
  /** The media server whose plays it records (one Tautulli per server). */
  serverId: Id;
  /** Base url incl. HTTP root, e.g. http://tautulli:8181 */
  url: string;
  /** Masked in responses. */
  apiKey: string;
  verifyTls: boolean;
  enabled: boolean;
  createdAt: IsoDateTime;
  updatedAt: IsoDateTime;
}

export type TautulliInstanceInput = Omit<TautulliInstance, 'id' | 'createdAt' | 'updatedAt'> & { id?: Id };

/** POST /api/v1/tautulli/test response. */
export interface TautulliTestResult {
  version: string;
  pmsName: string;
  pmsIdentifier: string;
  serverMatches: boolean;
  /** Earliest recorded play in the server's enabled libraries (null: none yet). */
  historySince: IsoDateTime | null;
  /** Enabled libraries whose history Tautulli does not keep. */
  librariesWithoutHistory: string[];
  /** Active users whose history is not kept (names are never shown). */
  usersWithoutHistory: number;
}

export interface PathMapping {
  id: Id;
  sourceType: PathSourceType;
  /** Media server id or arr instance id. */
  sourceId: Id;
  remotePath: string;
  localPath: string;
}

export type PathMappingInput = Omit<PathMapping, 'id'> & { id?: Id };

export interface Exclusion {
  id: Id;
  kind: ExclusionKind;
  value: string;
  title?: string;
  reason?: string;
  createdAt: IsoDateTime;
}

export type ExclusionInput = Omit<Exclusion, 'id' | 'createdAt'>;

// ---------------------------------------------------------------------------
// Actions, history, scans, commands, tasks
// ---------------------------------------------------------------------------

export interface Action {
  id: Id;
  groupId: Id;
  groupFileId: Id;
  versionKey: string;
  /** Display title of the group. */
  title: string;
  /** Server-side paths of the version's parts. */
  paths: string[];
  size: number;
  /** Chosen method once executed. */
  method?: DeletionMethod | '';
  status: ActionStatus;
  dryRun: boolean;
  message?: string;
  /** Where the file went (filesystem + recycle bin). */
  recyclePath?: string;
  /** True when the removal cannot be undone (no recycle bin anywhere). */
  permanent: boolean;
  createdAt: IsoDateTime;
  startedAt?: IsoDateTime | null;
  finishedAt?: IsoDateTime | null;
}

export interface ActionListParams extends PagingParams {
  /** Sent as a comma list. */
  status?: ActionStatus[];
}

export interface HistoryEvent {
  id: Id;
  eventType: HistoryEventType;
  groupId?: Id | null;
  actionId?: Id | null;
  title: string;
  message: string;
  data?: unknown;
  createdAt: IsoDateTime;
}

export interface HistoryListParams extends PagingParams {
  /** Sent as a comma list. */
  eventType?: HistoryEventType[];
  groupId?: Id;
}

export interface ScanStats {
  libraries: number;
  itemsExamined: number;
  groupsFound: number;
  newGroups: number;
  resolvedGroups: number;
  pendingGroups: number;
  reviewGroups: number;
  reclaimableBytes: number;
  autoApproved: number;
  errors: number;
}

export interface ScanRun {
  id: Id;
  trigger: CommandTrigger;
  /** Targeted scans never resolve unseen groups. */
  targeted: boolean;
  status: ScanStatus;
  stats: ScanStats;
  error?: string;
  startedAt: IsoDateTime;
  finishedAt?: IsoDateTime | null;
}

export interface Command {
  id: Id;
  name: CommandName;
  body: unknown;
  status: CommandStatus;
  trigger: CommandTrigger;
  message?: string;
  /** Note: JSON names are `queued` / `started` / `ended` (like *arr). */
  queued: IsoDateTime;
  started?: IsoDateTime | null;
  ended?: IsoDateTime | null;
  /** "00:00:12.345" */
  duration?: string;
}

export interface DuplicateScanBody {
  libraryIds?: Id[];
}

export interface TargetedScanBody {
  serverId?: Id;
  ratingKeys?: string[];
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
}

export interface SyncLibrariesBody {
  serverId?: Id;
}

/** Body of POST /api/v1/command — discriminated by `name`. */
export type CommandRequest =
  | ({ name: 'DuplicateScan' } & DuplicateScanBody)
  | ({ name: 'TargetedScan' } & TargetedScanBody)
  | ({ name: 'SyncLibraries' } & SyncLibrariesBody)
  | { name: 'ProcessQueue' }
  | { name: 'CheckHealth' }
  | { name: 'Backup'; type?: 'manual' }
  | { name: 'Housekeeping' }
  | { name: 'CleanRecycleBin' };

export interface ScheduledTask {
  /** Human name, e.g. "Duplicate Scan". */
  name: string;
  /** Command name. */
  taskName: CommandName;
  /** Minutes; 0 = disabled. */
  interval: number;
  lastExecution?: IsoDateTime | null;
  lastStartTime?: IsoDateTime | null;
  lastDuration?: string;
  nextExecution?: IsoDateTime | null;
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

export interface NotificationConfig {
  id: Id;
  name: string;
  kind: NotificationKind;
  /** Provider-specific fields (see ProviderSchema). Secrets masked as "********". */
  settings: Record<string, unknown>;
  triggers: NotificationTrigger[];
  enabled: boolean;
}

export type NotificationConfigInput = Omit<NotificationConfig, 'id'> & { id?: Id };

export type FieldSchemaType = 'text' | 'password' | 'url' | 'number' | 'checkbox' | 'select' | 'textarea';

/** notifications.FieldSchema */
export interface FieldSchema {
  name: string;
  label: string;
  type: FieldSchemaType;
  helpText?: string;
  required?: boolean;
  advanced?: boolean;
  secret?: boolean;
  options?: string[] | null;
  default?: unknown;
}

/** notifications.ProviderSchema — GET /api/v1/notification/schema */
export interface ProviderSchema {
  kind: NotificationKind;
  name: string;
  infoUrl?: string;
  fields: FieldSchema[];
}

/** GET /api/v1/notification/triggers */
export type NotificationTriggerOption = SelectOptionDto & { value: NotificationTrigger };

// ---------------------------------------------------------------------------
// Health / system
// ---------------------------------------------------------------------------

export interface HealthCheck {
  /** Check name, e.g. "PlexConnectivityCheck". */
  source: string;
  type: HealthType;
  message: string;
  wikiUrl?: string;
}

export interface SystemStatus {
  appName: 'Dupearr' | (string & {});
  instanceName: string;
  version: string;
  commit: string;
  buildDate: string;
  startTime: IsoDateTime;
  uptimeSeconds: number;
  osName: string;
  osArch: string;
  goVersion: string;
  isDocker: boolean;
  dataDirectory: string;
  configFile: string;
  databaseFile: string;
  databaseSize: number;
  urlBase: string;
  authentication: AuthenticationMethod | (string & {});
  dryRun: boolean;
  mode: Mode;
}

/** logging.Entry — GET /api/v1/log (paged) */
export interface LogEntry {
  time: IsoDateTime;
  level: LogLevel | (string & {});
  logger: string;
  message: string;
  exception?: string;
}

export interface LogListParams extends PagingParams {
  /** Minimum level. */
  level?: LogLevel;
}

/** logging.LogFile */
export interface LogFile {
  filename: string;
  lastWriteTime: IsoDateTime;
  size: number;
}

/** backup.Backup — Path = "/backup/<type>/<name>" (download relative to urlBase). */
export interface Backup {
  id: Id;
  name: string;
  path: string;
  type: BackupType;
  size: number;
  time: IsoDateTime;
}

/** POST /api/v1/system/backup/restore/{id} (and /upload) response. */
/** POST /system/backup/restore/confirm → the server restarts to apply the staged restore. */
export interface RestoreResponse {
  restartRequired: boolean;
}

/** One setting that differs between the backup and the running instance (backup.RestoreChange). */
export interface RestoreChange {
  /**
   * HostConfig / Settings property name, "users", "webhookToken", "mediaServers", "arrInstances",
   * "tautulliInstances", "pathMappings" or "notifications".
   */
  setting: string;
  current: string;
  backup: string;
  /** true: the restore uses the backup's value; false: the current value is kept. */
  applied: boolean;
  message?: string;
}

/** What a staged restore changes (backup.RestoreSummary). */
export interface RestoreSummary {
  securitySettingsRestored: boolean;
  changes: RestoreChange[] | null;
  cancelledRemovals: number;
  interruptedRemovals: number;
  reopenedGroups: number;
}

/**
 * POST /system/backup/restore/{id} and /system/backup/restore/upload: the backup is staged for
 * review; nothing changes until POST /system/backup/restore/confirm (DELETE /system/backup/restore
 * discards it; a restart discards an unconfirmed restore too).
 */
export interface RestoreStagedResponse {
  staged: boolean;
  restartRequired: boolean;
  summary: RestoreSummary | null;
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

/** GET/PUT /api/v1/config/host */
export interface HostConfig {
  bindAddress: string;
  port: number;
  urlBase: string;
  enableSsl: boolean;
  sslPort: number;
  sslCertPath: string;
  sslKeyPath: string;
  /**
   * Masked ("********") for browser sessions when a Forms account exists; revealed by
   * POST /config/host/apikey/reveal (current password). Sending the mask back keeps the key.
   */
  apiKey: string;
  authenticationMethod: AuthenticationMethod;
  authenticationRequired: AuthenticationRequired;
  /**
   * Reverse proxies whose forwarding headers Dupearr believes: IP addresses or CIDR ranges,
   * comma-separated (returned as "a, b"). Takes effect with the next request.
   */
  trustedProxies: string;
  /** Host names Dupearr is reached by through the proxy ("*.example.com" for sub-domains), comma-separated. */
  allowedHosts: string;
  username: string;
  /** Write-only, masked. */
  password: string;
  /** Write-only. */
  passwordConfirmation?: string;
  logLevel: LogLevel;
  logSizeLimit: number;
  instanceName: string;
  launchBrowser: boolean;
  branch: string;
  /** Read-only: fields forced by DUPEARR__ env vars. */
  envOverrides: string[] | null;
  /** PUT response: true when port/bind/ssl/urlBase changed. */
  restartRequired?: boolean;
  /** Read-only: the credential of the webhook URLs (can only queue scans). */
  webhookToken?: string;
  /** Write-only: confirms a change of the username, password, authentication, API key or trust lists. */
  currentPassword?: string;
  /** Write-only: keep the API key when the password changes (it is replaced by default). */
  keepApiKey?: boolean;
  /**
   * Write-only: save trust lists that stop trusting the browser making the change (External's
   * proxy, None's host rule); without it the server refuses such a change (400 confirmTrustChange).
   */
  confirmTrustChange?: boolean;
}

/** GET/PUT /api/v1/config/settings (models.Settings) */
export interface Settings {
  dryRun: boolean;
  mode: Mode;
  scanIntervalMinutes: number;
  minAgeHours: number;
  maxDeletionsPerRun: number;
  /** Ordered. */
  deletionMethods: DeletionMethod[];
  arrRescanAfterDelete: boolean;
  unmonitorWhenKeeperElsewhere: boolean;
  addExclusionWhenKeeperElsewhere: boolean;
  recycleBinPath: string;
  recycleBinCleanupDays: number;
  refreshPlexAfterDelete: boolean;
  cleanupPlexStaleEntries: boolean;
  treatEditionsAsDistinct: boolean;
  treat3DAsDistinct: boolean;
  languageVariantsAsDistinct: boolean;
  differentArrInstancesIntentional: boolean;
  maxGroupSize: number;
  stableScansRequired: number;
  maxBytesPerRunGb: number;
  durationTolerancePercent: number;
  durationToleranceMinutes: number;
  historyRetentionDays: number;
  backupIntervalDays: number;
  backupRetentionDays: number;
  /** Detect full-disc backups (BDMV / VIDEO_TS / ISO …) next to Plex items (default true). */
  detectDiscs: boolean;
  /**
   * Allow removing a full disc (default false). Even then: manual approval only, filesystem method
   * only, into the recycle bin only (refused when none is set).
   */
  allowDiscRemoval: boolean;
  /** A disc is never the only kept copy: the best non-disc copy is kept too (default true). */
  keepPlayableCopy: boolean;
}

// ---------------------------------------------------------------------------
// Auth / bootstrap
// ---------------------------------------------------------------------------

/** GET {urlBase}/initialize.json */
export interface InitializeResponse {
  apiRoot: string;
  urlBase: string;
  version: string;
  instanceName: string;
  authenticationMethod: AuthenticationMethod;
  commit?: string;
}

/** GET /api/v1/auth/status (no auth) */
export interface AuthStatus {
  setupRequired: boolean;
  authenticationMethod: AuthenticationMethod;
  authenticated: boolean;
  /**
   * While setup is required: the authentication settings environment variables force, e.g.
   * `{ authenticationRequired: 'DisabledForLocalAddresses' }` (shown read-only by the setup page).
   */
  envForced?: Partial<Record<'authenticationMethod' | 'authenticationRequired', string>>;
}

/** POST /api/v1/auth/setup (no auth, local clients only, only while setupRequired) */
export interface AuthSetupRequest {
  authenticationMethod: Extract<AuthenticationMethod, 'Forms'>;
  authenticationRequired: AuthenticationRequired;
  username: string;
  password: string;
  passwordConfirmation: string;
  /** The one-time code Dupearr prints in its log while setup is pending. */
  setupCode: string;
}

/** POST {urlBase}/login */
export interface LoginRequest {
  username: string;
  password: string;
  rememberMe: boolean;
}

// ---------------------------------------------------------------------------
// Webhooks (inbound) — response of /api/v1/webhook/*
// ---------------------------------------------------------------------------

export interface WebhookResponse {
  queued: boolean;
  commandId?: Id;
}

// ---------------------------------------------------------------------------
// Server-Sent Events (/api/v1/events)
// ---------------------------------------------------------------------------

export type ServerEventAction = 'updated' | 'deleted' | 'sync' | 'progress';

export interface ScanProgressResource {
  message: string;
  scanId: Id;
}

/** Discriminated union of every event the server pushes. */
export type ServerEvent =
  | { name: 'duplicate'; action: 'updated' | 'sync'; resource?: DuplicateGroupSummary }
  | { name: 'duplicate'; action: 'deleted'; resource?: { id: Id } }
  | { name: 'queue'; action: ServerEventAction; resource?: Action }
  | { name: 'history'; action: ServerEventAction; resource?: HistoryEvent }
  | { name: 'command'; action: ServerEventAction; resource?: Command }
  | { name: 'health'; action: ServerEventAction; resource?: HealthCheck[] }
  | { name: 'task'; action: ServerEventAction; resource?: ScheduledTask }
  | { name: 'scan'; action: 'progress'; resource?: ScanProgressResource }
  | { name: 'scan'; action: 'updated' | 'sync' | 'deleted'; resource?: ScanRun }
  | { name: 'settings'; action: ServerEventAction; resource?: Record<string, never> };

export type ServerEventName = ServerEvent['name'];
