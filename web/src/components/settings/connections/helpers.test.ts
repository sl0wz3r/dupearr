import { describe, expect, it } from 'vitest';
import { ApiError } from '@/api/client';
import type { HostConfig, Library, Profile, ProviderSchema } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { arrPayload, looksLikeArrApiKey, validateArr } from './ArrInstanceModal';
import {
  arrUrlCheck,
  canForceSave,
  looksLikeJwt,
  mergeFieldErrors,
  plexUrlCheck,
  safeExternalUrl,
  propertyLabel,
  splitApiError,
  summarizeSaveError,
  trimTrailingSlash,
  validateHttpUrl,
} from './connectionUtils';
import {
  envOverriddenFields,
  envVarName,
  hostConfigPayload,
  stripTransient,
  validateHostConfig,
} from './hostConfigForm';
import { libraryTypeLabel, profileOptions, scopeGroupHints } from './LibrariesTable';
import { mediaServerPayload, validateMediaServer } from './MediaServerModal';
import {
  defaultSettings,
  defaultTriggers,
  initialSettings,
  splitSettingsErrors,
  toSubmitSettings,
  triggersSummary,
  validateSettings,
} from './notificationForm';
import { triggerOptions } from './NotificationModal';
import { unmaskEdit } from './SecretInput';
import { parseTags } from './TagsInput';
import { maskWebhookUrl, webhookUrl } from './WebhookInfo';

const validation = (items: { propertyName: string; errorMessage: string }[]) =>
  new ApiError(items.map((i) => i.errorMessage).join('\n'), { status: 400, validationErrors: items });

describe('validateHttpUrl', () => {
  it.each<[string | null | undefined, string | null]>([
    ['http://192.168.1.10:32400', null],
    ['https://plex.example.com', null],
    ['  http://radarr:7878/radarr  ', null],
    ['', 'URL is required'],
    [null, 'URL is required'],
    ['192.168.1.10:32400', 'URL must start with http:// or https://'],
    ['plex:32400', 'URL must start with http:// or https://'],
    ['ftp://host', 'URL must start with http:// or https://'],
    ['javascript:alert(1)', 'URL must start with http:// or https://'],
    ['not a url', 'URL must start with http:// or https://'],
    ['http://', 'URL must be a full address, e.g. http://192.168.1.10:32400'],
    ['HTTPS://Plex.Example.com:443', null],
    ['http://user:pw@host:1', 'URL must not contain a user name or password'],
    ['http://host:1/?x=1', 'URL must not contain a query string or fragment'],
  ])('%s → %s', (input, expected) => {
    expect(validateHttpUrl(input)).toBe(expected);
  });

  it('runs app-specific checks', () => {
    expect(validateHttpUrl('http://10.0.0.2:32400/web/index.html', { extra: plexUrlCheck })).toMatch(/without "\/web"/);
    expect(validateHttpUrl('http://10.0.0.2:32400', { extra: plexUrlCheck })).toBeNull();
    expect(validateHttpUrl('http://radarr:7878/api/v3', { extra: arrUrlCheck })).toMatch(/Do not include "\/api"/);
    expect(validateHttpUrl('http://radarr:7878/radarr', { extra: arrUrlCheck })).toBeNull();
  });

  it('trims trailing slashes', () => {
    expect(trimTrailingSlash(' http://x:1/radarr/// ')).toBe('http://x:1/radarr');
  });

  it('detects short-lived JWT tokens', () => {
    expect(looksLikeJwt('eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiIxIn0.c2ln')).toBe(true);
    expect(looksLikeJwt('abcDEF123xyz')).toBe(false);
  });

  it('only allows http(s) external links', () => {
    expect(safeExternalUrl('https://wiki.servarr.com/radarr')).toBe('https://wiki.servarr.com/radarr');
    expect(safeExternalUrl('javascript:alert(1)')).toBeNull();
    expect(safeExternalUrl(undefined)).toBeNull();
  });
});

