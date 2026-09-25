/** Jellyfin copies in the duplicates UI (issue #4, docs/DECISIONS.md D12). */
import { render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DuplicateGroupDetail, GroupFile, MediaVersion } from '@/api/types';
import { GROUP_FLAG_DESCRIPTIONS, GROUP_FLAG_KIND, GROUP_FLAG_LABELS } from '@/lib/constants';
import DuplicateDetailPage from '@/pages/duplicates/DuplicateDetailPage';
import { approvalBlocker, expectedMethodNote } from './ApproveGroupDialog';
import { BulkActionDialog } from './BulkActionDialog';
import {
  canApproveFromList,
  hasJellyfinCopy,
  isJellyfinVersion,
  isManualOnly,
  reportOnlyReasons,
  summarizeBulk,
} from './duplicateUtils';
import { GROUP_FLAG_ICONS } from './FlagIcons';
import { GiB, LIBRARIES, makeFile, makeGroup, makeSettings, makeSummary, mockApi, renderPage } from './testing';

afterEach(() => {
  vi.unstubAllGlobals();
});

const SERVER_ID = '4a9f0000000000000000000000001201';

/** A Jellyfin copy: its version id is the media source id, and its file is mapped. */
function jfVersion(source: string, over: Partial<MediaVersion> = {}): Partial<MediaVersion> {
  return {
    key: `jellyfin:2:${source}`,
    serverId: 2,
    mediaId: 0,
    sourceId: source,
    ratingKey: SERVER_ID,
    parts: [
      {
        id: 0,
        itemId: source,
        path: `/media/movies/Heat (1995)/${source}.mkv`,
        localPath: `/mnt/movies/Heat (1995)/${source}.mkv`,
        size: 10 * GiB,
        duration: 10_200_000,
      },
    ],
    ...over,
  };
}

const KEEP = makeFile({ id: 21, rank: 1, decision: 'keep', engineDecision: 'keep', version: jfVersion('a'.repeat(32)) });
const LOSE = makeFile({
  id: 22,
  rank: 2,
  decision: 'remove',
  engineDecision: 'remove',
  version: jfVersion('b'.repeat(32), { resolution: '1080', height: 1080, width: 1920 }),
});

describe('Jellyfin helpers', () => {
  it('recognises Jellyfin copies by their version key', () => {
    expect(isJellyfinVersion(KEEP.version)).toBe(true);
    expect(isJellyfinVersion(makeFile().version)).toBe(false);
    expect(isJellyfinVersion(null)).toBe(false);
    expect(hasJellyfinCopy([makeFile(), LOSE])).toBe(true);
    expect(hasJellyfinCopy([makeFile()])).toBe(false);
  });

  it('collects report-only reasons once', () => {
    const strm = (id: number) =>
      makeFile({ id, version: jfVersion(String(id).padStart(32, '0'), { reportOnly: ['a .strm shortcut is never removed'] }) });
    expect(reportOnlyReasons([strm(1), strm(2), KEEP])).toEqual(['a .strm shortcut is never removed']);
    expect(reportOnlyReasons([KEEP])).toEqual([]);
  });

  it('labels the Jellyfin flags', () => {
    for (const flag of ['manual_only', 'report_only'] as const) {
      expect(GROUP_FLAG_LABELS[flag]).toBeTruthy();
      expect(GROUP_FLAG_DESCRIPTIONS[flag]).toBeTruthy();
      expect(GROUP_FLAG_KIND[flag]).toBeTruthy();
      expect(GROUP_FLAG_ICONS[flag]).toBeTruthy();
    }
    expect(GROUP_FLAG_LABELS.manual_only).toBe('Manual approval only');
    expect(GROUP_FLAG_LABELS.report_only).toBe('Report only');
  });
});

describe('list approvals', () => {
  const jf = makeSummary({ id: 8, removeCount: 1, reclaimableBytes: GiB, flags: ['manual_only'] });
  const plex = makeSummary({ id: 1, removeCount: 1, reclaimableBytes: GiB });

  it('never approves a manual-only group from the list', () => {
    expect(isManualOnly(['manual_only'])).toBe(true);
    expect(isManualOnly(null)).toBe(false);
    expect(canApproveFromList('pending', 1, null, ['manual_only'])).toBe(false);
    expect(canApproveFromList('pending', 1, null, ['stacked'])).toBe(true);
    const s = summarizeBulk('approve', [plex, jf]);
    expect(s.eligible.map((g) => g.id)).toEqual([1]);
    expect(s.manualSkipped).toBe(1);
    // Ignoring is fine from the list.
    expect(summarizeBulk('ignore', [jf]).eligible.map((g) => g.id)).toEqual([8]);
    expect(summarizeBulk('ignore', [jf]).manualSkipped).toBe(0);
  });

  it('explains the skipped Jellyfin group in the bulk dialog', () => {
    render(<BulkActionDialog open action="approve" groups={[plex, jf]} dryRun={false} onConfirm={vi.fn()} onCancel={vi.fn()} />);
    expect(screen.getByTestId('bulk-skipped')).toHaveTextContent(
      '1 group has a copy on a Jellyfin server and must be approved one at a time from the detail page.',
    );
  });
});

