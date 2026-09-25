/**
 * Pure helpers shared by the Duplicates list (`/`) and detail (`/duplicate/:id`) pages:
 * titles, status eligibility, bulk summaries, deletion caveats, external links and small
 * MediaVersion helpers. No React here (unit-tested in duplicateUtils.test.ts).
 */
import type {
  AudioTrack,
  BulkDuplicateAction,
  DeletionMethod,
  DuplicateGroupSummary,
  DuplicateGroupSummaryFile,
  GroupFile,
  GroupFlag,
  GroupStatus,
  Id,
  MediaPart,
  MediaType,
  MediaVersion,
  SubtitleTrack,
} from '@/api/types';
import {
  AUDIO_FORMAT_ORDER,
  DELETION_METHOD_LABELS,
  DELETION_METHODS,
  DYNAMIC_RANGE_SHORT_LABELS,
  RESOLUTION_SHORT_LABELS,
  labelOf,
} from '@/lib/constants';
import { discTypeShortLabel, episodeCode } from '@/lib/format';
import { removesDisc, removesDiscClip } from './disc';

// ---------------------------------------------------------------------------
// Titles
// ---------------------------------------------------------------------------

export interface TitledItem {
  mediaType: MediaType;
  title: string;
  year?: number;
  showTitle?: string;
  season?: number;
  episode?: number;
}

/** Movie "Heat (1995)"; episode "Show – S01E02 – Title" (missing parts are skipped). */
export function groupTitle(item: TitledItem): string {
  if (item.mediaType === 'episode') {
    const code = episodeCode(item.season, item.episode);
    const parts = [item.showTitle, code, item.title].filter((p): p is string => !!p && p.trim() !== '');
    return parts.length > 0 ? parts.join(' – ') : 'Untitled episode';
  }
  const title = item.title?.trim() || 'Untitled';
  return item.year && item.year > 0 ? `${title} (${item.year})` : title;
}

// ---------------------------------------------------------------------------
// Status eligibility (the server stays authoritative; these only drive the UI)
// ---------------------------------------------------------------------------

/** Statuses a group can be approved from on its detail page (review needs a deliberate, per-group approval). */
export const APPROVABLE_STATUSES: ReadonlySet<GroupStatus> = new Set<GroupStatus>(['pending', 'review', 'failed']);

/**
 * Statuses that can be approved from the LIST (bulk or row action). `review` groups are
 * deliberately excluded: they are suspect matches (possibly different titles merged by Plex) and
 * must be approved one by one from the detail page, after comparing the copies.
 */
export const BULK_APPROVABLE_STATUSES: ReadonlySet<GroupStatus> = new Set<GroupStatus>(['pending', 'failed']);

/** Statuses a group can be ignored from. */
export const IGNORABLE_STATUSES: ReadonlySet<GroupStatus> = new Set<GroupStatus>([
  'pending',
  'review',
  'deferred',
  'protected',
  'failed',
]);

/** Statuses in which per-file overrides are locked (removals already queued or done). */
export const OVERRIDE_LOCKED_STATUSES: ReadonlySet<GroupStatus> = new Set<GroupStatus>(['queued', 'resolved']);

/** Approve from the detail page, where every copy can be compared first. */
export function canApprove(status: GroupStatus, removeCount: number): boolean {
  return APPROVABLE_STATUSES.has(status) && removeCount > 0;
}

/**
 * docs/DECISIONS.md D12: a group with a copy on a read-only media server (Jellyfin) is flagged
 * `manual_only` — it is approved one at a time from its detail page, never from the list (the
 * server refuses such a group in a bulk approval with 409).
 */
export function isManualOnly(flags: readonly GroupFlag[] | null | undefined): boolean {
  return (flags ?? []).includes('manual_only');
}

