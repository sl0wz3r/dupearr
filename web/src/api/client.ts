/**
 * HTTP client for the Dupearr API (docs/API.md).
 *
 * Bootstrap flow (see `bootstrap()`):
 *   1. GET {urlBase}/initialize.json with the session cookie.
 *   2. 401 → GET {urlBase}/api/v1/auth/status → "setup" (first run) or "login".
 *   3. 200 → store the API root. Every later call is authenticated by the session cookie (or the
 *      local-address bypass / External / None) — the web UI never holds the master API key, so it
 *      never ends up in URLs (proxy logs, browser history) and a logout really ends the access.
 *      State-changing calls are same-origin fetches, which carry the Origin header the server's
 *      cross-site check requires.
 *
 * Paths given to `api.get/post/put/del` are relative to the API root, e.g. `api.get('/system/status')`.
 */
import type {
  AuthSetupRequest,
  AuthStatus,
  ErrorResponse,
  InitializeResponse,
  LoginRequest,
  ValidationFailure,
} from './types';

declare global {
  interface Window {
    /** Injected by the Go server into index.html (replaces <!--DUPEARR_HEAD-->). */
    Dupearr?: { urlBase?: string };
  }
}

// ---------------------------------------------------------------------------
// URL base / API root
// ---------------------------------------------------------------------------

/** Normalizes a url base to "" or "/something" (leading slash, no trailing slash). */
export function normalizeUrlBase(value: string | null | undefined): string {
  const trimmed = (value ?? '').trim().replace(/\/+$/, '');
  if (!trimmed) return '';
  return trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
}

/** The url base the SPA is served under (window.Dupearr.urlBase, injected by the server). */
export function getUrlBase(): string {
  if (typeof window === 'undefined') return '';
  return normalizeUrlBase(window.Dupearr?.urlBase);
}

/**
 * Resolves the API root reported by initialize.json against the url base: "/api/v1" with url base
 * "/dupearr" → "/dupearr/api/v1"; an apiRoot that already includes the url base is kept as is.
 */
