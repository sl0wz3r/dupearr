import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { configureApi } from '@/api/client';
import {
    formatUptime,
  logFileDownloadUrl,
  safeExternalUrl,
  shortCommit,
  uptimeSecondsAt,
} from './systemFormat';

describe('formatUptime', () => {
  it.each([
    [0, '0s'],
    [45, '45s'],
    [59.9, '59s'],
    [60, '1m'],
    [3599, '59m'],
    [3600, '1h 0m'],
    [3660, '1h 1m'],
    [86_400, '1d 0h 0m'],
    [93_784, '1d 2h 3m'],
    [-1, ''],
    [Number.NaN, ''],
    [null, ''],
    [undefined, ''],
  ] as const)('%s → %s', (seconds, expected) => {
    expect(formatUptime(seconds)).toBe(expected);
  });
});

describe('uptimeSecondsAt', () => {
  const now = Date.parse('2026-09-22T12:00:00Z');
  it('prefers startTime and falls back to uptimeSeconds', () => {
    expect(uptimeSecondsAt('2026-09-22T11:00:00Z', 5, now)).toBe(3600);
    expect(uptimeSecondsAt('', 42, now)).toBe(42);
    expect(uptimeSecondsAt('0001-01-01T00:00:00Z', 42, now)).toBe(42);
    expect(uptimeSecondsAt('not a date', 42, now)).toBe(42);
    // A start time in the future (clock skew) is not trusted.
    expect(uptimeSecondsAt('2026-09-22T13:00:00Z', 7, now)).toBe(7);
    expect(uptimeSecondsAt(null, null, now)).toBeNull();
    expect(uptimeSecondsAt(undefined, -3, now)).toBeNull();
  });
});

describe('safeExternalUrl', () => {
  it.each([
    ['https://wiki.servarr.com/sonarr/system#x', 'https://wiki.servarr.com/sonarr/system#x'],
    ['https://github.com/sl0wz3r/dupearr', 'https://github.com/sl0wz3r/dupearr'],
    ['javascript:alert(1)', null],
    ['data:text/html,<script>', null],
    ['/relative/path', null],
    ['', null],
    [null, null],
    [undefined, null],
  ] as const)('%s → %s', (input, expected) => {
    expect(safeExternalUrl(input)).toBe(expected);
  });
});

describe('shortCommit', () => {
  it('shortens long hashes and hides unknown values', () => {
    expect(shortCommit('0123456789abcdef0123456789abcdef01234567')).toBe('0123456789');
    expect(shortCommit('abc1234')).toBe('abc1234');
    expect(shortCommit('unknown')).toBe('');
    expect(shortCommit('  ')).toBe('');
    expect(shortCommit(undefined)).toBe('');
  });
});

describe('download urls', () => {
  beforeEach(() => {
    window.Dupearr = { urlBase: '/dupearr' };
    configureApi({ urlBase: '/dupearr', apiRoot: '/api/v1' });
  });
  afterEach(() => {
    delete window.Dupearr;
    configureApi({ urlBase: '', apiRoot: '/api/v1' });
  });

  it('builds log file downloads under the api root', () => {
    expect(logFileDownloadUrl('dupearr.0.txt')).toBe('/dupearr/api/v1/log/file/dupearr.0.txt');
    expect(logFileDownloadUrl('../secret')).toBe('/dupearr/api/v1/log/file/..%2Fsecret');
  });
});
