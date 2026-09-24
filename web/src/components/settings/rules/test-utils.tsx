/**
 * Test helpers for the settings pages: a fetch mock routed by method + path, and a render helper
 * with QueryClient, toasts and a data router (SaveBar's navigation guard needs `useBlocker`).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, waitFor } from '@testing-library/react';
import type { ReactElement } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { vi } from 'vitest';
import type { ProfileSchema } from '@/api/types';
import { ToastProvider } from '@/components/ui';

export function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

export interface RecordedCall {
  method: string;
  path: string;
  body: unknown;
}

export interface MockRoute {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE';
  /** Exact pathname (e.g. "/api/v1/profile") or a pattern. */
  path: string | RegExp;
  /** Returns a Response, or any value to be sent as JSON 200. */
  respond: (body: unknown, url: URL) => unknown;
}

/** Replaces global fetch; unmatched requests get a 404 JSON error. Returns the recorded calls. */
export function installFetchMock(routes: MockRoute[]) {
  const calls: RecordedCall[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const url = new URL(raw, 'http://localhost');
    const method = (init?.method ?? 'GET').toUpperCase();
    let body: unknown = undefined;
    if (typeof init?.body === 'string') {
      try {
        body = JSON.parse(init.body);
      } catch {
        body = init.body;
      }
    }
    calls.push({ method, path: url.pathname, body });
    const route = routes.find(
      (r) => r.method === method && (typeof r.path === 'string' ? r.path === url.pathname : r.path.test(url.pathname)),
    );
    if (!route) return jsonResponse({ message: `No mock for ${method} ${url.pathname}` }, 404);
    const out = await route.respond(body, url);
    return out instanceof Response ? out : jsonResponse(out);
  });
  vi.stubGlobal('fetch', fn);
  return {
    calls,
    fn,
    /** Calls matching method + path. */
    find: (method: string, path: string | RegExp) =>
      calls.filter((c) => c.method === method && (typeof path === 'string' ? c.path === path : path.test(c.path))),
  };
}

/**
 * Waits until an open modal has moved focus inside itself. Modal focuses its first control in a
 * requestAnimationFrame; typing before that lands keystrokes in the wrong field.
 */
export async function settleModal(): Promise<void> {
  await waitFor(() => {
    const active = document.activeElement;
    if (!active || !active.closest('[role="dialog"]')) throw new Error('modal focus not settled yet');
  });
}

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  });
}

/** Renders `ui` inside QueryClient + ToastProvider + a memory data router. */
export function renderWithProviders(ui: ReactElement) {
  const queryClient = createTestQueryClient();
  const router = createMemoryRouter([{ path: '*', element: ui }], { initialEntries: ['/'] });
  const utils = render(
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...utils, queryClient, router };
}

/** A realistic profile schema (subset of engine.CriteriaSchema + templates). */
export const TEST_SCHEMA: ProfileSchema = {
  criteria: [
    { type: 'health', label: 'File Health', description: 'Broken, unanalyzed or sample files lose.', kind: 'boolean', supportsTolerance: false, requiresArr: false },
    {
      type: 'resolution',
      label: 'Resolution',
      description: 'Video resolution tier (width first).',
      kind: 'ordered',
      options: [
        { value: '2160', label: '2160p (4K)' },
        { value: '1440', label: '1440p' },
        { value: '1080', label: '1080p' },
        { value: '720', label: '720p' },
        { value: '576', label: '576p' },
        { value: '480', label: '480p' },
        { value: 'sd', label: 'SD' },
      ],
      defaultOrder: ['2160', '1440', '1080', '720', '576', '480', 'sd'],
      supportsTolerance: false,
      requiresArr: false,
    },
    {
      type: 'dynamic_range',
      label: 'Dynamic Range',
      description: 'HDR format.',
      kind: 'ordered',
      options: [
        { value: 'dv_hdr10', label: 'Dolby Vision (HDR10)' },
        { value: 'hdr10plus', label: 'HDR10+' },
        { value: 'hdr10', label: 'HDR10' },
        { value: 'hlg', label: 'HLG' },
        { value: 'dv', label: 'Dolby Vision' },
        { value: 'sdr', label: 'SDR' },
      ],
      defaultOrder: ['dv_hdr10', 'hdr10plus', 'hdr10', 'hlg', 'dv', 'sdr'],
      supportsTolerance: false,
      requiresArr: false,
    },
    { type: 'custom_format_score', label: 'Custom Format Score', description: 'Score from the *arr.', kind: 'numeric', defaultDirection: 'higher', supportsTolerance: false, requiresArr: true },
    { type: 'video_bitrate', label: 'Video Bitrate', description: 'Video stream bitrate.', kind: 'numeric', defaultDirection: 'higher', supportsTolerance: true, requiresArr: false },
    { type: 'file_size', label: 'File Size', description: 'Total size of all parts.', kind: 'numeric', defaultDirection: 'higher', supportsTolerance: true, requiresArr: false },
    { type: 'date_added', label: 'Date Added', description: 'When the file was added.', kind: 'numeric', defaultDirection: 'higher', supportsTolerance: false, requiresArr: false },
    { type: 'arr_managed', label: 'Managed by *arr', description: 'Tracked by Radarr/Sonarr.', kind: 'boolean', supportsTolerance: false, requiresArr: true },
    { type: 'audio_language', label: 'Audio Language', description: 'Has an audio track in a language.', kind: 'boolean', supportsTolerance: false, requiresArr: false },
    { type: 'filename_score', label: 'Filename Score', description: 'Sum of matching pattern scores.', kind: 'patterns', supportsTolerance: false, requiresArr: false },
    { type: 'library', label: 'Library', description: 'Preferred library.', kind: 'ordered', supportsTolerance: false, requiresArr: false },
  ],
  templates: [
    {
      id: 0,
      name: 'Keep Highest Quality',
      isDefault: true,
      criteria: [
        { type: 'health', enabled: true },
        { type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', '576', '480', 'sd'] },
        { type: 'file_size', enabled: true, direction: 'higher', tolerancePercent: 5 },
      ],
      keepCount: 1,
      keepPer: '',
      protections: [{ type: 'arr_tag', value: 'dupearr-keep' }],
      createdAt: '0001-01-01T00:00:00Z',
      updatedAt: '0001-01-01T00:00:00Z',
    },
    {
      id: 0,
      name: 'Save Space',
      isDefault: false,
      criteria: [
        { type: 'health', enabled: true },
        { type: 'file_size', enabled: true, direction: 'lower' },
      ],
      keepCount: 1,
      keepPer: '',
      protections: [],
      createdAt: '0001-01-01T00:00:00Z',
      updatedAt: '0001-01-01T00:00:00Z',
    },
  ],
  protectionTypes: ['path_glob', 'library', 'arr_instance'],
  keepPer: ['', 'resolution', 'dynamic_range'],
};
