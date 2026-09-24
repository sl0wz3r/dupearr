import { useQueryClient } from '@tanstack/react-query';
import { DatabaseBackup, RefreshCw, Upload } from 'lucide-react';
import { useCallback, useState } from 'react';
import { ApiError, errorMessage } from '@/api/client';
import {
  useBackups,
  useConfirmRestore,
  useCreateBackup,
  useDeleteBackup,
  useDiscardRestore,
  useDownloadBackup,
  useHostConfig,
  useRestart,
  useRestoreBackup,
  useSystemStatus,
} from '@/api/hooks';
import type { Backup, RestoreResponse, RestoreStagedResponse, RestoreSummary } from '@/api/types';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
  ToolbarSeparator,
} from '@/components/page';
import { PasswordPrompt, passwordProtected } from '@/components/settings/connections/ApiKeyField';
import { BackupTable } from '@/components/system/BackupTable';
import { RestartOverlay, checkRestarted } from '@/components/system/RestartOverlay';
import { RestoreReviewDialog } from '@/components/system/RestoreReviewDialog';
import { RestoreSafetyNotes } from '@/components/system/RestoreSafetyNotes';
import { RestoreUploadModal } from '@/components/system/RestoreUploadModal';
import { saveBlob } from '@/components/system/systemFormat';
import { Alert, ConfirmDialog, LoadErrorAlert, useToast } from '@/components/ui';

/**
 * System → Backup at `/system/backup`: backups of config.xml + the database. Download, restore
 * (the backup is staged and its changes reviewed first; confirming restarts Dupearr behind a
 * full-page "Restarting…" overlay that polls /ping and reloads), delete, "Backup Now" and
 * "Restore from file" (zip upload with progress).
 */
