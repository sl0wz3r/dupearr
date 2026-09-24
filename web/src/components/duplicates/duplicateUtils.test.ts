import { describe, expect, it } from 'vitest';
import { approvalBlocker, expectedMethodNote } from './ApproveGroupDialog';
import { canRestore } from './GroupActionsList';
import {
  approvalSignatures,
  bestAudioTrack,
  canApprove,
  canApproveFromList,
  canIgnore,
  canUnignore,
  deletionCaveat,
  deletionOrderText,
  externalIdLinks,
  filesFingerprint,
  groupTitle,
  hasMissingPart,
  isHardlinked,
  requiresTypedConfirmation,
  sortByRank,
  subtitleLanguages,
  summarizeBulk,
  summaryFileLabel,
  summaryFingerprint,
  versionSize,
} from './duplicateUtils';
import { GiB, clipSetVersion, clipVersion, discVersion, makeFile, makeSummary } from './testing';

describe('groupTitle', () => {
  it.each([
    [{ mediaType: 'movie' as const, title: 'Heat', year: 1995 }, 'Heat (1995)'],
    [{ mediaType: 'movie' as const, title: 'Heat', year: 0 }, 'Heat'],
    [{ mediaType: 'movie' as const, title: '  ', year: 0 }, 'Untitled'],
    [{ mediaType: 'episode' as const, title: 'Pilot', showTitle: 'Show', season: 1, episode: 2 }, 'Show – S01E02 – Pilot'],
    [{ mediaType: 'episode' as const, title: '', showTitle: 'Show', season: 10, episode: 102 }, 'Show – S10E102'],
    [{ mediaType: 'episode' as const, title: '' }, 'Untitled episode'],
    // Unknown season (Plex reports a hidden season as -1): never "S-1".
    [{ mediaType: 'episode' as const, title: 'Pilot', showTitle: 'Show', season: -1, episode: 2 }, 'Show – S??E02 – Pilot'],
    // Season 0 (specials) is omitted from the JSON (omitempty) but still has a code.
    [{ mediaType: 'episode' as const, title: 'Special', showTitle: 'Show', episode: 3 }, 'Show – S00E03 – Special'],
  ])('%j → %s', (item, expected) => {
    expect(groupTitle(item)).toBe(expected);
  });
});

describe('status eligibility', () => {
  it('only approves groups with removals in approvable statuses', () => {
    expect(canApprove('pending', 1)).toBe(true);
    expect(canApprove('review', 1)).toBe(true);
    expect(canApprove('failed', 2)).toBe(true);
    expect(canApprove('pending', 0)).toBe(false);
    for (const s of ['deferred', 'protected', 'queued', 'resolved', 'ignored'] as const) expect(canApprove(s, 3)).toBe(false);
  });

  it('never approves review groups from the list', () => {
    expect(canApproveFromList('pending', 1)).toBe(true);
    expect(canApproveFromList('failed', 1)).toBe(true);
    expect(canApproveFromList('review', 1)).toBe(false);
    expect(canApproveFromList('pending', 0)).toBe(false);
    for (const s of ['deferred', 'protected', 'queued', 'resolved', 'ignored'] as const) expect(canApproveFromList(s, 3)).toBe(false);
  });

  it('never approves a group that removes a full disc from the list', () => {
    const discRemoved = [
      { decision: 'keep' as const, disc: null },
      { decision: 'remove' as const, disc: { type: 'bluray' as const, fileCount: 300, discs: 1 } },
    ];
    const discKept = [
      { decision: 'keep' as const, disc: { type: 'bluray' as const, fileCount: 300, discs: 1 } },
      { decision: 'remove' as const, disc: null },
    ];
    expect(canApproveFromList('pending', 1, discRemoved)).toBe(false);
    expect(canApproveFromList('pending', 1, discKept)).toBe(true);
    expect(canApproveFromList('pending', 1, null)).toBe(true);
    // The detail page (where the disc folder is shown) still approves it.
    expect(canApprove('pending', 1)).toBe(true);
  });

  it('never approves from the list a group that would remove a single disc clip (server flag discClip)', () => {
    const clipRemoved = [
      { decision: 'keep' as const, disc: null },
      { decision: 'remove' as const, disc: null, discClip: true },
    ];
    const clipKept = [
      { decision: 'keep' as const, disc: null, discClip: true },
      { decision: 'remove' as const, disc: null },
    ];
    expect(canApproveFromList('pending', 1, clipRemoved)).toBe(false);
    expect(canApproveFromList('failed', 1, clipRemoved)).toBe(false);
    expect(canApproveFromList('pending', 1, clipKept)).toBe(true);
  });

  it('ignores / unignores by status', () => {
    expect(canIgnore('pending')).toBe(true);
    expect(canIgnore('ignored')).toBe(false);
    expect(canIgnore('queued')).toBe(false);
    expect(canUnignore('ignored')).toBe(true);
    expect(canUnignore('pending')).toBe(false);
  });
});

