import { clsx } from 'clsx';
import { Disc3 } from 'lucide-react';
import type { DuplicateGroupSummaryFile } from '@/api/types';
import { DECISION_LABELS, labelOf } from '@/lib/constants';
import { discSummary, dynamicRangeLabel, formatBytes, resolutionLabel, videoCodecLabel } from '@/lib/format';
import { summaryFileLabel } from './duplicateUtils';

/**
 * "2160p (4K) · Dolby Vision · HEVC (H.265) — Movies 4K — Radarr 4K" (no decision, no size); a full
 * disc leads with "UHD Blu-ray disc (BDMV) · 312 files · 2 discs".
 */
export function summaryFileDescription(f: DuplicateGroupSummaryFile): string {
  return [
    discSummary(f.disc),
    f.discClip ? 'Single disc clip (never removed on its own)' : '',
    [resolutionLabel(f.resolution), dynamicRangeLabel(f.dynamicRange), videoCodecLabel(f.videoCodec)]
      .filter(Boolean)
      .join(' · '),
    f.libraryTitle,
    f.arrInstanceName,
  ]
    .filter(Boolean)
    .join(' — ');
}

/** Tooltip text of a summary file chip. */
export function summaryFileTooltip(f: DuplicateGroupSummaryFile): string {
  return [labelOf(DECISION_LABELS, f.decision), summaryFileDescription(f), formatBytes(f.size)]
    .filter(Boolean)
    .join(' — ');
}

export interface FileChipsProps {
  files: readonly DuplicateGroupSummaryFile[] | null | undefined;
  /** Max chips before "+N" (default 4). */
  max?: number;
  className?: string;
}

/**
 * Mini keep/remove chips ("4K DV", "1080p", disc icon + "UHD BD · 4K DV") for the files of a group
 * summary: green outline = keep, red strikethrough = remove. Keepers first.
 */
export function FileChips({ files, max = 4, className }: FileChipsProps) {
  const list = [...(files ?? [])].sort((a, b) =>
    a.decision === b.decision ? 0 : a.decision === 'keep' ? -1 : 1,
  );
  if (list.length === 0) return null;
  const shown = list.slice(0, max);
  const hidden = list.length - shown.length;
  return (
    <ul className={clsx('m-0 flex list-none flex-wrap items-center gap-1 p-0', className)} aria-label="Copies">
      {shown.map((f) => {
        const keep = f.decision === 'keep';
        const label = summaryFileLabel(f);
        return (
          <li
            key={f.id}
            title={summaryFileTooltip(f)}
            data-decision={f.decision}
            data-disc={f.disc ? f.disc.type : undefined}
            className={clsx(
              'inline-flex items-center gap-0.5 rounded-sm border px-1 text-[10px] leading-4 font-semibold whitespace-nowrap',
              keep ? 'border-success/60 bg-success/10 text-success' : 'border-danger/60 bg-danger/10 text-danger line-through',
            )}
          >
            <span className="sr-only">{keep ? 'Keep: ' : 'Remove: '}</span>
            {f.disc && <Disc3 aria-hidden width={10} height={10} className="shrink-0" />}
            {f.disc && <span className="sr-only">Full disc </span>}
            {label}
          </li>
        );
      })}
      {hidden > 0 && <li className="text-[10px] text-muted">+{hidden}</li>}
    </ul>
  );
}