/**
 * Approve from the list (row action or bulk). `review` groups are excluded: the list shows no
 * paths or comparison, so suspect matches must be approved from their detail page. So are groups
 * that remove a full disc (`files`, when given): a disc removal is only approved from the detail
 * page, where the disc folder that moves to the recycle bin is shown — groups that would remove a
 * single file of a disc (a loose `00174.m2ts` clip, reported by the server as `discClip`), which is
 * never approved at all — and manual-only groups (`flags`, when given; see {@link isManualOnly}).
 */
export function canApproveFromList(
  status: GroupStatus,
  removeCount: number,
  files?: readonly Pick<DuplicateGroupSummaryFile, 'decision' | 'disc' | 'discClip'>[] | null,
  flags?: readonly GroupFlag[] | null,
): boolean {
  return (
    BULK_APPROVABLE_STATUSES.has(status) &&
    removeCount > 0 &&
    !removesDisc(files) &&
    !removesDiscClip(files) &&
    !isManualOnly(flags)
  );
}

export function canIgnore(status: GroupStatus): boolean {
  return IGNORABLE_STATUSES.has(status);
}

export function canUnignore(status: GroupStatus): boolean {
  return status === 'ignored';
}

// ---------------------------------------------------------------------------
// Bulk actions
// ---------------------------------------------------------------------------

/** Bulk approvals of more than this many groups need the word below typed (when not a dry run). */
export const BULK_TYPED_CONFIRM_THRESHOLD = 10;
export const TYPED_CONFIRM_WORD = 'DELETE';

export interface BulkSummary {
  action: BulkDuplicateAction;
  /** Groups the action will be sent for. */
  eligible: DuplicateGroupSummary[];
  /** Selected groups the action does not apply to (never sent). */
  skipped: DuplicateGroupSummary[];
  /** Of `skipped`: groups in `review` (approve only). */
  reviewSkipped: number;
  /** Of `skipped`: otherwise approvable groups that remove a full disc (approve only). */
  discSkipped: number;
  /**
   * Of `skipped`: otherwise approvable groups that would remove a single file of a disc — a loose
   * clip such as `00174.m2ts` (approve only). Never approvable: they need a re-scan.
   */
  clipSkipped: number;
  /**
   * Of `skipped`: otherwise approvable groups with a copy on a read-only media server (Jellyfin),
   * approved one at a time from their detail page (approve only).
   */
  manualSkipped: number;
  /** Files that will be removed (approve) / files in the eligible groups (ignore/unignore). */
  files: number;
  /** Reclaimable bytes of the eligible groups. */
  bytes: number;
}

function isEligible(action: BulkDuplicateAction, g: DuplicateGroupSummary): boolean {
  switch (action) {
    case 'approve':
      return canApproveFromList(g.status, g.removeCount, g.files, g.flags);
    case 'ignore':
      return canIgnore(g.status);
    case 'unignore':
      return canUnignore(g.status);
    default:
      return false;
  }
}

/**
 * Splits a list selection (or one row) into the groups an action applies to and the ones it
 * skips. Approvals never include `review` groups or groups that remove a full disc (see
 * {@link canApproveFromList}).
 */
export function summarizeBulk(action: BulkDuplicateAction, groups: readonly DuplicateGroupSummary[]): BulkSummary {
  const eligible: DuplicateGroupSummary[] = [];
  const skipped: DuplicateGroupSummary[] = [];
  for (const g of groups) (isEligible(action, g) ? eligible : skipped).push(g);
  const reviewSkipped = action === 'approve' ? skipped.filter((g) => g.status === 'review').length : 0;
  const otherwiseApprovable = (g: DuplicateGroupSummary) => canApproveFromList(g.status, g.removeCount);
  const clipSkipped =
    action === 'approve' ? skipped.filter((g) => otherwiseApprovable(g) && removesDiscClip(g.files)).length : 0;
  const discSkipped =
    action === 'approve'
      ? skipped.filter((g) => otherwiseApprovable(g) && removesDisc(g.files) && !removesDiscClip(g.files)).length
      : 0;
  const manualSkipped =
    action === 'approve'
      ? skipped.filter((g) => canApproveFromList(g.status, g.removeCount, g.files) && isManualOnly(g.flags)).length
      : 0;
  const files = eligible.reduce(
    (sum, g) => sum + Math.max(0, action === 'approve' ? g.removeCount || 0 : g.fileCount || 0),
    0,
  );
  const bytes = eligible.reduce((sum, g) => sum + Math.max(0, g.reclaimableBytes || 0), 0);
  return { action, eligible, skipped, reviewSkipped, discSkipped, clipSkipped, manualSkipped, files, bytes };
}