describe('summarizeBulk', () => {
  const pending = makeSummary({ id: 1, removeCount: 1, reclaimableBytes: 10 * GiB });
  const failed = makeSummary({ id: 2, status: 'failed', removeCount: 2, reclaimableBytes: 5 * GiB });
  const review = makeSummary({ id: 3, status: 'review', removeCount: 1 });
  const ignored = makeSummary({ id: 4, status: 'ignored', removeCount: 1, fileCount: 3 });
  const nothing = makeSummary({ id: 5, removeCount: 0 });

  it('approve: skips review groups and groups without removals', () => {
    const s = summarizeBulk('approve', [pending, failed, review, ignored, nothing]);
    expect(s.eligible.map((g) => g.id)).toEqual([1, 2]);
    expect(s.skipped.map((g) => g.id)).toEqual([3, 4, 5]);
    expect(s.reviewSkipped).toBe(1);
    expect(s.files).toBe(3);
    expect(s.bytes).toBe(15 * GiB);
  });

  it('approve: a review group is never eligible from the list, even on its own', () => {
    const s = summarizeBulk('approve', [review]);
    expect(s.eligible).toHaveLength(0);
    expect(s.reviewSkipped).toBe(1);
  });

  it('approve: skips groups that remove a full disc and counts them', () => {
    const disc = makeSummary({
      id: 6,
      files: [
        { id: 61, decision: 'keep', resolution: '2160', dynamicRange: 'sdr', videoCodec: 'hevc', size: GiB, libraryTitle: 'Movies' },
        {
          id: 62,
          decision: 'remove',
          resolution: '1080',
          dynamicRange: 'sdr',
          videoCodec: 'h264',
          size: 40 * GiB,
          libraryTitle: 'Movies',
          disc: { type: 'bluray', fileCount: 280, discs: 1 },
        },
      ],
    });
    const s = summarizeBulk('approve', [pending, disc, review]);
    expect(s.eligible.map((g) => g.id)).toEqual([1]);
    expect(s.discSkipped).toBe(1);
    expect(s.reviewSkipped).toBe(1);
    // Ignoring a disc group is fine from the list.
    expect(summarizeBulk('ignore', [disc]).eligible.map((g) => g.id)).toEqual([6]);
    expect(summarizeBulk('ignore', [disc]).discSkipped).toBe(0);
  });

  it('approve: skips groups that would remove a single disc clip and counts them apart', () => {
    const clip = (id: number, decision: 'keep' | 'remove') => ({
      id,
      decision,
      resolution: '1080' as const,
      dynamicRange: 'sdr' as const,
      videoCodec: 'h264' as const,
      size: GiB,
      libraryTitle: 'Movies',
      discClip: true,
    });
    const clips = makeSummary({ id: 7, removeCount: 124, fileCount: 125, files: [clip(71, 'keep'), clip(72, 'remove'), clip(73, 'remove')] });
    const s = summarizeBulk('approve', [pending, clips]);
    expect(s.eligible.map((g) => g.id)).toEqual([1]);
    expect(s.clipSkipped).toBe(1);
    expect(s.discSkipped).toBe(0);
    expect(s.files).toBe(1);
    expect(summarizeBulk('ignore', [clips]).eligible.map((g) => g.id)).toEqual([7]);
    expect(summarizeBulk('ignore', [clips]).clipSkipped).toBe(0);
  });

  it('ignore / unignore', () => {
    expect(summarizeBulk('ignore', [pending, ignored]).eligible.map((g) => g.id)).toEqual([1]);
    const un = summarizeBulk('unignore', [pending, ignored]);
    expect(un.eligible.map((g) => g.id)).toEqual([4]);
    expect(un.files).toBe(3);
  });

  it('tolerates junk numbers', () => {
    const junk = makeSummary({ id: 9, removeCount: 1, reclaimableBytes: -5 });
    expect(summarizeBulk('approve', [junk]).bytes).toBe(0);
  });
});

