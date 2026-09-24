import { ShieldCheck } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { errorMessage, isApiError, login } from '@/api/client';
import { useAuthSetup, useAuthStatus } from '@/api/hooks/useAuth';
import type { AuthSetupRequest, AuthenticationRequired } from '@/api/types';
import { Alert, Button, LoadingIndicator, PasswordInput, Select, TextInput } from '@/components/ui';
import { AUTH_METHOD_LABELS, AUTH_REQUIRED_LABELS } from '@/lib/constants';
import { AuthField, AuthLayout } from './AuthLayout';

type SetupMethod = AuthSetupRequest['authenticationMethod'];

const METHOD_OPTIONS: { value: SetupMethod; label: string }[] = [
  { value: 'Forms', label: AUTH_METHOD_LABELS.Forms },
];

const REQUIRED_OPTIONS: { value: AuthenticationRequired; label: string }[] = [
  { value: 'Enabled', label: AUTH_REQUIRED_LABELS.Enabled },
  { value: 'DisabledForLocalAddresses', label: AUTH_REQUIRED_LABELS.DisabledForLocalAddresses },
];

interface FieldErrors {
  setupCode?: string[];
  username?: string[];
  password?: string[];
  passwordConfirmation?: string[];
  authenticationMethod?: string[];
  authenticationRequired?: string[];
}

/** Client-side checks mirroring the server's (server errors still win). */
function validateSetup(form: AuthSetupRequest): FieldErrors {
  const errors: FieldErrors = {};
  if (!form.setupCode.trim()) errors.setupCode = ['Enter the setup code from Dupearr’s log'];
  if (!form.username.trim()) errors.username = ['Username is required'];
  if (!form.password) errors.password = ['Password is required'];
  if (form.password && form.password !== form.passwordConfirmation) {
    errors.passwordConfirmation = ['Passwords do not match'];
  }
  return errors;
}

/**
 * First-run authentication setup (POST /api/v1/auth/setup), shown while `setupRequired`
 * (like *arr v4's "Authentication Required"). Forms → signs in automatically afterwards.
 */
