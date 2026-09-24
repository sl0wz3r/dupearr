import { RefreshCw } from 'lucide-react';
import { useEffect, useId } from 'react';
import { errorMessage } from '@/api/client';
import { useLogs } from '@/api/hooks';
import type { LogLevel } from '@/api/types';
import { useClampPage, usePaging } from '@/components/activity/usePaging';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { LogEventsTable } from '@/components/system/LogEventsTable';
import { LOG_LEVELS } from '@/components/system/logLevels';
import { useStoredState } from '@/components/system/useStoredState';
import { LoadErrorAlert, Pagination, Select, Switch } from '@/components/ui';
import { LOG_LEVEL_LABELS } from '@/lib/constants';

/** Auto-refresh period of System → Events. */
const EVENTS_REFRESH_MS = 10_000;

const LEVEL_OPTIONS = LOG_LEVELS.map((value) => ({ value, label: LOG_LEVEL_LABELS[value] }));
const isLogLevel = (v: unknown): v is LogLevel => typeof v === 'string' && (LOG_LEVELS as readonly string[]).includes(v);
const isBoolean = (v: unknown): v is boolean => typeof v === 'boolean';

/**
 * System → Events at `/system/events`: recent log entries from the in-memory buffer (newest
 * first), filtered by minimum level, with optional auto-refresh every 10 seconds. The level and
 * auto-refresh choices are remembered in this browser.
 */
export default function EventsPage() {
  const levelId = useId();
  const [level, setLevel] = useStoredState<LogLevel>('dupearr.events.level', 'info', isLogLevel);
  const [autoRefresh, setAutoRefresh] = useStoredState<boolean>('dupearr.events.autoRefresh', false, isBoolean);
  const paging = usePaging(50);
  const logs = useLogs({ page: paging.page, pageSize: paging.pageSize, level });
  useClampPage(paging, logs.data?.totalRecords);

  const { refetch } = logs;
  useEffect(() => {
    if (!autoRefresh) return;
    const timer = setInterval(() => void refetch(), EVENTS_REFRESH_MS);
    return () => clearInterval(timer);
  }, [autoRefresh, refetch]);

  const total = logs.data?.totalRecords ?? 0;

  return (
    <PageContent title="Events">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={logs.isFetching}
            onClick={() => void logs.refetch()}
          />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Events" subtitle="Recent log entries (kept in memory since the last start)." />

        <div className="mb-3 flex flex-wrap items-center justify-end gap-x-6 gap-y-3">
          <div className="flex items-center gap-2">
            <label htmlFor={levelId} className="text-sm whitespace-nowrap text-muted">
              Minimum level
            </label>
            <div className="w-32">
              <Select
                id={levelId}
                options={LEVEL_OPTIONS}
                value={level}
                onChange={(v) => {
                  setLevel(v);
                  paging.resetPage();
                }}
              />
            </div>
          </div>
          <Switch
            checked={autoRefresh}
            onChange={setAutoRefresh}
            label="Auto refresh"
            description={`Every ${EVENTS_REFRESH_MS / 1000} seconds`}
          />
        </div>

        {logs.isError && (
          <LoadErrorAlert
            title="Unable to load events"
            message={errorMessage(logs.error)}
            onRetry={() => void logs.refetch()}
            retrying={logs.isFetching}
            className="mb-4"
          />
        )}

        {!(logs.isError && !logs.data) && (
          <LogEventsTable
            entries={logs.data?.records ?? []}
            loading={logs.isLoading || (logs.isFetching && logs.isPlaceholderData)}
            emptyDescription={`No log entries at ${LOG_LEVEL_LABELS[level]} level or above.`}
          />
        )}

        {total > 0 && (
          <Pagination
            page={paging.page}
            pageSize={paging.pageSize}
            totalRecords={total}
            onPageChange={paging.setPage}
            onPageSizeChange={paging.setPageSize}
            pageSizeOptions={[25, 50, 100, 250]}
            loading={logs.isFetching}
          />
        )}
      </PageBody>
    </PageContent>
  );
}
