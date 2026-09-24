import { useQueryClient } from '@tanstack/react-query';
import { LogOut, Power } from 'lucide-react';
import { useEffect, useId, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { api, errorMessage, isApiError, withUrlBase } from '@/api/client';
import { useHostConfig, useRevokeSessions, useSettings, useUpdateHostConfig, useUpdateSettings } from '@/api/hooks/useSettings';
import { useRestart } from '@/api/hooks/useSystem';
import { queryKeys } from '@/api/queryKeys';
import type { AuthenticationMethod, AuthenticationRequired, HostConfig, LogLevel, Settings } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  SaveBar,
  SaveToolbarButton,
  SettingsSection,
  useSettingsForm,
} from '@/components/page';
import { hasErrors, mergeFieldErrors, summarizeSaveError, type FieldErrors } from '@/components/settings/connections/connectionUtils';
import { ApiKeyField, passwordProtected } from '@/components/settings/connections/ApiKeyField';
import {
  credentialChanged,
  envOverriddenFields,
  envVarName,
  hostConfigPayload,
  MAX_LOG_SIZE_LIMIT_MB,
  overlayEdits,
  passwordChanged,
  stripTransient,
  validateHostConfig,
  type HostField,
} from '@/components/settings/connections/hostConfigForm';
import { SecretInput } from '@/components/settings/connections/SecretInput';
import {
  SETTINGS_NUMBER_LIMITS,
  numberSettingProblem,
  settingsFieldLabel,
} from '@/components/settings/rules/mediaManagement';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  FormGroup,
  LoadingIndicator,
  NumberInput,
  PasswordInput,
  Select,
  Spinner,
  Switch,
  TextInput,
  useToast,
} from '@/components/ui';
import { AUTH_METHOD_LABELS, AUTH_REQUIRED_LABELS, LOG_LEVEL_LABELS, MASKED_SECRET, toOptions } from '@/lib/constants';

const HOST_FORM_FIELDS: readonly string[] = [
  'bindAddress',
  'port',
  'urlBase',
  'instanceName',
  'enableSsl',
  'sslPort',
  'sslCertPath',
  'sslKeyPath',
  'authenticationMethod',
  'authenticationRequired',
  'username',
  'password',
  'passwordConfirmation',
  'currentPassword',
  'logLevel',
  'logSizeLimit',
  'backupIntervalDays',
  'backupRetentionDays',
];

/** Badge shown next to fields forced by DUPEARR__ environment variables. */
function EnvBadge({ field }: { field: HostField }) {
  const name = envVarName(field);
  return (
    <Badge kind="info" outline title={name ? `Set by ${name}` : 'Set by an environment variable'}>
      Set by environment variable
    </Badge>
  );
}

/** Polls /ping after a restart request and reloads once Dupearr is back (2 min timeout). */
function useRestartWatcher(active: boolean): boolean {
  const [timedOut, setTimedOut] = useState(false);
  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    const ctrl = new AbortController();
    const started = Date.now();
    let sawDown = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const ping = async (): Promise<boolean> => {
      try {
        const res = await fetch(withUrlBase('/ping'), { cache: 'no-store', signal: ctrl.signal, credentials: 'same-origin' });
        return res.ok;
      } catch {
        return false;
      }
    };

    const tick = async () => {
      if (cancelled) return;
      const up = await ping();
      if (cancelled) return;
      if (!up) sawDown = true;
      const elapsed = Date.now() - started;
      // Reload once the server went down and came back (or is clearly up again after 15s).
      if (up && (sawDown || elapsed > 15_000)) {
        window.location.reload();
        return;
      }
      if (elapsed > 120_000) {
        setTimedOut(true);
        return;
      }
      timer = setTimeout(() => void tick(), 2000);
    };
    timer = setTimeout(() => void tick(), 2000);
    return () => {
      cancelled = true;
      ctrl.abort();
      clearTimeout(timer);
    };
  }, [active]);
  return timedOut;
}

