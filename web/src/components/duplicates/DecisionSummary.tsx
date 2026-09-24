import { Lock } from 'lucide-react';
import type { GroupFile, Id } from '@/api/types';
import { ByteSize, DecisionBadge } from '@/components/ui';
import { CRITERION_TYPE_LABELS, labelOf } from '@/lib/constants';
import { discTypeLabel, dynamicRangeLabel, formatBytes, formatNumber, resolutionLabel } from '@/lib/format';
import { isHardlinked, versionSize } from './duplicateUtils';

export interface DecisionSummaryProps {
  /** Files ordered by rank. */
  files: readonly GroupFile[];
  /** Server-computed reclaimable bytes (hardlinked copies count as 0). */
  reclaimableBytes: number;
  libraryNames?: ReadonlyMap<Id, string>;
}

/** "Keeping N · Removing M · Reclaim X" plus the engine's reasons for every copy. */
export function DecisionSummary({ files, reclaimableBytes, libraryNames }: DecisionSummaryProps) {
  const keep = files.filter((f) => f.decision === 'keep').length;
  const remove = files.length - keep;
  const hardlinkedRemovals = files.filter((f) => f.decision === 'remove' && isHardlinked(f.version)).length;

  return (
    <section aria-labelledby="decision-heading" className="rounded border border-border bg-card">
      <div className="flex flex-wrap items-baseline justify-between gap-2 border-b border-border px-4 py-2.5">
        <h2 id="decision-heading" className="m-0 text-base font-normal text-fg-strong">
          Decision
        </h2>
        <p className="m-0 text-sm text-fg" data-testid="decision-summary">
          Keeping <strong className="text-success">{formatNumber(keep)}</strong> · Removing{' '}
          <strong className="text-danger">{formatNumber(remove)}</strong> · Reclaim{' '}
          <ByteSize bytes={reclaimableBytes} className="font-semibold text-warning" />
        </p>
      </div>
      {keep === 0 && files.length > 0 && (
        <p className="m-0 border-b border-border bg-danger/10 px-4 py-2 text-sm text-danger">
          No copy would be kept — approval is blocked. Set a copy to Keep.
        </p>
      )}
      {hardlinkedRemovals > 0 && (
        <p className="m-0 border-b border-border px-4 py-2 text-xs text-warning">
          {hardlinkedRemovals === 1 ? 'One copy to remove is' : `${hardlinkedRemovals} copies to remove are`} hardlinked —
          removing {hardlinkedRemovals === 1 ? 'it' : 'them'} won’t free space.
        </p>
      )}
      <ol className="m-0 flex list-none flex-col divide-y divide-border p-0">
        {files.map((f, i) => {
          const v = f.version;
          const library = libraryNames?.get(v.libraryId) || v.libraryTitle;
          const reasons = (f.reasons ?? []).filter((r) => typeof r === 'string' && r.trim() !== '');
          return (
            <li key={f.id} className="flex flex-col gap-1 px-4 py-2.5" data-file-id={f.id}>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="w-6 text-xs font-semibold text-muted">#{f.rank > 0 ? f.rank : i + 1}</span>
                <DecisionBadge decision={f.decision} />
                {f.protected && (
                  <span className="inline-flex items-center gap-1 text-xs text-primary" title={f.protectedReason}>
                    <Lock aria-hidden width={12} height={12} />
                    Protected{f.protectedReason ? `: ${f.protectedReason}` : ''}
                  </span>
                )}
                <span className="min-w-0 text-fg-strong">
                  {[
                    discTypeLabel(v.disc?.type),
                    resolutionLabel(v.resolution),
                    dynamicRangeLabel(v.dynamicRange),
                    formatBytes(versionSize(v)),
                    library,
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                </span>
                {f.override && (
                  <span className="text-xs text-pink" title="Set manually">
                    (override)
                  </span>
                )}
                {f.decidingCriterion && f.decision === 'remove' && (
                  <span className="text-xs text-muted">
                    decided by <span className="text-accent-soft">{labelOf(CRITERION_TYPE_LABELS, f.decidingCriterion)}</span>
                  </span>
                )}
              </div>
              {reasons.length > 0 && (
                <ul className="m-0 ml-8 list-disc pl-4 text-xs text-muted">
                  {reasons.map((r, idx) => (
                    <li key={idx}>{r}</li>
                  ))}
                </ul>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
