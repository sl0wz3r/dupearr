/** Path mappings (remote → local). POST/PUT may return a warning header `X-Dupearr-Warning`. */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, requestWithMeta } from '../client';
import { queryKeys } from '../queryKeys';
import type { Id, PathMapping, PathMappingInput } from '../types';

export interface PathMappingSaveResult {
  mapping: PathMapping;
  /** Non-fatal warning, e.g. "local path does not exist". */
  warning: string | null;
}

export function usePathMappings() {
  return useQuery({
    queryKey: queryKeys.pathMappings.list,
    queryFn: ({ signal }) => api.get<PathMapping[]>('/pathmapping', undefined, { signal }),
  });
}

export function usePathMapping(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.pathMappings.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<PathMapping>(`/pathmapping/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.pathMappings.all });
    void qc.invalidateQueries({ queryKey: queryKeys.health });
  };
}

async function save(mapping: PathMappingInput): Promise<PathMappingSaveResult> {
  const res = mapping.id
    ? await requestWithMeta<PathMapping>('PUT', `/pathmapping/${mapping.id}`, { body: mapping })
    : await requestWithMeta<PathMapping>('POST', '/pathmapping', { body: mapping });
  return { mapping: res.data, warning: res.headers.get('X-Dupearr-Warning') };
}

/** Creates (no id) or updates (with id) a mapping. */
export function useSavePathMapping() {
  const invalidate = useInvalidate();
  return useMutation({ mutationFn: save, onSuccess: invalidate });
}

export function useDeletePathMapping() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/pathmapping/${id}`),
    onSuccess: invalidate,
  });
}
