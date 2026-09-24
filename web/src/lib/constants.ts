/**
 * Label maps (and display metadata) for every enum in internal/models + the API.
 * Use `labelOf(MAP, value)` to look up with a graceful fallback for unknown values.
 */
import type {
  ActionStatus,
  ArrKind,
  AudioFormat,
  AuthenticationMethod,
  AuthenticationRequired,
  BackupType,
  CommandName,
  CommandStatus,
  CommandTrigger,
  ContainerFormat,
  CriterionKind,
  CriterionType,
  Decision,
  DeletionMethod,
  Direction,
  DiscOrigin,
  DiscType,
  DynamicRange,
  ExclusionKind,
  GroupFlag,
  GroupStatus,
  HealthType,
  HistoryEventType,
  KeepPer,
  LogLevel,
  MediaServerKind,
  MediaType,
  Mode,
  NotificationKind,
  NotificationTrigger,
  PathSourceType,
  ProtectionType,
  ReleaseSource,
  ResolutionTier,
  ScanStatus,
  VideoCodec,
} from '@/api/types';

/** Visual "kind" shared by Badge/Alert/status colours. */
export type StatusKind =
  | 'default'
  | 'primary'
  | 'accent'
  | 'success'
  | 'warning'
  | 'danger'
  | 'info'
  | 'pink'
  | 'inverse';

