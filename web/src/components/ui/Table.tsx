import { clsx } from 'clsx';
import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react';
import { useRef, type ReactNode } from 'react';
import type { SortDirection } from '@/api/types';
import { Checkbox } from './Checkbox';
import { Spinner } from './Spinner';

export type RowId = string | number;

export interface TableColumn<T> {
  /** Unique key; also the sort key sent to the server when `sortable`. */
  key: string;
  header: ReactNode;
  /** Cell renderer (defaults to `row[key]`). */
  render?: (row: T, index: number) => ReactNode;
  sortable?: boolean;
  align?: 'left' | 'center' | 'right';
  /** CSS width, e.g. "120px" or "20%". */
  width?: string;
  /** Hide the column below a breakpoint (responsive tables). */
  hideBelow?: 'sm' | 'md' | 'lg' | 'xl';
  className?: string;
  headerClassName?: string;
}

export interface TableProps<T> {
  columns: readonly TableColumn<T>[];
  rows: readonly T[];
  getRowId: (row: T) => RowId;

  /** Controlled sort state (server-side sorting: refetch in onSortChange). */
  sortKey?: string;
  sortDirection?: SortDirection;
  onSortChange?: (key: string, direction: SortDirection) => void;

  /** Show checkboxes; selection is controlled via selectedIds/onSelectionChange. Shift-click selects ranges. */
  selectable?: boolean;
  selectedIds?: ReadonlySet<RowId> | readonly RowId[];
  onSelectionChange?: (ids: RowId[]) => void;
  /** Rows that can't be selected. */
  isRowSelectable?: (row: T) => boolean;

  onRowClick?: (row: T) => void;
  rowClassName?: (row: T) => string | undefined;

  loading?: boolean;
  /** Shown when there are no rows and not loading. */
  emptyState?: ReactNode;
  /** Tighter rows. */
  dense?: boolean;
  /** Sticky header inside a scroll container. */
  stickyHeader?: boolean;
  className?: string;
  'aria-label'?: string;
}

const HIDE: Record<NonNullable<TableColumn<unknown>['hideBelow']>, string> = {
  sm: 'hidden sm:table-cell',
  md: 'hidden md:table-cell',
  lg: 'hidden lg:table-cell',
  xl: 'hidden xl:table-cell',
};

const ALIGN = { left: 'text-left', center: 'text-center', right: 'text-right' } as const;

/** Next sort state when clicking a header: new column → ascending, same column → toggle. */
export function nextSort(
  current: { key?: string; direction?: SortDirection },
  clicked: string,
): { key: string; direction: SortDirection } {
  if (current.key !== clicked) return { key: clicked, direction: 'ascending' };
  return { key: clicked, direction: current.direction === 'ascending' ? 'descending' : 'ascending' };
}

/** Client-side sort helper for small, fully-loaded lists. */
export function sortRows<T>(
  rows: readonly T[],
  key: string | undefined,
  direction: SortDirection | undefined,
  accessor: (row: T, key: string) => unknown = (row, k) => (row as Record<string, unknown>)[k],
): T[] {
  if (!key) return [...rows];
  const dir = direction === 'descending' ? -1 : 1;
  return [...rows].sort((a, b) => {
    const va = accessor(a, key);
    const vb = accessor(b, key);
    if (va === vb) return 0;
    if (va === null || va === undefined) return 1;
    if (vb === null || vb === undefined) return -1;
    if (typeof va === 'number' && typeof vb === 'number') return (va - vb) * dir;
    return String(va).localeCompare(String(vb), undefined, { numeric: true, sensitivity: 'base' }) * dir;
  });
}

/**
 * *arr-style data table: sortable headers, optional selection column, row click, loading/empty states.
 * @example
 * <Table columns={cols} rows={page.records} getRowId={(r) => r.id}
 *   sortKey={sort.key} sortDirection={sort.dir} onSortChange={(k, d) => setSort({ key: k, dir: d })}
 *   selectable selectedIds={selected} onSelectionChange={setSelected} />
 */
