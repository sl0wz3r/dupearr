import { useSyncExternalStore } from 'react';
import { usePreferences } from '@/app/preferences';
import { formatDateTime, formatLongDateTime, formatRelativeTime, toDate } from '@/lib/format';

// One shared ticker for every mounted RelativeTime. The snapshot is quantized so repeated
// getSnapshot calls within a render are stable.
const TICK_MS = 15_000;
const listeners = new Set<() => void>();
let interval: ReturnType<typeof setInterval> | undefined;

const snapshot = () => Math.floor(Date.now() / TICK_MS) * TICK_MS;

function subscribe(cb: () => void) {
  listeners.add(cb);
  if (!interval) {
    interval = setInterval(() => {
      listeners.forEach((l) => l());
    }, TICK_MS);
  }
  return () => {
    listeners.delete(cb);
    if (listeners.size === 0 && interval) {
      clearInterval(interval);
      interval = undefined;
    }
  };
}

/** Current time (15s resolution), re-rendering every 15s (shared timer). */
export function useNow(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

export interface RelativeTimeProps {
  date: string | number | Date | null | undefined;
  /** Force relative (true) or absolute (false); default follows Settings → UI "show relative dates". */
  relative?: boolean;
  /** Text when the date is empty (default "-"). */
  fallback?: string;
  className?: string;
}

/**
 * "5 minutes ago" (or an absolute date per the UI preference) with the full date as a tooltip.
 */
export function RelativeTime({ date, relative, fallback = '-', className }: RelativeTimeProps) {
  const current = useNow();
  const { preferences } = usePreferences();
  const d = toDate(date);
  if (!d) return <span className={className}>{fallback}</span>;
  const useRelative = relative ?? preferences.showRelativeDates;
  const text = useRelative ? formatRelativeTime(d, current) : formatDateTime(d, preferences);
  return (
    <time dateTime={d.toISOString()} title={formatLongDateTime(d, preferences)} className={className}>
      {text}
    </time>
  );
}
