import { useIsFetching, useQueryClient } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { errorMessage } from '@/api/client';
import { useCommands, useScheduledTasks } from '@/api/hooks';
import { queryKeys } from '@/api/queryKeys';
import {
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { CommandsTable, ScheduledTasksTable } from '@/components/system/TasksTables';
import { LoadErrorAlert } from '@/components/ui';

/**
 * System → Tasks at `/system/tasks`: scheduled tasks (interval, last/next execution, run now) and
 * the command queue (recent commands, newest first). Both refresh through server events.
 */
export default function TasksPage() {
  const tasks = useScheduledTasks();
  const commands = useCommands();
  const qc = useQueryClient();
  const fetching =
    useIsFetching({ queryKey: queryKeys.system.tasks }) + useIsFetching({ queryKey: queryKeys.commands.list }) > 0;

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.system.tasks });
    void qc.invalidateQueries({ queryKey: queryKeys.commands.list });
  };

  return (
    <PageContent title="Tasks">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={RefreshCw} label="Refresh" spinning={fetching} onClick={refresh} />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Tasks" />

        <section aria-labelledby="scheduled-tasks-heading" className="mb-8">
          <h2 id="scheduled-tasks-heading" className="mb-2 text-lg font-light text-fg-strong">
            Scheduled
          </h2>
          {tasks.isError && (
            <LoadErrorAlert
              title="Unable to load scheduled tasks"
              message={errorMessage(tasks.error)}
              onRetry={() => void tasks.refetch()}
              retrying={tasks.isFetching}
              className="mb-3"
            />
          )}
          {!(tasks.isError && !tasks.data) && <ScheduledTasksTable tasks={tasks.data ?? []} loading={tasks.isLoading} />}
        </section>

        <section aria-labelledby="command-queue-heading">
          <h2 id="command-queue-heading" className="mb-2 text-lg font-light text-fg-strong">
            Queue
          </h2>
          {commands.isError && (
            <LoadErrorAlert
              title="Unable to load commands"
              message={errorMessage(commands.error)}
              onRetry={() => void commands.refetch()}
              retrying={commands.isFetching}
              className="mb-3"
            />
          )}
          {!(commands.isError && !commands.data) && (
            <CommandsTable commands={commands.data ?? []} loading={commands.isLoading} />
          )}
        </section>
      </PageBody>
    </PageContent>
  );
}
