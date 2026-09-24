import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/api/client';
import type { PlexPin, PlexPinStatus, PlexServer } from '@/api/types';
import { PlexSignIn } from './PlexSignIn';
import { PLEX_PIN_TIMEOUT_MS } from './plexSignInMachine';

/** Controllable stand-ins for the Plex hooks (useCreatePlexPin / usePlexPin / usePlexServers). */
const mocks = vi.hoisted(() => ({
  mutate: (() => {}) as (...args: unknown[]) => void,
  mutateCalls: [] as unknown[][],
  pin: { data: undefined as unknown, error: null as unknown },
  pinCalls: [] as [unknown, unknown][],
  servers: { data: undefined as unknown, isPending: true, isError: false, error: null as unknown },
  serverTokens: [] as unknown[],
}));

vi.mock('@/api/hooks/useMediaServers', () => ({
  useCreatePlexPin: () => ({
    mutate: (...args: unknown[]) => {
      mocks.mutateCalls.push(args);
      mocks.mutate(...args);
    },
    isPending: false,
  }),
  usePlexPin: (id: unknown, enabled: unknown) => {
    mocks.pinCalls.push([id, enabled]);
    return enabled ? { data: mocks.pin.data, error: mocks.pin.error } : { data: undefined, error: null };
  },
  usePlexServers: (token: unknown) => {
    mocks.serverTokens.push(token);
    const base = token ? mocks.servers : { data: undefined, isPending: true, isError: false, error: null };
    return { ...base, refetch: () => Promise.resolve() };
  },
}));

const AUTH_URL = 'https://app.plex.tv/auth#?clientID=dupearr&code=abcd';

interface FakePopup {
  closed: boolean;
  opener: unknown;
  location: { href: string };
  document: { title: string; body: { textContent: string } };
  close: () => void;
}

function fakePopup(): FakePopup & { closeCalls: number } {
  const popup = {
    closed: false,
    opener: window as unknown,
    location: { href: '' },
    document: { title: '', body: { textContent: '' } },
    closeCalls: 0,
    close() {
      popup.closeCalls += 1;
      popup.closed = true;
    },
  };
  return popup;
}

type MutateOpts = { onSuccess?: (pin: PlexPin) => void; onError?: (e: unknown) => void };

function lastMutateOpts(): MutateOpts {
  const call = mocks.mutateCalls.at(-1);
  if (!call) throw new Error('createPin.mutate was not called');
  return call[1] as MutateOpts;
}

function resolvePin(pin: PlexPin = { id: 42, code: 'abcd', authUrl: AUTH_URL }) {
  act(() => lastMutateOpts().onSuccess?.(pin));
}

const OWNED: PlexServer = {
  name: 'Home Server',
  clientIdentifier: 'owned-1',
  productVersion: '1.41.2',
  owned: true,
  accessToken: 'server-token',
  connections: [
    { uri: 'https://1-2-3-4.abc.plex.direct:32400', address: '1.2.3.4', port: 32400, protocol: 'https', local: false, relay: false },
    { uri: 'http://10.0.0.2:32400', address: '10.0.0.2', port: 32400, protocol: 'http', local: true, relay: false },
    { uri: 'https://relay.plex.direct:8443', address: 'relay', port: 8443, protocol: 'https', local: false, relay: true },
  ],
};

const SHARED: PlexServer = {
  name: "Friend's Server",
  clientIdentifier: 'shared-1',
  productVersion: '1.40.0',
  owned: false,
  accessToken: 'shared-token',
  connections: [{ uri: 'https://5-6-7-8.def.plex.direct:32400', address: '5.6.7.8', port: 32400, protocol: 'https', local: false, relay: false }],
};

function claimPin(token = 'account-token') {
  mocks.pin.data = { authenticated: true, authToken: token } satisfies PlexPinStatus;
}

beforeEach(() => {
  mocks.mutate = () => {};
  mocks.mutateCalls = [];
  mocks.pin = { data: undefined, error: null };
  mocks.pinCalls = [];
  mocks.servers = { data: [OWNED, SHARED], isPending: false, isError: false, error: null };
  mocks.serverTokens = [];
});

afterEach(() => {
  vi.useRealTimers();
});

