/** Activity: queue, actions, history, scans (docs/API.md → Activity). */
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, isApiError } from '../client';
import { queryKeys } from '../queryKeys';
import type {
  Action,
  ActionListParams,
  HistoryEvent,
  HistoryListParams,
  Id,
  PagedResponse,
  PagingParams,
  ScanRun,
} from '../types';

/** Pending/running actions (oldest first). */
export function useQueue(params: PagingParams = {}) {
  return useQuery({
    queryKey: queryKeys.queue.list(params),
    queryFn: ({ signal }) => api.get<PagedResponse<Action>>('/queue', { ...params }, { signal }),
    placeholderData: keepPreviousData,
  });
}

/**
 * DELETE /queue/{id} — cancel a pending action. The server refuses (409) an action that is already
 * running or finished, and 404s one that no longer exists: the list is refreshed then too, so the
 * page shows the action's real state.
 */
export function useCancelQueueItem() {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.queue.all });
    void qc.invalidateQueries({ queryKey: queryKeys.actions.all });
    void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
  };
  return useMutation({
    mutationFn: (id: Id) => api.del(`/queue/${id}`),
    onSuccess: refresh,
    onError: (e) => {
      if (isApiError(e) && (e.isConflict || e.status === 404)) refresh();
    },
  });
}

/** All actions (paged); filter `status` as a comma list. */
export function useActions(params: ActionListParams = {}) {
  return useQuery({
    queryKey: queryKeys.actions.list(params),
    queryFn: ({ signal }) => api.get<PagedResponse<Action>>('/action', { ...params }, { signal }),
    placeholderData: keepPreviousData,
  });
}

/** POST /action/{id}/restore — undo a filesystem recycle-bin removal. */
export function useRestoreAction() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: Id) => api.post<unknown>(`/action/${id}/restore`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.actions.all });
      void qc.invalidateQueries({ queryKey: queryKeys.history.all });
      void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
    },
  });
}

/** Paged audit log; filters `eventType` (comma list), `groupId`. */
export function useHistory(params: HistoryListParams = {}) {
  return useQuery({
    queryKey: queryKeys.history.list(params),
    queryFn: ({ signal }) => api.get<PagedResponse<HistoryEvent>>('/history', { ...params }, { signal }),
    placeholderData: keepPreviousData,
  });
}

/** Latest 50 scan runs. */
export function useScans() {
  return useQuery({
    queryKey: queryKeys.scans.all,
    queryFn: ({ signal }) => api.get<ScanRun[]>('/scan', undefined, { signal }),
  });
}
