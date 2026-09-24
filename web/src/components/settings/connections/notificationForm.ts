/**
 * Schema-driven notification settings (Settings → Connect): defaults, validation and the payload
 * sent to the API. Provider fields come from GET /notification/schema (notifications.FieldSchema).
 */
import type { FieldSchema, NotificationTrigger, NotificationTriggerOption, ProviderSchema } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { addressChanged, isMaskedSecret, secretAtNewAddressMessage, type FieldErrors } from './connectionUtils';

export type SettingsValues = Record<string, unknown>;

/** Secret fields (passwords, tokens, webhook URLs with keys) are masked "********" by the API. */
export function isSecretField(field: FieldSchema): boolean {
  return !!field.secret || field.type === 'password';
}

/**
 * The webhook "headers" textarea ("Key: Value" per line). The API masks each credential-bearing
 * value on its own line ("Authorization: ********"); sending such a line back unchanged keeps the
 * stored value (matched by header name), as long as the destination URL is unchanged.
 */
export function isHeaderListField(field: FieldSchema): boolean {
  return field.type === 'textarea' && field.name === 'headers';
}

/** True when a header list still holds a hidden stored value: a "Key: ********" line (commented out or not) or a hidden line. */
export function hasMaskedHeaderValues(value: unknown): boolean {
  if (typeof value !== 'string' || !value.includes(MASKED_SECRET)) return false;
  return value.split('\n').some((line) => {
    const t = line.trim();
    if (t === MASKED_SECRET) return true;
    const colon = t.indexOf(':');
    return colon > 0 && t.slice(colon + 1).trim() === MASKED_SECRET;
  });
}

/**
 * True when the field's current value stands for a stored secret the server fills in: a masked
 * secret field, or a header list with masked values. Such values are only restored while the
 * connection's destination is unchanged.
 */
export function holdsStoredSecret(field: FieldSchema, value: unknown): boolean {
  if (isSecretField(field)) return isMaskedSecret(value);
  if (isHeaderListField(field)) return hasMaskedHeaderValues(value);
  return false;
}

/** Fields that can hold a stored (masked) secret: secret fields and header lists. */
export function canHoldStoredSecret(field: FieldSchema): boolean {
  return isSecretField(field) || isHeaderListField(field);
}

/**
 * "Enter it again" message for a field whose stored secret can't be reused at a new address (see
 * {@link maskedSecretsAtNewAddress}).
 */
export function storedSecretReentryMessage(field: FieldSchema): string {
  const label = field.label || field.name;
  if (isHeaderListField(field)) {
    return `Re-enter the header values shown as ${MASKED_SECRET}: the address changed, and stored header values are only sent to the server they were saved for.`;
  }
  return secretAtNewAddressMessage(label);
}

/** Hint for a server error on a field that still holds a stored (masked) secret. */
export function storedSecretRefusedMessage(field: FieldSchema): string {
  const what = isHeaderListField(field) ? `the header values shown as ${MASKED_SECRET}` : `the ${field.label || field.name}`;
  return `Re-enter ${what}: a stored value is only reused while the server address is unchanged.`;
}

/**
 * Adds a clear "re-enter" message to server errors on fields that still hold a stored secret (the
 * server refuses a mask it can't restore, e.g. after the URL/host changed — 400 on that field).
 * Server messages that already ask for re-entry are left alone.
 */
export function withReentryHints(
  schema: ProviderSchema | null | undefined,
  values: SettingsValues,
  serverErrors: FieldErrors,
): FieldErrors {
  const out: FieldErrors = { ...serverErrors };
  for (const f of schema?.fields ?? []) {
    const messages = out[f.name];
    if (!messages?.length || !holdsStoredSecret(f, values[f.name])) continue;
    if (messages.some((m) => /re-?enter|masked|hidden/i.test(m))) continue;
    out[f.name] = [...messages, storedSecretRefusedMessage(f)];
  }
  return out;
}

/** Default value of one field: the schema default, else false/null/"" by type. */
export function fieldDefault(field: FieldSchema): unknown {
  if (field.default !== undefined && field.default !== null) return field.default;
  switch (field.type) {
    case 'checkbox':
      return false;
    case 'number':
      return null;
    default:
      return '';
  }
}

/** Settings of a new connection of this provider. */
export function defaultSettings(schema: ProviderSchema | null | undefined): SettingsValues {
  const out: SettingsValues = {};
  for (const f of schema?.fields ?? []) out[f.name] = fieldDefault(f);
  return out;
}

/**
 * Settings to edit: stored values over schema defaults. Unknown keys the server sent are kept so a
 * save never drops data the UI doesn't know about; `null`/non-object settings are tolerated.
 */
export function initialSettings(schema: ProviderSchema | null | undefined, stored: unknown): SettingsValues {
  const existing =
    stored !== null && typeof stored === 'object' && !Array.isArray(stored) ? (stored as SettingsValues) : {};
  return { ...defaultSettings(schema), ...existing };
}

export function isEmptyValue(value: unknown): boolean {
  return value === undefined || value === null || (typeof value === 'string' && value.trim() === '');
}

