/**
 * "Sign in with Plex" (plex.tv PIN flow) — pure state machine and helpers.
 *
 * Flow: idle → creating (POST /plex/pin) → waiting (popup open, GET /plex/pin/{id} polled every 2s)
 * → authenticated (account token) → the UI lists servers (GET /plex/servers, X-Plex-Token header) and the user
 * picks a server + connection. `waiting` ends with `timeout` after 5 minutes; `cancel` returns to
 * idle from any in-flight step. Late results of an abandoned attempt are ignored.
 */
import type { PlexConnection, PlexServer } from '@/api/types';

/** Give up waiting for the user to sign in after 5 minutes (docs/API.md: poll every 2s, ≤5 min). */
export const PLEX_PIN_TIMEOUT_MS = 5 * 60_000;

/** window.open target name (re-uses the same popup on repeated attempts). */
export const PLEX_POPUP_NAME = 'dupearr-plex-auth';

export type PlexSignInState =
  | { step: 'idle' }
  | { step: 'creating' }
  | {
      step: 'waiting';
      pinId: number;
      authUrl: string;
      /** The popup could not be opened (blocked) — the UI offers a link instead. */
      popupBlocked: boolean;
      /** Epoch ms when polling started (timeout reference). */
      startedAt: number;
    }
  | { step: 'authenticated'; token: string }
  | { step: 'timeout' }
  | { step: 'error'; message: string };

export type PlexSignInAction =
  | { type: 'start' }
  | { type: 'pinCreated'; pinId: number; authUrl: string; popupBlocked: boolean; now: number }
  | { type: 'authenticated'; token: string }
  | { type: 'failed'; message: string }
  | { type: 'timeout' }
  | { type: 'cancel' }
  | { type: 'reset' };

export const PLEX_SIGN_IN_INITIAL: PlexSignInState = { step: 'idle' };

/**
 * Transition function. Actions that don't apply to the current step are ignored (returns the same
 * state object), so stale async results (e.g. a PIN created after the user cancelled) are harmless.
 */
export function plexSignInReducer(state: PlexSignInState, action: PlexSignInAction): PlexSignInState {
  switch (action.type) {
    case 'start':
      // Restart is allowed from any settled step, not while an attempt is in flight.
      return state.step === 'creating' || state.step === 'waiting' ? state : { step: 'creating' };
    case 'pinCreated':
      if (state.step !== 'creating') return state;
      if (!Number.isFinite(action.pinId) || action.pinId <= 0 || !action.authUrl) {
        return { step: 'error', message: 'Plex returned an invalid sign-in PIN. Please try again.' };
      }
      return {
        step: 'waiting',
        pinId: action.pinId,
        authUrl: action.authUrl,
        popupBlocked: action.popupBlocked,
        startedAt: action.now,
      };
    case 'authenticated':
      if (state.step !== 'waiting') return state;
      if (!action.token) return state;
      return { step: 'authenticated', token: action.token };
    case 'failed':
      if (state.step !== 'creating' && state.step !== 'waiting') return state;
      return { step: 'error', message: action.message || 'Plex sign-in failed' };
    case 'timeout':
      return state.step === 'waiting' ? { step: 'timeout' } : state;
    case 'cancel':
      return state.step === 'creating' || state.step === 'waiting' ? PLEX_SIGN_IN_INITIAL : state;
    case 'reset':
      return PLEX_SIGN_IN_INITIAL;
    default:
      return state;
  }
}

/** Milliseconds left before the waiting step times out (0 when elapsed). */
export function remainingMs(startedAt: number, now: number, timeoutMs = PLEX_PIN_TIMEOUT_MS): number {
  return Math.max(0, startedAt + timeoutMs - now);
}

