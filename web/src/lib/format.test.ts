import { describe, expect, it } from 'vitest';
import {
  audioChannelsLabel,
  audioFormatLabel,
  containerLabel,
  discSummary,
  discTypeLabel,
  discTypeShortLabel,
  dynamicRangeLabel,
  episodeCode,
  formatBitrate,
  formatBytes,
  formatDateTokens,
  formatDiscCount,
  formatDuration,
  formatClipCount,
  formatFileCount,
  formatInterval,
  formatRelativeTime,
  formatTimeSpan,
  mediaTitle,
  parseTimeSpan,
  resolutionLabel,
  sourceLabel,
  statusLabel,
  toDate,
  videoCodecLabel,
} from './format';
import {
  CONTAINER_ORDER,
  DISC_TYPE_LABELS,
  GROUP_FLAG_DESCRIPTIONS,
  GROUP_FLAG_KIND,
  GROUP_FLAG_LABELS,
  GROUP_STATUS_LABELS,
  SOURCE_ORDER,
  humanize,
  labelOf,
} from './constants';

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [512, '512 B'],
    [1536, '1.5 KiB'],
    [1024 * 1024, '1.0 MiB'],
    [4.2 * 1024 ** 3, '4.2 GiB'],
    [58.3 * 1024 ** 4, '58.3 TiB'],
    [-2048, '-2.0 KiB'],
  ])('%d → %s', (bytes, expected) => {
    expect(formatBytes(bytes)).toBe(expected);
  });

  it('never shows 1024.0 of a unit after rounding', () => {
    expect(formatBytes(1024 * 1024 - 1)).toBe('1.0 MiB');
  });

  it('honours decimals and handles empty input', () => {
    expect(formatBytes(4.25 * 1024 ** 3, 2)).toBe('4.25 GiB');
    expect(formatBytes(null)).toBe('');
    expect(formatBytes(undefined)).toBe('');
    expect(formatBytes(Number.NaN)).toBe('');
  });
});

describe('formatDuration', () => {
  it.each([
    [7_980_000, '2h 13m'],
    [7_200_000, '2h'],
    [45_000, '45s'],
    [303_000, '5m 3s'],
    [300_000, '5m'],
    [93_600_000, '1d 2h'],
    [86_400_000, '1d'],
    [250, '250ms'],
  ])('%d ms → %s', (ms, expected) => {
    expect(formatDuration(ms)).toBe(expected);
  });

  it('returns "" for invalid values', () => {
    expect(formatDuration(null)).toBe('');
    expect(formatDuration(-1)).toBe('');
  });

  it('formats intervals in minutes', () => {
    expect(formatInterval(360)).toBe('6h');
    expect(formatInterval(0)).toBe('Disabled');
  });
});

describe('timespans', () => {
  it('parses .NET-style timespans', () => {
    expect(parseTimeSpan('00:00:12.345')).toBe(12_345);
    expect(parseTimeSpan('01:02:03')).toBe(3_723_000);
    expect(parseTimeSpan('1.02:00:00')).toBe(93_600_000);
    expect(parseTimeSpan('nonsense')).toBeNull();
  });

  it('formats them compactly', () => {
    expect(formatTimeSpan('00:00:12.345')).toBe('12s');
    expect(formatTimeSpan('bogus')).toBe('bogus');
  });
});

describe('dates', () => {
  const now = new Date('2026-09-22T12:00:00Z');

  it('formats relative times', () => {
    expect(formatRelativeTime(new Date(now.getTime() - 10_000), now)).toBe('just now');
    expect(formatRelativeTime(new Date(now.getTime() - 5 * 60_000), now)).toBe('5 minutes ago');
    expect(formatRelativeTime(new Date(now.getTime() - 2 * 3_600_000), now)).toBe('2 hours ago');
    expect(formatRelativeTime(new Date(now.getTime() - 24 * 3_600_000), now)).toBe('yesterday');
    expect(formatRelativeTime(new Date(now.getTime() - 3 * 86_400_000), now)).toBe('3 days ago');
    expect(formatRelativeTime(new Date(now.getTime() + 2 * 3_600_000), now)).toBe('in 2 hours');
  });

  it('treats empty and Go zero times as unset', () => {
    expect(toDate('')).toBeNull();
    expect(toDate('0001-01-01T00:00:00Z')).toBeNull();
    expect(formatRelativeTime(null, now)).toBe('');
  });

  it('formats moment-style tokens', () => {
    const d = new Date(2026, 8, 2, 17, 5, 9); // local time
    expect(formatDateTokens(d, 'MMM D YYYY')).toBe('Sep 2 2026');
    expect(formatDateTokens(d, 'YYYY-MM-DD')).toBe('2026-09-02');
    expect(formatDateTokens(d, 'dddd, MMMM D YYYY')).toBe('Wednesday, September 2 2026');
    expect(formatDateTokens(d, 'HH:mm:ss')).toBe('17:05:09');
    expect(formatDateTokens(d, 'h(:mm)a')).toBe('5:05pm');
    expect(formatDateTokens(new Date(2026, 0, 1, 9, 0), 'h(:mm)a')).toBe('9am');
  });
});

