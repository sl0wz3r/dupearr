/**
 * "Best value" detection for the comparison table on the duplicate detail page.
 *
 * The engine is the authority on decisions (rank, reasons, decidingCriterion). This module only
 * decides which cell of a row to highlight, mirroring the profile's criterion configuration
 * (order / direction) when the profile is known and the documented defaults otherwise
 * (docs/ARCHITECTURE.md §4.3, docs/DECISIONS.md D5). Missing values never win; an unknown play
 * history (D10) highlights nothing, since the engine treats it as a tie.
 */
import type { Criterion, CriterionType, Direction, GroupFile, MediaVersion, Profile } from '@/api/types';
import { AUDIO_FORMAT_ORDER, RESOLUTION_ORDER, SOURCE_ORDER, VIDEO_CODEC_ORDER } from '@/lib/constants';
import { toDate } from '@/lib/format';
import { hasMissingPart, versionSize } from './duplicateUtils';

/** Higher is better; null = unknown (never best). */
export type Score = number | null;

const EMPTY: ReadonlySet<number> = new Set<number>();

/**
 * Indices of the best scores. Empty when fewer than two files, nothing is known, or every file
 * shares the best value (no distinction worth highlighting).
 */
export function bestIndices(scores: readonly Score[]): ReadonlySet<number> {
  if (scores.length < 2) return EMPTY;
  let max = -Infinity;
  for (const s of scores) if (s !== null && Number.isFinite(s) && s > max) max = s;
  if (max === -Infinity) return EMPTY;
  const best = new Set<number>();
  scores.forEach((s, i) => {
    if (s === max) best.add(i);
  });
  return best.size === scores.length ? EMPTY : best;
}

/**
 * Engine default orders (docs/DECISIONS.md D5, internal/engine/criteria.go). Dynamic range and
 * container are spelled out here because they differ from the display orders in lib/constants
 * (HLG ranks above plain Dolby Vision; unknown containers rank above AVI/TS, a standalone M2TS
 * right before them). The source order ranks a full disc right after a remux.
 */
export const DEFAULT_DYNAMIC_RANGE_ORDER: readonly string[] = ['dv_hdr10', 'hdr10plus', 'hdr10', 'hlg', 'dv', 'sdr'];
export const DEFAULT_CONTAINER_ORDER: readonly string[] = ['mkv', 'mp4', 'm4v', 'm2ts', 'other', 'avi', 'ts'];

const DEFAULT_ORDERS: Partial<Record<CriterionType, readonly string[]>> = {
  resolution: RESOLUTION_ORDER,
  dynamic_range: DEFAULT_DYNAMIC_RANGE_ORDER,
  source: SOURCE_ORDER,
  video_codec: VIDEO_CODEC_ORDER,
  container: DEFAULT_CONTAINER_ORDER,
  audio_format: AUDIO_FORMAT_ORDER,
};

/** Profile criterion of a type (first match), if any. */
export function profileCriterion(profile: Profile | null | undefined, type: CriterionType): Criterion | undefined {
  return profile?.criteria?.find((c) => c?.type === type);
}

function orderFor(type: CriterionType, criterion: Criterion | undefined): readonly string[] {
  if (criterion?.order && criterion.order.length > 0) return criterion.order;
  return DEFAULT_ORDERS[type] ?? [];
}

function orderedScore(value: string | null | undefined, order: readonly string[]): Score {
  if (!value) return null;
  const idx = order.indexOf(value);
  return idx === -1 ? null : order.length - idx;
}

function directional(value: number | null | undefined, direction: Direction): Score {
  if (value === null || value === undefined || !Number.isFinite(value)) return null;
  return direction === 'lower' ? -value : value;
}

function positive(n: number | null | undefined): number | null {
  return n !== null && n !== undefined && Number.isFinite(n) && n > 0 ? n : null;
}

/** Best audio track position under `order` (lower index = better) → score. */
function bestAudioScore(v: MediaVersion, order: readonly string[]): Score {
  const tracks = v.audioTracks ?? [];
  let best: Score = null;
  for (const t of tracks) {
    const s = orderedScore(t?.format, order);
    if (s !== null && (best === null || s > best)) best = s;
  }
  return best;
}

function maxChannels(v: MediaVersion): number | null {
  const tracks = v.audioTracks ?? [];
  let max = 0;
  for (const t of tracks) max = Math.max(max, t?.channels ?? 0);
  return max > 0 ? max : null;
}

function allSame<T>(values: readonly T[]): boolean {
  return values.every((v) => v === values[0]);
}

