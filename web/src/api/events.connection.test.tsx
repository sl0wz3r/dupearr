import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { configureApi, onUnauthorized } from './client';
import { useServerEventsConnection } from './events';

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: MessageEvent<string>) => void) | null = null;
  onerror: (() => void) | null = null;
  url: string;
  init?: EventSourceInit;
  constructor(url: string, init?: EventSourceInit) {
    this.url = url;
    this.init = init;
    FakeEventSource.instances.push(this);
  }
  close() {}
}

afterEach(() => {
  FakeEventSource.instances = [];
  vi.unstubAllGlobals();
});

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

// r2-outbound-web#2: the live-update stream is authenticated by the session cookie; the URL never
// carries a credential (reverse-proxy access logs record it on every page load).
describe('useServerEventsConnection', () => {
  it('opens the stream without a credential in the URL and sends an expired session to the login page', async () => {
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
    vi.stubGlobal('EventSource', FakeEventSource);
    const fetchMock = vi.fn(async () => new Response('{"message":"Unauthorized"}', { status: 401, headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetchMock);
    const unauthorized = vi.fn();
    const off = onUnauthorized(unauthorized);

    renderHook(() => useServerEventsConnection(true), { wrapper });
    const es = FakeEventSource.instances[0]!;
    expect(es.url).toBe('/api/v1/events');
    expect(es.url).not.toMatch(/apikey/i);
    expect(es.init?.withCredentials).toBe(true);

    // EventSource hides the status of a failed connection: an authenticated probe finds the 401.
    es.onerror?.();
    await waitFor(() => expect(unauthorized).toHaveBeenCalled());
    off();
  });
});
