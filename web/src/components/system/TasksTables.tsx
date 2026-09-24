import { CalendarClock, ListTodo, RefreshCw } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '@/api/client';
import { useIsCommandActive, useRunCommand, useSettings } from '@/api/hooks';
import type { Command, CommandName, CommandRequest, ScheduledTask } from '@/api/types';
import { elapsedMs } from '@/components/activity/activityMeta';
import {
  CommandStatusBadge,
  ConfirmDialog,
  EmptyState,
  IconButton,
  RelativeTime,
  Spinner,
  Table,
  useNow,
  useToast,
  type TableColumn,
} from '@/components/ui';
import { TRIGGER_LABELS, labelOf } from '@/lib/constants';
import { commandLabel, formatDuration, formatInterval, formatTimeSpan } from '@/lib/format';

/**
 * Request body for running a scheduled task by hand. Scheduled tasks take no parameters, so the
 * command name alone is valid; a Backup started by hand is a manual backup (never pruned by the
 * scheduled-backup retention).
 */
export function manualTaskRequest(taskName: CommandName): CommandRequest {
  return taskName === 'Backup' ? { name: 'Backup', type: 'manual' } : { name: taskName };
}

/** Tasks that delete files permanently when run: "Run now" asks first. */
const CONFIRM_TASKS: ReadonlySet<CommandName> = new Set<CommandName>(['CleanRecycleBin']);

/** Confirmation text for running Clean Recycle Bin now. */
function CleanRecycleBinMessage() {
  const settings = useSettings();
  const days = settings.data?.recycleBinCleanupDays;
  // The server removes nothing when the retention is 0 or less (recycled files are kept).
  if (typeof days === 'number' && days <= 0) {
    return (
      <p className="m-0">
        The recycle bin retention is {days} days, so cleanup is off: nothing will be deleted. Set Recycle Bin Cleanup
        in Settings → Media Management to delete old recycled files.
      </p>
    );
  }
  return (
    <div className="space-y-2">
      <p className="m-0">
        Files in Dupearr&apos;s recycle bin that are older than the configured retention
        {typeof days === 'number' ? ` (${days} day${days === 1 ? '' : 's'})` : ''} are deleted permanently. They can
        no longer be restored from Activity → History → Actions.
      </p>
    </div>
  );
}

/** "Run now" for a scheduled task; spins while a command with that name is queued or running. */
export function RunTaskButton({ task }: { task: Pick<ScheduledTask, 'name' | 'taskName'> }) {
  const run = useRunCommand();
  const active = useIsCommandActive(task.taskName);
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const name = task.name || commandLabel(task.taskName);
  const needsConfirm = CONFIRM_TASKS.has(task.taskName);

  const start = () => {
    setConfirming(false);
    run.mutate(manualTaskRequest(task.taskName), {
      onSuccess: () => toast.info(`${name} started`),
      onError: (e) => toast.error(`Unable to start ${name}`, errorMessage(e)),
    });
  };

  return (
    <>
      <IconButton
        icon={RefreshCw}
        size="sm"
        label={`Run ${name} now`}
        title={active ? `${name} is running` : `Run ${name} now`}
        spinning={active}
        disabled={active || run.isPending}
        onClick={needsConfirm ? () => setConfirming(true) : start}
      />
      {needsConfirm && (
        <ConfirmDialog
          open={confirming}
          title={`Run ${name}`}
          confirmLabel="Run Now"
          onCancel={() => setConfirming(false)}
          onConfirm={start}
          message={task.taskName === 'CleanRecycleBin' ? <CleanRecycleBinMessage /> : `Run ${name} now?`}
        />
      )}
    </>
  );
}

