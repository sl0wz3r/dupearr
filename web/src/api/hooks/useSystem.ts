/** System: status, health, tasks, restart, logs, backups (docs/API.md → System). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type {
  Backup,
  Command,
  HealthCheck,
  Id,
  LogEntry,
  LogFile,
  LogListParams,
  PagedResponse,
  RestoreResponse,
  RestoreStagedResponse,
  ScheduledTask,
  SystemStatus,
} from '../types';

export function useSystemStatus() {
  return useQuery({
    queryKey: queryKeys.system.status,
    queryFn: ({ signal }) => api.get<SystemStatus>('/system/status', undefined, { signal }),
  });
}

/** Health issues (kept fresh by the `health` SSE event; also polled every 5 min as a fallback). */
export function useHealth() {
  return useQuery({
    queryKey: queryKeys.health,
    queryFn: ({ signal }) => api.get<HealthCheck[]>('/health', undefined, { signal }),
    refetchInterval: 5 * 60_000,
  });
}

/** POST /health/check — queues CheckHealth. */
export function useRunHealthCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<Command>('/health/check'),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.commands.all });
    },
  });
}

export function useScheduledTasks() {
  return useQuery({
    queryKey: queryKeys.system.tasks,
    queryFn: ({ signal }) => api.get<ScheduledTask[]>('/system/task', undefined, { signal }),
  });
}

/** POST /system/restart (202; process restarts after responding). */
export function useRestart() {
  return useMutation({
    mutationFn: () => api.post<Record<string, never>>('/system/restart'),
  });
}

/** Paged in-memory log entries (System → Events). */
export function useLogs(params: LogListParams = {}) {
  return useQuery({
    queryKey: queryKeys.system.logs(params),
    queryFn: ({ signal }) =>
      api.get<PagedResponse<LogEntry>>('/log', { ...params }, { signal }),
    placeholderData: (prev) => prev,
  });
}

export function useLogFiles() {
  return useQuery({
    queryKey: queryKeys.system.logFiles,
    queryFn: ({ signal }) => api.get<LogFile[]>('/log/file', undefined, { signal }),
  });
}

/** Content of one log file (text/plain). Pass `null` to disable. */
export function useLogFile(filename: string | null) {
  return useQuery({
    queryKey: queryKeys.system.logFile(filename ?? ''),
    queryFn: ({ signal }) =>
      api.text(`/log/file/${encodeURIComponent(filename ?? '')}`, undefined, { signal }),
    enabled: !!filename,
  });
}

export function useBackups() {
  return useQuery({
    queryKey: queryKeys.system.backups,
    queryFn: ({ signal }) => api.get<Backup[]>('/system/backup', undefined, { signal }),
  });
}

export function useCreateBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<Backup>('/system/backup'),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.system.backups });
    },
  });
}

export function useDeleteBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: Id) => api.del<Record<string, never>>(`/system/backup/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.system.backups });
    },
  });
}

/**
 * POST /system/backup/download/{id} — the backup zip as a Blob. A backup holds every credential, so
 * the current password is required when a Forms account exists (a session alone never downloads one).
 */
export function useDownloadBackup() {
  return useMutation({
    mutationFn: ({ id, currentPassword }: { id: Id; currentPassword?: string }) =>
      api.post<Blob>(`/system/backup/download/${id}`, currentPassword ? { currentPassword } : undefined, {
        responseType: 'blob',
      }),
  });
}

/** Stages a stored backup for restore and returns what it changes (nothing is applied yet). */
export function useRestoreBackup() {
  return useMutation({
    mutationFn: (id: Id) => api.post<RestoreStagedResponse>(`/system/backup/restore/${id}`),
  });
}

/** Stages an uploaded backup zip (multipart field `file`) for restore. */
export function useRestoreBackupUpload() {
  return useMutation({
    mutationFn: (file: File) => {
      const form = new FormData();
      form.append('file', file);
      return api.upload<RestoreStagedResponse>('/system/backup/restore/upload', form);
    },
  });
}

/** Confirms the staged restore; the server restarts to apply it. 409 when it became stale. */
export function useConfirmRestore() {
  return useMutation({
    mutationFn: () => api.post<RestoreResponse>('/system/backup/restore/confirm'),
  });
}

/** Discards the staged restore. */
export function useDiscardRestore() {
  return useMutation({
    mutationFn: () => api.del<Record<string, never>>('/system/backup/restore'),
  });
}
