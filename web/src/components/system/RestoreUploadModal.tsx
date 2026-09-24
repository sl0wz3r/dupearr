import { FileArchive, Upload } from 'lucide-react';
import { useId, useRef, useState, type ChangeEvent } from 'react';
import { ApiError, errorMessage } from '@/api/client';
import type { RestoreStagedResponse } from '@/api/types';
import { Alert, Button, Modal } from '@/components/ui';
import { formatBytes, formatPercent } from '@/lib/format';
import {
  MAX_BACKUP_UPLOAD_BYTES,
  isAbortError,
  useRestoreBackupUploadWithProgress,
  validateBackupFile,
} from './backupUpload';
import { RestoreSafetyNotes } from './RestoreSafetyNotes';

export interface RestoreUploadModalProps {
  open: boolean;
  onClose: () => void;
  /** Called after the server staged the backup for review (nothing is replaced yet). */
  onRestored: (response: RestoreStagedResponse | undefined, fileName: string) => void;
}

/**
 * System → Backup → "Restore from file": pick a Dupearr backup zip (≤ 512 MiB), confirm the
 * warning, upload with progress. The modal cannot be dismissed while the upload is in flight
 * (use Cancel, which is only offered until every byte has been sent).
 */
export function RestoreUploadModal({ open, onClose, onRestored }: RestoreUploadModalProps) {
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const upload = useRestoreBackupUploadWithProgress();

  const busy = upload.isPending;
  const progress = upload.progress;
  const sent = !!progress && progress.total > 0 && progress.loaded >= progress.total;
  const ratio = progress && progress.total > 0 ? Math.min(1, progress.loaded / progress.total) : 0;

  const resetAll = () => {
    setFile(null);
    setFileError(null);
    upload.reset();
    if (inputRef.current) inputRef.current.value = '';
  };

  const close = () => {
    if (busy) return;
    resetAll();
    onClose();
  };

  const onFileChange = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0] ?? null;
    upload.reset();
    setFile(f);
    setFileError(f ? validateBackupFile(f) : null);
  };

  const start = () => {
    const error = validateBackupFile(file);
    setFileError(error);
    if (error || !file) return;
    const name = file.name;
    upload.mutate(file, {
      onSuccess: (res) => {
        resetAll();
        onRestored(res, name);
      },
    });
  };

  const cancelled = upload.isError && isAbortError(upload.error);
  // A connection lost after every byte was sent may mean the restore was accepted and Dupearr is
  // already restarting: say so instead of claiming the restore failed.
  const connectionLost = upload.isError && upload.error instanceof ApiError && upload.error.status === 0 && sent;
  const uploadError = upload.isError && !cancelled && !connectionLost ? errorMessage(upload.error) : null;

  return (
    <Modal
      open={open}
      onClose={close}
      title="Restore Backup"
      size="md"
      closeOnBackdrop={!busy}
      hideCloseButton={busy}
      footer={
        <>
          {busy ? (
            <Button onClick={upload.cancel} disabled={sent}>
              Cancel Upload
            </Button>
          ) : (
            <Button onClick={close}>Cancel</Button>
          )}
          <Button
            variant="warning"
            icon={Upload}
            loading={busy}
            disabled={!file || !!fileError}
            onClick={start}
          >
            Upload and Review
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Alert kind="warning" title="This replaces your current configuration and database">
          Restoring a backup overwrites every setting, connection, profile and the duplicate history with the
          contents of the backup. After the upload you review what changes before anything is replaced; Dupearr
          then restarts.
        </Alert>
        <RestoreSafetyNotes />

        <div>
          <label htmlFor={inputId} className="mb-1.5 block text-sm font-semibold text-fg-strong">
            Backup file
          </label>
          <input
            ref={inputRef}
            id={inputId}
            type="file"
            accept=".zip,application/zip,application/x-zip-compressed"
            disabled={busy}
            onChange={onFileChange}
            aria-invalid={fileError ? true : undefined}
            aria-describedby={`${inputId}-help`}
            className="block w-full text-sm text-fg file:mr-3 file:cursor-pointer file:rounded file:border file:border-border-strong file:bg-card file:px-3 file:py-1.5 file:text-sm file:text-fg hover:file:bg-card-hover disabled:opacity-60"
          />
          <p id={`${inputId}-help`} className="mt-1.5 mb-0 text-xs text-muted">
            A <span className="font-mono">.zip</span> created by Dupearr (System → Backup), up to{' '}
            {formatBytes(MAX_BACKUP_UPLOAD_BYTES, 0)}.
          </p>
        </div>

        {file && (
          <div className="flex items-center gap-2 rounded border border-border bg-card-alt px-3 py-2 text-sm">
            <FileArchive aria-hidden width={16} height={16} className="shrink-0 text-muted" />
            <span className="min-w-0 flex-1 truncate font-mono text-xs" title={file.name}>
              {file.name}
            </span>
            <span className="shrink-0 text-xs text-muted">{formatBytes(file.size)}</span>
          </div>
        )}

        {fileError && (
          <Alert kind="error" title="Invalid backup file">
            {fileError}
          </Alert>
        )}

        {busy && progress && (
          <div>
            <div className="mb-1 flex justify-between text-xs text-muted">
              <span>{sent ? 'Validating backup…' : 'Uploading…'}</span>
              <span>
                {formatBytes(progress.loaded)} / {formatBytes(progress.total)} ({formatPercent(ratio)})
              </span>
            </div>
            <div
              role="progressbar"
              aria-label="Upload progress"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(ratio * 100)}
              className="h-2 w-full overflow-hidden rounded-full bg-card-hover"
            >
              <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${ratio * 100}%` }} />
            </div>
          </div>
        )}

        {cancelled && <Alert kind="info">Upload cancelled. Nothing was restored.</Alert>}
        {connectionLost && (
          <Alert kind="warning" title="Lost connection during the restore">
            The backup was uploaded, but the connection to Dupearr was lost before it answered. Nothing is restored
            until you confirm the review of its changes: check that Dupearr is reachable and upload it again.
          </Alert>
        )}
        {uploadError && (
          <Alert kind="error" title="Restore failed">
            {uploadError}
          </Alert>
        )}
      </div>
    </Modal>
  );
}
