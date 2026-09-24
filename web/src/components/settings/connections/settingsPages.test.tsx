/**
 * Smoke tests for the Media Servers / Applications / Connect / UI settings pages (they live in
 * src/pages/settings). API hooks are mocked; no network.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ArrInstance, InitializeResponse, Library, MediaServer, NotificationConfig, Profile } from '@/api/types';
import { AppInfoProvider } from '@/app/AppInfo';
import { PreferencesProvider, PREFERENCES_STORAGE_KEY } from '@/app/preferences';
import { MASKED_SECRET } from '@/lib/constants';
import ApplicationsPage from '@/pages/settings/ApplicationsPage';
import ConnectPage from '@/pages/settings/ConnectPage';
import MediaServersPage from '@/pages/settings/MediaServersPage';
import UiPage from '@/pages/settings/UiPage';

const mocks = vi.hoisted(() => ({
  servers: [] as unknown[],
  libraries: [] as unknown[],
  profiles: [] as unknown[],
  arr: [] as unknown[],
  notifications: [] as unknown[],
  libraryUpdates: [] as unknown[],
  syncs: [] as unknown[],
}));

const { query, idle } = vi.hoisted(() => ({
  query: (data: unknown) => ({ data, isPending: false, isError: false, error: null, refetch: () => Promise.resolve() }),
  idle: () => ({ mutate: () => {}, mutateAsync: async () => undefined, reset: () => {}, isPending: false, data: undefined, error: null }),
}));

vi.mock('@/api/hooks/useMediaServers', () => ({
  useMediaServers: () => query(mocks.servers),
  useLibraries: () => query(mocks.libraries),
  useServerLibraries: (serverId: number) => query((mocks.libraries as Library[]).filter((l) => l.serverId === serverId)),
  useUpdateLibrary: () => ({
    ...idle(),
    mutateAsync: async (lib: unknown) => {
      mocks.libraryUpdates.push(lib);
      return lib;
    },
  }),
  useSyncServerLibraries: () => ({
    ...idle(),
    mutate: (id: unknown) => {
      mocks.syncs.push(id);
    },
  }),
  useCreateMediaServer: idle,
  useUpdateMediaServer: idle,
  useDeleteMediaServer: idle,
  useTestMediaServer: idle,
  useCreatePlexPin: idle,
  usePlexPin: () => ({ data: undefined, error: null }),
  usePlexServers: () => query(undefined),
}));

vi.mock('@/api/hooks/useProfiles', () => ({ useProfiles: () => query(mocks.profiles) }));

vi.mock('@/api/hooks/useSettings', () => ({
  useHostConfig: () => query({ apiKey: '********', webhookToken: 'webhooktoken0123456789abcdef0123' }),
  useRegenerateWebhookToken: idle,
}));

vi.mock('@/api/hooks/useArr', () => ({
  useArrInstances: () => query(mocks.arr),
  useCreateArrInstance: idle,
  useUpdateArrInstance: idle,
  useDeleteArrInstance: idle,
  useTestArrInstance: idle,
}));

vi.mock('@/api/hooks/useNotifications', () => ({
  useNotifications: () => query(mocks.notifications),
  useNotificationSchema: () => query([{ kind: 'discord', name: 'Discord', fields: [] }]),
  useNotificationTriggers: () =>
    query([
      { value: 'onDuplicatesFound', label: 'On Duplicates Found' },
      { value: 'onFileDeleted', label: 'On File Deleted' },
      { value: 'onDeleteFailed', label: 'On Delete Failed' },
    ]),
  useCreateNotification: idle,
  useUpdateNotification: idle,
  useDeleteNotification: idle,
  useTestNotification: idle,
}));

const INIT: InitializeResponse = {
  apiRoot: '/api/v1',
  urlBase: '/dupearr',
  version: '0.1.0',
  instanceName: 'Dupearr',
  authenticationMethod: 'Forms',
};

function renderPage(page: ReactNode) {
  const router = createMemoryRouter([{ path: '*', element: page }], { initialEntries: ['/settings/x'] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <PreferencesProvider>
        <AppInfoProvider value={INIT}>
          <RouterProvider router={router} />
        </AppInfoProvider>
      </PreferencesProvider>
    </QueryClientProvider>,
  );
}

const SERVER: MediaServer = {
  id: 1,
  name: 'Home Plex',
  kind: 'plex',
  url: 'http://10.0.0.2:32400',
  token: MASKED_SECRET,
  machineIdentifier: 'machine-1',
  verifyTls: true,
  enabled: true,
  createdAt: '',
  updatedAt: '',
};

const lib = (over: Partial<Library>): Library => ({
  id: 1,
  serverId: 1,
  sectionKey: '1',
  title: 'Movies',
  type: 'movie',
  locations: ['/data/media/movies'],
  enabled: true,
  profileId: null,
  scopeGroup: '',
  updatedAt: '',
  ...over,
});

beforeEach(() => {
  mocks.servers = [SERVER];
  mocks.libraries = [
    lib({ id: 1, title: 'Movies' }),
    lib({ id: 2, title: 'Movies 4K', enabled: false, locations: ['/data/media/movies4k'] }),
  ];
  mocks.profiles = [
    { id: 1, name: 'Keep Highest Quality', isDefault: true },
    { id: 2, name: 'Save Space', isDefault: false },
  ] as Profile[];
  mocks.arr = [];
  mocks.notifications = [];
  mocks.libraryUpdates = [];
  mocks.syncs = [];
});

describe('MediaServersPage', () => {
  it('shows server cards and edits libraries in place', async () => {
    const user = userEvent.setup();
    renderPage(<MediaServersPage />);

    // Card title + libraries heading.
    expect(screen.getAllByText('Home Plex')).toHaveLength(2);
    expect(screen.getByRole('heading', { name: 'Home Plex' })).toBeInTheDocument();
    expect(screen.getByText('1/2 libraries')).toBeInTheDocument();
    expect(screen.getByText('machine-1')).toBeInTheDocument();
    expect(screen.getByText(/libraries with the same scope group are compared with each other/i)).toBeInTheDocument();

    const table = screen.getByRole('table', { name: 'Libraries of Home Plex' });
    expect(within(table).getByText('/data/media/movies4k')).toBeInTheDocument();

    await user.click(screen.getByRole('switch', { name: 'Scan Movies 4K' }));
    expect(mocks.libraryUpdates.at(-1)).toMatchObject({ id: 2, enabled: true });

    await user.selectOptions(screen.getByRole('combobox', { name: 'Profile for Movies' }), 'Save Space');
    expect(mocks.libraryUpdates.at(-1)).toMatchObject({ id: 1, profileId: 2 });

    const scope = screen.getByRole('combobox', { name: 'Scope group for Movies' });
    await user.type(scope, '  movies  ');
    await user.tab();
    expect(mocks.libraryUpdates.at(-1)).toMatchObject({ id: 1, scopeGroup: 'movies' });

    await user.click(screen.getByRole('button', { name: 'Sync' }));
    expect(mocks.syncs).toEqual([1]);
  });

  it('offers the Default profile option labelled with the default profile name', () => {
    renderPage(<MediaServersPage />);
    const select = screen.getByRole('combobox', { name: 'Profile for Movies' });
    expect(select).toHaveValue('');
    expect(within(select).getByRole('option', { name: 'Default (Keep Highest Quality)' })).toBeInTheDocument();
  });

  it('opens the add modal from the Add card', async () => {
    const user = userEvent.setup();
    mocks.servers = [];
    renderPage(<MediaServersPage />);
    await user.click(screen.getByRole('button', { name: 'Add media server' }));
    expect(screen.getByRole('dialog', { name: 'Add Media Server — Plex' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /sign in with plex/i })).toBeInTheDocument();
  });
});

describe('ApplicationsPage', () => {
  it('lists instances and shows webhook URLs without revealing the API key', () => {
    mocks.arr = [
      {
        id: 1,
        name: 'Radarr 4K',
        kind: 'radarr',
        url: 'http://radarr4k:7878',
        apiKey: MASKED_SECRET,
        verifyTls: true,
        enabled: false,
        tags: ['4k'],
        createdAt: '',
        updatedAt: '',
      },
    ] satisfies ArrInstance[];
    renderPage(<ApplicationsPage />);

    expect(screen.getByText('Radarr 4K')).toBeInTheDocument();
    expect(screen.getByText('Disabled')).toBeInTheDocument();
    expect(screen.getByText('4k')).toBeInTheDocument();

    // r2-outbound-web#1: webhook URLs carry the webhook token (hidden on screen), never the API key.
    const radarrUrl = screen.getByLabelText('Radarr webhook URL (token hidden)');
    expect(radarrUrl.textContent).toBe(`${window.location.origin}/dupearr/api/v1/webhook/radarr?apikey=••••••••`);
    expect(radarrUrl.textContent).not.toContain('webhooktoken0123456789abcdef0123');
    expect(screen.getByRole('button', { name: 'Copy Sonarr webhook URL' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Regenerate webhook token' })).toBeInTheDocument();
    expect(screen.getAllByText(/On File Import, On File Upgrade, On Rename/)).toHaveLength(2);
  });
});

describe('ConnectPage', () => {
  it('summarizes triggers on the cards', () => {
    mocks.notifications = [
      { id: 1, name: 'Discord', kind: 'discord', settings: {}, triggers: ['onDuplicatesFound', 'onFileDeleted', 'onDeleteFailed'], enabled: true },
      { id: 2, name: 'Quiet', kind: 'discord', settings: null as unknown as Record<string, unknown>, triggers: null as unknown as [], enabled: true },
    ] satisfies NotificationConfig[];
    renderPage(<ConnectPage />);
    expect(screen.getByText('On Duplicates Found, On File Deleted +1')).toBeInTheDocument();
    // Badge only — the summary line is omitted when there is nothing to summarize.
    expect(screen.getAllByText('No triggers')).toHaveLength(1);
  });
});

describe('UiPage', () => {
  it('persists preferences in this browser immediately', async () => {
    const user = userEvent.setup();
    renderPage(<UiPage />);
    const stored = () => JSON.parse(window.localStorage.getItem(PREFERENCES_STORAGE_KEY) ?? '{}') as Record<string, unknown>;

    await user.selectOptions(screen.getByLabelText('Theme'), 'Light');
    expect(document.documentElement).toHaveAttribute('data-theme', 'light');
    expect(stored().theme).toBe('light');

    await user.selectOptions(screen.getByLabelText('Time Format'), '24 hour (17:30)');
    expect(stored().timeFormat).toBe('HH:mm');

    await user.selectOptions(screen.getByLabelText('Default Page Size'), '50 per page');
    expect(stored().defaultPageSize).toBe(50);

    await user.click(screen.getByRole('switch', { name: 'Show advanced settings' }));
    expect(stored().showAdvanced).toBe(true);

    // Reset restores defaults after confirmation.
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await user.click(within(screen.getByRole('dialog', { name: 'Reset UI Settings' })).getByRole('button', { name: 'Reset' }));
    expect(stored().theme).toBe('dark');
    expect(stored().defaultPageSize).toBeUndefined();
  });
});
