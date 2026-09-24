import { LoaderCircle, Power, RotateCw } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ApiError, api, withUrlBase } from '@/api/client';
import type { SystemStatus } from '@/api/types';
import { Button } from '@/components/ui';
import { toDate } from '@/lib/format';

/** GET {urlBase}/ping (unauthenticated) — true when the server answers 2xx. Never throws. */
export async function pingServer(signal?: AbortSignal, timeoutMs = 5000): Promise<boolean> {
  if (signal?.aborted) return false;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const onAbort = () => controller.abort();
  signal?.addEventListener('abort', onAbort, { once: true });
  try {
    const res = await fetch(withUrlBase('/ping'), {
      method: 'GET',
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    });
    return res.ok;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', onAbort);
  }
}

/**
 * Outcome of asking a running server whether it is a new process:
 * - `restarted`: a different process answers (new start time, or it no longer accepts this session);
 * - `same`: the process that was running before the restore still answers (it has not restarted yet);
 * - `unknown`: nothing conclusive (no reference start time, network/server error).
 */
export type RestartCheck = 'restarted' | 'same' | 'unknown';

/**
 * Compares the server's current `startTime` (GET /system/status) with `previousStartTime`, the start
 * time seen before the restore. Never throws and never triggers the global login redirect.
 */
export async function checkRestarted(
  previousStartTime: string | null | undefined,
  signal?: AbortSignal,
): Promise<RestartCheck> {
  const before = toDate(previousStartTime);
  if (!before || signal?.aborted) return 'unknown';
  try {
    const status = await api.get<SystemStatus>('/system/status', undefined, { signal, skipAuthRedirect: true });
    const now = toDate(status?.startTime);
    if (!now) return 'unknown';
    return now.getTime() === before.getTime() ? 'same' : 'restarted';
  } catch (e) {
    // A restore signs every session out (the session key is never restored): the new process
    // rejects this session.
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) return 'restarted';
    return 'unknown';
  }
}

export interface RestartOverlayProps {
  open: boolean;
  /** Heading (default "Restarting…"). */
  title?: string;
  /** Explanation under the heading. */
  message?: string;
  /** Called once the server is back (default: reload the page). */
  onReady?: () => void;
  /** Delay between pings (default 2s). */
  pollIntervalMs?: number;
  /**
   * If the server never appears to go down (a very fast restart), treat it as back after this long
   * (default 15s) — unless `verify` proves the old process is still running.
   */
  graceMs?: number;
  /** After this long without success a "Reload now" hint is emphasised (default 3 min). */
  timeoutMs?: number;
  /** Injectable ping for tests. */
  ping?: (signal: AbortSignal) => Promise<boolean>;
  /**
   * Tells whether the server that answers /ping is a new process (see `checkRestarted`). Used while
   * the server has not been seen going down, so a restart that never happened is not mistaken for
   * a fast one. Without it (or on `unknown`) the grace period applies.
   */
  verify?: (signal: AbortSignal) => Promise<RestartCheck>;
  /** Offered (as "Restart Now") once `verify` reports the old process still running after the grace period. */
  onRestartRequest?: () => void;
}

const defaultReload = () => window.location.reload();

/**
 * Full-page blocking overlay shown while Dupearr restarts (after a backup restore). Polls `/ping`
 * until the server has gone down and come back (or, when it never looked down, until `verify`
 * confirms a new process or the grace period passed), then reloads the page. A "Reload now" button
 * is always available.
 */