/** Title-cases an identifier: "dry_run" → "Dry Run", "onFileDeleted" → "On File Deleted". */
export function humanize(value: string): string {
  return value
    .replace(/[_-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

/** Looks up a label, falling back to a humanized value (never throws on unknown values). */
export function labelOf<K extends string>(map: Readonly<Record<K, string>>, value: NoInfer<K> | string | null | undefined): string {
  if (value === null || value === undefined) return '';
  const hit = (map as Record<string, string>)[value];
  return hit ?? humanize(value);
}

/** Converts a label map to `{value, label}[]` options (in map order). */
export function toOptions<K extends string>(map: Readonly<Record<K, string>>, order?: readonly K[]) {
  const keys = order ?? (Object.keys(map) as K[]);
  return keys.map((value) => ({ value, label: map[value] }));
}

// ---------------------------------------------------------------------------
// Media attributes
// ---------------------------------------------------------------------------

export const MEDIA_TYPE_LABELS: Record<MediaType, string> = {
  movie: 'Movie',
  episode: 'Episode',
};

/** Best first (default resolution criterion order). */
export const RESOLUTION_ORDER: readonly ResolutionTier[] = ['2160', '1440', '1080', '720', '576', '480', 'sd'];

export const RESOLUTION_LABELS: Record<ResolutionTier, string> = {
  '2160': '2160p (4K)',
  '1440': '1440p',
  '1080': '1080p',
  '720': '720p',
  '576': '576p',
  '480': '480p',
  sd: 'SD',
};

/** Short badges for tight spaces. */
export const RESOLUTION_SHORT_LABELS: Record<ResolutionTier, string> = {
  '2160': '4K',
  '1440': '1440p',
  '1080': '1080p',
  '720': '720p',
  '576': '576p',
  '480': '480p',
  sd: 'SD',
};

export const DYNAMIC_RANGE_ORDER: readonly DynamicRange[] = ['dv_hdr10', 'hdr10plus', 'hdr10', 'dv', 'hlg', 'sdr'];

export const DYNAMIC_RANGE_LABELS: Record<DynamicRange, string> = {
  dv_hdr10: 'Dolby Vision (HDR10)',
  dv: 'Dolby Vision',
  hdr10plus: 'HDR10+',
  hdr10: 'HDR10',
  hlg: 'HLG',
  sdr: 'SDR',
};

export const DYNAMIC_RANGE_SHORT_LABELS: Record<DynamicRange, string> = {
  dv_hdr10: 'DV HDR10',
  dv: 'DV',
  hdr10plus: 'HDR10+',
  hdr10: 'HDR10',
  hlg: 'HLG',
  sdr: 'SDR',
};

export const VIDEO_CODEC_ORDER: readonly VideoCodec[] = ['hevc', 'av1', 'h264', 'vc1', 'mpeg2', 'mpeg4', 'vp9', 'other'];

export const VIDEO_CODEC_LABELS: Record<VideoCodec, string> = {
  av1: 'AV1',
  hevc: 'HEVC (H.265)',
  h264: 'AVC (H.264)',
  vc1: 'VC-1',
  mpeg2: 'MPEG-2',
  mpeg4: 'MPEG-4',
  vp9: 'VP9',
  other: 'Other',
};

export const AUDIO_FORMAT_ORDER: readonly AudioFormat[] = [
  'truehd_atmos',
  'truehd',
  'dtsx',
  'dts_hd_ma',
  'dts_hd_hra',
  'eac3_atmos',
  'flac',
  'pcm',
  'dts',
  'eac3',
  'ac3',
  'aac',
  'opus',
  'mp3',
  'other',
];

export const AUDIO_FORMAT_LABELS: Record<AudioFormat, string> = {
  truehd_atmos: 'TrueHD Atmos',
  truehd: 'TrueHD',
  dtsx: 'DTS:X',
  dts_hd_ma: 'DTS-HD MA',
  dts_hd_hra: 'DTS-HD HRA',
  eac3_atmos: 'EAC3 Atmos',
  flac: 'FLAC',
  pcm: 'PCM',
  dts: 'DTS',
  eac3: 'EAC3 (DD+)',
  ac3: 'AC3 (DD)',
  aac: 'AAC',
  opus: 'Opus',
  mp3: 'MP3',
  other: 'Other',
};

/**
 * Best first (the engine's default source order): a full disc carries the same audio/video as a
 * remux but Plex cannot play it, so it ranks right after `remux`.
 */
export const SOURCE_ORDER: readonly ReleaseSource[] = [
  'remux',
  'disc',
  'bluray',
  'webdl',
  'webrip',
  'hdtv',
  'dvd',
  'sdtv',
  'unknown',
];

export const SOURCE_LABELS: Record<ReleaseSource, string> = {
  remux: 'Remux',
  disc: 'Full disc (BDMV/VIDEO_TS/ISO)',
  bluray: 'Blu-ray',
  webdl: 'WEB-DL',
  webrip: 'WEBRip',
  hdtv: 'HDTV',
  dvd: 'DVD',
  sdtv: 'SDTV',
  unknown: 'Unknown',
};

/** Display order of the containers (the engine's default ranking is in comparison.ts). */
export const CONTAINER_ORDER: readonly ContainerFormat[] = ['mkv', 'mp4', 'm4v', 'm2ts', 'avi', 'ts', 'other'];

export const CONTAINER_LABELS: Record<ContainerFormat, string> = {
  mkv: 'MKV',
  mp4: 'MP4',
  m4v: 'M4V',
  m2ts: 'M2TS',
  avi: 'AVI',
  ts: 'TS',
  other: 'Other',
  disc: 'Full disc',
};

// ---------------------------------------------------------------------------
// Full-disc backups (docs/research/disc-structures.md §6.8)
// ---------------------------------------------------------------------------

/** Badge text of a disc version: "UHD Blu-ray disc (BDMV)". */
export const DISC_TYPE_LABELS: Record<DiscType, string> = {
  uhd_bluray: 'UHD Blu-ray disc (BDMV)',
  bluray: 'Blu-ray disc (BDMV)',
  dvd: 'DVD (VIDEO_TS)',
  hddvd: 'HD DVD',
  avchd: 'AVCHD',
  bdav: 'Blu-ray recording (BDAV)',
  iso: 'ISO image',
  bluray_clips: 'Blu-ray clips (loose .m2ts)',
  dvd_clips: 'DVD files (loose VOB)',
};

/** Short disc labels for the list's mini-chips: "UHD BD". */
export const DISC_TYPE_SHORT_LABELS: Record<DiscType, string> = {
  uhd_bluray: 'UHD BD',
  bluray: 'BD',
  dvd: 'DVD',
  hddvd: 'HD DVD',
  avchd: 'AVCHD',
  bdav: 'BDAV',
  iso: 'ISO',
  bluray_clips: 'BD clips',
  dvd_clips: 'DVD VOBs',
};

/**
 * Loose clip sets: a flattened disc backup whose clips (00800.m2ts …, VTS_01_1.VOB …) lie loose in
 * the movie folder. Plex lists every loose clip as its own version; the server merges them into ONE
 * disc version.
 */
export const DISC_CLIP_SET_TYPES: readonly DiscType[] = ['bluray_clips', 'dvd_clips'];

/** Origin badge of a loose clip set (instead of {@link DISC_ORIGIN_LABELS}). */
export const CLIP_SET_ORIGIN_LABELS: Record<DiscOrigin, string> = {
  filesystem: 'Not in Plex',
  plex: 'Plex: one version per clip',
};

export const CLIP_SET_ORIGIN_DESCRIPTIONS: Record<DiscOrigin, string> = {
  filesystem:
    'Found on disk in this title’s folder; Plex does not list these clips. A movie can span several clips, so they count as one copy and a clip is never removed on its own.',
  plex: 'Plex lists every loose clip of this folder as a separate version. A movie can span several clips, so Dupearr counts them as one copy and never removes a clip on its own.',
};

export const DISC_ORIGIN_LABELS: Record<DiscOrigin, string> = {
  filesystem: 'Not in Plex',
  plex: 'Custom Plex scanner',
};

export const DISC_ORIGIN_DESCRIPTIONS: Record<DiscOrigin, string> = {
  filesystem:
    'Found on disk next to this title. Plex’s default scanners skip disc folders and images, so Plex does not list or play it.',
  plex: 'A custom Plex scanner shows this disc’s files as the parts of one version. Plex cannot delete a disc safely.',
};

// ---------------------------------------------------------------------------
// Duplicate groups
// ---------------------------------------------------------------------------

export const GROUP_STATUSES: readonly GroupStatus[] = [
  'pending',
  'review',
  'deferred',
  'protected',
  'queued',
  'resolved',
  'ignored',
  'failed',
];

export const GROUP_STATUS_LABELS: Record<GroupStatus, string> = {
  pending: 'Pending',
  review: 'Needs Review',
  deferred: 'Deferred',
  protected: 'Protected',
  queued: 'Queued',
  resolved: 'Resolved',
  ignored: 'Ignored',
  failed: 'Failed',
};

export const GROUP_STATUS_DESCRIPTIONS: Record<GroupStatus, string> = {
  pending: 'Removals proposed; waiting for approval',
  review: 'Flagged (suspect match or conflicting data); never auto-approved',
  deferred: 'Removals blocked temporarily (minimum age, *arr download/import or playback); re-evaluated next scan',
  protected: 'Every removal candidate is protected by a rule; nothing to do',
  queued: 'Approved; removal actions queued or running',
  resolved: 'Removals done, or the duplicate disappeared on a later scan',
  ignored: 'Ignored by you; kept across scans until un-ignored',
  failed: 'Last execution failed; see actions and history',
};

export const GROUP_STATUS_KIND: Record<GroupStatus, StatusKind> = {
  pending: 'warning',
  review: 'pink',
  deferred: 'info',
  protected: 'primary',
  queued: 'accent',
  resolved: 'success',
  ignored: 'default',
  failed: 'danger',
};

/** Statuses that still need attention (default Duplicates filter). */
export const OPEN_GROUP_STATUSES: readonly GroupStatus[] = ['pending', 'review', 'deferred', 'queued', 'failed'];

export const GROUP_FLAG_LABELS: Record<GroupFlag, string> = {
  cross_library: 'Cross-library',
  duration_mismatch: 'Duration mismatch',
  multi_episode: 'Multi-episode file',
  stacked: 'Stacked (multi-part)',
  hardlinked: 'Hardlinked',
  min_age: 'Too new',
  missing_keeper_file: 'Keeper file missing',
  edition_split: 'Edition split',
  arr_untracked_keeper: 'Keeper not tracked by *arr',
  unanalyzed: 'Unanalyzed file',
  unavailable_version: 'Unavailable version',
  suspect_merge: 'Suspect match',
  variant_3d: '3D variant',
  language_variant: 'Language variant',
  intentional_arr_instances: 'Different *arr instances',
  arr_queue_busy: '*arr busy',
  arr_cutoff_unmet: 'Cutoff not met',
  playing: 'Playing',
  sample: 'Sample / truncated',
  same_file: 'Same file',
  full_disc: 'Full disc',
  disc_unreadable: 'Disc unreadable',
  disc_tracked_clip: '*arr tracks a disc clip',
  watch_unreadable: 'Play history unreadable',
};

export const GROUP_FLAG_DESCRIPTIONS: Record<GroupFlag, string> = {
  cross_library: 'Copies live in different libraries of the same scope group',
  duration_mismatch: 'Versions differ in runtime beyond the tolerance — may not be the same content',
  multi_episode: "A version's file is shared by several episodes",
  stacked: 'A version is split into several part files (cd1/cd2…)',
  hardlinked: 'A removed file is hardlinked; removal may not free space',
  min_age: 'A file is younger than the minimum age; removal is deferred',
  missing_keeper_file: 'Plex reports a kept version as not accessible — needs review',
  edition_split: 'Editions are treated as distinct; this group is one edition',
  arr_untracked_keeper: 'The keeper is not tracked by the *arr that tracks a removed copy',
  unanalyzed: 'Plex has not analyzed a version (no codec/resolution/bitrate) — needs review',
  unavailable_version: 'Plex reports a version’s file as missing; it is not counted as a copy',
  suspect_merge: 'Versions may be different titles merged by Plex (folders/years/durations/*arr ids differ) — never auto-removed',
  variant_3d: '2D and 3D versions are treated as distinct',
  language_variant: 'Versions have different audio languages and are treated as distinct',
  intentional_arr_instances: 'Versions are managed by different *arr instances (e.g. 4K + 1080p setup) — treated as intentional',
  arr_queue_busy: 'The *arr has this title in its download/import queue; removal is deferred',
  arr_cutoff_unmet: 'The keeper is below the *arr quality cutoff — the *arr may upgrade again and recreate the duplicate',
  playing: 'A version is currently playing; removal is deferred',
  sample: 'A version looks like a sample or truncated file (short duration)',
  same_file: 'Two versions may be the same file (same path or inode, or same name and size in different folders) — needs review',
  full_disc:
    'A copy is a full-disc backup (BDMV / VIDEO_TS / ISO), or the clips of a disc stored loose in the movie folder (00800.m2ts … / VTS_01_1.VOB …): hundreds of files that make up one copy. Discs are protected unless removing them is allowed in Settings → Media Management, and are then only removed as a whole, by manual approval, into the recycle bin — never clip by clip',
  disc_unreadable:
    'The main feature of a disc could not be read (resolution, HDR, audio and duration unknown) — needs review',
  disc_tracked_clip:
    'Radarr/Sonarr tracks a single file of a disc (e.g. BDMV/STREAM/00800.m2ts or a loose 00800.m2ts). Files of a disc are never removed one by one — that would break the disc',
  watch_unreadable:
    'The profile ranks by play history (Played / Last played), but Tautulli could not be read during the last scan: an unreadable history counts as unknown, never as "not played" — needs review',
};

export const GROUP_FLAG_KIND: Record<GroupFlag, StatusKind> = {
  cross_library: 'info',
  duration_mismatch: 'danger',
  multi_episode: 'warning',
  stacked: 'default',
  hardlinked: 'warning',
  min_age: 'info',
  missing_keeper_file: 'danger',
  edition_split: 'default',
  arr_untracked_keeper: 'warning',
  unanalyzed: 'warning',
  unavailable_version: 'warning',
  suspect_merge: 'danger',
  variant_3d: 'default',
  language_variant: 'default',
  intentional_arr_instances: 'info',
  arr_queue_busy: 'info',
  arr_cutoff_unmet: 'warning',
  playing: 'info',
  sample: 'warning',
  same_file: 'danger',
  full_disc: 'primary',
  disc_unreadable: 'warning',
  disc_tracked_clip: 'warning',
  watch_unreadable: 'warning',
};

export const DECISION_LABELS: Record<Decision, string> = {
  keep: 'Keep',
  remove: 'Remove',
};

export const DECISION_KIND: Record<Decision, StatusKind> = {
  keep: 'success',
  remove: 'danger',
};

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

export const CRITERION_TYPE_LABELS: Record<CriterionType, string> = {
  resolution: 'Resolution',
  dynamic_range: 'Dynamic Range (HDR)',
  source: 'Source',
  video_codec: 'Video Codec',
  audio_format: 'Audio Format',
  container: 'Container',
  library: 'Library',
  audio_channels: 'Audio Channels',
  video_bitrate: 'Video Bitrate',
  file_size: 'File Size',
  bit_depth: 'Bit Depth',
  custom_format_score: 'Custom Format Score',
  date_added: 'Date Added',
  audio_track_count: 'Audio Track Count',
  subtitle_track_count: 'Subtitle Track Count',
  arr_managed: 'Managed by *arr',
  audio_language: 'Audio Language',
  filename_score: 'Filename Score',
  health: 'File Health',
  played: 'Played',
  last_played: 'Last Played',
};

export const CRITERION_KIND_BY_TYPE: Record<CriterionType, CriterionKind> = {
  resolution: 'ordered',
  dynamic_range: 'ordered',
  source: 'ordered',
  video_codec: 'ordered',
  audio_format: 'ordered',
  container: 'ordered',
  library: 'ordered',
  audio_channels: 'numeric',
  video_bitrate: 'numeric',
  file_size: 'numeric',
  bit_depth: 'numeric',
  custom_format_score: 'numeric',
  date_added: 'numeric',
  audio_track_count: 'numeric',
  subtitle_track_count: 'numeric',
  arr_managed: 'boolean',
  audio_language: 'boolean',
  filename_score: 'patterns',
  health: 'boolean',
  played: 'boolean',
  last_played: 'numeric',
};

export const DIRECTION_LABELS: Record<Direction, string> = {
  higher: 'Higher is better',
  lower: 'Lower is better',
};

export const PROTECTION_TYPE_LABELS: Record<ProtectionType, string> = {
  path_glob: 'Path matches',
  library: 'Library',
  arr_instance: '*arr instance',
  arr_tag: '*arr tag',
};

/** "" = no partitioning (keep the best `keepCount` overall). */
export const KEEP_PER_LABELS: Record<KeepPer, string> = {
  '': 'Nothing (keep the best overall)',
  resolution: 'Each resolution',
  dynamic_range: 'Each dynamic range',
};

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

export const MEDIA_SERVER_KIND_LABELS: Record<MediaServerKind, string> = {
  plex: 'Plex',
};

export const ARR_KIND_LABELS: Record<ArrKind, string> = {
  radarr: 'Radarr',
  sonarr: 'Sonarr',
};

export const PATH_SOURCE_LABELS: Record<PathSourceType, string> = {
  server: 'Media Server',
  arr: 'Application (*arr)',
};

export const EXCLUSION_KIND_LABELS: Record<ExclusionKind, string> = {
  group_key: 'Duplicate group',
  path_prefix: 'Path prefix',
  library: 'Library',
  title_regex: 'Title (regex)',
};

// ---------------------------------------------------------------------------
// Actions / history / scans / commands
// ---------------------------------------------------------------------------

export const ACTION_STATUS_LABELS: Record<ActionStatus, string> = {
  pending: 'Pending',
  running: 'Running',
  succeeded: 'Deleted',
  dry_run: 'Dry Run',
  skipped: 'Skipped',
  failed: 'Failed',
  cancelled: 'Cancelled',
};

export const ACTION_STATUS_KIND: Record<ActionStatus, StatusKind> = {
  pending: 'default',
  running: 'accent',
  succeeded: 'success',
  dry_run: 'info',
  skipped: 'warning',
  failed: 'danger',
  cancelled: 'default',
};

export const DELETION_METHODS: readonly DeletionMethod[] = ['arr', 'plex', 'filesystem'];

export const DELETION_METHOD_LABELS: Record<DeletionMethod, string> = {
  arr: 'Radarr / Sonarr',
  plex: 'Plex',
  filesystem: 'Filesystem',
};

export const DELETION_METHOD_DESCRIPTIONS: Record<DeletionMethod, string> = {
  arr: "Delete through the owning *arr (uses the *arr's recycle bin when configured)",
  plex: 'Delete through Plex (requires "Allow media deletion" in Plex)',
  filesystem: 'Delete or move to the recycle bin directly on disk (requires path mappings)',
};

export const HISTORY_EVENT_LABELS: Record<HistoryEventType, string> = {
  scanCompleted: 'Scan Completed',
  scanFailed: 'Scan Failed',
  groupDetected: 'Duplicate Detected',
  groupApproved: 'Approved',
  groupIgnored: 'Ignored',
  groupUnignored: 'Unignored',
  groupResolved: 'Resolved',
  fileDeleted: 'File Deleted',
  fileDeleteDryRun: 'Dry Run Delete',
  fileDeleteFailed: 'Delete Failed',
  fileSkipped: 'File Skipped',
  fileRestored: 'File Restored',
  overrideChanged: 'Override Changed',
  security: 'Security',
};

export const HISTORY_EVENT_KIND: Record<HistoryEventType, StatusKind> = {
  scanCompleted: 'success',
  scanFailed: 'danger',
  groupDetected: 'warning',
  groupApproved: 'accent',
  groupIgnored: 'default',
  groupUnignored: 'default',
  groupResolved: 'success',
  fileDeleted: 'danger',
  fileDeleteDryRun: 'info',
  fileDeleteFailed: 'danger',
  fileSkipped: 'warning',
  fileRestored: 'primary',
  overrideChanged: 'pink',
  security: 'warning',
};

export const SCAN_STATUS_LABELS: Record<ScanStatus, string> = {
  running: 'Running',
  completed: 'Completed',
  failed: 'Failed',
};

export const SCAN_STATUS_KIND: Record<ScanStatus, StatusKind> = {
  running: 'accent',
  completed: 'success',
  failed: 'danger',
};

export const COMMAND_STATUS_LABELS: Record<CommandStatus, string> = {
  queued: 'Queued',
  started: 'Running',
  completed: 'Completed',
  failed: 'Failed',
  aborted: 'Aborted',
};

export const COMMAND_STATUS_KIND: Record<CommandStatus, StatusKind> = {
  queued: 'default',
  started: 'accent',
  completed: 'success',
  failed: 'danger',
  aborted: 'warning',
};

export const COMMAND_NAME_LABELS: Record<CommandName, string> = {
  DuplicateScan: 'Duplicate Scan',
  TargetedScan: 'Targeted Scan',
  ProcessQueue: 'Process Queue',
  SyncLibraries: 'Sync Libraries',
  CheckHealth: 'Check Health',
  Backup: 'Backup',
  Housekeeping: 'Housekeeping',
  CleanRecycleBin: 'Clean Recycle Bin',
};

export const TRIGGER_LABELS: Record<CommandTrigger, string> = {
  manual: 'Manual',
  scheduled: 'Scheduled',
  webhook: 'Webhook',
};

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

export const NOTIFICATION_TRIGGER_LABELS: Record<NotificationTrigger, string> = {
  onDuplicatesFound: 'On Duplicates Found',
  onFileDeleted: 'On File Deleted',
  onDeleteFailed: 'On Delete Failed',
  onScanCompleted: 'On Scan Completed',
  onHealthIssue: 'On Health Issue',
  onHealthRestored: 'On Health Restored',
};

export const NOTIFICATION_KIND_LABELS: Record<NotificationKind, string> = {
  discord: 'Discord',
  slack: 'Slack',
  telegram: 'Telegram',
  pushover: 'Pushover',
  gotify: 'Gotify',
  ntfy: 'ntfy',
  apprise: 'Apprise',
  webhook: 'Webhook',
  email: 'Email',
};

// ---------------------------------------------------------------------------
// Health / system / settings
// ---------------------------------------------------------------------------

export const HEALTH_TYPE_LABELS: Record<HealthType, string> = {
  ok: 'OK',
  notice: 'Notice',
  warning: 'Warning',
  error: 'Error',
};

export const HEALTH_TYPE_KIND: Record<HealthType, StatusKind> = {
  ok: 'success',
  notice: 'info',
  warning: 'warning',
  error: 'danger',
};

/** Higher = more severe. */
export const HEALTH_SEVERITY: Record<HealthType, number> = {
  ok: 0,
  notice: 1,
  warning: 2,
  error: 3,
};

export const MODE_LABELS: Record<Mode, string> = {
  manual: 'Manual (approve each group)',
  auto: 'Automatic (approve pending groups after each scan)',
};

export const AUTH_METHOD_LABELS: Record<AuthenticationMethod, string> = {
  None: 'None',
  Forms: 'Forms (Login Page)',
  External: 'External (reverse proxy)',
};

export const AUTH_REQUIRED_LABELS: Record<AuthenticationRequired, string> = {
  Enabled: 'Enabled',
  DisabledForLocalAddresses: 'Disabled for Local Addresses',
};

export const LOG_LEVEL_LABELS: Record<LogLevel, string> = {
  trace: 'Trace',
  debug: 'Debug',
  info: 'Info',
  warn: 'Warn',
  error: 'Error',
};

export const LOG_LEVEL_KIND: Record<LogLevel, StatusKind> = {
  trace: 'default',
  debug: 'default',
  info: 'info',
  warn: 'warning',
  error: 'danger',
};

export const BACKUP_TYPE_LABELS: Record<BackupType, string> = {
  scheduled: 'Scheduled',
  manual: 'Manual',
  update: 'Before update',
};

/** Masked secret value the API returns (send it back unchanged to keep the stored value). */
export const MASKED_SECRET = '********';

export const DEFAULT_PAGE_SIZE = 20;
export const PAGE_SIZE_OPTIONS: readonly number[] = [20, 50, 100, 250];
