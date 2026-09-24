/**
 * Settings → General page (lives in src/pages/settings/GeneralPage.tsx; tested here because this
 * directory holds the settings helpers it uses). Hooks are mocked; no network.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { HostConfig, Settings } from '@/api/types';
import { PreferencesProvider, PREFERENCES_STORAGE_KEY } from '@/app/preferences';
import { MASKED_SECRET } from '@/lib/constants';
import GeneralPage from '@/pages/settings/GeneralPage';

const mocks = vi.hoisted(() => ({
  host: undefined as unknown,
  settings: undefined as unknown,
  /** What a fresh GET returns at save time (defaults to host/settings). */
  freshHost: undefined as unknown,
  freshSettings: undefined as unknown,
  gets: [] as string[],
  updateHostCalls: [] as unknown[],
  updateHostResult: undefined as unknown,
  updateSettingsCalls: [] as unknown[],
  /** Thrown by the settings PUT when set. */
  updateSettingsError: undefined as unknown,
  regenerateCalls: 0,
  regeneratePasswords: [] as (string | undefined)[],
  revealPasswords: [] as (string | undefined)[],
  revokeCalls: 0,
  restartCalls: 0,
}));

// The page re-reads both configs right before saving (GET /config/host, GET /config/settings).
vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>();
  return {
    ...actual,
    api: {
      ...actual.api,
      get: async (path: string) => {
        mocks.gets.push(path);
        if (path === '/config/host') return mocks.freshHost ?? mocks.host;
        if (path === '/config/settings') return mocks.freshSettings ?? mocks.settings;
        throw new Error(`unexpected GET ${path}`);
      },
    },
  };
});

vi.mock('@/api/hooks/useSettings', () => ({
  useHostConfig: () => ({ data: mocks.host, isPending: false, isError: false, error: null, refetch: () => Promise.resolve() }),
  useSettings: () => ({ data: mocks.settings, isPending: false, isError: false, error: null, refetch: () => Promise.resolve() }),
  useUpdateHostConfig: () => ({
    isPending: false,
    mutateAsync: async (body: unknown) => {
      mocks.updateHostCalls.push(body);
      return mocks.updateHostResult ?? body;
    },
  }),
  useUpdateSettings: () => ({
    isPending: false,
    mutateAsync: async (body: unknown) => {
      mocks.updateSettingsCalls.push(body);
      if (mocks.updateSettingsError) throw mocks.updateSettingsError;
      return body;
    },
  }),
  useRegenerateApiKey: () => ({
    isPending: false,
    error: null,
    reset: () => {},
    mutate: (password: string | undefined, opts?: { onSuccess?: (cfg: unknown) => void }) => {
      mocks.regenerateCalls += 1;
      mocks.regeneratePasswords.push(password);
      opts?.onSuccess?.({ apiKey: 'new-key-ffffffffffffffffffffffff' });
    },
  }),
  useRevealApiKey: () => ({
    isPending: false,
    error: null,
    reset: () => {},
    mutate: (password: string | undefined, opts?: { onSuccess?: (res: { apiKey: string }) => void }) => {
      mocks.revealPasswords.push(password);
      opts?.onSuccess?.({ apiKey: 'revealed-0123456789abcdef01234567' });
    },
  }),
  useRevokeSessions: () => ({
    isPending: false,
    mutate: (_: unknown, opts?: { onSuccess?: () => void }) => {
      mocks.revokeCalls += 1;
      opts?.onSuccess?.();
    },
  }),
}));

vi.mock('@/api/hooks/useSystem', () => ({
  useRestart: () => ({
    isPending: false,
    mutate: () => {
      mocks.restartCalls += 1;
    },
  }),
}));

const HOST: HostConfig = {
  bindAddress: '*',
  port: 3873,
  urlBase: '',
  enableSsl: false,
  sslPort: 9873,
  sslCertPath: '',
  sslKeyPath: '',
  // Masked for browser sessions (r2-outbound-web#3): revealed only with the current password.
  apiKey: MASKED_SECRET,
  authenticationMethod: 'Forms',
  authenticationRequired: 'Enabled',
  username: 'admin',
  password: MASKED_SECRET,
  logLevel: 'info',
  logSizeLimit: 1,
  instanceName: 'Dupearr',
  launchBrowser: false,
  branch: 'main',
  envOverrides: [],
};

