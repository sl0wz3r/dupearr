/**
 * Duplicates list state ⇄ URL query string (`/?search=…&status=…&page=…`).
 *
 * Keeping filters/sort/page in the URL means the header search (which navigates to `/?search=`)
 * just works, the back button from a detail page restores the list, and views are shareable.
 * Defaults are omitted from the URL. Every value is validated — unknown values fall back to the
 * default rather than being sent to the server.
 */
import type {
  DuplicateListParams,
  DuplicateSortKey,
  GroupFlag,
  GroupStatus,
  Id,
  MediaType,
  SortDirection,
} from '@/api/types';
import { GROUP_FLAG_LABELS, GROUP_STATUSES, OPEN_GROUP_STATUSES, PAGE_SIZE_OPTIONS } from '@/lib/constants';
import { readStorage, writeStorage } from '@/lib/storage';

export const SORT_KEYS: readonly DuplicateSortKey[] = ['title', 'lastSeenAt', 'firstSeenAt', 'reclaimableBytes', 'status'];
export const DEFAULT_SORT_KEY: DuplicateSortKey = 'lastSeenAt';
export const DEFAULT_SORT_DIRECTION: SortDirection = 'descending';

/** `?status=all` = no status filter; absent = the open statuses. */
export const STATUS_ALL = 'all';

export interface DuplicateFilters {
  /** Empty = every status. */
  status: GroupStatus[];
  mediaType?: MediaType;
  libraryId?: Id;
  flag?: GroupFlag;
  search?: string;
}

export interface DuplicateListState extends DuplicateFilters {
  page: number;
  sortKey: DuplicateSortKey;
  sortDirection: SortDirection;
}

export const DEFAULT_LIST_STATE: DuplicateListState = {
  status: [...OPEN_GROUP_STATUSES],
  page: 1,
  sortKey: DEFAULT_SORT_KEY,
  sortDirection: DEFAULT_SORT_DIRECTION,
};

const STATUS_SET = new Set<string>(GROUP_STATUSES);
const FLAG_SET = new Set<string>(Object.keys(GROUP_FLAG_LABELS));

function positiveInt(raw: string | null): number | undefined {
  if (!raw || !/^\d{1,9}$/.test(raw)) return undefined;
  const n = Number(raw);
  return n > 0 ? n : undefined;
}

/** Status list in canonical (GROUP_STATUSES) order, de-duplicated. */
export function normalizeStatuses(statuses: readonly string[]): GroupStatus[] {
  const wanted = new Set(statuses.filter((s) => STATUS_SET.has(s)));
  return GROUP_STATUSES.filter((s) => wanted.has(s));
}

export function sameStatuses(a: readonly GroupStatus[], b: readonly GroupStatus[]): boolean {
  const na = normalizeStatuses(a);
  const nb = normalizeStatuses(b);
  return na.length === nb.length && na.every((s, i) => s === nb[i]);
}

function parseStatus(raw: string | null): GroupStatus[] {
  if (raw === null) return [...OPEN_GROUP_STATUSES];
  if (raw.trim().toLowerCase() === STATUS_ALL) return [];
  const list = normalizeStatuses(raw.split(',').map((s) => s.trim()));
  // Nothing valid → fall back to the default rather than silently showing everything.
  return list.length > 0 ? list : [...OPEN_GROUP_STATUSES];
}

/** Reads the list state from the URL (invalid values → defaults). */
export function parseListState(params: URLSearchParams): DuplicateListState {
  const mediaType = params.get('mediaType');
  const flag = params.get('flag');
  const sortKey = params.get('sortKey');
  const sortDirection = params.get('sortDirection');
  const search = params.get('search')?.trim();
  return {
    status: parseStatus(params.get('status')),
    mediaType: mediaType === 'movie' || mediaType === 'episode' ? mediaType : undefined,
    libraryId: positiveInt(params.get('libraryId')),
    flag: flag && FLAG_SET.has(flag) ? (flag as GroupFlag) : undefined,
    search: search ? search.slice(0, 200) : undefined,
    page: positiveInt(params.get('page')) ?? 1,
    sortKey: sortKey && (SORT_KEYS as readonly string[]).includes(sortKey) ? (sortKey as DuplicateSortKey) : DEFAULT_SORT_KEY,
    sortDirection:
      sortDirection === 'ascending' || sortDirection === 'descending' ? sortDirection : DEFAULT_SORT_DIRECTION,
  };
}

/** Writes the list state to URL params (defaults omitted). */
export function serializeListState(state: DuplicateListState): URLSearchParams {
  const p = new URLSearchParams();
  if (state.search) p.set('search', state.search);
  const status = normalizeStatuses(state.status);
  if (status.length === 0) p.set('status', STATUS_ALL);
  else if (!sameStatuses(status, OPEN_GROUP_STATUSES)) p.set('status', status.join(','));
  if (state.mediaType) p.set('mediaType', state.mediaType);
  if (state.libraryId) p.set('libraryId', String(state.libraryId));
  if (state.flag) p.set('flag', state.flag);
  if (state.sortKey !== DEFAULT_SORT_KEY) p.set('sortKey', state.sortKey);
  if (state.sortDirection !== DEFAULT_SORT_DIRECTION) p.set('sortDirection', state.sortDirection);
  if (state.page > 1) p.set('page', String(state.page));
  return p;
}

/** True when any filter differs from the defaults (search included). */
export function hasActiveFilters(state: DuplicateFilters): boolean {
  return (
    !sameStatuses(state.status, OPEN_GROUP_STATUSES) ||
    !!state.mediaType ||
    !!state.libraryId ||
    !!state.flag ||
    !!state.search
  );
}

/** Query params for `useDuplicates`. */
export function toQueryParams(state: DuplicateListState, pageSize: number): DuplicateListParams {
  return {
    page: state.page,
    pageSize,
    sortKey: state.sortKey,
    sortDirection: state.sortDirection,
    status: state.status.length > 0 ? normalizeStatuses(state.status) : undefined,
    mediaType: state.mediaType,
    libraryId: state.libraryId,
    flag: state.flag,
    search: state.search,
  };
}

// ---------------------------------------------------------------------------
// Per-browser conveniences (localStorage; never throws)
// ---------------------------------------------------------------------------

export type DuplicatesView = 'table' | 'posters';

export const VIEW_STORAGE_KEY = 'dupearr.duplicates.view';
export const PAGE_SIZE_STORAGE_KEY = 'dupearr.duplicates.pageSize';
export const DEFAULT_PAGE_SIZE = PAGE_SIZE_OPTIONS[0] ?? 20;

export function loadView(): DuplicatesView {
  return readStorage(VIEW_STORAGE_KEY) === 'posters' ? 'posters' : 'table';
}

export function saveView(view: DuplicatesView): void {
  writeStorage(VIEW_STORAGE_KEY, view);
}

export function loadPageSize(): number {
  const n = Number(readStorage(PAGE_SIZE_STORAGE_KEY));
  return PAGE_SIZE_OPTIONS.includes(n) ? n : DEFAULT_PAGE_SIZE;
}

export function savePageSize(size: number): void {
  if (PAGE_SIZE_OPTIONS.includes(size)) writeStorage(PAGE_SIZE_STORAGE_KEY, String(size));
}
