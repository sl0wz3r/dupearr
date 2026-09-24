import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { queryKeys } from '@/api/queryKeys';
import type { DuplicateGroupDetail, GroupFile, MediaPart, Settings } from '@/api/types';
import {
  CLIP_FOLDER,
  GiB,
  LIBRARIES,
  clipSetVersion,
  clipVersion,
  discVersion,
  json,
  makeFile,
  makeGroup,
  makeSettings,
  mockApi,
  renderPage,
  type MockRoute,
} from '@/components/duplicates/testing';
import DuplicateDetailPage from './DuplicateDetailPage';

afterEach(() => {
  vi.unstubAllGlobals();
});

const KEEPER = makeFile({
  id: 11,
  rank: 1,
  decision: 'keep',
  engineDecision: 'keep',
  reasons: ['Best copy'],
  values: { resolution: '2160p' },
  version: {
    arr: {
      instanceId: 1,
      instanceName: 'Radarr 4K',
      kind: 'radarr',
      fileId: 5,
      itemId: 7,
      episodeIds: null,
      itemPath: '/data/movies/Heat (1995)',
      monitored: true,
      qualityName: 'Remux-2160p',
      qualitySource: 'bluray',
      qualityResolution: 2160,
      qualityModifier: 'remux',
      customFormats: [],
      customFormatScore: 1500,
      releaseGroup: 'FraMeSToR',
      edition: '',
      languages: ['English'],
      dynamicRangeType: 'DV HDR10',
      tags: [],
      qualityCutoffNotMet: false,
      sceneName: '',
      dateAdded: '2026-01-01T00:00:00Z',
    },
  },
});

const PROTECTED = makeFile({
  id: 13,
  rank: 2,
  decision: 'keep',
  engineDecision: 'keep',
  protected: true,
  protectedReason: 'Path matches /data/keep/**',
  version: {
    key: 'plex:1:102',
    mediaId: 102,
    resolution: '720',
    width: 1280,
    height: 720,
    dynamicRange: 'sdr',
    videoCodec: 'h264',
    parts: [{ id: 1002, path: '/data/keep/Heat.720p.mkv', localPath: '', size: 4 * GiB, duration: 10_200_000 }],
  },
});

const LOSER = makeFile({
  id: 12,
  rank: 3,
  decision: 'remove',
  engineDecision: 'remove',
  decidingCriterion: 'resolution',
  reasons: ['Resolution 2160p > 1080p'],
  values: { resolution: '1080p' },
  version: {
    key: 'plex:1:101',
    mediaId: 101,
    resolution: '1080',
    width: 1920,
    height: 800,
    dynamicRange: 'sdr',
    videoCodec: 'h264',
    parts: [
      {
        id: 1001,
        path: '/data/movies/Heat (1995)/Heat.1080p.mkv',
        localPath: '/mnt/movies/Heat (1995)/Heat.1080p.mkv',
        size: 10 * GiB,
        duration: 10_200_000,
        linkCount: 2,
        exists: true,
      },
    ],
  },
});

// Deliberately out of rank order: the page must sort by rank.
const GROUP: DuplicateGroupDetail = makeGroup({ files: [LOSER, KEEPER, PROTECTED] });

function routes(opts: { group?: DuplicateGroupDetail; dryRun?: boolean; settings?: Partial<Settings> } = {}): MockRoute[] {
  return [
    { path: '/duplicate/1', respond: opts.group ?? GROUP },
    { path: '/library', respond: LIBRARIES },
    { path: '/config/settings', respond: makeSettings({ dryRun: opts.dryRun ?? true, ...opts.settings }) },
    {
      path: '/profile/1',
      respond: {
        id: 1,
        name: 'Keep Highest Quality',
        isDefault: true,
        criteria: [],
        keepCount: 1,
        keepPer: '',
        protections: [],
        createdAt: '2026-01-01T00:00:00Z',
        updatedAt: '2026-01-01T00:00:00Z',
      },
    },
  ];
}

function comparison() {
  return screen.getByRole('table', { name: 'Copy comparison' });
}

function row(id: string): HTMLElement {
  const el = comparison().querySelector<HTMLElement>(`tr[data-row="${id}"]`);
  if (!el) throw new Error(`row ${id} not found`);
  return el;
}

