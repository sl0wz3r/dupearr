import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ArrInstance, Library, Profile, ProfileInput } from '@/api/types';
import ProfilesPage from '@/pages/settings/ProfilesPage';
import { deleteErrorMessage } from './ProfileEditorModal';
import { ApiError } from '@/api/client';
import { installFetchMock, jsonResponse, renderWithProviders, settleModal, TEST_SCHEMA } from './test-utils';

const DEFAULT_PROFILE: Profile = {
  ...TEST_SCHEMA.templates[0]!,
  id: 1,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
  criteria: [
    { type: 'health', enabled: true },
    { type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', '576', '480', 'sd'] },
    { type: 'dynamic_range', enabled: true, order: ['dv_hdr10', 'hdr10plus', 'hdr10', 'hlg', 'dv', 'sdr'] },
    { type: 'file_size', enabled: true, direction: 'higher', tolerancePercent: 5 },
    { type: 'date_added', enabled: true, direction: 'higher' },
  ],
};
const SPACE_PROFILE: Profile = {
  ...TEST_SCHEMA.templates[1]!,
  id: 2,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};
const LIBS = [
  { id: 10, serverId: 1, title: 'Movies', profileId: null },
  { id: 11, serverId: 1, title: 'Movies 4K', profileId: 2 },
] as Library[];
const ARR = [{ id: 3, name: 'Radarr 4K', kind: 'radarr' }] as ArrInstance[];

function setup(overrides: { post?: (body: unknown) => unknown; del?: () => unknown } = {}) {
  let profiles: Profile[] = [SPACE_PROFILE, DEFAULT_PROFILE];
  const api = installFetchMock([
    { method: 'GET', path: '/api/v1/profile', respond: () => profiles },
    { method: 'GET', path: '/api/v1/profile/schema', respond: () => TEST_SCHEMA },
    { method: 'GET', path: '/api/v1/library', respond: () => LIBS },
    { method: 'GET', path: '/api/v1/arr', respond: () => ARR },
    { method: 'GET', path: /^\/api\/v1\/duplicate/, respond: () => ({}) },
    {
      method: 'POST',
      path: '/api/v1/profile',
      respond: (body) => {
        if (overrides.post) return overrides.post(body);
        const created = { ...(body as Profile), id: 3 };
        profiles = [...profiles, created];
        return created;
      },
    },
    {
      method: 'PUT',
      path: /^\/api\/v1\/profile\/\d+$/,
      respond: (body) => {
        const p = body as Profile;
        profiles = profiles.map((x) => (x.id === p.id ? p : x));
        return p;
      },
    },
    {
      method: 'DELETE',
      path: /^\/api\/v1\/profile\/\d+$/,
      respond: () => (overrides.del ? overrides.del() : {}),
    },
  ]);
  const user = userEvent.setup();
  renderWithProviders(<ProfilesPage />);
  return { api, user };
}

async function openCard(name: RegExp) {
  const card = await screen.findByRole('button', { name });
  return card;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<ProfilesPage>', () => {
  it('shows profile cards, default first, with a summary of the first criteria', async () => {
    setup();
    const card = await openCard(/Keep Highest Quality/);
    expect(within(card).getByText('Default')).toBeInTheDocument();
    expect(within(card).getByText('1. File Health')).toBeInTheDocument();
    expect(within(card).getByText('3. Dynamic Range')).toBeInTheDocument();
    expect(within(card).getByText('+2 more')).toBeInTheDocument();
    expect(within(card).getByText('Keep best 1')).toBeInTheDocument();
    expect(within(card).getByText('1 protection')).toBeInTheDocument();
    // Library without a profile uses the default.
    expect(within(card).getByText('Used by Movies')).toBeInTheDocument();

    const space = screen.getByRole('button', { name: /Save Space/ });
    expect(within(space).getByText('Used by Movies 4K')).toBeInTheDocument();
    // Default profile card comes first.
    const cards = screen.getAllByRole('button', { name: /Keep Highest Quality|Save Space/ });
    expect(cards[0]).toBe(card);
  });

  it('creates a profile from a template', async () => {
    const { user, api } = setup();
    await openCard(/Keep Highest Quality/);
    await user.click(screen.getAllByRole('button', { name: 'Add Profile' })[0]!);

    const picker = await screen.findByRole('dialog', { name: 'Add Profile' });
    expect(within(picker).getByText(/smallest file/)).toBeInTheDocument(); // template description
    expect(within(picker).getByText('Recommended')).toBeInTheDocument();
    await user.click(within(picker).getByRole('button', { name: /Save Space/ }));

    const editor = await screen.findByRole('dialog', { name: 'Add Profile' });
    await settleModal();
    // Name made unique against existing profiles.
    expect(within(editor).getByLabelText('Name')).toHaveValue('Save Space (2)');
    expect(within(editor).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    const explanation = within(editor).getByRole('region', { name: 'How this profile decides' });
    expect(explanation).toHaveTextContent('Keep the healthiest file; if still tied, prefer the smaller file.');

    await user.clear(within(editor).getByLabelText('Name'));
    await user.type(within(editor).getByLabelText('Name'), 'Tiny');
    await user.selectOptions(within(editor).getByLabelText('Keep per'), 'resolution');
    expect(explanation).toHaveTextContent('the best 4K AND the best 1080p');
    await user.click(within(editor).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(api.find('POST', '/api/v1/profile')).toHaveLength(1));
    const body = api.find('POST', '/api/v1/profile')[0]!.body as ProfileInput;
    expect(body).toEqual({
      name: 'Tiny',
      isDefault: false,
      keepCount: 1,
      keepPer: 'resolution',
      criteria: SPACE_PROFILE.criteria,
      protections: [],
    });
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('starts from scratch with only the health criterion', async () => {
    const { user } = setup();
    await openCard(/Keep Highest Quality/);
    await user.click(screen.getAllByRole('button', { name: 'Add Profile' })[0]!);
    await user.click(await screen.findByRole('button', { name: /Start from scratch/ }));
    const editor = await screen.findByRole('dialog', { name: 'Add Profile' });
    expect(within(editor).getByLabelText('Name')).toHaveValue('New Profile');
    const list = within(editor).getByRole('list', { name: 'Criteria' });
    expect(Array.from(list.children).map((li) => li.getAttribute('data-type'))).toEqual(['health']);
  });

  it('validates client-side before saving', async () => {
    const { user, api } = setup();
    await openCard(/Keep Highest Quality/);
    await user.click(screen.getAllByRole('button', { name: 'Add Profile' })[0]!);
    await user.click(await screen.findByRole('button', { name: /Start from scratch/ }));
    const editor = await screen.findByRole('dialog', { name: 'Add Profile' });
    await settleModal();
    await user.clear(within(editor).getByLabelText('Name'));
    await user.selectOptions(within(editor).getByRole('combobox', { name: 'Add criterion' }), 'audio_language');
    await user.click(within(editor).getByRole('button', { name: 'Add Criterion' }));
    await user.click(within(editor).getByRole('button', { name: 'Add Protection' }));
    await user.clear(within(editor).getByRole('textbox', { name: 'Protection 1 value' }));

    await user.click(within(editor).getByRole('button', { name: 'Save' }));
    expect(within(editor).getByText('Please fix 3 problems before saving')).toBeInTheDocument();
    expect(within(editor).getByText('Name is required')).toBeInTheDocument();
    expect(within(within(editor).getByRole('listitem', { name: 'Audio Language' })).getByText('Choose an audio language')).toBeInTheDocument();
    expect(within(editor).getByText('A value is required')).toBeInTheDocument();
    expect(api.find('POST', '/api/v1/profile')).toHaveLength(0);
  });

  it('maps server validation errors onto the fields and rows', async () => {
    const { user } = setup({
      post: () =>
        jsonResponse(
          [
            { propertyName: 'Name', errorMessage: 'A profile with this name already exists' },
            { propertyName: 'Criteria[1].Direction', errorMessage: 'Direction must be higher or lower' },
            { propertyName: 'Protections[0].Value', errorMessage: 'Tag not found in any *arr' },
            { propertyName: 'Mystery', errorMessage: 'Something else is wrong' },
          ],
          400,
        ),
    });
    await openCard(/Keep Highest Quality/);
    await user.click(screen.getAllByRole('button', { name: 'Add Profile' })[0]!);
    const picker = await screen.findByRole('dialog', { name: 'Add Profile' });
    await user.click(within(picker).getByRole('button', { name: /Keep Highest Quality/ }));
    const editor = await screen.findByRole('dialog', { name: 'Add Profile' });
    await settleModal();
    await user.click(within(editor).getByRole('button', { name: 'Save' }));

    expect(await within(editor).findByText('A profile with this name already exists')).toBeInTheDocument();
    // criteria[1] of the submitted body is resolution (template order).
    const resolution = within(editor).getByRole('listitem', { name: 'Resolution' });
    expect(within(resolution).getByText('Direction must be higher or lower')).toBeInTheDocument();
    expect(within(within(editor).getByRole('list', { name: 'Protections' })).getByText('Tag not found in any *arr')).toBeInTheDocument();
    // Unknown property → summary.
    const summary = within(editor).getByText('Please fix 4 problems before saving').closest('[role="alert"]') as HTMLElement;
    expect(within(summary).getByText('Something else is wrong')).toBeInTheDocument();
    // Editor stays open.
    expect(screen.getByRole('dialog', { name: 'Add Profile' })).toBeInTheDocument();
  });

  it('edits an existing profile with PUT', async () => {
    const { user, api } = setup();
    await user.click(await openCard(/Save Space/));
    const editor = await screen.findByRole('dialog', { name: 'Edit Profile — Save Space' });
    await settleModal();
    await user.click(within(editor).getByRole('button', { name: 'Move File Size up' }));
    await user.click(within(editor).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/profile/2')).toHaveLength(1));
    const body = api.find('PUT', '/api/v1/profile/2')[0]!.body as ProfileInput;
    expect(body.id).toBe(2);
    expect(body.criteria.map((c) => c.type)).toEqual(['file_size', 'health']);
  });

  it('asks before discarding unsaved changes', async () => {
    const { user } = setup();
    await user.click(await openCard(/Save Space/));
    const editor = await screen.findByRole('dialog', { name: 'Edit Profile — Save Space' });
    await settleModal();
    // Unchanged → Cancel closes immediately.
    await user.click(within(editor).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    await user.click(await openCard(/Save Space/));
    const again = await screen.findByRole('dialog', { name: 'Edit Profile — Save Space' });
    await settleModal();
    await user.type(within(again).getByLabelText('Name'), '!');
    await user.click(within(again).getByRole('button', { name: 'Cancel' }));
    const confirm = await screen.findByRole('dialog', { name: 'Discard Changes' });
    await user.click(within(confirm).getByRole('button', { name: 'Keep Editing' }));
    expect(screen.getByRole('dialog', { name: 'Edit Profile — Save Space' })).toBeInTheDocument();
    await user.click(within(again).getByRole('button', { name: 'Cancel' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Discard Changes' })).getByRole('button', { name: 'Discard Changes' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('cannot delete the default profile', async () => {
    const { user } = setup();
    await user.click(await openCard(/Keep Highest Quality/));
    const editor = await screen.findByRole('dialog', { name: 'Edit Profile — Keep Highest Quality' });
    expect(within(editor).getByRole('button', { name: 'Delete' })).toBeDisabled();
    expect(within(editor).getByRole('switch', { name: 'Default' })).toBeDisabled();
  });

  it('deletes a profile after confirmation and shows a 409 message', async () => {
    const { user, api } = setup({
      del: () => jsonResponse({ message: 'Profile is assigned to 1 library', description: 'Reassign Movies 4K first.' }, 409),
    });
    await user.click(await openCard(/Save Space/));
    const editor = await screen.findByRole('dialog', { name: 'Edit Profile — Save Space' });
    await user.click(within(editor).getByRole('button', { name: 'Delete' }));
    const confirm = await screen.findByRole('dialog', { name: 'Delete Profile' });
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(api.find('DELETE', '/api/v1/profile/2')).toHaveLength(1));
    expect(await within(editor).findByText('Profile is assigned to 1 library Reassign Movies 4K first.')).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Delete Profile' })).not.toBeInTheDocument();
  });

  it('clones a profile into a new editor', async () => {
    const { user } = setup();
    await user.click(await openCard(/Save Space/));
    const editor = await screen.findByRole('dialog', { name: 'Edit Profile — Save Space' });
    await user.click(within(editor).getByRole('button', { name: 'Clone' }));
    const clone = await screen.findByRole('dialog', { name: 'Add Profile' });
    expect(within(clone).getByLabelText('Name')).toHaveValue('Save Space (2)');
  });
});

describe('deleteErrorMessage', () => {
  it('explains a bare 409 and passes other errors through', () => {
    expect(deleteErrorMessage(new ApiError('Conflict', { status: 409 }))).toMatch(/default profile/);
    expect(deleteErrorMessage(new ApiError('In use', { status: 409, description: 'Reassign first.' }))).toBe('In use Reassign first.');
    expect(deleteErrorMessage(new ApiError('Boom', { status: 500 }))).toBe('Boom');
  });
});