describe('media labels', () => {
  it('labels resolutions', () => {
    expect(resolutionLabel('2160')).toBe('2160p (4K)');
    expect(resolutionLabel('1080')).toBe('1080p');
    expect(resolutionLabel('sd')).toBe('SD');
  });

  it('labels dynamic ranges', () => {
    expect(dynamicRangeLabel('dv_hdr10')).toBe('Dolby Vision (HDR10)');
    expect(dynamicRangeLabel('hdr10plus')).toBe('HDR10+');
    expect(dynamicRangeLabel('sdr')).toBe('SDR');
  });

  it('labels audio', () => {
    expect(audioFormatLabel('truehd_atmos')).toBe('TrueHD Atmos');
    expect(audioFormatLabel('dts_hd_ma')).toBe('DTS-HD MA');
    expect(audioChannelsLabel(8)).toBe('7.1');
    expect(audioChannelsLabel(6)).toBe('5.1');
    expect(audioChannelsLabel(2)).toBe('2.0');
    expect(audioChannelsLabel(0)).toBe('');
  });

  it('labels codecs, bitrates and statuses', () => {
    expect(videoCodecLabel('hevc')).toBe('HEVC (H.265)');
    expect(formatBitrate(24_500)).toBe('24.5 Mbps');
    expect(formatBitrate(640)).toBe('640 kbps');
    expect(statusLabel('review')).toBe('Needs Review');
  });

  it('falls back to humanized values for unknown enum values', () => {
    expect(resolutionLabel('4320')).toBe('4320');
    expect(labelOf(GROUP_STATUS_LABELS, 'brand_new')).toBe('Brand New');
    expect(humanize('onFileDeleted')).toBe('On File Deleted');
  });

  it.each([
    // Plex's hidden/unknown season is -1: shown as unknown, never "S-1".
    [-1, 2, 'S??E02'],
    [-5, -1, 'S??E??'],
    [Number.NaN, 4, 'S??E04'],
    [1.5, 4, 'S??E04'],
    // The API omits zero values: a missing season is season 0 (specials)…
    [undefined, 3, 'S00E03'],
    [null, 3, 'S00E03'],
    // …and an episode number below 1 is unknown (as in the server's titles).
    [0, 0, 'S00E??'],
    [2, undefined, 'S02E??'],
    [undefined, undefined, ''],
    [null, null, ''],
  ] as const)('episodeCode(%s, %s) → %j', (season, episode, expected) => {
    expect(episodeCode(season, episode)).toBe(expected);
  });

  it('builds media titles', () => {
    expect(episodeCode(1, 2)).toBe('S01E02');
    expect(episodeCode(10, 102)).toBe('S10E102');
    expect(mediaTitle({ mediaType: 'movie', title: 'Heat', year: 1995 })).toBe('Heat (1995)');
    expect(
      mediaTitle({ mediaType: 'episode', title: 'Pilot', showTitle: 'Lost', season: 1, episode: 1 }),
    ).toBe('Lost - S01E01 - Pilot');
    expect(
      mediaTitle({ mediaType: 'episode', title: 'Pilot', showTitle: 'Lost', season: -1, episode: 1 }),
    ).toBe('Lost - S??E01 - Pilot');
  });
});

