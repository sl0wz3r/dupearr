/** Jellyfin server form (issue #4, docs/DECISIONS.md D12) with mocked API hooks — no network. */
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MediaServer } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { jellyfinFormValues, jellyfinPayload, validateJellyfinServer } from './JellyfinServerForm';
import { MediaServerModal } from './MediaServerModal';

type UseState = typeof import('react').useState;
type Opts = { onSuccess?: (data: unknown, vars: unknown) => void; onError?: (e: unknown) => void };

const mocks = vi.hoisted(() => {
  const calls: Record<string, unknown[]> = {};
  const results: Record<string, unknown> = {};
  /** A useMutation stand-in: records variables, resolves with `results[name]` (an Error rejects). */
  const mutation = (name: string, useState: UseState) => () => {
    const [state, setState] = useState<{ data?: unknown; error?: unknown }>({});
    return {
      ...state,
      error: state.error ?? null,
      isPending: false,
      reset: () => setState({}),
      mutate: (vars: unknown, opts?: Opts) => {
        (calls[name] ??= []).push(vars);
        const r = results[name];
        if (r instanceof Error) {
          setState({ error: r });
          opts?.onError?.(r);
        } else {
          setState({ data: r ?? {} });
          opts?.onSuccess?.(r, vars);
        }
      },
    };
  };
  const servers: { list: unknown[] } = { list: [] };
  return { calls, results, mutation, servers };
});

vi.mock('@/api/hooks/useMediaServers', async () => {
  const { useState } = await import('react');
  return {
    useCreateMediaServer: mocks.mutation('createServer', useState),
    useUpdateMediaServer: mocks.mutation('updateServer', useState),
    useDeleteMediaServer: mocks.mutation('deleteServer', useState),
    useTestMediaServer: mocks.mutation('testServer', useState),
    useMediaServers: () => ({ data: mocks.servers.list, isPending: false, isError: false, error: null }),
    useCreatePlexPin: mocks.mutation('createPin', useState),
    usePlexPin: () => ({ data: undefined, error: null }),
    usePlexServers: () => ({ data: undefined, isPending: true, isError: false, error: null, refetch: () => Promise.resolve() }),
  };
});

beforeEach(() => {
  for (const k of Object.keys(mocks.calls)) delete mocks.calls[k];
  for (const k of Object.keys(mocks.results)) delete mocks.results[k];
  mocks.servers.list = [];
});

/** Lets the modal's initial-focus animation frame run so it doesn't steal focus mid-typing. */
async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 40)));
}

const KEY = 'a11ce0000000000000000000000000e7';

const SAVED: MediaServer = {
  id: 7,
  name: 'Jelly',
  kind: 'jellyfin',
  url: 'http://jellyfin:8096',
  token: MASKED_SECRET,
  machineIdentifier: '4a9f0000000000000000000000001201',
  verifyTls: true,
  enabled: true,
  createdAt: '',
  updatedAt: '',
};

describe('Jellyfin form helpers', () => {
  it('validates the URL and the API key', () => {
    const values = { ...jellyfinFormValues(null), url: 'jellyfin:8096' };
    const errors = validateJellyfinServer(values);
    expect(errors.url?.length).toBe(1);
    expect(errors.token?.[0]).toMatch(/API key is required/);
    expect(validateJellyfinServer({ ...values, url: 'http://jellyfin:8096', apiKey: KEY })).toEqual({});
  });

  it('refuses the web client address', () => {
    const values = { ...jellyfinFormValues(null), apiKey: KEY };
    for (const url of ['http://jellyfin:8096/web', 'http://jellyfin:8096/web/', 'http://host/jellyfin/web/index.html']) {
      expect(validateJellyfinServer({ ...values, url }).url?.[0]).toMatch(/without "\/web"/);
    }
    expect(validateJellyfinServer({ ...values, url: 'http://host/jellyfin' })).toEqual({});
  });

  it('keeps a stored key only for the address it was saved for', () => {
    const values = jellyfinFormValues(SAVED);
    expect(values.apiKey).toBe(MASKED_SECRET);
    expect(validateJellyfinServer(values, SAVED)).toEqual({});
    expect(validateJellyfinServer({ ...values, url: 'http://jellyfin:8096/jf' }, SAVED)).toEqual({});
    expect(validateJellyfinServer({ ...values, url: 'http://other:8096' }, SAVED).token?.[0]).toMatch(
      /Enter the API key again/,
    );
  });

  it('builds a Jellyfin payload', () => {
    const values = { ...jellyfinFormValues(null), name: ' Jelly ', url: 'http://jellyfin:8096/', apiKey: ` ${KEY} ` };
    expect(jellyfinPayload(values)).toEqual({
      name: 'Jelly',
      kind: 'jellyfin',
      url: 'http://jellyfin:8096',
      token: KEY,
      verifyTls: true,
      enabled: true,
    });
    expect(jellyfinPayload(jellyfinFormValues(SAVED), SAVED.id, true)).toMatchObject({
      id: 7,
      token: MASKED_SECRET,
      storage: '',
    });
  });
});

