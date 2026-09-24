import { ArchiveRestore, DatabaseBackup, Download, Trash } from 'lucide-react';
import type { Backup } from '@/api/types';
import { Badge, ByteSize, EmptyState, IconButton, RelativeTime, Table, type TableColumn } from '@/components/ui';
import { BACKUP_TYPE_LABELS, labelOf } from '@/lib/constants';

export interface BackupTableProps {
  backups: readonly Backup[];
  loading?: boolean;
  /**
   * Downloads a backup. A backup holds every credential, so the page asks for the password first
   * (POST /system/backup/download/{id}); there is no plain link a session could follow.
   */
  onDownload: (backup: Backup) => void;
  onRestore: (backup: Backup) => void;
  onDelete: (backup: Backup) => void;
  /** Disables row actions (e.g. while a restore is in progress). */
  disabled?: boolean;
}

/** System → Backup table: name (download), type, size, time, restore/delete. */
export function BackupTable({ backups, loading, onDownload, onRestore, onDelete, disabled }: BackupTableProps) {
  const columns: TableColumn<Backup>[] = [
    {
      key: 'name',
      header: 'Name',
      // Take the remaining width and truncate long file names instead of widening the table.
      className: 'max-w-0 w-full',
      render: (b) => (
        <button
          type="button"
          onClick={() => onDownload(b)}
          className="inline-flex max-w-full cursor-pointer items-center gap-1.5 bg-transparent p-0 font-mono text-xs text-fg-strong hover:text-accent-soft"
          title={`Download ${b.name}`}
          aria-label={`Download ${b.name}`}
        >
          <Download aria-hidden width={14} height={14} className="shrink-0 text-muted" />
          <span className="min-w-0 truncate">{b.name}</span>
        </button>
      ),
    },
    {
      key: 'type',
      header: 'Type',
      hideBelow: 'sm',
      render: (b) => (
        <Badge kind={b.type === 'manual' ? 'primary' : b.type === 'update' ? 'accent' : 'default'} outline>
          {labelOf(BACKUP_TYPE_LABELS, b.type)}
        </Badge>
      ),
    },
    {
      key: 'size',
      header: 'Size',
      align: 'right',
      hideBelow: 'md',
      className: 'whitespace-nowrap',
      render: (b) => <ByteSize bytes={b.size} />,
    },
    {
      key: 'time',
      header: 'Time',
      className: 'whitespace-nowrap',
      render: (b) => <RelativeTime date={b.time} />,
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '84px',
      render: (b) => (
        <span className="inline-flex items-center gap-0.5">
          <IconButton
            icon={ArchiveRestore}
            size="sm"
            label={`Restore ${b.name}`}
            title="Restore this backup"
            disabled={disabled}
            onClick={() => onRestore(b)}
          />
          <IconButton
            icon={Trash}
            size="sm"
            variant="danger"
            label={`Delete ${b.name}`}
            title="Delete this backup"
            disabled={disabled}
            onClick={() => onDelete(b)}
          />
        </span>
      ),
    },
  ];

  return (
    <Table
      aria-label="Backups"
      columns={columns}
      rows={backups}
      getRowId={(b) => b.id}
      loading={loading}
      emptyState={
        <EmptyState
          icon={DatabaseBackup}
          title="No backups"
          description="Scheduled backups are created automatically; use Backup Now to create one immediately."
          compact
        />
      }
    />
  );
}