/** System → Tasks → Scheduled. */
export function ScheduledTasksTable({ tasks, loading }: { tasks: readonly ScheduledTask[]; loading?: boolean }) {
  const columns: TableColumn<ScheduledTask>[] = [
    {
      key: 'name',
      header: 'Name',
      render: (t) => <span className="font-medium text-fg-strong">{t.name || commandLabel(t.taskName)}</span>,
    },
    {
      key: 'interval',
      header: 'Interval',
      className: 'whitespace-nowrap',
      render: (t) =>
        t.interval > 0 ? formatInterval(t.interval) : <span className="text-muted">{formatInterval(t.interval)}</span>,
    },
    {
      key: 'lastExecution',
      header: 'Last Execution',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (t) => <RelativeTime date={t.lastExecution} />,
    },
    {
      key: 'lastDuration',
      header: 'Last Duration',
      hideBelow: 'md',
      className: 'whitespace-nowrap',
      render: (t) => (t.lastDuration ? formatTimeSpan(t.lastDuration) : <span className="text-muted">-</span>),
    },
    {
      key: 'nextExecution',
      header: 'Next Execution',
      hideBelow: 'lg',
      className: 'whitespace-nowrap',
      render: (t) => (t.interval > 0 ? <RelativeTime date={t.nextExecution} /> : <span className="text-muted">-</span>),
    },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '48px',
      render: (t) => <RunTaskButton task={t} />,
    },
  ];
  return (
    <Table
      aria-label="Scheduled tasks"
      columns={columns}
      rows={tasks}
      getRowId={(t) => t.taskName || t.name}
      loading={loading}
      emptyState={<EmptyState icon={CalendarClock} title="No scheduled tasks" compact />}
    />
  );
}

/** Command duration: server value when finished, live elapsed time while running. */
function CommandDuration({ command }: { command: Command }) {
  const now = useNow();
  if (command.duration) return <>{formatTimeSpan(command.duration)}</>;
  const end = command.ended ?? (command.status === 'started' ? now : null);
  const ms = elapsedMs(command.started, end);
  return ms === null ? <span className="text-muted">-</span> : <>{formatDuration(ms)}</>;
}

/** System → Tasks → Queue: recent commands (newest first). */
export function CommandsTable({ commands, loading }: { commands: readonly Command[]; loading?: boolean }) {
  const columns: TableColumn<Command>[] = [
    {
      key: 'name',
      header: 'Name',
      render: (c) => <span className="font-medium text-fg-strong">{commandLabel(c.name)}</span>,
    },
    {
      key: 'trigger',
      header: 'Trigger',
      hideBelow: 'md',
      render: (c) => labelOf(TRIGGER_LABELS, c.trigger),
    },
    {
      key: 'status',
      header: 'Status',
      render: (c) => (
        <span className="inline-flex items-center gap-1.5">
          {c.status === 'started' && <Spinner size="xs" label="Running" />}
          <CommandStatusBadge status={c.status} />
        </span>
      ),
    },
    {
      key: 'message',
      header: 'Message',
      hideBelow: 'lg',
      className: 'max-w-0 w-[30%]',
      render: (c) =>
        c.message ? (
          <span className="block truncate" title={c.message}>
            {c.message}
          </span>
        ) : (
          <span className="text-muted">-</span>
        ),
    },
    {
      key: 'queued',
      header: 'Queued',
      hideBelow: 'xl',
      className: 'whitespace-nowrap',
      render: (c) => <RelativeTime date={c.queued} />,
    },
    {
      key: 'started',
      header: 'Started',
      hideBelow: 'sm',
      className: 'whitespace-nowrap',
      render: (c) => <RelativeTime date={c.started} />,
    },
    {
      key: 'ended',
      header: 'Ended',
      hideBelow: 'xl',
      className: 'whitespace-nowrap',
      render: (c) => <RelativeTime date={c.ended} />,
    },
    {
      key: 'duration',
      header: 'Duration',
      hideBelow: 'md',
      className: 'whitespace-nowrap',
      render: (c) => <CommandDuration command={c} />,
    },
  ];
  return (
    <Table
      aria-label="Command queue"
      columns={columns}
      rows={commands}
      getRowId={(c) => c.id}
      loading={loading}
      dense
      emptyState={
        <EmptyState icon={ListTodo} title="No recent commands" description="Commands you run appear here." compact />
      }
    />
  );
}
