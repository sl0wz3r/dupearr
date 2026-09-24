import { Eye, KeyRound, RefreshCw } from 'lucide-react';
import { useId, useRef, useState, type ReactNode } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { useRegenerateApiKey, useRevealApiKey } from '@/api/hooks/useSettings';
import type { HostConfig } from '@/api/types';
import { Button, ConfirmDialog, CopyButton, FormGroup, PasswordInput, TextInput, useToast } from '@/components/ui';
import { MASKED_SECRET } from '@/lib/constants';

/**
 * Whether the account's password guards the master credentials (a Forms account exists): the API
 * key is then masked for browser sessions and revealed or regenerated only with the current
 * password (docs/API.md → /config/host).
 */
export function passwordProtected(server: Pick<HostConfig, 'authenticationMethod' | 'password'>): boolean {
  return server.authenticationMethod === 'Forms' && !!server.password;
}

export interface PasswordPromptProps {
  open: boolean;
  title: string;
  message: ReactNode;
  confirmLabel: string;
  kind?: 'danger' | 'primary';
  needPassword: boolean;
  loading: boolean;
  error: unknown;
  onConfirm: (password: string) => void;
  onCancel: () => void;
}

/** A confirmation that asks for the current password (when needPassword). */
export function PasswordPrompt({ open, title, message, confirmLabel, kind = 'primary', needPassword, loading, error, onConfirm, onCancel }: PasswordPromptProps) {
  const [password, setPassword] = useState('');
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const fieldErrors = isApiError(error) ? error.errorsFor('currentPassword') : [];
  const general = error && fieldErrors.length === 0 ? errorMessage(error) : null;
  const close = () => {
    setPassword('');
    onCancel();
  };
  return (
    <ConfirmDialog
      open={open}
      title={title}
      message={message}
      confirmLabel={confirmLabel}
      kind={kind}
      loading={loading}
      onConfirm={() => {
        if (needPassword && !password) return;
        onConfirm(password);
        setPassword('');
      }}
      onCancel={close}
      initialFocusRef={needPassword ? inputRef : undefined}
    >
      {needPassword && (
        <FormGroup label="Current Password" htmlFor={inputId} errors={fieldErrors}>
          <PasswordInput
            ref={inputRef}
            id={inputId}
            autoComplete="current-password"
            value={password}
            invalid={fieldErrors.length > 0}
            onChange={(e) => setPassword(e.target.value)}
          />
        </FormGroup>
      )}
      {general && <p className="mb-0 text-sm text-danger">{general}</p>}
    </ConfirmDialog>
  );
}

export interface ApiKeyFieldProps {
  id: string;
  server: HostConfig;
  /** The key is set by DUPEARR__AUTH__APIKEY (cannot be regenerated here). */
  envOverridden: boolean;
  labelSuffix?: ReactNode;
}

/**
 * Settings → General "API Key": masked unless revealed with the current password, so an open tab or
 * a stolen session does not hand out the master credential. Regenerating needs the password too.
 */
export function ApiKeyField({ id, server, envOverridden, labelSuffix }: ApiKeyFieldProps) {
  const toast = useToast();
  const reveal = useRevealApiKey();
  const regenerate = useRegenerateApiKey();
  const [revealed, setRevealed] = useState<string | null>(null);
  const [prompt, setPrompt] = useState<'reveal' | 'regenerate' | null>(null);
  const needPassword = passwordProtected(server);
  const shown = revealed ?? (server.apiKey && server.apiKey !== MASKED_SECRET ? server.apiKey : null);

  const doReveal = (password: string) => {
    reveal.mutate(needPassword ? password : undefined, {
      onSuccess: (res) => {
        setRevealed(res.apiKey);
        setPrompt(null);
      },
    });
  };
  const doRegenerate = (password: string) => {
    regenerate.mutate(needPassword ? password : undefined, {
      onSuccess: (cfg) => {
        setRevealed(cfg?.apiKey && cfg.apiKey !== MASKED_SECRET ? cfg.apiKey : null);
        setPrompt(null);
        toast.success('API key regenerated', 'Update it in every script and application that uses it.');
      },
    });
  };

  return (
    <FormGroup
      label="API Key"
      htmlFor={id}
      labelSuffix={labelSuffix}
      helpText="For scripts and other applications (X-Api-Key header). The web UI does not use it, and webhooks use the webhook token (Settings → Connections)."
    >
      <div className="flex flex-wrap items-center gap-2">
        <TextInput
          id={id}
          readOnly
          value={shown ?? MASKED_SECRET}
          className="font-mono"
          spellCheck={false}
          aria-label={shown ? 'API key' : 'API key (hidden)'}
          suffix={shown ? <CopyButton value={shown} size="sm" label="Copy API key" /> : undefined}
          wrapperClassName="min-w-48 flex-1"
        />
        {!shown && (
          <Button icon={Eye} onClick={() => (needPassword ? setPrompt('reveal') : doReveal(''))} loading={reveal.isPending && prompt === null}>
            Show Key
          </Button>
        )}
        <Button
          variant="danger"
          icon={RefreshCw}
          onClick={() => setPrompt('regenerate')}
          loading={regenerate.isPending && prompt === null}
          disabled={envOverridden}
          title={envOverridden ? 'The API key is set by an environment variable' : undefined}
        >
          Regenerate
        </Button>
      </div>
      <PasswordPrompt
        open={prompt === 'reveal'}
        title="Show API Key"
        message="Enter your current password to show the API key."
        confirmLabel="Show Key"
        needPassword={needPassword}
        loading={reveal.isPending}
        error={reveal.error}
        onConfirm={doReveal}
        onCancel={() => {
          reveal.reset();
          setPrompt(null);
        }}
      />
      <PasswordPrompt
        open={prompt === 'regenerate'}
        title="Regenerate API Key"
        kind="danger"
        message={
          <>
            <p className="mt-0">
              The current key stops working immediately, and every other session is signed out. Scripts and tools using
              it must be updated with the new key.
            </p>
            <p className="mb-0 flex items-center gap-1.5 text-muted">
              <KeyRound aria-hidden width={14} height={14} /> This browser session keeps working.
            </p>
          </>
        }
        confirmLabel="Regenerate"
        needPassword={needPassword}
        loading={regenerate.isPending}
        error={regenerate.error}
        onConfirm={doRegenerate}
        onCancel={() => {
          regenerate.reset();
          setPrompt(null);
        }}
      />
    </FormGroup>
  );
}