/**
 * Identity of what a list approval confirms: every group's status, server signature, counts,
 * bytes and per-file decisions. Used by {@link useChangeGuard} to block a confirmation whose
 * content changed while the dialog was open (a changed signature would make the server refuse the
 * approval with 409, so it is surfaced before the user confirms).
 */
export function summaryFingerprint(groups: readonly DuplicateGroupSummary[]): string {
  return groups
    .map((g) =>
      [
        g.id,
        g.status,
        g.signature ?? '',
        g.removeCount,
        g.keepCount,
        g.reclaimableBytes,
        (g.files ?? []).map((f) => `${f.id}:${f.decision}${f.disc ? ':disc' : ''}${f.discClip ? ':clip' : ''}`).join(','),
      ].join('/'),
    )
    .join('|');
}

/**
 * Group id → server signature of each group, as sent with a bulk approval
 * (`POST /duplicate/bulk` `signatures`). Groups without a signature (an older server) are left
 * out: the server then approves them without the check.
 */
export function approvalSignatures(groups: readonly DuplicateGroupSummary[]): Record<Id, string> {
  const out: Record<Id, string> = {};
  for (const g of groups) if (g.signature) out[g.id] = g.signature;
  return out;
}

/**
 * Identity of what a detail-page approval confirms: status, and for every copy its decision,
 * protection, paths, size and — for a full disc — its root and file count (see
 * {@link summaryFingerprint}).
 */
export function filesFingerprint(status: GroupStatus, files: readonly GroupFile[]): string {
  return [
    status,
    ...files.map((f) => {
      const disc = f.version?.disc;
      return [
        f.id,
        f.decision,
        f.protected ? 'p' : '',
        versionSize(f.version),
        versionPaths(f.version).join(';'),
        disc ? `disc:${disc.type}:${disc.root}:${disc.localRoot}:${disc.fileCount}:${disc.discs}:${disc.clipCount ?? ''}` : '',
      ].join('/');
    }),
  ].join('|');
}

/** True when the user must type {@link TYPED_CONFIRM_WORD} before a bulk approval. */
export function requiresTypedConfirmation(action: BulkDuplicateAction, eligibleCount: number, dryRun: boolean): boolean {
  return action === 'approve' && !dryRun && eligibleCount > BULK_TYPED_CONFIRM_THRESHOLD;
}

// ---------------------------------------------------------------------------
// Deletion caveats
// ---------------------------------------------------------------------------

/** "Radarr / Sonarr → Plex → Filesystem" (configured order, falling back to the default order). */
export function deletionOrderText(methods: readonly DeletionMethod[] | null | undefined): string {
  const list = methods && methods.length > 0 ? methods : DELETION_METHODS;
  return list.map((m) => labelOf(DELETION_METHOD_LABELS, m)).join(' → ');
}

/** The caveat shown in every approval confirmation. */
export function deletionCaveat(methods: readonly DeletionMethod[] | null | undefined): string {
  return `Deletion method is chosen at execution (order: ${deletionOrderText(methods)}); deletions through Plex or without a recycle bin are permanent.`;
}

/**
 * Removals approved while dry run is on are stored as dry-run actions and stay simulated even if dry
 * run is turned off before the queue runs (the executor checks `settings.dryRun || action.dryRun`);
 * the group returns to pending afterwards, so the user approves again to remove files for real.
 */
