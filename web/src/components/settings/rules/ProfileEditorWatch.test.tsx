import { cleanup, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Profile } from '@/api/types';
import ProfilesPage from '@/pages/settings/ProfilesPage';
import type { ProfileDraft } from './criteria';
import { ProfileEditorModal } from './ProfileEditorModal';
import { installFetchMock, renderWithProviders, settleModal, TEST_SCHEMA } from './test-utils';

// docs/DECISIONS.md D10 (issue #5): the editor explains that the play-history criteria never decide
// without a Tautulli connection.

const PLAYED_DRAFT: ProfileDraft = {
  id: 1,
  name: 'Played first',
  isDefault: true,
  keepCount: 1,
  keepPer: '',
  protections: [],
  criteria: [
    { type: 'health', enabled: true },
    { type: 'played', enabled: true },
    { type: 'resolution', enabled: true, order: ['2160', '1080'] },
  ],
};

function renderEditor(draft: ProfileDraft, watchHistoryConnected: boolean | undefined) {
  installFetchMock([]);
  renderWithProviders(
    <ProfileEditorModal open initial={draft} schema={TEST_SCHEMA} watchHistoryConnected={watchHistoryConnected} onClose={() => {}} />,
  );
  return screen.getByRole('dialog');
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<ProfileEditorModal> play history', () => {
  it('warns when a play-history criterion is enabled and no Tautulli is connected', async () => {
    const dialog = renderEditor(PLAYED_DRAFT, false);
    await settleModal();
    expect(within(dialog).getByText('No Tautulli connection')).toBeInTheDocument();
    expect(within(dialog).getByText(/every copy.s play history is unknown and these criteria never decide/)).toBeInTheDocument();
  });

  it('stays quiet with a connection, while it is unknown, or when the criterion is off', async () => {
    let dialog = renderEditor(PLAYED_DRAFT, true);
    await settleModal();
    expect(within(dialog).queryByText('No Tautulli connection')).not.toBeInTheDocument();
    cleanup();

    dialog = renderEditor(PLAYED_DRAFT, undefined);
    expect(within(dialog).queryByText('No Tautulli connection')).not.toBeInTheDocument();
    cleanup();

    dialog = renderEditor(
      { ...PLAYED_DRAFT, criteria: PLAYED_DRAFT.criteria.map((c) => (c.type === 'played' ? { ...c, enabled: false } : c)) },
      false,
    );
    expect(within(dialog).queryByText('No Tautulli connection')).not.toBeInTheDocument();
  });

  it('knows from the Tautulli connections whether one is enabled (ProfilesPage)', async () => {
    const profile: Profile = { ...(PLAYED_DRAFT as Profile), createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' };
    installFetchMock([
      { method: 'GET', path: '/api/v1/profile', respond: () => [profile] },
      { method: 'GET', path: '/api/v1/profile/schema', respond: () => TEST_SCHEMA },
      { method: 'GET', path: '/api/v1/library', respond: () => [] },
      { method: 'GET', path: '/api/v1/arr', respond: () => [] },
      {
        method: 'GET',
        path: '/api/v1/tautulli',
        respond: () => [{ id: 1, name: 'Tautulli', serverId: 1, url: 'http://t:8181', apiKey: '********', verifyTls: true, enabled: false }],
      },
      { method: 'GET', path: /^\/api\/v1\/duplicate/, respond: () => ({}) },
    ]);
    const user = userEvent.setup();
    renderWithProviders(<ProfilesPage />);
    await user.click(await screen.findByRole('button', { name: /Played first/ }));
    const dialog = await screen.findByRole('dialog', { name: /Played first/ });
    expect(await within(dialog).findByText('No Tautulli connection')).toBeInTheDocument();
  });
});
