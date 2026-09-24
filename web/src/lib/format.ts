/**
 * Pure formatting helpers (bytes, durations, dates, media attribute labels).
 * No React here — see components/ui/ByteSize & RelativeTime for the component wrappers.
 */
import type {
  AudioFormat,
  AudioTrack,
  CommandName,
  ContainerFormat,
  DiscType,
  DynamicRange,
  GroupStatus,
  MediaType,
  ReleaseSource,
  ResolutionTier,
  VideoCodec,
} from '@/api/types';
import {
  ACTION_STATUS_LABELS,
  AUDIO_FORMAT_LABELS,
  COMMAND_NAME_LABELS,
  CONTAINER_LABELS,
  DISC_TYPE_LABELS,
  DISC_TYPE_SHORT_LABELS,
  DYNAMIC_RANGE_LABELS,
  GROUP_STATUS_LABELS,
  RESOLUTION_LABELS,
  SOURCE_LABELS,
  VIDEO_CODEC_LABELS,
  labelOf,
} from './constants';

type DateInput = string | number | Date | null | undefined;

// ---------------------------------------------------------------------------
// Numbers & sizes
// ---------------------------------------------------------------------------

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'];

/**
 * Binary byte size: 4509715660 → "4.2 GiB", 0 → "0 B", 1536 → "1.5 KiB".
 * `decimals` applies to KiB and above (bytes are always integers). Null/NaN → "".
 */
