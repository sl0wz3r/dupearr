import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import type { BulkDuplicateAction, DeletionMethod, DuplicateGroupSummary, Id } from '@/api/types';
import { Alert, Button, ByteSize, Modal, TextInput } from '@/components/ui';
import { formatNumber } from '@/lib/format';
import {
  approvalSignatures,
  deletionCaveat,
  DRY_RUN_NOTE,
  groupTitle,
  requiresTypedConfirmation,
  summarizeBulk,
  summaryFingerprint,
  TYPED_CONFIRM_WORD,
} from './duplicateUtils';
import { summaryFileDescription } from './FileChips';
import { useChangeGuard } from './useChangeGuard';

export interface BulkActionDialogProps {
  open: boolean;
  action: BulkDuplicateAction;
  /**
   * Selected groups, as currently listed (fresh rows only — never stale snapshots). Ineligible
   * ones are listed as skipped and never sent.
   */
  groups: readonly DuplicateGroupSummary[];
  /**
   * One group from its row action: its files to remove are listed with a link to its detail page.
   * An empty `groups` then means the group is no longer listed (it changed or disappeared).
   */
  single?: boolean;
  /** `settings.dryRun`; unknown settings must be passed as `false` (the safe assumption). */
  dryRun: boolean;
  deletionMethods?: readonly DeletionMethod[];
  loading?: boolean;
  /**
   * Why the last confirmation was refused (e.g. the server's 409 "changed since it was
   * displayed"). Shown in the dialog, which stays open so the refreshed rows can be reviewed.
   */
  error?: string | null;
  /**
   * Confirmed ids (eligible groups only). For approvals, `signatures` maps each id to the group
   * signature the user reviewed — captured when the dialog opened, or when they acknowledged a
   * change — for the server's stale-decision check.
   */
  onConfirm: (ids: Id[], signatures: Record<Id, string>) => void;
  onCancel: () => void;
}

const plural = (n: number, one: string, many = `${one}s`) => `${formatNumber(n)} ${n === 1 ? one : many}`;

const CONFIRM_LABELS: Record<BulkDuplicateAction, string> = {
  approve: 'Approve',
  ignore: 'Ignore',
  unignore: 'Unignore',
};

/**
 * Confirmation for approve / ignore / unignore of one or many groups. Approvals summarize
 * groups/files/bytes, carry the deletion-method caveat (or the dry-run note) and — for more than
 * BULK_TYPED_CONFIRM_THRESHOLD groups outside dry run — require typing "DELETE".
 */