export function Table<T>({
  columns,
  rows,
  getRowId,
  sortKey,
  sortDirection,
  onSortChange,
  selectable = false,
  selectedIds,
  onSelectionChange,
  isRowSelectable,
  onRowClick,
  rowClassName,
  loading = false,
  emptyState,
  dense = false,
  stickyHeader = false,
  className,
  ...aria
}: TableProps<T>) {
  const selected: ReadonlySet<RowId> =
    selectedIds instanceof Set ? selectedIds : new Set((selectedIds as readonly RowId[] | undefined) ?? []);
  const lastClicked = useRef<number | null>(null);

  const selectableRows = isRowSelectable ? rows.filter(isRowSelectable) : rows;
  const selectableIds = selectableRows.map(getRowId);
  const selectedVisible = selectableIds.filter((id) => selected.has(id)).length;
  const allSelected = selectableIds.length > 0 && selectedVisible === selectableIds.length;
  const someSelected = selectedVisible > 0 && !allSelected;

  const emit = (next: Set<RowId>) => onSelectionChange?.([...next]);

  const toggleAll = (checked: boolean) => {
    const next = new Set(selected);
    for (const id of selectableIds) {
      if (checked) next.add(id);
      else next.delete(id);
    }
    emit(next);
  };

  const toggleRow = (index: number, checked: boolean, shift: boolean) => {
    const next = new Set(selected);
    if (shift && lastClicked.current !== null && lastClicked.current !== index) {
      const [from, to] = [Math.min(lastClicked.current, index), Math.max(lastClicked.current, index)];
      for (let i = from; i <= to; i++) {
        const row = rows[i]!;
        if (isRowSelectable && !isRowSelectable(row)) continue;
        if (checked) next.add(getRowId(row));
        else next.delete(getRowId(row));
      }
    } else {
      const id = getRowId(rows[index]!);
      if (checked) next.add(id);
      else next.delete(id);
    }
    lastClicked.current = index;
    emit(next);
  };

  const cellPad = dense ? 'px-2.5 py-1.5' : 'px-3 py-2.5';
  const colCount = columns.length + (selectable ? 1 : 0);

  return (
    <div className={clsx('w-full overflow-x-auto', className)}>
      <table aria-label={aria['aria-label']} aria-busy={loading || undefined} className="w-full border-collapse text-sm">
        <thead className={clsx(stickyHeader && 'sticky top-0 z-10 bg-page')}>
          <tr className="border-b border-border">
            {selectable && (
              <th className={clsx('w-9', cellPad)}>
                <Checkbox
                  aria-label="Select all"
                  checked={allSelected}
                  indeterminate={someSelected}
                  disabled={selectableIds.length === 0}
                  onChange={toggleAll}
                />
              </th>
            )}
            {columns.map((col) => {
              const active = sortKey === col.key;
              const Icon = active ? (sortDirection === 'descending' ? ArrowDown : ArrowUp) : ArrowUpDown;
              return (
                <th
                  key={col.key}
                  scope="col"
                  style={col.width ? { width: col.width } : undefined}
                  aria-sort={active ? sortDirection : undefined}
                  className={clsx(
                    'font-semibold whitespace-nowrap text-fg-strong',
                    cellPad,
                    ALIGN[col.align ?? 'left'],
                    col.hideBelow && HIDE[col.hideBelow],
                    col.headerClassName,
                  )}
                >
                  {col.sortable && onSortChange ? (
                    <button
                      type="button"
                      onClick={() => {
                        const s = nextSort({ key: sortKey, direction: sortDirection }, col.key);
                        onSortChange(s.key, s.direction);
                      }}
                      className={clsx(
                        'group inline-flex items-center gap-1 font-semibold hover:text-accent-soft',
                        col.align === 'right' && 'flex-row-reverse',
                      )}
                    >
                      {col.header}
                      <Icon
                        aria-hidden
                        width={13}
                        height={13}
                        className={clsx(active ? 'text-accent-soft' : 'text-subtle opacity-0 group-hover:opacity-100')}
                      />
                    </button>
                  ) : (
                    col.header
                  )}
                </th>
              );
            })}
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
            const isSelected = selected.has(id);
            const canSelect = !isRowSelectable || isRowSelectable(row);
            return (
              <tr
                key={id}
                aria-selected={selectable ? isSelected : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                className={clsx(
                  'border-b border-border transition-colors',
                  isSelected ? 'bg-row-selected' : 'hover:bg-row-hover',
                  onRowClick && 'cursor-pointer',
                  loading && 'opacity-60',
                  rowClassName?.(row),
                )}
              >
                {selectable && (
                  <td className={clsx('w-9', cellPad)} onClick={(e) => e.stopPropagation()}>
                    <Checkbox
                      aria-label="Select row"
                      checked={isSelected}
                      disabled={!canSelect}
                      onChange={(checked, e) => {
                        // React derives checkbox onChange from the native click → shiftKey is available.
                        const shift = (e.nativeEvent as MouseEvent).shiftKey === true;
                        toggleRow(index, checked, shift);
                      }}
                    />
                  </td>
                )}
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
                    {col.render
                      ? col.render(row, index)
                      : ((row as Record<string, unknown>)[col.key] as ReactNode)}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