/**
 * Settings → General at `/settings/general`: host (bind address, port, URL base, instance name,
 * SSL), security (authentication, credentials, API key), logging and backups. Fields forced by
 * environment variables are read-only; a restart prompt appears when a saved change needs one.
 */
export default function GeneralPage() {
  const toast = useToast();
  const qc = useQueryClient();
  const idPrefix = useId();
  const id = (name: string) => `${idPrefix}-${name}`;

  const host = useHostConfig();
  const settings = useSettings();
  const hostForm = useSettingsForm<HostConfig>(host.data);
  const settingsForm = useSettingsForm<Settings>(settings.data);
  const updateHost = useUpdateHostConfig();
  const updateSettings = useUpdateSettings();
  const revokeSessions = useRevokeSessions();
  const restart = useRestart();
  /** Write-only: confirms credential changes (not part of the form, never cached). */
  const [currentPassword, setCurrentPassword] = useState('');
  const [keepApiKey, setKeepApiKey] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState(false);

  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [restartRequired, setRestartRequired] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);
  const [restarting, setRestarting] = useState(false);
  /** Properties the user changed since the last save/discard: only these are written back. */
  const [editedHost, setEditedHost] = useState<ReadonlySet<keyof HostConfig>>(() => new Set());
  const [editedSettings, setEditedSettings] = useState<ReadonlySet<keyof Settings>>(() => new Set());
  const restartTimedOut = useRestartWatcher(restarting);

  const server = host.data;
  const v = hostForm.values;
  const s = settingsForm.values;
  const env = envOverriddenFields(server?.envOverrides);
  const dirty = hostForm.dirty || settingsForm.dirty;

  // Settings saved from this page can be refused over a field Media Management edits (the PUT
  // carries the whole object): such messages are labelled with that field's name.
  const summary = summarizeSaveError(saveError, HOST_FORM_FIELDS, settingsFieldLabel);
  const errors = mergeFieldErrors(clientErrors, summary.fields);
  const errorsFor = (k: string) => errors[k];

  const setHost = <K extends keyof HostConfig>(key: K, value: HostConfig[K]) => {
    hostForm.setField(key, value);
    setEditedHost((prev) => (prev.has(key) ? prev : new Set([...prev, key])));
    setClientErrors((e) => (e[key as string]?.length ? { ...e, [key]: [] } : e));
    setSaveError(null);
  };
  const setSetting = <K extends keyof Settings>(key: K, value: Settings[K]) => {
    settingsForm.setField(key, value);
    setEditedSettings((prev) => (prev.has(key) ? prev : new Set([...prev, key])));
    setClientErrors((e) => (e[key as string]?.length ? { ...e, [key]: [] } : e));
    setSaveError(null);
  };

  // The server asks for the current password when a credential changes and a Forms account exists.
  const needCurrentPassword = !!(hostForm.dirty && v && server && passwordProtected(server) && credentialChanged(v, server));

  const validate = (): boolean => {
    let errs: FieldErrors = {};
    if (hostForm.dirty && v && server) errs = validateHostConfig(v, server);
    if (needCurrentPassword && !currentPassword) {
      errs.currentPassword = ['Enter your current password to change the username, password or authentication settings'];
    }
    if (settingsForm.dirty && s) {
      // The server's limits: 0 turns scheduled backups off / keeps every backup.
      for (const field of ['backupIntervalDays', 'backupRetentionDays'] as const) {
        const problem = numberSettingProblem(field, s[field]);
        if (problem) errs[field] = [problem];
      }
    }
    setClientErrors(errs);
    return !hasErrors(errs);
  };

  const save = async () => {
    if (!dirty || saving || !validate()) return;
    setSaveError(null);
    setSaving(true);
    try {
      // Both endpoints replace the whole object: write the user's edits over a fresh read so
      // changes made elsewhere meanwhile (e.g. Dry Run or Mode on Media Management, another
      // tab) are never reverted by this page's older snapshot.
      if (hostForm.dirty && v && server) {
        const fresh = await qc.fetchQuery({
          queryKey: queryKeys.config.host,
          queryFn: ({ signal }) => api.get<HostConfig>('/config/host', undefined, { signal }),
          staleTime: 0,
        });
        const payload = hostConfigPayload(overlayEdits(fresh, v, editedHost), fresh);
        if (needCurrentPassword) payload.currentPassword = currentPassword;
        if (passwordChanged(v, server) && keepApiKey) payload.keepApiKey = true;
        const response = await updateHost.mutateAsync(payload);
        const keyReplaced = !!response && passwordChanged(v, server) && !keepApiKey && !env.has('apiKey');
        let saved: HostConfig;
        if (response) {
          saved = response;
        } else {
          // Tolerate an empty 200 body: adopt what was sent (password masked) and re-read the config.
          saved = { ...payload, password: payload.password ? MASKED_SECRET : '' };
          void qc.invalidateQueries({ queryKey: queryKeys.config.host });
        }
        if (saved.restartRequired) setRestartRequired(true);
        hostForm.markSaved(stripTransient(saved, fresh));
        setEditedHost(new Set());
        setCurrentPassword('');
        setKeepApiKey(false);
        if (keyReplaced) {
          toast.info('API key replaced', 'Changing the password also replaced the API key: update your scripts (Security → API Key).');
        }
      }
      if (settingsForm.dirty && s) {
        const fresh = await qc.fetchQuery({
          queryKey: queryKeys.config.settings,
          queryFn: ({ signal }) => api.get<Settings>('/config/settings', undefined, { signal }),
          staleTime: 0,
        });
        const body = overlayEdits(fresh, s, editedSettings);
        const saved = (await updateSettings.mutateAsync(body)) ?? body;
        settingsForm.markSaved(saved);
        setEditedSettings(new Set());
      }
      toast.success('Settings saved');
    } catch (e) {
      setSaveError(e);
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    hostForm.reset();
    settingsForm.reset();
    setEditedHost(new Set());
    setEditedSettings(new Set());
    setClientErrors({});
    setSaveError(null);
    setCurrentPassword('');
    setKeepApiKey(false);
  };

  const doRevokeSessions = () => {
    setConfirmRevoke(false);
    revokeSessions.mutate(undefined, {
      onSuccess: () => toast.success('Signed out everywhere else', 'Every other browser and device must sign in again.'),
      onError: (e) => toast.error('Unable to sign out the other sessions', errorMessage(e)),
    });
  };

  const doRestart = () => {
    setConfirmRestart(false);
    restart.mutate(undefined, {
      onSuccess: () => setRestarting(true),
      // The process may exit before the response arrives: a network error means it is restarting.
      onError: (e) => {
        if (isApiError(e) && e.status === 0) setRestarting(true);
        else toast.error('Unable to restart Dupearr', errorMessage(e));
      },
    });
  };

  const loading = host.isPending || settings.isPending;

  let body: ReactNode;
  if (loading) {
    body = <LoadingIndicator message="Loading settings…" />;
  } else if (host.isError || !server || !v) {
    body = (
      <Alert
        kind="error"
        title="Unable to load host settings"
        actions={
          <Button size="sm" onClick={() => void host.refetch()}>
            Retry
          </Button>
        }
      >
        {errorMessage(host.error)}
      </Alert>
    );
  } else {
    const method = v.authenticationMethod;
    const methodOptions = toOptions(AUTH_METHOD_LABELS, ['Forms', 'External'] as AuthenticationMethod[]);
    // "None" can only be set via config.xml / env (docs/DECISIONS.md D1); show it when it is active.
    if (server.authenticationMethod === 'None' || method === 'None') {
      methodOptions.push({ value: 'None', label: AUTH_METHOD_LABELS.None });
    }
    const pwChanged = passwordChanged(v, server);

    const envProps = (field: HostField) =>
      env.has(field) ? { disabled: true, title: `Set by ${envVarName(field) ?? 'an environment variable'}` } : {};
    const envSuffix = (field: HostField) => (env.has(field) ? <EnvBadge field={field} /> : undefined);

    body = (
      <>
        {restarting ? (
          <Alert kind={restartTimedOut ? 'warning' : 'info'} title={restartTimedOut ? 'Dupearr did not come back yet' : 'Restarting Dupearr…'} className="mb-5">
            {restartTimedOut ? (
              'If you changed the port, URL base or SSL settings, open Dupearr at its new address.'
            ) : (
              <span className="flex items-center gap-2">
                <Spinner size="sm" label="Restarting" /> This page reloads automatically when Dupearr is back.
              </span>
            )}
          </Alert>
        ) : (
          restartRequired && (
            <Alert
              kind="warning"
              title="Restart required"
              className="mb-5"
              actions={
                <Button variant="warning" size="sm" icon={Power} onClick={() => setConfirmRestart(true)} loading={restart.isPending}>
                  Restart now
                </Button>
              }
            >
              Some changes (port, bind address, URL base or SSL) take effect after Dupearr restarts.
            </Alert>
          )
        )}

        <SettingsSection title="Host">
          <FormGroup
            label="Bind Address"
            htmlFor={id('bindAddress')}
            advanced
            labelSuffix={envSuffix('bindAddress')}
            errors={errorsFor('bindAddress')}
            helpText="IP address to listen on; * for all interfaces."
          >
            <TextInput
              id={id('bindAddress')}
              value={v.bindAddress ?? ''}
              onChange={(e) => setHost('bindAddress', e.target.value)}
              invalid={!!errorsFor('bindAddress')?.length}
              {...envProps('bindAddress')}
            />
          </FormGroup>
          <FormGroup
            label="Port Number"
            htmlFor={id('port')}
            labelSuffix={envSuffix('port')}
            errors={errorsFor('port')}
            width="sm"
          >
            <NumberInput
              id={id('port')}
              value={v.port}
              min={1}
              max={65535}
              onChange={(n) => setHost('port', n ?? v.port)}
              invalid={!!errorsFor('port')?.length}
              {...envProps('port')}
            />
          </FormGroup>
          <FormGroup
            label="URL Base"
            htmlFor={id('urlBase')}
            labelSuffix={envSuffix('urlBase')}
            errors={errorsFor('urlBase')}
            helpText="For reverse proxy support, e.g. /dupearr. Leave empty to serve at the root."
          >
            <TextInput
              id={id('urlBase')}
              value={v.urlBase ?? ''}
              placeholder="/dupearr"
              onChange={(e) => setHost('urlBase', e.target.value)}
              invalid={!!errorsFor('urlBase')?.length}
              spellCheck={false}
              {...envProps('urlBase')}
            />
          </FormGroup>
          <FormGroup
            label="Instance Name"
            htmlFor={id('instanceName')}
            labelSuffix={envSuffix('instanceName')}
            errors={errorsFor('instanceName')}
            helpText="Shown in the browser tab and in notifications."
          >
            <TextInput
              id={id('instanceName')}
              value={v.instanceName ?? ''}
              maxLength={100}
              onChange={(e) => setHost('instanceName', e.target.value)}
              invalid={!!errorsFor('instanceName')?.length}
              {...envProps('instanceName')}
            />
          </FormGroup>

          <FormGroup label="Enable SSL" htmlFor={id('enableSsl')} advanced labelSuffix={envSuffix('enableSsl')}>
            <Switch
              id={id('enableSsl')}
              checked={!!v.enableSsl}
              onChange={(c) => setHost('enableSsl', c)}
              aria-label="Enable SSL"
              disabled={env.has('enableSsl')}
            />
          </FormGroup>
          {v.enableSsl && (
            <>
              <FormGroup
                label="SSL Port"
                htmlFor={id('sslPort')}
                advanced
                labelSuffix={envSuffix('sslPort')}
                errors={errorsFor('sslPort')}
                width="sm"
              >
                <NumberInput
                  id={id('sslPort')}
                  value={v.sslPort}
                  min={1}
                  max={65535}
                  onChange={(n) => setHost('sslPort', n ?? v.sslPort)}
                  invalid={!!errorsFor('sslPort')?.length}
                  {...envProps('sslPort')}
                />
              </FormGroup>
              <FormGroup
                label="SSL Certificate Path"
                htmlFor={id('sslCertPath')}
                advanced
                labelSuffix={envSuffix('sslCertPath')}
                errors={errorsFor('sslCertPath')}
                helpText="PEM certificate (chain) file, e.g. /config/ssl/cert.pem"
              >
                <TextInput
                  id={id('sslCertPath')}
                  value={v.sslCertPath ?? ''}
                  onChange={(e) => setHost('sslCertPath', e.target.value)}
                  invalid={!!errorsFor('sslCertPath')?.length}
                  spellCheck={false}
                  {...envProps('sslCertPath')}
                />
              </FormGroup>
              <FormGroup
                label="SSL Key Path"
                htmlFor={id('sslKeyPath')}
                advanced
                labelSuffix={envSuffix('sslKeyPath')}
                errors={errorsFor('sslKeyPath')}
                helpText="PEM private key file, e.g. /config/ssl/key.pem"
              >
                <TextInput
                  id={id('sslKeyPath')}
                  value={v.sslKeyPath ?? ''}
                  onChange={(e) => setHost('sslKeyPath', e.target.value)}
                  invalid={!!errorsFor('sslKeyPath')?.length}
                  spellCheck={false}
                  {...envProps('sslKeyPath')}
                />
              </FormGroup>
            </>
          )}
        </SettingsSection>

        <SettingsSection title="Security">
          <FormGroup
            label="Authentication"
            htmlFor={id('authenticationMethod')}
            labelSuffix={envSuffix('authenticationMethod')}
            errors={errorsFor('authenticationMethod')}
            helpText="Forms shows Dupearr's login page (recommended)."
          >
            <Select
              id={id('authenticationMethod')}
              options={methodOptions}
              value={method}
              onChange={(m) => setHost('authenticationMethod', m)}
              {...envProps('authenticationMethod')}
            />
          </FormGroup>
          {method === 'None' && (
            <Alert kind="error" title="Authentication is disabled" className="mb-3">
              Anyone who can reach Dupearr can see your library and delete media. “None” can only be set in
              config.xml or with DUPEARR__AUTH__METHOD — choose Forms to require a login.
            </Alert>
          )}
          {method === 'External' && (
            <Alert kind="warning" title="External authentication" className="mb-3">
              Dupearr accepts every request and relies on a reverse proxy (e.g. Authelia, Authentik, oauth2-proxy)
              to authenticate users — including API calls. Only use this when Dupearr is reachable exclusively
              through that proxy. Webhooks still need the webhook token, and scripts send the API key.
            </Alert>
          )}
          {method !== 'None' && (
            <FormGroup
              label="Authentication Required"
              htmlFor={id('authenticationRequired')}
              labelSuffix={envSuffix('authenticationRequired')}
              errors={errorsFor('authenticationRequired')}
              helpText="Local addresses (LAN) can skip the login when set to “Disabled for Local Addresses”."
            >
              <Select
                id={id('authenticationRequired')}
                options={toOptions(AUTH_REQUIRED_LABELS)}
                value={v.authenticationRequired}
                onChange={(r: AuthenticationRequired) => setHost('authenticationRequired', r)}
                {...envProps('authenticationRequired')}
              />
            </FormGroup>
          )}
          {method === 'Forms' && (
            <>
              <FormGroup label="Username" htmlFor={id('username')} errors={errorsFor('username')}>
                <TextInput
                  id={id('username')}
                  value={v.username ?? ''}
                  autoComplete="username"
                  onChange={(e) => setHost('username', e.target.value)}
                  invalid={!!errorsFor('username')?.length}
                />
              </FormGroup>
              <FormGroup
                label="Password"
                htmlFor={id('password')}
                errors={errorsFor('password')}
                helpText={server.password ? 'Leave unchanged to keep the current password.' : undefined}
              >
                <SecretInput
                  id={id('password')}
                  value={v.password ?? ''}
                  autoComplete="new-password"
                  onChange={(p) => setHost('password', p)}
                  invalid={!!errorsFor('password')?.length}
                />
              </FormGroup>
              {pwChanged && (
                <FormGroup label="Password Confirmation" htmlFor={id('passwordConfirmation')} errors={errorsFor('passwordConfirmation')}>
                  <PasswordInput
                    id={id('passwordConfirmation')}
                    value={v.passwordConfirmation ?? ''}
                    autoComplete="new-password"
                    onChange={(e) => setHost('passwordConfirmation', e.target.value)}
                    invalid={!!errorsFor('passwordConfirmation')?.length}
                  />
                </FormGroup>
              )}
              {pwChanged && passwordProtected(server) && !env.has('apiKey') && (
                <div className="mb-4">
                  <Checkbox
                    checked={keepApiKey}
                    onChange={setKeepApiKey}
                    label="Keep the current API key"
                    description="By default a password change also replaces the API key, so a key read by whoever knew the old password stops working."
                  />
                </div>
              )}
            </>
          )}
          {needCurrentPassword && (
            <FormGroup
              label="Current Password"
              htmlFor={id('currentPassword')}
              errors={errorsFor('currentPassword')}
              helpText="Required to change the username, password or authentication settings."
            >
              <PasswordInput
                id={id('currentPassword')}
                value={currentPassword}
                autoComplete="current-password"
                onChange={(e) => {
                  setCurrentPassword(e.target.value);
                  setClientErrors((er) => (er.currentPassword?.length ? { ...er, currentPassword: [] } : er));
                  setSaveError(null);
                }}
                invalid={!!errorsFor('currentPassword')?.length}
              />
            </FormGroup>
          )}
          <ApiKeyField id={id('apiKey')} server={server} envOverridden={env.has('apiKey')} labelSuffix={envSuffix('apiKey')} />
          {passwordProtected(server) && (
            <FormGroup
              label="Sessions"
              helpText="Signs out every other browser and device (this one stays signed in). To also lock out scripts, regenerate the API key."
            >
              <Button icon={LogOut} onClick={() => setConfirmRevoke(true)} loading={revokeSessions.isPending}>
                Log out all other sessions
              </Button>
            </FormGroup>
          )}
        </SettingsSection>

        <SettingsSection title="Logging">
          <FormGroup
            label="Log Level"
            htmlFor={id('logLevel')}
            labelSuffix={envSuffix('logLevel')}
            errors={errorsFor('logLevel')}
            helpText="Trace and Debug are very verbose; use them only temporarily while troubleshooting."
          >
            <Select
              id={id('logLevel')}
              options={toOptions(LOG_LEVEL_LABELS)}
              value={v.logLevel}
              onChange={(l: LogLevel) => setHost('logLevel', l)}
              {...envProps('logLevel')}
            />
          </FormGroup>
          <FormGroup
            label="Log Size Limit"
            htmlFor={id('logSizeLimit')}
            advanced
            labelSuffix={envSuffix('logSizeLimit')}
            errors={errorsFor('logSizeLimit')}
            helpText="Maximum size of one log file before it is rotated."
            width="sm"
          >
            <NumberInput
              id={id('logSizeLimit')}
              value={v.logSizeLimit}
              min={1}
              max={MAX_LOG_SIZE_LIMIT_MB}
              unit="MB"
              onChange={(n) => setHost('logSizeLimit', n ?? v.logSizeLimit)}
              invalid={!!errorsFor('logSizeLimit')?.length}
              {...envProps('logSizeLimit')}
            />
          </FormGroup>
        </SettingsSection>

        <SettingsSection
          title="Backups"
          description={
            <>
              Scheduled backups of config.xml and the database. Manage them under{' '}
              <Link to="/system/backup" className="text-accent-soft underline">
                System → Backup
              </Link>
              .
            </>
          }
        >
          {settings.isError || !s ? (
            <Alert
              kind="error"
              title="Unable to load backup settings"
              actions={
                <Button size="sm" onClick={() => void settings.refetch()}>
                  Retry
                </Button>
              }
            >
              {errorMessage(settings.error)}
            </Alert>
          ) : (
            <>
              <FormGroup
                label="Backup Interval"
                htmlFor={id('backupIntervalDays')}
                errors={errorsFor('backupIntervalDays')}
                helpText={
                  s.backupIntervalDays > 0
                    ? `A scheduled backup is created every ${s.backupIntervalDays} day${s.backupIntervalDays === 1 ? '' : 's'}. 0 turns scheduled backups off.`
                    : 'Scheduled backups are off (0) — create backups manually under System → Backup.'
                }
                warning={s.backupIntervalDays === 0 ? 'Dupearr is not backed up automatically.' : undefined}
                width="sm"
              >
                <NumberInput
                  id={id('backupIntervalDays')}
                  value={s.backupIntervalDays}
                  min={SETTINGS_NUMBER_LIMITS.backupIntervalDays.min}
                  max={SETTINGS_NUMBER_LIMITS.backupIntervalDays.max}
                  unit="days"
                  onChange={(n) => setSetting('backupIntervalDays', n ?? s.backupIntervalDays)}
                  invalid={!!errorsFor('backupIntervalDays')?.length}
                />
              </FormGroup>
              <FormGroup
                label="Backup Retention"
                htmlFor={id('backupRetentionDays')}
                errors={errorsFor('backupRetentionDays')}
                helpText={
                  s.backupRetentionDays > 0
                    ? 'Scheduled backups older than this are deleted automatically. 0 keeps them forever.'
                    : 'Keep forever (0): scheduled backups are never deleted automatically.'
                }
                width="sm"
              >
                <NumberInput
                  id={id('backupRetentionDays')}
                  value={s.backupRetentionDays}
                  min={SETTINGS_NUMBER_LIMITS.backupRetentionDays.min}
                  max={SETTINGS_NUMBER_LIMITS.backupRetentionDays.max}
                  unit="days"
                  onChange={(n) => setSetting('backupRetentionDays', n ?? s.backupRetentionDays)}
                  invalid={!!errorsFor('backupRetentionDays')?.length}
                />
              </FormGroup>
            </>
          )}
        </SettingsSection>
      </>
    );
  }

  return (
    <PageContent title="General">
      <PageToolbar>
        <PageToolbarSection>
          <SaveToolbarButton dirty={dirty} saving={saving} onSave={() => void save()} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="General" />
        {body}
        <SaveBar
          dirty={dirty}
          saving={saving}
          onSave={() => void save()}
          onReset={reset}
          error={
            summary.messages.length > 0 || hasErrors(clientErrors) ? (
              <>
                {summary.messages.length > 0
                  ? summary.messages.map((m, i) => <div key={i}>{m}</div>)
                  : 'Please correct the highlighted fields.'}
              </>
            ) : undefined
          }
        />
      </PageBody>

      <ConfirmDialog
        open={confirmRevoke}
        title="Log Out All Other Sessions"
        message="Every other browser and device is signed out and must sign in again. This session stays signed in."
        confirmLabel="Log out"
        kind="warning"
        onConfirm={doRevokeSessions}
        onCancel={() => setConfirmRevoke(false)}
      />
      <ConfirmDialog
        open={confirmRestart}
        title="Restart Dupearr"
        message="Restart now? Running scans and tasks are interrupted and resume on their next schedule."
        confirmLabel="Restart"
        kind="warning"
        onConfirm={doRestart}
        onCancel={() => setConfirmRestart(false)}
      />
    </PageContent>
  );
}
