import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Action, Command } from '@/api/types';
import { jsonResponse, mockFetch, paged, renderWithProviders } from '@/components/activity/testUtils';
import QueuePage from './QueuePage';

function action(overrides: Partial<Action>): Action {
  return {
    id: 1,
    groupId: 10,
    groupFileId: 100,
    versionKey: 'plex:1:100',
    title: 'Heat (1995)',
    paths: ['/data/movies/Heat (1995)/Heat.1995.1080p.mkv'],
    size: 8_589_934_592,
    method: '',
    status: 'pending',
    dryRun: false,
    permanent: false,
    createdAt: '2026-09-20T10:00:00Z',
    ...overrides,
  };
}

function setup(initial: Action[], commands: Command[] = []) {
  let queue = [...initial];
  const mock = mockFetch(({ method, path }) => {
    if (method === 'GET' && path === '/api/v1/queue') return jsonResponse(paged(queue));
    if (method === 'GET' && path === '/api/v1/command') return jsonResponse(commands);
    const cancel = /^\/api\/v1\/queue\/(\d+)$/.exec(path);
    if (method === 'DELETE' && cancel) {
      queue = queue.filter((a) => a.id !== Number(cancel[1]));
      return jsonResponse({});
    }
    if (method === 'POST' && path === '/api/v1/command') {
      return jsonResponse(
        { id: 99, name: 'ProcessQueue', body: {}, status: 'queued', trigger: 'manual', queued: '2026-09-22T00:00:00Z' },
        201,
      );
    }
    return undefined;
  });
  renderWithProviders(<QueuePage />);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<QueuePage>', () => {
  it('lists queued actions with status, dry-run badge and a link to the group', async () => {
    setup([
      action({ id: 1 }),
      action({
        id: 2,
        groupId: 11,
        title: 'Alien (1979)',
        status: 'running',
        dryRun: true,
        paths: ['/data/movies/Alien (1979)/cd1.avi', '/data/movies/Alien (1979)/cd2.avi'],
      }),
    ]);

    const link = await screen.findByRole('link', { name: 'Heat (1995)' });
    expect(link).toHaveAttribute('href', '/duplicate/10');
    expect(screen.getByRole('link', { name: 'Alien (1979)' })).toHaveAttribute('href', '/duplicate/11');
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getByText('Dry Run')).toBeInTheDocument();
    expect(screen.getByText('Heat.1995.1080p.mkv')).toBeInTheDocument();
    // Stacked parts: the first file plus a "+1" count; every path is in the tooltip.
    expect(screen.getByText('cd1.avi')).toBeInTheDocument();
    expect(screen.getByText('+1')).toBeInTheDocument();
    expect(screen.getByTitle(/cd1\.avi\s+\/data\/movies\/Alien \(1979\)\/cd2\.avi$/)).toHaveAttribute(
      'title',
      '/data/movies/Alien (1979)/cd1.avi\n/data/movies/Alien (1979)/cd2.avi',
    );
    expect(screen.getByText(/and 1 more: \/data\/movies\/Alien \(1979\)\/cd2\.avi/)).toBeInTheDocument();
    // Only pending actions can be cancelled.
    expect(screen.getByRole('button', { name: 'Cancel removal of Heat (1995)' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Cancel removal of Alien (1979)' })).toBeDisabled();
  });

  it('cancels a pending action only after confirmation', async () => {
    const user = userEvent.setup();
    const { requests } = setup([action({ id: 1 }), action({ id: 2, groupId: 11, title: 'Alien (1979)' })]);

    await user.click(await screen.findByRole('button', { name: 'Cancel removal of Heat (1995)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel Removal' });
    expect(within(dialog).getByText('Heat (1995)')).toBeInTheDocument();
    expect(requests.some((r) => r.method === 'DELETE')).toBe(false);

    await user.click(within(dialog).getByRole('button', { name: 'Cancel Removal' }));

    await waitFor(() =>
      expect(requests.filter((r) => r.method === 'DELETE').map((r) => r.path)).toEqual(['/api/v1/queue/1']),
    );
    expect(await screen.findByText('Removal cancelled')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Heat (1995)' })).not.toBeInTheDocument());
    expect(screen.getByRole('link', { name: 'Alien (1979)' })).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('keeps the action when the confirmation is dismissed', async () => {
    const user = userEvent.setup();
    const { requests } = setup([action({ id: 1 })]);

    await user.click(await screen.findByRole('button', { name: 'Cancel removal of Heat (1995)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel Removal' });
    await user.click(within(dialog).getByRole('button', { name: 'Keep in Queue' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(requests.some((r) => r.method === 'DELETE')).toBe(false);
    expect(screen.getByRole('link', { name: 'Heat (1995)' })).toBeInTheDocument();
  });

  it('shows an error toast when cancelling fails', async () => {
    const user = userEvent.setup();
    mockFetch(({ method, path }) => {
      if (method === 'GET' && path === '/api/v1/queue') return jsonResponse(paged([action({ id: 1 })]));
      if (method === 'GET' && path === '/api/v1/command') return jsonResponse([]);
      if (method === 'DELETE') return jsonResponse({ message: 'Database is locked' }, 500);
      return undefined;
    });
    renderWithProviders(<QueuePage />);

    await user.click(await screen.findByRole('button', { name: 'Cancel removal of Heat (1995)' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel Removal' });
    await user.click(within(dialog).getByRole('button', { name: 'Cancel Removal' }));

    expect(await screen.findByText('Unable to cancel removal')).toBeInTheDocument();
    expect(screen.getByText('Database is locked')).toBeInTheDocument();
  });

  it.each([
    ['running', 'The removal is already running and cannot be cancelled'],
    ['succeeded', 'Only pending actions can be cancelled (this one is succeeded)'],
  ] as const)(
    "shows the server's 409 message and refreshes the queue when the action is already %s",
    async (now, message) => {
      const user = userEvent.setup();
      let status: Action['status'] = 'pending';
      const { requests } = mockFetch(({ method, path }) => {
        if (method === 'GET' && path === '/api/v1/queue') {
          return jsonResponse(paged(status === 'succeeded' ? [] : [action({ id: 1, status })]));
        }
        if (method === 'GET' && path === '/api/v1/command') return jsonResponse([]);
        if (method === 'DELETE') {
          // The executor picked the action up between the list load and the click.
          status = now;
          return jsonResponse({ message }, 409);
        }
        return undefined;
      });
      renderWithProviders(<QueuePage />);

      await user.click(await screen.findByRole('button', { name: 'Cancel removal of Heat (1995)' }));
      const dialog = await screen.findByRole('dialog', { name: 'Cancel Removal' });
      const loads = requests.filter((r) => r.method === 'GET' && r.path === '/api/v1/queue').length;
      await user.click(within(dialog).getByRole('button', { name: 'Cancel Removal' }));

      expect(await screen.findByText('Removal can no longer be cancelled')).toBeInTheDocument();
      expect(screen.getByText(message)).toBeInTheDocument();
      expect(screen.queryByText('Unable to cancel removal')).not.toBeInTheDocument();
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      // The list is reloaded so it shows the action's real state.
      await waitFor(() =>
        expect(requests.filter((r) => r.method === 'GET' && r.path === '/api/v1/queue').length).toBeGreaterThan(loads),
      );
      if (now === 'running') {
        expect(await screen.findByText('Running')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Cancel removal of Heat (1995)' })).toBeDisabled();
      } else {
        expect(await screen.findByText('Queue is empty')).toBeInTheDocument();
      }
    },
  );

  it('runs ProcessQueue from the toolbar', async () => {
    const user = userEvent.setup();
    const { requests } = setup([action({ id: 1 })]);

    await user.click(await screen.findByRole('button', { name: 'Process Queue' }));

    await waitFor(() => {
      const post = requests.find((r) => r.method === 'POST' && r.path === '/api/v1/command');
      expect(post?.body).toEqual({ name: 'ProcessQueue' });
    });
  });

  it('disables Process Queue while a ProcessQueue command is active', async () => {
    setup(
      [action({ id: 1 })],
      [{ id: 5, name: 'ProcessQueue', body: {}, status: 'started', trigger: 'scheduled', queued: '2026-09-22T00:00:00Z' }],
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'Process Queue' })).toBeDisabled());
  });

  it('requests the queue oldest first (processing order)', async () => {
    const { requests } = setup([action({ id: 1 })]);
    await screen.findByRole('link', { name: 'Heat (1995)' });
    const params = requests.find((r) => r.path === '/api/v1/queue')!.url.searchParams;
    expect(params.get('sortKey')).toBe('createdAt');
    expect(params.get('sortDirection')).toBe('ascending');
    expect(params.get('page')).toBe('1');
  });

  it('closes the confirmation without cancelling when the action starts running meanwhile', async () => {
    const user = userEvent.setup();
    let status: Action['status'] = 'pending';
    const { requests } = mockFetch(({ method, path }) => {
      if (method === 'GET' && path === '/api/v1/queue') return jsonResponse(paged([action({ id: 1, status })]));
      if (method === 'GET' && path === '/api/v1/command') return jsonResponse([]);
      return undefined;
    });
    const { queryClient } = renderWithProviders(<QueuePage />);

    await user.click(await screen.findByRole('button', { name: 'Cancel removal of Heat (1995)' }));
    await screen.findByRole('dialog', { name: 'Cancel Removal' });

    // A `queue` server event refreshes the list: the executor picked the action up.
    status = 'running';
    await queryClient.invalidateQueries({ queryKey: ['queue'] });

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByText('Removal already started')).toBeInTheDocument();
    expect(requests.some((r) => r.method === 'DELETE')).toBe(false);
  });

  it('shows an empty state when nothing is queued', async () => {
    setup([]);
    expect(await screen.findByText('Queue is empty')).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: 'Pagination' })).not.toBeInTheDocument();
  });
});
