import { ScrollText } from 'lucide-react';
import type { LogEntry } from '@/api/types';
import { ExpandableTable } from '@/components/activity/ExpandableTable';
import { Badge, EmptyState, RelativeTime, type TableColumn } from '@/components/ui';
import { levelKind, levelLabel } from './logLevels';

export interface LogEventsTableProps {
  entries: readonly LogEntry[];
  loading?: boolean;
  /** Text of the empty state (depends on the level filter). */
  emptyDescription?: string;
}

/**
 * Row ids for log entries (which have none): time + logger + message prefix, with an occurrence
 * suffix for identical entries. Independent of position, so expanded rows survive auto-refresh.
 */
export function logEntryIds(entries: readonly LogEntry[]): string[] {
  const seen = new Map<string, number>();
  return entries.map((e) => {
    const base = `${e.time}|${e.level}|${e.logger}|${(e.message ?? '').slice(0, 80)}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return n === 0 ? base : `${base}#${n}`;
  });
}

/** System → Events table: level badge, time, logger, message; exceptions expand below the row. */
export function LogEventsTable({ entries, loading, emptyDescription }: LogEventsTableProps) {
  const ids = logEntryIds(entries);
  const rows = entries.map((entry, index) => ({ entry, id: ids[index]! }));
  type Row = (typeof rows)[number];

  const columns: TableColumn<Row>[] = [
    {
      key: 'level',
      header: 'Level',
      width: '6rem',
      render: ({ entry }) => <Badge kind={levelKind(entry.level)}>{levelLabel(entry.level)}</Badge>,
    },
    {
      key: 'time',
      header: 'Time',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: ({ entry }) => <RelativeTime date={entry.time} />,
    },
    {
      key: 'logger',
      header: 'Component',
      hideBelow: 'md',
      className: 'whitespace-nowrap text-muted',
      render: ({ entry }) => entry.logger || '-',
    },
    {
      key: 'message',
      header: 'Message',
      render: ({ entry }) => (
        <div className="min-w-0">
          <div className="flex flex-wrap gap-x-2 text-xs text-muted md:hidden">
            <RelativeTime date={entry.time} className="sm:hidden" />
            {entry.logger && <span>{entry.logger}</span>}
          </div>
          <span className="break-words whitespace-pre-wrap text-fg">{entry.message}</span>
        </div>
      ),
    },
  ];

  return (
    <ExpandableTable
      aria-label="Log events"
      dense
      columns={columns}
      rows={rows}
      getRowId={(r) => r.id}
      isExpandable={(r) => !!r.entry.exception}
      detailsLabel={() => 'exception'}
      renderExpanded={(r) => (
        <pre className="m-0 max-h-96 overflow-auto rounded border border-border bg-page p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all text-danger">
          {r.entry.exception}
        </pre>
      )}
      loading={loading}
      emptyState={<EmptyState icon={ScrollText} title="No events" description={emptyDescription} compact />}
    />
  );
}