describe('JellyfinServerForm', () => {
  it('tests (administrator, server id, removals disabled) and saves a new server', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<MediaServerModal server={null} kind="jellyfin" onClose={onClose} />);
    await settle();
    expect(screen.getByText(/never deletes anything through Jellyfin/)).toBeInTheDocument();

    await user.type(screen.getByLabelText('URL'), 'http://jellyfin:8096');
    await user.type(screen.getByLabelText('API Key'), KEY);
    mocks.results.testServer = {
      version: '12.1.0',
      machineIdentifier: SAVED.machineIdentifier,
      friendlyName: 'Home Jellyfin',
      product: 'Jellyfin Server',
      administrator: true,
      mediaDeletionAllowed: false,
      removalsDisabled: 'Jellyfin has path substitutions configured',
    };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Connected to Jellyfin')).toBeInTheDocument();
    expect(screen.getByText('Home Jellyfin')).toBeInTheDocument();
    expect(screen.getByText('Jellyfin Server')).toBeInTheDocument();
    expect(screen.getByText('Server ID').nextElementSibling).toHaveTextContent(SAVED.machineIdentifier);
    expect(screen.getByText('Administrator').nextElementSibling).toHaveTextContent('✓');
    expect(screen.getByText('Removals from this server are disabled')).toBeInTheDocument();
    // No Plex-only wording.
    expect(screen.queryByText(/Allow media deletion/)).not.toBeInTheDocument();

    mocks.results.createServer = { id: 1, name: 'Jellyfin' };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.createServer).toEqual([
      {
        server: { name: 'Jellyfin', kind: 'jellyfin', url: 'http://jellyfin:8096', token: KEY, verifyTls: true, enabled: true },
        forceSave: false,
      },
    ]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('shows a credential that is not an administrator', async () => {
    const user = userEvent.setup();
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    mocks.results.testServer = { version: '12.1.0', machineIdentifier: 'x', friendlyName: 'J', administrator: false };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(mocks.calls.testServer).toEqual([expect.objectContaining({ id: 7, kind: 'jellyfin', token: MASKED_SECRET })]);
    expect(screen.getByText('Administrator').nextElementSibling).toHaveTextContent('No');
    expect(screen.queryByText('Removals from this server are disabled')).not.toBeInTheDocument();
  });

  it('warns when a saved server now reaches another Jellyfin server', async () => {
    const user = userEvent.setup();
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    mocks.results.testServer = { version: '12.1.0', machineIdentifier: '0000000000000000000000000000beef', friendlyName: 'J', administrator: true };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Different Jellyfin server')).toBeInTheDocument();
    expect(screen.getByText(/add it as a new media server instead/)).toBeInTheDocument();
  });

  it('never sends the stored API key to a new host', async () => {
    const user = userEvent.setup();
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    const url = screen.getByLabelText('URL');
    await user.clear(url);
    await user.type(url, 'http://elsewhere:8096');
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText(/Enter the API key again/)).toBeInTheDocument();
    expect(mocks.calls.testServer).toBeUndefined();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateServer).toBeUndefined();
  });

  it('shows the Storage field with two servers', () => {
    mocks.servers.list = [SAVED, { ...SAVED, id: 8, kind: 'plex', name: 'Plex' }];
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    expect(screen.getByLabelText('Storage')).toBeInTheDocument();
  });
});
