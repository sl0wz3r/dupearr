import { useQueryClient } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '@/api/client';
import { useLogFiles } from '@/api/hooks';
import { queryKeys } from '@/api/queryKeys';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { LogFilesTable } from '@/components/system/LogFilesTable';
import { LogViewerModal } from '@/components/system/LogViewerModal';
import { LoadErrorAlert } from '@/components/ui';

/**
 * System → Logs at `/system/logs`: log files in the data directory (newest first) with an in-app
 * viewer (level colours, search, refresh) and download.
 */
export default function LogsPage() {
  const files = useLogFiles();
  const qc = useQueryClient();
  const [viewing, setViewing] = useState<string | null>(null);

  /** Opens the viewer; a previously viewed file is marked stale so its newest content is fetched. */
  const view = (filename: string) => {
    void qc.invalidateQueries({ queryKey: queryKeys.system.logFile(filename), refetchType: 'none' });
    setViewing(filename);
  };

  return (
    <PageContent title="Log Files">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={files.isFetching}
            onClick={() => void files.refetch()}
          />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Log Files" subtitle="Rotated log files written to the logs folder of the data directory." />
        {files.isError && (
          <LoadErrorAlert
            title="Unable to load log files"
            message={errorMessage(files.error)}
            onRetry={() => void files.refetch()}
            retrying={files.isFetching}
            className="mb-4"
          />
        )}
        {!(files.isError && !files.data) && (
          <LogFilesTable files={files.data ?? []} loading={files.isLoading} onView={(f) => view(f.filename)} />
        )}
      </PageBody>
      <LogViewerModal filename={viewing} onClose={() => setViewing(null)} />
    </PageContent>
  );
}
