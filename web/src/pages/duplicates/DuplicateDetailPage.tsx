import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Check, Eye, EyeOff, FileQuestion, RefreshCw } from 'lucide-react';
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router';
import { errorMessage, isApiError } from '@/api/client';
import { useRestoreAction } from '@/api/hooks/useActivity';
import { useCommand } from '@/api/hooks/useCommands';
import {
  useApproveDuplicate,
  useDuplicate,
  useIgnoreDuplicate,
  useRescanDuplicate,
  useSetFileOverride,
  useUnignoreDuplicate,
} from '@/api/hooks/useDuplicates';
import { useLibraries } from '@/api/hooks/useMediaServers';
import { useProfile } from '@/api/hooks/useProfiles';
import { useSettings } from '@/api/hooks/useSettings';
import { queryKeys } from '@/api/queryKeys';
import type { Action, DuplicateGroup, DuplicateGroupDetail, GroupFile, Id } from '@/api/types';
import {
  ApproveGroupDialog,
  ComparisonTable,
  DecisionSummary,
  GroupActionsList,
  GroupHeaderCard,
  IgnoreDialog,
  OVERRIDE_LOCKED_STATUSES,
  canApprove,
  canIgnore,
  canUnignore,
  discMemberPath,
  groupTitle,
  overrideBlocker,
  sortByRank,
  type OverrideValue,
} from '@/components/duplicates';
import {
  PageBody,
  PageContent,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
  ToolbarSeparator,
} from '@/components/page';
import { Alert, Button, EmptyState, LinkButton, LoadingIndicator, useToast } from '@/components/ui';
import { formatNumber } from '@/lib/format';

