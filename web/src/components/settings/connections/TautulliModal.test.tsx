/** Tautulli connection modal (docs/DECISIONS.md D10) with mocked API hooks — no network. */
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/api/client';
import type { MediaServer, TautulliInstance } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { TautulliModal } from './TautulliModal';

type UseState = typeof import('react').useState;
type Opts = { onSuccess?: (data: unknown, vars: unknown) => void; onError?: (e: unknown) => void };

const mocks = vi.hoisted(() => {
  const calls: Record<string, unknown[]> = {};
  const results: Record<string, unknown> = {};
  const resolve = (name: string, vars: unknown) => {
    const r = results[name];
    return typeof r === 'function' ? (r as (v: unknown) => unknown)(vars) : r;
  };
  const mutation = (name: string, useState: UseState) => () => {
    const [state, setState] = useState<{ data?: unknown; error?: unknown }>({});
    return {
      ...state,
      error: state.error ?? null,
      isPending: false,
      reset: () => setState({}),
      mutate: (vars: unknown, opts?: Opts) => {
        (calls[name] ??= []).push(vars);
        const r = resolve(name, vars);
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
  return { calls, results, mutation };
});

vi.mock('@/api/hooks/useTautulli', async () => {
  const { useState } = await import('react');
  return {
    useCreateTautulli: mocks.mutation('create', useState),
    useUpdateTautulli: mocks.mutation('update', useState),
    useDeleteTautulli: mocks.mutation('delete', useState),
    useTestTautulli: mocks.mutation('test', useState),
  };
});

beforeEach(() => {
  for (const k of Object.keys(mocks.calls)) delete mocks.calls[k];
  for (const k of Object.keys(mocks.results)) delete mocks.results[k];
});

const KEY = 'abcdef0123456789abcdef0123456789';
const SERVERS = [
  { id: 1, name: 'Plex', kind: 'plex', url: 'http://plex:32400', token: MASKED_SECRET, machineIdentifier: 'm1', verifyTls: true, enabled: true },
  { id: 2, name: 'Plex 2', kind: 'plex', url: 'http://plex2:32400', token: MASKED_SECRET, machineIdentifier: 'm2', verifyTls: true, enabled: true },
] as MediaServer[];

async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 40)));
}

describe('TautulliModal', () => {
  it('tests (version, monitored server, history coverage) and saves a new connection', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<TautulliModal instance={null} servers={SERVERS} onClose={onClose} />);
    await settle();
    expect(screen.getByRole('dialog', { name: 'Add Tautulli' })).toBeInTheDocument();
    expect(screen.getByText(/Requires Tautulli 2\.18\.0 or later/)).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText('Plex Server'), '2');
    await user.type(screen.getByLabelText('URL'), 'http://tautulli:8181/');
    await user.type(screen.getByLabelText('API Key'), KEY);

    mocks.results.test = {
      version: 'v2.18.1',
      pmsName: 'Plex 2',
      pmsIdentifier: 'm2',
      serverMatches: true,
      historySince: '2025-03-01T20:00:00Z',
      librariesWithoutHistory: ['Movies 4K'],
      usersWithoutHistory: 2,
    };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Connected to Tautulli')).toBeInTheDocument();
    expect(screen.getByText('v2.18.1')).toBeInTheDocument();
    expect(screen.getByText('✓').parentElement).toHaveTextContent('Plex 2 ✓');
    expect(screen.getByText('Libraries without history: Movies 4K.')).toBeInTheDocument();
    expect(screen.getByText(/2 users have “Keep History” turned off/)).toBeInTheDocument();
    expect(mocks.calls.test).toEqual([
      expect.objectContaining({ serverId: 2, url: 'http://tautulli:8181', apiKey: KEY, verifyTls: true, enabled: true }),
    ]);

    mocks.results.create = (vars: unknown) =>
      (vars as { forceSave: boolean }).forceSave ? { id: 1, name: 'Tautulli' } : new ApiError('Unable to connect to Tautulli', { status: 502 });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('Unable to connect to Tautulli')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save anyway' }));
    expect(mocks.calls.create).toEqual([
      { instance: expect.objectContaining({ serverId: 2, name: 'Tautulli' }), forceSave: false },
      { instance: expect.objectContaining({ serverId: 2 }), forceSave: true },
    ]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('validates before calling the API', async () => {
    const user = userEvent.setup();
    render(<TautulliModal instance={null} servers={SERVERS} onClose={vi.fn()} />);
    await settle();
    await user.type(screen.getByLabelText('URL'), 'http://tautulli:8181/api/v2');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('Choose the Plex server this Tautulli monitors')).toBeInTheDocument();
    expect(screen.getByText(/Do not include "\/api\/v2"/)).toBeInTheDocument();
    expect(screen.getByText('API key is required')).toBeInTheDocument();
    expect(mocks.calls.create).toBeUndefined();
  });

  it('keeps the masked key for the same address, never sends it to a new host, and deletes', async () => {
    const user = userEvent.setup();
    const instance: TautulliInstance = {
      id: 5,
      name: 'Tautulli',
      serverId: 1,
      url: 'http://tautulli:8181',
      apiKey: MASKED_SECRET,
      verifyTls: true,
      enabled: true,
      createdAt: '',
      updatedAt: '',
    };
    render(<TautulliModal instance={instance} servers={SERVERS} onClose={vi.fn()} />);
    await settle();
    expect(screen.getByLabelText('API Key')).toHaveValue(MASKED_SECRET);
    mocks.results.update = { ...instance };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.update).toEqual([{ instance: expect.objectContaining({ id: 5, apiKey: MASKED_SECRET }), forceSave: false }]);

    const url = screen.getByLabelText('URL');
    await user.clear(url);
    await user.type(url, 'http://attacker.example:8181');
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText(/Enter the API key again/)).toBeInTheDocument();
    expect(mocks.calls.test).toBeUndefined();

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    const confirm = screen.getByRole('dialog', { name: 'Delete Tautulli' });
    expect(within(confirm).getByText(/every play history becomes unknown/)).toBeInTheDocument();
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }));
    expect(mocks.calls.delete).toEqual([5]);
  });

  it('shows a failed test without saving', async () => {
    const user = userEvent.setup();
    render(<TautulliModal instance={null} servers={SERVERS.slice(0, 1)} onClose={vi.fn()} />);
    await settle();
    // With one media server it is preselected.
    expect(screen.getByLabelText('Plex Server')).toHaveValue('1');
    await user.type(screen.getByLabelText('URL'), 'http://tautulli:8181');
    await user.type(screen.getByLabelText('API Key'), KEY);
    mocks.results.test = new ApiError('This Tautulli monitors another Plex server (Other), not Plex', { status: 400 });
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText(/monitors another Plex server/)).toBeInTheDocument();
    expect(mocks.calls.create).toBeUndefined();
  });
});
