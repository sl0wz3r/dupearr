import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Command, ScheduledTask } from '@/api/types';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import TasksPage from './TasksPage';

const TASKS: ScheduledTask[] = [
  {
    name: 'Check Health',
    taskName: 'CheckHealth',
    interval: 360,
    lastExecution: '2026-09-22T09:00:00Z',
    lastStartTime: '2026-09-22T08:59:58Z',
    lastDuration: '00:00:01.8120000',
    nextExecution: '2026-09-22T15:00:00Z',
  },
  { name: 'Duplicate Scan', taskName: 'DuplicateScan', interval: 0, lastDuration: '' },
  { name: 'Backup', taskName: 'Backup', interval: 10_080 },
  { name: 'Clean Recycle Bin', taskName: 'CleanRecycleBin', interval: 1440 },
];

const COMMANDS: Command[] = [
  {
    id: 2,
    name: 'DuplicateScan',
    body: {},
    status: 'started',
    trigger: 'manual',
    message: 'Scanning Movies',
    queued: '2026-09-22T10:00:00Z',
    started: '2026-09-22T10:00:01Z',
  },
  {
    id: 1,
    name: 'Backup',
    body: {},
    status: 'failed',
    trigger: 'scheduled',
    message: 'disk full',
    queued: '2026-09-22T09:00:00Z',
    started: '2026-09-22T09:00:00Z',
    ended: '2026-09-22T09:00:12Z',
    duration: '00:00:12.345',
  },
];

function setup(recycleBinCleanupDays = 7) {
  const mock = mockFetch(({ method, path, body }) => {
    if (method === 'GET' && path === '/api/v1/system/task') return jsonResponse(TASKS);
    if (method === 'GET' && path === '/api/v1/config/settings') return jsonResponse({ recycleBinCleanupDays });
    if (method === 'GET' && path === '/api/v1/command') return jsonResponse(COMMANDS);
    if (method === 'POST' && path === '/api/v1/command') {
      const name = (body as { name: string }).name;
      return jsonResponse({ id: 3, name, body: {}, status: 'queued', trigger: 'manual', queued: '' }, 201);
    }
    return undefined;
  });
  renderWithProviders(<TasksPage />);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<TasksPage>', () => {
  it('lists scheduled tasks with formatted interval and duration', async () => {
    setup();
    const table = within(await screen.findByRole('table', { name: 'Scheduled tasks' }));
    expect(await table.findByText('Check Health')).toBeInTheDocument();
    expect(table.getByText('6h')).toBeInTheDocument();
    expect(table.getByText('2s')).toBeInTheDocument();
    expect(table.getByText('Disabled')).toBeInTheDocument();
  });

  it('runs a task now and disables tasks whose command is already running', async () => {
    const user = userEvent.setup();
    const { requests } = setup();

    const scan = await screen.findByRole('button', { name: 'Run Duplicate Scan now' });
    await waitFor(() => expect(scan).toBeDisabled());

    await user.click(screen.getByRole('button', { name: 'Run Check Health now' }));
    await waitFor(() => {
      const post = requests.find((r) => r.method === 'POST' && r.path === '/api/v1/command');
      expect(post?.body).toEqual({ name: 'CheckHealth' });
    });
    expect(await screen.findByText('Check Health started')).toBeInTheDocument();
  });

  it('runs Backup by hand as a manual backup', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await user.click(await screen.findByRole('button', { name: 'Run Backup now' }));
    await waitFor(() => {
      const post = requests.find((r) => r.method === 'POST' && r.path === '/api/v1/command');
      expect(post?.body).toEqual({ name: 'Backup', type: 'manual' });
    });
  });

  it('asks before running Clean Recycle Bin (permanent deletion)', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await user.click(await screen.findByRole('button', { name: 'Run Clean Recycle Bin now' }));

    const dialog = await screen.findByRole('dialog', { name: 'Run Clean Recycle Bin' });
    expect(await within(dialog).findByText(/\(7 days\) are deleted permanently/)).toBeInTheDocument();
    expect(requests.some((r) => r.method === 'POST')).toBe(false);

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(requests.some((r) => r.method === 'POST')).toBe(false);

    await user.click(screen.getByRole('button', { name: 'Run Clean Recycle Bin now' }));
    const again = await screen.findByRole('dialog', { name: 'Run Clean Recycle Bin' });
    await user.click(within(again).getByRole('button', { name: 'Run Now' }));
    await waitFor(() => {
      const posts = requests.filter((r) => r.method === 'POST' && r.path === '/api/v1/command');
      expect(posts.map((r) => r.body)).toEqual([{ name: 'CleanRecycleBin' }]);
    });
  });

  it('says that a retention of 0 days keeps everything (the server cleans nothing then)', async () => {
    const user = userEvent.setup();
    setup(0);
    await user.click(await screen.findByRole('button', { name: 'Run Clean Recycle Bin now' }));
    const dialog = await screen.findByRole('dialog', { name: 'Run Clean Recycle Bin' });
    expect(await within(dialog).findByText(/cleanup is off: nothing will be deleted/i)).toBeInTheDocument();
    expect(within(dialog).queryByText(/are deleted permanently/)).not.toBeInTheDocument();
  });

  it('lists recent commands with status, trigger, message and duration', async () => {
    setup();
    const table = within(await screen.findByRole('table', { name: 'Command queue' }));
    expect(await table.findByText('Duplicate Scan')).toBeInTheDocument();
    expect(table.getByText('Running')).toBeInTheDocument();
    expect(table.getByText('Failed')).toBeInTheDocument();
    expect(table.getByText('Scheduled')).toBeInTheDocument();
    expect(table.getByText('disk full')).toBeInTheDocument();
    expect(table.getByText('12s')).toBeInTheDocument();
  });
});
