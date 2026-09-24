/** Small cells shared by the Queue and History → Actions tables. */
import { Link } from 'react-router';
import type { Action } from '@/api/types';
import { ActionStatusBadge, Badge, Spinner } from '@/components/ui';
import { DELETION_METHOD_LABELS, labelOf } from '@/lib/constants';

/** Group title linking to the duplicate detail page, with the action message underneath. */
export function ActionTitleCell({ action }: { action: Action }) {
  const title = action.title || `Group #${action.groupId}`;
  return (
    <div className="min-w-0">
      {action.groupId > 0 ? (
        <Link to={`/duplicate/${action.groupId}`} className="font-medium text-fg-strong hover:text-accent-soft">
          {title}
        </Link>
      ) : (
        <span className="font-medium text-fg-strong">{title}</span>
      )}
      {action.message && (
        <div className="mt-0.5 line-clamp-2 text-xs break-words text-muted" title={action.message}>
          {action.message}
        </div>
      )}
    </div>
  );
}

/** Status badge (spinner while running) plus a "Dry Run" badge for dry-run actions. */
export function ActionStatusCell({ action }: { action: Action }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {/* Keep the spinner next to its badge; only the dry-run badge may wrap on narrow screens. */}
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
        {action.status === 'running' && <Spinner size="xs" label="Running" />}
        <ActionStatusBadge status={action.status} />
      </span>
      {action.dryRun && action.status !== 'dry_run' && (
        <Badge kind="info" outline title="Dry run: nothing is deleted, the result is only recorded">
          Dry Run
        </Badge>
      )}
    </span>
  );
}

/** Deletion method label ("-" until the executor picked one). */
export function ActionMethodCell({ action }: { action: Action }) {
  if (!action.method) return <span className="text-muted">-</span>;
  return <span>{labelOf(DELETION_METHOD_LABELS, action.method)}</span>;
}

/**
 * How a removal went: `permanent` (cannot be undone), `recycle` (moved to a recycle bin), or null
 * when nothing was removed (not executed yet, failed, skipped, cancelled) or the method is unknown.
 */
export function removalKind(action: Action): 'permanent' | 'recycle' | null {
  const removed = action.status === 'succeeded' || action.status === 'dry_run';
  if (!action.method || !removed) return null;
  return action.permanent ? 'permanent' : 'recycle';
}

/**
 * Red "Permanent" vs green "Recycle bin" badge (DECISIONS D3/D6). Shown only for removals that
 * happened (or were simulated by a dry run) — for other statuses nothing was removed. With
 * `permanentOnly`, only the red badge is rendered (compact layouts).
 */
export function PermanentBadge({ action, permanentOnly = false }: { action: Action; permanentOnly?: boolean }) {
  const kind = removalKind(action);
  if (kind === null || (permanentOnly && kind !== 'permanent')) {
    return permanentOnly ? null : <span className="text-muted">-</span>;
  }
  const hypothetical = action.dryRun || action.status === 'dry_run';
  if (kind === 'permanent') {
    return (
      <Badge
        kind="danger"
        title={hypothetical ? 'Would be deleted permanently; could not be undone' : 'Deleted permanently; cannot be undone'}
      >
        Permanent
      </Badge>
    );
  }
  const title = action.recyclePath
    ? `Moved to the recycle bin: ${action.recyclePath}`
    : hypothetical
      ? 'Would go to a recycle bin'
      : 'Moved to a recycle bin (Dupearr or the *arr)';
  return (
    <Badge kind="success" title={title}>
      Recycle bin
    </Badge>
  );
}
