import { Undo2 } from 'lucide-react';
import type { Action, Id } from '@/api/types';
import { ActionStatusBadge, Badge, Button, ByteSize, EmptyState, RelativeTime } from '@/components/ui';
import { DELETION_METHOD_LABELS, labelOf } from '@/lib/constants';
import { toDate } from '@/lib/format';

export interface GroupActionsListProps {
  actions: readonly Action[] | null | undefined;
  onRestore: (action: Action) => void;
  /** Action with a restore request in flight. */
  restoringId?: Id | null;
}

/**
 * True when a removal can be undone from Dupearr's own recycle bin: a real (not dry-run),
 * succeeded filesystem removal with a recorded recycle-bin path. *arr and Plex deletions are
 * never restorable from here (an *arr recycle bin is managed by the *arr itself).
 */
export function canRestore(action: Action): boolean {
  return (
    action.status === 'succeeded' &&
    !action.dryRun &&
    !action.permanent &&
    (!action.method || action.method === 'filesystem') &&
    !!action.recyclePath
  );
}

const time = (a: Action) => toDate(a.createdAt)?.getTime() ?? 0;

/** Removal actions of a group (newest first) with status, method, permanence and restore. */
export function GroupActionsList({ actions, onRestore, restoringId }: GroupActionsListProps) {
  const list = [...(actions ?? [])].sort((a, b) => time(b) - time(a) || b.id - a.id);

  return (
    <section aria-labelledby="actions-heading" className="rounded border border-border bg-card">
      <h2 id="actions-heading" className="m-0 border-b border-border px-4 py-2.5 text-base font-normal text-fg-strong">
        Actions
      </h2>
      {list.length === 0 ? (
        <EmptyState compact title="No actions yet" description="Removals appear here once the group is approved." />
      ) : (
        <ul className="m-0 flex list-none flex-col divide-y divide-border p-0">
          {list.map((a) => (
            <li key={a.id} className="flex flex-col gap-1.5 px-4 py-2.5 text-sm" data-action-id={a.id}>
              <div className="flex flex-wrap items-center gap-1.5">
                <ActionStatusBadge status={a.status} />
                {a.method && <Badge outline>{labelOf(DELETION_METHOD_LABELS, a.method)}</Badge>}
                {a.permanent && (
                  <Badge kind="danger" title="Cannot be undone: no recycle bin was used">
                    Permanent
                  </Badge>
                )}
                {a.dryRun && (
                  <Badge kind="info" outline title="Nothing was deleted">
                    Dry run
                  </Badge>
                )}
                <ByteSize bytes={a.size} className="ml-auto text-xs text-muted" />
              </div>
              {(a.paths ?? []).map((p) => (
                <code key={p} className="font-mono text-xs break-all text-fg">
                  {p}
                </code>
              ))}
              {a.message && <div className="text-xs break-words text-fg">{a.message}</div>}
              {a.recyclePath && (
                <div className="text-xs break-all text-muted">
                  Recycle bin: <span className="font-mono whitespace-pre-line">{a.recyclePath}</span>
                </div>
              )}
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
                <span>
                  Created <RelativeTime date={a.createdAt} />
                </span>
                {a.startedAt && (
                  <span>
                    Started <RelativeTime date={a.startedAt} />
                  </span>
                )}
                {a.finishedAt && (
                  <span>
                    Finished <RelativeTime date={a.finishedAt} />
                  </span>
                )}
                {canRestore(a) && (
                  <Button
                    size="sm"
                    icon={Undo2}
                    className="ml-auto"
                    loading={restoringId === a.id}
                    disabled={restoringId != null && restoringId !== a.id}
                    onClick={() => onRestore(a)}
                  >
                    Restore
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
