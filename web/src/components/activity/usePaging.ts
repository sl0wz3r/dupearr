/**
 * Page/page-size state for server-paged tables (Activity → Queue/History, System → Events).
 */
import { useCallback, useEffect, useState } from 'react';
import { DEFAULT_PAGE_SIZE } from '@/lib/constants';

export interface PagingState {
  /** 1-based page. */
  page: number;
  pageSize: number;
  setPage: (page: number) => void;
  /** Changes the page size and goes back to page 1. */
  setPageSize: (pageSize: number) => void;
  /** Goes back to page 1 (call when a filter changes). */
  resetPage: () => void;
}

/** Controlled paging state; `initialPageSize` defaults to 20. */
export function usePaging(initialPageSize: number = DEFAULT_PAGE_SIZE): PagingState {
  const [page, setPageState] = useState(1);
  const [pageSize, setPageSizeState] = useState(initialPageSize);

  const setPage = useCallback((next: number) => {
    setPageState(Number.isFinite(next) && next >= 1 ? Math.floor(next) : 1);
  }, []);
  const setPageSize = useCallback((next: number) => {
    setPageSizeState(Number.isFinite(next) && next >= 1 ? Math.floor(next) : DEFAULT_PAGE_SIZE);
    setPageState(1);
  }, []);
  const resetPage = useCallback(() => setPageState(1), []);

  return { page, pageSize, setPage, setPageSize, resetPage };
}

/** Last valid 1-based page for a record count (always ≥ 1). */
export function lastPage(totalRecords: number, pageSize: number): number {
  if (!Number.isFinite(totalRecords) || totalRecords <= 0) return 1;
  return Math.max(1, Math.ceil(totalRecords / Math.max(1, pageSize)));
}

/**
 * Moves back to the last page when the current one no longer exists, e.g. after cancelling the
 * only item of the last page. `totalRecords` undefined (not loaded yet) is ignored.
 */
export function useClampPage(paging: PagingState, totalRecords: number | undefined): void {
  const { page, pageSize, setPage } = paging;
  useEffect(() => {
    if (totalRecords === undefined) return;
    const last = lastPage(totalRecords, pageSize);
    if (page > last) setPage(last);
  }, [page, pageSize, setPage, totalRecords]);
}