describe('PlexSignIn', () => {
  it('opens a popup, polls the PIN, lists servers and hands back the chosen connection', async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    const open = vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    const onSelect = vi.fn();
    const { rerender } = render(<PlexSignIn onSelect={onSelect} />);

    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    // Popup opened synchronously inside the click, detached from this tab.
    expect(open).toHaveBeenCalledTimes(1);
    expect(popup.opener).toBeNull();
    expect(mocks.mutateCalls).toHaveLength(1);

    resolvePin();
    expect(popup.location.href).toBe(AUTH_URL);
    expect(screen.getByText(/waiting for plex sign-in/i)).toBeInTheDocument();
    expect(screen.getByText(/5:00 left/)).toBeInTheDocument();
    expect(mocks.pinCalls.at(-1)).toEqual([42, true]);

    claimPin();
    rerender(<PlexSignIn onSelect={onSelect} />);
    expect(popup.closeCalls).toBe(1);
    expect(screen.getByText(/signed in to plex/i)).toBeInTheDocument();
    expect(mocks.serverTokens).toContain('account-token');
    // Polling stops once authenticated.
    expect(mocks.pinCalls.at(-1)?.[1]).toBe(false);

    // Owned server is preselected; the local non-relay connection is recommended and listed first.
    expect(screen.getByRole('radio', { name: /home server/i })).toBeChecked();
    const items = within(screen.getByRole('list', { name: 'Connections' })).getAllByRole('listitem');
    expect(items.map((li) => li.querySelector('code')?.textContent)).toEqual([
      'http://10.0.0.2:32400',
      'https://1-2-3-4.abc.plex.direct:32400',
      'https://relay.plex.direct:8443',
    ]);
    expect(items[0]).toHaveTextContent('Recommended');
    expect(items[2]).toHaveTextContent('Relay');
    expect(screen.queryByText(/you don't own this server/i)).not.toBeInTheDocument();

    await user.click(within(items[0]!).getByRole('button', { name: 'Use' }));
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'Home Server',
        url: 'http://10.0.0.2:32400',
        token: 'server-token',
        machineIdentifier: 'owned-1',
        owned: true,
      }),
    );
    expect(within(items[0]!).getByRole('button', { name: 'Selected' })).toBeInTheDocument();
  });

  it('warns that only the owner can delete media on a shared server', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    const onSelect = vi.fn();
    const { rerender } = render(<PlexSignIn onSelect={onSelect} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    claimPin();
    rerender(<PlexSignIn onSelect={onSelect} />);

    await user.click(screen.getByRole('radio', { name: /friend's server/i }));
    expect(screen.getByText(/you don't own this server/i)).toBeInTheDocument();
    expect(screen.getByText(/only the server owner can delete media/i)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Use' }));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ token: 'shared-token', owned: false }));
  });

  // SEC-033: plex.tv listed no per-server token for a shared server. Its owner would receive the
  // plex.tv account token (full control of the account) with every request, so it is never used.
  it('refuses a shared server without its own access token instead of sending the account token', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    mocks.servers = { data: [OWNED, { ...SHARED, accessToken: '' }], isPending: false, isError: false, error: null };
    const onSelect = vi.fn();
    const { rerender } = render(<PlexSignIn onSelect={onSelect} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    claimPin();
    rerender(<PlexSignIn onSelect={onSelect} />);

    await user.click(screen.getByRole('radio', { name: /friend's server/i }));
    expect(screen.getByText(/no access token for this server/i)).toBeInTheDocument();
    const use = screen.getByRole('button', { name: 'Use' });
    expect(use).toBeDisabled();
    await user.click(use);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it('warns before using the account token for an owned server without its own token', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    mocks.servers = { data: [{ ...OWNED, accessToken: '' }], isPending: false, isError: false, error: null };
    const onSelect = vi.fn();
    const { rerender } = render(<PlexSignIn onSelect={onSelect} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    claimPin();
    rerender(<PlexSignIn onSelect={onSelect} />);

    expect(screen.getByText(/using your plex\.tv account token/i)).toBeInTheDocument();
    const items = within(screen.getByRole('list', { name: 'Connections' })).getAllByRole('listitem');
    await user.click(within(items[0]!).getByRole('button', { name: 'Use' }));
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ token: 'account-token', tokenSource: 'account', owned: true }),
    );
  });

  it('shows no token warning when plex.tv lists a server token', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    const { rerender } = render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    claimPin();
    rerender(<PlexSignIn onSelect={vi.fn()} />);
    expect(screen.queryByText(/using your plex\.tv account token/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/no access token for this server/i)).not.toBeInTheDocument();
  });

  it('offers a link when the popup is blocked and keeps polling', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(null);
    render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();

    expect(screen.getByText(/blocked the popup/i)).toBeInTheDocument();
    const link = screen.getByRole('link', { name: /open plex sign-in/i });
    expect(link).toHaveAttribute('href', AUTH_URL);
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(mocks.pinCalls.at(-1)).toEqual([42, true]);
  });

  it('times out after 5 minutes, stops polling and closes the popup', () => {
    vi.useFakeTimers();
    const popup = fakePopup();
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    render(<PlexSignIn onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    expect(screen.getByText(/waiting for plex sign-in/i)).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(PLEX_PIN_TIMEOUT_MS - 1000);
    });
    expect(screen.getByText(/0:01 left/)).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(screen.getByText(/plex sign-in timed out/i)).toBeInTheDocument();
    expect(popup.closeCalls).toBe(1);
    expect(mocks.pinCalls.at(-1)?.[1]).toBe(false);

    // Try again starts a new attempt.
    fireEvent.click(screen.getByRole('button', { name: /try again/i }));
    expect(mocks.mutateCalls).toHaveLength(2);
  });

  it('cancel closes the popup and ignores a PIN claimed afterwards', async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    const onSelect = vi.fn();
    const { rerender } = render(<PlexSignIn onSelect={onSelect} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(popup.closeCalls).toBe(1);
    expect(screen.getByRole('button', { name: /sign in with plex/i })).toBeInTheDocument();

    claimPin();
    rerender(<PlexSignIn onSelect={onSelect} />);
    expect(screen.queryByText(/signed in to plex/i)).not.toBeInTheDocument();
    expect(mocks.serverTokens.every((t) => !t)).toBe(true);
  });

  it('ignores a PIN that arrives after the attempt was abandoned', async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    const { unmount } = render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    const opts = lastMutateOpts();
    unmount();
    expect(popup.closeCalls).toBe(1);
    // Late success must not navigate the (closed) popup.
    act(() => opts.onSuccess?.({ id: 1, code: 'x', authUrl: AUTH_URL }));
    expect(popup.location.href).toBe('');
  });

  it('refuses to open an untrusted sign-in URL', async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin({ id: 42, code: 'abcd', authUrl: 'https://evil.example/auth' });

    expect(screen.getByText(/plex sign-in failed/i)).toBeInTheDocument();
    expect(screen.getByText(/unexpected sign-in address/i)).toBeInTheDocument();
    expect(popup.location.href).toBe('');
    expect(popup.closeCalls).toBe(1);
  });

  it('shows PIN creation errors and retries', async () => {
    const user = userEvent.setup();
    const popup = fakePopup();
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    act(() => lastMutateOpts().onError?.(new ApiError('plex.tv is unreachable', { status: 502 })));

    expect(screen.getByText('plex.tv is unreachable')).toBeInTheDocument();
    expect(popup.closeCalls).toBe(1);
    await user.click(screen.getByRole('button', { name: /try again/i }));
    expect(mocks.mutateCalls).toHaveLength(2);
  });

  it('ends the attempt when the PIN expires (4xx) but keeps polling on transient errors', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    const { rerender } = render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();

    mocks.pin.error = new ApiError('Upstream server error', { status: 502 });
    rerender(<PlexSignIn onSelect={vi.fn()} />);
    expect(screen.getByText(/waiting for plex sign-in/i)).toBeInTheDocument();

    mocks.pin.error = new ApiError('PIN not found', { status: 404 });
    rerender(<PlexSignIn onSelect={vi.fn()} />);
    expect(screen.getByText(/plex sign-in failed: pin not found/i)).toBeInTheDocument();
  });

  it('explains when the account has no servers', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'open').mockReturnValue(fakePopup() as unknown as Window);
    mocks.servers = { data: [], isPending: false, isError: false, error: null };
    const { rerender } = render(<PlexSignIn onSelect={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /sign in with plex/i }));
    resolvePin();
    claimPin();
    rerender(<PlexSignIn onSelect={vi.fn()} />);
    expect(screen.getByText(/no plex media servers found/i)).toBeInTheDocument();

    // "Start over" returns to the sign-in button.
    await user.click(screen.getByRole('button', { name: /start over/i }));
    expect(screen.getByRole('button', { name: /sign in with plex/i })).toBeInTheDocument();
  });
});
