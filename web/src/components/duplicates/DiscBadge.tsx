import { Disc3 } from 'lucide-react';
import type { DiscType } from '@/api/types';
import { Badge } from '@/components/ui';
import { discSummary, discTypeLabel, discTypeShortLabel } from '@/lib/format';

export interface DiscBadgeProps {
  disc: { type: DiscType; fileCount?: number; discs?: number; clipCount?: number };
  /** Short text ("UHD BD") instead of the full label ("UHD Blu-ray disc (BDMV)"). */
  short?: boolean;
  className?: string;
}

/**
 * Marks a copy as a full-disc backup: "UHD Blu-ray disc (BDMV)", "DVD (VIDEO_TS)", "ISO image",
 * "Blu-ray clips (loose .m2ts)"… The tooltip adds the clip, file and disc counts
 * ("… · 312 files · 2 discs", "… · 125 clips").
 */
export function DiscBadge({ disc, short = false, className }: DiscBadgeProps) {
  return (
    <Badge kind="primary" outline icon={Disc3} title={discSummary(disc)} className={className}>
      {short ? discTypeShortLabel(disc.type) : discTypeLabel(disc.type)}
    </Badge>
  );
}
