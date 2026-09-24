/**
 * Test-only helpers for the duplicates pages: fixtures, a fetch router and a provider wrapper.
 * Imported by *.test.tsx files only (never by app code, so it is not bundled).
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { vi } from 'vitest';
import type {
  DiscInfo,
  DuplicateGroupDetail,
  DuplicateGroupSummary,
  DuplicateStats,
  GroupFile,
  Library,
  MediaPart,
  MediaServer,
  MediaVersion,
  PagedResponse,
  Settings,
} from '@/api/types';
import { ToastProvider } from '@/components/ui';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

export const GiB = 1024 ** 3;

export function makeSummary(over: Partial<DuplicateGroupSummary> = {}): DuplicateGroupSummary {
  return {
    id: 1,
    key: 'movie:tmdb:949',
    mediaType: 'movie',
    title: 'Heat',
    year: 1995,
    serverId: 1,
    libraryIds: [1],
    thumb: '/library/metadata/10/thumb/1',
    status: 'pending',
    flags: [],
    profileId: 1,
    fileCount: 2,
    keepCount: 1,
    removeCount: 1,
    reclaimableBytes: 10 * GiB,
    bestResolution: '2160',
    firstSeenAt: '2026-09-01T10:00:00Z',
    lastSeenAt: '2026-09-22T10:00:00Z',
    files: [
      { id: 11, decision: 'keep', resolution: '2160', dynamicRange: 'dv_hdr10', videoCodec: 'hevc', size: 60 * GiB, libraryTitle: 'Movies' },
      { id: 12, decision: 'remove', resolution: '1080', dynamicRange: 'sdr', videoCodec: 'h264', size: 10 * GiB, libraryTitle: 'Movies' },
    ],
    signature: `sig-${over.id ?? 1}`,
    ...over,
  };
}

export function makeVersion(over: Partial<MediaVersion> = {}): MediaVersion {
  return {
    key: 'plex:1:100',
    serverId: 1,
    libraryId: 1,
    libraryTitle: 'Movies',
    sectionKey: '1',
    ratingKey: '10',
    mediaId: 100,
    itemTitle: 'Heat',
    displayTitle: '4K DoVi/HDR10 (HEVC Main 10)',
    optimizedVersion: false,
    parts: [{ id: 1000, path: '/data/movies/Heat (1995)/Heat.2160p.mkv', localPath: '', size: 60 * GiB, duration: 10_200_000 }],
    container: 'mkv',
    durationMs: 10_200_000,
    bitrateKbps: 60_000,
    videoBitrateKbps: 55_000,
    width: 3840,
    height: 2160,
    resolution: '2160',
    videoCodec: 'hevc',
    videoProfile: 'main 10',
    bitDepth: 10,
    frameRate: '24p',
    dynamicRange: 'dv_hdr10',
    audioTracks: [
      { format: 'truehd_atmos', codec: 'truehd', profile: '', channels: 8, language: 'English', languageCode: 'eng', title: '', default: true, atmos: true },
      { format: 'ac3', codec: 'ac3', profile: '', channels: 6, language: 'French', languageCode: 'fre', title: '', default: false, atmos: false },
    ],
    subtitleTracks: [{ codec: 'srt', language: 'English', languageCode: 'eng', forced: false, external: false }],
    source: 'remux',
    edition: '',
    addedAt: '2026-01-01T00:00:00Z',
    arr: null,
    ...over,
  };
}

/** A single UHD Blu-ray (BDMV) found on disk next to the movie (override any field). */
export function makeDiscInfo(over: Partial<DiscInfo> = {}): DiscInfo {
  return {
    type: 'uhd_bluray',
    root: '/data/movies/Heat (1995)',
    localRoot: '/mnt/movies/Heat (1995)',
    discs: 1,
    fileCount: 312,
    mainFeature: 'BDMV/PLAYLIST/00800.mpls',
    readable: true,
    origin: 'filesystem',
    removable: true,
    ...over,
  };
}

/** Disc-version fields: key, synthetic part (the disc root, total size), source "disc". */
export function discVersion(disc: Partial<DiscInfo> = {}, over: Partial<MediaVersion> = {}): Partial<MediaVersion> {
  const info = makeDiscInfo(disc);
  return {
    key: 'disc:1:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c',
    mediaId: 0,
    displayTitle: '',
    parts: [{ id: 0, path: info.root, localPath: info.localRoot, size: 61 * GiB, duration: 10_200_000 }],
    container: '',
    source: 'disc',
    disc: info,
    ...over,
  };
}