const SETTINGS = {
  dryRun: true,
  mode: 'manual',
  scanIntervalMinutes: 360,
  minAgeHours: 168,
  maxDeletionsPerRun: 25,
  deletionMethods: ['arr', 'plex', 'filesystem'],
  backupIntervalDays: 7,
  backupRetentionDays: 28,
} as unknown as Settings;

function renderPage() {
  const router = createMemoryRouter(
    [
      { path: '/settings/general', element: <GeneralPage /> },
      { path: '/system/backup', element: <div>Backups</div> },
    ],
    { initialEntries: ['/settings/general'] },
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <PreferencesProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </PreferencesProvider>,
  );
}

function saveButton() {
  return screen.getAllByRole('button', { name: 'Save Changes' })[0]!;
}

beforeEach(() => {
  mocks.host = { ...HOST };
  mocks.settings = { ...SETTINGS };
  mocks.freshHost = undefined;
  mocks.freshSettings = undefined;
  mocks.gets = [];
  mocks.updateHostCalls = [];
  mocks.updateHostResult = undefined;
  mocks.updateSettingsCalls = [];
  mocks.updateSettingsError = undefined;
  mocks.regenerateCalls = 0;
  mocks.regeneratePasswords = [];
  mocks.revealPasswords = [];
  mocks.revokeCalls = 0;
  mocks.restartCalls = 0;
});

describe('GeneralPage — environment overrides', () => {
  it('disables fields set by environment variables and badges them', () => {
    mocks.host = { ...HOST, envOverrides: ['port', 'urlBase', 'DUPEARR__AUTH__APIKEY', 'logLevel'] };
    renderPage();

    expect(screen.getByLabelText('Port Number')).toBeDisabled();
    expect(screen.getByLabelText('URL Base')).toBeDisabled();
    expect(screen.getByLabelText('Log Level')).toBeDisabled();
    expect(screen.getByLabelText('Port Number')).toHaveAttribute('title', 'Set by DUPEARR__SERVER__PORT');
    expect(screen.getAllByText('Set by environment variable')).toHaveLength(4);
    // Regenerating an env-provided API key would be overwritten on restart.
    expect(screen.getByRole('button', { name: /regenerate/i })).toBeDisabled();

    // Everything else stays editable.
    expect(screen.getByLabelText('Instance Name')).toBeEnabled();
    expect(screen.getByLabelText('Username')).toBeEnabled();
    expect(screen.getByLabelText('Authentication')).toBeEnabled();
  });

  it('keeps everything editable without overrides (null tolerated)', () => {
    mocks.host = { ...HOST, envOverrides: null };
    renderPage();
    expect(screen.getByLabelText('Port Number')).toBeEnabled();
    expect(screen.getByLabelText('URL Base')).toBeEnabled();
    expect(screen.queryByText('Set by environment variable')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /regenerate/i })).toBeEnabled();
  });
});

