import { clsx } from 'clsx';
import type { ReactNode } from 'react';
import type { DuplicateStats, GroupStatus } from '@/api/types';
import { ByteSize, RelativeTime } from '@/components/ui';
import { GROUP_STATUS_DESCRIPTIONS, GROUP_STATUS_KIND, GROUP_STATUS_LABELS, type StatusKind } from '@/lib/constants';
import { formatNumber } from '@/lib/format';

const KIND_TEXT: Record<StatusKind, string> = {
  default: 'text-fg-strong',
  primary: 'text-primary',
  accent: 'text-accent-soft',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  info: 'text-info',
  pink: 'text-pink',
  inverse: 'text-fg-strong',
};

/** Statuses shown as tiles (in this order). */
export const STAT_STATUSES: readonly GroupStatus[] = ['pending', 'review', 'deferred', 'queued'];

interface TileProps {
  label: string;
  value: ReactNode;
  valueClassName?: string;
  title?: string;
  /** Makes the tile a button (e.g. filter by status). */
  onClick?: () => void;
  active?: boolean;
}

function Tile({ label, value, valueClassName, title, onClick, active }: TileProps) {
  const body = (
    <>
      <span className="block truncate text-[11px] font-semibold tracking-wide text-muted uppercase">{label}</span>
      <span className={clsx('mt-0.5 block truncate text-lg leading-tight font-light text-fg-strong', valueClassName)}>
        {value}
      </span>
    </>
  );
  const base = 'min-w-0 bg-card px-3 py-2 text-left';
  if (onClick) {
    return (
      <button
        type="button"
        title={title}
        onClick={onClick}
        aria-pressed={active}
        className={clsx(
          base,
          'transition-colors hover:bg-card-hover focus-visible:relative focus-visible:outline-2 focus-visible:outline-accent',
          active && 'bg-row-selected',
        )}
      >
        {body}
      </button>
    );
  }
  return (
    <div className={base} title={title}>
      {body}
    </div>
  );
}

export interface StatsStripProps {
  stats: DuplicateStats | undefined;
  loading?: boolean;
  /** Called with a status when its tile is clicked (filters the list). */
  onStatusClick?: (status: GroupStatus) => void;
  /** Currently filtered-on single status (highlights its tile). */
  activeStatus?: GroupStatus | null;
  /** Called when the Total tile is clicked (clears the status filter). */
  onTotalClick?: () => void;
  className?: string;
}

/** Dense stats header: totals per status, reclaimable/reclaimed space and the last scan. */
export function StatsStrip({ stats, loading, onStatusClick, activeStatus, onTotalClick, className }: StatsStripProps) {
  const dash = loading ? '…' : '-';
  const count = (n: number | undefined) => (n === undefined ? dash : formatNumber(n));
  const lastScan = stats?.lastScan ?? null;
  const lastScanDate = lastScan ? (lastScan.finishedAt ?? lastScan.startedAt) : null;

  return (
    <section
      aria-label="Duplicate statistics"
      className={clsx(
        'grid grid-cols-2 gap-px overflow-hidden rounded border border-border bg-border sm:grid-cols-4 xl:grid-cols-8',
        className,
      )}
    >
      <Tile
        label="Total"
        value={count(stats?.total)}
        title="All duplicate groups (any status)"
        onClick={onTotalClick}
      />
      {STAT_STATUSES.map((s) => (
        <Tile
          key={s}
          label={GROUP_STATUS_LABELS[s]}
          value={count(stats ? (stats.byStatus?.[s] ?? 0) : undefined)}
          valueClassName={stats?.byStatus?.[s] ? KIND_TEXT[GROUP_STATUS_KIND[s]] : undefined}
          title={`${GROUP_STATUS_DESCRIPTIONS[s]} — click to filter`}
          onClick={onStatusClick ? () => onStatusClick(s) : undefined}
          active={activeStatus === s}
        />
      ))}
      <Tile
        label="Reclaimable"
        value={stats ? <ByteSize bytes={stats.reclaimableBytes ?? 0} /> : dash}
        valueClassName="text-warning"
        title="Space freed by removing the proposed copies of pending, review and queued groups"
      />
      <Tile
        label="Reclaimed"
        value={stats ? <ByteSize bytes={stats.reclaimedBytes ?? 0} /> : dash}
        valueClassName="text-success"
        title="Space freed by completed removals (dry runs excluded)"
      />
      <Tile
        label="Last scan"
        value={
          !stats ? (
            dash
          ) : lastScan?.status === 'running' ? (
            <span className="text-accent-soft">Running</span>
          ) : lastScanDate ? (
            <span className={lastScan?.status === 'failed' ? 'text-danger' : undefined}>
              <RelativeTime date={lastScanDate} relative />
              {lastScan?.status === 'failed' && <span className="sr-only"> (failed)</span>}
            </span>
          ) : (
            'Never'
          )
        }
        title={lastScan?.status === 'failed' ? `Last scan failed${lastScan.error ? `: ${lastScan.error}` : ''}` : undefined}
      />
    </section>
  );
}
