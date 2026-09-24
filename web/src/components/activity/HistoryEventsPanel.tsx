import { ScrollText, X } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { errorMessage } from '@/api/client';
import { useHistory } from '@/api/hooks';
import type { HistoryEvent, HistoryEventType, Id } from '@/api/types';
import { Badge, ByteSize, EmptyState, LoadErrorAlert, Pagination, RelativeTime, type TableColumn } from '@/components/ui';
import { DELETION_METHOD_LABELS, HISTORY_EVENT_KIND, HISTORY_EVENT_LABELS, labelOf, toOptions } from '@/lib/constants';
import {
  HISTORY_EVENT_ICONS,
  UNKNOWN_EVENT_ICON,
  normalizeHistoryData,
  prettyJson,
  summarizeHistoryData,
} from './activityMeta';
import { ExpandableTable } from './ExpandableTable';
import { MultiSelectFilter } from './MultiSelectFilter';
import { useClampPage, usePaging } from './usePaging';

const EVENT_TYPE_OPTIONS = toOptions(HISTORY_EVENT_LABELS);

/** Badge with the event type's icon, label and colour. */
export function HistoryEventTypeBadge({ type }: { type: HistoryEventType | string }) {
  const Icon = HISTORY_EVENT_ICONS[type as HistoryEventType] ?? UNKNOWN_EVENT_ICON;
  return (
    <Badge kind={HISTORY_EVENT_KIND[type as HistoryEventType] ?? 'default'} icon={Icon}>
      {labelOf(HISTORY_EVENT_LABELS, type)}
    </Badge>
  );
}