describe('typed confirmation', () => {
  it.each([
    ['approve', 11, false, true],
    ['approve', 10, false, false],
    ['approve', 50, true, false],
    ['ignore', 50, false, false],
  ] as const)('%s × %d (dryRun=%s) → %s', (action, n, dryRun, expected) => {
    expect(requiresTypedConfirmation(action, n, dryRun)).toBe(expected);
  });
});

describe('deletion caveat', () => {
  it('uses the configured order, falling back to the default', () => {
    expect(deletionOrderText(['plex', 'arr'])).toBe('Plex → Radarr / Sonarr');
    expect(deletionOrderText([])).toBe('Radarr / Sonarr → Plex → Filesystem');
    expect(deletionCaveat(undefined)).toBe(
      'Deletion method is chosen at execution (order: Radarr / Sonarr → Plex → Filesystem); deletions through Plex or without a recycle bin are permanent.',
    );
  });

  it('describes the expected method of a copy', () => {
    const tracked = makeFile({ version: { arr: { instanceName: 'Radarr 4K' } as never } });
    const untracked = makeFile({ version: { arr: null } });
    const local = makeFile({
      version: { arr: null, parts: [{ id: 1, path: '/a', localPath: '/mnt/a', size: 1, duration: 1 }] },
    });
    expect(expectedMethodNote(tracked, ['arr', 'plex'])).toMatch(/via Radarr 4K — permanent unless/);
    expect(expectedMethodNote(untracked, ['arr', 'plex'])).toMatch(/via Plex — permanent/);
    expect(expectedMethodNote(local, ['filesystem', 'plex'])).toMatch(/filesystem/);
    expect(expectedMethodNote(untracked, ['arr', 'filesystem'])).toMatch(/may fail/);
  });

  it('always expects a disc to go to the recycle bin through the filesystem', () => {
    const disc = makeFile({ version: discVersion() });
    expect(expectedMethodNote(disc, ['arr', 'plex', 'filesystem'])).toMatch(
      /^Expected via the filesystem — the disc is moved to Dupearr’s recycle bin \(never through Plex or Radarr\/Sonarr\)\.$/,
    );
    expect(expectedMethodNote(disc, ['arr', 'plex'])).toMatch(/not one of your deletion methods, so the removal may be refused/);
  });

  it('expects a loose clip set to be moved as a whole to the recycle bin through the filesystem', () => {
    const set = makeFile({ version: clipSetVersion() });
    expect(expectedMethodNote(set, ['arr', 'plex', 'filesystem'])).toBe(
      'Expected via the filesystem — the loose clips are moved to Dupearr’s recycle bin (never through Plex or Radarr/Sonarr).',
    );
  });
});

