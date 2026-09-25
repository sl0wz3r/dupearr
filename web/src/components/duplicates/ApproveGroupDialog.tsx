import { Disc3 } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import type { DeletionMethod, DuplicateGroup, GroupFile, GroupStatus, Id } from '@/api/types';
import { Alert, Button, ByteSize, Checkbox, Modal } from '@/components/ui';
import { DELETION_METHOD_LABELS, DELETION_METHODS, GROUP_STATUS_LABELS, labelOf } from '@/lib/constants';
import { discTypeLabel, dynamicRangeLabel, formatNumber, resolutionLabel } from '@/lib/format';
import {
  clipCountOf,
  discApprovalBlocker,
  discRemovalKeepsNote,
  discRemovalNote,
  isClipSet,
  type DiscApprovalContext,
  type DiscSettings,
} from './disc';
import {
  APPROVABLE_STATUSES,
  deletionCaveat,
  DRY_RUN_NOTE,
  filesFingerprint,
  groupTitle,
  hasJellyfinCopy,
  hasMissingPart,
  isHardlinked,
  isJellyfinVersion,
  reportOnlyReasons,
  versionPaths,
  versionSize,
} from './duplicateUtils';
import { useChangeGuard } from './useChangeGuard';

/**
 * Best guess of how one copy will be removed, following the configured method order. The
 * executor decides for real at execution time (docs/ARCHITECTURE.md §6), so this is phrased as
 * an expectation and always mentions permanence.
 */
export function expectedMethodNote(file: GroupFile, methods: readonly DeletionMethod[] | null | undefined): string {
  const order = methods && methods.length > 0 ? methods : DELETION_METHODS;
  const v = file.version;
  if (v.disc) {
    // A disc is only ever removed as a whole by Dupearr itself (never per file through Plex/*arr);
    // so is a loose clip set (every clip of its folder at once).
    const what = isClipSet(v.disc) ? 'the loose clips are moved' : 'the disc is moved';
    const note = `Expected via the filesystem — ${what} to Dupearr’s recycle bin (never through Plex or Radarr/Sonarr).`;
    return order.includes('filesystem')
      ? note
      : `${note} Filesystem is not one of your deletion methods, so the removal may be refused.`;
  }
  const hasLocal = (v.parts ?? []).some((p) => !!p.localPath);
  if (isJellyfinVersion(v)) {
    // docs/DECISIONS.md D12: never through Jellyfin, and only into a recycle bin.
    for (const m of order) {
      if (m === 'arr' && v.arr) {
        const who = v.arr.instanceName || 'the *arr';
        return `Expected via ${who} — only when ${who} has a recycle bin configured (a Jellyfin copy is never deleted permanently).`;
      }
      if (m === 'filesystem' && hasLocal) {
        return 'Expected via the filesystem — moved to Dupearr’s recycle bin (refused when none is configured).';
      }
    }
    return 'No configured method looks applicable — Dupearr never deletes through Jellyfin, so the action may fail.';
  }
  for (const m of order) {
    if (m === 'arr' && v.arr) {
      const who = v.arr.instanceName || 'the *arr';
      return `Expected via ${who} — permanent unless ${who} has a recycle bin configured.`;
    }
    if (m === 'plex') return 'Expected via Plex — permanent (requires “Allow media deletion”).';
    if (m === 'filesystem' && hasLocal) {
      return 'Expected via the filesystem — moved to the recycle bin when one is configured, otherwise permanent.';
    }
  }
  return `No configured method (${order.map((m) => labelOf(DELETION_METHOD_LABELS, m)).join(', ')}) looks applicable — the action may fail.`;
}

export interface ApproveGroupDialogProps {
  open: boolean;
  group: DuplicateGroup;
  /** Files ordered by rank. */
  files: readonly GroupFile[];
  /** `settings.dryRun` (pass false when unknown — the safe assumption). */
  dryRun: boolean;
  deletionMethods?: readonly DeletionMethod[];
  libraryNames?: ReadonlyMap<Id, string>;
  /**
   * The settings a full-disc removal depends on (`allowDiscRemoval`, `recycleBinPath`,
   * `keepPlayableCopy`). Unknown (null/undefined) blocks every disc removal.
   */
  settings?: DiscSettings | null;
  loading?: boolean;
  /**
   * Page-level reason approval must wait (e.g. the group could not be refreshed, or a re-scan of
   * it is running). Shown in the dialog and disables the confirm button.
   */
  blockedReason?: string | null;
  /**
   * Why the last confirmation was refused (e.g. the server's 409 "changed since it was
   * displayed"). Shown in the dialog, which stays open while the group is refreshed.
   */
  error?: string | null;
  /**
   * Called with the group signature the user reviewed: captured when the dialog opened (or when
   * they acknowledged a change) and sent back so the server refuses a stale approval.
   */
  onConfirm: (signature: string) => void;
  onCancel: () => void;
}

