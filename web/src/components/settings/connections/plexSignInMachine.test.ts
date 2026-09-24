import { describe, expect, it } from 'vitest';
import type { PlexConnection, PlexServer } from '@/api/types';
import {
  formatCountdown,
  isTrustedPlexAuthUrl,
  PLEX_PIN_TIMEOUT_MS,
  PLEX_SIGN_IN_INITIAL,
  plexServerToken,
  plexSignInReducer,
  rankConnections,
  recommendedConnection,
  remainingMs,
  sortPlexServers,
  toSelection,
  type PlexSignInAction,
  type PlexSignInState,
} from './plexSignInMachine';

const AUTH_URL = 'https://app.plex.tv/auth#?clientID=x&code=abcd';
const waiting: PlexSignInState = { step: 'waiting', pinId: 7, authUrl: AUTH_URL, popupBlocked: false, startedAt: 1000 };

function run(actions: PlexSignInAction[], from: PlexSignInState = PLEX_SIGN_IN_INITIAL): PlexSignInState {
  return actions.reduce(plexSignInReducer, from);
}

describe('plexSignInReducer', () => {
  const pinCreated: PlexSignInAction = { type: 'pinCreated', pinId: 7, authUrl: AUTH_URL, popupBlocked: false, now: 1000 };

  it.each<{ name: string; actions: PlexSignInAction[]; from?: PlexSignInState; expected: PlexSignInState }>([
    { name: 'start → creating', actions: [{ type: 'start' }], expected: { step: 'creating' } },
    { name: 'pin created → waiting', actions: [{ type: 'start' }, pinCreated], expected: waiting },
    {
      name: 'popup blocked is remembered',
      actions: [{ type: 'start' }, { ...pinCreated, popupBlocked: true }],
      expected: { ...waiting, popupBlocked: true },
    },
    {
      name: 'authenticated → token',
      actions: [{ type: 'authenticated', token: 'tok' }],
      from: waiting,
      expected: { step: 'authenticated', token: 'tok' },
    },
    { name: 'empty token is ignored', actions: [{ type: 'authenticated', token: '' }], from: waiting, expected: waiting },
    { name: 'timeout while waiting', actions: [{ type: 'timeout' }], from: waiting, expected: { step: 'timeout' } },
    { name: 'cancel while waiting → idle', actions: [{ type: 'cancel' }], from: waiting, expected: { step: 'idle' } },
    { name: 'cancel while creating → idle', actions: [{ type: 'start' }, { type: 'cancel' }], expected: { step: 'idle' } },
    {
      name: 'failure while creating',
      actions: [{ type: 'start' }, { type: 'failed', message: 'boom' }],
      expected: { step: 'error', message: 'boom' },
    },
    {
      name: 'failure while waiting',
      actions: [{ type: 'failed', message: 'expired' }],
      from: waiting,
      expected: { step: 'error', message: 'expired' },
    },
    {
      name: 'invalid pin id → error',
      actions: [{ type: 'start' }, { ...pinCreated, pinId: 0 }],
      expected: { step: 'error', message: 'Plex returned an invalid sign-in PIN. Please try again.' },
    },
    { name: 'retry after timeout', actions: [{ type: 'start' }], from: { step: 'timeout' }, expected: { step: 'creating' } },
    { name: 'retry after error', actions: [{ type: 'start' }], from: { step: 'error', message: 'x' }, expected: { step: 'creating' } },
    { name: 'reset from authenticated', actions: [{ type: 'reset' }], from: { step: 'authenticated', token: 't' }, expected: { step: 'idle' } },
  ])('$name', ({ actions, from, expected }) => {
    expect(run(actions, from)).toEqual(expected);
  });

  it.each<{ name: string; from: PlexSignInState; action: PlexSignInAction }>([
    { name: 'late pin after cancel', from: { step: 'idle' }, action: pinCreated },
    { name: 'late token after timeout', from: { step: 'timeout' }, action: { type: 'authenticated', token: 'tok' } },
    { name: 'token while idle', from: { step: 'idle' }, action: { type: 'authenticated', token: 'tok' } },
    { name: 'timeout while idle', from: { step: 'idle' }, action: { type: 'timeout' } },
    { name: 'timeout after authenticated', from: { step: 'authenticated', token: 't' }, action: { type: 'timeout' } },
    { name: 'failure after authenticated', from: { step: 'authenticated', token: 't' }, action: { type: 'failed', message: 'x' } },
    { name: 'double start while waiting', from: waiting, action: { type: 'start' } },
    { name: 'cancel while idle', from: { step: 'idle' }, action: { type: 'cancel' } },
  ])('ignores stale/out-of-order action: $name', ({ from, action }) => {
    expect(plexSignInReducer(from, action)).toBe(from);
  });
});

describe('timeout helpers', () => {
  it('computes the remaining time and never goes negative', () => {
    expect(remainingMs(1000, 1000)).toBe(PLEX_PIN_TIMEOUT_MS);
    expect(remainingMs(1000, 1000 + PLEX_PIN_TIMEOUT_MS - 1)).toBe(1);
    expect(remainingMs(1000, 1000 + PLEX_PIN_TIMEOUT_MS + 5)).toBe(0);
  });

  it.each([
    [300_000, '5:00'],
    [245_000, '4:05'],
    [999, '0:01'],
    [0, '0:00'],
    [-10, '0:00'],
  ])('formatCountdown(%i) = %s', (ms, expected) => {
    expect(formatCountdown(ms)).toBe(expected);
  });
});