describe('GeneralPage — security', () => {
  it('warns loudly when authentication is None and offers None only then', () => {
    mocks.host = { ...HOST, authenticationMethod: 'None' };
    renderPage();
    expect(screen.getByText('Authentication is disabled')).toBeInTheDocument();
    const select = screen.getByLabelText('Authentication');
    expect(within(select).getByRole('option', { name: 'None' })).toBeInTheDocument();
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument();
  });

  it('does not offer None when another method is configured', () => {
    renderPage();
    const select = screen.getByLabelText('Authentication');
    expect(within(select).queryByRole('option', { name: 'None' })).not.toBeInTheDocument();
  });

  it('explains External authentication', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText('Authentication'), 'External');
    expect(screen.getByText('External authentication')).toBeInTheDocument();
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument();
  });

  it('asks for a confirmation only when the password changes and blocks mismatches', async () => {
    const user = userEvent.setup();
    renderPage();
    expect(screen.queryByLabelText('Password Confirmation')).not.toBeInTheDocument();

    const password = screen.getByLabelText('Password');
    expect(password).toHaveValue(MASKED_SECRET);
    await user.click(password);
    await user.type(password, 'n3w-secret');
    expect(password).toHaveValue('n3w-secret');

    const confirmation = screen.getByLabelText('Password Confirmation');
    await user.type(confirmation, 'different');
    await user.click(saveButton());
    expect(screen.getByText('Passwords do not match')).toBeInTheDocument();
    expect(mocks.updateHostCalls).toHaveLength(0);

    await user.clear(confirmation);
    await user.type(confirmation, 'n3w-secret');
    // r2-outbound-web#4: a credential change needs the current password (the server refuses it otherwise).
    await user.click(saveButton());
    expect(screen.getByText(/Enter your current password to change/)).toBeInTheDocument();
    expect(mocks.updateHostCalls).toHaveLength(0);
    await user.type(screen.getByLabelText('Current Password'), 'old-secret');
    await user.click(screen.getByLabelText(/Keep the current API key/));
    await user.click(saveButton());
    await waitFor(() => expect(mocks.updateHostCalls).toHaveLength(1));
    expect(mocks.updateHostCalls[0]).toMatchObject({
      password: 'n3w-secret',
      passwordConfirmation: 'n3w-secret',
      currentPassword: 'old-secret',
      keepApiKey: true,
    });
  });

  it('asks for the current password when the username or the authentication settings change', async () => {
    const user = userEvent.setup();
    renderPage();
    expect(screen.queryByLabelText('Current Password')).not.toBeInTheDocument();
    await user.type(screen.getByLabelText('Username'), '2');
    expect(screen.getByLabelText('Current Password')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Current Password'), 'pw');
    await user.click(saveButton());
    await waitFor(() => expect(mocks.updateHostCalls).toHaveLength(1));
    expect(mocks.updateHostCalls[0]).toMatchObject({ username: 'admin2', currentPassword: 'pw' });
  });

  it('never asks for (or sends) the current password for other changes', async () => {
    const user = userEvent.setup();
    renderPage();
    const name = screen.getByLabelText('Instance Name');
    await user.clear(name);
    await user.type(name, 'Mine');
    expect(screen.queryByLabelText('Current Password')).not.toBeInTheDocument();
    await user.click(saveButton());
    await waitFor(() => expect(mocks.updateHostCalls).toHaveLength(1));
    expect('currentPassword' in (mocks.updateHostCalls[0] as object)).toBe(false);
  });

  it('shows the API key only after the current password is entered', async () => {
    const user = userEvent.setup();
    renderPage();
    const field = screen.getByLabelText('API key (hidden)');
    expect(field).toHaveValue(MASKED_SECRET);
    expect(screen.queryByRole('button', { name: 'Copy API key' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show Key' }));
    const dialog = screen.getByRole('dialog', { name: 'Show API Key' });
    await user.click(within(dialog).getByRole('button', { name: 'Show Key' }));
    expect(mocks.revealPasswords).toHaveLength(0); // no password, no request
    await user.type(within(dialog).getByLabelText('Current Password'), 'secret');
    await user.click(within(dialog).getByRole('button', { name: 'Show Key' }));
    expect(mocks.revealPasswords).toEqual(['secret']);
    expect(screen.getByLabelText('API key')).toHaveValue('revealed-0123456789abcdef01234567');
    expect(screen.getByRole('button', { name: 'Copy API key' })).toBeInTheDocument();
  });

  it('logs out all other sessions after a confirmation', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(screen.getByRole('button', { name: 'Log out all other sessions' }));
    const dialog = screen.getByRole('dialog', { name: 'Log Out All Other Sessions' });
    await user.click(within(dialog).getByRole('button', { name: 'Log out' }));
    expect(mocks.revokeCalls).toBe(1);
  });
});

describe('GeneralPage — saving', () => {
  it('sends the masked password and the server API key back unchanged, then offers a restart', async () => {
    const user = userEvent.setup();
    mocks.updateHostResult = { ...HOST, port: 4000, restartRequired: true };
    renderPage();

    const port = screen.getByLabelText('Port Number');
    await user.clear(port);
    await user.type(port, '4000');
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateHostCalls).toHaveLength(1));
    const body = mocks.updateHostCalls[0] as HostConfig;
    expect(body.port).toBe(4000);
    expect(body.password).toBe(MASKED_SECRET);
    expect(body.apiKey).toBe(MASKED_SECRET); // the mask keeps the key
    expect('passwordConfirmation' in body).toBe(false);
    expect(mocks.updateSettingsCalls).toHaveLength(0);

    expect(await screen.findByText('Restart required')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /restart now/i }));
    const dialog = screen.getByRole('dialog', { name: 'Restart Dupearr' });
    await user.click(within(dialog).getByRole('button', { name: 'Restart' }));
    expect(mocks.restartCalls).toBe(1);
  });

  it('saves backup settings through the settings endpoint only', async () => {
    const user = userEvent.setup();
    renderPage();
    const interval = screen.getByLabelText('Backup Interval');
    await user.clear(interval);
    await user.type(interval, '14');
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateSettingsCalls).toHaveLength(1));
    expect(mocks.updateHostCalls).toHaveLength(0);
    expect(mocks.gets).toEqual(['/config/settings']);
    expect(mocks.updateSettingsCalls[0]).toMatchObject({ backupIntervalDays: 14, backupRetentionDays: 28, dryRun: true });
  });

  it('saves 0 for the backup interval (off) and retention (keep forever), like the server allows', async () => {
    const user = userEvent.setup();
    renderPage();
    expect(screen.getByText(/A scheduled backup is created every 7 days\. 0 turns scheduled backups off/)).toBeInTheDocument();
    const interval = screen.getByLabelText('Backup Interval');
    await user.clear(interval);
    await user.type(interval, '0');
    await user.tab(); // blur: the input clamps to its range
    const retention = screen.getByLabelText('Backup Retention');
    await user.clear(retention);
    await user.type(retention, '0');
    await user.tab();
    expect(interval).toHaveValue(0);
    expect(retention).toHaveValue(0);
    expect(screen.getByText(/Scheduled backups are off \(0\)/)).toBeInTheDocument();
    expect(screen.getByText('Dupearr is not backed up automatically.')).toBeInTheDocument();
    expect(screen.getByText(/Keep forever \(0\): scheduled backups are never deleted automatically/)).toBeInTheDocument();
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateSettingsCalls).toHaveLength(1));
    expect(mocks.updateSettingsCalls[0]).toMatchObject({ backupIntervalDays: 0, backupRetentionDays: 0 });
  });

  it("keeps backup values within the server's limits", async () => {
    const user = userEvent.setup();
    renderPage();
    const interval = screen.getByLabelText('Backup Interval');
    expect(interval).toHaveAttribute('min', '0');
    expect(interval).toHaveAttribute('max', '365');
    expect(screen.getByLabelText('Backup Retention')).toHaveAttribute('max', '3650');
    await user.clear(interval);
    await user.type(interval, '400');
    await user.click(saveButton()); // blurs the input first, which clamps it to 365
    await waitFor(() => expect(mocks.updateSettingsCalls).toHaveLength(1));
    expect(mocks.updateSettingsCalls[0]).toMatchObject({ backupIntervalDays: 365 });
  });

  it('maps settings validation errors to the backup fields and labels the others', async () => {
    const user = userEvent.setup();
    const { ApiError } = await import('@/api/client');
    mocks.updateSettingsError = new ApiError('Validation failed', {
      status: 400,
      validationErrors: [
        { propertyName: 'backupIntervalDays', errorMessage: 'Must be between 0 and 365' },
        { propertyName: 'maxBytesPerRunGb', errorMessage: 'Must be between 1 and 1000000' },
      ],
    });
    renderPage();
    const interval = screen.getByLabelText('Backup Interval');
    await user.clear(interval);
    await user.type(interval, '14');
    await user.click(saveButton());

    expect(await screen.findByText('Must be between 0 and 365')).toBeInTheDocument();
    expect(interval).toHaveAttribute('aria-invalid', 'true');
    // Set on Media Management, but refused with this page's save: named so it can be found.
    expect(screen.getByText('Max Size per Run: Must be between 1 and 1000000')).toBeInTheDocument();
  });

  it('never reverts settings changed elsewhere while the page was open (dry run, mode)', async () => {
    const user = userEvent.setup();
    renderPage();
    const interval = screen.getByLabelText('Backup Interval');
    await user.clear(interval);
    await user.type(interval, '14');
    // Meanwhile (another tab / the Media Management page) dry run was turned OFF and auto mode on,
    // and the retention was changed too. Only the field edited here may be written back.
    mocks.freshSettings = { ...SETTINGS, dryRun: false, mode: 'auto', backupRetentionDays: 60 };
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateSettingsCalls).toHaveLength(1));
    expect(mocks.updateSettingsCalls[0]).toEqual({
      ...SETTINGS,
      dryRun: false,
      mode: 'auto',
      backupRetentionDays: 60,
      backupIntervalDays: 14,
    });
  });

  it('keeps a dry run that was switched ON elsewhere (safety setting is never undone)', async () => {
    const user = userEvent.setup();
    mocks.settings = { ...SETTINGS, dryRun: false, mode: 'auto' };
    renderPage();
    const retention = screen.getByLabelText('Backup Retention');
    await user.clear(retention);
    await user.type(retention, '30');
    mocks.freshSettings = { ...SETTINGS, dryRun: true, mode: 'manual' };
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateSettingsCalls).toHaveLength(1));
    expect(mocks.updateSettingsCalls[0]).toMatchObject({ dryRun: true, mode: 'manual', backupRetentionDays: 30 });
  });

  it('writes only the edited host fields over a fresh read (API key regenerated meanwhile)', async () => {
    const user = userEvent.setup();
    renderPage();
    const name = screen.getByLabelText('Instance Name');
    await user.clear(name);
    await user.type(name, 'Dupearr 2');
    mocks.freshHost = { ...HOST, apiKey: 'ffffffffffffffffffffffffffffffff', authenticationRequired: 'DisabledForLocalAddresses', logLevel: 'debug' };
    await user.click(saveButton());

    await waitFor(() => expect(mocks.updateHostCalls).toHaveLength(1));
    expect(mocks.gets).toEqual(['/config/host']);
    expect(mocks.updateHostCalls[0]).toMatchObject({
      instanceName: 'Dupearr 2',
      apiKey: 'ffffffffffffffffffffffffffffffff',
      authenticationRequired: 'DisabledForLocalAddresses',
      logLevel: 'debug',
      password: MASKED_SECRET,
    });
  });

  it('does not save when the fresh read fails', async () => {
    const user = userEvent.setup();
    renderPage();
    const interval = screen.getByLabelText('Backup Interval');
    await user.clear(interval);
    await user.type(interval, '3');
    mocks.freshSettings = Promise.reject(new Error('Unable to reach Dupearr'));
    (mocks.freshSettings as Promise<unknown>).catch(() => {});
    await user.click(saveButton());

    expect(await screen.findAllByText('Unable to reach Dupearr')).not.toHaveLength(0);
    expect(mocks.updateSettingsCalls).toHaveLength(0);
  });

  it('rejects values the server would refuse (bind address, URL base)', async () => {
    const user = userEvent.setup();
    localStorage.setItem(PREFERENCES_STORAGE_KEY, JSON.stringify({ showAdvanced: true }));
    try {
      renderPage();
      const bind = screen.getByLabelText('Bind Address');
      await user.clear(bind);
      await user.type(bind, 'localhost');
      const urlBase = screen.getByLabelText('URL Base');
      await user.type(urlBase, '/a/../b');
      // The server accepts 1–10 MB; the field clamps to that range.
      const size = screen.getByLabelText('Log Size Limit');
      await user.clear(size);
      await user.type(size, '50');
      await user.click(saveButton());

      expect(screen.getByText('Bind address must be * or an IP address')).toBeInTheDocument();
      expect(screen.getByText('URL base must not contain "." or ".." segments')).toBeInTheDocument();
      expect(size).toHaveValue(10);
      expect(mocks.updateHostCalls).toHaveLength(0);
      expect(mocks.gets).toHaveLength(0);
    } finally {
      localStorage.removeItem(PREFERENCES_STORAGE_KEY);
    }
  });

  it('validates host fields before saving', async () => {
    const user = userEvent.setup();
    renderPage();
    const name = screen.getByLabelText('Instance Name');
    await user.clear(name);
    await user.click(saveButton());
    expect(screen.getByText('Instance name is required')).toBeInTheDocument();
    expect(mocks.updateHostCalls).toHaveLength(0);
  });

  it('regenerates the API key only after confirmation with the current password', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(screen.getByRole('button', { name: /regenerate/i }));
    const dialog = screen.getByRole('dialog', { name: 'Regenerate API Key' });
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(mocks.regenerateCalls).toBe(0);

    await user.click(screen.getByRole('button', { name: /regenerate/i }));
    const again = screen.getByRole('dialog', { name: 'Regenerate API Key' });
    await user.click(within(again).getByRole('button', { name: 'Regenerate' }));
    expect(mocks.regenerateCalls).toBe(0); // the password is required
    await user.type(within(again).getByLabelText('Current Password'), 'secret');
    await user.click(within(again).getByRole('button', { name: 'Regenerate' }));
    expect(mocks.regeneratePasswords).toEqual(['secret']);
    // The new key is shown once (the web UI itself does not use it).
    expect(screen.getByLabelText('API key')).toHaveValue('new-key-ffffffffffffffffffffffff');
  });
});
