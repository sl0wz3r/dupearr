import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { LogEntry } from '@/api/types';
import { jsonResponse, mockFetch, paged, renderWithProviders } from '@/components/activity/testUtils';
import EventsPage from './EventsPage';

const ENTRIES: LogEntry[] = [
  {
    time: '2026-09-22T10:00:03Z',
    level: 'error',
    logger: 'Executor',
    message: 'Delete failed for /data/movies/Heat.mkv',
    exception: 'permission denied\n  at executor.remove',
  },
  { time: '2026-09-22T10:00:02Z', level: 'warn', logger: 'Scanner', message: 'Plex item has no guids' },
  { time: '2026-09-22T10:00:01Z', level: 'info', logger: 'Scanner', message: 'Scan completed' },
];

const RANK: Record<string, number> = { trace: 0, debug: 1, info: 2, warn: 3, error: 4 };

function setup() {
  const mock = mockFetch(({ method, path, url }) => {
    if (method === 'GET' && path === '/api/v1/log') {
      const min = RANK[url.searchParams.get('level') ?? 'trace'] ?? 0;
      return jsonResponse(paged(ENTRIES.filter((e) => (RANK[e.level] ?? 0) >= min), { pageSize: 50 }));
    }
    return undefined;
  });
  renderWithProviders(<EventsPage />);
  return mock;
}

const logRequests = (requests: { path: string; url: URL }[]) =>
  requests.filter((r) => r.path === '/api/v1/log').map((r) => Object.fromEntries(r.url.searchParams));

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('<EventsPage>', () => {
  it('requests info and above by default and shows level badges', async () => {
    const { requests } = setup();
    expect(await screen.findByText('Scan completed')).toBeInTheDocument();
    expect(logRequests(requests)[0]).toEqual({ page: '1', pageSize: '50', level: 'info' });
    const table = within(screen.getByRole('table', { name: 'Log events' }));
    expect(table.getByText('Error')).toBeInTheDocument();
    expect(table.getByText('Warn')).toBeInTheDocument();
    expect(table.getByText('Info')).toBeInTheDocument();
    expect(table.getAllByText('Executor').length).toBeGreaterThan(0);
  });

  it('filters by minimum level, resets to page 1 and remembers the choice', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await screen.findByText('Scan completed');

    await user.selectOptions(screen.getByLabelText('Minimum level'), 'warn');

    await waitFor(() => expect(screen.queryByText('Scan completed')).not.toBeInTheDocument());
    expect(screen.getByText('Plex item has no guids')).toBeInTheDocument();
    expect(logRequests(requests).at(-1)).toEqual({ page: '1', pageSize: '50', level: 'warn' });
    expect(window.localStorage.getItem('dupearr.events.level')).toBe('"warn"');

    await user.selectOptions(screen.getByLabelText('Minimum level'), 'error');
    await waitFor(() => expect(screen.queryByText('Plex item has no guids')).not.toBeInTheDocument());
    expect(logRequests(requests).at(-1)?.level).toBe('error');
  });

  it('uses a remembered level', async () => {
    window.localStorage.setItem('dupearr.events.level', '"error"');
    const first = setup();
    await screen.findByText('Delete failed for /data/movies/Heat.mkv');
    expect(logRequests(first.requests)[0]?.level).toBe('error');
  });

  it('falls back to info for an invalid stored level', async () => {
    window.localStorage.setItem('dupearr.events.level', '"verbose"');
    const { requests } = setup();
    await screen.findByText('Scan completed');
    expect(logRequests(requests)[0]?.level).toBe('info');
  });

  it('expands exceptions', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('Scan completed');
    // Only the entry with an exception gets a toggle.
    const toggles = screen.getAllByRole('button', { name: 'Show exception' });
    expect(toggles).toHaveLength(1);

    await user.click(toggles[0]!);
    expect(screen.getByText(/permission denied/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Hide exception' })).toHaveAttribute('aria-expanded', 'true');
  });

  it('auto-refreshes every 10 seconds when enabled', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { requests } = setup();
    await screen.findByText('Scan completed');
    const before = logRequests(requests).length;

    act(() => {
      screen.getByRole('switch', { name: /Auto refresh/ }).click();
    });
    expect(window.localStorage.getItem('dupearr.events.autoRefresh')).toBe('true');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    await waitFor(() => expect(logRequests(requests).length).toBe(before + 1));

    act(() => {
      screen.getByRole('switch', { name: /Auto refresh/ }).click();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(logRequests(requests).length).toBe(before + 1);
  });
});