export function formatBytes(bytes: number | null | undefined, decimals = 1): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return '';
  const sign = bytes < 0 ? '-' : '';
  let value = Math.abs(bytes);
  if (value < 1024) return `${sign}${Math.round(value)} B`;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // Avoid "1024.0 MiB" after rounding: bump to the next unit.
  if (Number(value.toFixed(decimals)) >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${sign}${value.toFixed(decimals)} ${BYTE_UNITS[unit]}`;
}

/** Bitrate in kbps: 24500 → "24.5 Mbps", 640 → "640 kbps", 0/null → "". */
export function formatBitrate(kbps: number | null | undefined): string {
  if (!kbps || !Number.isFinite(kbps)) return '';
  if (kbps >= 1000) return `${(kbps / 1000).toFixed(1)} Mbps`;
  return `${Math.round(kbps)} kbps`;
}

/** Thousands separators: 12345 → "12,345". */
export function formatNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '';
  return new Intl.NumberFormat('en-US').format(value);
}

/** 0.1234 → "12%"; `decimals` optional. */
export function formatPercent(ratio: number | null | undefined, decimals = 0): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return '';
  return `${(ratio * 100).toFixed(decimals)}%`;
}

// ---------------------------------------------------------------------------
// Durations
// ---------------------------------------------------------------------------

/**
 * Milliseconds → compact duration: 7_980_000 → "2h 13m", 45_000 → "45s", 303_000 → "5m 3s",
 * 93_600_000 → "1d 2h", 250 → "250ms". Null/NaN/negative → "".
 */
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return '';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const totalSeconds = Math.round(ms / 1000);
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (days > 0) return hours ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return minutes ? `${hours}h ${minutes}m` : `${hours}h`;
  if (minutes > 0) return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
  return `${seconds}s`;
}

/** Seconds → compact duration (uptime). */
export function formatSeconds(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return '';
  return formatDuration(seconds * 1000);
}

/** Minutes → "6h", "1d", "30m"; 0 → "Disabled". */
export function formatInterval(minutes: number | null | undefined): string {
  if (minutes === null || minutes === undefined || !Number.isFinite(minutes)) return '';
  if (minutes <= 0) return 'Disabled';
  return formatDuration(minutes * 60_000);
}

/**
 * Parses a .NET-style timespan ("00:00:12.345", "1.02:03:04", "02:03:04.5") → ms. Null when invalid.
 */
export function parseTimeSpan(value: string | null | undefined): number | null {
  if (!value) return null;
  const m = /^(?:(\d+)\.)?(\d{1,2}):(\d{2}):(\d{2})(?:\.(\d+))?$/.exec(value.trim());
  if (!m) return null;
  const [, d, h, min, s, frac] = m;
  const fracMs = frac ? Math.round(Number(`0.${frac}`) * 1000) : 0;
  return (
    ((Number(d ?? 0) * 24 + Number(h)) * 60 + Number(min)) * 60_000 + Number(s) * 1000 + fracMs
  );
}

/** "00:00:12.345" → "12s" (falls back to the raw value when unparsable). */
export function formatTimeSpan(value: string | null | undefined): string {
  const ms = parseTimeSpan(value);
  return ms === null ? (value ?? '') : formatDuration(ms);
}

// ---------------------------------------------------------------------------
// Dates
// ---------------------------------------------------------------------------

export function toDate(value: DateInput): Date | null {
  if (value === null || value === undefined || value === '') return null;
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return null;
  // Go zero time ("0001-01-01T00:00:00Z") means "unset".
  if (d.getUTCFullYear() <= 1) return null;
  return d;
}

/** Short date formats offered in Settings → UI (moment-style tokens, like *arr). */
export type ShortDateFormat = 'MMM D YYYY' | 'DD MMM YYYY' | 'MM/D/YYYY' | 'MM/DD/YYYY' | 'DD/MM/YYYY' | 'YYYY-MM-DD';
export type LongDateFormat = 'dddd, MMMM D YYYY' | 'dddd, D MMMM YYYY';
export type TimeFormat = 'h(:mm)a' | 'HH:mm';

export interface DateFormatOptions {
  shortDateFormat?: ShortDateFormat;
  longDateFormat?: LongDateFormat;
  timeFormat?: TimeFormat;
}

const MONTHS = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
];
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

const pad = (n: number, len = 2) => String(n).padStart(len, '0');

/**
 * Formats a date with moment-style tokens (local time): YYYY, MMMM, MMM, MM, M, DD, D, dddd, ddd,
 * HH, H, hh, h, mm, ss, a. Text in [brackets] is literal. `h(:mm)a` omits ":00" minutes.
 */
export function formatDateTokens(date: Date, pattern: string): string {
  if (pattern === 'h(:mm)a') {
    const h = date.getHours() % 12 || 12;
    const m = date.getMinutes();
    return `${h}${m ? `:${pad(m)}` : ''}${date.getHours() < 12 ? 'am' : 'pm'}`;
  }
  return pattern.replace(
    /\[([^\]]*)]|YYYY|MMMM|MMM|MM|M|DD|D|dddd|ddd|HH|H|hh|h|mm|ss|a/g,
    (token, literal: string | undefined) => {
      if (literal !== undefined) return literal;
      switch (token) {
        case 'YYYY':
          return String(date.getFullYear());
        case 'MMMM':
          return MONTHS[date.getMonth()]!;
        case 'MMM':
          return MONTHS[date.getMonth()]!.slice(0, 3);
        case 'MM':
          return pad(date.getMonth() + 1);
        case 'M':
          return String(date.getMonth() + 1);
        case 'DD':
          return pad(date.getDate());
        case 'D':
          return String(date.getDate());
        case 'dddd':
          return WEEKDAYS[date.getDay()]!;
        case 'ddd':
          return WEEKDAYS[date.getDay()]!.slice(0, 3);
        case 'HH':
          return pad(date.getHours());
        case 'H':
          return String(date.getHours());
        case 'hh':
          return pad(date.getHours() % 12 || 12);
        case 'h':
          return String(date.getHours() % 12 || 12);
        case 'mm':
          return pad(date.getMinutes());
        case 'ss':
          return pad(date.getSeconds());
        case 'a':
          return date.getHours() < 12 ? 'am' : 'pm';
        default:
          return token;
      }
    },
  );
}

/** Absolute short date: "Sep 22 2026" (default format). */
export function formatDate(value: DateInput, opts: DateFormatOptions = {}): string {
  const d = toDate(value);
  if (!d) return '';
  return formatDateTokens(d, opts.shortDateFormat ?? 'MMM D YYYY');
}

/**
 * Short date of the UTC calendar day ("Mar 1 2025" in every time zone), for dates the server
 * writes as UTC days in its own texts (the play-history reasons): both then name the same day.
 */
export function formatUtcDate(value: DateInput, opts: DateFormatOptions = {}): string {
  const d = toDate(value);
  if (!d) return '';
  return formatDateTokens(
    new Date(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()),
    opts.shortDateFormat ?? 'MMM D YYYY',
  );
}

/** Time of day: "5:02pm" / "17:02" (with seconds optional). */
export function formatTime(value: DateInput, opts: DateFormatOptions & { includeSeconds?: boolean } = {}): string {
  const d = toDate(value);
  if (!d) return '';
  const fmt = opts.timeFormat ?? 'h(:mm)a';
  if (opts.includeSeconds) {
    return formatDateTokens(d, fmt === 'HH:mm' ? 'HH:mm:ss' : 'h:mm:ssa');
  }
  return formatDateTokens(d, fmt);
}

/** Absolute date + time: "Sep 22 2026 5:02pm". */
export function formatDateTime(
  value: DateInput,
  opts: DateFormatOptions & { includeSeconds?: boolean } = {},
): string {
  const d = toDate(value);
  if (!d) return '';
  return `${formatDate(d, opts)} ${formatTime(d, opts)}`;
}

/** Long absolute date + time for tooltips: "Tuesday, September 22 2026 5:02:13pm". */
export function formatLongDateTime(value: DateInput, opts: DateFormatOptions = {}): string {
  const d = toDate(value);
  if (!d) return '';
  return `${formatDateTokens(d, opts.longDateFormat ?? 'dddd, MMMM D YYYY')} ${formatTime(d, {
    ...opts,
    includeSeconds: true,
  })}`;
}

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
];

/**
 * Relative time: "just now", "5 minutes ago", "in 2 hours", "yesterday", "3 days ago",
 * "2 months ago". `now` is injectable for tests.
 */
export function formatRelativeTime(value: DateInput, now: DateInput = new Date()): string {
  const d = toDate(value);
  const n = toDate(now) ?? new Date();
  if (!d) return '';
  const diffSeconds = (d.getTime() - n.getTime()) / 1000;
  const abs = Math.abs(diffSeconds);
  if (abs < 45) return 'just now';
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  for (const [unit, seconds] of RELATIVE_UNITS) {
    // Round up to the unit when we're at ≥ 90% of it (e.g. 55 minutes → "1 hour ago").
    if (abs >= seconds * 0.9 || unit === 'minute') {
      const amount = Math.round(diffSeconds / seconds);
      return rtf.format(amount === 0 ? Math.sign(diffSeconds) : amount, unit);
    }
  }
  return rtf.format(Math.round(diffSeconds / 60), 'minute');
}

// ---------------------------------------------------------------------------
// Media labels
// ---------------------------------------------------------------------------

/** "2160" → "2160p (4K)", "sd" → "SD". */
export function resolutionLabel(tier: ResolutionTier | string | null | undefined): string {
  if (!tier) return '';
  return labelOf(RESOLUTION_LABELS, tier);
}

/** "dv_hdr10" → "Dolby Vision (HDR10)". */
export function dynamicRangeLabel(value: DynamicRange | string | null | undefined): string {
  if (!value) return '';
  return labelOf(DYNAMIC_RANGE_LABELS, value);
}

/** "truehd_atmos" → "TrueHD Atmos". */
export function audioFormatLabel(value: AudioFormat | string | null | undefined): string {
  if (!value) return '';
  return labelOf(AUDIO_FORMAT_LABELS, value);
}

/** "hevc" → "HEVC (H.265)". */
export function videoCodecLabel(value: VideoCodec | string | null | undefined): string {
  if (!value) return '';
  return labelOf(VIDEO_CODEC_LABELS, value);
}

/** "webdl" → "WEB-DL". */
export function sourceLabel(value: ReleaseSource | string | null | undefined): string {
  if (!value) return '';
  return labelOf(SOURCE_LABELS, value);
}

/** "m2ts" → "M2TS" (case-insensitive; unknown values are humanized). */
export function containerLabel(value: ContainerFormat | string | null | undefined): string {
  if (!value) return '';
  return labelOf(CONTAINER_LABELS, value.toLowerCase());
}

/** "uhd_bluray" → "UHD Blu-ray disc (BDMV)", "iso" → "ISO image". */
export function discTypeLabel(type: DiscType | string | null | undefined): string {
  if (!type) return '';
  return labelOf(DISC_TYPE_LABELS, type);
}

/** Chip text of a disc: "uhd_bluray" → "UHD BD", "dvd" → "DVD". */
export function discTypeShortLabel(type: DiscType | string | null | undefined): string {
  if (!type) return '';
  const hit = (DISC_TYPE_SHORT_LABELS as Record<string, string>)[type];
  return hit ?? 'Disc';
}

/** 1 → "1 file", 312 → "312 files", 0/unknown → "". */
export function formatFileCount(count: number | null | undefined): string {
  if (count === null || count === undefined || !Number.isFinite(count) || count <= 0) return '';
  return `${formatNumber(count)} ${count === 1 ? 'file' : 'files'}`;
}

/** Loose clip sets: 1 → "1 clip", 125 → "125 clips", 0/unknown → "". */
export function formatClipCount(count: number | null | undefined): string {
  if (count === null || count === undefined || !Number.isFinite(count) || count <= 0) return '';
  return `${formatNumber(count)} ${count === 1 ? 'clip' : 'clips'}`;
}

/** Multi-disc sets only: 2 → "2 discs"; 1 (a single disc) or unknown → "". */
export function formatDiscCount(discs: number | null | undefined): string {
  if (discs === null || discs === undefined || !Number.isFinite(discs) || discs <= 1) return '';
  return `${formatNumber(discs)} discs`;
}

/**
 * One-line disc description: "UHD Blu-ray disc (BDMV) · 312 files · 2 discs" (missing parts are
 * skipped; "" for a non-disc). A loose clip set with a clip count leads with it: "Blu-ray clips
 * (loose .m2ts) · 125 clips" (plus " · 127 files" when metadata files belong to the set too).
 */
export function discSummary(
  disc: { type: DiscType | string; fileCount?: number; discs?: number; clipCount?: number } | null | undefined,
): string {
  if (!disc) return '';
  const clips = disc.clipCount ?? 0;
  const files = clips > 0 && (disc.fileCount ?? 0) <= clips ? '' : formatFileCount(disc.fileCount);
  return [discTypeLabel(disc.type) || 'Disc', formatClipCount(clips), files, formatDiscCount(disc.discs)]
    .filter(Boolean)
    .join(' · ');
}

/** Channel count → layout: 8 → "7.1", 6 → "5.1", 2 → "2.0", 1 → "1.0". */
export function audioChannelsLabel(channels: number | null | undefined): string {
  if (!channels || channels < 1) return '';
  const known: Record<number, string> = { 1: '1.0', 2: '2.0', 3: '2.1', 6: '5.1', 7: '6.1', 8: '7.1', 10: '9.1' };
  return known[channels] ?? `${channels}ch`;
}

/** One-line audio description: "TrueHD Atmos 7.1 (English)". */
export function audioTrackLabel(track: AudioTrack | null | undefined): string {
  if (!track) return '';
  const parts = [audioFormatLabel(track.format), audioChannelsLabel(track.channels)].filter(Boolean);
  const lang = track.language || track.languageCode;
  return lang ? `${parts.join(' ')} (${lang})` : parts.join(' ');
}

/** Group status label: "review" → "Needs Review". */
export function statusLabel(status: GroupStatus | string | null | undefined): string {
  if (!status) return '';
  return labelOf(GROUP_STATUS_LABELS, status);
}

/** Action status label: "dry_run" → "Dry Run". */
export function actionStatusLabel(status: string | null | undefined): string {
  if (!status) return '';
  return labelOf(ACTION_STATUS_LABELS, status);
}

/** Command name label: "DuplicateScan" → "Duplicate Scan". */
export function commandLabel(name: CommandName | string | null | undefined): string {
  if (!name) return '';
  return labelOf(COMMAND_NAME_LABELS, name);
}

/**
 * Season/episode code: (1, 2) → "S01E02", matching the server's titles (engine.EpisodeLabel).
 * Unknown numbers show as "??", never "S-1": a negative season (Plex reports hidden seasons as
 * -1) and an episode number below 1. The API omits zero values, so a missing season is season 0
 * (specials): (undefined, 3) → "S00E03", (-1, 2) → "S??E02", (2, undefined) → "S02E??". Both
 * missing → "".
 */
export function episodeCode(season?: number | null, episode?: number | null): string {
  const missing = (n: number | null | undefined): n is null | undefined => n === null || n === undefined;
  if (missing(season) && missing(episode)) return '';
  const s = season ?? 0;
  const e = episode ?? 0;
  const seasonPart = Number.isInteger(s) && s >= 0 ? pad(s) : '??';
  const episodePart = Number.isInteger(e) && e > 0 ? pad(e) : '??';
  return `S${seasonPart}E${episodePart}`;
}

/** Display title of a group/summary: movie "Heat (1995)", episode "Show - S01E02 - Title". */
export function mediaTitle(item: {
  mediaType: MediaType;
  title: string;
  year?: number;
  showTitle?: string;
  season?: number;
  episode?: number;
}): string {
  if (item.mediaType === 'episode') {
    return [item.showTitle, episodeCode(item.season, item.episode), item.title].filter(Boolean).join(' - ');
  }
  return item.year ? `${item.title} (${item.year})` : item.title;
}

/** "3840x2160" (empty when unknown). */
export function formatDimensions(width?: number | null, height?: number | null): string {
  if (!width || !height) return '';
  return `${width}x${height}`;
}
