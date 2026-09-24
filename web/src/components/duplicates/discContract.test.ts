import { describe, expect, it } from 'vitest';
import type { DiscInfo, MediaVersion } from '@/api/types';
import { containerLabel, discTypeLabel, discTypeShortLabel } from '@/lib/format';
import { bestForCriterion } from './comparison';
import { discApprovalBlocker } from './disc';
import { isHardlinked, versionSize } from './duplicateUtils';
import { clipCountOf, isDiscPath, mainClipName } from './disc';
import { GiB, clipSetVersion, discVersion, makeFile } from './testing';

// The server's disc contract (internal/models.DiscInfo, docs/API.md): the values and fields the
// API sends must be understood by the UI.

function version(disc: Partial<DiscInfo>, over: Partial<MediaVersion> = {}): MediaVersion {
  return makeFile({ version: discVersion(disc, over) }).version;
}

describe('disc contract with the API', () => {
  it('labels every disc kind the server sends, including Blu-ray recordings (bdav)', () => {
    expect(discTypeLabel('bdav')).toBe('Blu-ray recording (BDAV)');
    expect(discTypeShortLabel('bdav')).toBe('BDAV');
  });

  it('labels the loose clip set kinds (a flattened Blu-ray/DVD backup merged into one version)', () => {
    expect(discTypeLabel('bluray_clips')).toBe('Blu-ray clips (loose .m2ts)');
    expect(discTypeLabel('dvd_clips')).toBe('DVD files (loose VOB)');
    expect(discTypeShortLabel('bluray_clips')).toBe('BD clips');
    expect(discTypeShortLabel('dvd_clips')).toBe('DVD VOBs');
  });

  it('understands the additive clip set fields (clipCount, mediaIds) and works without them', () => {
    const v = version({ type: 'bluray_clips', clipCount: 125, mediaIds: [1, 2, 3], fileCount: 125, origin: 'plex' });
    expect(clipCountOf(v)).toBe(125);
    // An older answer without clipCount: the clip-named parts are counted.
    expect(clipCountOf(makeFile({ version: clipSetVersion({ clipCount: undefined, mediaIds: undefined }) }).version)).toBe(3);
    expect(mainClipName(makeFile({ version: clipSetVersion() }).version)).toBe('00174.m2ts');
    // The server's guard and this one agree: a loose clip is a file of a disc (internal/disc.IsDiscPath).
    expect(isDiscPath('/data/Movies/Elemental (2023)/00174.m2ts')).toBe(true);
  });

  it('sizes a loose clip set by the sum of its clips (no measured totalBytes: Plex only)', () => {
    const v = makeFile({ version: clipSetVersion({ totalBytes: 0 }) }).version;
    expect(versionSize(v)).toBe(30 * GiB + GiB / 2 + GiB / 4);
  });

  it('labels the container of a disc version ("disc")', () => {
    expect(containerLabel('disc')).toBe('Full disc');
  });

  it('sizes a custom-scanner disc by the whole disc, not by the clips Plex lists as parts', () => {
    const v = version(
      { origin: 'plex', totalBytes: 61 * GiB, featureBytes: 48 * GiB, freedBytes: 61 * GiB },
      { parts: [{ id: 1, path: '/data/movies/Heat (1995)/BDMV/STREAM/00800.m2ts', localPath: '', size: 40 * GiB, duration: 1 }] },
    );
    expect(versionSize(v)).toBe(61 * GiB);
  });

  it('keeps the parts sum when the disc was not measured', () => {
    const v = version({ totalBytes: 0 });
    expect(versionSize(v)).toBe(61 * GiB);
  });

  it('reports a disc with hardlinked files (a removal frees nothing for them)', () => {
    expect(isHardlinked(version({ hardlinkedFiles: 3 }))).toBe(true);
    expect(isHardlinked(version({ hardlinkedFiles: 0 }))).toBe(false);
  });

  it('compares a disc on file size by its main feature, like the engine', () => {
    // 61 GiB on disk, but a 30 GiB feature: a 40 GiB remux is the larger copy.
    const d = makeFile({ id: 1, version: discVersion({ totalBytes: 61 * GiB, featureBytes: 30 * GiB }) });
    const remux = makeFile({ id: 2, version: { parts: [{ id: 2, path: '/m/Heat.mkv', localPath: '', size: 40 * GiB, duration: 1 }] } });
    expect([...bestForCriterion('file_size', [d, remux])]).toEqual([1]);
  });

  it('blocks the approval of a disc the server reports as not removable', () => {
    const files = [
      makeFile({ id: 1, decision: 'remove', version: discVersion({ removable: false, problem: 'the multi-disc set is unclear' }) }),
      makeFile({ id: 2, decision: 'keep' }),
    ];
    const settings = { allowDiscRemoval: true, recycleBinPath: '/bin', keepPlayableCopy: true };
    expect(discApprovalBlocker(files, { settings, mediaType: 'movie' })).toContain('the multi-disc set is unclear');
    files[0] = makeFile({ id: 1, decision: 'remove', version: discVersion({ removable: true }) });
    expect(discApprovalBlocker(files, { settings, mediaType: 'movie' })).toBeNull();
  });
});