describe('<DuplicateDetailPage>', () => {
  it('renders the header, the decision summary and a rank-ordered comparison with highlights', async () => {
    mockApi(routes());
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });

    expect(await screen.findByRole('heading', { level: 1, name: 'Heat' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /IMDb/ })).toHaveAttribute('href', 'https://www.imdb.com/title/tt0113277/');
    expect(screen.getByRole('link', { name: /TMDb/ })).toHaveAttribute('href', 'https://www.themoviedb.org/movie/949');
    expect(await screen.findByRole('link', { name: 'Keep Highest Quality' })).toBeInTheDocument();
    expect(screen.getByTestId('decision-summary')).toHaveTextContent('Keeping 2 · Removing 1 · Reclaim 10.0 GiB');
    expect(screen.getByText('Resolution 2160p > 1080p')).toBeInTheDocument();

    // Columns ordered by rank.
    const headers = [...comparison().querySelectorAll('th[data-file-id]')].map((th) => th.getAttribute('data-file-id'));
    expect(headers).toEqual(['11', '13', '12']);

    // Engine display values are used, best value highlighted, deciding row emphasized.
    const resolution = row('resolution');
    const cells = within(resolution).getAllByRole('cell');
    expect(cells[0]).toHaveTextContent('2160p');
    expect(cells[0]).toHaveAttribute('data-best', 'true');
    expect(cells[1]).not.toHaveAttribute('data-best');
    expect(cells[2]).toHaveTextContent('1080p');
    expect(cells[2]).toHaveTextContent('1920x800');
    expect(resolution).toHaveAttribute('data-deciding', 'true');
    expect(cells[2]).toHaveAttribute('data-decided', 'true');
    expect(row('size')).not.toHaveAttribute('data-deciding');

    // Hardlink note, protected lock with its reason, *arr details, path warnings.
    expect(within(row('size')).getByText('hardlinked — won’t free space')).toBeInTheDocument();
    expect(within(comparison()).getAllByText('Path matches /data/keep/**').length).toBeGreaterThan(0);
    expect(within(row('arr')).getByText(/Radarr 4K/)).toBeInTheDocument();
    expect(within(row('arr')).getByText(/CF score 1,500/)).toBeInTheDocument();
    expect(within(row('paths')).getByText('/data/movies/Heat (1995)/Heat.1080p.mkv')).toBeInTheDocument();

    // A protected copy can never be set to Remove.
    const protectedOverride = screen.getByRole('radiogroup', { name: 'Override for copy #2' });
    expect(within(protectedOverride).getByRole('radio', { name: 'Remove' })).toBeDisabled();
  });

  it('sets an override through the API and reverts with a toast when the server rejects it', async () => {
    const user = userEvent.setup();
    let reject = true;
    let current: DuplicateGroupDetail = GROUP;
    const api = mockApi([
      ...routes(),
      { path: '/duplicate/1', respond: () => current },
      {
        method: 'PUT',
        path: '/duplicate/1/file/12/override',
        respond: () => {
          if (reject) return json([{ propertyName: 'decision', errorMessage: 'At least one copy must be kept' }], 400);
          const updated: GroupFile = { ...LOSER, decision: 'keep', override: 'keep' };
          current = { ...GROUP, files: [KEEPER, PROTECTED, updated], reclaimableBytes: 0 };
          return current;
        },
      },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });

    const control = await screen.findByRole('radiogroup', { name: 'Override for copy #3' });
    expect(within(control).getByRole('radio', { name: 'Auto' })).toHaveAttribute('aria-checked', 'true');

    await user.click(within(control).getByRole('radio', { name: 'Keep' }));
    await waitFor(() => expect(api.find('PUT', '/duplicate/1/file/12/override')).toHaveLength(1));
    expect(api.find('PUT', '/duplicate/1/file/12/override')[0]!.body).toEqual({ decision: 'keep' });
    expect(await screen.findByText('At least one copy must be kept')).toBeInTheDocument();
    await waitFor(() =>
      expect(within(control).getByRole('radio', { name: 'Auto' })).toHaveAttribute('aria-checked', 'true'),
    );

    reject = false;
    await user.click(within(control).getByRole('radio', { name: 'Keep' }));
    await waitFor(() => expect(api.find('PUT', '/duplicate/1/file/12/override')).toHaveLength(2));
    await waitFor(() =>
      expect(
        within(screen.getByRole('radiogroup', { name: 'Override for copy #3' })).getByRole('radio', { name: 'Keep' }),
      ).toHaveAttribute('aria-checked', 'true'),
    );
    expect(comparison().querySelector('th[data-file-id="12"]')).toHaveAttribute('data-decision', 'keep');
  });

  it('clears an override with Auto (decision null)', async () => {
    const user = userEvent.setup();
    const overridden: GroupFile = { ...LOSER, decision: 'keep', override: 'keep' };
    const api = mockApi([
      ...routes({ group: { ...GROUP, files: [KEEPER, PROTECTED, overridden] } }),
      { method: 'PUT', path: '/duplicate/1/file/12/override', respond: GROUP },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });

    const control = await screen.findByRole('radiogroup', { name: 'Override for copy #3' });
    expect(within(control).getByRole('radio', { name: 'Keep' })).toHaveAttribute('aria-checked', 'true');
    await user.click(within(control).getByRole('radio', { name: 'Auto' }));
    await waitFor(() => expect(api.find('PUT', '/duplicate/1/file/12/override')).toHaveLength(1));
    expect(api.find('PUT', '/duplicate/1/file/12/override')[0]!.body).toEqual({ decision: null });
  });

  it('confirms approval with every file to remove and the deletion caveat', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...routes({ dryRun: false }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    const removals = within(dialog).getByRole('list', { name: 'Files to remove' });
    expect(within(removals).getAllByRole('listitem')).toHaveLength(1);
    expect(within(removals).getByText('/data/movies/Heat (1995)/Heat.1080p.mkv')).toBeInTheDocument();
    expect(within(removals).getByText('hardlinked — won’t free space')).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        'Deletion method is chosen at execution (order: Radarr / Sonarr → Plex → Filesystem); deletions through Plex or without a recycle bin are permanent.',
      ),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole('list', { name: 'Files to keep' })).toHaveTextContent('protected');

    await user.click(within(dialog).getByRole('button', { name: 'Approve & remove 1' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
    // The signature of the group as reviewed goes along, for the server's stale-decision check.
    expect(api.find('POST', '/duplicate/1/approve')[0]!.body).toEqual({ signature: 'abc' });
    expect(await screen.findByText('Approved “Heat (1995)”')).toBeInTheDocument();
  });

  it('shows the dry-run note in the approval dialog when dry run is on', async () => {
    const user = userEvent.setup();
    mockApi(routes({ dryRun: true }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/Dry run is enabled — these removals are only evaluated/)).toBeInTheDocument();
    // Approvals made in dry run stay simulated even if dry run is turned off before the queue runs.
    expect(within(dialog).getByText(/even if dry run is turned off before they run/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Approve (dry run)' })).toBeEnabled();
  });

  it('locks a resolved group and offers restore for recycled removals', async () => {
    const user = userEvent.setup();
    const group = makeGroup({
      files: [KEEPER, LOSER],
      status: 'resolved',
      actions: [
        {
          id: 40,
          groupId: 1,
          groupFileId: 12,
          versionKey: 'plex:1:101',
          title: 'Heat (1995)',
          paths: ['/data/movies/Heat (1995)/Heat.1080p.mkv'],
          size: 10 * GiB,
          method: 'filesystem',
          status: 'succeeded',
          dryRun: false,
          recyclePath: '/recycle/Heat.1080p.mkv',
          permanent: false,
          createdAt: '2026-09-22T09:00:00Z',
        },
      ],
    });
    const api = mockApi([...routes({ group }), { method: 'POST', path: '/action/40/restore', respond: {} }]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    // Resolved groups: overrides are locked, approve/ignore disabled.
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(within(screen.getByRole('radiogroup', { name: 'Override for copy #3' })).getByRole('radio', { name: 'Keep' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Restore' }));
    await waitFor(() => expect(api.find('POST', '/action/40/restore')).toHaveLength(1));
  });

  it('ignores the group, optionally adding an exclusion', async () => {
    const user = userEvent.setup();
    const api = mockApi([...routes(), { method: 'POST', path: '/duplicate/1/ignore', respond: { ...GROUP, status: 'ignored' } }]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Ignore' }));
    const dialog = await screen.findByRole('dialog', { name: 'Ignore duplicate' });
    await user.click(within(dialog).getByRole('checkbox', { name: /Also add an exclusion/ }));
    await user.click(within(dialog).getByRole('button', { name: 'Ignore' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/1/ignore')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/1/ignore')[0]!.body).toEqual({ addExclusion: true });
  });

  it('re-scans the group and stays busy while the targeted scan runs', async () => {
    const user = userEvent.setup();
    const queued = { id: 77, name: 'TargetedScan', body: {}, status: 'queued', trigger: 'manual', queued: '2026-09-22T10:00:00Z' };
    const api = mockApi([
      ...routes(),
      { method: 'POST', path: '/duplicate/1/rescan', respond: queued },
      { path: '/command/77', respond: queued },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    const button = screen.getByRole('button', { name: 'Re-scan' });
    await user.click(button);
    await waitFor(() => expect(api.find('POST', '/duplicate/1/rescan')).toHaveLength(1));
    expect(await screen.findByText('Re-scan queued')).toBeInTheDocument();
    await waitFor(() => expect(api.find('GET', '/command/77').length).toBeGreaterThan(0));
    expect(screen.getByRole('button', { name: 'Re-scan' })).toBeDisabled();
    // The group is about to be re-evaluated: no approval until the re-scan ends.
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
  });

  it('requires an explicit acknowledgement before approving a review group', async () => {
    const user = userEvent.setup();
    const group = makeGroup({ files: [LOSER, KEEPER, PROTECTED], status: 'review', statusReason: 'Folder titles differ' });
    const api = mockApi([
      ...routes({ group, dryRun: false }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    expect(within(dialog).getByText(/Folder titles differ/)).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve & remove 1' });
    expect(confirm).toBeDisabled();

    await user.click(within(dialog).getByRole('checkbox', { name: /I compared the copies/ }));
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
  });

  it('blocks approval when the copies change while the dialog is open, until reviewed', async () => {
    const user = userEvent.setup();
    let current: DuplicateGroupDetail = GROUP;
    const api = mockApi([
      ...routes({ dryRun: false }),
      { path: '/duplicate/1', respond: () => current },
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    const { queryClient } = renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    expect(within(dialog).getByRole('button', { name: 'Approve & remove 1' })).toBeEnabled();

    // A scan re-evaluates the group: the 4K keeper is now proposed for removal.
    current = {
      ...GROUP,
      files: [
        { ...KEEPER, decision: 'remove', engineDecision: 'remove', rank: 3 },
        { ...LOSER, decision: 'keep', engineDecision: 'keep', rank: 1 },
        PROTECTED,
      ],
    };
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.detail(1) }));

    expect(await within(dialog).findByText('This group changed while the dialog was open')).toBeInTheDocument();
    const removals = within(dialog).getByRole('list', { name: 'Files to remove' });
    expect(removals).toHaveTextContent('Heat.2160p.mkv');
    const confirm = within(dialog).getByRole('button', { name: 'Approve & remove 1' });
    expect(confirm).toBeDisabled();

    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
  });

  it('shows a 409 refusal in the dialog, refetches the group and re-approves with the reviewed signature', async () => {
    const user = userEvent.setup();
    const STALE = 'This duplicate changed since it was displayed (a scan or another user updated its decisions); review it again before approving';
    let current: DuplicateGroupDetail = GROUP;
    const approvals: unknown[] = [];
    const api = mockApi([
      ...routes({ dryRun: false }),
      { path: '/duplicate/1', respond: () => current },
      {
        method: 'POST',
        path: '/duplicate/1/approve',
        respond: (c: { body: unknown }) => {
          approvals.push(c.body);
          return approvals.length === 1 ? json({ message: STALE }, 409) : [{ id: 1 }];
        },
      },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    const loads = api.find('GET', '/duplicate/1').length;

    // A scan re-evaluated the group on the server after the dialog opened.
    current = {
      ...GROUP,
      signature: 'def',
      files: [
        { ...KEEPER, decision: 'remove', engineDecision: 'remove', rank: 3 },
        { ...LOSER, decision: 'keep', engineDecision: 'keep', rank: 1 },
        PROTECTED,
      ],
    };
    await user.click(within(dialog).getByRole('button', { name: 'Approve & remove 1' }));

    expect(await within(dialog).findByText(STALE)).toBeInTheDocument();
    expect(within(dialog).getByText('Approval refused')).toBeInTheDocument();
    expect(approvals[0]).toEqual({ signature: 'abc' });
    // The group is reloaded and the guard asks for a review of the new decisions.
    await waitFor(() => expect(api.find('GET', '/duplicate/1').length).toBeGreaterThan(loads));
    expect(await within(dialog).findByText('This group changed while the dialog was open')).toBeInTheDocument();
    expect(within(dialog).getByRole('list', { name: 'Files to remove' })).toHaveTextContent('Heat.2160p.mkv');
    const confirm = within(dialog).getByRole('button', { name: 'Approve & remove 1' });
    expect(confirm).toBeDisabled();

    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    await user.click(confirm);
    await waitFor(() => expect(approvals).toHaveLength(2));
    expect(approvals[1]).toEqual({ signature: 'def' });
    expect(await screen.findByText('Approved “Heat (1995)”')).toBeInTheDocument();
  });

  it('treats a new server signature as a change even when the copies look the same', async () => {
    const user = userEvent.setup();
    let current: DuplicateGroupDetail = GROUP;
    const api = mockApi([
      ...routes({ dryRun: true }),
      { path: '/duplicate/1', respond: () => current },
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    const { queryClient } = renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    current = { ...GROUP, signature: 'abc2' };
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.detail(1) }));

    expect(await within(dialog).findByText('This group changed while the dialog was open')).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve (dry run)' });
    expect(confirm).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    await user.click(confirm);
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/1/approve')[0]!.body).toEqual({ signature: 'abc2' });
  });

  it('keeps other approval errors in a toast with the dialog still open', async () => {
    const user = userEvent.setup();
    mockApi([
      ...routes({ dryRun: true }),
      {
        method: 'POST',
        path: '/duplicate/1/approve',
        respond: json({ message: 'Cannot approve: keeper file is missing' }, 400),
      },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    await user.click(within(dialog).getByRole('button', { name: 'Approve (dry run)' }));
    expect(await screen.findByText('Could not approve')).toBeInTheDocument();
    expect(screen.getByText('Cannot approve: keeper file is missing')).toBeInTheDocument();
    expect(within(dialog).queryByText('Approval refused')).not.toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Approve “Heat (1995)”' })).toBeInTheDocument();
  });

  it('disables approval while showing data that could not be refreshed', async () => {
    const user = userEvent.setup();
    let fail = false;
    mockApi([
      ...routes({ dryRun: false }),
      { path: '/duplicate/1', respond: () => (fail ? json({ message: 'Database is locked' }, 500) : GROUP) },
    ]);
    const { queryClient } = renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();

    fail = true;
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.detail(1) }));
    expect(await screen.findByText('Showing the last loaded data')).toBeInTheDocument();
    expect(screen.getByText(/Database is locked/)).toBeInTheDocument();
    // The last good data stays visible, but it can't be approved.
    expect(screen.getByRole('heading', { level: 1, name: 'Heat' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();

    fail = false;
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(screen.queryByText('Showing the last loaded data')).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it('shows a not-found state for unknown ids and an invalid state for bad ids', async () => {
    mockApi(routes());
    const { unmount } = renderPage(<DuplicateDetailPage />, { route: '/duplicate/99', path: '/duplicate/:id' });
    expect(await screen.findByText('Duplicate not found')).toBeInTheDocument();
    unmount();

    renderPage(<DuplicateDetailPage />, { route: '/duplicate/abc', path: '/duplicate/:id' });
    expect(await screen.findByText('Invalid duplicate')).toBeInTheDocument();
  });
});

describe('<DuplicateDetailPage> full discs', () => {
  const DISC_LOSER = makeFile({
    id: 14,
    rank: 2,
    decision: 'remove',
    engineDecision: 'remove',
    decidingCriterion: 'source',
    reasons: ['Source Remux > Full disc'],
    version: discVersion(
      { type: 'uhd_bluray', fileCount: 312, discs: 1 },
      { resolution: '2160', dynamicRange: 'dv_hdr10', videoCodec: 'hevc', audioTracks: [], subtitleTracks: [] },
    ),
  });
  const DISC_GROUP = makeGroup({
    files: [DISC_LOSER, KEEPER],
    flags: ['full_disc'],
    reclaimableBytes: 61 * GiB,
  });
  const ALLOWED: Partial<Settings> = { allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle' };

  it('shows a disc as one copy: kind, origin, counts, main feature and its root instead of its files', async () => {
    const group = makeGroup({
      files: [KEEPER, { ...DISC_LOSER, version: { ...DISC_LOSER.version, disc: { ...DISC_LOSER.version.disc!, discs: 2 } } }],
      flags: ['full_disc'],
    });
    mockApi(routes({ group }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    // Flag with its description in the header.
    const flags = screen.getByRole('list', { name: 'Flags' });
    expect(within(flags).getByText('Full disc')).toBeInTheDocument();
    expect(within(flags).getByText(/full-disc backup \(BDMV \/ VIDEO_TS \/ ISO\)/)).toBeInTheDocument();

    // Column header badge.
    expect(within(comparison().querySelector<HTMLElement>('th[data-file-id="14"]')!).getByText('UHD BD')).toBeInTheDocument();

    const cells = within(row('disc')).getAllByRole('cell');
    expect(cells[0]).toHaveTextContent('Regular video file');
    expect(within(cells[1]!).getByText('UHD Blu-ray disc (BDMV)')).toBeInTheDocument();
    expect(within(cells[1]!).getByText('Not in Plex')).toBeInTheDocument();
    expect(cells[1]).toHaveTextContent('312 files · 2 discs');
    expect(cells[1]).toHaveTextContent('Main feature: BDMV/PLAYLIST/00800.mpls');
    expect(cells[1]).not.toHaveTextContent('metadata unreadable');

    expect(within(row('size')).getAllByRole('cell')[1]).toHaveTextContent('whole disc · 312 files');
    expect(within(row('container')).getAllByRole('cell')[1]).toHaveTextContent('None — a full disc');
    const paths = within(row('paths')).getAllByRole('cell')[1]!;
    expect(paths).toHaveTextContent('Disc root');
    expect(within(paths).getByText('/data/movies/Heat (1995)')).toBeInTheDocument();
    expect(within(paths).getByText('local: /mnt/movies/Heat (1995)')).toBeInTheDocument();
    expect(screen.getByTestId('decision-summary')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Decision' })).toHaveTextContent('UHD Blu-ray disc (BDMV)');
  });

  it('collapses the hundreds of parts a custom Plex scanner exposes and warns when the disc is unreadable', async () => {
    const parts: MediaPart[] = Array.from({ length: 300 }, (_, i) => ({
      id: 5000 + i,
      path: `/data/discs/Heat (1995)/BDMV/STREAM/${String(i).padStart(5, '0')}.m2ts`,
      localPath: '',
      size: GiB / 5,
      duration: 1000,
    }));
    const plexDisc = makeFile({
      ...DISC_LOSER,
      version: discVersion(
        { origin: 'plex', readable: false, root: '/data/discs/Heat (1995)', localRoot: '', mainFeature: '' },
        { key: 'plex:1:300', mediaId: 300, parts },
      ),
    });
    mockApi(routes({ group: makeGroup({ files: [KEEPER, plexDisc], flags: ['full_disc', 'disc_unreadable'] }) }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    const discCell = within(row('disc')).getAllByRole('cell')[1]!;
    expect(within(discCell).getByText('Custom Plex scanner')).toBeInTheDocument();
    expect(within(discCell).getByText('metadata unreadable — quality unknown')).toBeInTheDocument();
    expect(discCell).not.toHaveTextContent('Main feature');

    const paths = within(row('paths')).getAllByRole('cell')[1]!;
    expect(within(paths).getByText('300 parts in Plex')).toBeInTheDocument();
    expect(within(paths).getAllByText(/BDMV\/STREAM\/\d{5}\.m2ts$/)).toHaveLength(25);
    expect(within(paths).getByText('…and 275 more')).toBeInTheDocument();
    expect(within(screen.getByRole('list', { name: 'Flags' })).getByText('Disc unreadable')).toBeInTheDocument();
  });

  it('confirms a disc removal as one whole-disc move to the recycle bin', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...routes({ group: DISC_GROUP, dryRun: false, settings: ALLOWED }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    const removals = within(dialog).getByRole('list', { name: 'Files to remove' });
    expect(within(removals).getByTestId('disc-removal-note')).toHaveTextContent(
      'The whole disc folder (312 files) will be moved to the recycle bin.',
    );
    expect(within(removals).getByText('/data/movies/Heat (1995)')).toBeInTheDocument();
    expect(within(removals).getByText(/Other video files, artwork, .nfo and subtitles in the same folder stay/)).toBeInTheDocument();
    expect(within(removals).getByText(/Expected via the filesystem — the disc is moved to Dupearr’s recycle bin/)).toBeInTheDocument();
    expect(within(dialog).queryByText('Approval blocked')).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Approve & remove 1' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
  });

  it.each([
    ['disc removal is off', { allowDiscRemoval: false, recycleBinPath: '/data/.dupearr-recycle' }, /Removing full discs is turned off/],
    ['no recycle bin is set', { allowDiscRemoval: true, recycleBinPath: '' }, /no recycle bin is set/],
  ])('blocks a disc removal when %s', async (_name, settings, message) => {
    const user = userEvent.setup();
    const api = mockApi(routes({ group: DISC_GROUP, dryRun: false, settings }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    expect(within(dialog).getByText('Approval blocked')).toBeInTheDocument();
    expect(within(dialog).getByText(message)).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve & remove 1' });
    expect(confirm).toBeDisabled();
    await user.click(confirm);
    expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(0);
  });

  it('blocks removing a single *arr-tracked clip inside a disc', async () => {
    const user = userEvent.setup();
    const clip = makeFile({
      ...LOSER,
      version: {
        ...LOSER.version,
        parts: [{ id: 1001, path: '/data/movies/Heat (1995)/BDMV/STREAM/00800.m2ts', localPath: '', size: 40 * GiB, duration: 1 }],
      },
    });
    mockApi(routes({ group: makeGroup({ files: [KEEPER, clip], flags: ['disc_tracked_clip'] }), settings: ALLOWED }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Heat' });
    expect(within(screen.getByRole('list', { name: 'Flags' })).getByText('*arr tracks a disc clip')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    expect(within(dialog).getByText(/never removed one by one/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Approve (dry run)' })).toBeDisabled();
  });
});

describe('<DuplicateDetailPage> loose disc clips (flattened Blu-ray backups)', () => {
  const ALLOWED: Partial<Settings> = { allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle', keepPlayableCopy: false };

  // A group stored before loose clip sets were recognised: Plex listed every loose clip of
  // "Elemental (2023)" as a separate version, and the engine ranked one clip first and marked the
  // others Remove (the incident: 125 clips deleted through Plex).
  const clipFile = (n: number, rank: number, decision: 'keep' | 'remove') =>
    makeFile({ id: 100 + n, rank, decision, engineDecision: decision, version: clipVersion(n) });
  const STORED = makeGroup({
    title: 'Elemental',
    year: 2023,
    files: [clipFile(174, 1, 'keep'), clipFile(175, 2, 'remove'), clipFile(176, 3, 'remove')],
    reclaimableBytes: 2 * GiB,
  });

  it('warns that files of a disc are listed as copies and locks Remove on every clip', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...routes({ group: STORED, dryRun: false, settings: ALLOWED }),
      { method: 'PUT', path: /^\/duplicate\/1\/file\/\d+\/override$/, respond: STORED },
      { method: 'POST', path: '/duplicate/1/rescan', respond: { id: 9, name: 'RescanDuplicate', status: 'queued' } },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Elemental' });

    expect(screen.getByText('Files of a disc are listed as separate copies')).toBeInTheDocument();
    expect(screen.getByText(/3 copies are a single file of a disc/)).toBeInTheDocument();

    for (const [n, rank] of [
      [174, 1],
      [175, 2],
      [176, 3],
    ] as const) {
      const header = comparison().querySelector<HTMLElement>(`th[data-file-id="${100 + n}"]`)!;
      expect(within(header).getByTestId('disc-member-note')).toHaveTextContent('One file of a disc — never removed on its own');
      const control = within(header).getByRole('radiogroup', { name: `Override for copy #${rank}` });
      const remove = within(control).getByRole('radio', { name: 'Remove' });
      expect(remove).toBeDisabled();
      expect(remove).toHaveAttribute('title', expect.stringContaining(`“${CLIP_FOLDER}/00${n}.m2ts” is one file of a Blu-ray stored as loose .m2ts clips`));
    }
    expect(within(row('disc')).getAllByRole('cell')[0]).toHaveTextContent('One file of a disc — never removed on its own');

    // Neither a click nor the keyboard marks a clip for removal.
    const first = screen.getByRole('radiogroup', { name: 'Override for copy #1' });
    await user.click(within(first).getByRole('radio', { name: 'Remove' }));
    within(first).getByRole('radio', { name: 'Auto' }).focus();
    await user.keyboard('{ArrowRight}{ArrowRight}');
    const bodies = api.find('PUT', /override$/).map((c) => c.body);
    expect(bodies).not.toContainEqual({ decision: 'remove' });
  });

  it('refuses to approve the removal of a clip, whatever the settings, and sends nothing', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...routes({ group: STORED, dryRun: false, settings: ALLOWED }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Elemental' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Elemental (2023)”' });
    expect(within(dialog).getByText('Approval blocked')).toBeInTheDocument();
    expect(within(dialog).getByText(/is one file of a Blu-ray stored as loose \.m2ts clips/)).toBeInTheDocument();
    expect(within(dialog).getByText(/clips are never removed one by one/)).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve & remove 2' });
    expect(confirm).toBeDisabled();
    await user.click(confirm);
    expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(0);
  });

  const SET = makeFile({
    id: 21,
    rank: 2,
    decision: 'remove',
    engineDecision: 'remove',
    decidingCriterion: 'source',
    reasons: ['Source Remux > Full disc'],
    version: clipSetVersion({}, { resolution: '1080', width: 1920, height: 1080, dynamicRange: 'sdr', videoCodec: 'h264' }),
  });
  const SET_GROUP = makeGroup({ title: 'Elemental', year: 2023, files: [KEEPER, SET], flags: ['full_disc'], reclaimableBytes: 31 * GiB });

  it('shows the clip set as ONE copy: its label, clip count and main clip', async () => {
    mockApi(routes({ group: SET_GROUP }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Elemental' });

    expect(screen.queryByText('Files of a disc are listed as separate copies')).not.toBeInTheDocument();
    expect(screen.getByText('2 copies')).toBeInTheDocument();
    const header = comparison().querySelector<HTMLElement>('th[data-file-id="21"]')!;
    expect(within(header).getByText('BD clips')).toBeInTheDocument();
    expect(within(header).queryByTestId('disc-member-note')).not.toBeInTheDocument();

    const cell = within(row('disc')).getAllByRole('cell')[1]!;
    expect(within(cell).getByText('Blu-ray clips (loose .m2ts)')).toBeInTheDocument();
    expect(within(cell).getByText('Plex: one version per clip')).toBeInTheDocument();
    expect(cell).toHaveTextContent('3 clips');
    expect(cell).not.toHaveTextContent('files');
    expect(cell).toHaveTextContent('Main clip: 00174.m2ts');
    expect(cell).not.toHaveTextContent('Main feature');

    expect(within(row('size')).getAllByRole('cell')[1]).toHaveTextContent('every clip · 3 clips');
    expect(within(row('container')).getAllByRole('cell')[1]).toHaveTextContent('M2TS');
    const paths = within(row('paths')).getAllByRole('cell')[1]!;
    expect(paths).toHaveTextContent('Clip folder');
    expect(within(paths).getByText(CLIP_FOLDER)).toBeInTheDocument();
    expect(within(paths).getByText('3 clips in Plex')).toBeInTheDocument();
    expect(within(paths).getByText('00174.m2ts')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Decision' })).toHaveTextContent('Blu-ray clips (loose .m2ts)');
  });

  it('confirms a whole clip-set removal (only the clips move) when disc removal is allowed', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...routes({ group: SET_GROUP, dryRun: false, settings: ALLOWED }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 1 }] },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Elemental' });

    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Elemental (2023)”' });
    const removals = within(dialog).getByRole('list', { name: 'Files to remove' });
    expect(within(removals).getByTestId('disc-removal-note')).toHaveTextContent(
      'Every loose Blu-ray clip (00800.m2ts …) of this folder (3 clips) will be moved to the recycle bin.',
    );
    expect(within(removals).getByText(/Other video files \(an MKV or MP4\), artwork, \.nfo and subtitles stay/)).toBeInTheDocument();
    expect(within(removals).getByText(/the loose clips are moved to Dupearr’s recycle bin/)).toBeInTheDocument();
    expect(within(dialog).queryByText('Approval blocked')).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Approve & remove 1' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
  });

  it('keeps a clip set protected while disc removal is off', async () => {
    const user = userEvent.setup();
    mockApi(routes({ group: SET_GROUP, dryRun: false, settings: { allowDiscRemoval: false, recycleBinPath: '/r' } }));
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    await screen.findByRole('heading', { level: 1, name: 'Elemental' });
    await user.click(screen.getByRole('button', { name: 'Approve' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Elemental (2023)”' });
    expect(within(dialog).getByText(/Removing full discs is turned off/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Approve & remove 1' })).toBeDisabled();
  });
});
