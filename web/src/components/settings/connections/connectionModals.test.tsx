/** Connection modals (Media Servers / Applications / Connect) with mocked API hooks — no network. */
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/api/client';
import type { ArrInstance, MediaServer, NotificationConfig, ProviderSchema } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { ArrInstanceModal } from './ArrInstanceModal';
import { MediaServerModal } from './MediaServerModal';
import { NotificationModal } from './NotificationModal';

type UseState = typeof import('react').useState;
type Opts = { onSuccess?: (data: unknown, vars: unknown) => void; onError?: (e: unknown) => void };

const mocks = vi.hoisted(() => {
  const calls: Record<string, unknown[]> = {};
  const results: Record<string, unknown> = {};
  const resolve = (name: string, vars: unknown) => {
    const r = results[name];
    return typeof r === 'function' ? (r as (v: unknown) => unknown)(vars) : r;
  };
  /** A useMutation stand-in: records variables, resolves with `results[name]` (an Error rejects). */
  const mutation = (name: string, useState: UseState) => () => {
    const [state, setState] = useState<{ data?: unknown; error?: unknown }>({});
    return {
      ...state,
      error: state.error ?? null,
      isPending: false,
      isSuccess: state.data !== undefined && !state.error,
      reset: () => setState({}),
      mutate: (vars: unknown, opts?: Opts) => {
        (calls[name] ??= []).push(vars);
        const r = resolve(name, vars);
        if (r instanceof Error) {
          setState({ error: r });
          opts?.onError?.(r);
        } else {
          setState({ data: r ?? {} });
          opts?.onSuccess?.(r, vars);
        }
      },
    };
  };
  /** The configured media servers useMediaServers answers with (one by default). */
  const servers: { list: unknown[] } = { list: [] };
  return { calls, results, mutation, servers };
});

vi.mock('@/api/hooks/useArr', async () => {
  const { useState } = await import('react');
  return {
    useCreateArrInstance: mocks.mutation('createArr', useState),
    useUpdateArrInstance: mocks.mutation('updateArr', useState),
    useDeleteArrInstance: mocks.mutation('deleteArr', useState),
    useTestArrInstance: mocks.mutation('testArr', useState),
  };
});

vi.mock('@/api/hooks/useMediaServers', async () => {
  const { useState } = await import('react');
  return {
    useCreateMediaServer: mocks.mutation('createServer', useState),
    useUpdateMediaServer: mocks.mutation('updateServer', useState),
    useDeleteMediaServer: mocks.mutation('deleteServer', useState),
    useTestMediaServer: mocks.mutation('testServer', useState),
    useMediaServers: () => ({ data: mocks.servers.list, isPending: false, isError: false, error: null }),
    useCreatePlexPin: mocks.mutation('createPin', useState),
    usePlexPin: () => ({ data: undefined, error: null }),
    usePlexServers: () => ({ data: undefined, isPending: true, isError: false, error: null, refetch: () => Promise.resolve() }),
  };
});

const SCHEMAS: ProviderSchema[] = [
  {
    kind: 'discord',
    name: 'Discord',
    infoUrl: 'https://support.discord.com/hc/en-us/articles/228383668',
    fields: [
      { name: 'webhookUrl', label: 'Webhook URL', type: 'url', required: true, secret: true },
      { name: 'username', label: 'Username', type: 'text' },
    ],
  },
  { kind: 'ntfy', name: 'ntfy', fields: [{ name: 'topic', label: 'Topic', type: 'text', required: true }] },
  {
    kind: 'gotify',
    name: 'Gotify',
    fields: [
      { name: 'serverUrl', label: 'Server URL', type: 'url', required: true },
      { name: 'appToken', label: 'App Token', type: 'password', required: true, secret: true },
    ],
  },
  {
    kind: 'webhook',
    name: 'Webhook',
    fields: [
      { name: 'url', label: 'URL', type: 'url', required: true },
      { name: 'username', label: 'Username', type: 'text' },
      { name: 'password', label: 'Password', type: 'password', secret: true },
      { name: 'headers', label: 'Headers', type: 'textarea', advanced: true },
    ],
  },
];