describe('save error handling', () => {
  it.each<[string, unknown, boolean]>([
    ['400 message (connection test failed)', new ApiError('Unable to connect', { status: 400 }), true],
    ['502 upstream', new ApiError('Upstream server error', { status: 502 }), true],
    ['validation', validation([{ propertyName: 'Url', errorMessage: 'Unable to connect to Plex' }]), true],
    ['network failure reaching Dupearr', new ApiError('Unable to reach Dupearr', { status: 0 }), false],
    ['unauthorized', new ApiError('Unauthorized', { status: 401 }), false],
    ['not found', new ApiError('Not found', { status: 404 }), false],
    ['conflict', new ApiError('Conflict', { status: 409 }), false],
    ['plain error', new Error('x'), false],
  ])('canForceSave: %s → %s', (_name, error, expected) => {
    expect(canForceSave(error)).toBe(expected);
  });

  it('splits validation errors into known fields and general messages', () => {
    const err = validation([
      { propertyName: 'Url', errorMessage: 'Invalid URL' },
      { propertyName: '', errorMessage: 'Connection refused' },
      { propertyName: 'somethingElse', errorMessage: 'Other problem' },
    ]);
    expect(splitApiError(err, ['url', 'name'])).toEqual({
      fields: { url: ['Invalid URL'] },
      // Errors on properties the form doesn't render still say which property they are about.
      general: ['Connection refused', 'Something Else: Other problem'],
    });
    expect(splitApiError(err, ['url', 'name'], (p) => (p === 'somethingElse' ? 'Custom' : undefined)).general).toEqual([
      'Connection refused',
      'Custom: Other problem',
    ]);
    expect(splitApiError(new ApiError('Boom', { status: 502, description: 'details' }), ['url'])).toEqual({
      fields: {},
      general: ['Boom', 'details'],
    });
    expect(splitApiError(null, ['url'])).toEqual({ fields: {}, general: [] });
  });

  it('matches properties ignoring case and index suffixes, without repeating a named label', () => {
    const err = validation([
      { propertyName: 'MaxBytesPerRunGB', errorMessage: 'Must be between 1 and 1000000' },
      { propertyName: 'deletionMethods[1]', errorMessage: "Deletion method 'plex' is listed twice" },
      { propertyName: 'settings.webhookUrl', errorMessage: 'Webhook Url is masked' },
    ]);
    expect(splitApiError(err, ['maxBytesPerRunGb', 'deletionMethods'])).toEqual({
      fields: {
        maxBytesPerRunGb: ['Must be between 1 and 1000000'],
        deletionMethods: ["Deletion method 'plex' is listed twice"],
      },
      general: ['Webhook Url is masked'],
    });
    expect(propertyLabel('settings.webhookUrl')).toBe('Webhook Url');
    expect(propertyLabel('criteria[2].patterns[0]')).toBe('Patterns');
  });

  it('always has a message to show and says whether force-save is possible', () => {
    expect(summarizeSaveError(null, [])).toEqual({ fields: {}, messages: [], canForce: false });
    const onlyFields = summarizeSaveError(validation([{ propertyName: 'url', errorMessage: 'bad' }]), ['url']);
    expect(onlyFields.messages).toEqual(['Please correct the highlighted fields.']);
    expect(onlyFields.fields).toEqual({ url: ['bad'] });
  });

  it('merges field errors without duplicates', () => {
    expect(mergeFieldErrors({ a: ['x'] }, { a: ['x', 'y'], b: ['z'] })).toEqual({ a: ['x', 'y'], b: ['z'] });
  });
});

describe('unmaskEdit', () => {
  it.each([
    ['********', '********a', 'a'],
    ['********', 'secret', 'secret'],
    ['********', '****x****', 'x'],
    ['********', '*******', ''],
    ['********', '', ''],
    ['********', '********', '********'],
    ['********', 'pasted-token********', 'pasted-token'],
    ['my*pass', 'my*passw', 'my*passw'],
    ['', 'abc', 'abc'],
  ])('%s → %s = %s', (prev, next, expected) => {
    expect(unmaskEdit(prev, next)).toBe(expected);
  });
});

describe('parseTags', () => {
  it('splits, trims and de-duplicates (case-insensitive)', () => {
    expect(parseTags(' 4k, Remux ,,4K, hdr', ['HDR'])).toEqual(['4k', 'Remux']);
    expect(parseTags('')).toEqual([]);
  });
});

