import { describe, expect, it } from 'vitest';
import { OPEN_GROUP_STATUSES } from '@/lib/constants';
import {
  DEFAULT_LIST_STATE,
  hasActiveFilters,
  loadPageSize,
  loadView,
  normalizeStatuses,
  PAGE_SIZE_STORAGE_KEY,
  parseListState,
  savePageSize,
  saveView,
  serializeListState,
  toQueryParams,
  VIEW_STORAGE_KEY,
} from './listState';

const parse = (qs: string) => parseListState(new URLSearchParams(qs));

describe('parseListState', () => {
  it.each([
    ['', DEFAULT_LIST_STATE],
    ['status=all', { ...DEFAULT_LIST_STATE, status: [] }],
    ['status=ALL', { ...DEFAULT_LIST_STATE, status: [] }],
    ['status=ignored,pending,pending', { ...DEFAULT_LIST_STATE, status: ['pending', 'ignored'] }],
    ['status=bogus', DEFAULT_LIST_STATE],
    ['status=bogus,review', { ...DEFAULT_LIST_STATE, status: ['review'] }],
    ['mediaType=episode', { ...DEFAULT_LIST_STATE, mediaType: 'episode' }],
    ['mediaType=show', DEFAULT_LIST_STATE],
    ['libraryId=7', { ...DEFAULT_LIST_STATE, libraryId: 7 }],
    ['libraryId=0', DEFAULT_LIST_STATE],
    ['libraryId=-3', DEFAULT_LIST_STATE],
    ['libraryId=1e3', DEFAULT_LIST_STATE],
    ['flag=hardlinked', { ...DEFAULT_LIST_STATE, flag: 'hardlinked' }],
    ['flag=<script>', DEFAULT_LIST_STATE],
    ['search=%20heat%20', { ...DEFAULT_LIST_STATE, search: 'heat' }],
    ['search=%20%20', DEFAULT_LIST_STATE],
    ['page=3', { ...DEFAULT_LIST_STATE, page: 3 }],
    ['page=0', DEFAULT_LIST_STATE],
    ['page=abc', DEFAULT_LIST_STATE],
    ['sortKey=title&sortDirection=ascending', { ...DEFAULT_LIST_STATE, sortKey: 'title', sortDirection: 'ascending' }],
    ['sortKey=path&sortDirection=sideways', DEFAULT_LIST_STATE],
  ])('%j', (qs, expected) => {
    expect(parse(qs)).toEqual({ mediaType: undefined, libraryId: undefined, flag: undefined, search: undefined, ...expected });
  });

  it('caps very long search terms', () => {
    expect(parse(`search=${'x'.repeat(500)}`).search).toHaveLength(200);
  });
});

describe('serializeListState', () => {
  it('omits defaults', () => {
    expect(serializeListState(DEFAULT_LIST_STATE).toString()).toBe('');
  });

  it('round-trips non-default state', () => {
    const state = {
      status: ['pending' as const, 'ignored' as const],
      mediaType: 'movie' as const,
      libraryId: 4,
      flag: 'cross_library' as const,
      search: 'alien',
      page: 2,
      sortKey: 'title' as const,
      sortDirection: 'ascending' as const,
    };
    const qs = serializeListState(state);
    expect(qs.get('status')).toBe('pending,ignored');
    expect(parseListState(qs)).toEqual(state);
  });

  it('writes status=all for an empty status list', () => {
    expect(serializeListState({ ...DEFAULT_LIST_STATE, status: [] }).get('status')).toBe('all');
  });
});

describe('helpers', () => {
  it('detects active filters', () => {
    expect(hasActiveFilters(DEFAULT_LIST_STATE)).toBe(false);
    expect(hasActiveFilters({ ...DEFAULT_LIST_STATE, status: [...OPEN_GROUP_STATUSES].reverse() })).toBe(false);
    expect(hasActiveFilters({ ...DEFAULT_LIST_STATE, status: [] })).toBe(true);
    expect(hasActiveFilters({ ...DEFAULT_LIST_STATE, search: 'x' })).toBe(true);
    expect(hasActiveFilters({ ...DEFAULT_LIST_STATE, flag: 'playing' })).toBe(true);
  });

  it('normalizes statuses into canonical order', () => {
    expect(normalizeStatuses(['failed', 'nope', 'pending', 'failed'])).toEqual(['pending', 'failed']);
  });

  it('builds query params (empty status = no filter)', () => {
    expect(toQueryParams({ ...DEFAULT_LIST_STATE, status: [] }, 50)).toMatchObject({
      page: 1,
      pageSize: 50,
      status: undefined,
      sortKey: 'lastSeenAt',
      sortDirection: 'descending',
    });
    expect(toQueryParams(DEFAULT_LIST_STATE, 20).status).toEqual([...OPEN_GROUP_STATUSES]);
  });

  it('persists view and page size with validation', () => {
    expect(loadView()).toBe('table');
    saveView('posters');
    expect(loadView()).toBe('posters');
    window.localStorage.setItem(VIEW_STORAGE_KEY, 'weird');
    expect(loadView()).toBe('table');

    expect(loadPageSize()).toBe(20);
    savePageSize(100);
    expect(loadPageSize()).toBe(100);
    savePageSize(7); // not an option: ignored
    expect(loadPageSize()).toBe(100);
    window.localStorage.setItem(PAGE_SIZE_STORAGE_KEY, '999999');
    expect(loadPageSize()).toBe(20);
  });
});
