/**
 * Unit tests for the safety guards added in review: stored secrets never follow an address change,
 * stale machine identifiers, server-aligned host validation, edit overlays and webhook URLs.
 */
import { describe, expect, it } from 'vitest';
import type { HostConfig, ProviderSchema, Settings } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { validateArr, type ArrFormValues } from './ArrInstanceModal';
import { addressChanged, secretAtNewAddressMessage, urlHost } from './connectionUtils';
import {
  MAX_LOG_SIZE_LIMIT_MB,
  overlayEdits,
  urlBaseProblem,
  validBindAddress,
  validateHostConfig,
} from './hostConfigForm';
import { changedMachineId, validateMediaServer, type MediaServerFormValues } from './MediaServerModal';
import {
  hasMaskedHeaderValues,
  isAddressField,
  maskedSecretsAtNewAddress,
  storedSecretReentryMessage,
  storedSecretRefusedMessage,
  toSubmitSettings,
  withReentryHints,
} from './notificationForm';
import { webhookUrl } from './WebhookInfo';

describe('urlHost / addressChanged', () => {
  it.each([
    ['http://10.0.0.2:32400', '10.0.0.2:32400'],
    ['HTTP://Plex.LAN:32400/', 'plex.lan:32400'],
    ['https://radarr.example', 'radarr.example'],
    ['https://radarr.example:443/radarr', 'radarr.example'],
    ['', null],
    ['not a url', null],
  ])('urlHost(%j) = %j', (input, expected) => {
    expect(urlHost(input)).toBe(expected);
  });

  it.each([
    // same server
    ['http://10.0.0.2:32400', 'http://10.0.0.2:32400/', false],
    ['http://radarr:7878', 'http://RADARR:7878/radarr', false],
    ['http://plex:32400', 'https://plex:32400', false], // upgrade to https is fine
    ['smtp.example.com', ' SMTP.example.com ', false], // non-URL (SMTP host) compares as text
    // different server
    ['http://10.0.0.2:32400', 'http://10.0.0.3:32400', true],
    ['http://radarr:7878', 'http://radarr:7879', true],
    ['https://plex:32400', 'http://plex:32400', true], // downgrade would send the secret in clear text
    ['http://radarr:7878', 'http://evil.example', true],
    ['smtp.example.com', 'smtp.evil.example', true],
    ['http://radarr:7878', '', true],
  ])('addressChanged(%j, %j) = %j', (before, after, expected) => {
    expect(addressChanged(before, after)).toBe(expected);
  });
});

const PLEX_VALUES: MediaServerFormValues = {
  name: 'Home',
  url: 'http://10.0.0.2:32400',
  token: MASKED_SECRET,
  verifyTls: true,
  enabled: true,
  machineIdentifier: 'm-1',
};

