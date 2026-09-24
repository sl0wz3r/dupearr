import { clsx } from 'clsx';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { Fragment, useId, useState, type ReactNode } from 'react';
import { Spinner, type RowId, type TableColumn } from '@/components/ui';

const HIDE: Record<NonNullable<TableColumn<unknown>['hideBelow']>, string> = {
  sm: 'hidden sm:table-cell',
  md: 'hidden md:table-cell',
  lg: 'hidden lg:table-cell',
  xl: 'hidden xl:table-cell',
};

const ALIGN = { left: 'text-left', center: 'text-center', right: 'text-right' } as const;

export interface ExpandableTableProps<T> {
  columns: readonly TableColumn<T>[];
  rows: readonly T[];
  getRowId: (row: T) => RowId;
  /** Rows that can be expanded (default: all). Others get no toggle. */
  isExpandable?: (row: T) => boolean;
  /** Content of the details row shown under an expanded row. */
  renderExpanded: (row: T) => ReactNode;
  /** What the details row shows, for the toggle's accessible name ("Show <x>" / "Hide <x>"; default "details"). */
  detailsLabel?: (row: T) => string;
  loading?: boolean;
  /** Shown when there are no rows and not loading. */
  emptyState?: ReactNode;
  dense?: boolean;
  rowClassName?: (row: T) => string | undefined;
  className?: string;
  'aria-label'?: string;
}

/**
 * Data table (same look as the UI kit `Table`) whose rows can be expanded to show a details row
 * spanning every column — used for history event data and log exceptions.
 */
export function ExpandableTable<T>({
  columns,
  rows,
  getRowId,
  isExpandable,
  renderExpanded,
  detailsLabel,
  loading = false,
  emptyState,
  dense = false,
  rowClassName,
  className,
  ...aria
}: ExpandableTableProps<T>) {
  const baseId = useId();
  const [expanded, setExpanded] = useState<ReadonlySet<RowId>>(() => new Set());
  const cellPad = dense ? 'px-2.5 py-1.5' : 'px-3 py-2.5';
  const colCount = columns.length + 1;

  const toggle = (id: RowId) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <div className={clsx('w-full overflow-x-auto', className)}>
      <table
        aria-label={aria['aria-label']}
        aria-busy={loading || undefined}
        className="w-full border-collapse text-sm"
      >
        <thead>
          <tr className="border-b border-border">
            <th scope="col" className={clsx('w-9', cellPad)}>
              <span className="sr-only">Details</span>
            </th>
            {columns.map((col) => (
              <th
                key={col.key}
                scope="col"
                style={col.width ? { width: col.width } : undefined}
                className={clsx(
                  'font-semibold whitespace-nowrap text-fg-strong',
                  cellPad,
                  ALIGN[col.align ?? 'left'],
                  col.hideBelow && HIDE[col.hideBelow],
                  col.headerClassName,
                )}
              >
                {col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr>
              <td colSpan={colCount} className="p-0">
                {loading ? (
                  <div className="flex justify-center py-10">
                    <Spinner size="lg" />
                  </div>
                ) : (
                  (emptyState ?? <div className="py-10 text-center text-muted">No items</div>)
                )}
              </td>
            </tr>
          )}
          {rows.map((row, index) => {
            const id = getRowId(row);
            const canExpand = isExpandable ? isExpandable(row) : true;
            const isOpen = canExpand && expanded.has(id);
            const detailsId = `${baseId}-details-${String(id)}`;
            const label = `${isOpen ? 'Hide' : 'Show'} ${detailsLabel?.(row) ?? 'details'}`;
            return (
              <Fragment key={id}>
                <tr
                  className={clsx(
                    'transition-colors hover:bg-row-hover',
                    isOpen ? 'bg-row-hover' : 'border-b border-border',
                    loading && 'opacity-60',
                    rowClassName?.(row),
                  )}
                >
                  <td className={clsx('w-9 align-middle', cellPad)}>
                    {canExpand && (
                      <button
                        type="button"
                        onClick={() => toggle(id)}
                        aria-expanded={isOpen}
                        aria-controls={isOpen ? detailsId : undefined}
                        aria-label={label}
                        title={label}
                        className="inline-flex size-6 items-center justify-center rounded text-muted transition-colors hover:bg-row-hover hover:text-fg-strong"
                      >
                        {isOpen ? (
                          <ChevronDown aria-hidden width={15} height={15} />
                        ) : (
                          <ChevronRight aria-hidden width={15} height={15} />
                        )}
                      </button>
                    )}
                  </td>
                  {columns.map((col) => (
                    <td
                      key={col.key}
                      className={clsx(
                        cellPad,
                        'align-middle',
                        ALIGN[col.align ?? 'left'],
                        col.hideBelow && HIDE[col.hideBelow],
                        col.className,
                      )}
                    >
                      {col.render ? col.render(row, index) : ((row as Record<string, unknown>)[col.key] as ReactNode)}
                    </td>
                  ))}
                </tr>
                {isOpen && (
                  <tr id={detailsId} className="border-b border-border bg-card-alt">
                    <td colSpan={colCount} className={clsx(cellPad, 'pl-12')}>
                      {renderExpanded(row)}
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
