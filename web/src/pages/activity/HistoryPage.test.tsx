import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Action, HistoryEvent, ScanRun } from '@/api/types';
import { jsonResponse, mockFetch, paged, renderWithProviders } from '@/components/activity/testUtils';
import HistoryPage from './HistoryPage';

const EVENTS: HistoryEvent[] = [
  {
    id: 3,
    eventType: 'fileDeleted',
    groupId: 10,
    actionId: 5,
    title: 'Heat (1995)',
    message: 'Deleted via Radarr (Radarr 4K)',
    data: { paths: ['/data/movies/Heat (1995)/Heat.1080p.mkv'], method: 'arr', permanent: true },
    createdAt: '2026-09-22T10:00:00Z',
  },
  {
    id: 2,
    eventType: 'scanCompleted',
    title: 'Scan completed',
    message: '12 groups found',
    createdAt: '2026-09-22T09:00:00Z',
  },
];

function action(overrides: Partial<Action>): Action {
  return {
    id: 5,
    groupId: 10,
    groupFileId: 100,
    versionKey: 'plex:1:100',
    title: 'Heat (1995)',
    paths: ['/data/movies/Heat (1995)/Heat.1080p.mkv'],
    size: 4_294_967_296,
    method: 'filesystem',
    status: 'succeeded',
    dryRun: false,
    recyclePath: '/recycle/Heat.1080p.mkv',
    permanent: false,
    createdAt: '2026-09-22T09:00:00Z',
    finishedAt: '2026-09-22T09:01:00Z',
    ...overrides,
  };
}

const ACTIONS: Action[] = [
  action({ id: 5 }),
  action({ id: 6, title: 'Alien (1979)', groupId: 11, method: 'arr', permanent: true, recyclePath: '' }),
  action({ id: 7, title: 'Up (2009)', groupId: 12, status: 'dry_run', dryRun: true }),
];

const SCANS: ScanRun[] = [
  {
    id: 1,
    trigger: 'scheduled',
    targeted: false,
    status: 'completed',
    stats: {
      libraries: 2,
      itemsExamined: 1500,
      groupsFound: 12,
      newGroups: 3,
      resolvedGroups: 1,
      pendingGroups: 8,
      reviewGroups: 1,
      reclaimableBytes: 53_687_091_200,
      autoApproved: 0,
      errors: 2,
    },
    error: 'Plex section 4 timed out',
    startedAt: '2026-09-22T09:00:00Z',
    finishedAt: '2026-09-22T09:02:30Z',
  },
];

