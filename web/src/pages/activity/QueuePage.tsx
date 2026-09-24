import { ListChecks, Play, RefreshCw } from 'lucide-react';
import { useEffect, useState } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { useCancelQueueItem, useIsCommandActive, useQueue, useRunCommand } from '@/api/hooks';
import type { Action } from '@/api/types';
import { canCancelQueueItem } from '@/components/activity/activityMeta';
import { QueueTable } from '@/components/activity/QueueTable';
import { useClampPage, usePaging } from '@/components/activity/usePaging';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
  ToolbarSeparator,
} from '@/components/page';
import { ConfirmDialog, EmptyState, LoadErrorAlert, Pagination, useToast } from '@/components/ui';

/**
 * Activity → Queue at `/activity/queue`: approved removal actions that are pending or running
 * (oldest first). Pending actions can be cancelled; "Process Queue" runs the executor now instead
 * of waiting for the scheduled task. Live updates arrive through the `queue` server event.
 */
export default function QueuePage() {
  const paging = usePaging();
  // Oldest first, the order the executor processes them (docs/API.md); explicit so the server's
  // generic action sort (newest first) never applies here.
  const queue = useQueue({
    page: paging.page,
    pageSize: paging.pageSize,
    sortKey: 'createdAt',
    sortDirection: 'ascending',
  });
  useClampPage(paging, queue.data?.totalRecords);

  const cancel = useCancelQueueItem();
  const run = useRunCommand();
  const processing = useIsCommandActive('ProcessQueue');
  const toast = useToast();
  const [toCancel, setToCancel] = useState<Action | null>(null);

  const records = queue.data?.records ?? [];
  const total = queue.data?.totalRecords ?? 0;

  // Live updates can start (or finish) the action while its confirmation is open: close the dialog
  // instead of sending a cancel for something that can no longer be cancelled.
  const live = toCancel ? records.find((a) => a.id === toCancel.id) : undefined;
  const stale = toCancel !== null && !cancel.isPending && live !== undefined && !canCancelQueueItem(live);
  useEffect(() => {
    if (!stale) return;
    setToCancel(null);
    toast.warning('Removal already started', 'Running removals can no longer be cancelled.');
  }, [stale, toast]);

  const processQueue = () => {
    run.mutate(
      { name: 'ProcessQueue' },
      {
        onSuccess: () => toast.info('Processing queue', 'Pending removals are being executed.'),
        onError: (e) => toast.error('Unable to process the queue', errorMessage(e)),
      },
    );
  };

  const confirmCancel = () => {
    const action = toCancel;
    if (!action) return;
    cancel.mutate(action.id, {
      onSuccess: () => {
        toast.success('Removal cancelled', `${action.title || 'The file'} will not be removed.`);
        setToCancel(null);
      },
      onError: (e) => {
        // 409: the executor started (or finished) the removal meanwhile — the server says which.
        if (isApiError(e) && e.isConflict) toast.warning('Removal can no longer be cancelled', errorMessage(e));
        else toast.error('Unable to cancel removal', errorMessage(e));
        setToCancel(null);
      },
    });
  };

  return (
    <PageContent title="Queue">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={Play}
            label="Process Queue"
            title="Execute pending removals now"
            spinning={processing}
            loading={run.isPending}
            disabled={processing}
            onClick={processQueue}
          />
          <ToolbarSeparator />
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={queue.isFetching}
            onClick={() => void queue.refetch()}
          />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Queue" subtitle="Approved removals waiting to be processed or running now." />

        {queue.isError && (
          <LoadErrorAlert
            title="Unable to load the queue"
            message={errorMessage(queue.error)}
            onRetry={() => void queue.refetch()}
            retrying={queue.isFetching}
            className="mb-4"
          />
        )}

        {!(queue.isError && !queue.data) && (
          <QueueTable
            rows={records}
            loading={queue.isLoading || (queue.isFetching && queue.isPlaceholderData)}
            onCancel={setToCancel}
            cancellingId={cancel.isPending ? (cancel.variables ?? null) : null}
            emptyState={
              <EmptyState
                icon={ListChecks}
                title="Queue is empty"
                description="Approved duplicate removals appear here until they are processed."
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
            loading={queue.isFetching}
          />
        )}
      </PageBody>

      <ConfirmDialog
        open={toCancel !== null}
        title="Cancel Removal"
        kind="warning"
        confirmLabel="Cancel Removal"
        cancelLabel="Keep in Queue"
        loading={cancel.isPending}
        onCancel={() => setToCancel(null)}
        onConfirm={confirmCancel}
        message={
          <>
            Cancel the queued removal of <strong className="text-fg-strong">{toCancel?.title || 'this file'}</strong>?
            The file will be kept and nothing will be deleted.
          </>
        }
      />
    </PageContent>
  );
}
