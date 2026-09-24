import { ListChecks, RotateCcw } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '@/api/client';
import { useActions, useRestoreAction } from '@/api/hooks';
import type { Action, ActionStatus } from '@/api/types';
import {
  Badge,
  ByteSize,
  ConfirmDialog,
  EmptyState,
  IconButton,
  LoadErrorAlert,
  Pagination,
  RelativeTime,
  Table,
  useToast,
  type TableColumn,
} from '@/components/ui';
import { ACTION_STATUS_LABELS, toOptions } from '@/lib/constants';
import { ActionMethodCell, ActionStatusCell, ActionTitleCell, PermanentBadge } from './ActionCells';
import { canRestoreAction } from './activityMeta';
import { MultiSelectFilter } from './MultiSelectFilter';
import { PathList } from './PathList';
import { useClampPage, usePaging } from './usePaging';

const STATUS_OPTIONS = toOptions(ACTION_STATUS_LABELS);

/**
 * History → Actions tab: every removal action (paged, newest first) with a status filter. Succeeded
 * filesystem removals that went to Dupearr's recycle bin can be restored (after confirmation).
 */
export function ActionsPanel() {
  const paging = usePaging();
  const [statuses, setStatuses] = useState<ActionStatus[]>([]);
  const actions = useActions({
    page: paging.page,
    pageSize: paging.pageSize,
    status: statuses.length > 0 ? statuses : undefined,
  });
  useClampPage(paging, actions.data?.totalRecords);

  const restore = useRestoreAction();
  const toast = useToast();
  const [toRestore, setToRestore] = useState<Action | null>(null);
  /**
   * Actions restored from this page. The action itself keeps status "succeeded" (there is no
   * "restored" status), so this stops a second restore of the same removal from being offered.
   */
  const [restored, setRestored] = useState<ReadonlySet<number>>(() => new Set());

  const records = actions.data?.records ?? [];
  const total = actions.data?.totalRecords ?? 0;

  const confirmRestore = () => {
    const action = toRestore;
    if (!action || restored.has(action.id) || !canRestoreAction(action)) {
      setToRestore(null);
      return;
    }
    restore.mutate(action.id, {
      onSuccess: () => {
        setRestored((prev) => new Set(prev).add(action.id));
        toast.success('File restored', `${action.title || 'The file'} was moved back from the recycle bin.`);
        setToRestore(null);
      },
      onError: (e) => {
        toast.error('Unable to restore file', errorMessage(e));
        setToRestore(null);
      },
    });
  };

  const columns: TableColumn<Action>[] = [
    { key: 'title', header: 'Title', render: (a) => <ActionTitleCell action={a} /> },
    {
      key: 'paths',
      header: 'Path',
      hideBelow: 'xl',
      className: 'max-w-0 w-[30%]',
      render: (a) => <PathList paths={a.paths} maxWidthClass="max-w-full" />,
    },
    {
      key: 'method',
      header: 'Method',
      hideBelow: 'md',
      className: 'whitespace-nowrap',
      render: (a) => <ActionMethodCell action={a} />,
    },
    { key: 'permanent', header: 'Removal', hideBelow: 'lg', render: (a) => <PermanentBadge action={a} /> },
    {
      key: 'status',
      header: 'Status',
      render: (a) => (
        <span className="inline-flex flex-wrap items-center gap-1.5">
          <ActionStatusCell action={a} />
          {/* The Removal column is hidden on small screens: never hide a permanent deletion (D6). */}
          <span className="contents lg:hidden">
            <PermanentBadge action={a} permanentOnly />
          </span>
        </span>
      ),
    },
    {
      key: 'size',
      header: 'Size',
      align: 'right',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (a) => <ByteSize bytes={a.size} />,
    },
    {
      key: 'finishedAt',
      header: 'Finished',
      hideBelow: 'md',
      className: 'whitespace-nowrap',
      render: (a) => <RelativeTime date={a.finishedAt} />,
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '48px',
      className: 'whitespace-nowrap',
      render: (a) =>
        restored.has(a.id) ? (
          <Badge kind="primary" outline title="Moved back from the recycle bin">
            Restored
          </Badge>
        ) : canRestoreAction(a) ? (
          <IconButton
            icon={RotateCcw}
            size="sm"
            label={`Restore ${a.title || 'file'} from the recycle bin`}
            loading={restore.isPending && restore.variables === a.id}
            disabled={restore.isPending && restore.variables !== a.id}
            onClick={() => setToRestore(a)}
          />
        ) : null,
    },
  ];

  const filtered = statuses.length > 0;

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-end gap-2">
        <MultiSelectFilter
          label="Status"
          options={STATUS_OPTIONS}
          value={statuses}
          onChange={(next) => {
            setStatuses(next);
            paging.resetPage();
          }}
        />
      </div>

      {actions.isError && (
        <LoadErrorAlert
          title="Unable to load actions"
          message={errorMessage(actions.error)}
          onRetry={() => void actions.refetch()}
          retrying={actions.isFetching}
          className="mb-4"
        />
      )}

      {!(actions.isError && !actions.data) && (
        <Table
          aria-label="Actions"
          columns={columns}
          rows={records}
          getRowId={(a) => a.id}
          loading={actions.isLoading || (actions.isFetching && actions.isPlaceholderData)}
          emptyState={
            <EmptyState
              icon={ListChecks}
              title={filtered ? 'No matching actions' : 'No actions yet'}
              description={
                filtered
                  ? 'No removal actions match the current filter.'
                  : 'Removals appear here once duplicate groups are approved.'
              }
              compact
            />
          }
        />
      )}

      {total > 0 && (
        <Pagination
          page={paging.page}
          pageSize={paging.pageSize}
          totalRecords={total}
          onPageChange={paging.setPage}
          onPageSizeChange={paging.setPageSize}
          loading={actions.isFetching}
        />
      )}

      <ConfirmDialog
        open={toRestore !== null}
        title="Restore File"
        kind="primary"
        confirmLabel="Restore"
        loading={restore.isPending}
        onCancel={() => setToRestore(null)}
        onConfirm={confirmRestore}
        message={
          <div className="space-y-3">
            <p className="m-0">
              Move <strong className="text-fg-strong">{toRestore?.title || 'this file'}</strong> back from the
              recycle bin to its original location? Plex will be refreshed, and the duplicate may be detected again
              on the next scan.
            </p>
            {toRestore && (
              <dl className="m-0 space-y-1 text-xs">
                <dt className="font-semibold text-muted">From</dt>
                <dd className="m-0 font-mono break-all whitespace-pre-line">{toRestore.recyclePath}</dd>
                {(toRestore.paths ?? []).length > 0 && (
                  <>
                    <dt className="font-semibold text-muted">To</dt>
                    <dd className="m-0 font-mono break-all">{(toRestore.paths ?? []).join(', ')}</dd>
                  </>
                )}
              </dl>
            )}
          </div>
        }
      />
    </div>
  );
}
