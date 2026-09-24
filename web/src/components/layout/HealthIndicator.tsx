import { clsx } from 'clsx';
import { HeartPulse } from 'lucide-react';
import { Link } from 'react-router';
import { useServerEvents } from '@/api/events';
import type { HealthCheck, HealthType } from '@/api/types';
import { HEALTH_SEVERITY } from '@/lib/constants';

/** Worst health type among issues ("ok" when none). */
export function worstHealth(checks: readonly HealthCheck[] | undefined): HealthType {
  let worst: HealthType = 'ok';
  for (const c of checks ?? []) {
    if ((HEALTH_SEVERITY[c.type] ?? 0) > HEALTH_SEVERITY[worst]) worst = c.type;
  }
  return worst;
}

/** Issues that count toward the indicator (notices excluded from the count colour but listed). */
export function healthIssues(checks: readonly HealthCheck[] | undefined): HealthCheck[] {
  return (checks ?? []).filter((c) => c.type !== 'ok');
}

const COLOR: Record<HealthType, string> = {
  ok: 'text-header-fg/70',
  notice: 'text-info',
  warning: 'text-warning',
  error: 'text-danger',
};

const BADGE: Record<HealthType, string> = {
  ok: 'bg-success',
  notice: 'bg-info',
  warning: 'bg-warning text-black',
  error: 'bg-danger',
};

/** Header heart icon: coloured by the worst health issue, with a count badge; links to System → Status. */
export function HealthIndicator() {
  const { health } = useServerEvents();
  const issues = healthIssues(health);
  const worst = worstHealth(issues);
  const label =
    issues.length === 0
      ? 'Health: no issues'
      : `Health: ${issues.length} issue${issues.length === 1 ? '' : 's'} — ${issues.map((i) => i.message).join('; ')}`;

  return (
    <Link
      to="/system/status"
      aria-label={label}
      title={label}
      className={clsx(
        'relative inline-flex size-9 items-center justify-center rounded transition-colors hover:bg-white/10',
        COLOR[worst],
      )}
    >
      <HeartPulse aria-hidden width={20} height={20} />
      {issues.length > 0 && (
        <span
          className={clsx(
            'absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] font-bold text-white',
            BADGE[worst],
          )}
        >
          {issues.length}
        </span>
      )}
    </Link>
  );
}