/** Expanded details of a history event: key facts (paths, method, permanence) + the raw data JSON. */
export function HistoryEventDetails({ event }: { event: HistoryEvent }) {
  const data = normalizeHistoryData(event.data);
  const s = summarizeHistoryData(event.data);
  const facts: { label: string; value: ReactNode }[] = [];
  if (s.paths.length > 0) {
    facts.push({
      label: s.paths.length === 1 ? 'Path' : 'Paths',
      value: (
        <ul className="m-0 list-none space-y-0.5 p-0 font-mono text-xs break-all">
          {s.paths.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ),
    });
  }
  if (s.method) facts.push({ label: 'Method', value: labelOf(DELETION_METHOD_LABELS, s.method) });
  if (s.permanent !== undefined) {
    // Dry runs remove nothing: label what *would* have happened.
    const hypothetical = s.dryRun === true || event.eventType === 'fileDeleteDryRun';
    facts.push({
      label: hypothetical ? 'Removal (dry run)' : 'Removal',
      value: s.permanent ? (
        <Badge kind="danger" title={hypothetical ? 'Would be deleted permanently; could not be undone' : undefined}>
          {hypothetical ? 'Would be permanent' : 'Permanent'}
        </Badge>
      ) : (
        <Badge kind="success" title={hypothetical ? 'Would go to a recycle bin' : undefined}>
          {hypothetical ? 'Would go to recycle bin' : 'Recycle bin'}
        </Badge>
      ),
    });
  }
  if (s.recyclePath) {
    // A whole-disc removal records one moved entry per line (BDMV/, CERTIFICATE/ …).
    facts.push({
      label: 'Recycle path',
      value: <span className="font-mono text-xs break-all whitespace-pre-line">{s.recyclePath}</span>,
    });
  }
  if (s.dryRun !== undefined) facts.push({ label: 'Dry run', value: s.dryRun ? 'Yes' : 'No' });
  if (s.size !== undefined) facts.push({ label: 'Size', value: <ByteSize bytes={s.size} /> });

  return (
    <div className="space-y-3">
      {facts.length > 0 && (
        <dl className="m-0 grid grid-cols-1 gap-x-4 gap-y-1.5 sm:grid-cols-[8rem_1fr]">
          {facts.map((f) => (
            <div key={f.label} className="contents">
              <dt className="text-xs font-semibold text-muted sm:pt-0.5">{f.label}</dt>
              <dd className="m-0 min-w-0 text-fg">{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {data !== null && (
        <pre
          aria-label="Event data"
          className="m-0 max-h-80 overflow-auto rounded border border-border bg-page p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all text-fg"
        >
          {prettyJson(data)}
        </pre>
      )}
    </div>
  );
}

export interface HistoryEventsPanelProps {
  /** Only events of this duplicate group (from `?groupId=`). */
  groupId?: Id;
  /** Removes the group filter. */
  onClearGroup?: () => void;
}

/** History → Events tab: paged audit log with an event-type filter and expandable event data. */
export function HistoryEventsPanel({ groupId, onClearGroup }: HistoryEventsPanelProps) {
  const paging = usePaging();
  const [eventTypes, setEventTypes] = useState<HistoryEventType[]>([]);
  const history = useHistory({
    page: paging.page,
    pageSize: paging.pageSize,
    eventType: eventTypes.length > 0 ? eventTypes : undefined,
    groupId,
  });
  useClampPage(paging, history.data?.totalRecords);

  // A different (or cleared) group filter starts again on the first page.
  const { resetPage } = paging;
  useEffect(() => {
    resetPage();
  }, [groupId, resetPage]);

  const records = history.data?.records ?? [];
  const total = history.data?.totalRecords ?? 0;
  const filtered = eventTypes.length > 0 || groupId !== undefined;

  const columns: TableColumn<HistoryEvent>[] = [
    {
      key: 'eventType',
      header: 'Event',
      render: (e) => <HistoryEventTypeBadge type={e.eventType} />,
    },
    {
      key: 'title',
      header: 'Title',
      // On phones the title cell also carries the message/date: let it wrap instead of scrolling.
      className: 'max-sm:w-full max-sm:max-w-0',
      render: (e) => (
        <div className="min-w-0">
          {e.groupId ? (
            <Link to={`/duplicate/${e.groupId}`} className="font-medium text-fg-strong hover:text-accent-soft">
              {e.title || `Group #${e.groupId}`}
            </Link>
          ) : (
            <span className="font-medium text-fg-strong">{e.title || '-'}</span>
          )}
          {e.message && <div className="mt-0.5 text-xs break-words text-muted md:hidden">{e.message}</div>}
          <RelativeTime date={e.createdAt} className="mt-0.5 block text-xs text-subtle sm:hidden" />
        </div>
      ),
    },
    {
      key: 'message',
      header: 'Message',
      hideBelow: 'md',
      render: (e) => <span className="break-words text-fg">{e.message}</span>,
    },
    {
      key: 'createdAt',
      header: 'Date',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (e) => <RelativeTime date={e.createdAt} />,
    },
  ];

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-end gap-2">
        {groupId !== undefined && (
          <span className="mr-auto inline-flex items-center gap-1.5 rounded border border-border-strong bg-card px-2 py-1 text-xs text-fg">
            Duplicate group #{groupId}
            {onClearGroup && (
              <button
                type="button"
                onClick={onClearGroup}
                aria-label="Clear group filter"
                title="Clear group filter"
                className="rounded text-muted hover:text-fg-strong"
              >
                <X aria-hidden width={13} height={13} />
              </button>
            )}
          </span>
        )}
        <MultiSelectFilter
          label="Event type"
          options={EVENT_TYPE_OPTIONS}
          value={eventTypes}
          onChange={(next) => {
            setEventTypes(next);
            paging.resetPage();
          }}
        />
      </div>

      {history.isError && (
        <LoadErrorAlert
          title="Unable to load history"
          message={errorMessage(history.error)}
          onRetry={() => void history.refetch()}
          retrying={history.isFetching}
          className="mb-4"
        />
      )}

      {!(history.isError && !history.data) && (
        <ExpandableTable
          aria-label="History events"
          columns={columns}
          rows={records}
          getRowId={(e) => e.id}
          isExpandable={(e) => normalizeHistoryData(e.data) !== null}
          detailsLabel={(e) => `details of ${labelOf(HISTORY_EVENT_LABELS, e.eventType)} event`}
          renderExpanded={(e) => <HistoryEventDetails event={e} />}
          loading={history.isLoading || (history.isFetching && history.isPlaceholderData)}
          emptyState={
            <EmptyState
              icon={ScrollText}
              title={filtered ? 'No matching events' : 'No history yet'}
              description={
                filtered
                  ? 'No events match the current filter.'
                  : 'Scans, approvals and removals are recorded here.'
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
          loading={history.isFetching}
        />
      )}
    </div>
  );
}
