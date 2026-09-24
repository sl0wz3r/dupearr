/** Settings: host config (config.xml) and operational settings (DB). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { HostConfig, Settings } from '../types';
import { MASKED_SECRET } from '@/lib/constants';

export function useHostConfig() {
  return useQuery({
    queryKey: queryKeys.config.host,
    queryFn: ({ signal }) => api.get<HostConfig>('/config/host', undefined, { signal }),
  });
}

/** PUT /config/host → HostConfig (check `restartRequired`). */
export function useUpdateHostConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: HostConfig) => api.put<HostConfig>('/config/host', body),
    onSuccess: (data) => {
      qc.setQueryData(queryKeys.config.host, data);
      void qc.invalidateQueries({ queryKey: queryKeys.system.status });
      void qc.invalidateQueries({ queryKey: queryKeys.health });
    },
  });
}

/**
 * POST /config/host/apikey — regenerates the API key (current password required when a Forms account
 * exists). The response carries the new key; the web UI itself authenticates with its session.
 */
export function useRegenerateApiKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (currentPassword: string | undefined) =>
      api.post<HostConfig>('/config/host/apikey', currentPassword ? { currentPassword } : undefined),
    onSuccess: (data) => {
      // Keep the cache free of the revealed key (GET /config/host masks it).
      if (data) qc.setQueryData(queryKeys.config.host, { ...data, apiKey: MASKED_SECRET });
    },
  });
}

/** POST /config/host/apikey/reveal — the API key, after checking the current password. */
export function useRevealApiKey() {
  return useMutation({
    mutationFn: (currentPassword: string | undefined) =>
      api.post<{ apiKey: string }>('/config/host/apikey/reveal', currentPassword ? { currentPassword } : undefined),
  });
}

/** POST /config/host/webhooktoken — replaces the webhook token (webhook URLs must be updated). */
export function useRegenerateWebhookToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<HostConfig>('/config/host/webhooktoken'),
    onSuccess: (data) => {
      if (data) qc.setQueryData(queryKeys.config.host, data);
    },
  });
}

/** POST /auth/sessions/revoke — signs out every other session (this one is re-issued). */
export function useRevokeSessions() {
  return useMutation({ mutationFn: () => api.post<unknown>('/auth/sessions/revoke') });
}

export function useSettings() {
  return useQuery({
    queryKey: queryKeys.config.settings,
    queryFn: ({ signal }) => api.get<Settings>('/config/settings', undefined, { signal }),
  });
}

/** PUT /config/settings (server re-evaluates open groups). */
export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Settings) => api.put<Settings>('/config/settings', body),
    onSuccess: (data) => {
      qc.setQueryData(queryKeys.config.settings, data);
      void qc.invalidateQueries({ queryKey: queryKeys.system.status });
      void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
      void qc.invalidateQueries({ queryKey: queryKeys.health });
      void qc.invalidateQueries({ queryKey: queryKeys.system.tasks });
    },
  });
}
