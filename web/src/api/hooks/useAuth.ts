/** Bootstrap + authentication (initialize.json, /login, /api/v1/auth/*). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { bootstrap, getAuthStatus, login, logout, setupAuth } from '../client';
import { queryKeys } from '../queryKeys';
import type { AuthSetupRequest, LoginRequest } from '../types';

/**
 * initialize.json bootstrap. Resolves to `{status:'ready', init}` | `{status:'login'}` |
 * `{status:'setup'}`; configures the API client as a side effect. Used by the AuthGate.
 */
export function useInitialize() {
  return useQuery({
    queryKey: queryKeys.initialize,
    queryFn: ({ signal }) => bootstrap(signal),
    staleTime: Infinity,
    // Drop the result once the gate unmounts (e.g. on /login) so the next visit re-bootstraps.
    gcTime: 0,
    retry: 1,
    refetchOnWindowFocus: false,
  });
}

/** GET /api/v1/auth/status (no auth). */
export function useAuthStatus() {
  return useQuery({
    queryKey: queryKeys.authStatus,
    queryFn: ({ signal }) => getAuthStatus(signal),
    staleTime: 0,
    retry: 1,
  });
}

/** POST {urlBase}/login; on success drops the cached bootstrap so the gate re-initializes. */
export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: LoginRequest) => login(body),
    onSuccess: () => {
      qc.removeQueries({ queryKey: queryKeys.initialize });
    },
  });
}

/** POST /api/v1/auth/setup (first run). */
export function useAuthSetup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AuthSetupRequest) => setupAuth(body),
    onSuccess: () => {
      qc.removeQueries({ queryKey: queryKeys.initialize });
      void qc.invalidateQueries({ queryKey: queryKeys.authStatus });
    },
  });
}

/** Logs out (POST /logout) and hard-navigates to the login page. */
export function useLogout() {
  const qc = useQueryClient();
  return () => {
    qc.clear();
    void logout();
  };
}