function setup(route = '/activity/history') {
  const mock = mockFetch(({ method, path, url }) => {
    if (method === 'GET' && path === '/api/v1/history') {
      const types = url.searchParams.get('eventType')?.split(',');
      return jsonResponse(paged(types ? EVENTS.filter((e) => types.includes(e.eventType)) : EVENTS));
    }
    if (method === 'GET' && path === '/api/v1/action') return jsonResponse(paged(ACTIONS));
    if (method === 'POST' && path === '/api/v1/action/5/restore') return jsonResponse({});
    if (method === 'GET' && path === '/api/v1/scan') return jsonResponse(SCANS);
    return undefined;
  });
  renderWithProviders(<HistoryPage />, { route });
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<HistoryPage> events', () => {
  it('shows events with type badges, group links and expandable data', async () => {
    const user = userEvent.setup();
    setup();

    const table = within(await screen.findByRole('table', { name: 'History events' }));
    expect(await table.findByRole('link', { name: 'Heat (1995)' })).toHaveAttribute('href', '/duplicate/10');
    expect(table.getByText('File Deleted')).toBeInTheDocument();
    expect(table.getByText('Scan Completed')).toBeInTheDocument();
    // Events without data have nothing to expand.
    expect(table.getAllByRole('button', { name: /^Show details/ })).toHaveLength(1);

    await user.click(table.getByRole('button', { name: 'Show details of File Deleted event' }));
    expect(table.getByText('Permanent')).toBeInTheDocument();
    expect(table.getByText('Radarr / Sonarr')).toBeInTheDocument();
    expect(table.getByText('/data/movies/Heat (1995)/Heat.1080p.mkv')).toBeInTheDocument();
    expect(table.getByLabelText('Event data')).toHaveTextContent('"permanent": true');
  });

  it('filters by event type', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await screen.findByText('Scan Completed');

    await user.click(screen.getByRole('button', { name: /Event type: All/ }));
    await user.click(within(screen.getByRole('group', { name: 'Event type filter' })).getByLabelText('File Deleted'));

    const table = () => within(screen.getByRole('table', { name: 'History events' }));
    await waitFor(() => expect(table().queryByText('Scan Completed')).not.toBeInTheDocument());
    expect(table().getByText('File Deleted')).toBeInTheDocument();
    const last = requests.filter((r) => r.path === '/api/v1/history').at(-1)!;
    expect(last.url.searchParams.get('eventType')).toBe('fileDeleted');
    expect(last.url.searchParams.get('page')).toBe('1');
    expect(screen.getByRole('button', { name: /Event type: File Deleted/ })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Show all' }));
    expect(await table().findByText('Scan Completed')).toBeInTheDocument();
    expect(requests.filter((r) => r.path === '/api/v1/history').at(-1)?.url.searchParams.has('eventType')).toBe(false);
  });

  it('limits events to a group from the url and clears the filter back to page 1', async () => {
    const user = userEvent.setup();
    const { requests } = mockFetch(({ method, path, url }) => {
      if (method === 'GET' && path === '/api/v1/history') {
        const page = Number(url.searchParams.get('page') ?? '1');
        return jsonResponse(paged(EVENTS, { page, totalRecords: 100 }));
      }
      return undefined;
    });
    renderWithProviders(<HistoryPage />, { route: '/activity/history?groupId=10' });
    await screen.findByText('Duplicate group #10');
    expect(requests.find((r) => r.path === '/api/v1/history')?.url.searchParams.get('groupId')).toBe('10');

    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    await waitFor(() =>
      expect(requests.filter((r) => r.path === '/api/v1/history').at(-1)?.url.searchParams.get('page')).toBe('2'),
    );

    await user.click(screen.getByRole('button', { name: 'Clear group filter' }));
    await waitFor(() => expect(screen.queryByText('Duplicate group #10')).not.toBeInTheDocument());
    await waitFor(() => {
      const last = requests.filter((r) => r.path === '/api/v1/history').at(-1)!;
      expect(last.url.searchParams.has('groupId')).toBe(false);
      expect(last.url.searchParams.get('page')).toBe('1');
    });
  });

  it('labels dry-run removals as hypothetical', async () => {
    const user = userEvent.setup();
    mockFetch(({ method, path }) => {
      if (method === 'GET' && path === '/api/v1/history') {
        return jsonResponse(
          paged([
            {
              id: 9,
              eventType: 'fileDeleteDryRun',
              groupId: 10,
              title: 'Heat (1995)',
              message: 'Dry run: would delete via Plex',
              data: { paths: ['/data/movies/Heat.mkv'], method: 'plex', permanent: true, dryRun: true },
              createdAt: '2026-09-22T10:00:00Z',
            },
          ]),
        );
      }
      return undefined;
    });
    renderWithProviders(<HistoryPage />, { route: '/activity/history' });
    const table = within(await screen.findByRole('table', { name: 'History events' }));
    await user.click(await table.findByRole('button', { name: 'Show details of Dry Run Delete event' }));
    expect(table.getByText('Would be permanent')).toBeInTheDocument();
    expect(table.getByText('Removal (dry run)')).toBeInTheDocument();
    expect(table.queryByText('Permanent')).not.toBeInTheDocument();
  });
});

