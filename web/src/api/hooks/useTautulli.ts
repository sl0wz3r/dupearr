/** Watch history: Tautulli connections (docs/API.md → Watch history (Tautulli)). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { Id, TautulliInstance, TautulliInstanceInput, TautulliTestResult } from '../types';

export function useTautulliInstances() {
  return useQuery({
    queryKey: queryKeys.tautulli.list,
    queryFn: ({ signal }) => api.get<TautulliInstance[]>('/tautulli', undefined, { signal }),
  });
}

function useInvalidateTautulli() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.tautulli.all });
    void qc.invalidateQueries({ queryKey: queryKeys.health });
  };
}

/** POST /tautulli (tests first unless `forceSave`). */
export function useCreateTautulli() {
  const invalidate = useInvalidateTautulli();
  return useMutation({
    mutationFn: ({ instance, forceSave }: { instance: TautulliInstanceInput; forceSave?: boolean }) =>
      api.post<TautulliInstance>('/tautulli', instance, { query: { forceSave: forceSave || undefined } }),
    onSuccess: invalidate,
  });
}

export function useUpdateTautulli() {
  const invalidate = useInvalidateTautulli();
  return useMutation({
    mutationFn: ({ instance, forceSave }: { instance: TautulliInstanceInput & { id: Id }; forceSave?: boolean }) =>
      api.put<TautulliInstance>(`/tautulli/${instance.id}`, instance, {
        query: { forceSave: forceSave || undefined },
      }),
    onSuccess: invalidate,
  });
}

export function useDeleteTautulli() {
  const invalidate = useInvalidateTautulli();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/tautulli/${id}`),
    onSuccess: invalidate,
  });
}

export function useTestTautulli() {
  return useMutation({
    mutationFn: (instance: TautulliInstanceInput) => api.post<TautulliTestResult>('/tautulli/test', instance),
  });
}
