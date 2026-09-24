import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Backup, Command } from '@/api/types';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import { FakeXhr } from '@/components/system/fakeXhr';
import BackupPage from './BackupPage';

const BACKUP: Backup = {
  id: 7,
  name: 'dupearr_backup_v0.1.0_2026.09.20_03.00.00.zip',
  path: '/backup/scheduled/dupearr_backup_v0.1.0_2026.09.20_03.00.00.zip',
  type: 'scheduled',
  size: 5_242_880,
  time: '2026-09-20T03:00:01Z',
};

/** What POST /system/backup/restore/{id} and /upload answer: the backup is staged for review. */
const STAGED = {
  staged: true,
  restartRequired: false,
  summary: {
    securitySettingsRestored: false,
    changes: [
      { setting: 'dryRun', current: 'false', backup: 'false', applied: false, message: 'Dry run is on after a restore' },
      { setting: 'mediaServers', current: '', backup: 'Evil PMS → http://10.9.9.9:32400', applied: true },
    ],
    cancelledRemovals: 2,
    interruptedRemovals: 0,
    reopenedGroups: 1,
  },
};

function setup({
  restoreStatus = 200,
  restoreBody = STAGED,
  confirmStatus = 200,
  confirmBody = { restartRequired: true },
  commands = [],
}: {
  restoreStatus?: number | 'network';
  restoreBody?: unknown;
  confirmStatus?: number | 'network';
  confirmBody?: unknown;
  commands?: Command[];
} = {}) {
  let backups = [BACKUP];
  const mock = mockFetch(({ method, path, body }) => {
    if (method === 'GET' && path === '/api/v1/system/backup') return jsonResponse(backups);
    if (method === 'GET' && path === '/api/v1/config/host') {
      return jsonResponse({ authenticationMethod: 'Forms', username: 'admin', password: '********' });
    }
    if (method === 'POST' && path === '/api/v1/system/backup/download/7') {
      const pw = (body as { currentPassword?: string } | undefined)?.currentPassword;
      if (pw !== 'secret-pw') {
        return jsonResponse([{ propertyName: 'currentPassword', errorMessage: 'The current password is incorrect' }], 400);
      }
      return new Response('PK\x03\x04', { status: 200, headers: { 'Content-Type': 'application/zip' } });
    }
    if (method === 'GET' && path === '/api/v1/system/status') return jsonResponse({ startTime: '2026-09-22T10:00:00Z' });
    if (method === 'GET' && path === '/api/v1/command') return jsonResponse(commands);
    if (method === 'POST' && path === '/api/v1/system/backup/restore/7') {
      if (restoreStatus === 'network') throw new TypeError('Failed to fetch');
      return restoreStatus === 200
        ? jsonResponse(restoreBody)
        : jsonResponse({ message: 'Backup is corrupt' }, restoreStatus);
    }
    if (method === 'POST' && path === '/api/v1/system/backup/restore/confirm') {
      if (confirmStatus === 'network') throw new TypeError('Failed to fetch');
      return confirmStatus === 200
        ? jsonResponse(confirmBody)
        : jsonResponse({ message: 'The security settings changed after the backup was staged for restore; stage the restore again' }, confirmStatus);
    }
    if (method === 'DELETE' && path === '/api/v1/system/backup/restore') return jsonResponse({});
    if (method === 'DELETE' && path === '/api/v1/system/backup/7') {
      backups = [];
      return jsonResponse({});
    }
    if (method === 'POST' && path === '/api/v1/system/backup') {
      const created: Backup = { ...BACKUP, id: 8, type: 'manual', name: 'dupearr_backup_manual.zip' };
      backups = [created, ...backups];
      return jsonResponse(created);
    }
    if (method === 'GET' && path === '/ping') return jsonResponse({ status: 'OK' });
    if (method === 'POST' && path === '/api/v1/system/restart') return jsonResponse({}, 202);
    return undefined;
  });
  renderWithProviders(<BackupPage />);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  FakeXhr.last = null;
});

/** Stages the stored backup (Restore → Continue) and returns the review dialog. */
async function stageStored(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: `Restore ${BACKUP.name}` }));
  const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
  await user.click(within(dialog).getByRole('button', { name: 'Continue' }));
  return screen.findByRole('dialog', { name: 'Review Restore' });
}

/** Opens the upload modal and selects a valid backup zip. */
async function chooseUpload(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Restore Backup' }));
  const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
  const file = new File(['PK'], 'dupearr_backup.zip', { type: 'application/zip' });
  await user.upload(within(dialog).getByLabelText('Backup file'), file);
  return { dialog, file };
}