/**
 * Why approval must be blocked client-side. Defense in depth only: the server re-checks every
 * invariant, but the UI never offers to confirm a removal list that visibly breaks one
 * (docs/ARCHITECTURE.md §6 safety invariants, docs/DECISIONS.md D2/D4), including the full-disc
 * rules ({@link discApprovalBlocker}; `disc` carries the settings and media type they need).
 */
export function approvalBlocker(
  files: readonly GroupFile[],
  status?: GroupStatus,
  disc: DiscApprovalContext = {},
): string | null {
  if (status !== undefined && !APPROVABLE_STATUSES.has(status)) {
    return `A group that is ${labelOf(GROUP_STATUS_LABELS, status).toLowerCase()} can't be approved.`;
  }
  const keepers = files.filter((f) => f.decision === 'keep');
  const removals = files.filter((f) => f.decision === 'remove');
  if (removals.length === 0) return 'Nothing to remove: every copy is kept.';
  if (keepers.length === 0) return 'No copy would be kept. Set at least one copy to Keep.';
  if (removals.some((f) => f.protected)) {
    return 'A protected copy is marked for removal. Re-scan the group before approving.';
  }
  const reportOnly = reportOnlyReasons(files);
  if (reportOnly.length > 0) return `This duplicate is only reported: ${reportOnly.join('; ')}.`;
  if (removals.some((f) => f.version?.optimizedVersion)) {
    return 'An optimized version (Plex Versions) is marked for removal. Re-scan the group before approving.';
  }
  if (keepers.every((f) => hasMissingPart(f.version))) {
    return hasJellyfinCopy(keepers)
      ? 'Every kept copy is missing. Re-scan the group before approving.'
      : 'Plex reports every kept copy as missing. Re-scan the group before approving.';
  }
  if (hasJellyfinCopy(files)) {
    // Jellyfin never reports whether a file exists: every copy's files must be visible to Dupearr.
    const unmapped = files.flatMap((f) => (f.version?.parts ?? []).filter((p) => !p.localPath)).at(0);
    if (unmapped) {
      return `No path mapping covers ${unmapped.path}: every copy of a Jellyfin duplicate needs one (Settings → Media Management).`;
    }
  }
  const keeperPaths = new Set(keepers.flatMap((f) => versionPaths(f.version)));
  if (removals.some((f) => versionPaths(f.version).some((p) => keeperPaths.has(p)))) {
    return 'A copy to remove shares a file path with a kept copy. Re-scan the group before approving.';
  }
  return discApprovalBlocker(files, disc);
}

/**
 * Approval confirmation for one group: every file that will be removed (paths, size, expected
 * method), the deletion-method caveat or the dry-run note, and the copies that stay.
 */