describe('<HistoryPage> actions', () => {
  it('shows removal permanence and restores recycle-bin removals after confirmation', async () => {
    const user = userEvent.setup();
    const { requests } = setup('/activity/history?tab=actions');

    const table = within(await screen.findByRole('table', { name: 'Actions' }));
    expect(await table.findByRole('link', { name: 'Alien (1979)' })).toBeInTheDocument();
    // Removal column + the compact copy next to the status (shown when the column is hidden on phones).
    expect(table.getAllByText('Permanent')).toHaveLength(2);
    expect(table.getAllByText('Recycle bin')).toHaveLength(2);
    expect(table.getAllByText('Deleted')).toHaveLength(2);
    expect(table.getByText('Dry Run')).toBeInTheDocument();

    // Only the succeeded filesystem removal with a recycle path is restorable.
    expect(table.getAllByRole('button', { name: /^Restore / })).toHaveLength(1);
    await user.click(table.getByRole('button', { name: 'Restore Heat (1995) from the recycle bin' }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore File' });
    expect(within(dialog).getByText('/recycle/Heat.1080p.mkv')).toBeInTheDocument();
    expect(requests.some((r) => r.method === 'POST')).toBe(false);

    await user.click(within(dialog).getByRole('button', { name: 'Restore' }));
    await waitFor(() =>
      expect(requests.filter((r) => r.method === 'POST').map((r) => r.path)).toEqual(['/api/v1/action/5/restore']),
    );
    expect(await screen.findByText('File restored')).toBeInTheDocument();

    // The action still reads "succeeded" after the refetch: a second restore is not offered.
    await waitFor(() => expect(table.queryByRole('button', { name: /^Restore / })).not.toBeInTheDocument());
    expect(table.getByText('Restored')).toBeInTheDocument();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
  });

  it('never offers restore for dry-run, permanent or non-filesystem removals', async () => {
    mockFetch(({ method, path }) => {
      if (method === 'GET' && path === '/api/v1/action') {
        return jsonResponse(
          paged([
            action({ id: 21, title: 'Dry', status: 'dry_run', dryRun: true }),
            action({ id: 22, title: 'Perm', permanent: true }),
            action({ id: 23, title: 'Arr', method: 'arr' }),
            action({ id: 24, title: 'Failed', status: 'failed' }),
            action({ id: 25, title: 'NoPath', recyclePath: '' }),
          ]),
        );
      }
      return undefined;
    });
    renderWithProviders(<HistoryPage />, { route: '/activity/history?tab=actions' });
    const table = within(await screen.findByRole('table', { name: 'Actions' }));
    expect(await table.findByRole('link', { name: 'NoPath' })).toBeInTheDocument();
    expect(table.queryByRole('button', { name: /^Restore / })).not.toBeInTheDocument();
  });

  it('filters actions by status', async () => {
    const user = userEvent.setup();
    const { requests } = setup('/activity/history?tab=actions');
    await screen.findByRole('table', { name: 'Actions' });

    await user.click(screen.getByRole('button', { name: /Status: All/ }));
    const group = within(screen.getByRole('group', { name: 'Status filter' }));
    await user.click(group.getByLabelText('Failed'));
    await user.click(group.getByLabelText('Deleted'));

    await waitFor(() =>
      expect(requests.filter((r) => r.path === '/api/v1/action').at(-1)?.url.searchParams.get('status')).toBe(
        'succeeded,failed',
      ),
    );
  });
});

describe('<HistoryPage> scans', () => {
  it('shows scan runs with duration, type and stats', async () => {
    setup('/activity/history?tab=scans');
    const table = within(await screen.findByRole('table', { name: 'Scans' }));
    expect(await table.findByText('Completed')).toBeInTheDocument();
    expect(table.getByText('2m 30s')).toBeInTheDocument();
    expect(table.getByText('Full')).toBeInTheDocument();
    expect(table.getByText('50.0 GiB')).toBeInTheDocument();
    expect(table.getByText('2 errors')).toBeInTheDocument();
    expect(table.getByText('Plex section 4 timed out')).toBeInTheDocument();
  });
});
