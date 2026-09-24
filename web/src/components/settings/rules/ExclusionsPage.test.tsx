import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Exclusion, ExclusionInput, Library } from '@/api/types';
import ExclusionsPage from '@/pages/settings/ExclusionsPage';
import { validateExclusion } from './ExclusionModal';
import { installFetchMock, jsonResponse, renderWithProviders, settleModal } from './test-utils';

const LIBS = [{ id: 10, serverId: 1, title: 'Kids Movies' }] as Library[];

function setup(initial: Exclusion[] = [], post?: (body: unknown) => unknown) {
  let exclusions = [...initial];
  const api = installFetchMock([
    { method: 'GET', path: '/api/v1/exclusion', respond: () => exclusions },
    { method: 'GET', path: '/api/v1/library', respond: () => LIBS },
    {
      method: 'POST',
      path: '/api/v1/exclusion',
      respond: (body) => {
        if (post) return post(body);
        const created = { ...(body as ExclusionInput), id: 100 + exclusions.length, createdAt: new Date().toISOString() };
        exclusions = [...exclusions, created as Exclusion];
        return created;
      },
    },
    {
      method: 'DELETE',
      path: /^\/api\/v1\/exclusion\/\d+$/,
      respond: (_body, url) => {
        const id = Number(url.pathname.split('/').pop());
        exclusions = exclusions.filter((e) => e.id !== id);
        return {};
      },
    },
  ]);
  const user = userEvent.setup();
  renderWithProviders(<ExclusionsPage />);
  return { api, user };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('validateExclusion', () => {
  it.each([
    ['group_key', 'movie:tmdb:603', null],
    ['group_key', 'episode:tvdb:81189:s01e01', null],
    ['group_key', 'movie:tmdb:603#extended', null],
    ['group_key', '603', /Group keys look like/],
    // The engine matches keys exactly (case-sensitive): an upper-case prefix would never match.
    ['group_key', 'Movie:tmdb:603', /Group keys look like/],
    ['group_key', 'movie:tmdb: 603', /Group keys look like/],
    ['group_key', '  plex:1:12345  ', null],
    ['title_regex', '  ^star wars  ', null],
    ['path_prefix', '/data/media/kids/', null],
    ['path_prefix', 'media/kids', /absolute path/],
    ['library', '', 'Choose a library'],
    ['title_regex', '(?i)^star wars', null],
    ['title_regex', '(?<=a)b', /Look-around/],
    ['title_regex', '', 'A regular expression is required'],
  ] as const)('%s %j', (kind, value, expected) => {
    const got = validateExclusion(kind, value);
    if (expected === null) expect(got).toBeNull();
    else if (typeof expected === 'string') expect(got).toBe(expected);
    else expect(got).toMatch(expected);
  });
});

describe('<ExclusionsPage>', () => {
  it('explains exclusions vs ignore when empty', async () => {
    setup();
    expect(await screen.findByText('No exclusions')).toBeInTheDocument();
    expect(screen.getByText(/remove content from duplicate detection entirely/)).toBeInTheDocument();
    expect(screen.getByText('Ignore')).toBeInTheDocument();
  });

  it('lists exclusions with kind labels and resolved library names', async () => {
    setup([
      { id: 1, kind: 'library', value: '10', createdAt: '2026-09-01T00:00:00Z' },
      { id: 2, kind: 'title_regex', value: '^Star Wars', title: 'Star Wars', reason: 'Keep all cuts', createdAt: '2026-09-02T00:00:00Z' },
    ]);
    const table = await screen.findByRole('table', { name: 'Exclusions' });
    expect(await within(table).findByText('Kids Movies')).toBeInTheDocument();
    expect(within(table).getByText('Library')).toBeInTheDocument();
    expect(within(table).getByText('Title (regex)')).toBeInTheDocument();
    expect(within(table).getByText('^Star Wars')).toBeInTheDocument();
    expect(within(table).getByText('Keep all cuts')).toBeInTheDocument();
  });

  it('adds a title regex exclusion after validating it', async () => {
    const { user, api } = setup();
    await screen.findByText('No exclusions');
    await user.click(screen.getAllByRole('button', { name: 'Add Exclusion' })[0]!);
    const dialog = await screen.findByRole('dialog', { name: 'Add Exclusion' });
    await settleModal();

    await user.selectOptions(within(dialog).getByLabelText('Kind'), 'title_regex');
    await user.type(within(dialog).getByLabelText('Value'), '(?!x)Star');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(within(dialog).getByText(/Look-around/)).toBeInTheDocument();
    expect(api.find('POST', '/api/v1/exclusion')).toHaveLength(0);

    await user.clear(within(dialog).getByLabelText('Value'));
    // Surrounding whitespace is trimmed like the engine does before compiling.
    await user.type(within(dialog).getByLabelText('Value'), ' (?i)^star wars ');
    await user.type(within(dialog).getByLabelText('Reason'), 'Collection');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(api.find('POST', '/api/v1/exclusion')).toHaveLength(1));
    expect(api.find('POST', '/api/v1/exclusion')[0]!.body).toEqual({
      kind: 'title_regex',
      value: '(?i)^star wars',
      reason: 'Collection',
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByText('(?i)^star wars')).toBeInTheDocument();
  });

  it('adds a library exclusion titled after the library', async () => {
    const { user, api } = setup();
    await screen.findByText('No exclusions');
    await user.click(screen.getAllByRole('button', { name: 'Add Exclusion' })[0]!);
    const dialog = await screen.findByRole('dialog', { name: 'Add Exclusion' });
    await settleModal();
    await user.selectOptions(within(dialog).getByLabelText('Kind'), 'library');
    await user.selectOptions(within(dialog).getByLabelText('Value'), '10');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.find('POST', '/api/v1/exclusion')).toHaveLength(1));
    expect(api.find('POST', '/api/v1/exclusion')[0]!.body).toEqual({ kind: 'library', value: '10', title: 'Kids Movies' });
  });

  it('shows server validation errors on the value field', async () => {
    const { user } = setup([], () => jsonResponse([{ propertyName: 'Value', errorMessage: 'Already excluded' }], 400));
    await screen.findByText('No exclusions');
    await user.click(screen.getAllByRole('button', { name: 'Add Exclusion' })[0]!);
    const dialog = await screen.findByRole('dialog', { name: 'Add Exclusion' });
    await settleModal();
    await user.type(within(dialog).getByLabelText('Value'), '/data/media/kids/');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('Already excluded')).toBeInTheDocument();
  });

  it('deletes an exclusion after confirmation', async () => {
    const { user, api } = setup([{ id: 7, kind: 'path_prefix', value: '/data/media/kids/', createdAt: '2026-09-01T00:00:00Z' }]);
    await user.click(await screen.findByRole('button', { name: 'Delete exclusion /data/media/kids/' }));
    const confirm = await screen.findByRole('dialog', { name: 'Delete Exclusion' });
    await user.click(within(confirm).getByRole('button', { name: 'Cancel' }));
    expect(api.find('DELETE', '/api/v1/exclusion/7')).toHaveLength(0);

    await user.click(screen.getByRole('button', { name: 'Delete exclusion /data/media/kids/' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Delete Exclusion' })).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(api.find('DELETE', '/api/v1/exclusion/7')).toHaveLength(1));
    expect(await screen.findByText('No exclusions')).toBeInTheDocument();
  });
});
