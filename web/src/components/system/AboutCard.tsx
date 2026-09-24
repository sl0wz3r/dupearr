import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { errorMessage } from '@/api/client';
import { useSystemStatus } from '@/api/hooks';
import type { SystemStatus } from '@/api/types';
import { usePreferences } from '@/app/preferences';
import { Alert, Badge, ByteSize, Card, CopyButton, LoadingIndicator, useNow } from '@/components/ui';
import { AUTH_METHOD_LABELS, labelOf } from '@/lib/constants';
import { formatDateTime, toDate } from '@/lib/format';
import { formatUptime, shortCommit, uptimeSecondsAt } from './systemFormat';

const MODE_SHORT_LABELS: Record<SystemStatus['mode'], string> = { manual: 'Manual', auto: 'Automatic' };

/** Two-column description list used by the Status cards. */
export function InfoList({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="m-0 grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-[minmax(9rem,max-content)_1fr]">
      {items.map((item) => (
        <div key={item.label} className="contents">
          <dt className="text-sm font-semibold text-muted sm:text-right">{item.label}</dt>
          <dd className="m-0 mb-2 min-w-0 break-words text-fg sm:mb-0">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A filesystem path with a copy button. */
function PathValue({ path, extra }: { path: string; extra?: ReactNode }) {
  if (!path) return <span className="text-muted">-</span>;
  return (
    <span className="inline-flex max-w-full items-center gap-1.5">
      <span className="min-w-0 font-mono text-xs break-all">{path}</span>
      {extra}
      <CopyButton value={path} size="sm" label={`Copy ${path}`} />
    </span>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-muted">{children}</span>;
}

/** Live uptime (updates every 15s via the shared ticker). */
function Uptime({ status }: { status: SystemStatus }) {
  const now = useNow();
  const seconds = uptimeSecondsAt(status.startTime, status.uptimeSeconds, now);
  const start = toDate(status.startTime);
  return (
    <span title={start ? `Started ${start.toLocaleString()}` : undefined}>
      {seconds === null ? '-' : formatUptime(seconds)}
    </span>
  );
}

/** Rows of the About card (exported for tests). */
export function useAboutItems(status: SystemStatus): { label: string; value: ReactNode }[] {
  const { preferences } = usePreferences();
  const commit = shortCommit(status.commit);
  const buildDate = toDate(status.buildDate);
  return [
    { label: 'Version', value: status.version || <Muted>unknown</Muted> },
    {
      label: 'Commit',
      value: commit ? (
        <span className="font-mono text-xs" title={status.commit}>
          {commit}
        </span>
      ) : (
        <Muted>unknown</Muted>
      ),
    },
    {
      label: 'Build Date',
      value: buildDate ? formatDateTime(buildDate, preferences) : status.buildDate || <Muted>unknown</Muted>,
    },
    { label: 'Go Version', value: status.goVersion || <Muted>unknown</Muted> },
    {
      label: 'OS / Arch',
      value: [status.osName, status.osArch].filter(Boolean).join(' / ') || <Muted>unknown</Muted>,
    },
    { label: 'Docker', value: status.isDocker ? 'Yes' : 'No' },
    { label: 'Uptime', value: <Uptime status={status} /> },
    { label: 'Data Directory', value: <PathValue path={status.dataDirectory} /> },
    { label: 'Config File', value: <PathValue path={status.configFile} /> },
    {
      label: 'Database',
      value: (
        <PathValue
          path={status.databaseFile}
          extra={
            status.databaseSize > 0 ? (
              <span className="shrink-0 text-xs text-muted">
                (<ByteSize bytes={status.databaseSize} />)
              </span>
            ) : undefined
          }
        />
      ),
    },
    {
      label: 'URL Base',
      value: status.urlBase ? <span className="font-mono text-xs">{status.urlBase}</span> : <Muted>(none)</Muted>,
    },
    { label: 'Authentication', value: labelOf(AUTH_METHOD_LABELS, status.authentication) || <Muted>unknown</Muted> },
    {
      label: 'Mode',
      value: labelOf(MODE_SHORT_LABELS, status.mode) || <Muted>unknown</Muted>,
    },
    {
      label: 'Dry Run',
      value: (
        <span className="inline-flex flex-wrap items-center gap-2">
          {status.dryRun ? (
            <Badge kind="info" title="Removals are only simulated; nothing is deleted">
              Enabled
            </Badge>
          ) : (
            <Badge kind="warning" outline title="Approved removals delete files">
              Disabled
            </Badge>
          )}
          <Link to="/settings/mediamanagement" className="text-xs text-accent-soft hover:underline">
            Change
          </Link>
        </span>
      ),
    },
  ];
}

function AboutList({ status }: { status: SystemStatus }) {
  return <InfoList items={useAboutItems(status)} />;
}

/** System → Status → About: version, runtime, paths and operating mode of this install. */
export function AboutCard() {
  const status = useSystemStatus();
  return (
    <Card title="About">
      {status.isLoading ? (
        <LoadingIndicator message="Loading status" className="py-6" />
      ) : status.isError || !status.data ? (
        <Alert kind="error" title="Unable to load system status">
          {errorMessage(status.error)}
        </Alert>
      ) : (
        <AboutList status={status.data} />
      )}
    </Card>
  );
}
