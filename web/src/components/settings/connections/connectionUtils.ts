/**
 * Pure helpers shared by the connection settings pages (Media Servers, Applications, Connect):
 * URL validation, masked-secret handling and API error → form error mapping.
 */
import { isApiError, rootPropertyName } from '@/api/client';
import { MASKED_SECRET, humanize } from '@/lib/constants';

/** Field name → error messages (the shape FormGroup's `errors` accepts per field). */
export type FieldErrors = Record<string, string[]>;

/** True when `value` is the "********" placeholder the API returns instead of a stored secret. */
export function isMaskedSecret(value: unknown): boolean {
  return value === MASKED_SECRET;
}

/** Removes trailing slashes (and surrounding whitespace) from a base URL: "http://x:7878/" → "http://x:7878". */
export function trimTrailingSlash(url: string): string {
  return url.trim().replace(/\/+$/, '');
}

export interface UrlValidationOptions {
  /** Field label used in messages (default "URL"). */
  label?: string;
  /** Extra checks for app-specific mistakes; return a message to reject. */
  extra?: (url: URL) => string | null;
}

/**
 * Validates a user-entered base URL: required, parseable, http/https scheme, host present and no
 * embedded credentials. Returns an error message or null when valid.
 */
export function validateHttpUrl(value: string | null | undefined, opts: UrlValidationOptions = {}): string | null {
  const label = opts.label ?? 'URL';
  const trimmed = (value ?? '').trim();
  if (!trimmed) return `${label} is required`;
  // "192.168.1.10:32400" / "plex:32400" / "javascript:…" — no http(s):// scheme.
  if (!/^https?:\/\//i.test(trimmed)) return `${label} must start with http:// or https://`;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return `${label} must be a full address, e.g. http://192.168.1.10:32400`;
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return `${label} must start with http:// or https://`;
  }
  if (!parsed.hostname) return `${label} must include a host name or IP address`;
  if (parsed.username || parsed.password) return `${label} must not contain a user name or password`;
  if (parsed.search || parsed.hash) return `${label} must not contain a query string or fragment`;
  return opts.extra?.(parsed) ?? null;
}

/** Plex: people often paste the Plex Web address (…/web/index.html) instead of the server URL. */
export function plexUrlCheck(url: URL): string | null {
  if (/^\/web(\/|$)/i.test(url.pathname)) {
    return 'Use the server address without "/web", e.g. http://192.168.1.10:32400';
  }
  return null;
}

/** Radarr/Sonarr: the URL is the base address (incl. URL base), never the API path. */
export function arrUrlCheck(url: URL): string | null {
  if (/\/api(\/v\d+)?\/?$/i.test(url.pathname)) {
    return 'Do not include "/api" — enter the base address (and URL base if any), e.g. http://radarr:7878';
  }
  return null;
}

/** Lower-case `host[:port]` of a URL (the port only when it isn't the scheme default), or null. */
export function urlHost(value: string | null | undefined): string | null {
  const s = (value ?? '').trim();
  if (!s) return null;
  try {
    const host = new URL(s).host.toLowerCase();
    return host || null;
  } catch {
    return null;
  }
}

/**
 * True when `next` addresses a different server than `previous`: another host or port, a
 * downgrade from https to http, or (for values that aren't URLs, e.g. an SMTP host name) different
 * text. An upgrade from http to https on the same host:port is not a change.
 */
export function addressChanged(previous: string | null | undefined, next: string | null | undefined): boolean {
  const before = urlHost(previous);
  const after = urlHost(next);
  if (before !== null && after !== null) {
    if (before !== after) return true;
    return /^https:/i.test((previous ?? '').trim()) && /^http:/i.test((next ?? '').trim());
  }
  return (previous ?? '').trim().toLowerCase() !== (next ?? '').trim().toLowerCase();
}

/**
 * Error for a stored secret that is still masked while the connection's address changed. The API
 * substitutes the stored value for "********" (docs/API.md → Secrets), so without this check
 * editing only the URL would send the saved token / API key / password to whatever host the new
 * URL names (a typo, or someone else's server). The user must enter the secret again.
 */
export function secretAtNewAddressMessage(label: string): string {
  return `Enter the ${label} again: the address changed, and a stored ${label} is only sent to the server it was saved for.`;
}

/** Plex tokens copied from a JWT-based sign-in expire after 7 days (docs/research/plex-api.md §3). */
export function looksLikeJwt(token: string): boolean {
  return /^eyJ[\w-]+\.[\w-]+\.[\w-]+$/.test(token.trim());
}

/** Validation errors of an ApiError grouped by (lower-camel) property name; {} for other errors. */
export function apiFieldErrors(error: unknown): FieldErrors {
  if (!isApiError(error) || !error.isValidation) return {};
  return error.fieldErrors();
}

