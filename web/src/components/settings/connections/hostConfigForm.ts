/**
 * Settings → General helpers (host config = config.xml): environment-override detection,
 * validation and the PUT /config/host payload.
 */
import { normalizeUrlBase } from '@/api/client';
import type { HostConfig } from '@/api/types';
import { isMaskedSecret, type FieldErrors } from './connectionUtils';

export type HostField = Exclude<
  keyof HostConfig,
  'envOverrides' | 'restartRequired' | 'webhookToken' | 'currentPassword' | 'keepApiKey'
>;

/** Every editable HostConfig property (JSON names). */
export const HOST_FIELDS: readonly HostField[] = [
  'bindAddress',
  'port',
  'urlBase',
  'enableSsl',
  'sslPort',
  'sslCertPath',
  'sslKeyPath',
  'apiKey',
  'authenticationMethod',
  'authenticationRequired',
  'username',
  'password',
  'passwordConfirmation',
  'logLevel',
  'logSizeLimit',
  'instanceName',
  'launchBrowser',
  'branch',
];

/** DUPEARR__SECTION__KEY environment variables → HostConfig property (docs/ARCHITECTURE.md §10). */
const ENV_VAR_FIELDS: Record<string, HostField> = {
  server__bindaddress: 'bindAddress',
  server__port: 'port',
  server__urlbase: 'urlBase',
  server__enablessl: 'enableSsl',
  server__sslport: 'sslPort',
  server__sslcertpath: 'sslCertPath',
  server__sslkeypath: 'sslKeyPath',
  auth__apikey: 'apiKey',
  auth__method: 'authenticationMethod',
  auth__required: 'authenticationRequired',
  log__level: 'logLevel',
  log__sizelimit: 'logSizeLimit',
  app__instancename: 'instanceName',
};

/**
 * Fields forced by environment variables. The API reports HostConfig property names
 * (e.g. ["port","urlBase"]); env var names ("DUPEARR__SERVER__PORT", "SERVER__PORT") are accepted
 * too, case-insensitively, so the UI stays correct if the backend reports either form.
 */
export function envOverriddenFields(list: readonly string[] | null | undefined): Set<HostField> {
  const byLower = new Map(HOST_FIELDS.map((f) => [f.toLowerCase(), f] as const));
  const out = new Set<HostField>();
  for (const raw of list ?? []) {
    if (typeof raw !== 'string') continue;
    const key = raw.trim().toLowerCase();
    if (!key) continue;
    const direct = byLower.get(key);
    if (direct) {
      out.add(direct);
      continue;
    }
    const env = ENV_VAR_FIELDS[key.replace(/^dupearr__/, '')];
    if (env) out.add(env);
  }
  return out;
}

/** Name of the env var that overrides a field (for tooltips), e.g. port → DUPEARR__SERVER__PORT. */
export function envVarName(field: HostField): string | null {
  const entry = Object.entries(ENV_VAR_FIELDS).find(([, f]) => f === field);
  return entry ? `DUPEARR__${entry[0].toUpperCase()}` : null;
}

function validPort(n: unknown): boolean {
  return typeof n === 'number' && Number.isInteger(n) && n >= 1 && n <= 65535;
}

/** Largest accepted log size limit in MB (internal/config MaxLogSizeLimit, Servarr's 1–10). */
export const MAX_LOG_SIZE_LIMIT_MB = 10;

/** "*" or an IPv4/IPv6 address (the server accepts exactly what Go's net.ParseIP accepts). */
export function validBindAddress(value: string): boolean {
  const v = value.trim();
  if (v === '*') return true;
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(v);
  if (v4) return v4.slice(1).every((o) => Number(o) <= 255 && (o === '0' || !o.startsWith('0')));
  // IPv6 (incl. "::" and an embedded IPv4 tail); the server does the exact check.
  return v.includes(':') && /^[0-9A-Fa-f:.]+$/.test(v) && (v.match(/::/g)?.length ?? 0) <= 1;
}

/**
 * Why a URL base is invalid (null when valid), mirroring the server: only unreserved URL
 * characters, no empty ("//"), "." or ".." path segments, at most 200 characters.
 */
export function urlBaseProblem(value: string): string | null {
  const base = normalizeUrlBase(value);
  if (!base) return null;
  if (base.length > 200) return 'URL base must be at most 200 characters';
  for (const segment of base.slice(1).split('/')) {
    if (segment === '') return 'URL base must not contain empty path segments (//)';
    if (segment === '.' || segment === '..') return 'URL base must not contain "." or ".." segments';
    if (!/^[A-Za-z0-9._~-]+$/.test(segment)) {
      return 'URL base may only contain letters, digits and - . _ ~ / (e.g. /dupearr)';
    }
  }
  return null;
}

/**
 * The latest server value with only the properties the user edited taken from the form. Settings
 * PUTs replace the whole object, so sending a snapshot taken when the page opened would silently
 * revert changes made meanwhile elsewhere (another tab, another page) — e.g. turning Dry Run back
 * off. Overlaying just the edited keys onto a fresh read avoids that.
 */
