import { describe, expect, it } from 'vitest';
import type { ArrFileInfo, Profile } from '@/api/types';
import { bestForCriterion, bestIndices, criterionScores, decidingCriteria } from './comparison';
import { GiB, discVersion, makeFile } from './testing';

const profile = (criteria: Profile['criteria']): Profile => ({
  id: 1,
  name: 'p',
  isDefault: true,
  criteria,
  keepCount: 1,
  keepPer: '',
  protections: [],
  createdAt: '',
  updatedAt: '',
});

const part = (size: number) => [{ id: 1, path: '/x', localPath: '', size, duration: 1 }];

describe('bestIndices', () => {
  it.each([
    [[3, 1, 2], [0]],
    [[3, 3, 1], [0, 1]],
    [[null, 2, 1], [1]],
    [[2, 2, 2], []], // no distinction
    [[null, null], []],
    [[5], []], // a single file never gets a highlight
    [[], []],
    [[Number.NaN, 1, 0], [1]],
  ])('%j → %j', (scores, expected) => {
    expect([...bestIndices(scores)]).toEqual(expected);
  });
});

describe('criterionScores', () => {
  const uhd = makeFile({ id: 1, version: { resolution: '2160', dynamicRange: 'dv', videoCodec: 'hevc', parts: part(60 * GiB) } });
  const hd = makeFile({ id: 2, version: { resolution: '1080', dynamicRange: 'hdr10', videoCodec: 'hevc', parts: part(10 * GiB) } });

  it('ranks ordered criteria by the default order', () => {
    expect([...bestForCriterion('resolution', [hd, uhd])]).toEqual([1]);
    // Default dynamic range order prefers HDR10 over DV without an HDR10 base layer.
    expect([...bestForCriterion('dynamic_range', [uhd, hd])]).toEqual([1]);
  });

  it('uses the engine default orders of D5 when the profile has none', () => {
    const hlg = makeFile({ id: 1, version: { dynamicRange: 'hlg' } });
    const dv = makeFile({ id: 2, version: { dynamicRange: 'dv' } });
    expect([...bestForCriterion('dynamic_range', [dv, hlg])]).toEqual([1]);
    // Unknown containers count as "other", which ranks above AVI and TS.
    const webm = makeFile({ id: 3, version: { container: 'webm' } });
    const avi = makeFile({ id: 4, version: { container: 'avi' } });
    const ts = makeFile({ id: 5, version: { container: 'ts' } });
    expect([...bestForCriterion('container', [avi, ts, webm])]).toEqual([2]);
  });

  it('ranks a standalone M2TS between the common containers and the unknown ones', () => {
    const m4v = makeFile({ id: 1, version: { container: 'm4v' } });
    const m2ts = makeFile({ id: 2, version: { container: 'm2ts' } });
    const webm = makeFile({ id: 3, version: { container: 'webm' } });
    const ts = makeFile({ id: 4, version: { container: 'ts' } });
    expect([...bestForCriterion('container', [m2ts, webm, ts])]).toEqual([0]);
    expect([...bestForCriterion('container', [m2ts, m4v])]).toEqual([1]);
  });

  it('treats the container as a tie when a full disc is compared', () => {
    const mkv = makeFile({ id: 1, version: { container: 'mkv' } });
    const disc = makeFile({ id: 2, version: discVersion() });
    expect(criterionScores('container', [mkv, disc])).toEqual([null, null]);
  });

  it('ranks a full disc right after a remux by default', () => {
    const remux = makeFile({ id: 1, version: { source: 'remux' } });
    const disc = makeFile({ id: 2, version: discVersion() });
    const encode = makeFile({ id: 3, version: { source: 'bluray' } });
    expect([...bestForCriterion('source', [disc, remux])]).toEqual([1]);
    expect([...bestForCriterion('source', [encode, disc])]).toEqual([1]);
  });

  it('follows the profile order and direction', () => {
    const p = profile([
      { type: 'resolution', enabled: true, order: ['1080', '2160'] },
      { type: 'file_size', enabled: true, direction: 'lower' },
    ]);
    expect([...bestForCriterion('resolution', [uhd, hd], p)]).toEqual([1]);
    expect([...bestForCriterion('file_size', [uhd, hd], p)]).toEqual([1]);
    expect([...bestForCriterion('file_size', [uhd, hd])]).toEqual([0]);
  });

  it('only compares bitrates of the same codec (D5)', () => {
    const a = makeFile({ id: 1, version: { videoCodec: 'hevc', videoBitrateKbps: 20_000 } });
    const b = makeFile({ id: 2, version: { videoCodec: 'h264', videoBitrateKbps: 30_000 } });
    const c = makeFile({ id: 3, version: { videoCodec: 'hevc', videoBitrateKbps: 0, bitrateKbps: 25_000 } });
    expect(criterionScores('video_bitrate', [a, b])).toEqual([null, null]);
    expect([...bestForCriterion('video_bitrate', [a, c])]).toEqual([1]); // falls back to overall bitrate
  });

  it('only compares custom format scores within one *arr instance with known scores', () => {
    const arr = (instanceId: number, score: number | null) =>
      ({ instanceId, customFormatScore: score }) as unknown as ArrFileInfo;
    const a = makeFile({ id: 1, version: { arr: arr(1, 100) } });
    const b = makeFile({ id: 2, version: { arr: arr(1, 50) } });
    const other = makeFile({ id: 3, version: { arr: arr(2, 500) } });
    const unknown = makeFile({ id: 4, version: { arr: arr(1, null) } });
    expect([...bestForCriterion('custom_format_score', [a, b])]).toEqual([0]);
    expect([...bestForCriterion('custom_format_score', [a, other])]).toEqual([]);
    expect([...bestForCriterion('custom_format_score', [a, unknown])]).toEqual([]);
  });

  it('scores audio by the best track and channels by the maximum', () => {
    const atmos = makeFile({ id: 1 }); // truehd_atmos 7.1 + ac3 5.1
    const aac = makeFile({
      id: 2,
      version: {
        audioTracks: [{ format: 'aac', codec: 'aac', profile: '', channels: 2, language: '', languageCode: '', title: '', default: true, atmos: false }],
      },
    });
    expect([...bestForCriterion('audio_format', [aac, atmos])]).toEqual([1]);
    expect([...bestForCriterion('audio_channels', [aac, atmos])]).toEqual([1]);
    expect([...bestForCriterion('subtitle_track_count', [aac, atmos])]).toEqual([]);
  });

  it('handles tracked/untracked, languages, health and unknown criteria', () => {
    const tracked = makeFile({ id: 1, version: { arr: { instanceId: 3 } as ArrFileInfo } });
    const untracked = makeFile({ id: 2, version: { arr: null } });
    expect([...bestForCriterion('arr_managed', [untracked, tracked])]).toEqual([1]);
    expect([...bestForCriterion('arr_managed', [untracked, tracked], profile([{ type: 'arr_managed', enabled: true, value: '9' }]))]).toEqual([]);

    const p = profile([{ type: 'audio_language', enabled: true, value: 'fre' }]);
    const english = makeFile({
      id: 3,
      version: { audioTracks: [{ format: 'ac3', codec: 'ac3', profile: '', channels: 6, language: 'English', languageCode: 'eng', title: '', default: true, atmos: false }] },
    });
    expect([...bestForCriterion('audio_language', [english, tracked], p)]).toEqual([1]);
    expect([...bestForCriterion('audio_language', [english, tracked])]).toEqual([]);

    const broken = makeFile({ id: 4, version: { width: 0 } });
    expect([...bestForCriterion('health', [broken, tracked])]).toEqual([1]);
    expect(criterionScores('filename_score', [broken, tracked])).toEqual([null, null]);

    // The engine's health verdict wins over the local heuristic (e.g. a sample-length file).
    const sample = makeFile({ id: 5, values: { health: 'Unhealthy: possible sample (5m of 2h)' } });
    const healthy = makeFile({ id: 6, values: { health: 'Healthy' } });
    expect(criterionScores('health', [sample, healthy])).toEqual([0, 1]);
    const missing = makeFile({ id: 7, version: { parts: [{ id: 1, path: '/m', localPath: '', size: 1, duration: 1, exists: false }] } });
    expect([...bestForCriterion('health', [missing, tracked])]).toEqual([1]);
  });

  it('never throws on sparse versions', () => {
    const sparse = makeFile({ id: 9, version: { parts: undefined as never, audioTracks: null, subtitleTracks: null, addedAt: '0001-01-01T00:00:00Z' } });
    for (const type of ['file_size', 'audio_format', 'audio_channels', 'date_added', 'subtitle_track_count', 'container'] as const) {
      expect(() => criterionScores(type, [sparse, makeFile()])).not.toThrow();
    }
    expect(criterionScores('date_added', [sparse])).toEqual([null]);
  });
});

describe('decidingCriteria', () => {
  it('maps criteria to the removed files they decided', () => {
    const files = [
      makeFile({ id: 1, decision: 'keep', decidingCriterion: '' }),
      makeFile({ id: 2, decision: 'remove', decidingCriterion: 'resolution' }),
      makeFile({ id: 3, decision: 'remove', decidingCriterion: 'resolution' }),
      makeFile({ id: 4, decision: 'remove', decidingCriterion: 'file_size' }),
      makeFile({ id: 5, decision: 'keep', decidingCriterion: 'source' }),
    ];
    expect(Object.fromEntries(decidingCriteria(files))).toEqual({ resolution: [2, 3], file_size: [4] });
  });
});
