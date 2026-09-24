import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { queryKeys } from '@/api/queryKeys';
import type { DuplicateGroupSummary } from '@/api/types';
import {
  GiB,
  LIBRARIES,
  SERVERS,
  json,
  makeSettings,
  makeStats,
  makeSummary,
  mockApi,
  paged,
  renderPage,
  type MockRoute,
} from '@/components/duplicates/testing';
import DuplicatesPage from './DuplicatesPage';

afterEach(() => {
  vi.unstubAllGlobals();
});

const HEAT = makeSummary();
const EPISODE = makeSummary({
  id: 2,
  key: 'episode:tvdb:81189:s1e2',
  mediaType: 'episode',
  title: 'Pilot',
  year: 2008,
  showTitle: 'Breaking Bad',
  season: 1,
  episode: 2,
  status: 'review',
  statusReason: 'Durations differ',
  flags: ['duration_mismatch', 'cross_library'],
  reclaimableBytes: 5 * GiB,
});

function baseRoutes(opts: {
  records?: DuplicateGroupSummary[];
  total?: number;
  dryRun?: boolean;
  servers?: typeof SERVERS;
  stats?: ReturnType<typeof makeStats>;
} = {}): MockRoute[] {
  const records = opts.records ?? [HEAT, EPISODE];
  return [
    { path: '/duplicate', respond: () => paged(records, { totalRecords: opts.total ?? records.length }) },
    { path: '/duplicate/stats', respond: opts.stats ?? makeStats() },
    { path: '/library', respond: LIBRARIES },
    { path: '/mediaserver', respond: opts.servers ?? SERVERS },
    { path: '/command', respond: [] },
    { path: '/config/settings', respond: makeSettings({ dryRun: opts.dryRun ?? true }) },
  ];
}

function table() {
  return screen.getByRole('table', { name: 'Duplicate groups' });
}

