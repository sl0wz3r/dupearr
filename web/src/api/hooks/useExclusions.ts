/** Exclusions (content removed from duplicate detection). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { Exclusion, ExclusionInput, Id } from '../types';

export function useExclusions() {
  return useQuery({
    queryKey: queryKeys.exclusions.all,
    queryFn: ({ signal }) => api.get<Exclusion[]>('/exclusion', undefined, { signal }),
  });
}

export function useCreateExclusion() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (exclusion: ExclusionInput) => api.post<Exclusion>('/exclusion', exclusion),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.exclusions.all });
    },
  });
}

export function useDeleteExclusion() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/exclusion/${id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.exclusions.all });
    },
  });
}