const TRIGGERS = [
  { value: 'onDuplicatesFound', label: 'On Duplicates Found' },
  { value: 'onFileDeleted', label: 'On File Deleted' },
  { value: 'onDeleteFailed', label: 'On Delete Failed' },
  { value: 'onScanCompleted', label: 'On Scan Completed' },
  { value: 'onHealthIssue', label: 'On Health Issue' },
];

vi.mock('@/api/hooks/useNotifications', async () => {
  const { useState } = await import('react');
  return {
    useNotificationSchema: () => ({ data: SCHEMAS, isPending: false, isError: false, error: null, refetch: () => Promise.resolve() }),
    useNotificationTriggers: () => ({ data: TRIGGERS, isPending: false, isError: false, error: null }),
    useCreateNotification: mocks.mutation('createNotification', useState),
    useUpdateNotification: mocks.mutation('updateNotification', useState),
    useDeleteNotification: mocks.mutation('deleteNotification', useState),
    useTestNotification: mocks.mutation('testNotification', useState),
  };
});

beforeEach(() => {
  for (const k of Object.keys(mocks.calls)) delete mocks.calls[k];
  for (const k of Object.keys(mocks.results)) delete mocks.results[k];
  mocks.servers.list = [];
});

const ARR_KEY = '0123456789abcdef0123456789abcdef';

/** Lets the modal's initial-focus animation frame run so it doesn't steal focus mid-typing. */
async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 40)));
}

describe('ArrInstanceModal', () => {
  it('adds Radarr: provider picker → test (permanent-delete warning) → save anyway', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<ArrInstanceModal instance={null} onClose={onClose} />);

    expect(screen.getByRole('dialog', { name: 'Add Application' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add Radarr' }));
    await settle();
    expect(screen.getByRole('dialog', { name: 'Add Radarr' })).toBeInTheDocument();
    expect(screen.getByLabelText('Name')).toHaveValue('Radarr');
    expect(screen.getByLabelText('URL')).toHaveAttribute('placeholder', 'http://radarr:7878');

    await user.type(screen.getByLabelText('URL'), 'http://radarr:7878/');
    await user.type(screen.getByLabelText('API Key'), ARR_KEY);

    mocks.results.testArr = { appName: 'Radarr', version: '5.14.0', instanceName: 'Radarr', recycleBin: '', recycleBinCleanupDays: 7 };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Connected to Radarr')).toBeInTheDocument();
    expect(screen.getByText('5.14.0')).toBeInTheDocument();
    expect(screen.getByText(/deletions through this instance are PERMANENT/)).toBeInTheDocument();
    expect(mocks.calls.testArr).toEqual([expect.objectContaining({ kind: 'radarr', url: 'http://radarr:7878', apiKey: ARR_KEY })]);

    mocks.results.createArr = (vars: unknown) =>
      (vars as { forceSave: boolean }).forceSave ? { id: 1, name: 'Radarr' } : new ApiError('Unable to connect to Radarr', { status: 400 });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('Unable to connect to Radarr')).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Save anyway' }));
    expect(mocks.calls.createArr).toEqual([
      { instance: expect.objectContaining({ kind: 'radarr', url: 'http://radarr:7878', tags: [] }), forceSave: false },
      { instance: expect.objectContaining({ kind: 'radarr' }), forceSave: true },
    ]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('warns when the URL points at the other application and when the recycle bin is set shows it', async () => {
    const user = userEvent.setup();
    render(<ArrInstanceModal instance={null} onClose={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Add Radarr' }));
    await settle();
    await user.type(screen.getByLabelText('URL'), 'http://sonarr:8989');
    await user.type(screen.getByLabelText('API Key'), ARR_KEY);
    mocks.results.testArr = { appName: 'Sonarr', version: '4.0.0', instanceName: 'Sonarr', recycleBin: '/data/.recycle', recycleBinCleanupDays: 7 };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('This is Sonarr, not Radarr')).toBeInTheDocument();
    expect(screen.getByText('/data/.recycle')).toBeInTheDocument();
    expect(screen.queryByText(/PERMANENT/)).not.toBeInTheDocument();
  });

  it('validates before calling the API', async () => {
    const user = userEvent.setup();
    render(<ArrInstanceModal instance={null} onClose={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Add Sonarr' }));
    await settle();
    await user.type(screen.getByLabelText('URL'), 'sonarr:8989');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('URL must start with http:// or https://')).toBeInTheDocument();
    expect(screen.getByText('API key is required')).toBeInTheDocument();
    expect(mocks.calls.createArr).toBeUndefined();
  });

  it('edits with the masked key and deletes after confirmation', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const instance: ArrInstance = {
      id: 7,
      name: 'Radarr 4K',
      kind: 'radarr',
      url: 'http://radarr4k:7878',
      apiKey: MASKED_SECRET,
      verifyTls: true,
      enabled: true,
      tags: null,
      createdAt: '',
      updatedAt: '',
    };
    render(<ArrInstanceModal instance={instance} onClose={onClose} />);
    expect(screen.getByLabelText('API Key')).toHaveValue(MASKED_SECRET);

    mocks.results.updateArr = { ...instance };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ id: 7, apiKey: MASKED_SECRET, tags: [] }), forceSave: false },
    ]);

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    const confirm = screen.getByRole('dialog', { name: 'Delete Radarr' });
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }));
    expect(mocks.calls.deleteArr).toEqual([7]);
  });

  it('never sends the stored API key to a new host', async () => {
    const user = userEvent.setup();
    const instance: ArrInstance = {
      id: 7,
      name: 'Radarr',
      kind: 'radarr',
      url: 'http://radarr:7878',
      apiKey: MASKED_SECRET,
      verifyTls: true,
      enabled: true,
      tags: [],
      createdAt: '',
      updatedAt: '',
    };
    render(<ArrInstanceModal instance={instance} onClose={vi.fn()} />);
    await settle();
    const url = screen.getByLabelText('URL');
    await user.clear(url);
    await user.type(url, 'http://attacker.example:7878');

    await user.click(screen.getByRole('button', { name: 'Test' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText(/Enter the API key again/)).toBeInTheDocument();
    expect(mocks.calls.testArr).toBeUndefined();
    expect(mocks.calls.updateArr).toBeUndefined();

    // Entering the key again (or going back to the saved host) unblocks it.
    await user.click(screen.getByLabelText('API Key'));
    await user.keyboard(ARR_KEY);
    mocks.results.updateArr = { ...instance };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ url: 'http://attacker.example:7878', apiKey: ARR_KEY }), forceSave: false },
    ]);
  });
});

