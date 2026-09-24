import { useIsFetching, useQueryClient } from '@tanstack/react-query';
import { ListChecks, RefreshCw, ScanSearch, ScrollText } from 'lucide-react';
import { useSearchParams } from 'react-router';
import { queryKeys } from '@/api/queryKeys';
import { ActionsPanel } from '@/components/activity/ActionsPanel';
import { HistoryEventsPanel } from '@/components/activity/HistoryEventsPanel';
import { ScansPanel } from '@/components/activity/ScansPanel';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { TabPanel, Tabs, type TabItem } from '@/components/ui';

type HistoryTab = 'events' | 'actions' | 'scans';

const TABS: readonly TabItem<HistoryTab>[] = [
  { id: 'events', label: 'Events', icon: ScrollText },
  { id: 'actions', label: 'Actions', icon: ListChecks },
  { id: 'scans', label: 'Scans', icon: ScanSearch },
];

const TAB_KEYS = {
  events: queryKeys.history.all,
  actions: queryKeys.actions.all,
  scans: queryKeys.scans.all,
} as const;

function parseTab(value: string | null): HistoryTab {
  return value === 'actions' || value === 'scans' ? value : 'events';
}

/** Positive integer from a query-string value, else undefined. */
function parseId(value: string | null): number | undefined {
  if (!value || !/^\d+$/.test(value)) return undefined;
  const n = Number(value);
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
}

/**
 * Activity → History at `/activity/history`: the audit log (Events), every removal action with
 * restore for recycle-bin removals (Actions), and recent scan runs (Scans). The active tab lives in
 * `?tab=`; `?groupId=` limits Events to one duplicate group.
 */
export default function HistoryPage() {
  const [params, setParams] = useSearchParams();
  const tab = parseTab(params.get('tab'));
  const groupId = parseId(params.get('groupId'));
  const qc = useQueryClient();
  const fetching = useIsFetching({ queryKey: TAB_KEYS[tab] }) > 0;

  const update = (patch: Record<string, string | null>) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        for (const [k, v] of Object.entries(patch)) {
          if (v === null) next.delete(k);
          else next.set(k, v);
        }
        return next;
      },
      { replace: true },
    );
  };

  return (
    <PageContent title="History">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={fetching}
            onClick={() => void qc.invalidateQueries({ queryKey: TAB_KEYS[tab] })}
          />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="History" subtitle="Audit log of scans, approvals and removals." />
        <Tabs
          aria-label="History sections"
          tabs={TABS}
          value={tab}
          onChange={(id) => update({ tab: id === 'events' ? null : id })}
        />
        <TabPanel>
          {tab === 'events' && (
            <HistoryEventsPanel groupId={groupId} onClearGroup={() => update({ groupId: null })} />
          )}
          {tab === 'actions' && <ActionsPanel />}
          {tab === 'scans' && <ScansPanel />}
        </TabPanel>
      </PageBody>
    </PageContent>
  );
}