describe('stored secrets never follow an address change', () => {
  it('media server: masked token + same host is fine, new host requires the token again', () => {
    const saved = { url: 'http://10.0.0.2:32400' };
    expect(validateMediaServer(PLEX_VALUES, saved)).toEqual({});
    expect(validateMediaServer({ ...PLEX_VALUES, url: 'http://10.0.0.2:32400/' }, saved)).toEqual({});
    expect(validateMediaServer({ ...PLEX_VALUES, url: 'http://attacker.example:32400' }, saved)).toEqual({
      token: [secretAtNewAddressMessage('token')],
    });
    // A newly entered token may go anywhere.
    expect(validateMediaServer({ ...PLEX_VALUES, url: 'http://10.0.0.9:32400', token: 'fresh' }, saved)).toEqual({});
    // New servers have nothing stored.
    expect(validateMediaServer({ ...PLEX_VALUES, url: 'http://10.0.0.9:32400', token: 'fresh' })).toEqual({});
  });

  it('arr instance: masked API key + new host requires the key again', () => {
    const values: ArrFormValues = {
      name: 'Radarr',
      url: 'http://radarr:7878',
      apiKey: MASKED_SECRET,
      verifyTls: true,
      enabled: true,
      tags: [],
    };
    const saved = { url: 'http://radarr:7878' };
    expect(validateArr(values, saved)).toEqual({});
    expect(validateArr({ ...values, url: 'http://radarr:7878/radarr' }, saved)).toEqual({});
    expect(validateArr({ ...values, url: 'http://radarr4k:7878' }, saved)).toEqual({
      apiKey: [secretAtNewAddressMessage('API key')],
    });
    expect(validateArr({ ...values, url: 'http://radarr4k:7878', apiKey: '0123456789abcdef0123456789abcdef' }, saved)).toEqual({});
  });

  const GOTIFY: ProviderSchema = {
    kind: 'gotify',
    name: 'Gotify',
    fields: [
      { name: 'serverUrl', label: 'Server URL', type: 'url', required: true },
      { name: 'appToken', label: 'App Token', type: 'password', required: true, secret: true },
      { name: 'priority', label: 'Priority', type: 'number', default: 5 },
    ],
  };
  const NTFY: ProviderSchema = {
    kind: 'ntfy',
    name: 'ntfy',
    fields: [
      { name: 'serverUrl', label: 'Server URL', type: 'url', default: 'https://ntfy.sh' },
      { name: 'topic', label: 'Topic', type: 'text', required: true },
      { name: 'accessToken', label: 'Access Token', type: 'password', secret: true },
      { name: 'password', label: 'Password', type: 'password', secret: true },
    ],
  };
  const EMAIL: ProviderSchema = {
    kind: 'email',
    name: 'Email',
    fields: [
      { name: 'host', label: 'Server', type: 'text', required: true },
      { name: 'port', label: 'Port', type: 'number', default: 587 },
      { name: 'password', label: 'Password', type: 'password', secret: true },
    ],
  };

  it('identifies address fields', () => {
    expect(GOTIFY.fields.filter(isAddressField).map((f) => f.name)).toEqual(['serverUrl']);
    expect(EMAIL.fields.filter(isAddressField).map((f) => f.name)).toEqual(['host', 'port']);
  });

  it.each<[string, ProviderSchema, unknown, Record<string, unknown>, string[]]>([
    ['unchanged', GOTIFY, { serverUrl: 'http://gotify:80', appToken: MASKED_SECRET }, { serverUrl: 'http://gotify:80', appToken: MASKED_SECRET }, []],
    // The server binds stored secrets to the exact URL text (path included), not only the host.
    ['path changed', GOTIFY, { serverUrl: 'http://gotify', appToken: MASKED_SECRET }, { serverUrl: 'http://gotify/push', appToken: MASKED_SECRET }, ['appToken']],
    ['surrounding spaces only', GOTIFY, { serverUrl: 'http://gotify', appToken: MASKED_SECRET }, { serverUrl: ' http://gotify ', appToken: MASKED_SECRET }, []],
    ['moved, masked', GOTIFY, { serverUrl: 'http://gotify', appToken: MASKED_SECRET }, { serverUrl: 'http://evil', appToken: MASKED_SECRET }, ['appToken']],
    ['moved, re-entered', GOTIFY, { serverUrl: 'http://gotify', appToken: MASKED_SECRET }, { serverUrl: 'http://evil', appToken: 'new' }, []],
    // Stored without a server URL (= the default) and the form shows the default: unchanged.
    ['default kept', NTFY, { topic: 't', accessToken: MASKED_SECRET }, { serverUrl: 'https://ntfy.sh', topic: 't', accessToken: MASKED_SECRET, password: '' }, []],
    ['default → self-hosted', NTFY, { topic: 't', accessToken: MASKED_SECRET, password: MASKED_SECRET }, { serverUrl: 'https://ntfy.home', topic: 't', accessToken: MASKED_SECRET, password: MASKED_SECRET }, ['accessToken', 'password']],
    ['SMTP host moved', EMAIL, { host: 'smtp.example.com', password: MASKED_SECRET }, { host: 'smtp.evil.example', port: 587, password: MASKED_SECRET }, ['password']],
    ['SMTP host case only', EMAIL, { host: 'smtp.example.com', password: MASKED_SECRET }, { host: 'SMTP.Example.com', port: 587, password: MASKED_SECRET }, []],
    ['SMTP port moved', EMAIL, { host: 'smtp.example.com', port: 587, password: MASKED_SECRET }, { host: 'smtp.example.com', port: 2525, password: MASKED_SECRET }, ['password']],
    ['SMTP default port kept', EMAIL, { host: 'smtp.example.com', password: MASKED_SECRET }, { host: 'smtp.example.com', port: 587, password: MASKED_SECRET }, []],
    ['new connection', GOTIFY, null, { serverUrl: 'http://evil', appToken: MASKED_SECRET }, []],
  ])('%s', (_name, schema, stored, values, expected) => {
    expect(maskedSecretsAtNewAddress(schema, stored, values)).toEqual(expected);
  });

  it('treats a replaced secret URL as an address change for the other masked secrets', () => {
    const schema: ProviderSchema = {
      kind: 'webhook',
      name: 'Webhook',
      fields: [
        { name: 'url', label: 'URL', type: 'url', secret: true },
        { name: 'password', label: 'Password', type: 'password', secret: true },
      ],
    };
    const stored = { url: MASKED_SECRET, password: MASKED_SECRET };
    expect(maskedSecretsAtNewAddress(schema, stored, { ...stored })).toEqual([]);
    expect(maskedSecretsAtNewAddress(schema, stored, { url: 'https://evil.example/hook', password: MASKED_SECRET })).toEqual([
      'password',
    ]);
  });
});

