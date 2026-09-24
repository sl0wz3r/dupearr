import { ScanSearch } from 'lucide-react';
import { errorMessage } from '@/api/client';
import { useScans } from '@/api/hooks';
import type { ScanRun, ScanStats } from '@/api/types';
import { Badge, EmptyState, LoadErrorAlert, RelativeTime, Spinner, Table, useNow, type TableColumn } from '@/components/ui';
import { SCAN_STATUS_KIND, SCAN_STATUS_LABELS, TRIGGER_LABELS, labelOf } from '@/lib/constants';
import { formatBytes, formatDuration, formatNumber } from '@/lib/format';
import { elapsedMs } from './activityMeta';

/** Compact stats line: "12 groups · 3 new · 1 resolved · 4.2 GiB reclaimable · 2 errors". */
export function ScanStatsSummary({ stats }: { stats: ScanStats | null | undefined }) {
  if (!stats) return <span className="text-muted">-</span>;
  const n = (v: number | undefined) => formatNumber(v ?? 0);
  const errors = stats.errors ?? 0;
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-fg">
      <span title={`${n(stats.itemsExamined)} items examined in ${n(stats.libraries)} libraries`}>
        <span className="font-semibold text-fg-strong">{n(stats.groupsFound)}</span> groups
      </span>
      <span aria-hidden className="text-subtle">
        ·
      </span>
      <span>
        <span className="font-semibold text-fg-strong">{n(stats.newGroups)}</span> new
      </span>
      <span aria-hidden className="text-subtle">
        ·
      </span>
      <span>
        <span className="font-semibold text-fg-strong">{n(stats.resolvedGroups)}</span> resolved
      </span>
      <span aria-hidden className="text-subtle">
        ·
      </span>
      <span>
        <span className="font-semibold text-fg-strong">{formatBytes(stats.reclaimableBytes ?? 0)}</span> reclaimable
      </span>
      {errors > 0 && (
        <>
          <span aria-hidden className="text-subtle">
            ·
          </span>
          <span className="font-semibold text-danger">
            {formatNumber(errors)} error{errors === 1 ? '' : 's'}
          </span>
        </>
      )}
    </span>
  );
}

/** Scan duration; live elapsed time (15s resolution) while running. */
function ScanDuration({ scan }: { scan: ScanRun }) {
  const now = useNow();
  if (scan.status === 'running') {
    const ms = elapsedMs(scan.startedAt, now);
    return (
      <span className="inline-flex items-center gap-1.5 text-muted">
        <Spinner size="xs" label="Running" />
        {ms !== null ? formatDuration(ms) : 'Running'}
      </span>
    );
  }
  const ms = elapsedMs(scan.startedAt, scan.finishedAt);
  return <span>{ms !== null ? formatDuration(ms) : '-'}</span>;
}

/** History → Scans tab: the latest 50 scan runs with their stats. */
export function ScansPanel() {
  const scans = useScans();
  const rows = scans.data ?? [];

  const columns: TableColumn<ScanRun>[] = [
    {
      key: 'startedAt',
      header: 'Started',
      className: 'whitespace-nowrap',
      render: (s) => <RelativeTime date={s.startedAt} />,
    },
    {
      key: 'duration',
      header: 'Duration',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (s) => <ScanDuration scan={s} />,
    },
    {
      key: 'trigger',
      header: 'Trigger',
      hideBelow: 'lg',
      render: (s) => labelOf(TRIGGER_LABELS, s.trigger),
    },
    {
      key: 'targeted',
      header: 'Type',
      hideBelow: 'md',
      render: (s) =>
        s.targeted ? (
          <Badge kind="info" outline title="Limited to specific items; never resolves unseen groups">
            Targeted
          </Badge>
        ) : (
          <Badge outline title="All enabled libraries">
            Full
          </Badge>
        ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (s) => <Badge kind={SCAN_STATUS_KIND[s.status] ?? 'default'}>{labelOf(SCAN_STATUS_LABELS, s.status)}</Badge>,
    },
    {
      key: 'stats',
      header: 'Results',
      render: (s) => (
        <div className="min-w-0">
          <ScanStatsSummary stats={s.stats} />
          {s.error && (
            <div className="mt-1 line-clamp-2 text-xs break-words text-danger" title={s.error}>
              {s.error}
            </div>
          )}
        </div>
      ),
    },
  ];

  return (
    <div>
      {scans.isError && (
        <LoadErrorAlert
          title="Unable to load scans"
          message={errorMessage(scans.error)}
          onRetry={() => void scans.refetch()}
          retrying={scans.isFetching}
          className="mb-4"
        />
      )}
      {!(scans.isError && !scans.data) && (
        <Table
          aria-label="Scans"
          columns={columns}
          rows={rows}
          getRowId={(s) => s.id}
          loading={scans.isLoading}
          emptyState={
            <EmptyState
              icon={ScanSearch}
              title="No scans yet"
              description="Run a scan from the Duplicates page or wait for the scheduled Duplicate Scan task."
              compact
            />
          }
        />
      )}
    </div>
  );
}
