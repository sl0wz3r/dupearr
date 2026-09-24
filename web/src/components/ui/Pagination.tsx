import { clsx } from 'clsx';
import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react';
import { PAGE_SIZE_OPTIONS } from '@/lib/constants';
import { formatNumber } from '@/lib/format';
import { IconButton } from './IconButton';
import { Select } from './Select';

export interface PaginationProps {
  /** 1-based. */
  page: number;
  pageSize: number;
  totalRecords: number;
  onPageChange: (page: number) => void;
  /** Shows a page-size dropdown when provided. */
  onPageSizeChange?: (pageSize: number) => void;
  pageSizeOptions?: readonly number[];
  /** Dims controls while a page loads. */
  loading?: boolean;
  className?: string;
}

/**
 * *arr-style pager: « ‹ Page X of Y › » + total records (+ optional page size).
 * Pair with a PagedResponse: `<Pagination page={d.page} pageSize={d.pageSize} totalRecords={d.totalRecords} … />`
 */
export function Pagination({
  page,
  pageSize,
  totalRecords,
  onPageChange,
  onPageSizeChange,
  pageSizeOptions = PAGE_SIZE_OPTIONS,
  loading = false,
  className,
}: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(totalRecords / Math.max(1, pageSize)));
  const current = Math.min(Math.max(1, page), totalPages);
  const go = (p: number) => {
    const next = Math.min(Math.max(1, p), totalPages);
    if (next !== current) onPageChange(next);
  };

  return (
    <nav
      aria-label="Pagination"
      className={clsx('flex flex-wrap items-center justify-between gap-3 py-3 text-sm', loading && 'opacity-70', className)}
    >
      <div className="text-muted">
        Total records: <span className="text-fg">{formatNumber(totalRecords)}</span>
      </div>
      <div className="flex items-center gap-1">
        <IconButton icon={ChevronsLeft} label="First page" size="sm" disabled={current <= 1} onClick={() => go(1)} />
        <IconButton
          icon={ChevronLeft}
          label="Previous page"
          size="sm"
          disabled={current <= 1}
          onClick={() => go(current - 1)}
        />
        <span className="px-2 whitespace-nowrap text-fg" aria-live="polite">
          Page {formatNumber(current)} of {formatNumber(totalPages)}
        </span>
        <IconButton
          icon={ChevronRight}
          label="Next page"
          size="sm"
          disabled={current >= totalPages}
          onClick={() => go(current + 1)}
        />
        <IconButton
          icon={ChevronsRight}
          label="Last page"
          size="sm"
          disabled={current >= totalPages}
          onClick={() => go(totalPages)}
        />
      </div>
      {onPageSizeChange ? (
        <div className="flex items-center gap-2 text-muted">
          <span className="whitespace-nowrap">Per page</span>
          <div className="w-24">
            <Select
              aria-label="Page size"
              options={pageSizeOptions.map((n) => ({ value: String(n), label: String(n) }))}
              value={String(pageSize)}
              onChange={(v) => onPageSizeChange(Number(v))}
            />
          </div>
        </div>
      ) : (
        <div className="hidden sm:block" />
      )}
    </nav>
  );
}