/** Parses the `:id` route param (positive integer) — anything else is "invalid". */
function parseId(raw: string | undefined): Id | null {
  if (!raw || !/^\d{1,15}$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

/** Loose runtime check before trusting a mutation response as a group. */
function isGroup(value: unknown, id: Id): value is DuplicateGroup {
  return (
    !!value &&
    typeof value === 'object' &&
    (value as DuplicateGroup).id === id &&
    Array.isArray((value as DuplicateGroup).files)
  );
}

interface PendingOverride {
  fileId: Id;
  value: OverrideValue;
}

/**
 * Duplicate detail at `/duplicate/:id`: header, decision summary, side-by-side comparison with
 * per-copy overrides, approve / ignore / unignore / re-scan, and the group's removal actions.
 */
export default function DuplicateDetailPage() {
  const params = useParams<{ id: string }>();
  const id = parseId(params.id);
  const navigate = useNavigate();
  const location = useLocation();
  const qc = useQueryClient();
  const toast = useToast();

  const query = useDuplicate(id);
  const group = query.data;
  const settings = useSettings();
  const libraries = useLibraries();
  const profile = useProfile(group?.profileId);

  const approve = useApproveDuplicate();
  const ignore = useIgnoreDuplicate();
  const unignore = useUnignoreDuplicate();
  const setOverride = useSetFileOverride();
  const rescan = useRescanDuplicate();
  const restore = useRestoreAction();

  // Follow the TargetedScan started by "Re-scan" (polled while active) for the spinner, and
  // refresh the group when it ends (SSE normally does this too; this covers a dropped stream).
  const rescanCommand = useCommand(id !== null && rescan.variables === id ? rescan.data?.id : undefined);
  const rescanStatus = rescanCommand.data?.status;
  const rescanning = rescan.isPending || rescanStatus === 'queued' || rescanStatus === 'started';
  const lastRescanStatus = useRef(rescanStatus);
  useEffect(() => {
    const previous = lastRescanStatus.current;
    lastRescanStatus.current = rescanStatus;
    if (id === null || previous === rescanStatus) return;
    if (rescanStatus === 'completed') {
      void qc.invalidateQueries({ queryKey: queryKeys.duplicates.detail(id) });
    } else if (rescanStatus === 'failed' || rescanStatus === 'aborted') {
      void qc.invalidateQueries({ queryKey: queryKeys.duplicates.detail(id) });
      toast.error('Re-scan failed', rescanCommand.data?.message);
    }
  }, [rescanStatus, rescanCommand.data?.message, id, qc, toast]);

  const [pendingOverride, setPendingOverride] = useState<PendingOverride | null>(null);
  const [approveOpen, setApproveOpen] = useState(false);
  /** Server message of a refused (409) approval, shown in the still-open dialog. */
  const [approveError, setApproveError] = useState<string | null>(null);
  const [ignoreOpen, setIgnoreOpen] = useState(false);
  const [restoringId, setRestoringId] = useState<Id | null>(null);

  const files = useMemo(() => sortByRank(group?.files), [group?.files]);
  const libraryNames = useMemo(
    () => new Map((libraries.data ?? []).map((l) => [l.id, l.title] as const)),
    [libraries.data],
  );
  // Unknown settings ⇒ assume a real run (stricter wording), never the other way round.
  const dryRun = settings.data?.dryRun === true;

  const goBack = () => {
    // Return to the list with its filters when we came from inside the app; else go home.
    if (location.key !== 'default') navigate(-1);
    else navigate('/');
  };

  // --- Overrides ---------------------------------------------------------------

  const overrideValue = (f: GroupFile): OverrideValue =>
    pendingOverride?.fileId === f.id ? pendingOverride.value : f.override || 'auto';

  const onOverride = (f: GroupFile, value: OverrideValue) => {
    if (!group || pendingOverride) return;
    // SAFETY (client-side backup of the server's refusal): a single file of a disc — a loose
    // 00800.m2ts clip or a file inside BDMV/ — is never marked for removal on its own.
    const refused = overrideBlocker(f, value);
    if (refused) {
      toast.error('A disc clip is never removed on its own', refused);
      return;
    }
    const groupId = group.id;
    setPendingOverride({ fileId: f.id, value });
    setOverride.mutate(
      { groupId, fileId: f.id, decision: value === 'auto' ? null : value },
      {
        onSuccess: (updated) => {
          if (isGroup(updated, groupId)) {
            qc.setQueryData<DuplicateGroupDetail>(queryKeys.duplicates.detail(groupId), (old) =>
              old ? { ...old, ...updated, actions: old.actions ?? [] } : old,
            );
          }
          setPendingOverride(null);
        },
        onError: (e) => {
          // Revert to the server state and explain (e.g. 400: at least one copy must be kept).
          setPendingOverride(null);
          toast.error('Override rejected', errorMessage(e));
        },
      },
    );
  };

  // --- Group actions -------------------------------------------------------------

  const openApprove = () => {
    setApproveError(null);
    setApproveOpen(true);
  };

  const closeApprove = () => {
    setApproveOpen(false);
    setApproveError(null);
  };

  /** `signature`: the group signature the user reviewed in the dialog. */
  const confirmApprove = (signature: string) => {
    if (!group) return;
    const title = groupTitle(group);
    setApproveError(null);
    approve.mutate(
      { id: group.id, signature },
      {
        onSuccess: (actions) => {
          const n = Array.isArray(actions) ? actions.length : 0;
          toast.success(
            `Approved “${title}”`,
            `${formatNumber(n)} ${n === 1 ? 'removal' : 'removals'} queued${dryRun ? ' (dry run: nothing is deleted)' : ''}.`,
          );
          closeApprove();
        },
        onError: (e) => {
          // 409: the decisions changed since they were shown (or the group was queued/resolved
          // meanwhile). The hook refetches the group; the dialog stays open and its change guard
          // asks the user to review the refreshed copies before approving again.
          if (isApiError(e) && e.isConflict) setApproveError(errorMessage(e));
          else toast.error('Could not approve', errorMessage(e));
        },
      },
    );
  };

  const confirmIgnore = (addExclusion: boolean) => {
    if (!group) return;
    ignore.mutate(
      { id: group.id, addExclusion },
      {
        onSuccess: () => {
          toast.success('Ignored', addExclusion ? 'An exclusion was added for future scans.' : undefined);
          setIgnoreOpen(false);
        },
        onError: (e) => toast.error('Could not ignore', errorMessage(e)),
      },
    );
  };

  const doUnignore = () => {
    if (!group) return;
    unignore.mutate(group.id, {
      onSuccess: () => toast.success('Unignored', 'The group was re-evaluated.'),
      onError: (e) => toast.error('Could not unignore', errorMessage(e)),
    });
  };

  const doRescan = () => {
    if (!group) return;
    rescan.mutate(group.id, {
      onSuccess: () => toast.info('Re-scan queued', 'The group refreshes when the targeted scan finishes.'),
      onError: (e) => toast.error('Could not re-scan', errorMessage(e)),
    });
  };

  const doRestore = (action: Action) => {
    setRestoringId(action.id);
    restore.mutate(action.id, {
      onSuccess: () => toast.success('File restored', action.paths?.[0]),
      onError: (e) => toast.error('Restore failed', errorMessage(e)),
      onSettled: () => setRestoringId(null),
    });
  };

  // --- Render ----------------------------------------------------------------------

  const removeCount = files.filter((f) => f.decision === 'remove').length;
  // Copies that are single files of a disc (typically loose 00800.m2ts clips Plex lists as separate
  // versions, stored before loose clip sets were recognised).
  const clipCopies = files.filter((f) => !f.version.disc && discMemberPath(f.version) !== null).length;
  // SAFETY: never approve from data we know may be out of date — a failed background refresh
  // leaves the last good copy on screen, and a running re-scan is about to re-evaluate it.
  const stale = query.isError && !!group;
  const approveBlockedReason = stale
    ? 'This group could not be refreshed, so what is shown may be out of date. Retry before approving.'
    : rescanning
      ? 'A re-scan of this group is running. Wait for it to finish, then review the copies again.'
      : null;
  const approvable = !!group && canApprove(group.status, removeCount) && approveBlockedReason === null;
  const ignorable = !!group && canIgnore(group.status);
  const unignorable = !!group && canUnignore(group.status);
  const lockedReason = !group
    ? null
    : OVERRIDE_LOCKED_STATUSES.has(group.status)
      ? 'Removals for this group are queued or done — overrides are locked.'
      : approve.isPending
        ? 'Approving…'
        : null;

  let body: ReactNode;
  if (id === null) {
    body = (
      <EmptyState
        icon={FileQuestion}
        title="Invalid duplicate"
        description={`“${params.id ?? ''}” is not a valid duplicate id.`}
        action={<LinkButton to="/">Back to Duplicates</LinkButton>}
      />
    );
  } else if (query.isPending) {
    body = <LoadingIndicator message="Loading duplicate…" />;
  } else if (query.isError && !group) {
    body =
      isApiError(query.error) && query.error.isNotFound ? (
        <EmptyState
          icon={FileQuestion}
          title="Duplicate not found"
          description="It may have been resolved and cleaned up, or the id is wrong."
          action={<LinkButton to="/">Back to Duplicates</LinkButton>}
        />
      ) : (
        <Alert
          kind="error"
          title="Could not load the duplicate"
          actions={
            <Button size="sm" onClick={() => void query.refetch()}>
              Retry
            </Button>
          }
        >
          {errorMessage(query.error)}
        </Alert>
      );
  } else if (group) {
    body = (
      <div className="flex flex-col gap-4">
        {stale && (
          <Alert
            kind="warning"
            title="Showing the last loaded data"
            actions={
              <Button size="sm" loading={query.isFetching} onClick={() => void query.refetch()}>
                Retry
              </Button>
            }
          >
            {errorMessage(query.error)} Approval is disabled until the group is refreshed.
          </Alert>
        )}
        <GroupHeaderCard group={group} libraryNames={libraryNames} profileName={profile.data?.name} />
        {clipCopies > 0 && (
          <Alert
            kind="warning"
            title="Files of a disc are listed as separate copies"
            actions={
              <Button size="sm" disabled={rescanning} loading={rescan.isPending} onClick={doRescan}>
                Re-scan
              </Button>
            }
          >
            {clipCopies === 1 ? 'One copy is' : `${formatNumber(clipCopies)} copies are`} a single file of a disc (e.g. a
            Blu-ray clip like 00800.m2ts stored loose in the movie folder). A movie can span several clips, so these
            files are never removed one by one. Re-scan this group to show the clips of a folder as one copy.
          </Alert>
        )}
        <DecisionSummary files={files} reclaimableBytes={group.reclaimableBytes} libraryNames={libraryNames} />
        <section aria-labelledby="comparison-heading" className="flex flex-col gap-2">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h2 id="comparison-heading" className="m-0 text-base font-normal text-fg-strong">
              Comparison
            </h2>
            {lockedReason && <span className="text-xs text-muted">{lockedReason}</span>}
          </div>
          {files.length === 0 ? (
            <EmptyState compact title="No copies" description="This group has no files left." />
          ) : (
            <ComparisonTable
              files={files}
              profile={profile.data}
              libraryNames={libraryNames}
              overrideValue={overrideValue}
              onOverride={onOverride}
              pendingFileId={pendingOverride?.fileId ?? null}
              lockedReason={lockedReason}
            />
          )}
        </section>
        <GroupActionsList actions={group.actions} onRestore={doRestore} restoringId={restoringId} />
      </div>
    );
  }

  return (
    <PageContent title={group ? groupTitle(group) : 'Duplicate'}>
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={Check}
            label="Approve"
            kind="success"
            disabled={!approvable || pendingOverride !== null}
            loading={approve.isPending}
            onClick={openApprove}
            title={
              approvable
                ? 'Approve the proposed removals'
                : (approveBlockedReason ?? 'Nothing to approve in this status')
            }
          />
          {unignorable ? (
            <ToolbarButton icon={Eye} label="Unignore" loading={unignore.isPending} onClick={doUnignore} />
          ) : (
            <ToolbarButton
              icon={EyeOff}
              label="Ignore"
              disabled={!ignorable}
              loading={ignore.isPending}
              onClick={() => setIgnoreOpen(true)}
            />
          )}
          <ToolbarSeparator />
          <ToolbarButton
            icon={RefreshCw}
            label="Re-scan"
            spinning={rescanning}
            disabled={!group || rescanning}
            onClick={doRescan}
            title="Re-scan this item on the media server"
          />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <ToolbarButton icon={ArrowLeft} label="Back" onClick={goBack} />
        </PageToolbarSection>
      </PageToolbar>

      <PageBody>{body}</PageBody>

      {group && (
        <>
          <ApproveGroupDialog
            open={approveOpen}
            group={group}
            files={files}
            dryRun={dryRun}
            deletionMethods={settings.data?.deletionMethods}
            libraryNames={libraryNames}
            settings={settings.data ?? null}
            loading={approve.isPending}
            blockedReason={approveBlockedReason}
            error={approveError}
            onConfirm={confirmApprove}
            onCancel={closeApprove}
          />
          <IgnoreDialog
            open={ignoreOpen}
            title={groupTitle(group)}
            loading={ignore.isPending}
            onConfirm={confirmIgnore}
            onCancel={() => setIgnoreOpen(false)}
          />
        </>
      )}
    </PageContent>
  );
}
