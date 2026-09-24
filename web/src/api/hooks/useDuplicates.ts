/** Duplicate groups (docs/API.md → Duplicates). */
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, isApiError } from '../client';
import { queryKeys } from '../queryKeys';
import type {
  Action,
  ApproveDuplicateRequest,
  BulkDuplicateRequest,
  BulkDuplicateResponse,
  Command,
  Decision,
  DuplicateGroup,
  DuplicateGroupDetail,
  DuplicateGroupSummary,
  DuplicateListParams,
  DuplicateStats,
  Id,
  PagedResponse,
} from '../types';

/** Paged list; `status` is sent as a comma list. Keeps the previous page while loading the next. */
export function useDuplicates(params: DuplicateListParams = {}) {
  return useQuery({
    queryKey: queryKeys.duplicates.list(params),
    queryFn: ({ signal }) =>
      api.get<PagedResponse<DuplicateGroupSummary>>('/duplicate', { ...params }, { signal }),
    placeholderData: keepPreviousData,
  });
}

export function useDuplicateStats() {
  return useQuery({
    queryKey: queryKeys.duplicates.stats,
    queryFn: ({ signal }) => api.get<DuplicateStats>('/duplicate/stats', undefined, { signal }),
  });
}

/** Full group + actions. */
export function useDuplicate(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.duplicates.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<DuplicateGroupDetail>(`/duplicate/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

function useInvalidateDuplicates() {
  const qc = useQueryClient();
  return (id?: Id) => {
    void qc.invalidateQueries({ queryKey: queryKeys.duplicates.lists });
    void qc.invalidateQueries({ queryKey: queryKeys.duplicates.stats });
    if (id != null) void qc.invalidateQueries({ queryKey: queryKeys.duplicates.detail(id) });
    else void qc.invalidateQueries({ queryKey: queryKeys.duplicates.details });
  };
}

/** Variables of {@link useApproveDuplicate}. */
export interface ApproveDuplicateVariables {
  id: Id;
  /**
   * Signature of the group as the user reviewed it (captured when the confirmation opened). The
   * server refuses the approval with 409 when the group's decisions changed since.
   */
  signature?: string;
}

/**
 * POST /duplicate/{id}/approve → queued actions (400 when invariants fail, 409 when the group
 * changed since it was reviewed or can no longer be approved). A refused approval refreshes the
 * group so the user reviews its current decisions.
 */
export function useApproveDuplicate() {
  const invalidate = useInvalidateDuplicates();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, signature }: ApproveDuplicateVariables) => {
      const body: ApproveDuplicateRequest | undefined = signature ? { signature } : undefined;
      return api.post<Action[]>(`/duplicate/${id}/approve`, body);
    },
    onSuccess: (_actions, { id }) => {
      invalidate(id);
      void qc.invalidateQueries({ queryKey: queryKeys.queue.all });
      void qc.invalidateQueries({ queryKey: queryKeys.actions.all });
    },
    onError: (e, { id }) => {
      // The server's view differs from what was shown (409 changed/queued/resolved, 400 invariant,
      // 404 gone): reload it. Network failures (status 0) don't tell us anything new.
      if (isApiError(e) && e.status >= 400 && e.status < 500) invalidate(id);
    },
  });
}

export function useIgnoreDuplicate() {
  const invalidate = useInvalidateDuplicates();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, addExclusion = false }: { id: Id; addExclusion?: boolean }) =>
      api.post<DuplicateGroup>(`/duplicate/${id}/ignore`, { addExclusion }),
    onSuccess: (_g, { id, addExclusion }) => {
      invalidate(id);
      if (addExclusion) void qc.invalidateQueries({ queryKey: queryKeys.exclusions.all });
    },
  });
}

export function useUnignoreDuplicate() {
  const invalidate = useInvalidateDuplicates();
  return useMutation({
    mutationFn: (id: Id) => api.post<DuplicateGroup>(`/duplicate/${id}/unignore`),
    onSuccess: (_g, id) => invalidate(id),
  });
}

/** PUT /duplicate/{id}/file/{fileId}/override — decision `null` clears the override. */
export function useSetFileOverride() {
  const invalidate = useInvalidateDuplicates();
  return useMutation({
    mutationFn: ({ groupId, fileId, decision }: { groupId: Id; fileId: Id; decision: Decision | null }) =>
      api.put<DuplicateGroup>(`/duplicate/${groupId}/file/${fileId}/override`, { decision }),
    onSuccess: (_g, { groupId }) => invalidate(groupId),
  });
}

/** POST /duplicate/{id}/rescan → TargetedScan command. */
export function useRescanDuplicate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: Id) => api.post<Command>(`/duplicate/${id}/rescan`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.commands.all });
    },
  });
}

/**
 * POST /duplicate/bulk → `{succeeded, failed}`. Approvals should carry `signatures` (the rows as
 * the user reviewed them); groups that changed since come back in `failed` with the reason.
 */
export function useBulkDuplicateAction() {
  const invalidate = useInvalidateDuplicates();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BulkDuplicateRequest) => api.post<BulkDuplicateResponse>('/duplicate/bulk', body),
    onSuccess: (_r, body) => {
      invalidate();
      if (body.action === 'approve') {
        void qc.invalidateQueries({ queryKey: queryKeys.queue.all });
        void qc.invalidateQueries({ queryKey: queryKeys.actions.all });
      }
    },
    onError: (e) => {
      if (isApiError(e) && e.status >= 400 && e.status < 500) invalidate();
    },
  });
}
