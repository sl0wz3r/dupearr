import { Download, Eye, FileText } from 'lucide-react';
import type { LogFile } from '@/api/types';
import { ByteSize, EmptyState, IconButton, RelativeTime, Table, type TableColumn } from '@/components/ui';
import { logFileDownloadUrl } from './systemFormat';

export interface LogFilesTableProps {
  files: readonly LogFile[];
  loading?: boolean;
  onView: (file: LogFile) => void;
}

/** System → Logs table: filename (opens the viewer), last write, size, view/download. */
export function LogFilesTable({ files, loading, onView }: LogFilesTableProps) {
  const columns: TableColumn<LogFile>[] = [
    {
      key: 'filename',
      header: 'Filename',
      render: (f) => (
        <button
          type="button"
          onClick={() => onView(f)}
          className="font-mono text-xs text-fg-strong hover:text-accent-soft hover:underline"
          title={`View ${f.filename}`}
        >
          {f.filename}
        </button>
      ),
    },
    {
      key: 'lastWriteTime',
      header: 'Last Write Time',
      className: 'whitespace-nowrap',
      render: (f) => <RelativeTime date={f.lastWriteTime} />,
    },
    {
      key: 'size',
      header: 'Size',
      align: 'right',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (f) => <ByteSize bytes={f.size} />,
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '84px',
      render: (f) => (
        <span className="inline-flex items-center gap-0.5">
          <IconButton icon={Eye} size="sm" label={`View ${f.filename}`} onClick={() => onView(f)} />
          <a
            href={logFileDownloadUrl(f.filename)}
            download={f.filename}
            aria-label={`Download ${f.filename}`}
            title={`Download ${f.filename}`}
            className="inline-flex size-7 items-center justify-center rounded text-muted transition-colors hover:bg-row-hover hover:text-fg-strong"
          >
            <Download aria-hidden width={15} height={15} />
          </a>
        </span>
      ),
    },
  ];

  return (
    <Table
      aria-label="Log files"
      columns={columns}
      rows={files}
      getRowId={(f) => f.filename}
      loading={loading}
      emptyState={<EmptyState icon={FileText} title="No log files" compact />}
    />
  );
}