describe('MediaServerModal', () => {
  it('tests (explaining how to allow media deletion) and saves a new server', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<MediaServerModal server={null} onClose={onClose} />);
    await settle();

    await user.type(screen.getByLabelText('URL'), 'http://10.0.0.2:32400');
    await user.type(screen.getByLabelText('Token'), 'plex-token-123');
    mocks.results.testServer = { version: '1.41.2', machineIdentifier: 'm-1', friendlyName: 'Home', mediaDeletionAllowed: false };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Connected to Plex')).toBeInTheDocument();
    expect(screen.getByText('Home')).toBeInTheDocument();
    expect(screen.getByText('“Allow media deletion” is off')).toBeInTheDocument();

    mocks.results.createServer = { id: 1, name: 'Plex' };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.createServer).toEqual([
      {
        server: { name: 'Plex', kind: 'plex', url: 'http://10.0.0.2:32400', token: 'plex-token-123', verifyTls: true, enabled: true },
        forceSave: false,
      },
    ]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it.each([
    [true, 'Yes', false],
    [false, 'No', true],
    [null, 'Unknown', false],
    [undefined, 'Unknown', false],
  ] as const)('shows the owner token status (owned: %s → %s)', async (owned, label, warns) => {
    const user = userEvent.setup();
    render(<MediaServerModal server={null} onClose={vi.fn()} />);
    await settle();
    await user.type(screen.getByLabelText('URL'), 'http://10.0.0.2:32400');
    await user.type(screen.getByLabelText('Token'), 'plex-token-123');
    mocks.results.testServer = {
      version: '1.41.2',
      machineIdentifier: 'm-1',
      friendlyName: 'Home',
      mediaDeletionAllowed: true,
      ...(owned === undefined ? {} : { owned }),
    };
    await user.click(screen.getByRole('button', { name: 'Test' }));

    const term = screen.getByText('Owner token');
    expect(term.nextElementSibling).toHaveTextContent(label);
    const warning = screen.queryByText(/Only the server owner's token can delete media via Plex/);
    if (warns) {
      expect(warning).toBeInTheDocument();
      expect(screen.getByText("Not the server owner's token")).toBeInTheDocument();
    } else {
      expect(warning).not.toBeInTheDocument();
    }
  });

  it('rejects Plex Web URLs and missing tokens', async () => {
    const user = userEvent.setup();
    render(<MediaServerModal server={null} onClose={vi.fn()} />);
    await settle();
    await user.type(screen.getByLabelText('URL'), 'http://10.0.0.2:32400/web/index.html');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText(/without "\/web"/)).toBeInTheDocument();
    expect(screen.getByText(/token is required/i)).toBeInTheDocument();
    expect(mocks.calls.createServer).toBeUndefined();
  });

  it('warns when an edited server now reaches a different machine', async () => {
    const user = userEvent.setup();
    const server: MediaServer = {
      id: 3,
      name: 'Home',
      kind: 'plex',
      url: 'http://10.0.0.2:32400',
      token: MASKED_SECRET,
      machineIdentifier: 'old-machine',
      verifyTls: true,
      enabled: true,
      createdAt: '',
      updatedAt: '',
    };
    render(<MediaServerModal server={server} onClose={vi.fn()} />);
    mocks.results.testServer = { version: '1', machineIdentifier: 'new-machine', friendlyName: 'Other', mediaDeletionAllowed: true };
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(mocks.calls.testServer).toEqual([expect.objectContaining({ id: 3, token: MASKED_SECRET })]);
    expect(screen.getByText('Different Plex server')).toBeInTheDocument();
    expect(screen.queryByText('“Allow media deletion” is off')).not.toBeInTheDocument();

    // Saving a server that now reaches another machine needs an explicit confirmation.
    mocks.results.updateServer = { ...server };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    const dialog = screen.getByRole('dialog', { name: 'Switch to a different Plex server?' });
    expect(within(dialog).getByText('new-machine')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(mocks.calls.updateServer).toBeUndefined();

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await user.click(
      within(screen.getByRole('dialog', { name: 'Switch to a different Plex server?' })).getByRole('button', { name: 'Save' }),
    );
    expect(mocks.calls.updateServer).toEqual([{ server: expect.objectContaining({ id: 3 }), forceSave: false }]);
  });

  const SAVED: MediaServer = {
    id: 4,
    name: 'Home',
    kind: 'plex',
    url: 'http://10.0.0.2:32400',
    token: MASKED_SECRET,
    machineIdentifier: 'm-1',
    verifyTls: true,
    enabled: true,
    createdAt: '',
    updatedAt: '',
  };

  it('never sends the stored token to a new host', async () => {
    const user = userEvent.setup();
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    const url = screen.getByLabelText('URL');
    await user.clear(url);
    await user.type(url, 'http://10.0.0.99:32400');
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText(/Enter the token again/)).toBeInTheDocument();
    expect(mocks.calls.testServer).toBeUndefined();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateServer).toBeUndefined();
  });

  it('keeps the machine ID while URL and token are unchanged and drops it after a manual edit', async () => {
    const user = userEvent.setup();
    const { unmount } = render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    mocks.results.updateServer = { ...SAVED };
    await user.type(screen.getByLabelText('Name'), ' 2');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateServer).toEqual([
      { server: expect.objectContaining({ name: 'Home 2', machineIdentifier: 'm-1', token: MASKED_SECRET }), forceSave: false },
    ]);
    unmount();

    // Same host, different path (e.g. reverse proxy): the token may stay, but the machine ID is
    // re-learned by the server's connection test instead of trusting the old one.
    mocks.calls.updateServer = [];
    render(<MediaServerModal server={SAVED} onClose={vi.fn()} />);
    await settle();
    await user.type(screen.getByLabelText('URL'), '/plex');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    const [call] = mocks.calls.updateServer as { server: Record<string, unknown> }[];
    expect(call!.server).toMatchObject({ url: 'http://10.0.0.2:32400/plex', token: MASKED_SECRET });
    expect(call!.server).not.toHaveProperty('machineIdentifier');
  });
});

