import { useIsFetching, useQueryClient } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { queryKeys } from '@/api/queryKeys';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { AboutCard } from '@/components/system/AboutCard';
import { HealthCard } from '@/components/system/HealthCard';
import { MoreInfoCard } from '@/components/system/MoreInfoCard';

/**
 * System → Status at `/system/status`: health issues (with wiki links and "Check Now"), details
 * about this install (version, runtime, paths, uptime, auth, mode/dry run) and project links.
 */
export default function StatusPage() {
  const qc = useQueryClient();
  const fetching =
    useIsFetching({ queryKey: queryKeys.system.status }) + useIsFetching({ queryKey: queryKeys.health }) > 0;

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.system.status });
    void qc.invalidateQueries({ queryKey: queryKeys.health });
  };

  return (
    <PageContent title="Status">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={RefreshCw} label="Refresh" spinning={fetching} onClick={refresh} />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Status" />
        <div className="flex flex-col gap-5">
          <HealthCard />
          <AboutCard />
          <MoreInfoCard />
        </div>
      </PageBody>
    </PageContent>
  );
}
