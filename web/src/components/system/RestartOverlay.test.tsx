import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { configureApi, onUnauthorized } from '@/api/client';
import { jsonResponse, mockFetch } from '@/components/activity/testUtils';
import { checkRestarted, pingServer, RestartOverlay, type RestartCheck } from './RestartOverlay';

/** A ping that answers from a scripted sequence (the last answer repeats). */
function scriptedPing(answers: boolean[]) {
  let i = 0;
  return vi.fn(async () => answers[Math.min(i++, answers.length - 1)]!);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<RestartOverlay>', () => {
  it('renders nothing and does not poll while closed', async () => {
    const ping = scriptedPing([true]);
    render(<RestartOverlay open={false} ping={ping} pollIntervalMs={1} onReady={vi.fn()} />);
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(ping).not.toHaveBeenCalled();
  });

  it('reloads once the server went down and came back', async () => {
    const onReady = vi.fn();
    const ping = scriptedPing([true, false, false, true]);
    render(<RestartOverlay open ping={ping} pollIntervalMs={5} graceMs={60_000} onReady={onReady} />);

    expect(screen.getByRole('alertdialog', { name: 'Restarting…' })).toBeInTheDocument();
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
    expect(ping).toHaveBeenCalledTimes(4);
    // Polling stops after the reload was triggered.
    await new Promise((r) => setTimeout(r, 30));
    expect(ping).toHaveBeenCalledTimes(4);
    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it('keeps waiting while the server is still up before the grace period (restart not begun)', async () => {
    const onReady = vi.fn();
    const ping = scriptedPing([true]);
    render(<RestartOverlay open ping={ping} pollIntervalMs={5} graceMs={60_000} onReady={onReady} />);
    await waitFor(() => expect(ping.mock.calls.length).toBeGreaterThanOrEqual(3));
    expect(onReady).not.toHaveBeenCalled();
  });

  it('treats an always-up server as restarted after the grace period (fast restarts)', async () => {
    const onReady = vi.fn();
    render(<RestartOverlay open ping={scriptedPing([true])} pollIntervalMs={5} graceMs={20} onReady={onReady} />);
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
  });

  it('never reloads into a process that has not restarted, and offers a restart', async () => {
    const user = userEvent.setup();
    const onReady = vi.fn();
    const onRestartRequest = vi.fn();
    const verify = vi.fn(async (): Promise<RestartCheck> => 'same');
    render(
      <RestartOverlay
        open
        ping={scriptedPing([true])}
        verify={verify}
        onRestartRequest={onRestartRequest}
        pollIntervalMs={5}
        graceMs={20}
        onReady={onReady}
      />,
    );
    expect(await screen.findByText(/has not restarted yet/)).toBeInTheDocument();
    // Well past the grace period: still waiting.
    await new Promise((r) => setTimeout(r, 60));
    expect(onReady).not.toHaveBeenCalled();
    expect(verify.mock.calls.length).toBeGreaterThan(1);

    await user.click(screen.getByRole('button', { name: 'Restart Now' }));
    expect(onRestartRequest).toHaveBeenCalledTimes(1);
  });

  it('reloads as soon as verify confirms a new process (fast restart)', async () => {
    const onReady = vi.fn();
    const verify = vi.fn(async (): Promise<RestartCheck> => 'restarted');
    render(
      <RestartOverlay open ping={scriptedPing([true])} verify={verify} pollIntervalMs={5} graceMs={60_000} onReady={onReady} />,
    );
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
    expect(verify).toHaveBeenCalledTimes(1);
  });

  it('falls back to the grace period when verify is inconclusive', async () => {
    const onReady = vi.fn();
    const verify = vi.fn(async (): Promise<RestartCheck> => 'unknown');
    render(<RestartOverlay open ping={scriptedPing([true])} verify={verify} pollIntervalMs={5} graceMs={20} onReady={onReady} />);
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
  });

  it('reloads after down → up without asking verify', async () => {
    const onReady = vi.fn();
    const verify = vi.fn(async (): Promise<RestartCheck> => 'same');
    render(
      <RestartOverlay open ping={scriptedPing([false, true])} verify={verify} pollIntervalMs={5} graceMs={60_000} onReady={onReady} />,
    );
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
    expect(verify).not.toHaveBeenCalled();
  });

  it('offers a manual reload', async () => {
    const user = userEvent.setup();
    const onReady = vi.fn();
    render(<RestartOverlay open ping={scriptedPing([false])} pollIntervalMs={60_000} onReady={onReady} />);
    await user.click(screen.getByRole('button', { name: 'Reload now' }));
    expect(onReady).toHaveBeenCalledTimes(1);
  });

  it('stops polling when unmounted', async () => {
    const ping = scriptedPing([false]);
    const { unmount } = render(<RestartOverlay open ping={ping} pollIntervalMs={5} onReady={vi.fn()} />);
    await waitFor(() => expect(ping).toHaveBeenCalled());
    unmount();
    const calls = ping.mock.calls.length;
    await new Promise((r) => setTimeout(r, 30));
    expect(ping.mock.calls.length).toBe(calls);
  });
});

describe('pingServer', () => {
  it('is true for a 2xx /ping and false for errors or failures', async () => {
    const { requests } = mockFetch(({ path }) => (path === '/ping' ? jsonResponse({ status: 'OK' }) : undefined));
    await expect(pingServer()).resolves.toBe(true);
    expect(requests[0]?.path).toBe('/ping');

    mockFetch(() => jsonResponse({ status: 'Error' }, 500));
    await expect(pingServer()).resolves.toBe(false);

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch');
      }),
    );
    await expect(pingServer()).resolves.toBe(false);
  });
});

describe('checkRestarted', () => {
  const START = '2026-09-22T10:00:00Z';

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    ['same start time', () => jsonResponse({ startTime: '2026-09-22T10:00:00.000Z' }), 'same'],
    ['new start time', () => jsonResponse({ startTime: '2026-09-22T10:05:00Z' }), 'restarted'],
    ['api key rejected by the restored config', () => jsonResponse({ message: 'Unauthorized' }, 401), 'restarted'],
    ['server error', () => jsonResponse({ message: 'boom' }, 500), 'unknown'],
    ['no start time', () => jsonResponse({}), 'unknown'],
  ] as const)('%s → %s', async (_name, respond, expected) => {
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
    const { requests } = mockFetch(({ path }) => (path === '/api/v1/system/status' ? respond() : undefined));
    const unauthorized = vi.fn();
    const unsubscribe = onUnauthorized(unauthorized);
    try {
      await expect(checkRestarted(START)).resolves.toBe(expected);
    } finally {
      unsubscribe();
    }
    expect(requests).toHaveLength(1);
    // Never bounces the overlay to the login page.
    expect(unauthorized).not.toHaveBeenCalled();
  });

  it('is unknown without a reference start time or when the network fails', async () => {
    const { requests } = mockFetch(() => jsonResponse({ startTime: START }));
    await expect(checkRestarted(undefined)).resolves.toBe('unknown');
    await expect(checkRestarted('0001-01-01T00:00:00Z')).resolves.toBe('unknown');
    expect(requests).toHaveLength(0);

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch');
      }),
    );
    await expect(checkRestarted(START)).resolves.toBe('unknown');
  });
});
