/** Decision profiles (docs/API.md → Profiles). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { Id, Profile, ProfileInput, ProfileSchema } from '../types';

export function useProfiles() {
  return useQuery({
    queryKey: queryKeys.profiles.list,
    queryFn: ({ signal }) => api.get<Profile[]>('/profile', undefined, { signal }),
  });
}

export function useProfile(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.profiles.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<Profile>(`/profile/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
  });
}

/** Criteria schema, templates, protection types and keepPer values (static per server version). */
export function useProfileSchema() {
  return useQuery({
    queryKey: queryKeys.profiles.schema,
    queryFn: ({ signal }) => api.get<ProfileSchema>('/profile/schema', undefined, { signal }),
    staleTime: Infinity,
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: queryKeys.profiles.all });
    // Profile changes re-evaluate open groups.
    void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
  };
}

export function useCreateProfile() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (profile: ProfileInput) => api.post<Profile>('/profile', profile),
    onSuccess: invalidate,
  });
}

export function useUpdateProfile() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (profile: ProfileInput & { id: Id }) => api.put<Profile>(`/profile/${profile.id}`, profile),
    onSuccess: invalidate,
  });
}

/** DELETE /profile/{id} — deleting the default profile → 409. */
export function useDeleteProfile() {
  const invalidate = useInvalidate();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: Id) => api.del(`/profile/${id}`),
    onSuccess: () => {
      invalidate();
      void qc.invalidateQueries({ queryKey: queryKeys.libraries.all });
    },
  });
}