export default function BackupPage() {
  const backups = useBackups();
  const create = useCreateBackup();
  const remove = useDeleteBackup();
  const restore = useRestoreBackup();
  const confirm = useConfirmRestore();
  const discard = useDiscardRestore();
  const restartServer = useRestart();
  const status = useSystemStatus();
  const host = useHostConfig();
  const download = useDownloadBackup();
  const qc = useQueryClient();
  const toast = useToast();
  /** The backup whose download waits for the password (null = none). */
  const [toDownload, setToDownload] = useState<Backup | null>(null);
  // A backup holds every credential: with a Forms account, downloading one needs the password.
  const needPassword = host.data ? passwordProtected(host.data) : true;

  const startDownload = (backup: Backup, currentPassword?: string) => {
    download.mutate(
      { id: backup.id, currentPassword },
      {
        onSuccess: (blob) => {
          setToDownload(null);
          if (blob) saveBlob(blob, backup.name);
        },
        onError: (e) => {
          // A wrong password is shown in the prompt; anything else closes it.
          if (!(e instanceof ApiError && e.errorsFor('currentPassword').length > 0)) {
            setToDownload(null);
            toast.error('Unable to download backup', errorMessage(e));
          }
        },
      },
    );
  };
  const requestDownload = (backup: Backup) => {
    download.reset();
    if (needPassword) setToDownload(backup);
    else startDownload(backup);
  };

  const [toRestore, setToRestore] = useState<Backup | null>(null);
  const [toDelete, setToDelete] = useState<Backup | null>(null);
  const [uploadOpen, setUploadOpen] = useState(false);
  /** The staged restore under review (null = none). */
  const [review, setReview] = useState<{ summary: RestoreSummary; name?: string } | null>(null);
  const [reviewError, setReviewError] = useState<string | null>(null);
  /** Start time of the process that accepted the restore (null = not restarting). */
  const [restartFrom, setRestartFrom] = useState<{ startTime: string | undefined } | null>(null);
  const restarting = restartFrom !== null;

  /** A staged restore: show its changes for review (nothing is applied until confirmed). */
  const afterStaged = (res: RestoreStagedResponse | undefined, name?: string) => {
    setReviewError(null);
    setReview({
      summary: res?.summary ?? { securitySettingsRestored: false, changes: [], cancelledRemovals: 0, interruptedRemovals: 0, reopenedGroups: 0 },
      name,
    });
  };

  const confirmReviewed = () => {
    setReviewError(null);
    confirm.mutate(undefined, {
      onSuccess: (res) => {
        setReview(null);
        afterRestore(res);
      },
      onError: (e) => {
        if (e instanceof ApiError && e.status === 0) {
          setReview(null);
          restoreFailed(e);
          return;
        }
        if (e instanceof ApiError && (e.status === 409 || e.status === 404)) {
          // Stale (security settings changed meanwhile) or no longer staged: it must be staged again.
          setReview(null);
          toast.error('The restore was not applied', errorMessage(e));
          return;
        }
        setReviewError(errorMessage(e));
      },
    });
  };

  const discardReviewed = () => {
    discard.mutate(undefined, {
      onSettled: () => setReview(null),
      onSuccess: () => toast.info('Restore discarded', 'Nothing was changed.'),
      onError: (e) => toast.error('Unable to discard the staged restore', `${errorMessage(e)} — it is discarded at the next restart anyway.`),
    });
  };

  /** After a confirmed restore the server restarts itself; show the overlay unless it says otherwise. */
  const afterRestore = (res: RestoreResponse | undefined) => {
    if (res && res.restartRequired === false) {
      toast.success('Backup restored');
      // Everything cached may describe the previous configuration/database.
      void qc.invalidateQueries();
      return;
    }
    setRestartFrom({ startTime: status.data?.startTime });
  };

  /** Staging failed: nothing was restored (an unconfirmed staged restore is never applied). */
  const stageFailed = (e: unknown) => {
    toast.error(
      'Unable to restore backup',
      e instanceof ApiError && e.status === 0
        ? 'The connection to Dupearr was lost before the backup was staged. Nothing was restored; try again.'
        : errorMessage(e),
    );
  };

  /** A confirmation that lost the connection may still have been applied (the server restarts). */
  const restoreFailed = (e: unknown) => {
    if (e instanceof ApiError && e.status === 0) {
      toast.warning(
        'Lost connection during the restore',
        'Dupearr may be restarting with the restored backup. Wait a moment, then reload this page to check.',
      );
      return;
    }
    toast.error('Unable to restore backup', errorMessage(e));
  };

  const previousStartTime = restartFrom?.startTime;
  const verifyRestart = useCallback(
    (signal: AbortSignal) => checkRestarted(previousStartTime, signal),
    [previousStartTime],
  );

  const requestRestart = () => {
    restartServer.mutate(undefined, {
      onError: (e) => toast.error('Unable to restart Dupearr', errorMessage(e)),
    });
  };

  const createBackup = () => {
    create.mutate(undefined, {
      onSuccess: (b) => toast.success('Backup created', b?.name),
      onError: (e) => toast.error('Unable to create backup', errorMessage(e)),
    });
  };

  const confirmRestore = () => {
    const backup = toRestore;
    if (!backup) return;
    restore.mutate(backup.id, {
      onSuccess: (res) => {
        setToRestore(null);
        afterStaged(res, backup.name);
      },
      onError: (e) => {
        setToRestore(null);
        stageFailed(e);
      },
    });
  };

  const confirmDelete = () => {
    const backup = toDelete;
    if (!backup) return;
    remove.mutate(backup.id, {
      onSuccess: () => {
        setToDelete(null);
        toast.success('Backup deleted', backup.name);
      },
      onError: (e) => {
        setToDelete(null);
        toast.error('Unable to delete backup', errorMessage(e));
      },
    });
  };

  const busy = restore.isPending || remove.isPending || restarting || review !== null;

  return (
    <PageContent title="Backup">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={DatabaseBackup}
            label="Backup Now"
            loading={create.isPending}
            disabled={busy}
            onClick={createBackup}
          />
          <ToolbarButton
            icon={Upload}
            label="Restore Backup"
            title="Restore from a backup file"
            disabled={busy}
            onClick={() => setUploadOpen(true)}
          />
          <ToolbarSeparator />
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={backups.isFetching}
            onClick={() => void backups.refetch()}
          />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Backup" subtitle="Backups contain config.xml and the Dupearr database." />
        {backups.isError && (
          <LoadErrorAlert
            title="Unable to load backups"
            message={errorMessage(backups.error)}
            onRetry={() => void backups.refetch()}
            retrying={backups.isFetching}
            className="mb-4"
          />
        )}
        {!(backups.isError && !backups.data) && (
          <BackupTable
            backups={backups.data ?? []}
            loading={backups.isLoading}
            disabled={busy}
            onDownload={requestDownload}
            onRestore={setToRestore}
            onDelete={setToDelete}
          />
        )}
      </PageBody>

      <PasswordPrompt
        open={toDownload !== null}
        title="Download Backup"
        message={
          <p className="m-0">
            A backup contains every credential Dupearr holds (API key, Plex token, *arr API keys, notification secrets).
            Enter your password to download{' '}
            <strong className="font-mono text-xs break-all text-fg-strong">{toDownload?.name}</strong>, and keep the file
            private.
          </p>
        }
        confirmLabel="Download"
        needPassword
        loading={download.isPending}
        error={download.error}
        onConfirm={(password) => toDownload && startDownload(toDownload, password)}
        onCancel={() => {
          setToDownload(null);
          download.reset();
        }}
      />

      <ConfirmDialog
        open={toRestore !== null}
        title="Restore Backup"
        kind="warning"
        confirmLabel="Continue"
        loading={restore.isPending}
        onCancel={() => setToRestore(null)}
        onConfirm={confirmRestore}
        message={
          <div className="space-y-3">
            <p className="m-0">
              Restore <strong className="font-mono text-xs break-all text-fg-strong">{toRestore?.name}</strong>?
            </p>
            <Alert kind="warning">
              The current configuration and database will be replaced by the backup. You review what changes before
              anything is replaced; Dupearr then restarts.
            </Alert>
            <RestoreSafetyNotes />
          </div>
        }
      />

      <ConfirmDialog
        open={toDelete !== null}
        title="Delete Backup"
        confirmLabel="Delete"
        loading={remove.isPending}
        onCancel={() => setToDelete(null)}
        onConfirm={confirmDelete}
        message={
          <>
            Delete <strong className="font-mono text-xs break-all text-fg-strong">{toDelete?.name}</strong>? This
            cannot be undone.
          </>
        }
      />

      <RestoreUploadModal
        open={uploadOpen}
        onClose={() => setUploadOpen(false)}
        onRestored={(res, name) => {
          setUploadOpen(false);
          afterStaged(res, name);
        }}
      />

      <RestoreReviewDialog
        summary={review?.summary ?? null}
        name={review?.name}
        confirming={confirm.isPending}
        discarding={discard.isPending}
        error={reviewError}
        onConfirm={confirmReviewed}
        onDiscard={discardReviewed}
      />

      <RestartOverlay
        open={restarting}
        verify={verifyRestart}
        onRestartRequest={restartServer.isPending ? undefined : requestRestart}
      />
    </PageContent>
  );
}