/** Default label of a server property path: its last named segment, humanized ("settings.webhookUrl" → "Webhook Url"). */
export function propertyLabel(propertyName: string): string {
  const named = propertyName
    .split(/[.[\]]/)
    .map((p) => p.trim())
    .filter((p) => p && !/^\d+$/.test(p));
  return humanize(named.at(-1) ?? '');
}

/** "Label: message", unless the message already names the field (or there is no label). */
function withPropertyLabel(label: string, message: string): string {
  if (!label || message.toLowerCase().includes(label.toLowerCase())) return message;
  return `${label}: ${message}`;
}

/**
 * Splits an API error into errors for known form fields and a general message for everything
 * else (non-validation errors, or validation failures on properties the form doesn't render).
 * Property names are matched ignoring case, and by their first segment ("deletionMethods[1]" →
 * deletionMethods). General messages about a property are prefixed with its label (`labelFor`,
 * default: the humanized property name) so they still say which setting is wrong.
 */
export function splitApiError(
  error: unknown,
  knownFields: readonly string[],
  labelFor: (propertyName: string) => string | undefined = () => undefined,
): { fields: FieldErrors; general: string[] } {
  if (!error) return { fields: {}, general: [] };
  if (!isApiError(error)) {
    return { fields: {}, general: [error instanceof Error ? error.message : String(error)] };
  }
  if (!error.isValidation) {
    const general = [error.message];
    if (error.description && error.description !== error.message) general.push(error.description);
    return { fields: {}, general };
  }
  const known = new Map(knownFields.map((f) => [f.toLowerCase(), f] as const));
  const fields: FieldErrors = {};
  const general: string[] = [];
  for (const [name, messages] of Object.entries(error.fieldErrors())) {
    const field = name ? (known.get(name.toLowerCase()) ?? known.get(rootPropertyName(name).toLowerCase())) : undefined;
    if (field) {
      fields[field] = [...(fields[field] ?? []), ...messages];
    } else if (name) {
      const label = labelFor(name) ?? propertyLabel(name);
      general.push(...messages.map((m) => withPropertyLabel(label, m)));
    } else {
      general.push(...messages);
    }
  }
  return { fields, general };
}

/**
 * Whether a failed save may be retried with `?forceSave=true` (skip the connection test): only for
 * failures that can be a connection-test result (400/422 and 5xx such as 502 upstream errors).
 * Never for network failures reaching Dupearr itself, auth, not-found or conflict errors. The
 * server still validates a force-saved body, so offering it for a plain validation error is
 * harmless (the retry fails with the same field errors); client-side validation catches most of
 * those before the first request anyway.
 */
export function canForceSave(error: unknown): boolean {
  if (!isApiError(error)) return false;
  return error.status === 400 || error.status === 422 || error.status >= 500;
}

/** The save alert's message when every error belongs to a form field. */
export const CORRECT_FIELDS_MESSAGE = 'Please correct the highlighted fields.';

export interface SaveErrorSummary {
  /** Errors to show under known form fields. */
  fields: FieldErrors;
  /** Messages for the error alert (never empty when there is an error). */
  messages: string[];
  /** Offer "Save anyway" (forceSave). */
  canForce: boolean;
}

/** Everything a connection modal needs to render a failed save (see {@link splitApiError} for `labelFor`). */
export function summarizeSaveError(
  error: unknown,
  knownFields: readonly string[],
  labelFor?: (propertyName: string) => string | undefined,
): SaveErrorSummary {
  if (!error) return { fields: {}, messages: [], canForce: false };
  const { fields, general } = splitApiError(error, knownFields, labelFor);
  let messages = general;
  if (messages.length === 0) {
    messages = Object.keys(fields).length > 0 ? [CORRECT_FIELDS_MESSAGE] : ['Unable to save'];
  }
  return { fields, messages, canForce: canForceSave(error) };
}

/** Merges field error maps (client-side + server-side), de-duplicating messages. */
export function mergeFieldErrors(...maps: FieldErrors[]): FieldErrors {
  const out: FieldErrors = {};
  for (const map of maps) {
    for (const [k, v] of Object.entries(map)) {
      const list = (out[k] ??= []);
      for (const msg of v) if (!list.includes(msg)) list.push(msg);
    }
  }
  return out;
}

/** True when at least one field has an error. */
export function hasErrors(errors: FieldErrors): boolean {
  return Object.values(errors).some((v) => v.length > 0);
}

/** Only http(s) links are rendered as clickable (schema info links come from the server). */
export function safeExternalUrl(value: string | null | undefined): string | null {
  if (!value) return null;
  try {
    const u = new URL(value);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.toString() : null;
  } catch {
    return null;
  }
}