export function resolveApiRoot(apiRoot: string | null | undefined, urlBase: string): string {
  const root = (apiRoot || '/api/v1').replace(/\/+$/, '');
  if (/^https?:\/\//i.test(root)) return root;
  const withSlash = root.startsWith('/') ? root : `/${root}`;
  if (!urlBase || withSlash === urlBase || withSlash.startsWith(`${urlBase}/`)) return withSlash;
  return `${urlBase}${withSlash}`;
}

/** Prefixes a server path (e.g. "/login", "/backup/manual/x.zip") with the url base. */
export function withUrlBase(path: string): string {
  const p = path.startsWith('/') ? path : `/${path}`;
  return `${getUrlBase()}${p}`;
}

interface ApiConfig {
  urlBase: string;
  apiRoot: string;
}

const config: ApiConfig = {
  urlBase: getUrlBase(),
  apiRoot: `${getUrlBase()}/api/v1`,
};

/** Sets the url base / API root (normally done by `bootstrap()`; tests may call it directly). */
export function configureApi(next: Partial<ApiConfig>): void {
  if (next.urlBase !== undefined) config.urlBase = normalizeUrlBase(next.urlBase);
  if (next.apiRoot !== undefined) config.apiRoot = resolveApiRoot(next.apiRoot, config.urlBase);
}

export function getApiConfig(): Readonly<ApiConfig> {
  return { ...config };
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** Error thrown by every API helper. */
export class ApiError extends Error {
  /** HTTP status (0 = network failure / aborted). */
  readonly status: number;
  /** `description` of a `{message, description}` error body. */
  readonly description?: string;
  /** Validation failures of a 400 `[{propertyName, errorMessage}]` body (empty otherwise). */
  readonly validationErrors: ValidationFailure[];
  /** Parsed body (JSON or text), when any. */
  readonly body: unknown;

  constructor(
    message: string,
    opts: { status: number; description?: string; validationErrors?: ValidationFailure[]; body?: unknown },
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = opts.status;
    this.description = opts.description;
    this.validationErrors = opts.validationErrors ?? [];
    this.body = opts.body;
  }

  get isValidation(): boolean {
    return this.validationErrors.length > 0;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  get isNotFound(): boolean {
    return this.status === 404;
  }

  /** 409: the request conflicts with the current state (e.g. a duplicate changed since it was shown). */
  get isConflict(): boolean {
    return this.status === 409;
  }

  /** Validation messages grouped by property name (case-insensitive keys are lower-cased first letter). */
  fieldErrors(): Record<string, string[]> {
    const out: Record<string, string[]> = {};
    for (const v of this.validationErrors) {
      const key = normalizePropertyName(v.propertyName);
      (out[key] ??= []).push(v.errorMessage);
    }
    return out;
  }

  /**
   * Messages for a single top-level field. The match is on the property's first path segment and
   * ignores case, so "Url", "url", "deletionMethods[1]" (→ deletionMethods) and
   * "MaxBytesPerRunGB" (→ maxBytesPerRunGb) all find their field.
   */
  errorsFor(propertyName: string): string[] {
    const wanted = propertyName.toLowerCase();
    return this.validationErrors
      .filter((v) => rootPropertyName(v.propertyName).toLowerCase() === wanted)
      .map((v) => v.errorMessage);
  }
}

function normalizePropertyName(name: string): string {
  if (!name) return '';
  return name.charAt(0).toLowerCase() + name.slice(1);
}

/** First segment of a property path: "deletionMethods[1]" / "settings.url" → "deletionMethods" / "settings". */
export function rootPropertyName(name: string | null | undefined): string {
  return (name ?? '').trim().split(/[.[]/, 1)[0] ?? '';
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** Human readable message for any thrown value. */
export function errorMessage(e: unknown, fallback = 'Something went wrong'): string {
  if (e instanceof ApiError) return e.message || fallback;
  if (e instanceof Error) return e.message || fallback;
  if (typeof e === 'string') return e || fallback;
  return fallback;
}

function isValidationArray(body: unknown): body is ValidationFailure[] {
  return (
    Array.isArray(body) &&
    body.length > 0 &&
    body.every(
      (x) => x !== null && typeof x === 'object' && typeof (x as ValidationFailure).errorMessage === 'string',
    )
  );
}

function isErrorResponse(body: unknown): body is ErrorResponse {
  return (
    body !== null &&
    typeof body === 'object' &&
    !Array.isArray(body) &&
    typeof (body as ErrorResponse).message === 'string'
  );
}

/** Builds an ApiError from an already-parsed error body. */
export function buildApiError(status: number, statusText: string, body: unknown): ApiError {
  if (isValidationArray(body)) {
    const messages = body.map((v) => v.errorMessage).filter(Boolean);
    return new ApiError(messages.join('\n') || 'Validation failed', {
      status,
      validationErrors: body.map((v) => ({
        propertyName: v.propertyName ?? '',
        errorMessage: v.errorMessage,
        ...(v.severity ? { severity: v.severity } : {}),
      })),
      body,
    });
  }
  if (isErrorResponse(body)) {
    return new ApiError(body.message || defaultStatusMessage(status, statusText), {
      status,
      description: body.description || undefined,
      body,
    });
  }
  if (typeof body === 'string' && body.trim()) {
    const text = body.trim();
    return new ApiError(text.length > 300 ? `${text.slice(0, 300)}…` : text, { status, body });
  }
  return new ApiError(defaultStatusMessage(status, statusText), { status, body });
}

function defaultStatusMessage(status: number, statusText: string): string {
  if (status === 401) return 'Unauthorized';
  if (status === 403) return 'Forbidden';
  if (status === 404) return 'Not found';
  if (status === 502) return 'Upstream server error';
  if (status >= 500) return statusText ? `Server error: ${statusText}` : 'Server error';
  return statusText || `Request failed (${status})`;
}

/** Reads a failed Response and turns it into an ApiError (JSON array / {message} / text). */
export async function parseApiError(res: Response): Promise<ApiError> {
  let body: unknown = undefined;
  try {
    const text = await res.text();
    if (text) {
      const ct = res.headers.get('content-type') ?? '';
      if (ct.includes('json') || /^[[{]/.test(text.trim())) {
        try {
          body = JSON.parse(text);
        } catch {
          body = text;
        }
      } else {
        body = text;
      }
    }
  } catch {
    // body unreadable: fall through with undefined
  }
  return buildApiError(res.status, res.statusText, body);
}

// ---------------------------------------------------------------------------
// Unauthorized handling
// ---------------------------------------------------------------------------

type UnauthorizedHandler = (error: ApiError) => void;
const unauthorizedHandlers = new Set<UnauthorizedHandler>();

/** Registers a callback invoked when any API call returns 401 (session expired). Returns an unsubscribe fn. */
export function onUnauthorized(handler: UnauthorizedHandler): () => void {
  unauthorizedHandlers.add(handler);
  return () => {
    unauthorizedHandlers.delete(handler);
  };
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

export type QueryValue = string | number | boolean | null | undefined | readonly (string | number)[];
export type QueryParams = Record<string, QueryValue>;

/** Serializes query params: skips null/undefined/"" and empty arrays; arrays become comma lists. */
export function toQueryString(params?: QueryParams): string {
  if (!params) return '';
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue;
    if (Array.isArray(value)) {
      if (value.length === 0) continue;
      sp.set(key, value.join(','));
    } else {
      sp.set(key, String(value));
    }
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}

/**
 * Absolute (path-absolute) URL of an API endpoint, e.g. apiUrl('/system/status') → "/api/v1/system/status".
 * Also used for <img src>, downloads and the EventSource: same-origin requests that the session
 * cookie authenticates (never put a credential into a URL).
 */
export function apiUrl(path: string, query?: QueryParams): string {
  const p = path.startsWith('/') ? path : `/${path}`;
  return `${config.apiRoot}${p}${toQueryString(query)}`;
}

export interface RequestOptions {
  query?: QueryParams;
  /** JSON-serialized unless it is FormData / Blob / string. */
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  /** Response handling: "json" (default), "text" or "blob". */
  responseType?: 'json' | 'text' | 'blob';
  /** Treat the path as already absolute (skip the API root). */
  absolute?: boolean;
  /** Don't fire the global unauthorized handlers on 401. */
  skipAuthRedirect?: boolean;
}

export interface ApiResponse<T> {
  data: T;
  status: number;
  headers: Headers;
}

/** Low-level request returning data + status + headers (e.g. to read `X-Dupearr-Warning`). */
export async function requestWithMeta<T>(
  method: string,
  path: string,
  opts: RequestOptions = {},
): Promise<ApiResponse<T>> {
  const url = opts.absolute ? `${path}${toQueryString(opts.query)}` : apiUrl(path, opts.query);
  const headers: Record<string, string> = {
    Accept: opts.responseType === 'text' ? 'text/plain, */*' : 'application/json, */*',
    ...opts.headers,
  };

  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    if (
      opts.body instanceof FormData ||
      opts.body instanceof Blob ||
      typeof opts.body === 'string' ||
      opts.body instanceof URLSearchParams
    ) {
      body = opts.body;
    } else {
      body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }
  }

  let res: Response;
  try {
    res = await fetch(url, {
      method,
      headers,
      body,
      signal: opts.signal,
      credentials: 'same-origin',
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new ApiError('Unable to reach Dupearr. Check that the server is running.', { status: 0 });
  }

  if (!res.ok) {
    const error = await parseApiError(res);
    if (res.status === 401 && !opts.skipAuthRedirect) {
      unauthorizedHandlers.forEach((h) => h(error));
    }
    throw error;
  }

  let data: unknown;
  if (opts.responseType === 'blob') {
    data = await res.blob();
  } else if (opts.responseType === 'text') {
    data = await res.text();
  } else if (res.status === 204) {
    data = undefined;
  } else {
    const text = await res.text();
    if (!text) {
      data = undefined;
    } else {
      try {
        data = JSON.parse(text);
      } catch {
        data = text;
      }
    }
  }
  return { data: data as T, status: res.status, headers: res.headers };
}

export async function request<T>(method: string, path: string, opts?: RequestOptions): Promise<T> {
  return (await requestWithMeta<T>(method, path, opts)).data;
}

type BodyOpts = Omit<RequestOptions, 'body'>;

/** Typed helpers. Paths are relative to the API root ("/api/v1"). */
export const api = {
  get: <T>(path: string, query?: QueryParams, opts?: Omit<RequestOptions, 'query'>) =>
    request<T>('GET', path, { ...opts, query }),
  post: <T = void>(path: string, body?: unknown, opts?: BodyOpts) =>
    request<T>('POST', path, { ...opts, body }),
  put: <T = void>(path: string, body?: unknown, opts?: BodyOpts) =>
    request<T>('PUT', path, { ...opts, body }),
  del: <T = void>(path: string, opts?: RequestOptions) => request<T>('DELETE', path, opts),
  /** GET returning text/plain (log files). */
  text: (path: string, query?: QueryParams, opts?: Omit<RequestOptions, 'query' | 'responseType'>) =>
    request<string>('GET', path, { ...opts, query, responseType: 'text' }),
  /** POST multipart/form-data. */
  upload: <T>(path: string, form: FormData, opts?: BodyOpts) =>
    request<T>('POST', path, { ...opts, body: form }),
};

// ---------------------------------------------------------------------------
// Bootstrap & auth (no API key required)
// ---------------------------------------------------------------------------

export type BootstrapResult =
  | { status: 'ready'; init: InitializeResponse }
  | { status: 'login'; authStatus?: AuthStatus }
  | { status: 'setup'; authStatus: AuthStatus };

/** GET /api/v1/auth/status (unauthenticated). */
export function getAuthStatus(signal?: AbortSignal): Promise<AuthStatus> {
  return request<AuthStatus>('GET', `${getUrlBase()}/api/v1/auth/status`, {
    absolute: true,
    signal,
    skipAuthRedirect: true,
  });
}

/**
 * Loads initialize.json (session cookie) and configures the client. Resolves to "login"/"setup"
 * when the user isn't authenticated; rejects on network/server errors.
 */
export async function bootstrap(signal?: AbortSignal): Promise<BootstrapResult> {
  const urlBase = getUrlBase();
  try {
    const init = await request<InitializeResponse>('GET', `${urlBase}/initialize.json`, {
      absolute: true,
      signal,
      skipAuthRedirect: true,
    });
    const effectiveBase = normalizeUrlBase(init.urlBase ?? urlBase);
    configureApi({ urlBase: effectiveBase, apiRoot: init.apiRoot });
    return { status: 'ready', init };
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
      try {
        const authStatus = await getAuthStatus(signal);
        if (authStatus.setupRequired) return { status: 'setup', authStatus };
        return { status: 'login', authStatus };
      } catch {
        return { status: 'login' };
      }
    }
    throw e;
  }
}

/** POST {urlBase}/login (JSON). Throws ApiError(401) on bad credentials. */
export async function login(body: LoginRequest): Promise<void> {
  await request<unknown>('POST', `${getUrlBase()}/login`, {
    absolute: true,
    body,
    skipAuthRedirect: true,
  });
}

/** POST {urlBase}/logout, then hard-navigates to the login page. */
export async function logout(): Promise<void> {
  try {
    await request<unknown>('POST', `${getUrlBase()}/logout`, { absolute: true, skipAuthRedirect: true });
  } catch {
    // ignore: we navigate away regardless
  }
  window.location.assign(`${getUrlBase()}/login`);
}

/** POST /api/v1/auth/setup (first run; unauthenticated, local clients only). */
export function setupAuth(body: AuthSetupRequest): Promise<unknown> {
  return request<unknown>('POST', `${getUrlBase()}/api/v1/auth/setup`, {
    absolute: true,
    body,
    skipAuthRedirect: true,
  });
}
