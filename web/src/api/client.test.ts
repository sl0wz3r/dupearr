import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  api,
  ApiError,
  apiUrl,
  bootstrap,
  buildApiError,
  configureApi,
  errorMessage,
  normalizeUrlBase,
  onUnauthorized,
  parseApiError,
  requestWithMeta,
  resolveApiRoot,
  rootPropertyName,
  toQueryString,
} from './client';

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

describe('error parsing', () => {
  it('parses validation arrays into field errors', async () => {
    const res = jsonResponse(
      [
        { propertyName: 'Url', errorMessage: 'URL is required' },
        { propertyName: 'url', errorMessage: 'URL must be absolute' },
        { propertyName: 'apiKey', errorMessage: 'API key is required' },
      ],
      400,
    );
    const err = await parseApiError(res);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.isValidation).toBe(true);
    expect(err.message).toBe('URL is required\nURL must be absolute\nAPI key is required');
    expect(err.fieldErrors()).toEqual({
      url: ['URL is required', 'URL must be absolute'],
      apiKey: ['API key is required'],
    });
    expect(err.errorsFor('Url')).toEqual(['URL is required', 'URL must be absolute']);
    expect(err.errorsFor('name')).toEqual([]);
  });

  it('finds field errors ignoring case and index/path suffixes, and flags conflicts', () => {
    const err = new ApiError('x', {
      status: 400,
      validationErrors: [
        { propertyName: 'MaxBytesPerRunGB', errorMessage: 'Too small' },
        { propertyName: 'deletionMethods[1]', errorMessage: 'Listed twice' },
        { propertyName: 'settings.url', errorMessage: 'Bad URL' },
      ],
    });
    expect(err.errorsFor('maxBytesPerRunGb')).toEqual(['Too small']);
    expect(err.errorsFor('deletionMethods')).toEqual(['Listed twice']);
    expect(err.errorsFor('settings')).toEqual(['Bad URL']);
    expect(err.errorsFor('url')).toEqual([]);
    expect(rootPropertyName('criteria[2].order')).toBe('criteria');
    expect(rootPropertyName(undefined)).toBe('');
    expect(new ApiError('changed', { status: 409 }).isConflict).toBe(true);
    expect(err.isConflict).toBe(false);
  });

  it('parses {message, description} bodies', async () => {
    const err = await parseApiError(
      jsonResponse({ message: 'Profile is the default', description: 'Pick another default first' }, 409),
    );
    expect(err.status).toBe(409);
    expect(err.message).toBe('Profile is the default');
    expect(err.description).toBe('Pick another default first');
    expect(err.validationErrors).toEqual([]);
  });

  it('falls back to text bodies and status text', async () => {
    const text = await parseApiError(new Response('upstream exploded', { status: 502 }));
    expect(text.message).toBe('upstream exploded');

    const empty = await parseApiError(new Response(null, { status: 500, statusText: 'Internal Server Error' }));
    expect(empty.message).toBe('Server error: Internal Server Error');

    expect(buildApiError(404, '', undefined).message).toBe('Not found');
    expect(buildApiError(401, '', { message: 'Unauthorized' }).isUnauthorized).toBe(true);
  });

  it('tolerates malformed JSON', async () => {
    const err = await parseApiError(
      new Response('{not json', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    );
    expect(err.message).toBe('{not json');
  });

  it('errorMessage() handles any thrown value', () => {
    expect(errorMessage(new ApiError('boom', { status: 500 }))).toBe('boom');
    expect(errorMessage(new Error('plain'))).toBe('plain');
    expect(errorMessage('text')).toBe('text');
    expect(errorMessage(undefined, 'fallback')).toBe('fallback');
  });
});