describe('webhook URL', () => {
  it('builds the URL with the url base and an encoded api key', () => {
    expect(webhookUrl('http://dupearr:3873/', '/dupearr', 'radarr', 'ab c')).toBe(
      'http://dupearr:3873/dupearr/api/v1/webhook/radarr?apikey=ab%20c',
    );
    expect(webhookUrl('https://d.example', '', 'sonarr', 'k')).toBe('https://d.example/api/v1/webhook/sonarr?apikey=k');
    expect(webhookUrl('https://d.example', 'sub/', 'plex', 'k')).toBe('https://d.example/sub/api/v1/webhook/plex?apikey=k');
  });

  it('masks the api key for display', () => {
    expect(maskWebhookUrl('http://h/api/v1/webhook/radarr?apikey=secret123')).toBe(
      'http://h/api/v1/webhook/radarr?apikey=••••••••',
    );
  });
});

describe('media server form', () => {
  const values = { name: ' Plex ', url: 'http://10.0.0.2:32400/', token: ' tok ', verifyTls: true, enabled: true, machineIdentifier: '' };

  it('validates name, url and token', () => {
    expect(validateMediaServer(values)).toEqual({});
    expect(validateMediaServer({ ...values, name: '', url: 'plex:32400', token: '' })).toEqual({
      name: ['Name is required'],
      url: ['URL must start with http:// or https://'],
      token: [expect.stringMatching(/token is required/i)],
    });
  });

  it('builds the payload, keeping a masked token as-is', () => {
    expect(mediaServerPayload(values)).toEqual({
      name: 'Plex',
      kind: 'plex',
      url: 'http://10.0.0.2:32400',
      token: 'tok',
      verifyTls: true,
      enabled: true,
    });
    expect(mediaServerPayload({ ...values, token: MASKED_SECRET, machineIdentifier: 'm1' }, 5)).toMatchObject({
      id: 5,
      token: MASKED_SECRET,
      machineIdentifier: 'm1',
    });
  });
});

describe('arr form', () => {
  const values = { name: 'Radarr 4K', url: 'http://radarr4k:7878/', apiKey: '0123456789abcdef0123456789abcdef', verifyTls: false, enabled: true, tags: ['4k'] };

  it('validates and builds the payload', () => {
    expect(validateArr(values)).toEqual({});
    expect(validateArr({ ...values, apiKey: ' ' })).toEqual({ apiKey: ['API key is required'] });
    expect(arrPayload(values, 'radarr', 3)).toEqual({
      id: 3,
      name: 'Radarr 4K',
      kind: 'radarr',
      url: 'http://radarr4k:7878',
      apiKey: '0123456789abcdef0123456789abcdef',
      verifyTls: false,
      enabled: true,
      tags: ['4k'],
    });
  });

  it('flags keys that do not look like *arr API keys', () => {
    expect(looksLikeArrApiKey(MASKED_SECRET)).toBe(true);
    expect(looksLikeArrApiKey('0123456789abcdef0123456789abcdef')).toBe(true);
    expect(looksLikeArrApiKey('http://radarr:7878')).toBe(false);
  });
});

