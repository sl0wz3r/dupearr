import { Ban, Plus, RefreshCw, Trash2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { errorMessage } from '@/api/client';
import { useDeleteExclusion, useExclusions, useLibraries } from '@/api/hooks';
import type { Exclusion, Library, SortDirection } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { ExclusionModal } from '@/components/settings/rules/ExclusionModal';
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  RelativeTime,
  Table,
  sortRows,
  useToast,
  type TableColumn,
} from '@/components/ui';
import { EXCLUSION_KIND_LABELS, labelOf } from '@/lib/constants';

/** Value as shown in the table (library ids resolved to titles). */
function displayValue(e: Exclusion, libraries: readonly Library[]): string {
  if (e.kind === 'library') {
    const lib = libraries.find((l) => String(l.id) === e.value);
    return lib ? lib.title : `Library #${e.value}`;
  }
  return e.value;
}

/**
 * Settings → Exclusions at `/settings/exclusions`: content removed from duplicate detection
 * entirely (table, add modal, delete confirmation).
 */
export default function ExclusionsPage() {
  const exclusions = useExclusions();
  const libraries = useLibraries();
  const del = useDeleteExclusion();
  const toast = useToast();

  const [adding, setAdding] = useState(0); // >0 = open; value used as a remount key
  const [toDelete, setToDelete] = useState<Exclusion | null>(null);
  const [sort, setSort] = useState<{ key: string; direction: SortDirection }>({
    key: 'createdAt',
    direction: 'descending',
  });

  const libs = useMemo(() => libraries.data ?? [], [libraries.data]);
  const rows = useMemo(
    () =>
      sortRows(exclusions.data ?? [], sort.key, sort.direction, (row, key) =>
        key === 'kind'
          ? labelOf(EXCLUSION_KIND_LABELS, row.kind)
          : key === 'value'
            ? displayValue(row, libs)
            : (row as unknown as Record<string, unknown>)[key],
      ),
    [exclusions.data, sort, libs],
  );

  const columns: TableColumn<Exclusion>[] = [
    {
      key: 'kind',
      header: 'Kind',
      sortable: true,
      width: '160px',
      render: (e) => <Badge outline>{labelOf(EXCLUSION_KIND_LABELS, e.kind)}</Badge>,
    },
    {
      key: 'value',
      header: 'Value',
      sortable: true,
      render: (e) =>
        e.kind === 'library' ? (
          <span className="text-fg-strong">{displayValue(e, libs)}</span>
        ) : (
          <code className="break-all">{e.value}</code>
        ),
    },
    { key: 'title', header: 'Title', sortable: true, hideBelow: 'md', render: (e) => e.title || <span className="text-muted">-</span> },
    {
      key: 'reason',
      header: 'Reason',
      hideBelow: 'lg',
      render: (e) => (e.reason ? <span className="text-muted">{e.reason}</span> : <span className="text-muted">-</span>),
    },
    {
      key: 'createdAt',
      header: 'Created',
      sortable: true,
      width: '140px',
      hideBelow: 'sm',
      render: (e) => <RelativeTime date={e.createdAt} />,
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '56px',
      render: (e) => (
        <IconButton
          icon={Trash2}
          size="sm"
          variant="danger"
          label={`Delete exclusion ${displayValue(e, libs)}`}
          onClick={() => setToDelete(e)}
        />
      ),
    },
  ];

  const open = () => setAdding((n) => n + 1);

  return (
    <PageContent title="Exclusions">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={Plus} label="Add Exclusion" onClick={open} />
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={exclusions.isFetching}
            onClick={() => void exclusions.refetch()}
          />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader
          title="Exclusions"
          subtitle="Content that is never considered a duplicate."
          actions={
            <Button icon={Plus} onClick={open}>
              Add Exclusion
            </Button>
          }
        />
        {exclusions.error ? (
          <Alert
            kind="error"
            title="Could not load exclusions"
            actions={
              <Button size="sm" onClick={() => void exclusions.refetch()}>
                Retry
              </Button>
            }
          >
            {errorMessage(exclusions.error)}
          </Alert>
        ) : (
          <Table
            aria-label="Exclusions"
            columns={columns}
            rows={rows}
            getRowId={(e) => e.id}
            loading={exclusions.isPending}
            sortKey={sort.key}
            sortDirection={sort.direction}
            onSortChange={(key, direction) => setSort({ key, direction })}
            emptyState={
              <EmptyState
                icon={Ban}
                title="No exclusions"
                description={
                  <>
                    Exclusions remove content from duplicate detection entirely — excluded files are never grouped, shown
                    or removed. To leave a single duplicate group alone but keep it visible, use <strong>Ignore</strong>{' '}
                    on the Duplicates page instead (ignoring can also add an exclusion for you).
                  </>
                }
                action={
                  <Button icon={Plus} onClick={open}>
                    Add Exclusion
                  </Button>
                }
              />
            }
          />
        )}
      </PageBody>

      {adding > 0 && <ExclusionModal key={adding} open libraries={libs} onClose={() => setAdding(0)} />}

      <ConfirmDialog
        open={!!toDelete}
        title="Delete Exclusion"
        message={
          toDelete ? (
            <>
              Delete the exclusion <strong>{displayValue(toDelete, libs)}</strong>? Matching content will be considered
              again on the next scan.
            </>
          ) : null
        }
        confirmLabel="Delete"
        loading={del.isPending}
        onCancel={() => setToDelete(null)}
        onConfirm={() => {
          if (!toDelete) return;
          del.mutate(toDelete.id, {
            onSuccess: () => {
              toast.success('Exclusion deleted');
              setToDelete(null);
            },
            onError: (e) => {
              toast.error('Could not delete the exclusion', errorMessage(e));
              setToDelete(null);
            },
          });
        }}
      />
    </PageContent>
  );
}
