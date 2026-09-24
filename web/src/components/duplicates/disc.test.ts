import { describe, expect, it } from 'vitest';
import type { GroupFile } from '@/api/types';
import {
  CLIP_SET_REMOVAL_KEEPS_NOTE,
  DISC_REMOVAL_KEEPS_NOTE,
  baseName,
  clipCountOf,
  clipKind,
  clipParts,
  discApprovalBlocker,
  discMemberBlocker,
  discMemberPath,
  discRelativePath,
  discRemovalKeepsNote,
  discRemovalNote,
  isClipName,
  isClipSet,
  isClipSetType,
  isDiscPath,
  isDiscVersion,
  isLooseClipPath,
  mainClipName,
  overrideBlocker,
  removesDisc,
  removesDiscClip,
  type DiscSettings,
} from './disc';
import { CLIP_FOLDER, GiB, clipPart, clipSetVersion, clipVersion, discVersion, makeFile } from './testing';

describe('isDiscPath', () => {
  it.each([
    // Blu-ray structure (any depth, any case, Windows separators).
    '/movies/Heat (1995)/BDMV/STREAM/00800.m2ts',
    '/movies/Heat (1995)/bdmv/stream/00800.M2TS',
    'D:\\Movies\\Heat (1995)\\BDMV\\STREAM\\00800.m2ts',
    '/movies/Heat (1995)/BDMV',
    '/movies/Heat (1995)/BDMV/index.bdmv',
    '/movies/Heat (1995)/BDMV/PLAYLIST/00800.mpls',
    '/movies/Heat (1995)/BDMV/CLIPINF/00800.clpi',
    '/movies/Heat (1995)/BDMV/STREAM/SSIF/00800.ssif',
    '/movies/Heat (1995)/CERTIFICATE/id.bdmv',
    '/movies/Heat (1995)/AACS/Unit_Key_RO.inf',
    '/movies/Heat (1995)/MAKEMKV/AACS/x',
    '/movies/Heat (1995)/Disc 1/BDMV/STREAM/00001.m2ts',
    // DVD: nested and flat.
    '/movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB',
    '/movies/Heat (1995)/VIDEO_TS.IFO',
    '/movies/Heat (1995)/VTS_01_1.VOB',
    '/movies/Heat (1995)/vts_02_0.bup',
    // HD DVD / AVCHD / BDAV.
    '/movies/Heat (1995)/HVDVD_TS/FEATURE_1.EVO',
    '/home/PRIVATE/AVCHD/BDMV/STREAM/00000.MTS',
    '/rec/BDAV/STREAM/00001.m2ts',
    // Loose disc clips (a flattened Blu-ray/DVD backup): a file of a disc wherever it lies.
    '/data/Movies/Elemental (2023)/00174.m2ts',
    '/movies/Heat (1995)/00800.m2ts',
    '/movies/Heat (1995)/00800.M2TS',
    '/movies/Heat (1995)/00001.mts',
    '/movies/Heat (1995)/00001.m2t',
    'D:\\Movies\\Elemental (2023)\\00175.m2ts',
    '00174.m2ts',
    '/movies/Heat (1995)/VIDEO_TS.VOB',
  ])('%s is a file of a disc', (p) => {
    expect(isDiscPath(p)).toBe(true);
  });

  it.each([
    '/movies/Heat (1995)/Heat.2160p.mkv',
    // A standalone .m2ts / .ts / .mts not named like a clip (tsMuxeR remux) is an ordinary file.
    '/movies/Heat (1995)/Heat.m2ts',
    '/movies/Heat (1995)/Heat.ts',
    '/movies/Heat (1995)/Heat.mts',
    // Only exactly five digits are a clip name.
    '/movies/Heat (1995)/0800.m2ts',
    '/movies/Heat (1995)/000800.m2ts',
    '/movies/Heat (1995)/Heat 00800.m2ts',
    '/movies/Heat (1995)/00800.mkv',
    '/movies/Heat (1995)/00800.m2ts.part',
    '/movies/00800 (2020)/Movie.mkv',
    // An ISO is one file (a disc version, but not a member of a disc structure).
    '/movies/Heat (1995)/Heat.iso',
    // Names that merely contain a disc word.
    '/movies/BDMV Collection/Heat.mkv',
    '/movies/Heat (1995)/Heat.BDMV.Remux.mkv',
    '/movies/Heat (1995)/video_ts_notes.txt',
    '',
  ])('%s is not', (p) => {
    expect(isDiscPath(p)).toBe(false);
  });

  it('tolerates null/undefined', () => {
    expect(isDiscPath(null)).toBe(false);
    expect(isDiscPath(undefined)).toBe(false);
  });
});