describe('notification settings', () => {
  const schema: ProviderSchema = {
    kind: 'gotify',
    name: 'Gotify',
    fields: [
      { name: 'serverUrl', label: 'Server URL', type: 'url', required: true },
      { name: 'appToken', label: 'App Token', type: 'text', required: true, secret: true },
      { name: 'priority', label: 'Priority', type: 'number', default: 5 },
      { name: 'markdown', label: 'Markdown', type: 'checkbox' },
      { name: 'level', label: 'Level', type: 'select', options: ['low', 'high'] },
      { name: 'title', label: 'Title', type: 'text' },
    ],
  };

  it('builds defaults and merges stored settings (null tolerated)', () => {
    expect(defaultSettings(schema)).toEqual({ serverUrl: '', appToken: '', priority: 5, markdown: false, level: '', title: '' });
    expect(initialSettings(schema, { appToken: MASKED_SECRET, extra: 1 })).toMatchObject({ appToken: MASKED_SECRET, extra: 1, priority: 5 });
    expect(initialSettings(schema, null)).toEqual(defaultSettings(schema));
    expect(initialSettings(null, [1, 2])).toEqual({});
  });

  it('validates required, number, url and select values; masked secrets pass', () => {
    expect(
      validateSettings(schema, { serverUrl: 'not a url', appToken: '', priority: 'abc', level: 'medium' }),
    ).toEqual({
      serverUrl: ['Server URL must be a full http:// or https:// URL, e.g. https://example.com/…'],
      appToken: ['App Token is required'],
      priority: ['Priority must be a number'],
      level: ['Level must be one of: low, high'],
    });
    // Parseable but not an http(s) endpoint.
    for (const serverUrl of ['javascript:alert(1)', 'ftp://gotify.local', 'mailto:a@b.c']) {
      expect(validateSettings(schema, { serverUrl, appToken: 'x' }).serverUrl).toEqual([
        'Server URL must be a full http:// or https:// URL, e.g. https://example.com/…',
      ]);
    }
    expect(validateSettings(schema, { serverUrl: 'https://gotify.local', appToken: MASKED_SECRET, priority: '7' })).toEqual({});
  });

  it('coerces the payload and never trims or alters secrets', () => {
    expect(
      toSubmitSettings(schema, {
        serverUrl: ' https://g.local ',
        appToken: MASKED_SECRET,
        priority: '7',
        markdown: 'true',
        title: ' Hi ',
        extra: 'x',
      }),
    ).toEqual({ serverUrl: 'https://g.local', appToken: MASKED_SECRET, priority: 7, markdown: true, title: 'Hi', extra: 'x', level: undefined });
    expect(toSubmitSettings(schema, { appToken: ' spaced ' }).appToken).toBe(' spaced ');
  });

  it('maps server validation errors onto settings fields', () => {
    expect(
      splitSettingsErrors({ 'settings.serverUrl': ['bad'], appToken: ['missing'], name: ['required'], 'settings[Priority]': ['nan'] }, schema),
    ).toEqual([{ serverUrl: ['bad'], appToken: ['missing'], priority: ['nan'] }, { name: ['required'] }]);
  });

  it('defaults triggers and summarizes them', () => {
    expect(
      defaultTriggers([
        { value: 'onDuplicatesFound', label: 'a' },
        { value: 'onScanCompleted', label: 'b' },
        { value: 'onDeleteFailed', label: 'c' },
      ]),
    ).toEqual(['onDuplicatesFound', 'onDeleteFailed']);
    expect(defaultTriggers(null)).toEqual([]);
    expect(triggersSummary([], String)).toBe('No triggers');
    expect(triggersSummary(null, String)).toBe('No triggers');
    expect(triggersSummary(['a', 'b', 'c', 'd'], (v) => v.toUpperCase())).toBe('A, B +2');
  });

  it('falls back to built-in trigger labels and keeps unknown stored triggers', () => {
    const opts = triggerOptions(undefined, ['onCustom']);
    expect(opts.some((o) => o.value === 'onFileDeleted')).toBe(true);
    expect(opts.at(-1)).toEqual({ value: 'onCustom', label: 'On Custom' });
    expect(triggerOptions([{ value: 'onHealthIssue', label: 'Health!' }], [])).toEqual([{ value: 'onHealthIssue', label: 'Health!' }]);
  });
});

describe('libraries', () => {
  const lib = (over: Partial<Library>): Library => ({
    id: 1,
    serverId: 1,
    sectionKey: '1',
    title: 'Movies',
    type: 'movie',
    locations: ['/data/movies'],
    enabled: true,
    profileId: null,
    scopeGroup: '',
    updatedAt: '2026-01-01T00:00:00Z',
    ...over,
  });

  it('labels library types', () => {
    expect(libraryTypeLabel('movie')).toBe('Movies');
    expect(libraryTypeLabel('show')).toBe('TV Shows');
    expect(libraryTypeLabel('artist')).toBe('Artist');
    expect(libraryTypeLabel('')).toBe('Unknown');
  });

  it('lists profiles with Default first and keeps a missing assigned profile', () => {
    const profiles = [{ id: 1, name: 'Keep Highest Quality', isDefault: true }, { id: 2, name: 'Save Space', isDefault: false }] as Profile[];
    expect(profileOptions(profiles, null)).toEqual([
      { value: '', label: 'Default (Keep Highest Quality)' },
      { value: '1', label: 'Keep Highest Quality' },
      { value: '2', label: 'Save Space' },
    ]);
    expect(profileOptions([], 9).at(-1)).toEqual({ value: '9', label: 'Missing profile (#9)' });
  });

  it('hints at scope groups that cannot match anything', () => {
    const hints = scopeGroupHints([
      lib({ id: 1, scopeGroup: 'movies' }),
      lib({ id: 2, scopeGroup: 'movies', title: 'Movies 4K' }),
      lib({ id: 3, scopeGroup: 'lonely' }),
      lib({ id: 4, scopeGroup: 'mixed' }),
      lib({ id: 5, scopeGroup: 'mixed', type: 'show' }),
      lib({ id: 6, scopeGroup: 'off', enabled: false }),
    ]);
    expect(hints.has('movies')).toBe(false);
    expect(hints.get('lonely')).toMatch(/no other enabled library/i);
    expect(hints.get('mixed')).toMatch(/mixes movie and tv/i);
    expect(hints.has('off')).toBe(false);
  });
});