describe('isTrustedPlexAuthUrl', () => {
  it.each([
    [AUTH_URL, true],
    ['https://plex.tv/link/?pin=abcd', true],
    ['http://app.plex.tv/auth#?code=x', false],
    ['https://app.plex.tv.evil.example/auth', false],
    ['https://evilplex.tv/auth', false],
    ['javascript:alert(1)', false],
    ['', false],
    [null, false],
    ['not a url', false],
  ])('%s → %s', (url, expected) => {
    expect(isTrustedPlexAuthUrl(url)).toBe(expected);
  });
});

const conn = (over: Partial<PlexConnection>): PlexConnection => ({
  uri: 'http://10.0.0.2:32400',
  address: '10.0.0.2',
  port: 32400,
  protocol: 'http',
  local: true,
  relay: false,
  ...over,
});

describe('rankConnections', () => {
  it('prefers local non-relay, then remote, then relay; https first within a tier', () => {
    const relay = conn({ uri: 'https://relay.plex.direct:8443', protocol: 'https', local: false, relay: true });
    const remoteHttp = conn({ uri: 'http://1.2.3.4:32400', local: false });
    const remoteHttps = conn({ uri: 'https://1-2-3-4.x.plex.direct:32400', protocol: 'https', local: false });
    const localHttp = conn({ uri: 'http://10.0.0.2:32400' });
    const localHttps = conn({ uri: 'https://10-0-0-2.x.plex.direct:32400', protocol: 'https' });
    const ranked = rankConnections([relay, remoteHttp, localHttp, remoteHttps, localHttps]);
    expect(ranked.map((c) => c.uri)).toEqual([localHttps.uri, localHttp.uri, remoteHttps.uri, remoteHttp.uri, relay.uri]);
  });

  it('drops entries without a uri and tolerates null', () => {
    expect(rankConnections(null)).toEqual([]);
    expect(rankConnections([conn({ uri: '' })])).toEqual([]);
  });

  it('does not mutate its input', () => {
    const input = [conn({ relay: true, local: false, uri: 'https://r' }), conn({})];
    const copy = [...input];
    rankConnections(input);
    expect(input).toEqual(copy);
  });

  it('recommends the best connection or null', () => {
    expect(recommendedConnection({ connections: [] })).toBeNull();
    expect(recommendedConnection({ connections: [conn({ local: false, uri: 'http://r' }), conn({})] })?.uri).toBe(
      'http://10.0.0.2:32400',
    );
  });
});

const server = (over: Partial<PlexServer>): PlexServer => ({
  name: 'Home',
  clientIdentifier: 'abc',
  productVersion: '1.41.0',
  owned: true,
  accessToken: 'server-token',
  connections: [conn({})],
  ...over,
});

describe('servers', () => {
  it('sorts owned servers first, then by name', () => {
    const sorted = sortPlexServers([
      server({ name: 'Zed', owned: false, clientIdentifier: '1' }),
      server({ name: 'Beta', owned: true, clientIdentifier: '2' }),
      server({ name: 'Alpha', owned: false, clientIdentifier: '3' }),
    ]);
    expect(sorted.map((s) => s.name)).toEqual(['Beta', 'Alpha', 'Zed']);
    expect(sortPlexServers(null)).toEqual([]);
  });

  it('builds the selection with the server token', () => {
    const c = conn({ uri: 'http://10.0.0.2:32400/' });
    expect(toSelection(server({}), c, 'account')).toMatchObject({
      name: 'Home',
      url: 'http://10.0.0.2:32400',
      token: 'server-token',
      tokenSource: 'server',
      machineIdentifier: 'abc',
      owned: true,
    });
    expect(toSelection(server({ owned: false, accessToken: 'shared-token' }), c, 'account')).toMatchObject({
      token: 'shared-token',
      tokenSource: 'server',
      owned: false,
    });
  });

  // SEC-033: the plex.tv account token controls the whole Plex account. A shared server belongs to
  // someone else, who would receive it with every request Dupearr makes.
  it.each(['', '   '])('never hands the account token to a shared server without its own token (%j)', (accessToken) => {
    const c = conn({});
    expect(toSelection(server({ owned: false, accessToken }), c, 'account')).toBeNull();
    expect(plexServerToken(server({ owned: false, accessToken }), 'account')).toBeNull();
  });

  it('falls back to the account token only for an owned server, and says so', () => {
    const c = conn({});
    expect(plexServerToken(server({ accessToken: '' }), 'account')).toEqual({ token: 'account', source: 'account' });
    expect(toSelection(server({ accessToken: '' }), c, 'account')).toMatchObject({
      token: 'account',
      tokenSource: 'account',
      owned: true,
    });
    expect(plexServerToken(server({ accessToken: '' }), '')).toBeNull();
    expect(plexServerToken(server({}), 'account')).toEqual({ token: 'server-token', source: 'server' });
  });
});