/** "4:05" */
export function formatCountdown(ms: number): string {
  const total = Math.ceil(Math.max(0, ms) / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

/**
 * Only navigate the popup to https URLs on plex.tv (app.plex.tv/auth#?…). The URL comes from our
 * own server, but refusing anything else keeps a misconfigured/compromised backend from turning
 * the popup into an open redirect.
 */
export function isTrustedPlexAuthUrl(value: string | null | undefined): boolean {
  if (!value) return false;
  try {
    const u = new URL(value);
    return u.protocol === 'https:' && (u.hostname === 'plex.tv' || u.hostname.endsWith('.plex.tv'));
  } catch {
    return false;
  }
}

/** Lower is better: local non-relay → remote non-relay → relay; https before http within a tier. */
export function connectionScore(c: PlexConnection): number {
  const tier = c.relay ? 2 : c.local ? 0 : 1;
  const https = (c.protocol || '').toLowerCase() === 'https' || /^https:/i.test(c.uri || '') ? 0 : 0.5;
  return tier + https;
}

/** Connections sorted best first (stable for equal scores); never mutates the input. */
export function rankConnections(connections: readonly PlexConnection[] | null | undefined): PlexConnection[] {
  return (connections ?? [])
    .filter((c) => !!c && typeof c.uri === 'string' && c.uri !== '')
    .map((c, i) => ({ c, i }))
    .sort((a, b) => connectionScore(a.c) - connectionScore(b.c) || a.i - b.i)
    .map(({ c }) => c);
}

/** The connection Dupearr suggests (best ranked), or null when the server lists none. */
export function recommendedConnection(server: Pick<PlexServer, 'connections'>): PlexConnection | null {
  return rankConnections(server.connections)[0] ?? null;
}

/** Servers the user owns first (only the owner can delete media), then by name. */
export function sortPlexServers(servers: readonly PlexServer[] | null | undefined): PlexServer[] {
  return [...(servers ?? [])].sort(
    (a, b) => Number(b.owned) - Number(a.owned) || (a.name || '').localeCompare(b.name || ''),
  );
}

/** Where a selection's token comes from: the server's own access token, or the plex.tv account token. */
export type PlexTokenSource = 'server' | 'account';

/**
 * The token Dupearr should store for `server`: its own access token from plex.tv.
 *
 * When plex.tv lists none, an owned server falls back to the plex.tv account token. That works on
 * the owner's own server, but it controls the whole Plex account, so the UI warns first
 * (`source: 'account'`). A shared server (owned=false) never gets the account token: the server,
 * and the connection addresses plex.tv lists for it, belong to someone else, who would receive the
 * token with every request. It returns null then, and the user must enter a token manually.
 */
export function plexServerToken(
  server: Pick<PlexServer, 'owned' | 'accessToken'>,
  accountToken: string,
): { token: string; source: PlexTokenSource } | null {
  const own = (server.accessToken ?? '').trim();
  if (own) return { token: own, source: 'server' };
  const account = (accountToken ?? '').trim();
  if (server.owned === true && account) return { token: account, source: 'account' };
  return null;
}

/** What the sign-in hands to the media server form. */
export interface PlexServerSelection {
  name: string;
  url: string;
  /** Server access token, or the account token for an owned server without one (see plexServerToken). */
  token: string;
  tokenSource: PlexTokenSource;
  machineIdentifier: string;
  owned: boolean;
  connection: PlexConnection;
}

/**
 * Builds the form values for a chosen server + connection, or null when there is no token that may
 * be sent to this server (a shared server without its own access token, see plexServerToken).
 */
export function toSelection(
  server: PlexServer,
  connection: PlexConnection,
  accountToken: string,
): PlexServerSelection | null {
  const token = plexServerToken(server, accountToken);
  if (!token) return null;
  return {
    name: server.name || 'Plex',
    url: connection.uri.replace(/\/+$/, ''),
    token: token.token,
    tokenSource: token.source,
    machineIdentifier: server.clientIdentifier || '',
    owned: !!server.owned,
    connection,
  };
}

/** Popup window features centred over the current window. */
export function popupFeatures(width = 600, height = 720): string {
  if (typeof window === 'undefined') return `width=${width},height=${height}`;
  const left = Math.max(0, Math.round(window.screenX + (window.outerWidth - width) / 2));
  const top = Math.max(0, Math.round(window.screenY + (window.outerHeight - height) / 2));
  return `width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`;
}