describe('approvalBlocker', () => {
  const keep = makeFile({ id: 1, decision: 'keep' });
  const remove = makeFile({
    id: 2,
    decision: 'remove',
    version: { parts: [{ id: 2, path: '/b.mkv', localPath: '', size: 1, duration: 1 }] },
  });

  it('allows a normal group', () => {
    expect(approvalBlocker([keep, remove])).toBeNull();
  });

  it.each([
    ['nothing to remove', [keep], /Nothing to remove/],
    ['no keeper', [{ ...keep, decision: 'remove' as const }, remove], /No copy would be kept/],
    [
      'every keeper missing',
      [makeFile({ id: 1, decision: 'keep', version: { parts: [{ id: 1, path: '/a', localPath: '', size: 1, duration: 1, exists: false }] } }), remove],
      /missing/,
    ],
    [
      'shared path',
      [makeFile({ id: 1, decision: 'keep', version: { parts: [{ id: 1, path: '/b.mkv', localPath: '', size: 1, duration: 1 }] } }), remove],
      /shares a file path/,
    ],
  ])('blocks when %s', (_name, files, message) => {
    expect(approvalBlocker(files)).toMatch(message);
  });

  it.each([
    ['a protected copy is marked for removal', [keep, { ...remove, protected: true }], undefined, /protected copy/],
    [
      'an optimized version is marked for removal',
      [keep, makeFile({ ...remove, version: { ...remove.version, optimizedVersion: true } })],
      undefined,
      /optimized version/,
    ],
    ['the group is queued', [keep, remove], 'queued' as const, /can't be approved/],
    ['the group is deferred', [keep, remove], 'deferred' as const, /can't be approved/],
    ['the group is resolved', [keep, remove], 'resolved' as const, /can't be approved/],
  ])('blocks when %s (defense in depth)', (_name, files, status, message) => {
    expect(approvalBlocker(files, status)).toMatch(message);
  });

  it.each(['pending', 'review', 'failed'] as const)('allows a %s group', (status) => {
    expect(approvalBlocker([keep, remove], status)).toBeNull();
  });

  describe('full discs', () => {
    const disc = makeFile({ id: 3, decision: 'remove', version: discVersion() });
    const allowed = { allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle', keepPlayableCopy: true };

    it('allows a manual whole-disc removal when allowed', () => {
      expect(approvalBlocker([keep, disc], 'pending', { settings: allowed, mediaType: 'movie' })).toBeNull();
    });

    it('blocks a disc removal without the settings that allow it', () => {
      expect(approvalBlocker([keep, disc], 'pending')).toMatch(/settings are not loaded/);
      expect(approvalBlocker([keep, disc], 'pending', { settings: { ...allowed, allowDiscRemoval: false } })).toMatch(
        /Removing full discs is turned off/,
      );
      expect(approvalBlocker([keep, disc], 'pending', { settings: { ...allowed, recycleBinPath: '' } })).toMatch(
        /only be moved to the recycle bin/,
      );
    });

    it('blocks removing a single file inside a disc by any method', () => {
      const clip = makeFile({
        id: 4,
        decision: 'remove',
        version: { parts: [{ id: 4, path: '/m/Heat/BDMV/STREAM/00800.m2ts', localPath: '', size: 1, duration: 1 }] },
      });
      expect(approvalBlocker([keep, clip], 'pending', { settings: allowed })).toMatch(/never removed one by one/);
    });

    it('blocks removing a loose clip (a flattened Blu-ray) by any method, even with disc removal allowed', () => {
      const clip = makeFile({ id: 4, decision: 'remove', version: clipVersion(175) });
      expect(approvalBlocker([keep, clip], 'pending', { settings: allowed, mediaType: 'movie' })).toMatch(
        /one file of a Blu-ray stored as loose \.m2ts clips\. A movie can span several clips, so clips are never removed one by one/,
      );
      expect(approvalBlocker([keep, clip], 'review', { settings: { ...allowed, keepPlayableCopy: false } })).toMatch(
        /never removed one by one/,
      );
    });

    it('status and generic checks come first', () => {
      expect(approvalBlocker([keep, disc], 'queued', { settings: allowed })).toMatch(/can't be approved/);
      expect(approvalBlocker([keep, { ...disc, protected: true }], 'pending', { settings: allowed })).toMatch(/protected copy/);
    });
  });
});

describe('fingerprints', () => {
  it('summaryFingerprint changes with status, decisions and bytes only', () => {
    const g = makeSummary();
    expect(summaryFingerprint([g])).toBe(summaryFingerprint([makeSummary()]));
    expect(summaryFingerprint([g])).not.toBe(summaryFingerprint([{ ...g, status: 'review' }]));
    expect(summaryFingerprint([g])).not.toBe(summaryFingerprint([{ ...g, reclaimableBytes: 1 }]));
    const swapped = { ...g, files: g.files.map((f) => ({ ...f, decision: f.decision === 'keep' ? ('remove' as const) : ('keep' as const) })) };
    expect(summaryFingerprint([g])).not.toBe(summaryFingerprint([swapped]));
    expect(summaryFingerprint([g])).toBe(summaryFingerprint([{ ...g, lastSeenAt: '2030-01-01T00:00:00Z' }]));
    // A new server signature (versions/decisions changed) is a change even if nothing shown differs.
    expect(summaryFingerprint([g])).not.toBe(summaryFingerprint([{ ...g, signature: 'other' }]));
    expect(summaryFingerprint([])).toBe('');
  });

  it('approvalSignatures maps ids to signatures, skipping groups without one', () => {
    expect(
      approvalSignatures([makeSummary({ id: 1, signature: 's1' }), makeSummary({ id: 2, signature: '' }), makeSummary({ id: 3 })]),
    ).toEqual({ 1: 's1', 3: 'sig-3' });
    expect(approvalSignatures([])).toEqual({});
  });

  it('filesFingerprint changes with decisions, protection and paths', () => {
    const a = makeFile({ id: 1, decision: 'keep' });
    const b = makeFile({ id: 2, decision: 'remove', version: { parts: [{ id: 2, path: '/b.mkv', localPath: '', size: 5, duration: 1 }] } });
    const base = filesFingerprint('pending', [a, b]);
    expect(filesFingerprint('pending', [a, b])).toBe(base);
    expect(filesFingerprint('review', [a, b])).not.toBe(base);
    expect(filesFingerprint('pending', [{ ...a, decision: 'remove' }, { ...b, decision: 'keep' }])).not.toBe(base);
    expect(filesFingerprint('pending', [a, { ...b, protected: true }])).not.toBe(base);
    const moved = makeFile({ ...b, version: { parts: [{ id: 2, path: '/c.mkv', localPath: '', size: 5, duration: 1 }] } });
    expect(filesFingerprint('pending', [a, moved])).not.toBe(base);
  });

  it('filesFingerprint changes when a disc changes', () => {
    const a = makeFile({ id: 1, decision: 'keep' });
    const disc = makeFile({ id: 2, decision: 'remove', version: discVersion() });
    const base = filesFingerprint('pending', [a, disc]);
    const grown = makeFile({ id: 2, decision: 'remove', version: discVersion({ fileCount: 400 }) });
    const unmapped = makeFile({ id: 2, decision: 'remove', version: discVersion({ localRoot: '' }) });
    expect(filesFingerprint('pending', [a, grown])).not.toBe(base);
    expect(filesFingerprint('pending', [a, unmapped])).not.toBe(base);
  });

  it('summaryFingerprint changes when a file becomes a disc', () => {
    const g = makeSummary();
    const discs = { ...g, files: g.files.map((f) => ({ ...f, disc: { type: 'bluray' as const, fileCount: 3, discs: 1 } })) };
    expect(summaryFingerprint([g])).not.toBe(summaryFingerprint([discs]));
  });
});

describe('externalIdLinks', () => {
  it('builds links for valid ids only', () => {
    const links = externalIdLinks({ imdb: 'tt0113277', tmdb: '949', tvdb: '123', plex: 'plex://x' }, 'movie');
    expect(links).toEqual([
      { key: 'imdb', label: 'IMDb', id: 'tt0113277', url: 'https://www.imdb.com/title/tt0113277/' },
      { key: 'tmdb', label: 'TMDb', id: '949', url: 'https://www.themoviedb.org/movie/949' },
      { key: 'tvdb', label: 'TVDB', id: '123', url: 'https://www.thetvdb.com/dereferrer/movie/123' },
    ]);
  });

  it('never builds URLs from malformed ids', () => {
    const links = externalIdLinks({ imdb: 'javascript:alert(1)', tmdb: '1/../../x', tvdb: '' }, 'movie');
    expect(links).toEqual([
      { key: 'imdb', label: 'IMDb', id: 'javascript:alert(1)' },
      { key: 'tmdb', label: 'TMDb', id: '1/../../x' },
    ]);
  });

  it('has no TMDb page for an episode id', () => {
    const links = externalIdLinks({ tmdb: '62085', tvdb: '349232' }, 'episode');
    expect(links[0]!.url).toBeUndefined();
    expect(links[1]!.url).toBe('https://www.thetvdb.com/dereferrer/episode/349232');
    expect(externalIdLinks(null, 'movie')).toEqual([]);
  });
});

describe('version helpers', () => {
  it('sizes, hardlinks, missing parts', () => {
    const v = makeFile({
      version: {
        parts: [
          { id: 1, path: '/a', localPath: '', size: 5, duration: 1, linkCount: 1 },
          { id: 2, path: '/b', localPath: '', size: 7, duration: 1, linkCount: 3, exists: false },
        ],
      },
    }).version;
    expect(versionSize(v)).toBe(12);
    expect(isHardlinked(v)).toBe(true);
    expect(hasMissingPart(v)).toBe(true);
    expect(versionSize(undefined)).toBe(0);
  });

  it('picks the best audio track and subtitle languages', () => {
    const v = makeFile().version;
    expect(bestAudioTrack(v.audioTracks)?.format).toBe('truehd_atmos');
    expect(bestAudioTrack([])).toBeNull();
    expect(subtitleLanguages([{ codec: '', language: '', languageCode: '', forced: false, external: false }, ...(v.subtitleTracks ?? [])])).toEqual(['Unknown', 'English']);
  });

  it('labels summary files and sorts by rank', () => {
    expect(summaryFileLabel({ resolution: '2160', dynamicRange: 'dv_hdr10' })).toBe('4K DV HDR10');
    expect(summaryFileLabel({ resolution: '1080', dynamicRange: 'sdr' })).toBe('1080p');
    expect(summaryFileLabel({ resolution: '' as never, dynamicRange: 'hdr10' })).toBe('? HDR10');
    // Full discs lead with their kind; an ISO's quality is unknown.
    expect(
      summaryFileLabel({ resolution: '2160', dynamicRange: 'dv_hdr10', disc: { type: 'uhd_bluray', fileCount: 312, discs: 1 } }),
    ).toBe('UHD BD · 4K DV HDR10');
    expect(summaryFileLabel({ resolution: '1080', dynamicRange: 'sdr', disc: { type: 'bluray', fileCount: 200, discs: 1 } })).toBe(
      'BD · 1080p',
    );
    expect(summaryFileLabel({ resolution: '' as never, dynamicRange: 'sdr', disc: { type: 'iso', fileCount: 1, discs: 1 } })).toBe(
      'ISO',
    );
    const files = [makeFile({ id: 3, rank: 2 }), makeFile({ id: 1, rank: 0 }), makeFile({ id: 2, rank: 1 })];
    expect(sortByRank(files).map((f) => f.id)).toEqual([2, 3, 1]);
  });

  it('only restores succeeded, recycled, real removals', () => {
    const base = { status: 'succeeded', dryRun: false, recyclePath: '/r/x' } as const;
    expect(canRestore(base as never)).toBe(true);
    expect(canRestore({ ...base, dryRun: true } as never)).toBe(false);
    expect(canRestore({ ...base, recyclePath: '' } as never)).toBe(false);
    expect(canRestore({ ...base, status: 'failed' } as never)).toBe(false);
    expect(canRestore({ ...base, method: 'filesystem' } as never)).toBe(true);
    expect(canRestore({ ...base, method: 'arr' } as never)).toBe(false);
    expect(canRestore({ ...base, method: 'plex' } as never)).toBe(false);
    expect(canRestore({ ...base, permanent: true } as never)).toBe(false);
  });
});
