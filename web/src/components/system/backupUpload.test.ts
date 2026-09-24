import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, configureApi } from '@/api/client';
import { MAX_BACKUP_UPLOAD_BYTES, isAbortError, uploadWithProgress, validateBackupFile } from './backupUpload';
import { FakeXhr } from './fakeXhr';

describe('validateBackupFile', () => {
  it.each([
    [null, /Choose a backup file/],
    [{ name: 'dupearr_backup.zip', size: 1024 }, null],
    [{ name: 'DUPEARR.ZIP', size: 1 }, null],
    [{ name: 'dupearr_backup.zip', size: MAX_BACKUP_UPLOAD_BYTES }, null],
    [{ name: 'dupearr_backup.zip', size: MAX_BACKUP_UPLOAD_BYTES + 1 }, /maximum is 512 MiB/],
    [{ name: 'dupearr_backup.zip', size: 0 }, /empty/],
    [{ name: 'dupearr.db', size: 10 }, /must be .zip/],
    [{ name: 'config.xml', size: 10 }, /must be .zip/],
    [{ name: 'backup.zip.exe', size: 10 }, /must be .zip/],
  ] as const)('%j', (file, expected) => {
    const result = validateBackupFile(file);
    if (expected === null) expect(result).toBeNull();
    else expect(result).toMatch(expected);
  });
});

describe('uploadWithProgress', () => {
  beforeEach(() => {
    FakeXhr.last = null;
    vi.stubGlobal('XMLHttpRequest', FakeXhr);
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('posts the form (session cookie, no API key), reports progress and resolves the JSON body', async () => {
    const progress = vi.fn();
    const form = new FormData();
    const promise = uploadWithProgress<{ restartRequired: boolean }>('/system/backup/restore/upload', form, {
      onProgress: progress,
    });
    const xhr = FakeXhr.last!;
    expect(xhr.method).toBe('POST');
    expect(xhr.url).toBe('/api/v1/system/backup/restore/upload');
    expect(xhr.headers['X-Api-Key']).toBeUndefined();
    expect(xhr.headers['Content-Type']).toBeUndefined();
    expect(xhr.withCredentials).toBe(false);
    expect(xhr.body).toBe(form);

    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 50, total: 100 });
    xhr.upload.onprogress?.({ lengthComputable: false, loaded: 60, total: 0 });
    expect(progress).toHaveBeenCalledTimes(1);
    expect(progress).toHaveBeenCalledWith({ loaded: 50, total: 100 });
    // The end of the upload always reports 100%, even without a final progress event.
    xhr.upload.onload?.();
    expect(progress).toHaveBeenLastCalledWith({ loaded: 100, total: 100 });

    xhr.respond(200, '{"restartRequired":true}');
    await expect(promise).resolves.toEqual({ restartRequired: true });
  });

  it('rejects with a parsed ApiError on HTTP errors', async () => {
    const promise = uploadWithProgress('/x', new FormData());
    FakeXhr.last!.respond(400, '{"message":"Backup is missing dupearr.db"}');
    const err = await promise.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(400);
    expect((err as ApiError).message).toBe('Backup is missing dupearr.db');
  });

  it('rejects with a network ApiError when the connection fails', async () => {
    const promise = uploadWithProgress('/x', new FormData());
    FakeXhr.last!.onerror?.();
    const err = await promise.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(0);
  });

  it('aborts through the signal (and never starts when already aborted)', async () => {
    const controller = new AbortController();
    const promise = uploadWithProgress('/x', new FormData(), { signal: controller.signal });
    controller.abort();
    const err = await promise.catch((e: unknown) => e);
    expect(isAbortError(err)).toBe(true);
    expect(FakeXhr.last!.aborted).toBe(true);

    FakeXhr.last = null;
    const pre = uploadWithProgress('/x', new FormData(), { signal: controller.signal });
    expect(isAbortError(await pre.catch((e: unknown) => e))).toBe(true);
    expect(FakeXhr.last).toBeNull();
  });
});