describe('approval of a Jellyfin duplicate', () => {
  it('expects a recycle bin and never Jellyfin itself', () => {
    const arr = {
      ...LOSE,
      version: { ...LOSE.version, arr: { ...(makeFile().version.arr ?? {}), instanceName: 'Radarr' } as MediaVersion['arr'] },
    } as GroupFile;
    expect(expectedMethodNote(arr, ['arr', 'plex', 'filesystem'])).toBe(
      'Expected via Radarr — only when Radarr has a recycle bin configured (a Jellyfin copy is never deleted permanently).',
    );
    expect(expectedMethodNote(LOSE, ['plex', 'filesystem'])).toBe(
      'Expected via the filesystem — moved to Dupearr’s recycle bin (refused when none is configured).',
    );
    expect(expectedMethodNote(LOSE, ['plex'])).toMatch(/never deletes through Jellyfin/);
  });

  it('blocks report-only and unmapped copies', () => {
    expect(approvalBlocker([KEEP, LOSE], 'pending')).toBeNull();
    const shortcut = makeFile({ ...KEEP, version: { ...KEEP.version, reportOnly: ['it is a .strm shortcut'] } });
    expect(approvalBlocker([shortcut, LOSE], 'pending')).toBe('This duplicate is only reported: it is a .strm shortcut.');
    const unmapped = makeFile({
      ...KEEP,
      version: { ...KEEP.version, parts: [{ ...KEEP.version.parts[0]!, localPath: '' }] },
    });
    expect(approvalBlocker([unmapped, LOSE], 'pending')).toMatch(/^No path mapping covers \/media\/movies\/Heat \(1995\)\/a+\.mkv/);
    const gone = makeFile({ ...KEEP, version: { ...KEEP.version, parts: [{ ...KEEP.version.parts[0]!, exists: false }] } });
    expect(approvalBlocker([gone, LOSE], 'pending')).toBe('Every kept copy is missing. Re-scan the group before approving.');
  });
});

describe('DuplicateDetailPage with Jellyfin copies', () => {
  const routes = (group: DuplicateGroupDetail) => [
    { path: '/duplicate/1', respond: group },
    { path: '/library', respond: LIBRARIES },
    { path: '/config/settings', respond: makeSettings({ dryRun: false }) },
    { path: '/profile/1', respond: { id: 1, name: 'Default', isDefault: true, criteria: [], keepCount: 1, keepPer: '', protections: [], createdAt: '', updatedAt: '' } },
  ];

  it('shows the server kind, the read-only note and the report-only reasons', async () => {
    const shortcut = makeFile({
      id: 23,
      rank: 3,
      protected: true,
      protectedReason: 'Report only: it is a .strm shortcut',
      version: jfVersion('c'.repeat(32), { reportOnly: ['it is a .strm shortcut'] }),
    });
    const group = makeGroup({
      serverId: 2,
      externalIds: { tmdb: '949' },
      status: 'protected',
      statusReason: 'Report only: it is a .strm shortcut',
      flags: ['manual_only', 'report_only'],
      files: [KEEP, { ...LOSE, decision: 'keep' }, shortcut],
    });
    mockApi(routes(group));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    expect(screen.getByTitle('A copy is listed by a Jellyfin server (read-only)')).toHaveTextContent('Jellyfin');
    const note = screen.getByText('Listed by a Jellyfin server').closest('[role="status"]') as HTMLElement;
    expect(note).toHaveTextContent(/Dupearr never deletes through Jellyfin/);
    const alert = screen
      .getAllByRole('alert')
      .find((el) => el.textContent?.startsWith('Report only')) as HTMLElement;
    expect(alert).toHaveTextContent('Dupearr only reports this duplicate');
    expect(within(alert).getByRole('listitem')).toHaveTextContent('it is a .strm shortcut');
    const flags = screen.getByRole('list', { name: 'Flags' });
    expect(within(flags).getByText('Manual approval only')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
  });

  it('shows no Jellyfin note for a Plex duplicate', async () => {
    mockApi(routes(makeGroup({ files: [makeFile(), makeFile({ id: 12, rank: 2, decision: 'remove' })] })));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });
    expect(screen.queryByText('Listed by a Jellyfin server')).not.toBeInTheDocument();
    expect(screen.queryByTitle('A copy is listed by a Jellyfin server (read-only)')).not.toBeInTheDocument();
  });
});