export function RestartOverlay({
  open,
  title = 'Restarting…',
  message = 'Dupearr is restarting. This page reloads automatically when it is back.',
  onReady = defaultReload,
  pollIntervalMs = 2000,
  graceMs = 15_000,
  timeoutMs = 180_000,
  ping = pingServer,
  verify,
  onRestartRequest,
}: RestartOverlayProps) {
  const [elapsed, setElapsed] = useState(0);
  const [sawDown, setSawDown] = useState(false);
  const [notRestarted, setNotRestarted] = useState(false);
  const readyRef = useRef(onReady);
  const pingRef = useRef(ping);
  const verifyRef = useRef(verify);
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    readyRef.current = onReady;
    pingRef.current = ping;
    verifyRef.current = verify;
  });

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const started = Date.now();
    let down = false;
    let done = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    setElapsed(0);
    setSawDown(false);
    setNotRestarted(false);

    const finish = () => {
      done = true;
      readyRef.current();
    };

    const tick = async () => {
      const up = await pingRef.current(controller.signal);
      if (controller.signal.aborted || done) return;
      setElapsed(Date.now() - started);
      if (!up) {
        if (!down) {
          down = true;
          setSawDown(true);
          setNotRestarted(false);
        }
      } else if (down) {
        finish();
        return;
      } else {
        // Up without having been seen down: either a very fast restart or no restart yet.
        const check = verifyRef.current ? await verifyRef.current(controller.signal) : 'unknown';
        if (controller.signal.aborted || done) return;
        const since = Date.now() - started;
        setElapsed(since);
        if (check === 'restarted') {
          finish();
          return;
        }
        if (check === 'same') {
          // Never reload into the old process: the restored backup is only applied on the next start.
          if (since >= graceMs) setNotRestarted(true);
        } else if (since >= graceMs) {
          finish();
          return;
        }
      }
      timer = setTimeout(() => void tick(), pollIntervalMs);
    };
    timer = setTimeout(() => void tick(), pollIntervalMs);

    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [open, pollIntervalMs, graceMs]);

  useEffect(() => {
    if (open) dialogRef.current?.focus();
  }, [open]);

  if (!open) return null;

  const slow = elapsed >= timeoutMs;
  const progress = notRestarted
    ? 'Dupearr is still running the previous configuration…'
    : sawDown
      ? 'Waiting for Dupearr to come back…'
      : 'Waiting for Dupearr to stop…';
  return createPortal(
    <div className="fixed inset-0 z-[80] flex items-center justify-center bg-page/95 p-6 backdrop-blur-sm">
      <div
        ref={dialogRef}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="restart-overlay-title"
        aria-describedby="restart-overlay-message"
        tabIndex={-1}
        onKeyDown={(e) => {
          // Keep focus inside the overlay: the page behind it is unusable while restarting.
          if (e.key === 'Tab') {
            e.preventDefault();
            const buttons = Array.from(
              e.currentTarget.querySelectorAll<HTMLButtonElement>('button:not([disabled])'),
            );
            if (buttons.length === 0) return;
            const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
            const next = e.shiftKey ? index - 1 : index + 1;
            buttons[(next + buttons.length) % buttons.length]?.focus();
          }
        }}
        className="flex max-w-md flex-col items-center gap-4 text-center outline-none"
      >
        <LoaderCircle aria-hidden width={48} height={48} className="animate-spin text-accent" />
        <h2 id="restart-overlay-title" className="m-0 text-2xl font-light text-fg-strong">
          {title}
        </h2>
        <p id="restart-overlay-message" className="m-0 text-sm text-muted">
          {message}
        </p>
        <p className="m-0 text-xs text-subtle" aria-live="polite">
          {progress}
          {elapsed > 0 && ` (${Math.round(elapsed / 1000)}s)`}
        </p>
        {notRestarted && (
          <p role="alert" className="m-0 text-sm text-warning">
            Dupearr has not restarted yet. The restored backup is only applied when it starts again
            {onRestartRequest ? ': restart it now, or restart the container or service.' : ': restart the container or service.'}
          </p>
        )}
        {slow && !notRestarted && (
          <p role="alert" className="m-0 text-sm text-warning">
            This is taking longer than expected. Check the container or service logs, then reload.
          </p>
        )}
        <div className="flex flex-wrap items-center justify-center gap-2">
          {notRestarted && onRestartRequest && (
            <Button icon={Power} size="sm" variant="warning" onClick={onRestartRequest}>
              Restart Now
            </Button>
          )}
          <Button
            icon={RotateCw}
            size="sm"
            variant={slow ? 'primary' : 'ghost'}
            onClick={() => readyRef.current()}
          >
            Reload now
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