describe('NotificationModal', () => {
  it('adds a connection from the schema with default triggers', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<NotificationModal config={null} onClose={onClose} />);

    await user.click(screen.getByRole('button', { name: 'Add Discord' }));
    await settle();
    expect(screen.getByLabelText('Name')).toHaveValue('Discord');
    // All triggers except the noisy scan summary are pre-selected.
    expect(screen.getByRole('checkbox', { name: 'On Scan Completed' })).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'On File Deleted' })).toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('Webhook URL is required')).toBeInTheDocument();
    expect(mocks.calls.createNotification).toBeUndefined();

    await user.type(screen.getByLabelText(/webhook url/i), 'https://discord.com/api/webhooks/1/x');
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Test notification sent')).toBeInTheDocument();

    await user.click(screen.getByRole('checkbox', { name: 'On Health Issue' }));
    mocks.results.createNotification = { id: 1, name: 'Discord' };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.createNotification).toEqual([
      {
        name: 'Discord',
        kind: 'discord',
        settings: { webhookUrl: 'https://discord.com/api/webhooks/1/x', username: '' },
        triggers: ['onDuplicatesFound', 'onFileDeleted', 'onDeleteFailed'],
        enabled: true,
      },
    ]);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('keeps masked secrets when editing and shows test failures inline', async () => {
    const user = userEvent.setup();
    const config: NotificationConfig = {
      id: 9,
      name: 'Team Discord',
      kind: 'discord',
      settings: { webhookUrl: MASKED_SECRET, username: 'dupe' },
      triggers: ['onFileDeleted'],
      enabled: true,
    };
    render(<NotificationModal config={config} onClose={vi.fn()} />);
    expect(screen.getByLabelText(/webhook url/i)).toHaveValue(MASKED_SECRET);

    mocks.results.testNotification = new ApiError('Discord returned 401', { status: 400 });
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Test failed')).toBeInTheDocument();
    expect(screen.getByText('Discord returned 401')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateNotification).toEqual([
      expect.objectContaining({
        id: 9,
        settings: { webhookUrl: MASKED_SECRET, username: 'dupe' },
        triggers: ['onFileDeleted'],
      }),
    ]);
  });

  it('never sends a stored secret to a changed server address', async () => {
    const user = userEvent.setup();
    const config: NotificationConfig = {
      id: 11,
      name: 'Gotify',
      kind: 'gotify',
      settings: { serverUrl: 'http://gotify:80', appToken: MASKED_SECRET },
      triggers: ['onFileDeleted'],
      enabled: true,
    };
    render(<NotificationModal config={config} onClose={vi.fn()} />);
    await settle();
    const serverUrl = screen.getByLabelText(/server url/i);
    await user.clear(serverUrl);
    await user.type(serverUrl, 'https://evil.example');
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText(/Enter the App Token again/)).toBeInTheDocument();
    expect(screen.getByLabelText(/app token/i)).toHaveAttribute('aria-invalid', 'true');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.testNotification).toBeUndefined();
    expect(mocks.calls.updateNotification).toBeUndefined();

    // Back to the saved address: the stored token may be kept again (and the error clears).
    await user.clear(serverUrl);
    await user.type(serverUrl, 'http://gotify:80');
    expect(screen.queryByText(/Enter the App Token again/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateNotification).toEqual([
      expect.objectContaining({ id: 11, settings: { serverUrl: 'http://gotify:80', appToken: MASKED_SECRET } }),
    ]);
  });

  it('sends masked webhook header lines back untouched and asks to re-enter them after a URL change', async () => {
    const user = userEvent.setup();
    const headers = `Accept: application/json\nAuthorization: ${MASKED_SECRET}`;
    const config: NotificationConfig = {
      id: 12,
      name: 'Hook',
      kind: 'webhook',
      settings: { url: 'https://hooks.example/abc', username: 'me', password: '', headers },
      triggers: ['onFileDeleted'],
      enabled: true,
    };
    render(<NotificationModal config={config} onClose={vi.fn()} />);
    await settle();

    // Editing another field keeps the "Key: ********" lines exactly as received.
    const username = screen.getByLabelText('Username');
    await user.clear(username);
    await user.type(username, 'someone');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateNotification).toEqual([
      expect.objectContaining({ id: 12, settings: expect.objectContaining({ username: 'someone', headers }) }),
    ]);

    // A new URL: the stored header values would not be restored — re-enter them first.
    const url = screen.getByLabelText(/^URL/);
    await user.clear(url);
    await user.type(url, 'https://other.example/hook');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateNotification).toHaveLength(1);
    // The (advanced) Headers field is revealed with its error.
    expect(screen.getByText(/Re-enter the header values shown as \*{8}/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Headers/)).toHaveAttribute('aria-invalid', 'true');
  });

  it('shows a re-entry message under a masked secret the server refused after a URL change', async () => {
    const user = userEvent.setup();
    const config: NotificationConfig = {
      id: 13,
      name: 'Gotify',
      kind: 'gotify',
      settings: { serverUrl: 'http://gotify:80', appToken: MASKED_SECRET },
      triggers: ['onFileDeleted'],
      enabled: true,
    };
    render(<NotificationModal config={config} onClose={vi.fn()} />);
    await settle();
    // The server binds the secret to the exact stored URL (it changed elsewhere meanwhile) and refuses the mask.
    mocks.results.testNotification = new ApiError('App Token is masked; re-enter the value', {
      status: 400,
      validationErrors: [
        {
          propertyName: 'appToken',
          errorMessage:
            'App Token is masked; re-enter the value (a stored secret is only kept while the server address is unchanged)',
        },
      ],
    });
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByLabelText(/app token/i)).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getAllByText(/App Token is masked; re-enter the value/).length).toBeGreaterThanOrEqual(1);

    // A refusal whose message doesn't say so still tells the user to re-enter the stored secret.
    mocks.results.testNotification = new ApiError('Invalid', {
      status: 400,
      validationErrors: [{ propertyName: 'appToken', errorMessage: 'App Token is invalid' }],
    });
    await user.click(screen.getByRole('button', { name: 'Test' }));
    expect(screen.getByText('Re-enter the App Token: a stored value is only reused while the server address is unchanged.')).toBeInTheDocument();
  });

  it('maps server validation errors onto provider fields', async () => {
    const user = userEvent.setup();
    render(<NotificationModal config={null} onClose={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: 'Add ntfy' }));
    await settle();
    await user.type(screen.getByLabelText(/topic/i), 'dupes');
    mocks.results.createNotification = new ApiError('Topic is taken', {
      status: 400,
      validationErrors: [{ propertyName: 'Settings.Topic', errorMessage: 'Topic is taken' }],
    });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByLabelText(/topic/i)).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('Please correct the highlighted fields.')).toBeInTheDocument();
  });
});