describe('<BackupPage>', () => {
  it('lists backups', async () => {
    setup();
    expect(await screen.findByRole('button', { name: `Download ${BACKUP.name}` })).toBeInTheDocument();
    expect(screen.getByText('Scheduled')).toBeInTheDocument();
    expect(screen.getByText('5.0 MiB')).toBeInTheDocument();
  });

  // GAP-09: a backup holds every credential, so a session alone never downloads one: the password
  // is asked for and sent in the body (never a credential in a URL), and there is no plain link.
  it('downloads a backup only with the current password', async () => {
    const user = userEvent.setup();
    const createObjectURL = vi.fn(() => 'blob:backup');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const { requests } = setup();
    expect(screen.queryByRole('link', { name: BACKUP.name })).not.toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: `Download ${BACKUP.name}` }));
    const prompt = await screen.findByRole('dialog', { name: 'Download Backup' });
    await user.type(within(prompt).getByLabelText('Current Password'), 'wrong');
    await user.click(within(prompt).getByRole('button', { name: 'Download' }));
    expect(await within(prompt).findByText('The current password is incorrect')).toBeInTheDocument();
    expect(createObjectURL).not.toHaveBeenCalled();

    await user.type(within(prompt).getByLabelText('Current Password'), 'secret-pw');
    await user.click(within(prompt).getByRole('button', { name: 'Download' }));
    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(click).toHaveBeenCalledTimes(1);
    click.mockRestore();
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Download Backup' })).not.toBeInTheDocument());
    const downloads = requests.filter((r) => r.path.includes('/download/'));
    expect(downloads.map((r) => [r.method, r.path])).toEqual([
      ['POST', '/api/v1/system/backup/download/7'],
      ['POST', '/api/v1/system/backup/download/7'],
    ]);
    expect(requests.some((r) => r.path.startsWith('/backup/') || r.url.search.includes('apikey'))).toBe(false);
  });

  // r2-data-files#6: the backup is staged and its changes reviewed before anything is replaced.
  it('restores only after confirming the warning and reviewing the changes, then shows the restarting overlay', async () => {
    const user = userEvent.setup();
    const { requests } = setup();

    await user.click(await screen.findByRole('button', { name: `Restore ${BACKUP.name}` }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
    expect(within(dialog).getByText(/You review what changes before anything is replaced/)).toBeInTheDocument();
    expect(requests.some((r) => r.path.includes('/restore/'))).toBe(false);
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Continue' }));
    const review = await screen.findByRole('dialog', { name: 'Review Restore' });
    expect(within(review).getByText('Media Servers')).toBeInTheDocument();
    expect(within(review).getByText('Evil PMS → http://10.9.9.9:32400')).toBeInTheDocument();
    expect(within(review).getByText('Dry run stays on')).toBeInTheDocument();
    expect(within(review).getByText(/2 queued and 0 running removal/)).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    expect(requests.filter((r) => r.method === 'POST').map((r) => r.path)).toEqual(['/api/v1/system/backup/restore/7']);

    await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));
    const overlay = await screen.findByRole('alertdialog', { name: 'Restarting…' });
    expect(overlay).toHaveTextContent('reloads automatically');
    expect(requests.filter((r) => r.method === 'POST').map((r) => r.path)).toEqual([
      '/api/v1/system/backup/restore/7',
      '/api/v1/system/backup/restore/confirm',
    ]);
    expect(screen.queryByRole('dialog', { name: 'Review Restore' })).not.toBeInTheDocument();
  });

  it('discards a staged restore without restarting', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    const review = await stageStored(user);
    await user.click(within(review).getByRole('button', { name: 'Discard' }));
    await waitFor(() =>
      expect(requests.some((r) => r.method === 'DELETE' && r.path === '/api/v1/system/backup/restore')).toBe(true),
    );
    expect(requests.some((r) => r.path === '/api/v1/system/backup/restore/confirm')).toBe(false);
    expect(await screen.findByText('Restore discarded')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('reports a stale staged restore (security settings changed meanwhile) without restarting', async () => {
    const user = userEvent.setup();
    setup({ confirmStatus: 409 });
    const review = await stageStored(user);
    await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));
    expect(await screen.findByText('The restore was not applied')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('keeps the overlay up while the old process still answers, and can request a restart', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const { requests } = setup();
      const review = await stageStored(user);
      await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));
      await screen.findByRole('alertdialog', { name: 'Restarting…' });

      // /ping keeps answering and /system/status reports the same start time: no restart happened.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(await screen.findByText(/has not restarted yet/)).toBeInTheDocument();
      expect(requests.some((r) => r.path === '/ping')).toBe(true);
      expect(requests.filter((r) => r.path === '/api/v1/system/status').length).toBeGreaterThan(1);

      await user.click(screen.getByRole('button', { name: 'Restart Now' }));
      await waitFor(() =>
        expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/system/restart')).toBe(true),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it('says that queued removals are cancelled, and warns when removals are running right now', async () => {
    const user = userEvent.setup();
    setup({
      commands: [{ id: 5, name: 'ProcessQueue', body: {}, status: 'started', trigger: 'scheduled', queued: '2026-09-22T00:00:00Z' }],
    });
    await user.click(await screen.findByRole('button', { name: `Restore ${BACKUP.name}` }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
    expect(within(dialog).getByText(/queued when the backup was made are cancelled/)).toBeInTheDocument();
    expect(await within(dialog).findByText('Removals are running right now')).toBeInTheDocument();
  });

  it('does not claim failure when the connection drops during a confirmed restore', async () => {
    const user = userEvent.setup();
    setup({ confirmStatus: 'network' });
    const review = await stageStored(user);
    await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));

    expect(await screen.findByText('Lost connection during the restore')).toBeInTheDocument();
    expect(screen.queryByText('Unable to restore backup')).not.toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('shows no overlay when the server says no restart is required', async () => {
    const user = userEvent.setup();
    setup({ confirmBody: { restartRequired: false } });
    const review = await stageStored(user);
    await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));

    expect(await screen.findByText('Backup restored')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('uploads a backup file with progress, then shows the restarting overlay', async () => {
    vi.stubGlobal('XMLHttpRequest', FakeXhr);
    const user = userEvent.setup();
    setup();
    const { dialog, file } = await chooseUpload(user);
    await user.click(within(dialog).getByRole('button', { name: 'Upload and Review' }));

    const xhr = FakeXhr.last!;
    expect(xhr.method).toBe('POST');
    expect(xhr.url).toBe('/api/v1/system/backup/restore/upload');
    expect(xhr.headers['X-Api-Key']).toBeUndefined();
    expect((xhr.body as FormData).get('file')).toBeInstanceOf(File);
    expect(((xhr.body as FormData).get('file') as File).name).toBe(file.name);
    // The modal cannot be dismissed while uploading.
    expect(within(dialog).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();

    act(() => xhr.sendAll(file.size));
    expect(await within(dialog).findByText('Validating backup…')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Cancel Upload' })).toBeDisabled();

    act(() => xhr.respond(200, JSON.stringify(STAGED)));
    const review = await screen.findByRole('dialog', { name: 'Review Restore' });
    expect(within(review).getByText(file.name)).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Restore Backup' })).not.toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
    await user.click(within(review).getByRole('button', { name: 'Restore and Restart' }));
    expect(await screen.findByRole('alertdialog', { name: 'Restarting…' })).toBeInTheDocument();
  });

  it('reports a rejected upload and a connection lost after the upload differently', async () => {
    vi.stubGlobal('XMLHttpRequest', FakeXhr);
    const user = userEvent.setup();
    setup();
    const { dialog, file } = await chooseUpload(user);

    await user.click(within(dialog).getByRole('button', { name: 'Upload and Review' }));
    act(() => FakeXhr.last!.respond(400, '{"message":"Backup is missing dupearr.db"}'));
    expect(await within(dialog).findByText('Backup is missing dupearr.db')).toBeInTheDocument();
    expect(within(dialog).getByText('Restore failed')).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Upload and Review' }));
    act(() => {
      FakeXhr.last!.sendAll(file.size);
      FakeXhr.last!.onerror?.();
    });
    expect(await within(dialog).findByText('Lost connection during the restore')).toBeInTheDocument();
    expect(within(dialog).queryByText('Restore failed')).not.toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('does not restore when the confirmation is cancelled', async () => {
    const user = userEvent.setup();
    const { requests } = setup();

    await user.click(await screen.findByRole('button', { name: `Restore ${BACKUP.name}` }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(requests.some((r) => r.method === 'POST')).toBe(false);
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('shows an error and no overlay when the restore is rejected', async () => {
    const user = userEvent.setup();
    setup({ restoreStatus: 400 });

    await user.click(await screen.findByRole('button', { name: `Restore ${BACKUP.name}` }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }));

    expect(await screen.findByText('Unable to restore backup')).toBeInTheDocument();
    expect(screen.getByText('Backup is corrupt')).toBeInTheDocument();
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
  });

  it('deletes a backup after confirmation', async () => {
    const user = userEvent.setup();
    const { requests } = setup();

    await user.click(await screen.findByRole('button', { name: `Delete ${BACKUP.name}` }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Backup' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() =>
      expect(requests.some((r) => r.method === 'DELETE' && r.path === '/api/v1/system/backup/7')).toBe(true),
    );
    expect(await screen.findByText('No backups')).toBeInTheDocument();
  });

  it('creates a backup from the toolbar', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByRole('button', { name: `Download ${BACKUP.name}` });

    await user.click(screen.getByRole('button', { name: 'Backup Now' }));

    expect(await screen.findByText('Backup created')).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Download dupearr_backup_manual.zip' })).toBeInTheDocument();
  });

  it('rejects oversized and non-zip uploads before sending anything', async () => {
    const user = userEvent.setup({ applyAccept: false });
    const { requests } = setup();
    await user.click(await screen.findByRole('button', { name: 'Restore Backup' }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore Backup' });
    const input = within(dialog).getByLabelText('Backup file');

    const big = new File(['x'], 'dupearr_backup.zip', { type: 'application/zip' });
    Object.defineProperty(big, 'size', { value: 600 * 1024 * 1024 });
    await user.upload(input, big);
    expect(within(dialog).getByText(/the maximum is 512 MiB/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Upload and Review' })).toBeDisabled();

    await user.upload(input, new File(['x'], 'config.xml', { type: 'text/xml' }));
    expect(within(dialog).getByText(/must be .zip files/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Upload and Review' })).toBeDisabled();

    expect(requests.some((r) => r.method === 'POST')).toBe(false);
  });
});
