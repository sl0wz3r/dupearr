import { Film, Tv } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import type { DuplicateGroupSummary, Id, SortDirection } from '@/api/types';
import { ByteSize, GroupStatusBadge, RelativeTime, Table, type RowId, type TableColumn } from '@/components/ui';
import { formatNumber } from '@/lib/format';
import { groupTitle } from './duplicateUtils';
import { FileChips } from './FileChips';
import { FlagIcons } from './FlagIcons';
import { GroupRowActions, type GroupRowActionHandlers } from './GroupRowActions';
import { PosterImage } from './PosterImage';

/** Library names of a group: from the library list, falling back to the files' library titles. */
export function groupLibraryNames(group: DuplicateGroupSummary, libraryNames?: ReadonlyMap<Id, string>): string[] {
  const names = new Set<string>();
  for (const id of group.libraryIds ?? []) {
    const name = libraryNames?.get(id);
    if (name) names.add(name);
  }
  if (names.size === 0) {
    for (const f of group.files ?? []) if (f.libraryTitle) names.add(f.libraryTitle);
  }
  return [...names];
}

export interface DuplicatesTableProps extends GroupRowActionHandlers {
  rows: readonly DuplicateGroupSummary[];
  loading?: boolean;
  sortKey: string;
  sortDirection: SortDirection;
  onSortChange: (key: string, direction: SortDirection) => void;
  selectable: boolean;
  selectedIds: ReadonlySet<RowId>;
  onSelectionChange: (ids: RowId[]) => void;
  libraryNames?: ReadonlyMap<Id, string>;
  emptyState?: ReactNode;
}

/** Desktop table of duplicate groups (server-side sorting). */
export function DuplicatesTable({
  rows,
  loading,
  sortKey,
  sortDirection,
  onSortChange,
  selectable,
  selectedIds,
  onSelectionChange,
  libraryNames,
  emptyState,
  ...actions
}: DuplicatesTableProps) {
  const columns: TableColumn<DuplicateGroupSummary>[] = [
    {
      key: 'poster',
      header: <span className="sr-only">Poster</span>,
      width: '48px',
      className: 'py-1!',
      render: (g) => <PosterImage serverId={g.serverId} thumb={g.thumb} mediaType={g.mediaType} width={60} height={90} className="w-9" />,
    },
    {
      key: 'title',
      header: 'Title',
      sortable: true,
      render: (g) => {
        const TypeIcon = g.mediaType === 'episode' ? Tv : Film;
        const title = groupTitle(g);
        // Titles wrap (max two lines) instead of truncating: a nowrap cell would force the whole
        // table wider than the page on medium screens.
        return (
          <div className="flex min-w-40 items-start gap-1.5">
            <TypeIcon aria-hidden width={13} height={13} className="mt-0.5 shrink-0 text-subtle" />
            <Link
              to={`/duplicate/${g.id}`}
              title={title}
              className="line-clamp-2 font-medium break-words text-fg-strong no-underline hover:text-accent-soft hover:underline"
            >
              {title}
            </Link>
          </div>
        );
      },
    },
    {
      key: 'library',
      header: 'Library',
      hideBelow: 'lg',
      render: (g) => {
        const names = groupLibraryNames(g, libraryNames);
        return (
          <span className="block max-w-56 truncate text-fg" title={names.join(', ')}>
            {names.join(', ') || '-'}
          </span>
        );
      },
    },
    {
      key: 'copies',
      header: 'Copies',
      render: (g) => (
        <div className="flex items-center gap-2">
          <span className="w-4 text-right tabular-nums text-fg-strong" title={`${g.keepCount} keep · ${g.removeCount} remove`}>
            {formatNumber(g.fileCount)}
          </span>
          <FileChips files={g.files} />
        </div>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      sortable: true,
      render: (g) => (
        <div className="flex flex-wrap items-center gap-1.5">
          <GroupStatusBadge status={g.status} title={g.statusReason || undefined} />
          <FlagIcons flags={g.flags} />
        </div>
      ),
    },
    {
      key: 'reclaimableBytes',
      header: 'Reclaimable',
      sortable: true,
      align: 'right',
      render: (g) => <ByteSize bytes={g.reclaimableBytes} className="tabular-nums" />,
    },
    {
      key: 'lastSeenAt',
      header: 'Last Seen',
      sortable: true,
      hideBelow: 'xl',
      render: (g) => <RelativeTime date={g.lastSeenAt} className="whitespace-nowrap text-muted" />,
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '96px',
      render: (g) => <GroupRowActions group={g} {...actions} />,
    },
  ];

  return (
    <Table
      aria-label="Duplicate groups"
      columns={columns}
      rows={rows}
      getRowId={(g) => g.id}
      sortKey={sortKey}
      sortDirection={sortDirection}
      onSortChange={onSortChange}
      selectable={selectable}
      selectedIds={selectedIds}
      onSelectionChange={onSelectionChange}
      loading={loading}
      emptyState={emptyState}
      dense
    />
  );
}