describe('host config', () => {
  const server: HostConfig = {
    bindAddress: '*',
    port: 3873,
    urlBase: '',
    enableSsl: false,
    sslPort: 9873,
    sslCertPath: '',
    sslKeyPath: '',
    apiKey: 'server-key',
    authenticationMethod: 'Forms',
    authenticationRequired: 'Enabled',
    username: 'admin',
    password: MASKED_SECRET,
    logLevel: 'info',
    logSizeLimit: 1,
    instanceName: 'Dupearr',
    launchBrowser: false,
    branch: 'main',
    envOverrides: [],
  };

  it('recognises env overrides by property name or env var name', () => {
    expect([...envOverriddenFields(['port', 'URLBASE', 'DUPEARR__AUTH__APIKEY', 'log__level', 'bogus', ''])].sort()).toEqual(
      ['apiKey', 'logLevel', 'port', 'urlBase'],
    );
    expect(envOverriddenFields(null).size).toBe(0);
    expect(envVarName('port')).toBe('DUPEARR__SERVER__PORT');
    expect(envVarName('username')).toBeNull();
  });

  it('validates ports, SSL, url base and Forms credentials', () => {
    expect(validateHostConfig(server, server)).toEqual({});
    expect(
      validateHostConfig(
        { ...server, port: 70000, urlBase: '/du pe', enableSsl: true, sslPort: 70000, instanceName: ' ', bindAddress: '' },
        server,
      ),
    ).toEqual({
      bindAddress: [expect.any(String)],
      port: ['Port must be between 1 and 65535'],
      urlBase: [expect.any(String)],
      instanceName: ['Instance name is required'],
      sslPort: ['SSL port must be between 1 and 65535'],
      sslCertPath: [expect.any(String)],
      sslKeyPath: [expect.any(String)],
    });
    expect(validateHostConfig({ ...server, enableSsl: true, sslPort: 3873, sslCertPath: '/c', sslKeyPath: '/k' }, server)).toEqual({
      sslPort: ['SSL port must differ from the HTTP port'],
    });
    expect(validateHostConfig({ ...server, password: 'new', passwordConfirmation: 'other' }, server)).toEqual({
      passwordConfirmation: ['Passwords do not match'],
    });
    expect(validateHostConfig({ ...server, password: '' }, server).password).toEqual(['Password is required for Forms authentication']);
    // No stored password yet (e.g. switching from External): one is required.
    const external = { ...server, authenticationMethod: 'External' as const, password: '' };
    expect(validateHostConfig({ ...external, authenticationMethod: 'Forms' }, external).password).toHaveLength(1);
    expect(validateHostConfig(external, external)).toEqual({});
  });

  it('sends the server api key, keeps an unchanged password masked and drops response-only fields', () => {
    const payload = hostConfigPayload(
      { ...server, apiKey: 'stale-key', urlBase: 'dupearr/', restartRequired: true, passwordConfirmation: '' },
      server,
    );
    expect(payload.apiKey).toBe('server-key');
    expect(payload.urlBase).toBe('/dupearr');
    expect(payload.password).toBe(MASKED_SECRET);
    expect('passwordConfirmation' in payload).toBe(false);
    expect('restartRequired' in payload).toBe(false);

    const changed = hostConfigPayload({ ...server, password: 'n3w', passwordConfirmation: 'n3w' }, server);
    expect(changed.password).toBe('n3w');
    expect(changed.passwordConfirmation).toBe('n3w');
  });

  it('strips transient fields from a PUT response', () => {
    const out = stripTransient({ ...server, restartRequired: true, envOverrides: null }, { ...server, envOverrides: ['port'] });
    expect('restartRequired' in out).toBe(false);
    expect(out.envOverrides).toEqual(['port']);
  });
});