/** Folder of the loose-clip fixtures (the real-world incident: a flattened Blu-ray backup). */
export const CLIP_FOLDER = '/data/Movies/Elemental (2023)';
export const CLIP_LOCAL_FOLDER = '/mnt/Movies/Elemental (2023)';

/**
 * A loose Blu-ray clip part `NNNNN.m2ts` directly in {@link CLIP_FOLDER} (no BDMV/): Plex lists
 * each one as its own version of the movie.
 */
export function clipPart(clip: number, over: Partial<MediaPart> = {}): MediaPart {
  const name = `${String(clip).padStart(5, '0')}.m2ts`;
  return { id: 7000 + clip, path: `${CLIP_FOLDER}/${name}`, localPath: '', size: GiB, duration: 60_000, ...over };
}

/**
 * A pre-change per-clip version: one loose clip listed by Plex as its own version (how groups with
 * 100+ "copies" were stored before loose clip sets were recognised).
 */
export function clipVersion(clip: number, part: Partial<MediaPart> = {}, over: Partial<MediaVersion> = {}): Partial<MediaVersion> {
  return {
    key: `plex:1:${500 + clip}`,
    mediaId: 500 + clip,
    displayTitle: '1080p (H.264)',
    container: 'm2ts',
    source: 'unknown',
    parts: [clipPart(clip, part)],
    ...over,
  };
}

/**
 * A loose Blu-ray clip set (type `bluray_clips`, origin plex): Plex's per-clip versions merged into
 * one disc version. The main feature is clip 00174 (the longest).
 */
export function clipSetVersion(disc: Partial<DiscInfo> = {}, over: Partial<MediaVersion> = {}): Partial<MediaVersion> {
  const parts = [
    clipPart(174, { size: 30 * GiB, duration: 5_520_000 }),
    clipPart(175, { size: GiB / 2, duration: 90_000 }),
    clipPart(176, { size: GiB / 4, duration: 45_000 }),
  ];
  return {
    key: 'disc:1:5d41402abc4b2a76b9719d911017c592aaaaaaaa',
    mediaId: 0,
    displayTitle: '',
    container: 'm2ts',
    source: 'disc',
    durationMs: 5_520_000,
    parts,
    disc: makeDiscInfo({
      type: 'bluray_clips',
      root: CLIP_FOLDER,
      localRoot: CLIP_LOCAL_FOLDER,
      fileCount: 3,
      clipCount: 3,
      mainFeature: '',
      origin: 'plex',
      mediaIds: [674, 675, 676],
      ...disc,
    }),
    ...over,
  };
}

export function makeFile(over: Partial<Omit<GroupFile, 'version'>> & { version?: Partial<MediaVersion> } = {}): GroupFile {
  const { version, ...rest } = over;
  return {
    id: 11,
    groupId: 1,
    version: makeVersion(version),
    decision: 'keep',
    engineDecision: 'keep',
    rank: 1,
    reasons: [],
    decidingCriterion: '',
    protected: false,
    values: {},
    ...rest,
  };
}

export function makeGroup(over: Partial<DuplicateGroupDetail> = {}): DuplicateGroupDetail {
  return {
    id: 1,
    key: 'movie:tmdb:949',
    mediaType: 'movie',
    title: 'Heat',
    year: 1995,
    serverId: 1,
    libraryIds: [1],
    externalIds: { tmdb: '949', imdb: 'tt0113277', plex: 'plex://movie/5d776' },
    thumb: '/library/metadata/10/thumb/1',
    status: 'pending',
    flags: [],
    profileId: 1,
    files: [],
    reclaimableBytes: 10 * GiB,
    firstSeenAt: '2026-09-01T10:00:00Z',
    lastSeenAt: '2026-09-22T10:00:00Z',
    updatedAt: '2026-09-22T10:00:00Z',
    lastScanId: 3,
    signature: 'abc',
    stableCount: 2,
    actions: [],
    ...over,
  };
}

export function makeSettings(over: Partial<Settings> = {}): Settings {
  return {
    dryRun: true,
    mode: 'manual',
    scanIntervalMinutes: 360,
    minAgeHours: 168,
    maxDeletionsPerRun: 25,
    deletionMethods: ['arr', 'plex', 'filesystem'],
    arrRescanAfterDelete: true,
    unmonitorWhenKeeperElsewhere: true,
    addExclusionWhenKeeperElsewhere: false,
    recycleBinPath: '',
    recycleBinCleanupDays: 7,
    refreshPlexAfterDelete: true,
    cleanupPlexStaleEntries: true,
    treatEditionsAsDistinct: true,
    treat3DAsDistinct: true,
    languageVariantsAsDistinct: true,
    differentArrInstancesIntentional: true,
    maxGroupSize: 4,
    stableScansRequired: 2,
    maxBytesPerRunGb: 500,
    durationTolerancePercent: 10,
    durationToleranceMinutes: 5,
    historyRetentionDays: 90,
    backupIntervalDays: 7,
    backupRetentionDays: 28,
    detectDiscs: true,
    allowDiscRemoval: false,
    keepPlayableCopy: true,
    ...over,
  };
}

