import { clsx } from 'clsx';
import {
  CircleArrowUp,
  CircleSlash,
  Clapperboard,
  Copy,
  Disc3,
  Download,
  FileQuestionMark,
  FileExclamationPoint,
  FileX,
  Glasses,
  Hourglass,
  Languages,
  Layers,
  Library,
  Link2,
  Merge,
  Play,
  ScanSearch,
  Scissors,
  Server,
  Split,
  Timer,
  Unlink,
  type LucideIcon,
} from 'lucide-react';
import type { GroupFlag } from '@/api/types';
import { Tooltip } from '@/components/ui';
import { GROUP_FLAG_DESCRIPTIONS, GROUP_FLAG_KIND, GROUP_FLAG_LABELS, labelOf, type StatusKind } from '@/lib/constants';

/** Icon per group flag (unknown flags fall back to a generic icon). */
export const GROUP_FLAG_ICONS: Record<GroupFlag, LucideIcon> = {
  cross_library: Library,
  duration_mismatch: Timer,
  multi_episode: Split,
  stacked: Layers,
  hardlinked: Link2,
  min_age: Hourglass,
  missing_keeper_file: FileX,
  edition_split: Scissors,
  arr_untracked_keeper: Unlink,
  unanalyzed: ScanSearch,
  unavailable_version: CircleSlash,
  suspect_merge: Merge,
  variant_3d: Glasses,
  language_variant: Languages,
  intentional_arr_instances: Server,
  arr_queue_busy: Download,
  arr_cutoff_unmet: CircleArrowUp,
  playing: Play,
  sample: FileExclamationPoint,
  same_file: Copy,
  full_disc: Disc3,
  disc_unreadable: FileQuestionMark,
  disc_tracked_clip: Clapperboard,
};

const KIND_TEXT: Record<StatusKind, string> = {
  default: 'text-muted',
  primary: 'text-primary',
  accent: 'text-accent-soft',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  info: 'text-info',
  pink: 'text-pink',
  inverse: 'text-fg-strong',
};

export interface FlagIconsProps {
  flags: readonly GroupFlag[] | null | undefined;
  className?: string;
  /** Icon size in px (default 14). */
  size?: number;
}

/** Compact row of flag icons, each with a tooltip (label + description). */
export function FlagIcons({ flags, className, size = 14 }: FlagIconsProps) {
  if (!flags || flags.length === 0) return null;
  return (
    <span className={clsx('inline-flex flex-wrap items-center gap-1', className)}>
      {flags.map((flag) => {
        const Icon = GROUP_FLAG_ICONS[flag] ?? CircleSlash;
        const label = labelOf(GROUP_FLAG_LABELS, flag);
        const description = GROUP_FLAG_DESCRIPTIONS[flag];
        return (
          <Tooltip
            key={flag}
            content={
              <>
                <span className="font-semibold">{label}</span>
                {description && <span className="block text-header-fg/80">{description}</span>}
              </>
            }
          >
            <span
              tabIndex={0}
              role="img"
              aria-label={description ? `${label}: ${description}` : label}
              data-flag={flag}
              className={clsx('inline-flex rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-accent', KIND_TEXT[GROUP_FLAG_KIND[flag] ?? 'default'])}
            >
              <Icon width={size} height={size} aria-hidden />
            </span>
          </Tooltip>
        );
      })}
    </span>
  );
}
