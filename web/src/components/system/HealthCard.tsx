import { clsx } from 'clsx';
import { BookOpen, CircleAlert, CircleCheck, Info, RefreshCw, TriangleAlert, type LucideIcon } from 'lucide-react';
import { errorMessage } from '@/api/client';
import { useHealth, useIsCommandActive, useRunHealthCheck } from '@/api/hooks';
import type { HealthCheck, HealthType } from '@/api/types';
import { Button, Card, LoadingIndicator, LoadErrorAlert, useToast } from '@/components/ui';
import { HEALTH_SEVERITY, HEALTH_TYPE_LABELS, labelOf } from '@/lib/constants';
import { safeExternalUrl } from './systemFormat';

const TYPE_ICON: Record<HealthType, LucideIcon> = {
  ok: CircleCheck,
  notice: Info,
  warning: TriangleAlert,
  error: CircleAlert,
};

const TYPE_COLOR: Record<HealthType, string> = {
  ok: 'text-success',
  notice: 'text-info',
  warning: 'text-warning',
  error: 'text-danger',
};

/** Failing checks only (type ≠ ok), most severe first, stable by source. */
export function sortHealthIssues(checks: readonly HealthCheck[] | null | undefined): HealthCheck[] {
  return (checks ?? [])
    .filter((c) => c && c.type !== 'ok')
    .map((c, i) => ({ c, i }))
    .sort((a, b) => (HEALTH_SEVERITY[b.c.type] ?? 0) - (HEALTH_SEVERITY[a.c.type] ?? 0) || a.i - b.i)
    .map(({ c }) => c);
}

/** One health issue row: type icon, message + source, wiki link. */
function HealthRow({ check }: { check: HealthCheck }) {
  const Icon = TYPE_ICON[check.type] ?? Info;
  const wiki = safeExternalUrl(check.wikiUrl);
  return (
    <li className="flex items-start gap-3 border-b border-border py-2.5 last:border-b-0">
      <Icon
        aria-label={labelOf(HEALTH_TYPE_LABELS, check.type)}
        role="img"
        width={18}
        height={18}
        className={clsx('mt-0.5 shrink-0', TYPE_COLOR[check.type] ?? 'text-muted')}
      />
      <div className="min-w-0 flex-1">
        <div className="break-words text-fg">{check.message}</div>
        {check.source && <div className="mt-0.5 text-xs text-muted">{check.source}</div>}
      </div>
      {wiki && (
        <a
          href={wiki}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={`Read the wiki for more information about: ${check.message}`}
          title="Read the wiki for more information"
          className="inline-flex size-8 shrink-0 items-center justify-center rounded text-muted transition-colors hover:bg-row-hover hover:text-accent-soft"
        >
          <BookOpen aria-hidden width={17} height={17} />
        </a>
      )}
    </li>
  );
}

/** System → Status → Health: current health issues with wiki links and a "Check Now" button. */
export function HealthCard() {
  const health = useHealth();
  const check = useRunHealthCheck();
  const running = useIsCommandActive('CheckHealth');
  const toast = useToast();
  const issues = sortHealthIssues(health.data);

  const runCheck = () => {
    check.mutate(undefined, {
      onError: (e) => toast.error('Unable to start the health check', errorMessage(e)),
    });
  };

  return (
    <Card
      title="Health"
      actions={
        <Button size="sm" icon={RefreshCw} loading={check.isPending || running} onClick={runCheck}>
          {running ? 'Checking…' : 'Check Now'}
        </Button>
      }
    >
      {health.isLoading ? (
        <LoadingIndicator message="Loading health checks" className="py-6" />
      ) : health.isError ? (
        <LoadErrorAlert
          title="Unable to load health checks"
          message={errorMessage(health.error)}
          onRetry={() => void health.refetch()}
          retrying={health.isFetching}
        />
      ) : issues.length === 0 ? (
        <div role="status" className="flex items-center gap-3 py-2 text-fg">
          <CircleCheck aria-hidden width={20} height={20} className="shrink-0 text-success" />
          No issues with your configuration.
        </div>
      ) : (
        <ul aria-label="Health issues" className="m-0 list-none p-0">
          {issues.map((c, i) => (
            <HealthRow key={`${c.source}-${i}`} check={c} />
          ))}
        </ul>
      )}
    </Card>
  );
}