describe('disc versions', () => {
  const disc = makeFile({ id: 2, version: discVersion() });
  const clip = makeFile({
    id: 3,
    version: {
      parts: [
        {
          id: 3,
          path: '/data/movies/Heat (1995)/BDMV/STREAM/00800.m2ts',
          localPath: '/mnt/movies/Heat (1995)/BDMV/STREAM/00800.m2ts',
          size: 40 * GiB,
          duration: 1,
        },
      ],
    },
  });
  const localOnly = makeFile({
    id: 4,
    version: { parts: [{ id: 4, path: '/data/x/Heat.m2ts', localPath: '/mnt/x/VIDEO_TS/VTS_01_1.VOB', size: 1, duration: 1 }] },
  });

  it('recognises disc versions', () => {
    expect(isDiscVersion(disc.version)).toBe(true);
    expect(isDiscVersion(makeFile().version)).toBe(false);
    expect(isDiscVersion(undefined)).toBe(false);
  });

  it('finds a regular copy whose file lies inside a disc (server or local path)', () => {
    expect(discMemberPath(clip.version)).toBe('/data/movies/Heat (1995)/BDMV/STREAM/00800.m2ts');
    expect(discMemberPath(localOnly.version)).toBe('/mnt/x/VIDEO_TS/VTS_01_1.VOB');
    expect(discMemberPath(makeFile().version)).toBeNull();
    // A disc version is removed as a whole: its own paths are never "members".
    expect(discMemberPath(disc.version)).toBeNull();
  });

  it('describes what a disc removal moves', () => {
    expect(discRemovalNote({ type: 'uhd_bluray', fileCount: 312, discs: 1 })).toBe(
      'The whole disc folder (312 files) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'dvd', fileCount: 1, discs: 1 })).toBe(
      'The whole disc folder (1 file) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'bluray', fileCount: 1024, discs: 2 })).toBe(
      'Every disc folder of this 2-disc set (1,024 files) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'bluray', fileCount: 0, discs: 1 })).toBe(
      'The whole disc folder will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'iso', fileCount: 1, discs: 1 })).toBe('The ISO image will be moved to the recycle bin.');
  });

  it('shows part paths relative to the disc root', () => {
    expect(discRelativePath('/d/Heat (1995)', '/d/Heat (1995)/BDMV/STREAM/00800.m2ts')).toBe('BDMV/STREAM/00800.m2ts');
    expect(discRelativePath('/d/Heat (1995)/', '/d/Heat (1995)/BDMV/index.bdmv')).toBe('BDMV/index.bdmv');
    expect(discRelativePath('M:\\Heat', 'M:\\Heat\\BDMV\\STREAM\\00001.m2ts')).toBe('BDMV\\STREAM\\00001.m2ts');
    // Outside the root (or a sibling folder sharing its prefix): unchanged.
    expect(discRelativePath('/d/Heat', '/d/Heat 2/BDMV/x.m2ts')).toBe('/d/Heat 2/BDMV/x.m2ts');
    expect(discRelativePath('/d/Heat', '/d/Heat')).toBe('/d/Heat');
    expect(discRelativePath('', '/d/x.m2ts')).toBe('/d/x.m2ts');
  });

  it('spots list rows that remove a disc', () => {
    const f = (decision: 'keep' | 'remove', disc: boolean) => ({
      decision,
      disc: disc ? { type: 'bluray' as const, fileCount: 300, discs: 1 } : null,
    });
    expect(removesDisc([f('keep', false), f('remove', true)])).toBe(true);
    expect(removesDisc([f('keep', true), f('remove', false)])).toBe(false);
    expect(removesDisc([{ decision: 'remove' }])).toBe(false);
    expect(removesDisc(null)).toBe(false);
  });
});

describe('discApprovalBlocker', () => {
  const allowed: DiscSettings = { allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle', keepPlayableCopy: true };
  const mkv = (decision: GroupFile['decision'], id = 1) => makeFile({ id, decision });
  const disc = (decision: GroupFile['decision'], id = 2, over: Parameters<typeof discVersion>[0] = {}) =>
    makeFile({ id, decision, version: discVersion(over) });

  it('allows a whole-disc removal when disc removal is allowed and a recycle bin is set', () => {
    expect(discApprovalBlocker([mkv('keep'), disc('remove')], { settings: allowed, mediaType: 'movie' })).toBeNull();
  });

  it('ignores groups without discs, even with unknown settings', () => {
    expect(discApprovalBlocker([mkv('keep'), mkv('remove', 2)])).toBeNull();
  });

  it('allows keeping a disc next to the kept playable copy', () => {
    expect(discApprovalBlocker([mkv('keep'), disc('keep'), mkv('remove', 3)], { settings: allowed })).toBeNull();
  });

  it.each([
    ['disc removal is off', { ...allowed, allowDiscRemoval: false }, /Removing full discs is turned off/],
    ['no recycle bin is set', { ...allowed, recycleBinPath: '  ' }, /no recycle bin is set/],
    ['settings are unknown', null, /settings are not loaded/],
  ])('blocks a disc removal when %s', (_name, settings, message) => {
    expect(discApprovalBlocker([mkv('keep'), disc('remove')], { settings })).toMatch(message);
  });

  it('blocks a disc Dupearr cannot reach (no path mapping)', () => {
    expect(discApprovalBlocker([mkv('keep'), disc('remove', 2, { localRoot: '' })], { settings: allowed })).toMatch(
      /cannot reach “\/data\/movies\/Heat \(1995\)” \(no path mapping\)/,
    );
  });

  it('blocks a disc removal in a TV library (always protected in v1)', () => {
    expect(discApprovalBlocker([mkv('keep'), disc('remove')], { settings: allowed, mediaType: 'episode' })).toMatch(
      /TV libraries are always kept/,
    );
  });

  it('refuses a single file inside a disc structure, whatever the settings', () => {
    const clip = makeFile({
      id: 3,
      decision: 'remove',
      version: { parts: [{ id: 3, path: 'M:\\Heat (1995)\\BDMV\\STREAM\\00800.m2ts', localPath: '', size: 1, duration: 1 }] },
    });
    expect(discApprovalBlocker([mkv('keep'), clip], { settings: allowed })).toMatch(
      /inside a disc folder .* never removed one by one/,
    );
  });

  it('never leaves only discs when a playable copy is removed (keepPlayableCopy on or unknown)', () => {
    const files = [disc('keep'), mkv('remove', 3)];
    expect(discApprovalBlocker(files, { settings: allowed })).toMatch(/Every kept copy would be a full disc/);
    expect(discApprovalBlocker(files, {})).toMatch(/Every kept copy would be a full disc/);
    expect(discApprovalBlocker(files, { settings: { ...allowed, keepPlayableCopy: false } })).toBeNull();
  });

  it('never counts a kept loose clip (a group stored per clip) as the playable copy', () => {
    const clip = (id: number, clipNo: number) => makeFile({ id, decision: 'keep', version: clipVersion(clipNo) });
    const files = [clip(4, 174), clip(5, 175), mkv('remove', 3)];
    expect(discApprovalBlocker(files, { settings: allowed })).toMatch(/Every kept copy would be/);
    expect(discApprovalBlocker(files, { settings: { ...allowed, keepPlayableCopy: false } })).toBeNull();
  });

  it('allows removing one of two discs when no regular copy exists', () => {
    expect(discApprovalBlocker([disc('keep', 1), disc('remove', 2, { root: '/data/b' })], { settings: allowed })).toBeNull();
  });
});

describe('disc clip names (loose clips of a flattened backup)', () => {
  it.each([
    ['00174.m2ts', 'bluray'],
    ['00800.M2TS', 'bluray'],
    ['00000.mts', 'bluray'],
    ['99999.m2t', 'bluray'],
    ['VTS_01_1.VOB', 'dvd'],
    ['vts_12_0.vob', 'dvd'],
    ['VIDEO_TS.VOB', 'dvd'],
    ['VIDEO_TS.IFO', 'dvd'],
    ['video_ts.bup', 'dvd'],
    // Full paths are reduced to their base name (either separator).
    ['/data/Movies/Elemental (2023)/00174.m2ts', 'bluray'],
    ['M:\\Movies\\Elemental (2023)\\00175.m2ts', 'bluray'],
    ['/x/BDMV/STREAM/00800.m2ts', 'bluray'],
    // Copies a flattened backup keeps (the real library has "00004.1.m2ts") and the names a file
    // manager gives a clashing copy — the same rule as internal/disc (copyMarkers).
    ['00004.1.m2ts', 'bluray'],
    ['00800 (1).m2ts', 'bluray'],
    ['00800 - Copy (2).M2TS', 'bluray'],
    ['00800 2.m2ts', 'bluray'],
    ['00800 copy.mts', 'bluray'],
    ['00800_1.m2ts', 'bluray'],
    ['VTS_01_1 (2).VOB', 'dvd'],
    ['VIDEO_TS - Copy.IFO', 'dvd'],
  ] as const)('%s is a %s clip name', (name, kind) => {
    expect(isClipName(name)).toBe(true);
    expect(clipKind(name)).toBe(kind);
  });

  it.each([
    'Heat.m2ts',
    '0800.m2ts',
    '1917.m2ts',
    '000800.m2ts',
    '00800 (2019).m2ts',
    '00800.1234.m2ts',
    '00800 Movie.m2ts',
    'VTS_01_1 (2019).VOB',
    'Heat 00800.m2ts',
    '00800.mkv',
    '00800.ts',
    '00800.m2ts.part',
    'VTS_01_1.IFO', // a DVD file, but not a clip (still a disc file by isDiscPath)
    'VTS_1_1.VOB',
    'Movie.VOB',
    '/data/Movies/00174 (2020)/',
    '',
  ])('%s is not a clip name', (name) => {
    expect(isClipName(name)).toBe(false);
    expect(clipKind(name)).toBeNull();
  });

  it('tolerates null/undefined', () => {
    expect(isClipName(null)).toBe(false);
    expect(isClipName(undefined)).toBe(false);
    expect(isLooseClipPath(null)).toBe(false);
  });

  it('tells loose clips from clips inside a disc structure', () => {
    expect(isLooseClipPath('/data/Movies/Elemental (2023)/00174.m2ts')).toBe(true);
    expect(isLooseClipPath('D:\\Movies\\Elemental (2023)\\00174.M2TS')).toBe(true);
    expect(isLooseClipPath('/data/Movies/Heat (1995)/VTS_01_1.VOB')).toBe(true);
    expect(isLooseClipPath('00174.m2ts')).toBe(true);
    expect(isLooseClipPath('/data/Movies/Heat (1995)/BDMV/STREAM/00800.m2ts')).toBe(false);
    expect(isLooseClipPath('/data/Movies/Heat (1995)/VIDEO_TS/VTS_01_1.VOB')).toBe(false);
    expect(isLooseClipPath('/data/Movies/Heat (1995)/Heat.m2ts')).toBe(false);
    expect(isDiscPath('/data/Movies/Elemental (2023)/00800 (1).m2ts')).toBe(true);
    expect(isDiscPath('/data/Movies/The Sandlot (1993)/VTS_01_1 (2).VOB')).toBe(true);
    expect(isDiscPath('/data/Movies/The Sandlot (1993)/VTS_01_0 (2).IFO')).toBe(true);
    expect(isDiscPath('/data/Movies/1917 (2019)/1917.m2ts')).toBe(false);
  });

  it('takes the base name of a path', () => {
    expect(baseName('/a/b/00174.m2ts')).toBe('00174.m2ts');
    expect(baseName('C:\\a\\00174.m2ts')).toBe('00174.m2ts');
    expect(baseName('/a/b/')).toBe('b');
    expect(baseName('00174.m2ts')).toBe('00174.m2ts');
    expect(baseName(null)).toBe('');
  });
});

describe('loose clip sets', () => {
  const set = makeFile({ id: 5, version: clipSetVersion() });

  it('recognises the clip set kinds', () => {
    expect(isClipSetType('bluray_clips')).toBe(true);
    expect(isClipSetType('dvd_clips')).toBe(true);
    expect(isClipSetType('bluray')).toBe(false);
    expect(isClipSetType(undefined)).toBe(false);
    expect(isClipSet(set.version.disc)).toBe(true);
    expect(isClipSet(makeFile({ version: discVersion() }).version.disc)).toBe(false);
    expect(isClipSet(null)).toBe(false);
    // A clip set is a disc version (protected, removed only as a whole).
    expect(isDiscVersion(set.version)).toBe(true);
    // …so its own clips are never "members" to be refused one by one.
    expect(discMemberPath(set.version)).toBeNull();
    expect(overrideBlocker(set, 'remove')).toBeNull();
  });

  it('counts its clips: the server count, else the clip-named parts', () => {
    expect(clipCountOf(set.version)).toBe(3);
    expect(clipCountOf(makeFile({ version: clipSetVersion({ clipCount: 125 }) }).version)).toBe(125);
    expect(clipCountOf(makeFile({ version: clipSetVersion({ clipCount: undefined }) }).version)).toBe(3);
    expect(clipParts(set.version)).toHaveLength(3);
    expect(clipCountOf(makeFile({ version: discVersion() }).version)).toBe(0);
    expect(clipCountOf(makeFile().version)).toBe(0);
  });

  it('names the main clip: the longest clip part, or the main feature the server reports', () => {
    expect(mainClipName(set.version)).toBe('00174.m2ts');
    const tie = makeFile({
      version: clipSetVersion({}, { parts: [clipPart(1, { duration: 5, size: 1 }), clipPart(2, { duration: 5, size: 9 })] }),
    });
    expect(mainClipName(tie.version)).toBe('00002.m2ts');
    expect(mainClipName(makeFile({ version: clipSetVersion({ mainFeature: '00175.m2ts' }) }).version)).toBe('00175.m2ts');
    // A playlist main feature is not a clip: the longest clip is shown.
    expect(mainClipName(makeFile({ version: clipSetVersion({ mainFeature: '00800.mpls' }) }).version)).toBe('00174.m2ts');
    expect(mainClipName(makeFile({ version: discVersion() }).version)).toBe('');
    expect(mainClipName(makeFile().version)).toBe('');
  });

  it('describes what a clip set removal moves (every clip of the folder, nothing else)', () => {
    expect(discRemovalNote({ type: 'bluray_clips', fileCount: 125, discs: 1, clipCount: 125 })).toBe(
      'Every loose Blu-ray clip (00800.m2ts …) of this folder (125 clips) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'bluray_clips', fileCount: 128, discs: 1, clipCount: 125 })).toBe(
      'Every loose Blu-ray clip (00800.m2ts …) of this folder (125 clips · 128 files) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'dvd_clips', fileCount: 9, discs: 1 })).toBe(
      'Every loose DVD file (VIDEO_TS.*, VTS_*) of this folder (9 files) will be moved to the recycle bin.',
    );
    expect(discRemovalNote({ type: 'bluray_clips', fileCount: 0, discs: 1 })).toBe(
      'Every loose Blu-ray clip (00800.m2ts …) of this folder will be moved to the recycle bin.',
    );
    expect(discRemovalKeepsNote({ type: 'bluray_clips' })).toBe(CLIP_SET_REMOVAL_KEEPS_NOTE);
    expect(CLIP_SET_REMOVAL_KEEPS_NOTE).toMatch(/Other video files \(an MKV or MP4\), artwork, \.nfo and subtitles stay/);
    expect(discRemovalKeepsNote({ type: 'bluray' })).toBe(DISC_REMOVAL_KEEPS_NOTE);
  });
});

describe('discApprovalBlocker with loose clips', () => {
  const allowed: DiscSettings = { allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle', keepPlayableCopy: false };
  const clip = (n: number, decision: 'keep' | 'remove', part = {}) =>
    makeFile({ id: n, decision, version: clipVersion(n, part) });
  const mkv = (decision: 'keep' | 'remove', id = 1) => makeFile({ id, decision });

  it('refuses a stored per-clip group (Plex listed each loose clip as a copy), whatever the settings', () => {
    // The incident: 100+ "copies" of Elemental, all loose clips of one folder, all but one marked Remove.
    const files = [clip(174, 'keep'), ...Array.from({ length: 124 }, (_, i) => clip(175 + i, 'remove'))];
    for (const settings of [allowed, { ...allowed, keepPlayableCopy: true }, null]) {
      const msg = discApprovalBlocker(files, { settings, mediaType: 'movie' });
      expect(msg).toContain(`“${CLIP_FOLDER}/00175.m2ts” is one file of a Blu-ray stored as loose .m2ts clips`);
      expect(msg).toMatch(/clips are never removed one by one/);
      expect(msg).toMatch(/re-scan/);
    }
  });

  it('refuses a partial leftover clip next to an MKV (1–2 clips left after the incident)', () => {
    expect(discApprovalBlocker([mkv('keep'), clip(174, 'remove')], { settings: allowed })).toMatch(/never removed one by one/);
    expect(discApprovalBlocker([clip(174, 'keep'), mkv('remove')], { settings: allowed })).toBeNull();
  });

  it('refuses a clip by any path form: Windows separators, upper case, local path only, loose DVD VOBs', () => {
    const win = makeFile({ id: 3, decision: 'remove', version: { parts: [{ id: 3, path: 'M:\\Elemental\\00174.M2TS', localPath: '', size: 1, duration: 1 }] } });
    const local = makeFile({
      id: 4,
      decision: 'remove',
      version: { parts: [{ id: 4, path: '/data/x/Elemental.m2ts', localPath: '/mnt/x/00174.m2ts', size: 1, duration: 1 }] },
    });
    const vob = makeFile({ id: 5, decision: 'remove', version: { parts: [{ id: 5, path: '/m/Heat/VTS_01_1.VOB', localPath: '', size: 1, duration: 1 }] } });
    expect(discApprovalBlocker([mkv('keep'), win], { settings: allowed })).toMatch(/Blu-ray stored as loose \.m2ts clips/);
    expect(discApprovalBlocker([mkv('keep'), local], { settings: allowed })).toContain('“/mnt/x/00174.m2ts”');
    expect(discApprovalBlocker([mkv('keep'), vob], { settings: allowed })).toMatch(/a DVD stored as loose VOB files/);
  });

  it('allows a whole clip-set removal only when disc removal is allowed, with a recycle bin and a path mapping', () => {
    const set = (over = {}) => makeFile({ id: 9, decision: 'remove', version: clipSetVersion(over) });
    expect(discApprovalBlocker([mkv('keep'), set()], { settings: allowed, mediaType: 'movie' })).toBeNull();
    expect(discApprovalBlocker([mkv('keep'), set()], { settings: { ...allowed, allowDiscRemoval: false } })).toMatch(
      /Removing full discs is turned off/,
    );
    expect(discApprovalBlocker([mkv('keep'), set()], { settings: null })).toMatch(/settings are not loaded/);
    expect(discApprovalBlocker([mkv('keep'), set({ localRoot: '' })], { settings: allowed })).toMatch(
      /cannot move the clips to the recycle bin/,
    );
    expect(discApprovalBlocker([mkv('keep'), set()], { settings: allowed, mediaType: 'episode' })).toMatch(/TV libraries/);
  });

  it('never keeps only a clip set when a playable copy is removed (a feature can span clips)', () => {
    const files = [makeFile({ id: 9, decision: 'keep', version: clipSetVersion() }), mkv('remove')];
    expect(discApprovalBlocker(files, { settings: { ...allowed, keepPlayableCopy: true } })).toMatch(
      /Every kept copy would be a full disc or a set of loose disc clips, which Plex cannot reliably play/,
    );
    expect(discApprovalBlocker(files, {})).toMatch(/loose disc clips/);
    expect(discApprovalBlocker(files, { settings: allowed })).toBeNull();
  });
});

describe('per-copy override of a disc clip', () => {
  const loose = makeFile({ id: 3, version: clipVersion(174) });

  it('refuses Remove on a single clip (loose or inside BDMV) and allows Keep / Auto', () => {
    expect(overrideBlocker(loose, 'remove')).toMatch(/one file of a Blu-ray stored as loose \.m2ts clips/);
    expect(overrideBlocker(loose, 'keep')).toBeNull();
    expect(overrideBlocker(loose, 'auto')).toBeNull();
    const inside = makeFile({ version: { parts: [{ id: 1, path: '/m/Heat/BDMV/STREAM/00800.m2ts', localPath: '', size: 1, duration: 1 }] } });
    expect(overrideBlocker(inside, 'remove')).toMatch(/inside a disc folder/);
    expect(discMemberBlocker(makeFile().version)).toBeNull();
    expect(overrideBlocker(makeFile(), 'remove')).toBeNull();
  });
});

describe('list rows with a disc clip', () => {
  it('spots list rows that would remove a single disc clip (server flag discClip)', () => {
    expect(removesDiscClip([{ decision: 'keep' }, { decision: 'remove', discClip: true }])).toBe(true);
    expect(removesDiscClip([{ decision: 'keep', discClip: true }, { decision: 'remove' }])).toBe(false);
    expect(removesDiscClip([{ decision: 'remove', discClip: false }])).toBe(false);
    expect(removesDiscClip(undefined)).toBe(false);
  });
});