describe('webhook header values shown as ********', () => {
  const WEBHOOK: ProviderSchema = {
    kind: 'webhook',
    name: 'Webhook',
    fields: [
      { name: 'url', label: 'URL', type: 'url', required: true },
      { name: 'password', label: 'Password', type: 'password', secret: true },
      { name: 'headers', label: 'Headers', type: 'textarea', advanced: true },
    ],
  };
  const headersField = WEBHOOK.fields[2]!;
  const MASKED_HEADERS = `Accept: application/json\nAuthorization: ${MASKED_SECRET}\n# X-Api-Key: ${MASKED_SECRET}`;

  it.each([
    [`Authorization: ${MASKED_SECRET}`, true],
    [`# X-Api-Key:   ${MASKED_SECRET}  `, true],
    [MASKED_SECRET, true], // a malformed line hidden whole
    ['Accept: application/json\nX-Token: abc', false],
    [`X-Token: ${MASKED_SECRET}x`, false], // a new value typed over the mask
    ['', false],
    [null, false],
  ])('hasMaskedHeaderValues(%j) → %s', (value, expected) => {
    expect(hasMaskedHeaderValues(value)).toBe(expected);
  });

  it('sends the masked header lines back verbatim so the server keeps the stored values', () => {
    const values = { url: 'https://hooks.example/abc', password: MASKED_SECRET, headers: `${MASKED_HEADERS}\n` };
    expect(toSubmitSettings(WEBHOOK, values).headers).toBe(`${MASKED_HEADERS}\n`);
    expect(maskedSecretsAtNewAddress(WEBHOOK, { ...values }, values)).toEqual([]);
  });

  it('asks to re-enter masked header values (and secrets) once the URL changes', () => {
    const stored = { url: 'https://hooks.example/abc', password: '', headers: MASKED_HEADERS };
    expect(maskedSecretsAtNewAddress(WEBHOOK, stored, { ...stored, url: 'https://hooks.example/other' })).toEqual(['headers']);
    expect(
      maskedSecretsAtNewAddress(WEBHOOK, { ...stored, password: MASKED_SECRET }, { ...stored, password: MASKED_SECRET, url: 'https://evil.example/' }),
    ).toEqual(['password', 'headers']);
    // Re-entered values: nothing masked is left to leak.
    expect(
      maskedSecretsAtNewAddress(WEBHOOK, stored, { ...stored, url: 'https://evil.example/', headers: 'Authorization: Bearer new' }),
    ).toEqual([]);
    expect(storedSecretReentryMessage(headersField)).toMatch(/^Re-enter the header values shown as \*{8}/);
    expect(storedSecretReentryMessage(WEBHOOK.fields[1]!)).toBe(secretAtNewAddressMessage('Password'));
  });

  it('adds a re-entry hint to server errors on fields that still hold a stored secret', () => {
    const values = { url: 'https://evil.example/', password: MASKED_SECRET, headers: MASKED_HEADERS };
    const hinted = withReentryHints(WEBHOOK, values, {
      password: ['Password is masked; re-enter the value (a stored secret is only kept while the server address is unchanged)'],
      headers: ['Headers are invalid'],
      url: ['URL is unreachable'],
    });
    // The server already asked for re-entry: left alone.
    expect(hinted.password).toHaveLength(1);
    expect(hinted.headers).toEqual(['Headers are invalid', storedSecretRefusedMessage(headersField)]);
    expect(storedSecretRefusedMessage(headersField)).toMatch(/^Re-enter the header values shown as \*{8}/);
    expect(hinted.url).toEqual(['URL is unreachable']);
    // No stored secret in the field: nothing to re-enter.
    expect(withReentryHints(WEBHOOK, { ...values, headers: 'X: y' }, { headers: ['bad'] }).headers).toEqual(['bad']);
  });
});

describe('changedMachineId', () => {
  it.each([
    ['', 'm-2', '', null], // nothing saved yet (new server / force-saved)
    ['m-1', undefined, 'm-1', null],
    ['m-1', 'm-1', 'm-2', null], // the test result is authoritative
    ['m-1', 'm-2', '', 'm-2'],
    ['m-1', undefined, 'm-3', 'm-3'], // "Sign in with Plex" picked another server
    ['m-1', undefined, '', null], // unknown after a manual edit (no test yet)
  ])('saved %j, tested %j, form %j → %j', (saved, tested, form, expected) => {
    expect(changedMachineId(saved, tested, form)).toBe(expected);
  });
});