export const DRY_RUN_NOTE =
  'Dry run is enabled — these removals are only evaluated and recorded as dry-run actions, even if dry run is turned off before they run. Approve again with dry run off to delete files.';

// ---------------------------------------------------------------------------
// External ids
// ---------------------------------------------------------------------------

export interface ExternalLink {
  key: 'imdb' | 'tmdb' | 'tvdb';
  label: string;
  id: string;
  /** Absent when the id is malformed or has no public page for this media type. */
  url?: string;
}

const EXTERNAL_ORDER: ExternalLink['key'][] = ['imdb', 'tmdb', 'tvdb'];
const EXTERNAL_LABELS: Record<ExternalLink['key'], string> = { imdb: 'IMDb', tmdb: 'TMDb', tvdb: 'TVDB' };

/**
 * Links for the imdb/tmdb/tvdb ids of a group. Ids are validated (never interpolated raw into a
 * URL); the Plex GUID ("plex") is not a public web page and is skipped.
 */
export function externalIdLinks(ids: Record<string, string> | null | undefined, mediaType: MediaType): ExternalLink[] {
  if (!ids) return [];
  const out: ExternalLink[] = [];
  for (const key of EXTERNAL_ORDER) {
    const raw = ids[key];
    if (typeof raw !== 'string') continue;
    const id = raw.trim();
    if (!id) continue;
    const link: ExternalLink = { key, label: EXTERNAL_LABELS[key], id };
    if (key === 'imdb' && /^tt\d{5,12}$/.test(id)) {
      link.url = `https://www.imdb.com/title/${id}/`;
    } else if (key === 'tmdb' && /^\d{1,12}$/.test(id) && mediaType === 'movie') {
      link.url = `https://www.themoviedb.org/movie/${id}`;
    } else if (key === 'tvdb' && /^\d{1,12}$/.test(id)) {
      link.url = `https://www.thetvdb.com/dereferrer/${mediaType === 'episode' ? 'episode' : 'movie'}/${id}`;
    }
    out.push(link);
  }
  return out;
}

// ---------------------------------------------------------------------------
// MediaVersion helpers
// ---------------------------------------------------------------------------

function parts(v: MediaVersion | null | undefined): MediaPart[] {
  return Array.isArray(v?.parts) ? v.parts : [];
}

/**
 * Size of a version (bytes): the sum of the part sizes, or — for a full disc that was measured — every
 * file of the disc (the server's MediaVersion.TotalSize: a custom Plex scanner lists only the disc's
 * clips as parts).
 */
export function versionSize(v: MediaVersion | null | undefined): number {
  const total = v?.disc?.totalBytes;
  if (typeof total === 'number' && Number.isFinite(total) && total > 0) return total;
  return parts(v).reduce((sum, p) => sum + (Number.isFinite(p.size) && p.size > 0 ? p.size : 0), 0);
}

/** True when any part (or any file of a disc) is hardlinked (removal won't free its space). */
export function isHardlinked(v: MediaVersion | null | undefined): boolean {
  if ((v?.disc?.hardlinkedFiles ?? 0) > 0) return true;
  return parts(v).some((p) => (p.linkCount ?? 0) > 1);
}

/** Highest hardlink count of the parts (0 = unknown). */
export function maxLinkCount(v: MediaVersion | null | undefined): number {
  return parts(v).reduce((max, p) => Math.max(max, p.linkCount ?? 0), 0);
}

/**
 * True when any part is missing (`exists === false`; absent = unknown): Plex reports it, and for a
 * Jellyfin copy Dupearr found its mapped file gone from disk (Jellyfin never reports it).
 */
export function hasMissingPart(v: MediaVersion | null | undefined): boolean {
  return parts(v).some((p) => p.exists === false);
}

/**
 * docs/DECISIONS.md D12: a copy listed by a Jellyfin server ("jellyfin:<serverId>:<sourceId>").
 * Dupearr only reads from Jellyfin: such a copy is removed through Radarr/Sonarr or into Dupearr's
 * recycle bin, only into a recycle bin, and never through Jellyfin.
 */