export function makeStats(over: Partial<DuplicateStats> = {}): DuplicateStats {
  return {
    total: 3,
    byStatus: { pending: 2, review: 1 },
    reclaimableBytes: 15 * GiB,
    reclaimedBytes: 0,
    lastScan: null,
    ...over,
  };
}

export function paged<T>(records: T[], over: Partial<PagedResponse<T>> = {}): PagedResponse<T> {
  return {
    page: 1,
    pageSize: 20,
    sortKey: 'lastSeenAt',
    sortDirection: 'descending',
    totalRecords: records.length,
    records,
    ...over,
  };
}

export const LIBRARIES: Library[] = [
  {
    id: 1,
    serverId: 1,
    sectionKey: '1',
    title: 'Movies',
    type: 'movie',
    locations: ['/data/movies'],
    enabled: true,
    profileId: null,
    scopeGroup: '',
    updatedAt: '2026-09-01T00:00:00Z',
  },
];

export const SERVERS: MediaServer[] = [
  {
    id: 1,
    name: 'Plex',
    kind: 'plex',
    url: 'http://plex:32400',
    token: '********',
    machineIdentifier: 'abc',
    verifyTls: true,
    enabled: true,
    createdAt: '2026-09-01T00:00:00Z',
    updatedAt: '2026-09-01T00:00:00Z',
  },
];

// ---------------------------------------------------------------------------
// fetch router
// ---------------------------------------------------------------------------

export interface ApiCall {
  method: string;
  /** Path relative to the API root, e.g. "/duplicate/bulk". */
  path: string;
  query: URLSearchParams;
  body: unknown;
}

export type RouteHandler = (call: ApiCall) => unknown;

export interface MockRoute {
  method?: string;
  /** Exact path relative to /api/v1, or a RegExp tested against it. */
  path: string | RegExp;
  /** Returned value is sent as JSON (200) unless it is already a Response. */
  respond: RouteHandler | unknown;
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/**
 * Stubs global fetch with a tiny router over /api/v1. Later routes win (so tests can override
 * defaults by appending). Unmatched requests return 404 so missing mocks fail loudly.
 */
export function mockApi(routes: MockRoute[]) {
  const calls: ApiCall[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const url = new URL(raw, 'http://localhost');
    const method = (init?.method ?? 'GET').toUpperCase();
    const path = url.pathname.replace(/^\/api\/v1/, '');
    let body: unknown = undefined;
    if (typeof init?.body === 'string') {
      try {
        body = JSON.parse(init.body);
      } catch {
        body = init.body;
      }
    }
    const call: ApiCall = { method, path, query: url.searchParams, body };
    calls.push(call);
    for (let i = routes.length - 1; i >= 0; i--) {
      const r = routes[i]!;
      if ((r.method ?? 'GET').toUpperCase() !== method) continue;
      const hit = typeof r.path === 'string' ? r.path === path : r.path.test(path);
      if (!hit) continue;
      const out = typeof r.respond === 'function' ? (r.respond as RouteHandler)(call) : r.respond;
      const resolved = await out;
      return resolved instanceof Response ? resolved : json(resolved);
    }
    return json({ message: `No mock for ${method} ${path}` }, 404);
  });
  vi.stubGlobal('fetch', fetchMock);
  return {
    calls,
    fetchMock,
    /** Calls matching a method + path. */
    find: (method: string, path: string | RegExp) =>
      calls.filter(
        (c) => c.method === method.toUpperCase() && (typeof path === 'string' ? c.path === path : path.test(c.path)),
      ),
  };
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

export function renderPage(ui: ReactElement, { route = '/', path = '/' }: { route?: string; path?: string } = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity, staleTime: 0 }, mutations: { retry: false } },
  });
  const result = render(
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <MemoryRouter initialEntries={[route]}>
          <Routes>
            <Route path={path} element={ui} />
            <Route path="*" element={<div>Other page</div>} />
          </Routes>
        </MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...result, queryClient };
}
