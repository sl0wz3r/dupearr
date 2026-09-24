import { X } from 'lucide-react';
import type { ReactNode } from 'react';
import type { Action } from '@/api/types';
import { ByteSize, IconButton, RelativeTime, Table, type TableColumn } from '@/components/ui';
import { ActionMethodCell, ActionStatusCell, ActionTitleCell } from './ActionCells';
import { canCancelQueueItem } from './activityMeta';
import { PathList } from './PathList';

export interface QueueTableProps {
  rows: readonly Action[];
  loading?: boolean;
  /** Asks to cancel a pending action (the page confirms first). */
  onCancel: (action: Action) => void;
  /** Id of the action currently being cancelled (spinner on its button). */
  cancellingId?: number | null;
  emptyState?: ReactNode;
}

/** Activity → Queue table: pending/running removal actions. */
export function QueueTable({ rows, loading, onCancel, cancellingId, emptyState }: QueueTableProps) {
  const columns: TableColumn<Action>[] = [
    { key: 'title', header: 'Title', render: (a) => <ActionTitleCell action={a} /> },
    {
      key: 'paths',
      header: 'Path',
      hideBelow: 'md',
      className: 'max-w-0 w-[40%]',
      render: (a) => <PathList paths={a.paths} maxWidthClass="max-w-full" />,
    },
    {
      key: 'method',
      header: 'Method',
      hideBelow: 'xl',
      className: 'whitespace-nowrap',
      render: (a) => <ActionMethodCell action={a} />,
    },
    {
      key: 'size',
      header: 'Size',
      align: 'right',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (a) => <ByteSize bytes={a.size} />,
    },
    {
      key: 'createdAt',
      header: 'Created',
      hideBelow: 'lg',
      className: 'whitespace-nowrap',
      render: (a) => <RelativeTime date={a.createdAt} />,
    },
    { key: 'status', header: 'Status', render: (a) => <ActionStatusCell action={a} /> },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '48px',
      render: (a) => {
        const cancellable = canCancelQueueItem(a);
        return (
          <IconButton
            icon={X}
            variant="danger"
            size="sm"
            label={`Cancel removal of ${a.title || `group ${a.groupId}`}`}
            title={cancellable ? 'Cancel this removal' : 'Only pending removals can be cancelled'}
            disabled={!cancellable}
            loading={cancellingId === a.id}
            onClick={() => onCancel(a)}
          />
        );
      },
    },
  ];

  return (
    <Table
      aria-label="Queue"
      columns={columns}
      rows={rows}
      getRowId={(a) => a.id}
      loading={loading}
      emptyState={emptyState}
    />
  );
}