export function overlayEdits<T extends object>(fresh: T, edited: T, keys: Iterable<keyof T>): T {
  const out = { ...fresh };
  for (const k of keys) out[k] = edited[k];
  return out;
}

/** Whether the password field was edited (the API returns it masked or empty). */
export function passwordChanged(values: Pick<HostConfig, 'password'>, server: Pick<HostConfig, 'password'>): boolean {
  return (values.password ?? '') !== (server.password ?? '');
}

type CredentialFields = Pick<HostConfig, 'username' | 'password' | 'authenticationMethod' | 'authenticationRequired'>;

/**
 * Whether the form changes a credential (username, password, authentication method or requirement):
 * the server then requires the current password whenever a Forms account exists (currentPassword).
 */
export function credentialChanged(values: CredentialFields, server: CredentialFields): boolean {
  return (
    passwordChanged(values, server) ||
    (values.username ?? '').trim() !== (server.username ?? '') ||
    values.authenticationMethod !== server.authenticationMethod ||
    values.authenticationRequired !== server.authenticationRequired
  );
}

/** Client-side validation of the host config form (the server validates again). */
export function validateHostConfig(values: HostConfig, server: HostConfig): FieldErrors {
  const e: FieldErrors = {};
  const add = (k: string, msg: string) => (e[k] ??= []).push(msg);

  const bind = (values.bindAddress ?? '').trim();
  if (!bind) add('bindAddress', 'Bind address is required (use * for all interfaces)');
  else if (!validBindAddress(bind)) add('bindAddress', 'Bind address must be * or an IP address');
  if (!validPort(values.port)) add('port', 'Port must be between 1 and 65535');

  const urlBaseError = urlBaseProblem(values.urlBase ?? '');
  if (urlBaseError) add('urlBase', urlBaseError);

  const instanceName = (values.instanceName ?? '').trim();
  if (!instanceName) add('instanceName', 'Instance name is required');
  else if ([...instanceName].length > 100) add('instanceName', 'Instance name must be at most 100 characters');

  if (values.enableSsl) {
    if (!validPort(values.sslPort)) add('sslPort', 'SSL port must be between 1 and 65535');
    else if (values.sslPort === values.port) add('sslPort', 'SSL port must differ from the HTTP port');
    if (!(values.sslCertPath ?? '').trim()) add('sslCertPath', 'Certificate path is required when SSL is enabled');
    if (!(values.sslKeyPath ?? '').trim()) add('sslKeyPath', 'Key path is required when SSL is enabled');
  }

  if (values.authenticationMethod === 'Forms') {
    if (!(values.username ?? '').trim()) add('username', 'Username is required for Forms authentication');
    const changed = passwordChanged(values, server);
    const hasStored = !!server.password;
    if (!changed && !hasStored) add('password', 'Password is required for Forms authentication');
    if (changed) {
      if (!values.password) add('password', 'Password is required for Forms authentication');
      else if (isMaskedSecret(values.password)) add('password', 'Enter a new password');
      if ((values.passwordConfirmation ?? '') !== (values.password ?? '')) {
        add('passwordConfirmation', 'Passwords do not match');
      }
    }
  }

  if (
    typeof values.logSizeLimit !== 'number' ||
    !Number.isInteger(values.logSizeLimit) ||
    values.logSizeLimit < 1 ||
    values.logSizeLimit > MAX_LOG_SIZE_LIMIT_MB
  ) {
    add('logSizeLimit', `Log size limit must be between 1 and ${MAX_LOG_SIZE_LIMIT_MB} MB`);
  }
  return e;
}

/**
 * PUT body: the API key always comes from the server (it is read-only here and may have been
 * regenerated meanwhile), the password is only sent when changed (otherwise the stored/masked value
 * goes back, which keeps it), and response-only properties are dropped.
 */
export function hostConfigPayload(values: HostConfig, server: HostConfig): HostConfig {
  const out: HostConfig = {
    ...values,
    apiKey: server.apiKey,
    urlBase: normalizeUrlBase(values.urlBase),
    bindAddress: (values.bindAddress ?? '').trim(),
    instanceName: (values.instanceName ?? '').trim(),
    username: (values.username ?? '').trim(),
    sslCertPath: (values.sslCertPath ?? '').trim(),
    sslKeyPath: (values.sslKeyPath ?? '').trim(),
  };
  delete out.restartRequired;
  if (!passwordChanged(values, server)) {
    out.password = server.password;
    delete out.passwordConfirmation;
  }
  return out;
}

/** Drops response-only and write-only properties before adopting a PUT response as the form baseline. */
export function stripTransient(cfg: HostConfig, previous?: HostConfig): HostConfig {
  const out: HostConfig = { ...cfg, envOverrides: cfg.envOverrides ?? previous?.envOverrides ?? [] };
  delete out.restartRequired;
  delete out.passwordConfirmation;
  delete out.currentPassword;
  delete out.keepApiKey;
  return out;
}