export function BulkActionDialog({
  open,
  action,
  groups,
  single = false,
  dryRun,
  deletionMethods,
  loading = false,
  error,
  onConfirm,
  onCancel,
}: BulkActionDialogProps) {
  const summary = useMemo(() => summarizeBulk(action, groups), [action, groups]);
  const needsTyping = requiresTypedConfirmation(action, summary.eligible.length, dryRun);
  const [typed, setTyped] = useState('');
  const cancelRef = useRef<HTMLButtonElement>(null);
  const inputId = useId();

  // Reset the typed word whenever the dialog is (re)opened.
  useEffect(() => {
    if (open) setTyped('');
  }, [open]);

  // What is approved must be what was read: block when the eligible groups (their status, server
  // signature, decisions, bytes) or dry run change while the dialog is open. Ignore/unignore
  // delete nothing and are not guarded. The signatures sent with the approval are the ones
  // captured with the guard's baseline (on open, or when the user acknowledged a change).
  const signatures = useMemo(() => approvalSignatures(summary.eligible), [summary.eligible]);
  const guard = useChangeGuard(
    open && action === 'approve',
    `${dryRun ? 'dry' : 'real'}#${summaryFingerprint(summary.eligible)}`,
    signatures,
  );

  const typedOk = !needsTyping || typed.trim() === TYPED_CONFIRM_WORD;
  const canConfirm = summary.eligible.length > 0 && typedOk && !guard.changed && !loading;
  const onlyGroup = single ? summary.eligible[0] : undefined;
  const vanished = single && groups.length === 0;
  const title = vanished
    ? `${CONFIRM_LABELS[action]} group`
    : single && groups[0]
      ? `${CONFIRM_LABELS[action]} “${groupTitle(groups[0])}”`
      : `${CONFIRM_LABELS[action]} ${plural(summary.eligible.length, 'group')}`;

  const confirm = () => {
    if (!canConfirm) return;
    const ids = summary.eligible.map((g) => g.id);
    const reviewed: Record<Id, string> = {};
    if (action === 'approve') {
      for (const id of ids) {
        const sig = guard.snapshot[id] ?? signatures[id];
        if (sig) reviewed[id] = sig;
      }
    }
    onConfirm(ids, reviewed);
  };

  return (
    <Modal
      open={open}
      onClose={() => {
        if (!loading) onCancel();
      }}
      title={title}
      size={single ? 'md' : 'sm'}
      initialFocusRef={needsTyping ? undefined : cancelRef}
      footer={
        <>
          <Button ref={cancelRef} onClick={onCancel} disabled={loading}>
            Cancel
          </Button>
          <Button
            variant={action === 'approve' ? (dryRun ? 'primary' : 'danger') : 'primary'}
            onClick={confirm}
            disabled={!canConfirm}
            loading={loading}
          >
            {action === 'approve' && dryRun ? 'Approve (dry run)' : CONFIRM_LABELS[action]}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm text-fg">
        {vanished ? (
          <p className="m-0">
            This group changed and is no longer listed with the current filters. Close this dialog and review it again.
          </p>
        ) : summary.eligible.length === 0 ? (
          <p className="m-0">
            None of the selected groups can be {action === 'approve' ? 'approved' : action === 'ignore' ? 'ignored' : 'unignored'}.
          </p>
        ) : action === 'approve' ? (
          <p className="m-0" data-testid="bulk-summary">
            {single ? 'Remove' : 'Approve'} <strong className="text-fg-strong">{plural(summary.files, 'file')}</strong>
            {!single && (
              <>
                {' '}
                from <strong className="text-fg-strong">{plural(summary.eligible.length, 'group')}</strong>
              </>
            )}
            , reclaiming <ByteSize bytes={summary.bytes} className="font-semibold text-fg-strong" />.
          </p>
        ) : action === 'ignore' ? (
          <p className="m-0" data-testid="bulk-summary">
            Ignore {plural(summary.eligible.length, 'group')} ({plural(summary.files, 'file')}). Ignored groups are kept
            across scans until un-ignored; nothing is deleted.
          </p>
        ) : (
          <p className="m-0" data-testid="bulk-summary">
            Unignore {plural(summary.eligible.length, 'group')}. They are re-evaluated with their profile and may propose
            removals again.
          </p>
        )}

        {onlyGroup && action === 'approve' && (
          <div className="flex flex-col gap-1.5">
            <ul
              aria-label="Files to remove"
              className="m-0 flex list-none flex-col gap-1 rounded border border-border bg-card-alt p-2"
            >
              {(onlyGroup.files ?? [])
                .filter((f) => f.decision === 'remove')
                .map((f) => (
                  <li key={f.id} className="flex items-center justify-between gap-3 text-xs">
                    <span className="min-w-0 truncate text-danger" title={summaryFileDescription(f)}>
                      {summaryFileDescription(f)}
                    </span>
                    <ByteSize bytes={f.size} className="shrink-0 text-muted" />
                  </li>
                ))}
            </ul>
            <Link
              to={`/duplicate/${onlyGroup.id}`}
              className="self-start text-xs text-accent-soft no-underline hover:underline"
              onClick={onCancel}
            >
              Compare the copies and see their paths first
            </Link>
          </div>
        )}

        {!single && action === 'approve' && summary.eligible.length > 0 && (
          <ul
            aria-label="Groups to approve"
            className="m-0 flex max-h-44 list-none flex-col gap-1 overflow-y-auto rounded border border-border bg-card-alt p-2"
          >
            {summary.eligible.map((g) => (
              <li key={g.id} className="flex items-center justify-between gap-3 text-xs">
                <span className="min-w-0 truncate text-fg" title={groupTitle(g)}>
                  {groupTitle(g)}
                </span>
                <span className="shrink-0 text-muted">
                  {plural(g.removeCount, 'file')} · <ByteSize bytes={g.reclaimableBytes} />
                </span>
              </li>
            ))}
          </ul>
        )}

        {summary.skipped.length > 0 && (
          <p className="m-0 text-muted" data-testid="bulk-skipped">
            {plural(summary.skipped.length, 'selected group')} {summary.skipped.length === 1 ? 'is' : 'are'} not eligible
            and will be skipped.
            {summary.reviewSkipped > 0 && (
              <>
                {' '}
                {plural(summary.reviewSkipped, 'group')} {summary.reviewSkipped === 1 ? 'needs' : 'need'} review and must
                be approved one at a time from the detail page.
              </>
            )}
            {summary.discSkipped > 0 && (
              <>
                {' '}
                {plural(summary.discSkipped, 'group')} {summary.discSkipped === 1 ? 'removes' : 'remove'} a full disc and
                must be approved one at a time from the detail page.
              </>
            )}
            {summary.clipSkipped > 0 && (
              <>
                {' '}
                {plural(summary.clipSkipped, 'group')} would remove a single disc clip (e.g. a loose 00800.m2ts). A movie
                can span several clips, so clips are never removed one by one — re-scan{' '}
                {summary.clipSkipped === 1 ? 'it' : 'them'} to show the clips as one copy.
              </>
            )}
          </p>
        )}

        {error && (
          <Alert kind="error" title={action === 'approve' ? 'Approval refused' : 'Action refused'}>
            {error}
          </Alert>
        )}

        {guard.changed && summary.eligible.length > 0 && (
          <Alert
            kind="warning"
            title="The selection changed while the dialog was open"
            actions={
              <Button size="sm" onClick={guard.acknowledge}>
                I’ve reviewed the changes
              </Button>
            }
          >
            A scan or another session updated {single ? 'this group' : 'some of these groups'}. Review the summary again
            before confirming.
          </Alert>
        )}

        {action === 'approve' && summary.eligible.length > 0 && (
          <Alert kind={dryRun ? 'info' : 'warning'} title={dryRun ? 'Dry run' : 'Files will be deleted'}>
            {dryRun && <p className="m-0 mb-1">{DRY_RUN_NOTE}</p>}
            <p className="m-0">{deletionCaveat(deletionMethods)}</p>
          </Alert>
        )}

        {needsTyping && summary.eligible.length > 0 && (
          <div className="flex flex-col gap-1.5">
            <label htmlFor={inputId} className="text-fg">
              Type <strong className="font-mono text-danger">{TYPED_CONFIRM_WORD}</strong> to approve{' '}
              {plural(summary.eligible.length, 'group')}:
            </label>
            <TextInput
              id={inputId}
              data-autofocus
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  confirm();
                }
              }}
              invalid={typed.length > 0 && !typedOk}
            />
          </div>
        )}
      </div>
    </Modal>
  );
}
