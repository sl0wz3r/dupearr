import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { queryKeys } from '@/api/queryKeys';
import type { PathMapping, Settings } from '@/api/types';
import { PREFERENCES_STORAGE_KEY, PreferencesProvider } from '@/app/preferences';
import MediaManagementPage from '@/pages/settings/MediaManagementPage';
import { installFetchMock, jsonResponse, renderWithProviders } from './test-utils';

const SETTINGS: Settings = {
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
};

interface SetupOptions {
  putResponse?: (body: unknown) => unknown;
  /** GET /config/settings body (default: the current settings). */
  getResponse?: () => unknown;
  mappings?: PathMapping[];
  /** Render with "Show Advanced" on. */
  advanced?: boolean;
}

function setup(settings: Partial<Settings> = {}, putResponseOrOptions?: ((body: unknown) => unknown) | SetupOptions) {
  const opts: SetupOptions =
    typeof putResponseOrOptions === 'function' ? { putResponse: putResponseOrOptions } : (putResponseOrOptions ?? {});
  const { putResponse } = opts;
  let current: Settings = { ...SETTINGS, ...settings };
  const server = {
    get: () => current,
    /** Simulates a change made elsewhere (another tab, the API). */
    change: (patch: Partial<Settings>) => {
      current = { ...current, ...patch };
    },
  };
  const api = installFetchMock([
    { method: 'GET', path: '/api/v1/config/settings', respond: () => (opts.getResponse ? opts.getResponse() : current) },
    {
      method: 'PUT',
      path: '/api/v1/config/settings',
      respond: (body) => {
        if (putResponse) return putResponse(body);
        current = body as Settings;
        return current;
      },
    },
    { method: 'GET', path: '/api/v1/pathmapping', respond: () => opts.mappings ?? [] },
    { method: 'GET', path: '/api/v1/mediaserver', respond: () => [] },
    { method: 'GET', path: '/api/v1/arr', respond: () => [] },
    { method: 'GET', path: '/api/v1/library', respond: () => [] },
    { method: 'GET', path: '/api/v1/system/status', respond: () => ({}) },
    { method: 'GET', path: '/api/v1/health', respond: () => [] },
    { method: 'GET', path: '/api/v1/system/task', respond: () => [] },
  ]);
  const user = userEvent.setup();
  if (opts.advanced) localStorage.setItem(PREFERENCES_STORAGE_KEY, JSON.stringify({ showAdvanced: true }));
  const { queryClient } = renderWithProviders(
    opts.advanced ? (
      <PreferencesProvider>
        <MediaManagementPage />
      </PreferencesProvider>
    ) : (
      <MediaManagementPage />
    ),
  );
  /** Refetches the settings like an SSE `settings` event does. */
  const refetchSettings = () => queryClient.invalidateQueries({ queryKey: queryKeys.config.settings });
  return { api, user, server, queryClient, refetchSettings };
}

