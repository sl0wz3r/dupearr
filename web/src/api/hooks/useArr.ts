/** Applications: Radarr / Sonarr instances (docs/API.md → Applications). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { ArrInstance, ArrInstanceInput, ArrTestResult, Id } from '../types';

export function useArrInstances() {
  return useQuery({
    queryKey: queryKeys.arr.list,
    queryFn: ({ signal }) => api.get<ArrInstance[]>('/arr', undefined, { signal }),
  });
}

export function useArrInstance(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.arr.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<ArrInstance>(`/arr/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

function useInvalidateArr() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.arr.all });
    void qc.invalidateQueries({ queryKey: queryKeys.health });
  };
}

/** POST /arr (tests first unless `forceSave`). */
export function useCreateArrInstance() {
  const invalidate = useInvalidateArr();
  return useMutation({
    mutationFn: ({ instance, forceSave }: { instance: ArrInstanceInput; forceSave?: boolean }) =>
      api.post<ArrInstance>('/arr', instance, { query: { forceSave: forceSave || undefined } }),
    onSuccess: invalidate,
  });
}

export function useUpdateArrInstance() {
  const invalidate = useInvalidateArr();
  return useMutation({
    mutationFn: ({ instance, forceSave }: { instance: ArrInstanceInput & { id: Id }; forceSave?: boolean }) =>
      api.put<ArrInstance>(`/arr/${instance.id}`, instance, {
        query: { forceSave: forceSave || undefined },
      }),
    onSuccess: invalidate,
  });
}

export function useDeleteArrInstance() {
  const invalidate = useInvalidateArr();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/arr/${id}`),
    onSuccess: invalidate,
  });
}

export function useTestArrInstance() {
  return useMutation({
    mutationFn: (instance: ArrInstanceInput) => api.post<ArrTestResult>('/arr/test', instance),
  });
}