/** Per-file scores for one criterion (higher = better, null = unknown / not comparable). */
export function criterionScores(
  type: CriterionType,
  files: readonly GroupFile[],
  profile?: Profile | null,
): Score[] {
  const versions = files.map((f) => f.version);
  const criterion = profileCriterion(profile, type);
  const direction: Direction = criterion?.direction ?? 'higher';

  switch (type) {
    case 'resolution':
    case 'dynamic_range':
    case 'source':
    case 'video_codec': {
      const order = orderFor(type, criterion);
      const key = (
        { resolution: 'resolution', dynamic_range: 'dynamicRange', source: 'source', video_codec: 'videoCodec' } as const
      )[type];
      return versions.map((v) => orderedScore(v?.[key], order));
    }
    case 'container': {
      // A full disc has no container of its own: the criterion is a tie when a disc is involved.
      if (versions.some((v) => v?.disc)) return versions.map(() => null);
      const order = orderFor(type, criterion);
      return versions.map((v) => {
        const c = (v?.container ?? '').toLowerCase();
        if (!c) return null;
        return orderedScore(order.includes(c) ? c : 'other', order);
      });
    }
    case 'audio_format': {
      const order = orderFor(type, criterion);
      return versions.map((v) => (v ? bestAudioScore(v, order) : null));
    }
    case 'library': {
      // Only meaningful when the profile ranks libraries.
      const order = criterion?.order ?? [];
      if (order.length === 0) return versions.map(() => null);
      return versions.map((v) => (v ? orderedScore(String(v.libraryId), order) : null));
    }
    case 'audio_channels':
      return versions.map((v) => directional(v ? maxChannels(v) : null, direction));
    case 'video_bitrate': {
      // D5: bitrates are only comparable between versions with the same video codec.
      if (!allSame(versions.map((v) => v?.videoCodec))) return versions.map(() => null);
      return versions.map((v) => directional(positive(v?.videoBitrateKbps) ?? positive(v?.bitrateKbps), direction));
    }
    case 'file_size':
      // A disc ranks by its main feature's clips (the engine's rankingSize), never by the whole disc
      // (menus, extras and 3D files would always win).
      return versions.map((v) => directional(positive(v?.disc?.featureBytes) ?? positive(versionSize(v)), direction));
    case 'bit_depth':
      return versions.map((v) => directional(positive(v?.bitDepth), direction));
    case 'custom_format_score': {
      // D5: only when every version is tracked by the same *arr instance and all scores are known.
      const instances = versions.map((v) => v?.arr?.instanceId ?? null);
      const scores = versions.map((v) => v?.arr?.customFormatScore);
      if (instances.some((i) => i === null) || !allSame(instances)) return versions.map(() => null);
      if (scores.some((s) => s === null || s === undefined || !Number.isFinite(s))) return versions.map(() => null);
      return scores.map((s) => directional(s as number, direction));
    }
    case 'date_added':
      return versions.map((v) => directional(toDate(v?.addedAt)?.getTime() ?? null, direction));
    case 'audio_track_count':
      return versions.map((v) => directional(v?.audioTracks?.length ?? 0, direction));
    case 'subtitle_track_count':
      return versions.map((v) => directional(v?.subtitleTracks?.length ?? 0, direction));
    case 'arr_managed': {
      const wanted = criterion?.value?.trim();
      return versions.map((v) => {
        if (!v?.arr) return 0;
        return !wanted || String(v.arr.instanceId) === wanted ? 1 : 0;
      });
    }
    case 'audio_language': {
      const wanted = criterion?.value?.trim().toLowerCase();
      if (!wanted) return versions.map(() => null);
      return versions.map((v) =>
        (v?.audioTracks ?? []).some(
          (t) => t?.languageCode?.toLowerCase() === wanted || t?.language?.toLowerCase() === wanted,
        )
          ? 1
          : 0,
      );
    }
    case 'health':
      // The engine's verdict ("Healthy" / "Unhealthy: …") when present; otherwise mirror D4:
      // missing file, or no codec / width 0 / bitrate 0 ⇒ unhealthy.
      return files.map((f) => {
        const shown = f.values?.health?.trim();
        if (shown) return /^healthy\b/i.test(shown) ? 1 : 0;
        const v = f.version;
        if (!v) return null;
        if (hasMissingPart(v)) return 0;
        return v.videoCodec && v.width > 0 && (positive(v.bitrateKbps) ?? positive(v.videoBitrateKbps)) ? 1 : 0;
      });
    case 'played':
    case 'last_played': {
      // docs/DECISIONS.md D10: plays are counted per Plex item (versions of one item tie), and an
      // unknown or unreadable history ties with every copy. Highlight only when every copy's
      // history is known and the copies are not all versions of one item.
      const keys = versions.map((v) => v?.ratingKey ?? '');
      const known = versions.map((v) => (v?.watch?.status === 'known' ? v.watch : null));
      if (known.some((w) => w === null) || (keys[0] !== '' && allSame(keys))) return versions.map(() => null);
      // A copy with no plays recorded only loses to a play on or after its `since` date (the date
      // it was added): when the latest play is older than that, the engine ties them.
      const latest = Math.max(
        ...known.map((w) => (w!.plays > 0 ? (toDate(w!.lastPlayed)?.getTime() ?? -Infinity) : -Infinity)),
      );
      const tied = known.some((w) => w!.plays <= 0 && !(latest >= (toDate(w!.since)?.getTime() ?? Infinity)));
      if (tied) return versions.map(() => null);
      if (type === 'played') return known.map((w) => (w!.plays > 0 ? 1 : 0));
      return known.map((w) => (w!.plays > 0 ? (toDate(w!.lastPlayed)?.getTime() ?? null) : 0));
    }
    case 'filename_score':
    default:
      return versions.map(() => null);
  }
}

/** Indices of the files holding the best value for a criterion. */
export function bestForCriterion(
  type: CriterionType,
  files: readonly GroupFile[],
  profile?: Profile | null,
): ReadonlySet<number> {
  return bestIndices(criterionScores(type, files, profile));
}

/**
 * Criterion → ids of the files whose removal it decided (from `decidingCriterion` of removed or
 * lower-ranked files), used to emphasize the deciding rows.
 */
export function decidingCriteria(files: readonly GroupFile[]): Map<CriterionType, number[]> {
  const out = new Map<CriterionType, number[]>();
  for (const f of files) {
    const c = f.decidingCriterion;
    if (!c || f.decision !== 'remove') continue;
    const list = out.get(c) ?? [];
    list.push(f.id);
    out.set(c, list);
  }
  return out;
}