function lastPut(api: ReturnType<typeof installFetchMock>): Settings {
  const puts = api.find('PUT', '/api/v1/config/settings');
  return puts[puts.length - 1]!.body as Settings;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<MediaManagementPage> dry run gate', () => {
  it('asks for confirmation before turning dry run off, and cancelling keeps it on', async () => {
    const { user, api } = setup();
    const dryRun = await screen.findByRole('switch', { name: 'Dry run' });
    expect(dryRun).toHaveAttribute('aria-checked', 'true');

    await user.click(dryRun);
    const dialog = await screen.findByRole('dialog', { name: 'Turn Off Dry Run?' });
    expect(within(dialog).getByText(/approved removals really delete files/)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Keep Dry Run On' }));
    expect(screen.queryByRole('dialog', { name: 'Turn Off Dry Run?' })).not.toBeInTheDocument();
    expect(dryRun).toHaveAttribute('aria-checked', 'true');
    // Nothing changed → no save bar, nothing sent.
    expect(screen.queryByRole('button', { name: 'Save Changes' })).not.toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('turns dry run off only after confirming, then saves it', async () => {
    const { user, api } = setup();
    const dryRun = await screen.findByRole('switch', { name: 'Dry run' });

    await user.click(dryRun);
    const dialog = await screen.findByRole('dialog', { name: 'Turn Off Dry Run?' });
    await user.click(within(dialog).getByRole('button', { name: 'Turn Off Dry Run' }));

    expect(dryRun).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByText('Dry run is off: approved removals really delete files.')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    const body = api.find('PUT', '/api/v1/config/settings')[0]!.body as Settings;
    expect(body.dryRun).toBe(false);
    // Every other field is sent back unchanged (PUT replaces the resource).
    expect(body).toEqual({ ...SETTINGS, dryRun: false });
  });

  it('turns dry run back on without a confirmation', async () => {
    const { user } = setup({ dryRun: false });
    const dryRun = await screen.findByRole('switch', { name: 'Dry run' });
    expect(dryRun).toHaveAttribute('aria-checked', 'false');
    await user.click(dryRun);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(dryRun).toHaveAttribute('aria-checked', 'true');
  });

  it('mentions automatic mode in the confirmation', async () => {
    const { user } = setup({ mode: 'auto' });
    await user.click(await screen.findByRole('switch', { name: 'Dry run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Turn Off Dry Run?' });
    expect(within(dialog).getByText(/Automatic mode is on/)).toBeInTheDocument();
  });
});

describe('<MediaManagementPage> form', () => {
  it('shows the stable scans setting only in automatic mode', async () => {
    const { user } = setup();
    const mode = await screen.findByLabelText('Approval Mode');
    expect(screen.queryByLabelText('Stable Scans Required')).not.toBeInTheDocument();
    await user.selectOptions(mode, 'auto');
    expect(screen.getByLabelText('Stable Scans Required')).toHaveValue(2);
    expect(screen.getByText(/seen identically in 2 consecutive full scans/)).toBeInTheDocument();
  });

  it('explains minimum age and scan interval in human units', async () => {
    setup({ minAgeHours: 36, scanIntervalMinutes: 0 });
    expect(await screen.findByText(/less than 1 day 12 hours ago \(or with an unknown date\) are never removed/)).toBeInTheDocument();
    expect(screen.getByText(/Scheduled scans are off/)).toBeInTheDocument();
  });

  it('says the minimum age is off at 0 hours instead of "less than 0 hours"', async () => {
    setup({ minAgeHours: 0 });
    expect(await screen.findByText(/Off \(0\): copies can be removed as soon as they are detected/)).toBeInTheDocument();
    expect(screen.queryByText(/less than 0 hours/)).not.toBeInTheDocument();
    expect(screen.getByText('Recently added files are not protected.')).toBeInTheDocument();
  });

  it('saves 0 (keep forever) for the recycle bin cleanup and explains it', async () => {
    const { user, api } = setup({ recycleBinPath: '/data/.dupearr-recycle' });
    const cleanup = await screen.findByLabelText('Recycle Bin Cleanup');
    expect(screen.getByText(/moved to the recycle bin more than 7 days ago are permanently deleted/)).toBeInTheDocument();
    await user.clear(cleanup);
    await user.type(cleanup, '0');
    await user.tab(); // blur: the input clamps to its range
    expect(cleanup).toHaveValue(0);
    expect(screen.getByText(/Keep forever \(0\): the Clean Recycle Bin task never deletes anything/)).toBeInTheDocument();
    expect(screen.getByText('The recycle bin grows until you empty it.')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api).recycleBinCleanupDays).toBe(0);
  });

  it('saves 0 (keep forever) for the history retention', async () => {
    const { user, api } = setup({}, { advanced: true });
    const retention = await screen.findByLabelText('History Retention');
    await user.clear(retention);
    await user.type(retention, '0');
    await user.tab();
    expect(retention).toHaveValue(0);
    expect(screen.getByText(/Keep forever \(0\): the Housekeeping task never deletes history events/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api).historyRetentionDays).toBe(0);
  });

  it('allows up to 100 stable scans, like the server', async () => {
    const { user, api } = setup({ mode: 'auto' });
    const scans = await screen.findByLabelText('Stable Scans Required');
    expect(scans).toHaveAttribute('max', '100');
    await user.clear(scans);
    await user.type(scans, '100');
    await user.tab();
    expect(scans).toHaveValue(100);
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api).stableScansRequired).toBe(100);
  });

  it('refuses a scan interval below 15 minutes (0 turns scans off)', async () => {
    const { user, api } = setup();
    const interval = await screen.findByLabelText('Scan Interval');
    await user.clear(interval);
    await user.type(interval, '10');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(await screen.findByText('Must be 0 (scheduled scans off) or at least 15 minutes')).toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('describes deletion method permanence and warns without a recycle bin', async () => {
    const { user } = setup();
    const details = await screen.findByRole('list', { name: 'Deletion method details' });
    expect(within(details).getByText(/permanent unless its recycle bin is set/)).toBeInTheDocument();
    expect(within(details).getByText(/requires “Allow media deletion”/)).toBeInTheDocument();
    expect(within(details).getByText('Permanent (no recycle bin)')).toBeInTheDocument();
    expect(screen.getByText('No recycle bin: filesystem removals are permanent.')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Recycle Bin'), '/data/.dupearr-recycle');
    expect(within(details).getByText('Recoverable (recycle bin)')).toBeInTheDocument();
    expect(screen.queryByText('No recycle bin: filesystem removals are permanent.')).not.toBeInTheDocument();
  });

  it('reorders deletion methods', async () => {
    const { user, api } = setup();
    await user.click(await screen.findByRole('button', { name: 'Move Plex up' }));
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect((api.find('PUT', '/api/v1/config/settings')[0]!.body as Settings).deletionMethods).toEqual([
      'plex',
      'arr',
      'filesystem',
    ]);
  });

  it('blocks saving a relative recycle bin path', async () => {
    const { user, api } = setup();
    await user.type(await screen.findByLabelText('Recycle Bin'), 'recycle');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(await screen.findByText('Use an absolute path, e.g. /data/.dupearr-recycle')).toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('maps server validation errors onto fields', async () => {
    const { user } = setup({}, () =>
      jsonResponse(
        [
          { propertyName: 'MaxDeletionsPerRun', errorMessage: 'Must be at most 1000' },
          { propertyName: 'somethingElse', errorMessage: 'Unexpected problem' },
        ],
        400,
      ),
    );
    const max = await screen.findByLabelText('Max Deletions per Run');
    await user.clear(max);
    await user.type(max, '5000');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(await screen.findByText('Must be at most 1000')).toBeInTheDocument();
    // Errors on properties the page doesn't show still name the property.
    expect(screen.getByText('Something Else: Unexpected problem')).toBeInTheDocument();
  });

  it('maps the server settings validation (camelCase property names) to the right fields', async () => {
    const { user } = setup({}, () =>
      jsonResponse(
        [
          { propertyName: 'maxBytesPerRunGb', errorMessage: 'Must be between 1 and 1000000' },
          { propertyName: 'deletionMethods[1]', errorMessage: "Deletion method 'plex' is listed twice" },
          { propertyName: 'recycleBinPath', errorMessage: 'Must not be a filesystem root' },
          // Advanced fields (hidden by default) are revealed with their error.
          { propertyName: 'durationTolerancePercent', errorMessage: 'Must be between 0 and 100' },
          { propertyName: 'historyRetentionDays', errorMessage: 'Must be between 0 and 36500' },
          // Edited on another page: listed in the save bar under its label.
          { propertyName: 'backupIntervalDays', errorMessage: 'Must be between 0 and 365' },
        ],
        400,
      ),
    );
    const max = await screen.findByLabelText('Max Deletions per Run');
    await user.clear(max);
    await user.type(max, '30');
    expect(screen.queryByLabelText('Duration Tolerance')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));

    const errorOf = (label: string, message: string) => {
      const input = screen.getByLabelText(label);
      const group = input.closest('.grid') as HTMLElement;
      expect(within(group).getByText(message)).toBeInTheDocument();
    };
    expect(await screen.findByText('Must be between 1 and 1000000')).toBeInTheDocument();
    errorOf('Max Size per Run', 'Must be between 1 and 1000000');
    errorOf('Recycle Bin', 'Must not be a filesystem root');
    errorOf('Duration Tolerance', 'Must be between 0 and 100');
    errorOf('History Retention', 'Must be between 0 and 36500');
    expect(screen.getByText("Deletion method 'plex' is listed twice")).toBeInTheDocument();
    expect(screen.getByText('Backup Interval: Must be between 0 and 365')).toBeInTheDocument();
    expect(screen.getByText('Some settings are invalid — fix the highlighted fields.')).toBeInTheDocument();
  });
});

describe('<MediaManagementPage> concurrent changes', () => {
  it('never re-sends a stale dry run value changed elsewhere while editing another field', async () => {
    const { user, api, server, refetchSettings } = setup({ dryRun: false });
    const dryRun = await screen.findByRole('switch', { name: 'Dry run' });
    expect(dryRun).toHaveAttribute('aria-checked', 'false');

    const max = screen.getByLabelText('Max Deletions per Run');
    await user.clear(max);
    await user.type(max, '10');
    await user.tab();

    // Someone turns dry run back on (emergency stop) while this form is dirty.
    server.change({ dryRun: true, scanIntervalMinutes: 120 });
    await refetchSettings();
    await waitFor(() => expect(dryRun).toHaveAttribute('aria-checked', 'true'));

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api)).toEqual({ ...SETTINGS, dryRun: true, scanIntervalMinutes: 120, maxDeletionsPerRun: 10 });
  });

  it('keeps the fields the user did edit when the server changes others', async () => {
    const { user, api, server, refetchSettings } = setup();
    await user.click(await screen.findByRole('switch', { name: 'Dry run' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Turn Off Dry Run' }));

    server.change({ minAgeHours: 48 });
    await refetchSettings();
    await waitFor(() => expect(screen.getByLabelText('Minimum Age')).toHaveValue(48));
    expect(screen.getByRole('switch', { name: 'Dry run' })).toHaveAttribute('aria-checked', 'false');

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api)).toEqual({ ...SETTINGS, dryRun: false, minAgeHours: 48 });
  });

  it('forgets an edit once it matches the server, so later server changes are shown', async () => {
    const { user, server, refetchSettings } = setup();
    const dryRun = await screen.findByRole('switch', { name: 'Dry run' });
    await user.click(dryRun);
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Turn Off Dry Run' }));
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeInTheDocument();

    // Saved elsewhere with the same value → nothing left to save here.
    server.change({ dryRun: false });
    await refetchSettings();
    await waitFor(() => expect(screen.queryByText('You have unsaved changes')).not.toBeInTheDocument());

    // Then dry run is switched back on elsewhere: the page must show it, not the old local edit.
    server.change({ dryRun: true });
    await refetchSettings();
    await waitFor(() => expect(dryRun).toHaveAttribute('aria-checked', 'true'));
    expect(screen.queryByText('You have unsaved changes')).not.toBeInTheDocument();
  });
});

describe('<MediaManagementPage> automatic mode gate', () => {
  it('asks for confirmation before automatic mode when dry run is off', async () => {
    const { user, api } = setup({ dryRun: false });
    const mode = await screen.findByLabelText('Approval Mode');
    await user.selectOptions(mode, 'auto');
    const dialog = await screen.findByRole('dialog', { name: 'Turn On Automatic Mode?' });
    expect(within(dialog).getByText(/really deleted without asking/)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Stay in Manual Mode' }));
    expect(mode).toHaveValue('manual');
    expect(screen.queryByLabelText('Stable Scans Required')).not.toBeInTheDocument();

    await user.selectOptions(mode, 'auto');
    await user.click(
      within(await screen.findByRole('dialog', { name: 'Turn On Automatic Mode?' })).getByRole('button', {
        name: 'Use Automatic Mode',
      }),
    );
    expect(mode).toHaveValue('auto');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api)).toEqual({ ...SETTINGS, dryRun: false, mode: 'auto' });
  });

  it('switches to automatic mode without a dialog while dry run is on', async () => {
    const { user } = setup();
    const mode = await screen.findByLabelText('Approval Mode');
    await user.selectOptions(mode, 'auto');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(mode).toHaveValue('auto');
  });
});

describe('<MediaManagementPage> incomplete server data', () => {
  it('refuses to save settings with missing fields (a PUT would reset them, e.g. dryRun=false)', async () => {
    const { dryRun: _dryRun, maxDeletionsPerRun: _max, ...partial } = SETTINGS;
    const { user, api } = setup({}, { getResponse: () => partial });
    expect(await screen.findByText(/These fields are missing or invalid: dryRun, maxDeletionsPerRun/)).toBeInTheDocument();

    await user.type(screen.getByLabelText('Recycle Bin'), '/data/.dupearr-recycle');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(screen.getByText(/Saving is disabled: the server returned incomplete settings/)).toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('reloads the settings when the save response is not a Settings object', async () => {
    let calls = 0;
    const { user, api } = setup({}, (body) => {
      calls++;
      return calls === 1 ? {} : body;
    });
    await user.type(await screen.findByLabelText('Recycle Bin'), '/data/.dupearr-recycle');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    // The empty body was written to the cache by the mutation; the page refetches instead of using it.
    await waitFor(() => expect(api.find('GET', '/api/v1/config/settings').length).toBeGreaterThanOrEqual(2));
    expect(await screen.findByRole('switch', { name: 'Dry run' })).toHaveAttribute('aria-checked', 'true');
    expect(screen.queryByText(/These fields are missing/)).not.toBeInTheDocument();
  });
});

describe('<MediaManagementPage> recycle bin', () => {
  const mappings: PathMapping[] = [{ id: 1, sourceType: 'server', sourceId: 1, remotePath: '/data/media', localPath: '/data/media' }];

  it('blocks a recycle bin that contains a mapped media folder', async () => {
    const { user, api } = setup({}, { mappings });
    const bin = await screen.findByLabelText('Recycle Bin');
    await waitFor(() => expect(screen.getByRole('table', { name: 'Path mappings' })).toBeInTheDocument());
    await user.type(bin, '/data');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(await screen.findByText(/must not contain the media folder \/data\/media/)).toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('warns about a recycle bin inside a media folder but still saves it', async () => {
    const { user, api } = setup({}, { mappings });
    await waitFor(() => expect(screen.getByRole('table', { name: 'Path mappings' })).toBeInTheDocument());
    await user.type(await screen.findByLabelText('Recycle Bin'), '/data/media/recycle');
    expect(screen.getByText(/inside the media folder \/data\/media/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api).recycleBinPath).toBe('/data/media/recycle');
  });
});

describe('<MediaManagementPage> full-disc backups', () => {
  it('shows the disc toggles with their defaults and help', async () => {
    setup();
    const detect = await screen.findByRole('switch', { name: 'Detect Full-Disc Backups' });
    expect(detect).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByRole('switch', { name: 'Allow removing full discs' })).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('switch', { name: 'Always Keep a Plex-Playable Copy' })).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByText(/Blu-ray \(BDMV\), DVD \(VIDEO_TS\), HD DVD and AVCHD folders and ISO images/)).toBeInTheDocument();
    expect(screen.getByText(/Off \(recommended\): every disc is kept and shown as protected/)).toBeInTheDocument();
    expect(screen.getByText(/Plex can’t play a disc folder/)).toBeInTheDocument();
  });

  it('asks for confirmation before allowing disc removal, and cancelling keeps discs protected', async () => {
    const { user, api } = setup();
    const allow = await screen.findByRole('switch', { name: 'Allow removing full discs' });

    await user.click(allow);
    const dialog = await screen.findByRole('dialog', { name: 'Allow Removing Full Discs?' });
    expect(within(dialog).getByText('Only when you approve its group yourself')).toBeInTheDocument();
    expect(within(dialog).getByText(/Never through Plex or Radarr\/Sonarr/)).toBeInTheDocument();
    // The default settings have no recycle bin.
    expect(within(dialog).getByText(/No recycle bin is set yet/)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Keep Discs Protected' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(allow).toHaveAttribute('aria-checked', 'false');
    expect(screen.queryByRole('button', { name: 'Save Changes' })).not.toBeInTheDocument();
    expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(0);
  });

  it('allows disc removal after confirming, warns without a recycle bin, then saves it', async () => {
    const { user, api } = setup();
    const allow = await screen.findByRole('switch', { name: 'Allow removing full discs' });
    await user.click(allow);
    const dialog = await screen.findByRole('dialog', { name: 'Allow Removing Full Discs?' });
    await user.click(within(dialog).getByRole('button', { name: 'Allow Disc Removal' }));

    expect(allow).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByText('No recycle bin is set: every disc removal is refused until you set one below.')).toBeInTheDocument();
    expect(screen.getByText(/On: a disc that loses to a better copy can be removed — only when you approve its group yourself/)).toBeInTheDocument();

    await user.type(screen.getByLabelText('Recycle Bin'), '/data/.dupearr-recycle');
    expect(screen.queryByText(/every disc removal is refused/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api)).toEqual({ ...SETTINGS, allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle' });
  });

  it('turns disc removal off without a confirmation', async () => {
    const { user, api } = setup({ allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle' });
    const allow = await screen.findByRole('switch', { name: 'Allow removing full discs' });
    expect(allow).toHaveAttribute('aria-checked', 'true');
    await user.click(allow);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api).allowDiscRemoval).toBe(false);
  });

  it('saves disc detection and the playable-copy rule, warning when a disc may be the only copy', async () => {
    const { user, api } = setup();
    await user.click(await screen.findByRole('switch', { name: 'Detect Full-Disc Backups' }));
    expect(screen.getByText(/Off: discs next to your movies are not shown/)).toBeInTheDocument();
    await user.click(screen.getByRole('switch', { name: 'Always Keep a Plex-Playable Copy' }));
    expect(screen.getByText(/A disc can become the only kept copy/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(api.find('PUT', '/api/v1/config/settings')).toHaveLength(1));
    expect(lastPut(api)).toEqual({ ...SETTINGS, detectDiscs: false, keepPlayableCopy: false });
  });
});
