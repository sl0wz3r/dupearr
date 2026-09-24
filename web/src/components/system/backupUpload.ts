/**
 * Backup restore upload with byte progress. `fetch` cannot report upload progress, so this uses
 * XMLHttpRequest with the same conventions as `api.upload` (API root, X-Api-Key, error parsing).
 * The server contract is unchanged: POST /api/v1/system/backup/restore/upload, multipart `file`.
 */
import { useMutation } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, apiUrl, buildApiError } from '@/api/client';
import type { RestoreStagedResponse } from '@/api/types';
import { formatBytes } from '@/lib/format';

/** Largest accepted backup upload (512 MiB). */
export const MAX_BACKUP_UPLOAD_BYTES = 512 * 1024 * 1024;

/** Validates a backup file before upload; returns an error message or null when acceptable. */
export function validateBackupFile(file: Pick<File, 'name' | 'size'> | null | undefined): string | null {
  if (!file) return 'Choose a backup file to restore.';
  if (!/\.zip$/i.test(file.name)) return 'Backups must be .zip files created by Dupearr.';
  if (!Number.isFinite(file.size) || file.size <= 0) return 'The selected file is empty.';
  if (file.size > MAX_BACKUP_UPLOAD_BYTES) {
    return `The selected file is ${formatBytes(file.size)}; the maximum is ${formatBytes(MAX_BACKUP_UPLOAD_BYTES, 0)}.`;
  }
  return null;
}

export interface UploadProgress {
  loaded: number;
  total: number;
}

export interface UploadOptions {
  onProgress?: (progress: UploadProgress) => void;
  signal?: AbortSignal;
}

function parseBody(text: string, contentType: string): unknown {
  if (!text) return undefined;
  if (contentType.includes('json') || /^[[{]/.test(text.trim())) {
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
  return text;
}

/**
 * POSTs multipart form data to an API path (relative to the API root) reporting upload progress.
 * Rejects with ApiError on HTTP/network errors and with an AbortError when `signal` aborts.
 */
export function uploadWithProgress<T>(path: string, form: FormData, opts: UploadOptions = {}): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    if (opts.signal?.aborted) {
      reject(new DOMException('Upload cancelled', 'AbortError'));
      return;
    }
    const xhr = new XMLHttpRequest();
    xhr.open('POST', apiUrl(path));
    // Same-origin semantics like the fetch-based client: the session cookie authenticates it, and
    // the browser's Origin header satisfies the server's cross-site check.
    xhr.withCredentials = false;
    xhr.setRequestHeader('Accept', 'application/json, */*');

    const onAbort = () => xhr.abort();
    opts.signal?.addEventListener('abort', onAbort, { once: true });
    const cleanup = () => opts.signal?.removeEventListener('abort', onAbort);

    let total = 0;
    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;
      total = e.total;
      opts.onProgress?.({ loaded: e.loaded, total: e.total });
    };
    // Every byte has been sent: make sure the final progress (100%) is reported.
    xhr.upload.onload = () => {
      if (total > 0) opts.onProgress?.({ loaded: total, total });
    };
    xhr.onload = () => {
      cleanup();
      const body = parseBody(xhr.responseText ?? '', xhr.getResponseHeader('content-type') ?? '');
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else reject(buildApiError(xhr.status, xhr.statusText, body));
    };
    xhr.onerror = () => {
      cleanup();
      reject(new ApiError('Unable to reach Dupearr. Check that the server is running.', { status: 0 }));
    };
    xhr.ontimeout = xhr.onerror;
    xhr.onabort = () => {
      cleanup();
      reject(new DOMException('Upload cancelled', 'AbortError'));
    };
    xhr.send(form);
  });
}

/** True for the rejection of a cancelled upload. */
export function isAbortError(e: unknown): boolean {
  return e instanceof DOMException && e.name === 'AbortError';
}

/**
 * Restores an uploaded backup zip with progress and cancellation (cancelling is only offered
 * until the bytes are sent). The server restarts after a successful restore.
 */
export function useRestoreBackupUploadWithProgress() {
  const [progress, setProgress] = useState<UploadProgress | null>(null);
  const controller = useRef<AbortController | null>(null);

  // Abort an in-flight upload when the component using it unmounts.
  useEffect(() => () => controller.current?.abort(), []);

  const mutation = useMutation({
    mutationFn: (file: File) => {
      const error = validateBackupFile(file);
      if (error) return Promise.reject(new ApiError(error, { status: 0 }));
      controller.current?.abort();
      const c = new AbortController();
      controller.current = c;
      setProgress({ loaded: 0, total: file.size });
      const form = new FormData();
      form.append('file', file, file.name);
      return uploadWithProgress<RestoreStagedResponse>('/system/backup/restore/upload', form, {
        signal: c.signal,
        onProgress: setProgress,
      });
    },
    onSettled: () => {
      controller.current = null;
    },
  });

  const cancel = useCallback(() => controller.current?.abort(), []);
  const { reset: resetMutation } = mutation;
  const reset = useCallback(() => {
    setProgress(null);
    resetMutation();
  }, [resetMutation]);

  return { ...mutation, progress, cancel, reset };
}
