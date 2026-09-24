/** Media servers, libraries and Plex sign-in (docs/API.md → Media servers & libraries). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, apiUrl } from '../client';
import { queryKeys } from '../queryKeys';
import type {
  Id,
  Library,
  MediaServer,
  MediaServerInput,
  MediaServerTestResult,
  PlexPin,
  PlexPinStatus,
  PlexServer,
} from '../types';

export function useMediaServers() {
  return useQuery({
    queryKey: queryKeys.mediaServers.list,
    queryFn: ({ signal }) => api.get<MediaServer[]>('/mediaserver', undefined, { signal }),
  });
}

export function useMediaServer(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.mediaServers.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<MediaServer>(`/mediaserver/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

function useInvalidateServers() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.mediaServers.all });
    void qc.invalidateQueries({ queryKey: queryKeys.libraries.all });
    // Deleting a server deletes its Tautulli connection too (ON DELETE CASCADE).
    void qc.invalidateQueries({ queryKey: queryKeys.tautulli.all });
    void qc.invalidateQueries({ queryKey: queryKeys.health });
  };
}

/** POST /mediaserver (tests the connection first unless `forceSave`). */
export function useCreateMediaServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: ({ server, forceSave }: { server: MediaServerInput; forceSave?: boolean }) =>
      api.post<MediaServer>('/mediaserver', server, { query: { forceSave: forceSave || undefined } }),
    onSuccess: invalidate,
  });
}

export function useUpdateMediaServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: ({ server, forceSave }: { server: MediaServerInput & { id: Id }; forceSave?: boolean }) =>
      api.put<MediaServer>(`/mediaserver/${server.id}`, server, {
        query: { forceSave: forceSave || undefined },
      }),
    onSuccess: invalidate,
  });
}

export function useDeleteMediaServer() {
  const invalidate = useInvalidateServers();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/mediaserver/${id}`),
    onSuccess: invalidate,
  });
}

/** POST /mediaserver/test — 400/502 surface as ApiError with the server message. */
export function useTestMediaServer() {
  return useMutation({
    mutationFn: (server: MediaServerInput) => api.post<MediaServerTestResult>('/mediaserver/test', server),
  });
}

/** Libraries of one server. */
export function useServerLibraries(serverId: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.mediaServers.libraries(serverId ?? 0),
    queryFn: ({ signal }) => api.get<Library[]>(`/mediaserver/${serverId}/library`, undefined, { signal }),
    enabled: serverId != null && serverId > 0,
  });
}

/** POST /mediaserver/{id}/library/sync — re-sync from the server. */
export function useSyncServerLibraries() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (serverId: Id) => api.post<Library[]>(`/mediaserver/${serverId}/library/sync`),
    onSuccess: (libs, serverId) => {
      qc.setQueryData(queryKeys.mediaServers.libraries(serverId), libs);
      void qc.invalidateQueries({ queryKey: queryKeys.libraries.all });
    },
  });
}

/** All libraries of all servers. */
export function useLibraries() {
  return useQuery({
    queryKey: queryKeys.libraries.all,
    queryFn: ({ signal }) => api.get<Library[]>('/library', undefined, { signal }),
  });
}

/** PUT /library/{id} (enabled, profileId, scopeGroup editable). */
export function useUpdateLibrary() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (library: Library) => api.put<Library>(`/library/${library.id}`, library),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.libraries.all });
      void qc.invalidateQueries({ queryKey: queryKeys.mediaServers.all });
      void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
    },
  });
}

/** URL of the poster proxy (`<img src>`, same origin: authenticated by the session cookie). */
export function mediaCoverUrl(serverId: Id, path: string, width = 150, height = 225): string {
  return apiUrl(`/mediacover/${serverId}`, { path, w: width, h: height });
}

// --- Plex sign-in (plex.tv PIN flow) ---------------------------------------

/** POST /plex/pin → open `authUrl` in a popup, then poll with usePlexPin. */
export function useCreatePlexPin() {
  return useMutation({
    mutationFn: () => api.post<PlexPin>('/plex/pin'),
  });
}

/** Polls GET /plex/pin/{id} every 2s while `enabled` and not yet authenticated. */
export function usePlexPin(id: Id | null | undefined, enabled = true) {
  return useQuery({
    queryKey: queryKeys.plex.pin(id ?? 0),
    queryFn: ({ signal }) => api.get<PlexPinStatus>(`/plex/pin/${id}`, undefined, { signal }),
    enabled: enabled && id != null && id > 0,
    refetchInterval: (query) => (query.state.data?.authenticated ? false : 2000),
    gcTime: 0,
  });
}

/**
 * GET /plex/servers — servers available to a plex.tv account. The account token goes in the
 * X-Plex-Token header, never in the URL: it controls the whole Plex account, and URLs end up in
 * reverse-proxy access logs, CDN logs and browser history.
 */
export function usePlexServers(token: string | null | undefined) {
  return useQuery({
    queryKey: queryKeys.plex.servers(token ?? ''),
    queryFn: ({ signal }) =>
      api.get<PlexServer[]>('/plex/servers', undefined, { signal, headers: { 'X-Plex-Token': token ?? '' } }),
    enabled: !!token,
    staleTime: 5 * 60_000,
  });
}