const HOST: HostConfig = {
  bindAddress: '*',
  port: 3873,
  urlBase: '',
  enableSsl: false,
  sslPort: 9873,
  sslCertPath: '',
  sslKeyPath: '',
  apiKey: '0123456789abcdef0123456789abcdef',
  authenticationMethod: 'Forms',
  authenticationRequired: 'Enabled',
  trustedProxies: '',
  allowedHosts: '',
  username: 'admin',
  password: MASKED_SECRET,
  logLevel: 'info',
  logSizeLimit: 1,
  instanceName: 'Dupearr',
  launchBrowser: false,
  branch: 'main',
  envOverrides: [],
};

describe('host config validation mirrors the server', () => {
  it.each([
    ['*', true],
    ['0.0.0.0', true],
    ['192.168.1.10', true],
    ['::', true],
    ['::1', true],
    ['fe80::1', true],
    ['2001:db8::8a2e:370:7334', true],
    ['localhost', false],
    ['256.1.1.1', false],
    ['01.2.3.4', false], // Go's net.ParseIP rejects leading zeros
    ['1::2::3', false],
    ['1.2.3', false],
  ])('validBindAddress(%j) = %j', (value, ok) => {
    expect(validBindAddress(value)).toBe(ok);
  });

  it.each([
    ['', null],
    ['/', null],
    ['/dupearr', null],
    ['dupearr/', null],
    ['/media/dupearr', null],
    ['/a/../b', 'URL base must not contain "." or ".." segments'],
    ['/.', 'URL base must not contain "." or ".." segments'],
    ['/a//b', 'URL base must not contain empty path segments (//)'],
    ['/du pe', 'URL base may only contain letters, digits and - . _ ~ / (e.g. /dupearr)'],
    ['/a?b', 'URL base may only contain letters, digits and - . _ ~ / (e.g. /dupearr)'],
    [`/${'a'.repeat(200)}`, 'URL base must be at most 200 characters'],
  ])('urlBaseProblem(%j) = %j', (value, problem) => {
    expect(urlBaseProblem(value)).toBe(problem);
  });

  it('limits the log size to the server maximum', () => {
    expect(MAX_LOG_SIZE_LIMIT_MB).toBe(10);
    expect(validateHostConfig({ ...HOST, logSizeLimit: 10 }, HOST)).toEqual({});
    expect(validateHostConfig({ ...HOST, logSizeLimit: 11 }, HOST).logSizeLimit).toEqual([
      'Log size limit must be between 1 and 10 MB',
    ]);
    expect(validateHostConfig({ ...HOST, logSizeLimit: 1.5 }, HOST).logSizeLimit).toHaveLength(1);
  });

  it('limits the instance name to 100 characters', () => {
    expect(validateHostConfig({ ...HOST, instanceName: 'x'.repeat(100) }, HOST)).toEqual({});
    expect(validateHostConfig({ ...HOST, instanceName: 'x'.repeat(101) }, HOST).instanceName).toHaveLength(1);
  });
});

describe('overlayEdits', () => {
  it('takes only the edited keys from the form and everything else from the fresh read', () => {
    const snapshot = { dryRun: true, mode: 'manual', backupIntervalDays: 7, backupRetentionDays: 28 } as Settings;
    const edited = { ...snapshot, backupIntervalDays: 14 };
    const fresh = { ...snapshot, dryRun: false, mode: 'auto', backupRetentionDays: 60 } as Settings;
    expect(overlayEdits(fresh, edited, new Set<keyof Settings>(['backupIntervalDays']))).toEqual({
      dryRun: false,
      mode: 'auto',
      backupIntervalDays: 14,
      backupRetentionDays: 60,
    });
    // Nothing edited → the fresh value unchanged (and a new object).
    const same = overlayEdits(fresh, edited, []);
    expect(same).toEqual(fresh);
    expect(same).not.toBe(fresh);
  });
});

describe('webhookUrl with a URL base', () => {
  it.each([
    ['http://dupearr:3873', '/dupearr', 'http://dupearr:3873/dupearr/api/v1/webhook/radarr?apikey=k'],
    ['http://dupearr:3873/dupearr', '/dupearr', 'http://dupearr:3873/dupearr/api/v1/webhook/radarr?apikey=k'],
    ['http://dupearr:3873/dupearr/', 'dupearr', 'http://dupearr:3873/dupearr/api/v1/webhook/radarr?apikey=k'],
    // A host that happens to be named like the URL base is not stripped.
    ['http://dupearr', '/dupearr', 'http://dupearr/dupearr/api/v1/webhook/radarr?apikey=k'],
    ['http://dupearr:3873', '', 'http://dupearr:3873/api/v1/webhook/radarr?apikey=k'],
  ])('%j + %j', (base, urlBase, expected) => {
    expect(webhookUrl(base, urlBase, 'radarr', 'k')).toBe(expected);
  });
});
