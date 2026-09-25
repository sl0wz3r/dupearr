import { useQueryClient } from '@tanstack/react-query';
import {
  Check,
  CirclePlay,
  CopyCheck,
  Eye,
  EyeOff,
  Funnel,
  FunnelX,
  LayoutGrid,
  RefreshCw,
  RotateCw,
  ServerOff,
  SquareCheck,
  Table2,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { errorMessage, isApiError } from '@/api/client';
import { useServerEvents } from '@/api/events';
import {
  useApproveDuplicate,
  useBulkDuplicateAction,
  useDuplicateStats,
  useDuplicates,
  useIgnoreDuplicate,
  useUnignoreDuplicate,
} from '@/api/hooks/useDuplicates';
import { useCommand, useIsCommandActive, useRunCommand } from '@/api/hooks/useCommands';
import { useLibraries, useMediaServers } from '@/api/hooks/useMediaServers';
import { useSettings } from '@/api/hooks/useSettings';
import { queryKeys } from '@/api/queryKeys';
import type {
  BulkDuplicateAction,
  BulkDuplicateResponse,
  CommandName,
  CommandStatus,
  DuplicateGroupSummary,
  GroupStatus,
  Id,
  SortDirection,
} from '@/api/types';
import { useIsMobile } from '@/components/layout/useMediaQuery';
import {
  BulkActionDialog,
  DuplicateCards,
  DuplicatesTable,
  FilterBar,
  IgnoreDialog,
  StatsStrip,
  ToolbarMenu,
  groupTitle,
  hasActiveFilters,
  loadPageSize,
  loadView,
  parseListState,
  sameStatuses,
  savePageSize,
  saveView,
  serializeListState,
  toQueryParams,
  type DuplicateFilters,
  type DuplicateListState,
  type DuplicatesView,
  type ToolbarMenuItem,
} from '@/components/duplicates';
import {
  PageBody,
  PageContent,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
  ToolbarSeparator,
} from '@/components/page';
import {
  Alert,
  Button,
  EmptyState,
  LinkButton,
  LoadingIndicator,
  Pagination,
  Spinner,
  useToast,
  type RowId,
} from '@/components/ui';
import { GROUP_STATUS_LABELS, GROUP_STATUSES, OPEN_GROUP_STATUSES } from '@/lib/constants';
import { formatNumber } from '@/lib/format';

// ---------------------------------------------------------------------------
// Filter presets (toolbar Filter menu)
// ---------------------------------------------------------------------------

type PresetId = 'open' | 'all' | GroupStatus;

const PRESETS: { id: PresetId; label: string; description?: string; statuses: GroupStatus[] }[] = [
  {
    id: 'open',
    label: 'Open',
    description: 'Pending, review, deferred, queued and failed (default)',
    statuses: [...OPEN_GROUP_STATUSES],
  },
  { id: 'all', label: 'All', description: 'Every status', statuses: [] },
  ...GROUP_STATUSES.map((s) => ({ id: s as PresetId, label: GROUP_STATUS_LABELS[s], statuses: [s] })),
];

const PRESET_ITEMS: ToolbarMenuItem<PresetId>[] = PRESETS.map(({ id, label, description }) => ({
  value: id,
  label,
  description,
}));

function currentPreset(status: readonly GroupStatus[]): PresetId | null {
  if (status.length === 0) return 'all';
  return PRESETS.find((p) => p.statuses.length > 0 && sameStatuses(p.statuses, status))?.id ?? null;
}

const plural = (n: number, one: string, many = `${one}s`) => `${formatNumber(n)} ${n === 1 ? one : many}`;

/** Queued or running. */
const isActive = (status: CommandStatus | undefined) => status === 'queued' || status === 'started';

const BULK_VERBS: Record<BulkDuplicateAction, string> = {
  approve: 'Approved',
  ignore: 'Ignored',
  unignore: 'Unignored',
};

const EMPTY_SELECTION: ReadonlyMap<Id, DuplicateGroupSummary> = new Map();

interface Selection {
  /** Query the selection belongs to: it is dropped whenever filters/sort/page change. */
  key: string;
  groups: ReadonlyMap<Id, DuplicateGroupSummary>;
}

/**
 * Duplicates — main list at `/` (docs/ARCHITECTURE.md §9).
 *
 * Paged table / poster grid of duplicate groups with server-side sorting and filters kept in the
 * URL (`?search=` comes from the header search), stats, "Scan Now" with live progress, and
 * approve / ignore / unignore for single groups or a selection.
 */
export default function DuplicatesPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const state = useMemo(() => parseListState(searchParams), [searchParams]);
  const [pageSize, setPageSize] = useState(loadPageSize);
  const [view, setView] = useState<DuplicatesView>(loadView);
  const isMobile = useIsMobile();
  const toast = useToast();
  const qc = useQueryClient();

  const queryParams = useMemo(() => toQueryParams(state, pageSize), [state, pageSize]);
  const duplicates = useDuplicates(queryParams);
  const stats = useDuplicateStats();
  const libraries = useLibraries();
  const servers = useMediaServers();
  const settings = useSettings();
  const { scanProgress } = useServerEvents();

  const scanCommand = useRunCommand();
  const processCommand = useRunCommand();
  // Busy state comes from the commands themselves (never from the last progress message, which
  // may be stale if the SSE stream missed the end of a scan). The command started here is also
  // followed directly (polled while active) so the button can't flicker back before the
  // commands list refreshes.
  const startedScan = useCommand(scanCommand.data?.id);
  const startedProcess = useCommand(processCommand.data?.id);
  const scanActive = useIsCommandActive('DuplicateScan');
  const targetedScanActive = useIsCommandActive('TargetedScan');
  const processActive = useIsCommandActive('ProcessQueue');
  const scanning = scanActive || scanCommand.isPending || isActive(startedScan.data?.status);
  const processing = processActive || processCommand.isPending || isActive(startedProcess.data?.status);
  // Progress line: full scans, and targeted scans (webhooks / re-scan) that report progress.
  const showScanProgress = scanning || (targetedScanActive && scanProgress !== null);

  const bulk = useBulkDuplicateAction();
  const approve = useApproveDuplicate();
  const ignore = useIgnoreDuplicate();
  const unignore = useUnignoreDuplicate();

  // Unknown settings ⇒ assume a real run (stricter confirmations), never the other way round.
  const dryRun = settings.data?.dryRun === true;
  const deletionMethods = settings.data?.deletionMethods;

  // --- URL state -------------------------------------------------------------

  const update = useCallback(
    (patch: Partial<DuplicateListState>) => {
      const next: DuplicateListState = { ...state, page: 1, ...patch };
      setSearchParams(serializeListState(next), { replace: true });
    },
    [state, setSearchParams],
  );

  const updateFilters = (patch: Partial<DuplicateFilters>) => update(patch);
  const resetFilters = () => setSearchParams(new URLSearchParams(), { replace: true });

  // Past the last page (e.g. after ignoring the last rows of it): jump back to the last page.
  const data = duplicates.data;
  useEffect(() => {
    if (!data || duplicates.isPlaceholderData) return;
    if (data.records.length === 0 && state.page > 1 && data.totalRecords > 0) {
      const last = Math.max(1, Math.ceil(data.totalRecords / Math.max(1, pageSize)));
      if (last < state.page) update({ page: last });
    }
  }, [data, duplicates.isPlaceholderData, state.page, pageSize, update]);

  // --- Selection ---------------------------------------------------------------

  const records = useMemo(() => data?.records ?? [], [data]);
  const rowsById = useMemo(() => new Map(records.map((r) => [r.id, r])), [records]);
  const selectionKey = JSON.stringify(queryParams);
  const [selectMode, setSelectMode] = useState(false);
  const [selection, setSelection] = useState<Selection>({ key: selectionKey, groups: EMPTY_SELECTION });
  const selectedMap = selectMode && selection.key === selectionKey ? selection.groups : EMPTY_SELECTION;
  // SAFETY: a selection only ever refers to rows as currently listed (SSE keeps the page fresh).
  // A selected group that left the page (e.g. it turned `review` or `deferred` after a scan) is
  // dropped rather than approved on the strength of its old snapshot.
  const selectedGroups = useMemo(
    () =>
      [...selectedMap.keys()]
        .map((id) => rowsById.get(id))
        .filter((g): g is DuplicateGroupSummary => g !== undefined),
    [selectedMap, rowsById],
  );
  const selectedIds = useMemo(() => new Set<RowId>(selectedGroups.map((g) => g.id)), [selectedGroups]);

  const setSelectedIds = (ids: Iterable<RowId>) => {
    const next = new Map<Id, DuplicateGroupSummary>();
    for (const raw of ids) {
      const id = Number(raw);
      const g = rowsById.get(id);
      if (g) next.set(id, g);
    }
    setSelection({ key: selectionKey, groups: next });
  };

  const toggleOne = (id: Id, on: boolean) => {
    const ids = new Set<RowId>(selectedIds);
    if (on) ids.add(id);
    else ids.delete(id);
    setSelectedIds(ids);
  };

  const clearSelection = () => setSelection({ key: selectionKey, groups: EMPTY_SELECTION });

  const toggleSelectMode = () => {
    setSelectMode((on) => !on);
    clearSelection();
  };

  // --- Commands ------------------------------------------------------------------

  const runCommand = (name: Extract<CommandName, 'DuplicateScan' | 'ProcessQueue'>) => {
    const mutation = name === 'DuplicateScan' ? scanCommand : processCommand;
    mutation.mutate(
      { name },
      {
        onSuccess: () => {
          if (name === 'ProcessQueue') {
            toast.info('Processing queue', dryRun ? 'Dry run: removals are recorded, nothing is deleted.' : undefined);
          }
        },
        onError: (e) =>
          toast.error(name === 'DuplicateScan' ? 'Could not start the scan' : 'Could not process the queue', errorMessage(e)),
      },
    );
  };

  const refreshing = (duplicates.isFetching || stats.isFetching) && !duplicates.isPending;
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.duplicates.all });
  };

  // --- Actions -------------------------------------------------------------------

  const [bulkAction, setBulkAction] = useState<BulkDuplicateAction | null>(null);
  const [approveTarget, setApproveTarget] = useState<DuplicateGroupSummary | null>(null);
  /** Server message of a refused (409) row approval, shown in the still-open dialog. */
  const [approveError, setApproveError] = useState<string | null>(null);
  const [ignoreTarget, setIgnoreTarget] = useState<DuplicateGroupSummary | null>(null);
  const [busyId, setBusyId] = useState<Id | null>(null);

  const reportBulk = (action: BulkDuplicateAction, result: BulkDuplicateResponse | undefined, sent: Id[]) => {
    const succeeded = Array.isArray(result?.succeeded) ? result.succeeded : [];
    const failed = Array.isArray(result?.failed) ? result.failed : [];
    const dryRunNote = action === 'approve' && dryRun ? 'Dry run: removals are recorded, nothing is deleted.' : undefined;
    if (failed.length === 0) {
      toast.success(`${BULK_VERBS[action]} ${plural(succeeded.length || sent.length, 'group')}`, dryRunNote);
      return;
    }
    const titleOf = (id: Id) => {
      const g = rowsById.get(id) ?? selectedMap.get(id);
      return g ? groupTitle(g) : `#${id}`;
    };
    const shown = failed.slice(0, 5);
    toast.show({
      kind: succeeded.length > 0 ? 'warning' : 'error',
      title: `${BULK_VERBS[action]} ${formatNumber(succeeded.length)} of ${plural(succeeded.length + failed.length, 'group')}`,
      message: (
        <ul className="m-0 list-disc pl-4">
          {shown.map((f) => (
            <li key={f.id}>
              <span className="font-semibold">{titleOf(f.id)}</span>: {f.message || 'failed'}
            </li>
          ))}
          {failed.length > shown.length && <li>…and {formatNumber(failed.length - shown.length)} more</li>}
        </ul>
      ),
      duration: 12_000,
    });
  };

  /** `signatures`: the rows as reviewed in the dialog (approve only; see BulkActionDialog). */
  const confirmBulk = (ids: Id[], signatures: Record<Id, string>) => {
    const action = bulkAction;
    if (!action || ids.length === 0) return;
    bulk.mutate(
      { ids, action, ...(action === 'approve' ? { signatures } : {}) },
      {
        onSuccess: (result) => {
          reportBulk(action, result, ids);
          setBulkAction(null);
          clearSelection();
        },
        onError: (e) => toast.error(`Bulk ${action} failed`, errorMessage(e)),
      },
    );
  };

  const openApprove = (g: DuplicateGroupSummary) => {
    setApproveError(null);
    setApproveTarget(g);
  };

  const closeApprove = () => {
    setApproveTarget(null);
    setApproveError(null);
  };

  const confirmApprove = (ids: Id[], signatures: Record<Id, string>) => {
    const target = approveTarget;
    const id = ids[0];
    if (!target || id === undefined) return;
    setBusyId(id);
    setApproveError(null);
    approve.mutate(
      { id, signature: signatures[id] },
      {
        onSuccess: (actions) => {
          const n = Array.isArray(actions) ? actions.length : target.removeCount;
          toast.success(
            `Approved “${groupTitle(target)}”`,
            `${plural(n, 'removal')} queued${dryRun ? ' (dry run: nothing is deleted)' : ''}.`,
          );
          closeApprove();
        },
        onError: (e) => {
          // 409: the group changed since it was shown (or was queued/resolved meanwhile). The list
          // is refetched; the dialog stays open and its change guard shows what changed.
          if (isApiError(e) && e.isConflict) setApproveError(errorMessage(e));
          else toast.error('Could not approve', errorMessage(e));
        },
        onSettled: () => setBusyId(null),
      },
    );
  };

  const confirmIgnore = (addExclusion: boolean) => {
    const target = ignoreTarget;
    if (!target) return;
    setBusyId(target.id);
    ignore.mutate(
      { id: target.id, addExclusion },
      {
        onSuccess: () => {
          toast.success(`Ignored “${groupTitle(target)}”`, addExclusion ? 'An exclusion was added.' : undefined);
          setIgnoreTarget(null);
        },
        onError: (e) => toast.error('Could not ignore', errorMessage(e)),
        onSettled: () => setBusyId(null),
      },
    );
  };

  const doUnignore = (g: DuplicateGroupSummary) => {
    setBusyId(g.id);
    unignore.mutate(g.id, {
      onSuccess: () => toast.success(`Unignored “${groupTitle(g)}”`),
      onError: (e) => toast.error('Could not unignore', errorMessage(e)),
      onSettled: () => setBusyId(null),
    });
  };

  const rowActions = {
    onApprove: openApprove,
    onIgnore: setIgnoreTarget,
    onUnignore: doUnignore,
    busyId,
  };

  // --- View ----------------------------------------------------------------------

  const toggleView = () => {
    const next: DuplicatesView = view === 'table' ? 'posters' : 'table';
    setView(next);
    saveView(next);
  };

  const libraryNames = useMemo(
    () => new Map((libraries.data ?? []).map((l) => [l.id, l.title] as const)),
    [libraries.data],
  );

  const filtersActive = hasActiveFilters(state);
  const totalGroups = stats.data?.total;
  const noServers = Array.isArray(servers.data) && servers.data.length === 0;

  let emptyState: ReactNode;
  if (totalGroups === 0 || (totalGroups === undefined && !filtersActive)) {
    emptyState = noServers ? (
      <EmptyState
        icon={ServerOff}
        title="No media servers configured"
        description="Add your Plex server so Dupearr can scan its libraries for duplicate movies and episodes."
        action={
          <LinkButton to="/settings/mediaservers" variant="primary">
            Add Media Server
          </LinkButton>
        }
      />
    ) : (
      <EmptyState
        icon={CopyCheck}
        title="No duplicates found"
        description="Your libraries look clean. Run a scan to check again."
        action={
          <Button variant="primary" icon={RefreshCw} loading={scanning} onClick={() => runCommand('DuplicateScan')}>
            Scan Now
          </Button>
        }
      />
    );
  } else {
    emptyState = (
      <EmptyState
        icon={FunnelX}
        title={filtersActive ? 'No duplicates match the current filters' : 'No open duplicates'}
        description={
          filtersActive
            ? 'Try another status, library or flag, or clear the search.'
            : `All ${plural(totalGroups ?? 0, 'duplicate group')} are resolved, protected or ignored.`
        }
        action={
          filtersActive ? (
            <Button onClick={resetFilters}>Reset filters</Button>
          ) : (
            <Button onClick={() => update({ status: [] })}>Show all statuses</Button>
          )
        }
      />
    );
  }

  const layout: 'table' | 'posters' | 'list' = view === 'posters' ? 'posters' : isMobile ? 'list' : 'table';
  const onlyStatus = state.status.length === 1 ? state.status[0]! : null;
  const selectedCount = selectedGroups.length;

  return (
    <PageContent title="Duplicates">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={RefreshCw}
            label={scanning ? 'Scanning' : 'Scan Now'}
            spinning={scanning}
            disabled={scanning}
            onClick={() => runCommand('DuplicateScan')}
            title={(scanning && scanProgress?.message) || 'Scan every enabled library for duplicates'}
          />
          <ToolbarButton
            icon={CirclePlay}
            label="Process Queue"
            spinning={processing}
            disabled={processing}
            onClick={() => runCommand('ProcessQueue')}
            title="Run the approved removals now"
          />
          <ToolbarButton icon={RotateCw} label="Refresh" spinning={refreshing} onClick={refresh} />
          <ToolbarSeparator />
          <ToolbarButton
            icon={SquareCheck}
            label={selectMode ? 'Cancel Select' : 'Select'}
            active={selectMode}
            onClick={toggleSelectMode}
          />
          {selectMode && selectedCount > 0 && (
            <>
              <ToolbarButton
                icon={Check}
                kind="success"
                label={`Approve (${formatNumber(selectedCount)})`}
                onClick={() => setBulkAction('approve')}
              />
              <ToolbarButton icon={EyeOff} label="Ignore" onClick={() => setBulkAction('ignore')} />
              <ToolbarButton icon={Eye} label="Unignore" onClick={() => setBulkAction('unignore')} />
            </>
          )}
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <ToolbarButton
            icon={view === 'table' ? LayoutGrid : Table2}
            label={view === 'table' ? 'Posters' : 'Table'}
            title={view === 'table' ? 'Show posters' : 'Show table'}
            onClick={toggleView}
          />
          <ToolbarMenu
            icon={Funnel}
            label="Filter"
            menuLabel="Status filter"
            items={PRESET_ITEMS}
            value={currentPreset(state.status)}
            active={filtersActive}
            onSelect={(id) => {
              const preset = PRESETS.find((p) => p.id === id);
              if (preset) update({ status: [...preset.statuses] });
            }}
          />
        </PageToolbarSection>
      </PageToolbar>

      <PageBody>
        <h1 className="sr-only">Duplicates</h1>
        <div className="flex flex-col gap-4">
          <StatsStrip
            stats={stats.data}
            loading={stats.isPending}
            activeStatus={onlyStatus}
            onStatusClick={(s) => update({ status: [s] })}
            onTotalClick={() => update({ status: [] })}
          />

          {showScanProgress && (
            <div role="status" className="flex min-w-0 items-center gap-2 text-sm text-muted">
              <Spinner size="sm" label="Scanning" />
              <span className="truncate">{scanProgress?.message || 'Scanning libraries for duplicates…'}</span>
            </div>
          )}

          <FilterBar
            filters={state}
            onChange={updateFilters}
            onReset={resetFilters}
            stats={stats.data}
            libraries={libraries.data}
            serverCount={servers.data?.length ?? 1}
            collapsible={isMobile}
          />

          {selectMode && (
            <div className="-mb-2 text-xs text-muted" aria-live="polite">
              {selectedCount > 0
                ? `${plural(selectedCount, 'group')} selected on this page`
                : 'Select groups to approve, ignore or unignore them together. Selection is cleared when the page or filters change.'}
            </div>
          )}

          {duplicates.isPending ? (
            <LoadingIndicator message="Loading duplicates…" />
          ) : duplicates.isError && !data ? (
            <Alert
              kind="error"
              title="Could not load duplicates"
              actions={
                <Button size="sm" onClick={() => void duplicates.refetch()}>
                  Retry
                </Button>
              }
            >
              {errorMessage(duplicates.error)}
            </Alert>
          ) : (
            <>
              {duplicates.isError && (
                <Alert kind="warning" title="Showing cached results">
                  {errorMessage(duplicates.error)}
                </Alert>
              )}
              {layout === 'table' ? (
                <div className="rounded border border-border bg-card">
                  <DuplicatesTable
                    rows={records}
                    loading={duplicates.isPlaceholderData}
                    sortKey={state.sortKey}
                    sortDirection={state.sortDirection}
                    onSortChange={(key, direction: SortDirection) =>
                      update({ sortKey: key as DuplicateListState['sortKey'], sortDirection: direction })
                    }
                    selectable={selectMode}
                    selectedIds={selectedIds}
                    onSelectionChange={setSelectedIds}
                    libraryNames={libraryNames}
                    emptyState={emptyState}
                    {...rowActions}
                  />
                </div>
              ) : (
                <DuplicateCards
                  rows={records}
                  layout={layout}
                  loading={duplicates.isPlaceholderData}
                  selectable={selectMode}
                  selectedIds={selectedIds as ReadonlySet<Id>}
                  onToggleSelect={toggleOne}
                  libraryNames={libraryNames}
                  emptyState={<div className="rounded border border-border bg-card">{emptyState}</div>}
                  {...rowActions}
                />
              )}
              {data && data.totalRecords > 0 && (
                <Pagination
                  page={state.page}
                  pageSize={pageSize}
                  totalRecords={data.totalRecords}
                  loading={duplicates.isPlaceholderData}
                  onPageChange={(page) => update({ page })}
                  onPageSizeChange={(size) => {
                    setPageSize(size);
                    savePageSize(size);
                    update({ page: 1 });
                  }}
                />
              )}
            </>
          )}
        </div>
      </PageBody>

      <BulkActionDialog
        open={bulkAction !== null}
        action={bulkAction ?? 'approve'}
        groups={selectedGroups}
        dryRun={dryRun}
        deletionMethods={deletionMethods}
        loading={bulk.isPending}
        onConfirm={confirmBulk}
        onCancel={() => setBulkAction(null)}
      />
      <BulkActionDialog
        open={approveTarget !== null}
        action="approve"
        single
        // Only the row as currently listed: if it changed out of the page, the dialog says so.
        groups={approveTarget && rowsById.has(approveTarget.id) ? [rowsById.get(approveTarget.id)!] : []}
        dryRun={dryRun}
        deletionMethods={deletionMethods}
        loading={approve.isPending}
        error={approveError}
        onConfirm={confirmApprove}
        onCancel={closeApprove}
      />
      <IgnoreDialog
        open={ignoreTarget !== null}
        title={ignoreTarget ? groupTitle(ignoreTarget) : ''}
        loading={ignore.isPending}
        onConfirm={confirmIgnore}
        onCancel={() => setIgnoreTarget(null)}
      />
    </PageContent>
  );
}
