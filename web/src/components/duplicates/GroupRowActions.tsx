import { clsx } from 'clsx';
import { Check, Eye, EyeOff } from 'lucide-react';
import type { DuplicateGroupSummary, Id } from '@/api/types';
import { IconButton } from '@/components/ui';
import { canApproveFromList, canIgnore, canUnignore } from './duplicateUtils';

/** Row-level callbacks shared by the table, the mobile cards and the poster grid. */
export interface GroupRowActionHandlers {
  onApprove: (group: DuplicateGroupSummary) => void;
  onIgnore: (group: DuplicateGroupSummary) => void;
  onUnignore: (group: DuplicateGroupSummary) => void;
  /** Group with a single action in flight (its buttons show a spinner). */
  busyId?: Id | null;
}

export interface GroupRowActionsProps extends GroupRowActionHandlers {
  group: DuplicateGroupSummary;
  className?: string;
}

/**
 * Approve / Ignore / Unignore icon buttons, shown according to the group's status. `review` groups
 * and groups that remove a full disc get no Approve button here: they are approved from their
 * detail page after comparing the copies.
 */
export function GroupRowActions({ group, onApprove, onIgnore, onUnignore, busyId, className }: GroupRowActionsProps) {
  const busy = busyId === group.id;
  const approvable = canApproveFromList(group.status, group.removeCount, group.files);
  return (
    <div className={clsx('flex items-center justify-end gap-0.5', className)}>
      {approvable && (
        <IconButton
          icon={Check}
          label={`Approve ${group.title}`}
          title="Approve removals"
          size="sm"
          variant="primary"
          loading={busy}
          disabled={busyId != null && !busy}
          onClick={(e) => {
            e.stopPropagation();
            onApprove(group);
          }}
        />
      )}
      {canIgnore(group.status) && (
        <IconButton
          icon={EyeOff}
          label={`Ignore ${group.title}`}
          title="Ignore"
          size="sm"
          disabled={busyId != null}
          onClick={(e) => {
            e.stopPropagation();
            onIgnore(group);
          }}
        />
      )}
      {canUnignore(group.status) && (
        <IconButton
          icon={Eye}
          label={`Unignore ${group.title}`}
          title="Unignore"
          size="sm"
          loading={busy}
          disabled={busyId != null && !busy}
          onClick={(e) => {
            e.stopPropagation();
            onUnignore(group);
          }}
        />
      )}
    </div>
  );
}
