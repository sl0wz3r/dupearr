import { QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ArrInstance, Library, MediaServer, PathMapping } from '@/api/types';
import { ToastProvider } from '@/components/ui';
import {
  PathMappingModal,
  PathMappingsSection,
  draftFromMapping,
  parseSourceKey,
  sourceKey,
  sourceName,
  validatePathMappingDraft,
} from './PathMappings';
import { createTestQueryClient, installFetchMock, jsonResponse, settleModal } from './test-utils';

const SERVERS = [{ id: 1, name: 'Home Plex', kind: 'plex' }] as MediaServer[];
const ARR = [{ id: 3, name: 'Radarr 4K', kind: 'radarr' }] as ArrInstance[];
const LIBS = [
  { id: 10, serverId: 1, title: 'Movies', locations: ['/data/media/movies'] },
  { id: 11, serverId: 1, title: 'TV', locations: ['/data/media/tv', '/data/media/movies'] },
  { id: 12, serverId: 2, title: 'Other server', locations: ['/elsewhere'] },
] as Library[];

async function renderModal(initial = draftFromMapping(null), onClose = vi.fn()) {
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={createTestQueryClient()}>
      <ToastProvider>
        <PathMappingModal open initial={initial} servers={SERVERS} arrInstances={ARR} libraries={LIBS} onClose={onClose} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  await settleModal();
  return { user, onClose };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('source helpers', () => {
  it('encodes and parses source keys', () => {
    expect(sourceKey('server', 1)).toBe('server:1');
    expect(parseSourceKey('arr:3')).toEqual({ sourceType: 'arr', sourceId: 3 });
    expect(parseSourceKey('')).toBeNull();
    expect(parseSourceKey('server:0')).toBeNull();
    expect(parseSourceKey('plex:1')).toBeNull();
  });

  it('names known and unknown sources', () => {
    expect(sourceName({ sourceType: 'server', sourceId: 1 }, SERVERS, ARR)).toEqual({
      name: 'Home Plex',
      kind: 'Plex',
      known: true,
    });
    expect(sourceName({ sourceType: 'arr', sourceId: 3 }, SERVERS, ARR)).toEqual({
      name: 'Radarr 4K',
      kind: 'Radarr',
      known: true,
    });
    expect(sourceName({ sourceType: 'arr', sourceId: 9 }, SERVERS, ARR).known).toBe(false);
  });
});

describe('validatePathMappingDraft', () => {
  const ok = { source: 'server:1', remotePath: '/data/media', localPath: '/data/media' };

  it.each([
    ['a valid mapping', ok, {}],
    ['a filesystem root as local path', { ...ok, localPath: '/' }, { localPath: [expect.stringMatching(/not the filesystem root/)] }],
    ['a drive root as local path', { ...ok, localPath: 'D:\\' }, { localPath: [expect.stringMatching(/not the filesystem root/)] }],
    ['a doubled slash root', { ...ok, localPath: '//' }, { localPath: [expect.stringMatching(/not the filesystem root/)] }],
    ['a root remote path (translates every path into the mapped folder)', { ...ok, remotePath: '/' }, {}],
    ['a relative local path', { ...ok, localPath: 'media' }, { localPath: ['Use an absolute path, e.g. /data/media/movies'] }],
  ])('%s', (_name, draft, expected) => {
    expect(validatePathMappingDraft(draft)).toEqual(expected);
  });
});

describe('<PathMappingModal>', () => {
  it('refuses to map the filesystem root as the local path', async () => {
    const api = installFetchMock([]);
    const { user } = await renderModal();
    await user.selectOptions(screen.getByLabelText('Source'), 'server:1');
    await user.type(screen.getByLabelText('Remote Path'), '/data');
    await user.type(screen.getByLabelText('Local Path'), '/');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText(/not the filesystem root — filesystem removals are only allowed inside mapped folders/)).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it('validates required, absolute paths before sending anything', async () => {
    const api = installFetchMock([]);
    const { user } = await renderModal();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText(/Choose the media server or \*arr instance/)).toBeInTheDocument();
    expect(screen.getByText('Remote path is required')).toBeInTheDocument();
    expect(screen.getByText('Local path is required')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Remote Path'), 'media/movies');
    expect(screen.getAllByText('Use an absolute path, e.g. /data/media/movies')).toHaveLength(1);
    expect(api.calls).toHaveLength(0);
  });

  it('offers servers and *arr instances, suggests library folders and creates the mapping', async () => {
    const api = installFetchMock([
      {
        method: 'POST',
        path: '/api/v1/pathmapping',
        respond: (body) => ({ ...(body as PathMapping), id: 5 }),
      },
    ]);
    const { user, onClose } = await renderModal();
    const source = screen.getByLabelText('Source');
    expect(within(source).getByRole('group', { name: 'Media Servers' })).toBeInTheDocument();
    expect(within(source).getByRole('group', { name: 'Applications' })).toBeInTheDocument();
    await user.selectOptions(source, 'server:1');

    const remote = screen.getByLabelText('Remote Path');
    const listId = remote.getAttribute('list');
    expect(listId).toBeTruthy();
    const suggestions = Array.from(document.getElementById(listId!)!.querySelectorAll('option')).map((o) => o.value);
    expect(suggestions).toEqual(['/data/media/movies', '/data/media/tv']);

    await user.type(remote, ' /data/media/movies ');
    await user.type(screen.getByLabelText('Local Path'), '/mnt/movies');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(api.find('POST', '/api/v1/pathmapping')[0]!.body).toEqual({
      sourceType: 'server',
      sourceId: 1,
      remotePath: '/data/media/movies',
      localPath: '/mnt/movies',
    });
  });

  it('shows X-Dupearr-Warning, stays open and updates (not re-creates) on the next save', async () => {
    const api = installFetchMock([
      {
        method: 'POST',
        path: '/api/v1/pathmapping',
        respond: (body) =>
          jsonResponse({ ...(body as PathMapping), id: 7 }, 201, {
            'X-Dupearr-Warning': 'Local path /mnt/nope does not exist',
          }),
      },
      { method: 'PUT', path: '/api/v1/pathmapping/7', respond: (body) => body },
    ]);
    const { user, onClose } = await renderModal();
    await user.selectOptions(screen.getByLabelText('Source'), 'arr:3');
    await user.type(screen.getByLabelText('Remote Path'), '/movies');
    await user.type(screen.getByLabelText('Local Path'), '/mnt/nope');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    const alert = await screen.findByText('Local path /mnt/nope does not exist');
    expect(alert.closest('[role="alert"]')).toHaveTextContent('Saved, with a warning');
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Done' })).toBeInTheDocument();

    await user.clear(screen.getByLabelText('Local Path'));
    await user.type(screen.getByLabelText('Local Path'), '/data/media/movies');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(api.find('POST', '/api/v1/pathmapping')).toHaveLength(1);
    expect(api.find('PUT', '/api/v1/pathmapping/7')[0]!.body).toEqual({
      id: 7,
      sourceType: 'arr',
      sourceId: 3,
      remotePath: '/movies',
      localPath: '/data/media/movies',
    });
  });

  it('maps server validation errors onto fields', async () => {
    installFetchMock([
      {
        method: 'PUT',
        path: '/api/v1/pathmapping/4',
        respond: () =>
          jsonResponse(
            [
              { propertyName: 'LocalPath', errorMessage: 'Local path overlaps another mapping' },
              { propertyName: 'SourceId', errorMessage: 'Unknown source' },
              { propertyName: '', errorMessage: 'Something odd' },
            ],
            400,
          ),
      },
    ]);
    const { user } = await renderModal(
      draftFromMapping({ id: 4, sourceType: 'server', sourceId: 1, remotePath: '/a', localPath: '/b' }),
    );
    expect(screen.getByRole('dialog', { name: 'Edit Path Mapping' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Local path overlaps another mapping')).toBeInTheDocument();
    expect(screen.getByText('Unknown source')).toBeInTheDocument();
    expect(screen.getByText('Something odd')).toBeInTheDocument();
  });
});

describe('<PathMappingsSection>', () => {
  it('lists mappings with source names, edits and deletes after confirmation', async () => {
    let mappings: PathMapping[] = [
      { id: 4, sourceType: 'server', sourceId: 1, remotePath: '/data/media/movies', localPath: '/data/media/movies' },
      { id: 5, sourceType: 'arr', sourceId: 99, remotePath: '/movies', localPath: '/data/media/movies' },
    ];
    const api = installFetchMock([
      { method: 'GET', path: '/api/v1/pathmapping', respond: () => mappings },
      { method: 'GET', path: '/api/v1/mediaserver', respond: () => SERVERS },
      { method: 'GET', path: '/api/v1/arr', respond: () => ARR },
      { method: 'GET', path: '/api/v1/library', respond: () => LIBS },
      { method: 'GET', path: '/api/v1/health', respond: () => [] },
      {
        method: 'DELETE',
        path: '/api/v1/pathmapping/4',
        respond: () => {
          mappings = mappings.filter((m) => m.id !== 4);
          return {};
        },
      },
    ]);
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={createTestQueryClient()}>
        <ToastProvider>
          <PathMappingsSection />
        </ToastProvider>
      </QueryClientProvider>,
    );

    const table = await screen.findByRole('table', { name: 'Path mappings' });
    expect(await within(table).findByText('Home Plex')).toBeInTheDocument();
    expect(within(table).getByText('*arr instance #99')).toBeInTheDocument();
    expect(screen.getByLabelText('Example mapping')).toHaveTextContent('Plex sees /data/media/movies');

    await user.click(within(table).getByRole('button', { name: 'Edit mapping Home Plex /data/media/movies' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit Path Mapping' });
    await settleModal();
    expect(within(dialog).getByLabelText('Remote Path')).toHaveValue('/data/media/movies');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await user.click(within(table).getByRole('button', { name: 'Delete mapping Home Plex /data/media/movies' }));
    const confirm = screen.getByRole('dialog', { name: 'Delete Path Mapping' });
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(api.find('DELETE', '/api/v1/pathmapping/4')).toHaveLength(1));
    await waitFor(() => expect(within(table).queryByText('Home Plex')).not.toBeInTheDocument());
  });

  it('shows an empty state', async () => {
    installFetchMock([
      { method: 'GET', path: '/api/v1/pathmapping', respond: () => [] },
      { method: 'GET', path: '/api/v1/mediaserver', respond: () => [] },
      { method: 'GET', path: '/api/v1/arr', respond: () => [] },
      { method: 'GET', path: '/api/v1/library', respond: () => [] },
    ]);
    render(
      <QueryClientProvider client={createTestQueryClient()}>
        <PathMappingsSection />
      </QueryClientProvider>,
    );
    expect(await screen.findByText('No path mappings')).toBeInTheDocument();
  });
});