/** Coerces a form value to a number (null when empty/invalid). */
export function toNumber(value: unknown): number | null {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value === 'string' && value.trim() !== '') {
    const n = Number(value.trim());
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

/** Client-side validation of provider settings (required, numbers, URLs, select options). */
export function validateSettings(schema: ProviderSchema | null | undefined, values: SettingsValues): FieldErrors {
  const errors: FieldErrors = {};
  for (const f of schema?.fields ?? []) {
    const v = values[f.name];
    const label = f.label || f.name;
    if (f.type === 'checkbox') continue;
    if (isEmptyValue(v)) {
      if (f.required) errors[f.name] = [`${label} is required`];
      continue;
    }
    if (isMaskedSecret(v)) continue; // stored secret, kept by the server
    if (f.type === 'number' && toNumber(v) === null) {
      errors[f.name] = [`${label} must be a number`];
    } else if (f.type === 'url') {
      // Every provider URL (webhooks, Gotify/ntfy/Apprise servers) is an http(s) endpoint.
      let protocol = '';
      try {
        protocol = new URL(String(v).trim()).protocol;
      } catch {
        protocol = '';
      }
      if (protocol !== 'http:' && protocol !== 'https:') {
        errors[f.name] = [`${label} must be a full http:// or https:// URL, e.g. https://example.com/…`];
      }
    } else if (f.type === 'select' && f.options && f.options.length > 0 && !f.options.includes(String(v))) {
      errors[f.name] = [`${label} must be one of: ${f.options.join(', ')}`];
    }
  }
  return errors;
}

/**
 * Settings payload: numbers coerced, text trimmed (never secrets — a password may legitimately
 * contain spaces), masked secrets sent back unchanged so the server keeps the stored value.
 * Textareas (the webhook header list included) are sent verbatim: its "Key: ********" lines
 * must reach the server exactly as received so the stored header values are kept.
 */
export function toSubmitSettings(schema: ProviderSchema | null | undefined, values: SettingsValues): SettingsValues {
  const out: SettingsValues = { ...values };
  for (const f of schema?.fields ?? []) {
    const v = values[f.name];
    if (isMaskedSecret(v)) continue;
    switch (f.type) {
      case 'number':
        out[f.name] = toNumber(v);
        break;
      case 'checkbox':
        out[f.name] = v === true || v === 'true';
        break;
      case 'text':
      case 'url':
      case 'select':
        if (typeof v === 'string' && !isSecretField(f)) out[f.name] = v.trim();
        break;
      default:
        break;
    }
  }
  return out;
}

/** Fields holding the provider's server address: every URL field and the SMTP `host` and `port`. */
export function isAddressField(field: FieldSchema): boolean {
  const name = field.name.toLowerCase();
  return field.type === 'url' || name === 'host' || (name === 'port' && field.type === 'number');
}

function effectiveValue(field: FieldSchema, value: unknown): string {
  const v = isEmptyValue(value) ? fieldDefault(field) : value;
  return v === null || v === undefined ? '' : String(v);
}

/** True when an address field's value moved, compared the way the server binds stored secrets. */
function addressFieldMoved(field: FieldSchema, prev: unknown, next: unknown): boolean {
  if (field.type === 'number') return toNumber(effectiveValue(field, prev)) !== toNumber(effectiveValue(field, next));
  const before = effectiveValue(field, prev).trim();
  const after = effectiveValue(field, next).trim();
  // The server only restores stored secrets for the exact same destination (URLs compared as
  // text, path included — webhook services give each tenant a path; hosts ignoring case).
  if (field.type === 'url') return before !== after;
  return addressChanged(before, after);
}

/**
 * Names of fields still holding a stored secret — a "********" secret field, or header lines
 * shown as "Key: ********" — although an address field (a URL, or the SMTP host/port) now
 * differs from the stored connection. The server only reuses stored secrets for the same
 * destination, so saving/testing such a form fails: the user has to enter those values again
 * (see storedSecretReentryMessage). Empty for new connections (`stored` null) and when no
 * address changed.
 */
export function maskedSecretsAtNewAddress(
  schema: ProviderSchema | null | undefined,
  stored: unknown,
  values: SettingsValues,
): string[] {
  if (!schema || stored === null || typeof stored !== 'object' || Array.isArray(stored)) return [];
  const before = stored as SettingsValues;
  const moved = schema.fields.some((f) => {
    if (!isAddressField(f)) return false;
    const prev = before[f.name];
    const next = values[f.name];
    if (isMaskedSecret(prev) || isMaskedSecret(next)) return isMaskedSecret(prev) !== isMaskedSecret(next);
    return addressFieldMoved(f, prev, next);
  });
  if (!moved) return [];
  return schema.fields.filter((f) => holdsStoredSecret(f, values[f.name])).map((f) => f.name);
}

/**
 * Maps server validation errors to settings fields. Accepts "webhookUrl", "settings.webhookUrl"
 * and "settings[webhookUrl]" (case-insensitive first letter); returns [fieldErrors, rest].
 */
export function splitSettingsErrors(
  errors: FieldErrors,
  schema: ProviderSchema | null | undefined,
): [FieldErrors, FieldErrors] {
  const names = new Map((schema?.fields ?? []).map((f) => [f.name.toLowerCase(), f.name]));
  const settings: FieldErrors = {};
  const rest: FieldErrors = {};
  for (const [key, messages] of Object.entries(errors)) {
    const m = /^settings(?:\.|\[)([^\]]+)\]?$/i.exec(key);
    const candidate = (m ? m[1]! : key).toLowerCase();
    const field = names.get(candidate);
    if (field) settings[field] = [...(settings[field] ?? []), ...messages];
    else rest[key] = messages;
  }
  return [settings, rest];
}

/** Triggers pre-selected for a new connection: everything except the (noisy) scan summary. */
export function defaultTriggers(options: readonly NotificationTriggerOption[] | null | undefined): NotificationTrigger[] {
  return (options ?? []).map((o) => o.value).filter((v) => v !== 'onScanCompleted');
}

/** "On Duplicates Found, On File Deleted +2" for cards. */
export function triggersSummary(
  triggers: readonly string[] | null | undefined,
  labels: (value: string) => string,
  max = 2,
): string {
  const list = triggers ?? [];
  if (list.length === 0) return 'No triggers';
  const shown = list.slice(0, max).map(labels).join(', ');
  return list.length > max ? `${shown} +${list.length - max}` : shown;
}