describe('full-disc labels', () => {
  it.each([
    ['uhd_bluray', 'UHD Blu-ray disc (BDMV)', 'UHD BD'],
    ['bluray', 'Blu-ray disc (BDMV)', 'BD'],
    ['dvd', 'DVD (VIDEO_TS)', 'DVD'],
    ['hddvd', 'HD DVD', 'HD DVD'],
    ['avchd', 'AVCHD', 'AVCHD'],
    ['iso', 'ISO image', 'ISO'],
    // Loose clip sets (a flattened backup: clips directly in the movie folder).
    ['bluray_clips', 'Blu-ray clips (loose .m2ts)', 'BD clips'],
    ['dvd_clips', 'DVD files (loose VOB)', 'DVD VOBs'],
  ] as const)('%s → %s / %s', (type, label, short) => {
    expect(discTypeLabel(type)).toBe(label);
    expect(DISC_TYPE_LABELS[type]).toBe(label);
    expect(discTypeShortLabel(type)).toBe(short);
  });

  it('falls back gracefully for unknown or missing disc types', () => {
    expect(discTypeLabel('bluray_3d')).toBe('Bluray 3d');
    expect(discTypeShortLabel('bluray_3d')).toBe('Disc');
    expect(discTypeLabel(undefined)).toBe('');
    expect(discTypeShortLabel(null)).toBe('');
  });

  it('counts files and discs', () => {
    expect(formatFileCount(312)).toBe('312 files');
    expect(formatFileCount(1)).toBe('1 file');
    expect(formatFileCount(12_345)).toBe('12,345 files');
    expect(formatFileCount(0)).toBe('');
    expect(formatFileCount(undefined)).toBe('');
    expect(formatDiscCount(2)).toBe('2 discs');
    expect(formatDiscCount(1)).toBe('');
    expect(formatDiscCount(0)).toBe('');
  });

  it('summarizes a disc in one line', () => {
    expect(discSummary({ type: 'uhd_bluray', fileCount: 312, discs: 2 })).toBe('UHD Blu-ray disc (BDMV) · 312 files · 2 discs');
    expect(discSummary({ type: 'dvd', fileCount: 24, discs: 1 })).toBe('DVD (VIDEO_TS) · 24 files');
    expect(discSummary({ type: 'iso' })).toBe('ISO image');
    expect(discSummary(null)).toBe('');
  });

  it('counts the clips of a loose clip set', () => {
    expect(formatClipCount(125)).toBe('125 clips');
    expect(formatClipCount(1)).toBe('1 clip');
    expect(formatClipCount(1_024)).toBe('1,024 clips');
    expect(formatClipCount(0)).toBe('');
    expect(formatClipCount(undefined)).toBe('');
    expect(formatClipCount(Number.NaN)).toBe('');
    // Clips lead; the file count only shows when metadata files belong to the set too.
    expect(discSummary({ type: 'bluray_clips', fileCount: 125, discs: 1, clipCount: 125 })).toBe(
      'Blu-ray clips (loose .m2ts) · 125 clips',
    );
    expect(discSummary({ type: 'bluray_clips', fileCount: 128, discs: 1, clipCount: 125 })).toBe(
      'Blu-ray clips (loose .m2ts) · 125 clips · 128 files',
    );
    expect(discSummary({ type: 'dvd_clips', fileCount: 9, discs: 1 })).toBe('DVD files (loose VOB) · 9 files');
  });

  it('labels the disc source and the m2ts container', () => {
    expect(sourceLabel('disc')).toBe('Full disc (BDMV/VIDEO_TS/ISO)');
    // Default source rank: a full disc right after a remux.
    expect(SOURCE_ORDER.slice(0, 3)).toEqual(['remux', 'disc', 'bluray']);
    expect(containerLabel('m2ts')).toBe('M2TS');
    expect(containerLabel('M2TS')).toBe('M2TS');
    expect(containerLabel('ts')).toBe('TS');
    expect(containerLabel('')).toBe('');
    expect(CONTAINER_ORDER).toContain('m2ts');
  });

  it.each(['full_disc', 'disc_unreadable', 'disc_tracked_clip'] as const)('labels and describes the %s flag', (flag) => {
    expect(GROUP_FLAG_LABELS[flag]).toBeTruthy();
    expect(GROUP_FLAG_DESCRIPTIONS[flag].length).toBeGreaterThan(20);
    expect(GROUP_FLAG_KIND[flag]).toBeTruthy();
  });
});