describe('url helpers', () => {
  it('normalizes url bases', () => {
    expect(normalizeUrlBase('')).toBe('');
    expect(normalizeUrlBase('/')).toBe('');
    expect(normalizeUrlBase('dupearr/')).toBe('/dupearr');
    expect(normalizeUrlBase('/dupearr')).toBe('/dupearr');
  });

  it('resolves the api root against the url base', () => {
    expect(resolveApiRoot('/api/v1', '')).toBe('/api/v1');
    expect(resolveApiRoot('/api/v1', '/dupearr')).toBe('/dupearr/api/v1');
    expect(resolveApiRoot('/dupearr/api/v1', '/dupearr')).toBe('/dupearr/api/v1');
    expect(resolveApiRoot(undefined, '/x')).toBe('/x/api/v1');
  });

  it('serializes query params (comma lists, skips empties)', () => {
    expect(toQueryString({ status: ['pending', 'review'], page: 2, search: '', flag: undefined, x: null })).toBe(
      '?status=pending%2Creview&page=2',
    );
    expect(toQueryString({ ids: [] })).toBe('');
    expect(toQueryString()).toBe('');
  });
});

describe('requests', () => {
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock);
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
  });

  afterEach(() => {
    fetchMock.mockReset();
    vi.unstubAllGlobals();
  });

  // r2-outbound-web#2/#3: the web UI authenticates with its session cookie; it never holds or sends
  // the master API key (not in headers, not in URLs).
  it('sends JSON bodies to the api root with the session cookie and no API key', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ id: 1 }, 201));
    const result = await api.post<{ id: number }>('/profile', { name: 'Best' });
    expect(result).toEqual({ id: 1 });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('/api/v1/profile');
    expect(init?.method).toBe('POST');
    const headers = init?.headers as Record<string, string>;
    expect(headers['X-Api-Key']).toBeUndefined();
    expect(init?.credentials).toBe('same-origin');
    expect(headers['Content-Type']).toBe('application/json');
    expect(init?.body).toBe('{"name":"Best"}');
    expect(apiUrl('/duplicate', { status: ['pending'] })).toBe('/api/v1/duplicate?status=pending');
  });

  it('throws ApiError with validation details', async () => {
    fetchMock.mockResolvedValue(jsonResponse([{ propertyName: 'name', errorMessage: 'Name is required' }], 400));
    const err = await api.put('/profile/1', {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).errorsFor('name')).toEqual(['Name is required']);
  });

  it('notifies unauthorized handlers on 401', async () => {
    const handler = vi.fn();
    const off = onUnauthorized(handler);
    fetchMock.mockResolvedValue(jsonResponse({ message: 'Unauthorized' }, 401));
    await expect(api.get('/system/status')).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledTimes(1);
    off();
  });

  it('maps network failures to status 0', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(api.get('/health')).rejects.toMatchObject({ status: 0 });
  });

  it('exposes response headers (path mapping warnings)', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ id: 3 }, 201, { 'X-Dupearr-Warning': 'Local path does not exist' }));
    const res = await requestWithMeta('POST', '/pathmapping', { body: {} });
    expect(res.headers.get('X-Dupearr-Warning')).toBe('Local path does not exist');
  });

  it('bootstrap → login on 401, setup when required', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ message: 'Unauthorized' }, 401))
      .mockResolvedValueOnce(jsonResponse({ setupRequired: true, authenticationMethod: 'Forms', authenticated: false }));
    await expect(bootstrap()).resolves.toMatchObject({ status: 'setup' });

    fetchMock
      .mockResolvedValueOnce(jsonResponse({ message: 'Unauthorized' }, 401))
      .mockResolvedValueOnce(jsonResponse({ setupRequired: false, authenticationMethod: 'Forms', authenticated: false }));
    await expect(bootstrap()).resolves.toMatchObject({ status: 'login' });
  });

  it('bootstrap configures the client from initialize.json', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        apiRoot: '/api/v1',
        // An older server may still send a key: it is ignored.
        apiKey: 'from-init',
        urlBase: '',
        version: '0.1.0',
        instanceName: 'Dupearr',
        authenticationMethod: 'Forms',
      }),
    );
    await expect(bootstrap()).resolves.toMatchObject({ status: 'ready' });
    expect(fetchMock.mock.calls[0]![0]).toBe('/initialize.json');

    fetchMock.mockResolvedValueOnce(jsonResponse([]));
    await api.get('/health');
    const [url, init] = fetchMock.mock.calls[1]!;
    const headers = init?.headers as Record<string, string>;
    expect(headers['X-Api-Key']).toBeUndefined();
    expect(String(url)).not.toContain('from-init');
  });
});