describe('several Plex servers (docs/DECISIONS.md D11)', () => {
  const server = (id: number, name: string, storage: '' | 'separate' = ''): MediaServer => ({
    id,
    name,
    kind: 'plex',
    url: `http://plex${id}.lan:32400`,
    token: MASKED_SECRET,
    machineIdentifier: `m${id}`,
    verifyTls: true,
    enabled: true,
    storage,
    createdAt: '',
    updatedAt: '',
  });
  const radarr: ArrInstance = {
    id: 7,
    name: 'Radarr',
    kind: 'radarr',
    url: 'http://radarr:7878',
    apiKey: MASKED_SECRET,
    verifyTls: true,
    enabled: true,
    tags: [],
    serverIds: [],
    linksConfirmed: false,
    createdAt: '',
    updatedAt: '',
  };

  it('shows no Storage field with one server', () => {
    mocks.servers.list = [server(1, 'Plex')];
    render(<MediaServerModal server={server(1, 'Plex')} onClose={vi.fn()} />);
    expect(screen.queryByLabelText('Storage')).not.toBeInTheDocument();
  });

  it('shows and saves the Storage field with two servers', async () => {
    const user = userEvent.setup();
    mocks.servers.list = [server(1, 'Plex A'), server(2, 'Plex B')];
    render(<MediaServerModal server={server(2, 'Plex B')} onClose={vi.fn()} />);
    await settle();
    const select = screen.getByLabelText('Storage');
    await user.selectOptions(select, 'separate');
    mocks.results.updateServer = server(2, 'Plex B', 'separate');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateServer).toEqual([
      { server: expect.objectContaining({ id: 2, storage: 'separate' }), forceSave: false },
    ]);
  });

  it('keeps the Storage field for a separate server even when it is the only one', () => {
    mocks.servers.list = [server(2, 'Plex B', 'separate')];
    render(<MediaServerModal server={server(2, 'Plex B', 'separate')} onClose={vi.fn()} />);
    expect(screen.getByLabelText('Storage')).toHaveValue('separate');
  });

  it('shows no links field with one server and sends no links', async () => {
    const user = userEvent.setup();
    mocks.servers.list = [server(1, 'Plex')];
    render(<ArrInstanceModal instance={radarr} onClose={vi.fn()} />);
    expect(screen.queryByRole('group', { name: 'Plex servers it feeds' })).not.toBeInTheDocument();
    mocks.results.updateArr = { ...radarr };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    const sent = (mocks.calls.updateArr?.[0] as { instance: Record<string, unknown> }).instance;
    expect(sent).not.toHaveProperty('serverIds');
    expect(sent).not.toHaveProperty('linksConfirmed');
  });

  it('saves the chosen servers as confirmed links with two servers only once confirmed', async () => {
    const user = userEvent.setup();
    mocks.servers.list = [server(1, 'Plex A'), server(2, 'Plex B', 'separate')];
    render(<ArrInstanceModal instance={radarr} onClose={vi.fn()} />);
    await settle();
    const group = screen.getByRole('group', { name: 'Plex servers it feeds' });
    expect(within(group).getByText('Separate storage: never matched by path or name')).toBeInTheDocument();
    expect(screen.getByText(/Not confirmed yet/)).toBeInTheDocument();
    const confirm = screen.getByLabelText(/Radarr feeds exactly the servers ticked above/);
    expect(confirm).toBeDisabled();
    await user.click(within(group).getByLabelText('Plex A'));
    await user.click(confirm);
    mocks.results.updateArr = { ...radarr, serverIds: [1], linksConfirmed: true };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ id: 7, serverIds: [1], linksConfirmed: true }), forceSave: false },
    ]);
  });

  // Saving the modal for another change (a new API key) never confirms the list as a side effect.
  it('keeps unconfirmed links unconfirmed when saving without the confirmation', async () => {
    const user = userEvent.setup();
    mocks.servers.list = [server(1, 'Plex A'), server(2, 'Plex B')];
    render(<ArrInstanceModal instance={{ ...radarr, serverIds: [1] }} onClose={vi.fn()} />);
    await settle();
    expect(screen.getByLabelText(/Radarr feeds exactly the servers ticked above/)).not.toBeChecked();
    mocks.results.updateArr = { ...radarr, serverIds: [1] };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ id: 7, serverIds: [1], linksConfirmed: false }), forceSave: false },
    ]);
  });

  // "Feeds none of the servers" is never a confirmation (docs/DECISIONS.md D11).
  it('never confirms an empty selection', async () => {
    const user = userEvent.setup();
    mocks.servers.list = [server(1, 'Plex A'), server(2, 'Plex B')];
    render(<ArrInstanceModal instance={{ ...radarr, serverIds: [1], linksConfirmed: true }} onClose={vi.fn()} />);
    await settle();
    const confirm = screen.getByLabelText(/Radarr feeds exactly the servers ticked above/);
    expect(confirm).toBeChecked();
    await user.click(within(screen.getByRole('group', { name: 'Plex servers it feeds' })).getByLabelText('Plex A'));
    expect(confirm).not.toBeChecked();
    mocks.results.updateArr = { ...radarr };
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ id: 7, serverIds: [], linksConfirmed: false }), forceSave: false },
    ]);
  });
});
