/**
 * Test helpers for the Activity/System pages: a fetch router mock and a provider wrapper
 * (TanStack Query + toasts + memory router). Test-only — never imported by application code.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, type RenderResult } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter } from 'react-router';
import { vi } from 'vitest';
import { configureApi } from '@/api/client';
import { ToastProvider } from '@/components/ui';

export interface RecordedRequest {
  method: string;
  url: URL;
  /** Path relative to the origin, e.g. "/api/v1/queue". */
  path: string;
  body: unknown;
}

export type FetchHandler = (req: RecordedRequest) => Response | Promise<Response> | undefined;

/** JSON response helper. */
export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(body === undefined ? '' : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function parseBody(body: BodyInit | null | undefined): unknown {
  if (typeof body !== 'string') return body ?? undefined;
  try {
    return JSON.parse(body);
  } catch {
    return body;
  }
}

/**
 * Replaces global fetch with a router. Unhandled requests get a 404 `{message}`; every request is
 * recorded in `requests`. Call `vi.unstubAllGlobals()` in afterEach.
 */
export function mockFetch(handler: FetchHandler) {
  const requests: RecordedRequest[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, 'http://localhost');
    const req: RecordedRequest = {
      method: (init?.method ?? 'GET').toUpperCase(),
      url,
      path: url.pathname,
      body: parseBody(init?.body),
    };
    requests.push(req);
    const res = await handler(req);
    return res ?? jsonResponse({ message: `No mock for ${req.method} ${url.pathname}` }, 404);
  });
  vi.stubGlobal('fetch', fn);
  return { fetch: fn, requests };
}

/** A fresh QueryClient without retries (deterministic tests). */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  });
}

/** Renders `ui` inside Query + Toast + MemoryRouter providers with the API configured. */
export function renderWithProviders(
  ui: ReactElement,
  { route = '/', queryClient = createTestQueryClient() }: { route?: string; queryClient?: QueryClient } = {},
): RenderResult & { queryClient: QueryClient } {
  configureApi({ urlBase: '', apiRoot: '/api/v1' });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...result, queryClient };
}

/** A paged response body. */
export function paged<T>(records: T[], extra: { page?: number; pageSize?: number; totalRecords?: number } = {}) {
  return {
    page: extra.page ?? 1,
    pageSize: extra.pageSize ?? 20,
    sortKey: '',
    sortDirection: 'descending' as const,
    totalRecords: extra.totalRecords ?? records.length,
    records,
  };
}
