import { describe, expect, it } from 'vitest';
import type { Action } from '@/api/types';
import { HISTORY_EVENT_LABELS } from '@/lib/constants';
import {
  HISTORY_EVENT_ICONS,
  canCancelQueueItem,
  canRestoreAction,
  elapsedMs,
  normalizeHistoryData,
  prettyJson,
  summarizeHistoryData,
} from './activityMeta';
import { baseName } from './PathList';

function action(overrides: Partial<Action> = {}): Action {
  return {
    id: 1,
    groupId: 1,
    groupFileId: 1,
    versionKey: 'plex:1:1',
    title: 'Heat (1995)',
    paths: ['/data/movies/Heat.mkv'],
    size: 1,
    method: 'filesystem',
    status: 'succeeded',
    dryRun: false,
    recyclePath: '/recycle/2026-09-22/data/movies/Heat.mkv',
    permanent: false,
    createdAt: '2026-09-22T00:00:00Z',
    ...overrides,
  };
}

describe('HISTORY_EVENT_ICONS', () => {
  it('has an icon for every event type', () => {
    for (const type of Object.keys(HISTORY_EVENT_LABELS)) {
      expect(HISTORY_EVENT_ICONS[type as keyof typeof HISTORY_EVENT_ICONS]).toBeTruthy();
    }
  });
});

describe('normalizeHistoryData', () => {
  it.each([
    [null, null],
    [undefined, null],
    ['', null],
    ['   ', null],
    [{}, null],
    [[], null],
    ['{}', null],
    ['{"a":1}', { a: 1 }],
    ['not json', 'not json'],
    [{ a: 1 }, { a: 1 }],
    [[1], [1]],
    [0, 0],
  ] as const)('%j → %j', (input, expected) => {
    expect(normalizeHistoryData(input)).toEqual(expected);
  });
});

describe('summarizeHistoryData', () => {
  it('extracts known keys and ignores wrong types', () => {
    expect(
      summarizeHistoryData({
        paths: ['/a.mkv', 42, '', '/b.mkv'],
        path: '/a.mkv',
        method: 'arr',
        permanent: true,
        recyclePath: '',
        dryRun: false,
        size: 1024,
        message: 'Deleted via Radarr',
        extra: { nested: true },
      }),
    ).toEqual({
      paths: ['/a.mkv', '/b.mkv'],
      method: 'arr',
      permanent: true,
      dryRun: false,
      size: 1024,
      message: 'Deleted via Radarr',
    });
  });

  it('accepts JSON strings and tolerates junk', () => {
    expect(summarizeHistoryData('{"path":"/x.mkv","permanent":false}')).toEqual({
      paths: ['/x.mkv'],
      permanent: false,
    });
    expect(summarizeHistoryData({ permanent: 'yes', size: 'big', method: 3 })).toEqual({ paths: [] });
    expect(summarizeHistoryData([1, 2])).toEqual({ paths: [] });
    expect(summarizeHistoryData(null)).toEqual({ paths: [] });
  });
});

describe('prettyJson', () => {
  it('pretty prints and never throws', () => {
    expect(prettyJson({ a: [1] })).toBe('{\n  "a": [\n    1\n  ]\n}');
    expect(prettyJson('raw text')).toBe('raw text');
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(prettyJson(cyclic)).toBe('[object Object]');
    expect(prettyJson(undefined)).toBe('undefined');
  });
});

describe('canRestoreAction', () => {
  it('only allows succeeded, real filesystem removals with a recycle path', () => {
    expect(canRestoreAction(action())).toBe(true);
    expect(canRestoreAction(action({ status: 'dry_run' }))).toBe(false);
    expect(canRestoreAction(action({ dryRun: true }))).toBe(false);
    expect(canRestoreAction(action({ method: 'arr' }))).toBe(false);
    expect(canRestoreAction(action({ method: 'plex' }))).toBe(false);
    expect(canRestoreAction(action({ recyclePath: '' }))).toBe(false);
    expect(canRestoreAction(action({ recyclePath: '  ' }))).toBe(false);
    expect(canRestoreAction(action({ recyclePath: undefined }))).toBe(false);
    expect(canRestoreAction(action({ permanent: true }))).toBe(false);
    expect(canRestoreAction(action({ status: 'failed' }))).toBe(false);
  });
});

describe('canCancelQueueItem', () => {
  it('only allows pending actions', () => {
    expect(canCancelQueueItem(action({ status: 'pending' }))).toBe(true);
    expect(canCancelQueueItem(action({ status: 'running' }))).toBe(false);
    expect(canCancelQueueItem(action({ status: 'succeeded' }))).toBe(false);
  });
});

describe('elapsedMs', () => {
  it('computes durations and rejects missing/invalid/negative spans', () => {
    expect(elapsedMs('2026-09-22T10:00:00Z', '2026-09-22T10:01:30Z')).toBe(90_000);
    expect(elapsedMs('2026-09-22T10:00:00Z', Date.parse('2026-09-22T10:00:05Z'))).toBe(5000);
    expect(elapsedMs('2026-09-22T10:00:00Z', '2026-09-22T09:00:00Z')).toBeNull();
    expect(elapsedMs('0001-01-01T00:00:00Z', '2026-09-22T09:00:00Z')).toBeNull();
    expect(elapsedMs(null, '2026-09-22T09:00:00Z')).toBeNull();
    expect(elapsedMs('2026-09-22T10:00:00Z', null)).toBeNull();
    expect(elapsedMs('garbage', '2026-09-22T09:00:00Z')).toBeNull();
  });
});

describe('baseName', () => {
  it.each([
    ['/data/movies/Heat (1995)/Heat.mkv', 'Heat.mkv'],
    ['C:\\Movies\\Heat.mkv', 'Heat.mkv'],
    ['/data/movies/', 'movies'],
    ['Heat.mkv', 'Heat.mkv'],
  ])('%s → %s', (input, expected) => {
    expect(baseName(input)).toBe(expected);
  });
});