export function ApproveGroupDialog({
  open,
  group,
  files,
  dryRun,
  deletionMethods,
  libraryNames,
  settings,
  loading = false,
  blockedReason,
  error,
  onConfirm,
  onCancel,
}: ApproveGroupDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const removals = files.filter((f) => f.decision === 'remove');
  const keepers = files.filter((f) => f.decision === 'keep');
  const blocker = approvalBlocker(files, group.status, { settings, mediaType: group.mediaType });
  const needsReviewAck = group.status === 'review';
  const [reviewAck, setReviewAck] = useState(false);
  // Whatever the user reads here is what they confirm: a live update of the copies, of the
  // server's signature (versions + decisions) or of dry run while the dialog is open must be
  // acknowledged before Approve is possible again. The signature sent with the approval is the one
  // captured with that baseline.
  const signature = group.signature ?? '';
  const guard = useChangeGuard(
    open,
    `${dryRun ? 'dry' : 'real'}#${signature}#${filesFingerprint(group.status, files)}`,
    signature,
  );

  useEffect(() => {
    if (open) setReviewAck(false);
  }, [open]);

  const canConfirm = !blocker && !blockedReason && !guard.changed && (!needsReviewAck || reviewAck) && !loading;
  const confirm = () => {
    if (canConfirm) onConfirm(guard.snapshot);
  };
  const describe = (f: GroupFile) =>
    [
      discTypeLabel(f.version.disc?.type),
      resolutionLabel(f.version.resolution),
      dynamicRangeLabel(f.version.dynamicRange),
      libraryNames?.get(f.version.libraryId) || f.version.libraryTitle,
      f.version.arr?.instanceName,
    ]
      .filter(Boolean)
      .join(' · ');

  return (
    <Modal
      open={open}
      onClose={() => {
        if (!loading) onCancel();
      }}
      title={`Approve “${groupTitle(group)}”`}
      size="lg"
      initialFocusRef={cancelRef}
      footer={
        <>
          <Button ref={cancelRef} onClick={onCancel} disabled={loading}>
            Cancel
          </Button>
          <Button variant={dryRun ? 'primary' : 'danger'} onClick={confirm} loading={loading} disabled={!canConfirm}>
            {dryRun ? 'Approve (dry run)' : `Approve & remove ${formatNumber(removals.length)}`}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm text-fg">
        <p className="m-0" data-testid="approve-summary">
          Remove <strong className="text-fg-strong">{formatNumber(removals.length)}</strong> of{' '}
          {formatNumber(files.length)} copies, reclaiming{' '}
          <ByteSize bytes={group.reclaimableBytes} className="font-semibold text-fg-strong" />.
        </p>

        {blocker && <Alert kind="error" title="Approval blocked">{blocker}</Alert>}

        {error && (
          <Alert kind="error" title="Approval refused">
            {error}
          </Alert>
        )}

        {!blocker && blockedReason && <Alert kind="warning" title="Approval unavailable">{blockedReason}</Alert>}

        {guard.changed && (
          <Alert
            kind="warning"
            title="This group changed while the dialog was open"
            actions={
              <Button size="sm" onClick={guard.acknowledge}>
                I’ve reviewed the changes
              </Button>
            }
          >
            The copies to remove (or the dry-run setting) were updated by a scan or another session. Review the lists
            below again before approving.
          </Alert>
        )}

        {needsReviewAck && (
          <Alert kind="warning" title="Needs review">
            {group.statusReason || 'This group was flagged for review.'} Make sure every copy really is the same
            content before approving.
          </Alert>
        )}

        <Alert kind={dryRun ? 'info' : 'warning'} title={dryRun ? 'Dry run' : 'Files will be deleted'}>
          {dryRun && <p className="m-0 mb-1">{DRY_RUN_NOTE}</p>}
          <p className="m-0">{deletionCaveat(deletionMethods)}</p>
          {hasJellyfinCopy(files) && (
            <p className="m-0 mt-1" data-testid="jellyfin-note">
              Dupearr never deletes through Jellyfin: Jellyfin copies are only moved to a recycle bin (Radarr/Sonarr’s
              or Dupearr’s), and Jellyfin is told about each removed file.
            </p>
          )}
        </Alert>

        <div>
          <h3 className="m-0 mb-1.5 text-xs font-semibold tracking-wide text-danger uppercase">Will be removed</h3>
          <ul className="m-0 flex list-none flex-col gap-2 p-0" aria-label="Files to remove">
            {removals.map((f) => {
              const disc = f.version.disc;
              return (
                <li key={f.id} className="rounded border border-danger/40 bg-danger/5 p-2.5" data-disc={disc ? disc.type : undefined}>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-semibold text-fg-strong">
                      #{f.rank} {describe(f)}
                    </span>
                    <ByteSize bytes={versionSize(f.version)} className="text-muted" />
                  </div>
                  {disc ? (
                    <div className="mt-1 flex flex-col gap-1">
                      <p className="m-0 flex items-start gap-1.5 font-semibold text-danger" data-testid="disc-removal-note">
                        <Disc3 aria-hidden width={14} height={14} className="mt-0.5 shrink-0" />
                        <span>{discRemovalNote({ ...disc, clipCount: clipCountOf(f.version) })}</span>
                      </p>
                      <code className="block font-mono text-xs break-all text-fg">{disc.root || '—'}</code>
                      {disc.localRoot && disc.localRoot !== disc.root && (
                        <code className="block font-mono text-[11px] break-all text-muted">local: {disc.localRoot}</code>
                      )}
                      <p className="m-0 text-xs text-muted">{discRemovalKeepsNote(disc)}</p>
                    </div>
                  ) : (
                    versionPaths(f.version).map((p) => (
                      <code key={p} className="mt-1 block font-mono text-xs break-all text-fg">
                        {p}
                      </code>
                    ))
                  )}
                  {isHardlinked(f.version) && (
                    <div className="mt-1 text-xs text-warning">hardlinked — won’t free space</div>
                  )}
                  <div className="mt-1 text-xs text-muted">{expectedMethodNote(f, deletionMethods)}</div>
                </li>
              );
            })}
          </ul>
        </div>

        {keepers.length > 0 && (
          <div>
            <h3 className="m-0 mb-1.5 text-xs font-semibold tracking-wide text-success uppercase">Will be kept</h3>
            <ul className="m-0 flex list-none flex-col gap-1 p-0" aria-label="Files to keep">
              {keepers.map((f) => (
                <li key={f.id} className="flex flex-wrap items-center justify-between gap-2 text-xs">
                  <span className="min-w-0 text-fg">
                    #{f.rank} {describe(f)}
                    {f.protected && <span className="text-primary"> · protected</span>}
                  </span>
                  <ByteSize bytes={versionSize(f.version)} className="text-muted" />
                </li>
              ))}
            </ul>
          </div>
        )}

        {needsReviewAck && !blocker && (
          <Checkbox
            checked={reviewAck}
            onChange={setReviewAck}
            disabled={loading}
            label="I compared the copies: they are the same content and the ones listed above can be removed"
          />
        )}
      </div>
    </Modal>
  );
}