export function isJellyfinVersion(v: Pick<MediaVersion, 'key'> | null | undefined): boolean {
  return (v?.key ?? '').startsWith('jellyfin:');
}

/** True when any copy of the group is listed by a Jellyfin server. */
export function hasJellyfinCopy(files: readonly Pick<GroupFile, 'version'>[] | null | undefined): boolean {
  return (files ?? []).some((f) => isJellyfinVersion(f.version));
}

/**
 * Why the group is only reported, never acted on: the report-only reasons of its copies (a .strm
 * shortcut, unreadable stack parts, an unmapped copy …), each once.
 */
export function reportOnlyReasons(files: readonly Pick<GroupFile, 'version'>[] | null | undefined): string[] {
  return [...new Set((files ?? []).flatMap((f) => f.version?.reportOnly ?? []))];
}

/** Paths of the parts (server-side). */
export function versionPaths(v: MediaVersion | null | undefined): string[] {
  return parts(v)
    .map((p) => p.path)
    .filter((p): p is string => typeof p === 'string' && p !== '');
}

function audioRank(format: string): number {
  const idx = (AUDIO_FORMAT_ORDER as readonly string[]).indexOf(format);
  return idx === -1 ? AUDIO_FORMAT_ORDER.length : idx;
}

/** Best audio track: best format in the default order, then most channels. */
export function bestAudioTrack(tracks: readonly AudioTrack[] | null | undefined): AudioTrack | null {
  if (!tracks || tracks.length === 0) return null;
  let best: AudioTrack | null = null;
  for (const t of tracks) {
    if (!t) continue;
    if (
      !best ||
      audioRank(t.format) < audioRank(best.format) ||
      (audioRank(t.format) === audioRank(best.format) && (t.channels ?? 0) > (best.channels ?? 0))
    ) {
      best = t;
    }
  }
  return best;
}

/** Unique, display-ready subtitle languages ("English", "French", …; "Unknown" for blanks). */
export function subtitleLanguages(tracks: readonly SubtitleTrack[] | null | undefined): string[] {
  if (!tracks) return [];
  const seen = new Set<string>();
  for (const t of tracks) {
    const lang = (t?.language || t?.languageCode || 'Unknown').trim() || 'Unknown';
    seen.add(lang);
  }
  return [...seen];
}

/**
 * Short chip text for a summary file: "4K DV HDR10", "1080p"; a full disc leads with its kind:
 * "UHD BD · 4K DV HDR10", "ISO" (an ISO's quality is unknown).
 */
export function summaryFileLabel(
  f: Pick<DuplicateGroupSummaryFile, 'resolution' | 'dynamicRange'> & Partial<Pick<DuplicateGroupSummaryFile, 'disc'>>,
): string {
  const res = f.resolution ? labelOf(RESOLUTION_SHORT_LABELS, f.resolution) : '';
  const hdr = f.dynamicRange && f.dynamicRange !== 'sdr' ? labelOf(DYNAMIC_RANGE_SHORT_LABELS, f.dynamicRange) : '';
  if (f.disc) {
    const quality = res ? [res, hdr].filter(Boolean).join(' ') : '';
    const kind = discTypeShortLabel(f.disc.type);
    return quality ? `${kind} · ${quality}` : kind;
  }
  return hdr ? `${res || '?'} ${hdr}` : res || '?';
}

/** Files ordered by rank (1 = best), stable by id for equal/missing ranks. */
export function sortByRank(files: readonly GroupFile[] | null | undefined): GroupFile[] {
  if (!files) return [];
  const rank = (f: GroupFile) => (Number.isFinite(f.rank) && f.rank > 0 ? f.rank : Number.MAX_SAFE_INTEGER);
  return [...files].sort((a, b) => rank(a) - rank(b) || a.id - b.id);
}