export default function SetupPage() {
  const status = useAuthStatus();
  const setup = useAuthSetup();
  const navigate = useNavigate();

  const [form, setForm] = useState<AuthSetupRequest>({
    authenticationMethod: 'Forms',
    authenticationRequired: 'Enabled',
    username: '',
    password: '',
    passwordConfirmation: '',
    setupCode: '',
  });
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [loginError, setLoginError] = useState<string | null>(null);
  const [signingIn, setSigningIn] = useState(false);

  if (status.isPending) return <LoadingIndicator fill className="min-h-dvh bg-page" />;
  if (status.data && !status.data.setupRequired && !setup.isSuccess) {
    return <Navigate to={status.data.authenticated ? '/' : '/login'} replace />;
  }

  const set = <K extends keyof AuthSetupRequest>(key: K, value: AuthSetupRequest[K]) => {
    setForm((f) => ({ ...f, [key]: value }));
    setClientErrors((e) => ({ ...e, [key]: undefined }));
    if (setup.isError) setup.reset(); // clear stale server errors once the user edits
  };

  // An environment variable forces the requirement: shown read-only, never offered as a choice
  // the save would discard (the server refuses a different value).
  const forcedRequired = status.data?.envForced?.authenticationRequired as AuthenticationRequired | undefined;
  const required: AuthenticationRequired = forcedRequired ?? form.authenticationRequired;

  const apiError = isApiError(setup.error) ? setup.error : null;
  // 403 is either a wrong/missing setup code (shown on that field) or a non-local client/host name.
  const codeRefused = apiError?.status === 403 && /setup code/i.test(apiError.message);
  const fieldErrors = (key: keyof FieldErrors): string[] => [
    ...(clientErrors[key] ?? []),
    ...(apiError?.errorsFor(key) ?? []),
    ...(key === 'setupCode' && codeRefused ? ['The setup code is wrong or has expired: copy the current one from Dupearr’s log'] : []),
  ];
  const generalError = setup.error
    ? codeRefused
      ? null
      : apiError?.status === 403
        ? 'Authentication can only be set up from a local network address, opening Dupearr by its local IP address or local host name.'
        : apiError?.isValidation
          ? null
          : errorMessage(setup.error)
    : null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errors = validateSetup(form);
    setClientErrors(errors);
    if (Object.keys(errors).length > 0) return;
    setLoginError(null);
    const body = { ...form, authenticationRequired: required, username: form.username.trim(), setupCode: form.setupCode.trim() };
    setup.mutate(body, {
      onSuccess: async () => {
        setSigningIn(true);
        try {
          await login({ username: body.username, password: body.password, rememberMe: false });
          navigate('/', { replace: true });
        } catch (err) {
          setLoginError(`Account created, but signing in failed: ${errorMessage(err)}`);
          setSigningIn(false);
        }
      },
    });
  };

  return (
    <AuthLayout
      title="Authentication Required"
      subtitle="Dupearr can delete files, so it requires authentication. Create the account used to sign in."
    >
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {generalError && <Alert kind="error">{generalError}</Alert>}
        {loginError && (
          <Alert
            kind="warning"
            actions={
              <Button size="sm" onClick={() => navigate('/login', { replace: true })}>
                Sign in
              </Button>
            }
          >
            {loginError}
          </Alert>
        )}

        <AuthField
          label="Setup Code"
          htmlFor="setup-code"
          errors={fieldErrors('setupCode')}
          help={
            <>
              Dupearr prints a one-time setup code in its log while setup is pending (Docker:{' '}
              <code>docker logs &lt;container&gt;</code>; Unraid: the container’s log). It proves you have access to the
              server.
            </>
          }
        >
          <TextInput
            id="setup-code"
            autoComplete="one-time-code"
            autoCapitalize="characters"
            spellCheck={false}
            autoFocus
            invalid={fieldErrors('setupCode').length > 0}
            value={form.setupCode}
            onChange={(e) => set('setupCode', e.target.value)}
          />
        </AuthField>

        <AuthField
          label="Authentication Method"
          htmlFor="setup-method"
          errors={fieldErrors('authenticationMethod')}
          help={
            form.authenticationMethod === 'Forms'
              ? 'A login page with an optional “remember me”.'
              : 'Your browser’s built-in sign-in prompt.'
          }
        >
          <Select
            id="setup-method"
            options={METHOD_OPTIONS}
            value={form.authenticationMethod}
            onChange={(v) => set('authenticationMethod', v)}
          />
        </AuthField>

        <AuthField
          label="Authentication Required"
          htmlFor="setup-required"
          errors={fieldErrors('authenticationRequired')}
          help={
            <>
              {required === 'DisabledForLocalAddresses'
                ? 'Devices on your local network can use Dupearr without signing in.'
                : 'Everyone must sign in, including local devices.'}
              {forcedRequired && (
                <>
                  {' '}
                  Set by the <code>DUPEARR__AUTH__REQUIRED</code> environment variable; change it there.
                </>
              )}
            </>
          }
        >
          <Select
            id="setup-required"
            options={REQUIRED_OPTIONS}
            value={required}
            disabled={!!forcedRequired}
            onChange={(v) => set('authenticationRequired', v)}
          />
        </AuthField>

        <AuthField label="Username" htmlFor="setup-username" errors={fieldErrors('username')}>
          <TextInput
            id="setup-username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            invalid={fieldErrors('username').length > 0}
            value={form.username}
            onChange={(e) => set('username', e.target.value)}
          />
        </AuthField>

        <AuthField label="Password" htmlFor="setup-password" errors={fieldErrors('password')}>
          <PasswordInput
            id="setup-password"
            autoComplete="new-password"
            invalid={fieldErrors('password').length > 0}
            value={form.password}
            onChange={(e) => set('password', e.target.value)}
          />
        </AuthField>

        <AuthField
          label="Password Confirmation"
          htmlFor="setup-password-confirmation"
          errors={fieldErrors('passwordConfirmation')}
        >
          <PasswordInput
            id="setup-password-confirmation"
            autoComplete="new-password"
            invalid={fieldErrors('passwordConfirmation').length > 0}
            value={form.passwordConfirmation}
            onChange={(e) => set('passwordConfirmation', e.target.value)}
          />
        </AuthField>

        <Button
          type="submit"
          variant="primary"
          size="lg"
          icon={ShieldCheck}
          fullWidth
          loading={setup.isPending || signingIn}
        >
          Save and Continue
        </Button>
      </form>
    </AuthLayout>
  );
}
