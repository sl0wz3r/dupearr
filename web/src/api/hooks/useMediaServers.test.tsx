import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { configureApi } from '../client';
import { usePlexServers } from './useMediaServers';

interface Captured {
  url: string;
  headers: Record<string, string>;
}

function stubFetch() {
  const calls: Captured[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
      const headers: Record<string, string> = {};
      new Headers(init?.headers).forEach((v, k) => {
        headers[k] = v;
      });
      calls.push({ url, headers });
      return new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } });
    }),
  );
  return calls;
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('usePlexServers', () => {
  // SEC-006: the plex.tv account token controls the whole Plex account. It must never be put in a
  // URL, where reverse-proxy access logs, CDN logs and browser history would keep it.
  it('sends the plex.tv account token in the X-Plex-Token header, never in the URL', async () => {
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
    const calls = stubFetch();
    const token = 'plex-account-token-SECRET';

    const { result } = renderHook(() => usePlexServers(token), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(calls).toHaveLength(1);
    const call = calls[0]!;
    const url = new URL(call.url, 'http://localhost');
    expect(url.pathname).toBe('/api/v1/plex/servers');
    expect(url.search).toBe('');
    expect(call.url).not.toContain(token);
    expect(call.headers['x-plex-token']).toBe(token);
  });

  it('does not call the API without a token', () => {
    const calls = stubFetch();
    renderHook(() => usePlexServers(''), { wrapper });
    expect(calls).toHaveLength(0);
  });
});