describe('<DuplicatesPage>', () => {
  it('renders rows from the API with the default open-status filter and sort', async () => {
    const api = mockApi(baseRoutes());
    renderPage(<DuplicatesPage />);

    expect(await screen.findByRole('link', { name: 'Heat (1995)' })).toHaveAttribute('href', '/duplicate/1');
    expect(screen.getByRole('link', { name: 'Breaking Bad – S01E02 – Pilot' })).toHaveAttribute('href', '/duplicate/2');

    const rows = within(table()).getAllByRole('row');
    expect(rows).toHaveLength(3); // header + 2
    expect(within(rows[1]!).getByText('Pending')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('10.0 GiB')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('Movies')).toBeInTheDocument();
    // Mini keep/remove chips.
    const chips = within(rows[1]!).getAllByRole('listitem');
    expect(chips.map((c) => c.getAttribute('data-decision'))).toEqual(['keep', 'remove']);
    expect(chips[0]).toHaveTextContent('4K DV HDR10');
    expect(chips[1]).toHaveTextContent('1080p');
    // Review row: status + flag icons with descriptions.
    expect(within(rows[2]!).getByText('Needs Review')).toBeInTheDocument();
    expect(within(rows[2]!).getByRole('img', { name: /Duration mismatch/ })).toBeInTheDocument();

    const list = api.find('GET', '/duplicate')[0]!;
    expect(list.query.get('status')).toBe('pending,review,deferred,queued,failed');
    expect(list.query.get('sortKey')).toBe('lastSeenAt');
    expect(list.query.get('sortDirection')).toBe('descending');
    expect(list.query.get('page')).toBe('1');
    expect(list.query.get('pageSize')).toBe('20');
  });

  it('labels an episode with an unknown season as S??, never S-1', async () => {
    mockApi(baseRoutes({ records: [{ ...EPISODE, season: -1, status: 'pending' }] }));
    renderPage(<DuplicatesPage />);
    expect(await screen.findByRole('link', { name: 'Breaking Bad – S??E02 – Pilot' })).toBeInTheDocument();
    expect(screen.queryByText(/S-1/)).not.toBeInTheDocument();
  });

  it('reads ?search= from the URL and sends it to the server', async () => {
    const api = mockApi(baseRoutes({ records: [HEAT] }));
    renderPage(<DuplicatesPage />, { route: '/?search=heat&status=all' });

    await screen.findByRole('link', { name: 'Heat (1995)' });
    const list = api.find('GET', '/duplicate')[0]!;
    expect(list.query.get('search')).toBe('heat');
    expect(list.query.has('status')).toBe(false);
    expect(screen.getByText('“heat”')).toBeInTheDocument();
  });

  it('sorts server-side when a sortable header is clicked', async () => {
    const user = userEvent.setup();
    const api = mockApi(baseRoutes());
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(within(table()).getByRole('button', { name: 'Reclaimable' }));
    await waitFor(() => {
      const last = api.find('GET', '/duplicate').at(-1)!;
      expect(last.query.get('sortKey')).toBe('reclaimableBytes');
      expect(last.query.get('sortDirection')).toBe('ascending');
    });
  });

  it('bulk-approves the selection after confirming a summary, skipping review groups', async () => {
    const user = userEvent.setup();
    const second = makeSummary({ id: 3, title: 'Alien', year: 1979, removeCount: 2, fileCount: 3, reclaimableBytes: 5 * GiB });
    const api = mockApi([
      ...baseRoutes({ records: [HEAT, EPISODE, second], dryRun: true }),
      {
        method: 'POST',
        path: '/duplicate/bulk',
        respond: { succeeded: [1], failed: [{ id: 3, message: 'Keeper file missing' }] },
      },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    // Bulk buttons only appear in select mode with a non-empty selection.
    expect(screen.queryByRole('button', { name: /^Approve \(/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Select' }));
    for (const box of within(table()).getAllByRole('checkbox', { name: 'Select row' })) await user.click(box);
    await user.click(screen.getByRole('button', { name: 'Approve (3)' }));

    const dialog = await screen.findByRole('dialog', { name: 'Approve 2 groups' });
    expect(within(dialog).getByTestId('bulk-summary')).toHaveTextContent('Approve 3 files from 2 groups, reclaiming 15.0 GiB.');
    expect(within(dialog).getByTestId('bulk-skipped')).toHaveTextContent(
      '1 selected group is not eligible and will be skipped. 1 group needs review and must be approved one at a time from the detail page.',
    );
    expect(within(dialog).getByText(/Dry run is enabled/)).toBeInTheDocument();
    const listed = within(within(dialog).getByRole('list', { name: 'Groups to approve' })).getAllByRole('listitem');
    expect(listed.map((li) => li.textContent)).toEqual(['Heat (1995)1 file · 10.0 GiB', 'Alien (1979)2 files · 5.0 GiB']);

    await user.click(within(dialog).getByRole('button', { name: 'Approve (dry run)' }));

    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/bulk')[0]!.body).toEqual({
      ids: [1, 3],
      action: 'approve',
      signatures: { '1': 'sig-1', '3': 'sig-3' },
    });

    const toast = (await screen.findByText('Approved 1 of 2 groups')).closest<HTMLElement>('[role="status"],[role="alert"]')!;
    expect(within(toast).getByText('Alien (1979)')).toBeInTheDocument();
    expect(within(toast).getByText(/Keeper file missing/)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('requires typing DELETE to bulk-approve more than 10 groups outside dry run', async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: 12 }, (_, i) => makeSummary({ id: 100 + i, title: `Movie ${i}` }));
    const api = mockApi([
      ...baseRoutes({ records: many, dryRun: false }),
      { method: 'POST', path: '/duplicate/bulk', respond: (c: { body: unknown }) => ({ succeeded: (c.body as { ids: number[] }).ids, failed: [] }) },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Movie 0 (1995)' });

    await user.click(screen.getByRole('button', { name: 'Select' }));
    await user.click(within(table()).getByRole('checkbox', { name: 'Select all' }));
    await user.click(screen.getByRole('button', { name: 'Approve (12)' }));

    const dialog = await screen.findByRole('dialog', { name: 'Approve 12 groups' });
    expect(within(dialog).getByText(/Deletion method is chosen at execution \(order: Radarr \/ Sonarr → Plex → Filesystem\)/)).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve' });
    expect(confirm).toBeDisabled();

    const input = within(dialog).getByLabelText(/Type DELETE to approve 12 groups/);
    await user.type(input, 'delete');
    expect(confirm).toBeDisabled();
    await user.clear(input);
    await user.type(input, 'DELETE');
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect((api.find('POST', '/duplicate/bulk')[0]!.body as { ids: number[] }).ids).toHaveLength(12);
    expect(await screen.findByText('Approved 12 groups')).toBeInTheDocument();
  });

  it('approves a single group from its row, but never a review group', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...baseRoutes({ dryRun: false }),
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 50 }] },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    // Suspect matches must be approved from their detail page.
    expect(screen.queryByRole('button', { name: 'Approve Pilot' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ignore Pilot' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Approve Heat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    expect(within(dialog).getByTestId('bulk-summary')).toHaveTextContent('Remove 1 file, reclaiming 10.0 GiB.');
    const removals = within(dialog).getByRole('list', { name: 'Files to remove' });
    expect(within(removals).getAllByRole('listitem')).toHaveLength(1);
    expect(removals).toHaveTextContent('1080p');
    expect(within(dialog).getByRole('link', { name: /Compare the copies/ })).toHaveAttribute('href', '/duplicate/1');
    expect(within(dialog).getByText(/deletions through Plex or without a recycle bin are permanent/)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
    // The signature of the row as reviewed goes along, for the server's stale-decision check.
    expect(api.find('POST', '/duplicate/1/approve')[0]!.body).toEqual({ signature: 'sig-1' });
    expect(await screen.findByText('Approved “Heat (1995)”')).toBeInTheDocument();
    expect(api.find('POST', '/duplicate/bulk')).toHaveLength(0);
  });

  it('shows full discs in the chips and approves a disc removal only from its detail page', async () => {
    const user = userEvent.setup();
    const DISC = makeSummary({
      id: 4,
      title: 'Alien',
      year: 1979,
      flags: ['full_disc'],
      files: [
        { id: 41, decision: 'keep', resolution: '2160', dynamicRange: 'hdr10', videoCodec: 'hevc', size: 60 * GiB, libraryTitle: 'Movies' },
        {
          id: 42,
          decision: 'remove',
          resolution: '2160',
          dynamicRange: 'dv_hdr10',
          videoCodec: 'hevc',
          size: 80 * GiB,
          libraryTitle: 'Movies',
          disc: { type: 'uhd_bluray', fileCount: 312, discs: 2 },
        },
      ],
    });
    const api = mockApi([
      ...baseRoutes({ records: [HEAT, DISC], dryRun: true }),
      { method: 'POST', path: '/duplicate/bulk', respond: { succeeded: [1], failed: [] } },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Alien (1979)' });

    const rows = within(table()).getAllByRole('row');
    const chips = within(rows[2]!).getAllByRole('listitem');
    expect(chips.map((c) => c.getAttribute('data-disc'))).toEqual([null, 'uhd_bluray']);
    expect(chips[1]).toHaveTextContent('UHD BD · 4K DV HDR10');
    expect(chips[1]!.getAttribute('title')).toContain('UHD Blu-ray disc (BDMV) · 312 files · 2 discs');
    expect(within(rows[2]!).getByRole('img', { name: /^Full disc:/ })).toBeInTheDocument();

    // No row approval for a disc removal; the regular group keeps its button.
    expect(screen.queryByRole('button', { name: 'Approve Alien' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ignore Alien' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve Heat' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Select' }));
    for (const box of within(table()).getAllByRole('checkbox', { name: 'Select row' })) await user.click(box);
    await user.click(screen.getByRole('button', { name: 'Approve (2)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve 1 group' });
    expect(within(dialog).getByTestId('bulk-skipped')).toHaveTextContent(
      '1 selected group is not eligible and will be skipped. 1 group removes a full disc and must be approved one at a time from the detail page.',
    );
    await user.click(within(dialog).getByRole('button', { name: 'Approve (dry run)' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect((api.find('POST', '/duplicate/bulk')[0]!.body as { ids: number[] }).ids).toEqual([1]);
  });

  it('never approves from the list a group that would remove a single disc clip (loose 00174.m2ts)', async () => {
    const user = userEvent.setup();
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
    const CLIPS = makeSummary({
      id: 5,
      title: 'Elemental',
      year: 2023,
      fileCount: 3,
      keepCount: 1,
      removeCount: 2,
      files: [clip(51, 'keep'), clip(52, 'remove'), clip(53, 'remove')],
    });
    const api = mockApi([
      ...baseRoutes({ records: [HEAT, CLIPS], dryRun: false }),
      { method: 'POST', path: '/duplicate/bulk', respond: { succeeded: [1], failed: [] } },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Elemental (2023)' });

    expect(screen.queryByRole('button', { name: 'Approve Elemental' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ignore Elemental' })).toBeInTheDocument();
    const rows = within(table()).getAllByRole('row');
    expect(within(rows[2]!).getAllByRole('listitem')[0]!.getAttribute('title')).toContain('Single disc clip (never removed on its own)');

    await user.click(screen.getByRole('button', { name: 'Select' }));
    for (const box of within(table()).getAllByRole('checkbox', { name: 'Select row' })) await user.click(box);
    await user.click(screen.getByRole('button', { name: 'Approve (2)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve 1 group' });
    expect(within(dialog).getByTestId('bulk-skipped')).toHaveTextContent(
      '1 selected group is not eligible and will be skipped. 1 group would remove a single disc clip (e.g. a loose 00800.m2ts). A movie can span several clips, so clips are never removed one by one — re-scan it to show the clips as one copy.',
    );
    await user.click(within(dialog).getByRole('button', { name: /^Approve/ }));
    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect((api.find('POST', '/duplicate/bulk')[0]!.body as { ids: number[] }).ids).toEqual([1]);
  });

  it('drops selected groups that left the page instead of approving their stale snapshot', async () => {
    const user = userEvent.setup();
    const alien = makeSummary({ id: 3, title: 'Alien', year: 1979 });
    let records: DuplicateGroupSummary[] = [HEAT, alien];
    const api = mockApi([
      ...baseRoutes(),
      { path: '/duplicate', respond: () => paged(records) },
      { method: 'POST', path: '/duplicate/bulk', respond: { succeeded: [1], failed: [] } },
    ]);
    const { queryClient } = renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Alien (1979)' });

    await user.click(screen.getByRole('button', { name: 'Select' }));
    for (const box of within(table()).getAllByRole('checkbox', { name: 'Select row' })) await user.click(box);
    expect(screen.getByRole('button', { name: 'Approve (2)' })).toBeInTheDocument();

    // A scan turns Alien into a review group: it leaves the (pending-only) page.
    records = [HEAT];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.lists }));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Alien (1979)' })).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Approve (1)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve 1 group' });
    await user.click(within(dialog).getByRole('button', { name: 'Approve (dry run)' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/bulk')[0]!.body).toEqual({ ids: [1], action: 'approve', signatures: { '1': 'sig-1' } });
  });

  it('blocks a row approval whose group changed while the dialog was open until reviewed', async () => {
    const user = userEvent.setup();
    let records: DuplicateGroupSummary[] = [HEAT];
    const api = mockApi([
      ...baseRoutes({ dryRun: false }),
      { path: '/duplicate', respond: () => paged(records) },
      { method: 'POST', path: '/duplicate/1/approve', respond: [{ id: 50 }] },
    ]);
    const { queryClient } = renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Approve Heat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    const confirm = within(dialog).getByRole('button', { name: 'Approve' });
    expect(confirm).toBeEnabled();

    // Re-evaluated elsewhere: the 4K copy is now the one proposed for removal.
    records = [
      makeSummary({
        reclaimableBytes: 60 * GiB,
        files: HEAT.files.map((f) => ({ ...f, decision: f.decision === 'keep' ? ('remove' as const) : ('keep' as const) })),
      }),
    ];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.lists }));

    expect(await within(dialog).findByText('The selection changed while the dialog was open')).toBeInTheDocument();
    expect(confirm).toBeDisabled();
    expect(within(dialog).getByRole('list', { name: 'Files to remove' })).toHaveTextContent('2160p');
    await user.click(confirm);
    expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(0);

    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    await waitFor(() => expect(api.find('POST', '/duplicate/1/approve')).toHaveLength(1));
  });

  it('refuses a row approval when the group is no longer listed', async () => {
    const user = userEvent.setup();
    let records: DuplicateGroupSummary[] = [HEAT, EPISODE];
    const api = mockApi([...baseRoutes({ dryRun: false }), { path: '/duplicate', respond: () => paged(records) }]);
    const { queryClient } = renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Approve Heat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });

    records = [EPISODE];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.lists }));
    expect(await within(dialog).findByText(/no longer listed/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(api.find('POST', /\/approve$/)).toHaveLength(0);
  });

  it('shows a 409 row-approval refusal, refetches the list and approves only after the change is reviewed', async () => {
    const user = userEvent.setup();
    const STALE = 'This duplicate changed since it was displayed (a scan or another user updated its decisions); review it again before approving';
    let records: DuplicateGroupSummary[] = [HEAT];
    const approvals: unknown[] = [];
    const api = mockApi([
      ...baseRoutes({ dryRun: false }),
      { path: '/duplicate', respond: () => paged(records) },
      {
        method: 'POST',
        path: '/duplicate/1/approve',
        respond: (c: { body: unknown }) => {
          approvals.push(c.body);
          return approvals.length === 1 ? json({ message: STALE }, 409) : [{ id: 50 }];
        },
      },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Approve Heat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve “Heat (1995)”' });
    const listLoads = api.find('GET', '/duplicate').length;

    // Meanwhile a scan re-evaluated the group server-side (new signature, the 4K copy now loses).
    records = [
      makeSummary({
        signature: 'sig-1b',
        reclaimableBytes: 60 * GiB,
        files: HEAT.files.map((f) => ({ ...f, decision: f.decision === 'keep' ? ('remove' as const) : ('keep' as const) })),
      }),
    ];
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }));

    // The server's message is shown in the still-open dialog and the list is reloaded.
    expect(await within(dialog).findByText(STALE)).toBeInTheDocument();
    expect(within(dialog).getByText('Approval refused')).toBeInTheDocument();
    expect(approvals[0]).toEqual({ signature: 'sig-1' });
    await waitFor(() => expect(api.find('GET', '/duplicate').length).toBeGreaterThan(listLoads));
    expect(await within(dialog).findByText('The selection changed while the dialog was open')).toBeInTheDocument();
    const confirm = within(dialog).getByRole('button', { name: 'Approve' });
    expect(confirm).toBeDisabled();
    expect(within(dialog).getByRole('list', { name: 'Files to remove' })).toHaveTextContent('2160p');

    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    await user.click(confirm);
    await waitFor(() => expect(approvals).toHaveLength(2));
    expect(approvals[1]).toEqual({ signature: 'sig-1b' });
    expect(await screen.findByText('Approved “Heat (1995)”')).toBeInTheDocument();
  });

  it('bulk-approves with the signatures of the rows as reviewed, re-captured after a change', async () => {
    const user = userEvent.setup();
    const alien = makeSummary({ id: 3, title: 'Alien', year: 1979 });
    let records: DuplicateGroupSummary[] = [HEAT, alien];
    const api = mockApi([
      ...baseRoutes({ dryRun: true }),
      { path: '/duplicate', respond: () => paged(records) },
      { method: 'POST', path: '/duplicate/bulk', respond: { succeeded: [1, 3], failed: [] } },
    ]);
    const { queryClient } = renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Alien (1979)' });

    await user.click(screen.getByRole('button', { name: 'Select' }));
    for (const box of within(table()).getAllByRole('checkbox', { name: 'Select row' })) await user.click(box);
    await user.click(screen.getByRole('button', { name: 'Approve (2)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Approve 2 groups' });
    const confirm = within(dialog).getByRole('button', { name: 'Approve (dry run)' });
    expect(confirm).toBeEnabled();

    // Only Alien's server signature changes (same counts and decisions on screen).
    records = [HEAT, { ...alien, signature: 'sig-3b' }];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.duplicates.lists }));
    expect(await within(dialog).findByText('The selection changed while the dialog was open')).toBeInTheDocument();
    expect(confirm).toBeDisabled();

    await user.click(within(dialog).getByRole('button', { name: 'I’ve reviewed the changes' }));
    await user.click(confirm);
    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/bulk')[0]!.body).toEqual({
      ids: [1, 3],
      action: 'approve',
      signatures: { '1': 'sig-1', '3': 'sig-3b' },
    });
  });

  it('sends no signatures with bulk ignore', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...baseRoutes({ records: [HEAT] }),
      { method: 'POST', path: '/duplicate/bulk', respond: { succeeded: [1], failed: [] } },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Select' }));
    await user.click(within(table()).getByRole('checkbox', { name: 'Select row' }));
    await user.click(screen.getByRole('button', { name: 'Ignore' }));
    const dialog = await screen.findByRole('dialog', { name: 'Ignore 1 group' });
    await user.click(within(dialog).getByRole('button', { name: 'Ignore' }));
    await waitFor(() => expect(api.find('POST', '/duplicate/bulk')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/bulk')[0]!.body).toEqual({ ids: [1], action: 'ignore' });
  });

  it('starts a scan and shows it as running while the command is active', async () => {
    const user = userEvent.setup();
    const queued = { id: 9, name: 'DuplicateScan', body: {}, status: 'queued', trigger: 'manual', queued: '2026-09-22T10:00:00Z' };
    const api = mockApi([
      ...baseRoutes(),
      { method: 'POST', path: '/command', respond: json(queued, 201) },
      { path: '/command/9', respond: queued },
    ]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Scan Now' }));
    await waitFor(() => expect(api.find('POST', '/command')).toHaveLength(1));
    expect(api.find('POST', '/command')[0]!.body).toEqual({ name: 'DuplicateScan' });
    expect(await screen.findByRole('button', { name: 'Scanning' })).toBeDisabled();
    expect(screen.getByText('Scanning libraries for duplicates…')).toBeInTheDocument();
  });

  it('ignores a single group from its row with the exclusion option', async () => {
    const user = userEvent.setup();
    const api = mockApi([...baseRoutes({ records: [HEAT] }), { method: 'POST', path: '/duplicate/1/ignore', respond: { ...HEAT, status: 'ignored' } }]);
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    await user.click(screen.getByRole('button', { name: 'Ignore Heat' }));
    const dialog = await screen.findByRole('dialog', { name: 'Ignore duplicate' });
    await user.click(within(dialog).getByRole('checkbox', { name: /Also add an exclusion/ }));
    await user.click(within(dialog).getByRole('button', { name: 'Ignore' }));

    await waitFor(() => expect(api.find('POST', '/duplicate/1/ignore')).toHaveLength(1));
    expect(api.find('POST', '/duplicate/1/ignore')[0]!.body).toEqual({ addExclusion: true });
  });

  it('shows the "no media servers" empty state with a link to settings', async () => {
    mockApi(baseRoutes({ records: [], servers: [], stats: makeStats({ total: 0, byStatus: {} }) }));
    renderPage(<DuplicatesPage />);

    expect(await screen.findByText('No media servers configured')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Add Media Server' })).toHaveAttribute('href', '/settings/mediaservers');
  });

  it('offers Scan Now when no duplicates exist', async () => {
    const user = userEvent.setup();
    const api = mockApi([
      ...baseRoutes({ records: [], stats: makeStats({ total: 0, byStatus: {} }) }),
      {
        method: 'POST',
        path: '/command',
        respond: json({ id: 9, name: 'DuplicateScan', body: {}, status: 'queued', trigger: 'manual', queued: '2026-09-22T10:00:00Z' }, 201),
      },
    ]);
    renderPage(<DuplicatesPage />);

    const title = await screen.findByText('No duplicates found');
    const empty = title.parentElement!;
    await user.click(within(empty).getByRole('button', { name: 'Scan Now' }));
    await waitFor(() => expect(api.find('POST', '/command')).toHaveLength(1));
    expect(api.find('POST', '/command')[0]!.body).toEqual({ name: 'DuplicateScan' });
  });

  it('explains when the filters exclude every group', async () => {
    const user = userEvent.setup();
    mockApi(baseRoutes({ records: [], stats: makeStats({ total: 5 }) }));
    renderPage(<DuplicatesPage />, { route: '/?mediaType=episode' });

    expect(await screen.findByText('No duplicates match the current filters')).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: 'Reset filters' }).at(-1)!);
    await waitFor(() => expect(screen.queryByText('No duplicates match the current filters')).not.toBeInTheDocument());
    expect(await screen.findByText('No open duplicates')).toBeInTheDocument();
  });

  it('updates the status filter from the chips', async () => {
    const user = userEvent.setup();
    const api = mockApi(baseRoutes());
    renderPage(<DuplicatesPage />);
    await screen.findByRole('link', { name: 'Heat (1995)' });

    const filters = screen.getByRole('group', { name: 'Status' });
    await user.click(within(filters).getByRole('button', { name: 'All' }));
    await waitFor(() => expect(api.find('GET', '/duplicate').at(-1)!.query.has('status')).toBe(false));

    await user.click(within(filters).getByRole('button', { name: /^Ignored/ }));
    await waitFor(() => expect(api.find('GET', '/duplicate').at(-1)!.query.get('status')).toBe('ignored'));
  });
});
