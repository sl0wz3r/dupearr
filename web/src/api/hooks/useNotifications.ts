/** Notifications (Settings → Connect). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type {
  Id,
  NotificationConfig,
  NotificationConfigInput,
  NotificationTriggerOption,
  ProviderSchema,
} from '../types';

export function useNotifications() {
  return useQuery({
    queryKey: queryKeys.notifications.list,
    queryFn: ({ signal }) => api.get<NotificationConfig[]>('/notification', undefined, { signal }),
  });
}

export function useNotification(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.notifications.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<NotificationConfig>(`/notification/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

export function useNotificationSchema() {
  return useQuery({
    queryKey: queryKeys.notifications.schema,
    queryFn: ({ signal }) => api.get<ProviderSchema[]>('/notification/schema', undefined, { signal }),
    staleTime: Infinity,
  });
}

export function useNotificationTriggers() {
  return useQuery({
    queryKey: queryKeys.notifications.triggers,
    queryFn: ({ signal }) =>
      api.get<NotificationTriggerOption[]>('/notification/triggers', undefined, { signal }),
    staleTime: Infinity,
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.notifications.list });
    void qc.invalidateQueries({ queryKey: [...queryKeys.notifications.all, 'detail'] });
  };
}

export function useCreateNotification() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (cfg: NotificationConfigInput) => api.post<NotificationConfig>('/notification', cfg),
    onSuccess: invalidate,
  });
}

export function useUpdateNotification() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (cfg: NotificationConfigInput & { id: Id }) =>
      api.put<NotificationConfig>(`/notification/${cfg.id}`, cfg),
    onSuccess: invalidate,
  });
}

export function useDeleteNotification() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/notification/${id}`),
    onSuccess: invalidate,
  });
}

/** POST /notification/test — 400 `{message}` surfaces as ApiError. */
export function useTestNotification() {
  return useMutation({
    mutationFn: (cfg: NotificationConfigInput) => api.post<Record<string, never>>('/notification/test', cfg),
  });
}
