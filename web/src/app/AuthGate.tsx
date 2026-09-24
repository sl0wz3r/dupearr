import { useQueryClient } from '@tanstack/react-query';
import { useEffect, type ReactNode } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router';
import { errorMessage, getUrlBase, onUnauthorized } from '@/api/client';
import { ServerEventsProvider } from '@/api/events';
import { useInitialize } from '@/api/hooks/useAuth';
import { queryKeys } from '@/api/queryKeys';
import { ErrorFallback } from '@/components/ErrorBoundary';
import { LoadingIndicator } from '@/components/ui/Spinner';
import { AppInfoProvider } from './AppInfo';

/** ASCII control characters (the URL parser silently drops tab/CR/LF) or a backslash (read as "/"). */
const UNSAFE_RETURN_URL_CHARS = /[\u0000-\u001f\u007f\\]/;

/** Resolution base when the page has no http(s) origin (tests, sandboxed frames). */
const FALLBACK_ORIGIN = 'http://dupearr.invalid';

/**
 * Path + search + hash to come back to after login: only a path within this app, never the
 * login/setup pages. The result is used both for router navigation and for
 * `window.location.assign(withUrlBase(...))`, so it must not be able to name another origin.
 *
 * Mirrors the server's safeReturnURL: control characters and backslashes are refused anywhere
 * ("/\t/evil.example" and "/\\evil.example" both parse as "//evil.example"), the value is resolved
 * against this origin and must stay on it, and the normalized path must not start with "//"
 * ("/.//evil.example" normalizes to the path "//evil.example").
 */
export function safeReturnUrl(value: string | null | undefined): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//')) return '/';
  if (UNSAFE_RETURN_URL_CHARS.test(value)) return '/';

  const origin =
    typeof window !== 'undefined' && /^https?:\/\//i.test(window.location.origin)
      ? window.location.origin
      : FALLBACK_ORIGIN;
  let url: URL;
  try {
    url = new URL(value, origin);
  } catch {
    return '/';
  }
  if (url.origin !== origin) return '/';

  const path = `${url.pathname}${url.search}${url.hash}`;
  if (!url.pathname.startsWith('/') || url.pathname.startsWith('//')) return '/';
  if (isAuthPage(url.pathname)) return '/';
  return path;
}

/** /login and /setup (also under the url base, e.g. "/dupearr/login"). */
function isAuthPage(pathname: string): boolean {
  const base = getUrlBase();
  const local = base && (pathname === base || pathname.startsWith(`${base}/`)) ? pathname.slice(base.length) : pathname;
  return [pathname, local].some((p) => /^\/(login|setup)(\/|$)/i.test(p));
}

/**
 * Loads initialize.json before rendering the app. Redirects to /login or /setup when needed and
 * back to /login whenever an API call returns 401 (expired session).
 */
export function AuthGate({ children }: { children: ReactNode }) {
  const init = useInitialize();
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();

  useEffect(
    () =>
      onUnauthorized(() => {
        const returnUrl = `${window.location.pathname}${window.location.search}`;
        qc.removeQueries({ queryKey: queryKeys.initialize });
        navigate(`/login?returnUrl=${encodeURIComponent(safeReturnUrl(stripBase(returnUrl)))}`, { replace: true });
      }),
    [navigate, qc],
  );

  if (init.isPending) return <LoadingIndicator fill message="Loading Dupearr…" className="h-dvh bg-page" />;

  if (init.isError) {
    return (
      <div className="h-dvh bg-page">
        <ErrorFallback
          error={new Error(`Failed to load Dupearr: ${errorMessage(init.error)}`)}
          onRetry={() => void init.refetch()}
        />
      </div>
    );
  }

  const result = init.data;
  if (result.status === 'setup') return <Navigate to="/setup" replace />;
  if (result.status === 'login') {
    const returnUrl = `${location.pathname}${location.search}`;
    return <Navigate to={`/login?returnUrl=${encodeURIComponent(safeReturnUrl(returnUrl))}`} replace />;
  }

  return (
    <AppInfoProvider value={result.init}>
      <ServerEventsProvider>{children}</ServerEventsProvider>
    </AppInfoProvider>
  );
}

/** window.location.pathname includes the url base; router paths don't. */
function stripBase(path: string): string {
  const base = getUrlBase();
  return base && path.startsWith(base) ? path.slice(base.length) || '/' : path;
}
