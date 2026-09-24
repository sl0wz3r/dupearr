import { LogIn, RefreshCw } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate, useSearchParams } from 'react-router';
import { errorMessage, isApiError, withUrlBase } from '@/api/client';
import { useAuthStatus, useLogin } from '@/api/hooks/useAuth';
import { safeReturnUrl } from '@/app/AuthGate';
import { Alert, Button, Checkbox, LoadingIndicator, PasswordInput, TextInput } from '@/components/ui';
import { AuthField, AuthLayout } from './AuthLayout';

/**
 * Forms-authentication login (POST {urlBase}/login). Redirects to first-run setup when no user
 * exists, and back to `?returnUrl=` after signing in.
 */
export default function LoginPage() {
  const [params] = useSearchParams();
  const returnUrl = safeReturnUrl(params.get('returnUrl'));
  const navigate = useNavigate();
  const status = useAuthStatus();
  const login = useLogin();

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  // Off by default: a persistent cookie outlives the browser session (shared or lost devices).
  const [rememberMe, setRememberMe] = useState(false);

  if (status.isPending) return <LoadingIndicator fill className="min-h-dvh bg-page" />;
  if (status.data?.setupRequired) return <Navigate to="/setup" replace />;
  if (status.data?.authenticated) return <Navigate to={returnUrl} replace />;

  const method = status.data?.authenticationMethod;
  if (method === 'None') {
    // None only trusts requests that name Dupearr by an IP address or a local host name (a
    // DNS-rebinding guard, docs/API.md). Reaching this page means this address is not one of them:
    // "Continue" would only come straight back here.
    return <NoneAuthUntrustedHost checking={status.isFetching} onRetry={() => void status.refetch()} />;
  }
  if (method === 'External') {
    // External trusts only requests relayed by the authenticating reverse proxy. Reaching this page
    // means this request was not (or named a host that is not allowed): "Continue" would only come
    // straight back here.
    return <ExternalAuthUntrusted checking={status.isFetching} onRetry={() => void status.refetch()} />;
  }
  if (method && method !== 'Forms') {
    return (
      <AuthLayout title="Sign in">
        <Alert kind="info" title={`${method} authentication`}>
          This instance does not use the login page.
        </Alert>
        <Button
          variant="primary"
          fullWidth
          className="mt-4"
          onClick={() => window.location.assign(withUrlBase(returnUrl))}
        >
          Continue to Dupearr
        </Button>
      </AuthLayout>
    );
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!username || !password) return;
    login.mutate(
      { username: username.trim(), password, rememberMe },
      { onSuccess: () => navigate(returnUrl, { replace: true }) },
    );
  };

  const error = login.error
    ? isApiError(login.error) && login.error.status === 401
      ? 'Incorrect username or password.'
      : errorMessage(login.error)
    : null;

  return (
    <AuthLayout title="Sign in" subtitle="Sign in to continue to Dupearr.">
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {error && <Alert kind="error">{error}</Alert>}
        {status.isError && (
          <Alert kind="warning">Unable to reach Dupearr: {errorMessage(status.error)}</Alert>
        )}
        <AuthField label="Username" htmlFor="login-username">
          <TextInput
            id="login-username"
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </AuthField>
        <AuthField label="Password" htmlFor="login-password">
          <PasswordInput
            id="login-password"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </AuthField>
        <Checkbox checked={rememberMe} onChange={setRememberMe} label="Remember me" />
        <Button
          type="submit"
          variant="primary"
          size="lg"
          icon={LogIn}
          fullWidth
          loading={login.isPending}
          disabled={!username || !password}
        >
          Sign in
        </Button>
      </form>
    </AuthLayout>
  );
}

/**
 * Authentication "External", but the server did not trust this request: External only trusts
 * requests relayed by the reverse proxy that does the login (TrustedProxies / AllowedHosts, or —
 * without them — requests that name Dupearr by an IP address or a local host name).
 */
function ExternalAuthUntrusted({ checking, onRetry }: { checking: boolean; onRetry: () => void }) {
  return (
    <AuthLayout title="Open Dupearr through your reverse proxy">
      <div className="flex flex-col gap-4 text-sm">
        <Alert kind="warning" title="External authentication">
          Dupearr relies on a reverse proxy (Authelia, Authentik, oauth2-proxy, …) to sign you in, but this request did
          not come through it — or used a host name Dupearr does not trust.
        </Alert>
        <ul className="m-0 flex list-disc flex-col gap-1 pl-5 text-fg">
          <li>Open Dupearr through the address of your reverse proxy.</li>
          <li>
            If you are the administrator: set <code>DUPEARR__AUTH__TRUSTEDPROXIES</code> to the proxy&apos;s IP address
            (and <code>DUPEARR__AUTH__ALLOWEDHOSTS</code> to Dupearr&apos;s host name behind it) and restart Dupearr.
            Without them, External only trusts requests that open Dupearr by its IP address or a local host name.
          </li>
          <li>
            Or switch to <strong>Forms</strong> (Dupearr&apos;s own login page) in <code>config.xml</code> or with{' '}
            <code>DUPEARR__AUTH__METHOD</code>.
          </li>
        </ul>
        <Button variant="primary" fullWidth icon={RefreshCw} loading={checking} onClick={onRetry}>
          Check Again
        </Button>
      </div>
    </AuthLayout>
  );
}

/**
 * Authentication "None", but the server did not trust this request: None only trusts requests that
 * name Dupearr by an IP address, localhost or a local host name (single-label names like "tower",
 * or names under .local, .lan, .home.arpa, .internal, …), so that a DNS-rebinding page cannot drive
 * the API. A public host name (e.g. a reverse-proxy domain) needs a real authentication method.
 */
function NoneAuthUntrustedHost({ checking, onRetry }: { checking: boolean; onRetry: () => void }) {
  const { host, port, protocol } = window.location;
  const example = (name: string) => `${protocol}//${name}${port ? `:${port}` : ''}`;
  return (
    <AuthLayout title="Open Dupearr by its local address">
      <div className="flex flex-col gap-4 text-sm">
        <Alert kind="warning" title="Authentication is off (None)">
          Without authentication, Dupearr only accepts requests to its IP address, <code>localhost</code> or a local
          host name — this protects it against malicious websites (DNS rebinding).
          {host && (
            <>
              {' '}
              <strong className="break-all">{host}</strong> is not one of them.
            </>
          )}
        </Alert>
        <div>
          <p className="m-0 font-semibold text-fg-strong">To continue, either:</p>
          <ul className="m-0 mt-1 flex list-disc flex-col gap-1 pl-5 text-fg">
            <li>
              open Dupearr by its IP address or local host name, e.g. <code>{example('192.168.1.10')}</code>,{' '}
              <code>{example('localhost')}</code> or <code>{example('tower')}</code> (names ending in <code>.local</code>,{' '}
              <code>.lan</code>, <code>.home.arpa</code> or <code>.internal</code> work too); or
            </li>
            <li>
              turn on authentication — needed for a public host name such as a reverse-proxy domain:{' '}
              <strong>Forms</strong> (a login page) or <strong>External</strong> (only behind a reverse proxy that does its
              own login). Change it under Settings → General when opened by its IP address,
              or set <code>AuthenticationMethod</code> in <code>config.xml</code> (or the <code>DUPEARR__AUTH__METHOD</code>{' '}
              environment variable) and restart Dupearr.
            </li>
          </ul>
        </div>
        <Button variant="primary" fullWidth icon={RefreshCw} loading={checking} onClick={onRetry}>
          Check Again
        </Button>
      </div>
    </AuthLayout>
  );
}
