/**
 * Live updates over Server-Sent Events (`GET {apiRoot}/events?apikey=…`).
 *
 * - `ServerEventsProvider` (mounted once by the AuthGate) owns the EventSource, reconnects with
 *   exponential backoff, and invalidates the TanStack Query keys affected by each event.
 * - `useServerEvents()` exposes the connection state, the latest scan progress message and the
 *   health list to any component.
 * - `useServerEvent(listener)` subscribes to raw events (e.g. to toast when a command finishes).
 */
import { useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query';
import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { api, apiUrl } from './client';
import { useHealth } from './hooks/useSystem';
import { queryKeys } from './queryKeys';
import type { Command, HealthCheck, Id, ScanRun, ServerEvent } from './types';

export type ConnectionState = 'connecting' | 'open' | 'reconnecting' | 'closed';

export interface ScanProgress {
  message: string;
  scanId: Id;
  /** Epoch ms when the message was received. */
  at: number;
}

export type ServerEventListener = (event: ServerEvent) => void;

export interface ServerEventsValue {
  connection: ConnectionState;
  /** Latest `scan` progress message while a scan runs (null otherwise). */
  scanProgress: ScanProgress | null;
  /** Current health issues (query `['health']`, kept fresh by `health` events). */
  health: HealthCheck[] | undefined;
  /** Subscribe to raw events; returns an unsubscribe function. */
  subscribe: (listener: ServerEventListener) => () => void;
}

const noopSubscribe = () => () => {};

const ServerEventsContext = createContext<ServerEventsValue>({
  connection: 'closed',
  scanProgress: null,
  health: undefined,
  subscribe: noopSubscribe,
});

// ---------------------------------------------------------------------------
// Pure helpers (unit-testable)
// ---------------------------------------------------------------------------

/** Reconnect delay: 1s, 2s, 4s … capped at 30s, ±20% jitter (`random` injectable for tests). */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const base = Math.min(30_000, 1000 * 2 ** Math.max(0, attempt));
  const jitter = base * 0.2 * (random() * 2 - 1);
  return Math.round(Math.max(500, base + jitter));
}

/** Parses one SSE `data:` payload; returns null for malformed / unknown messages. */
export function parseServerEvent(data: string): ServerEvent | null {
  try {
    const parsed: unknown = JSON.parse(data);
    if (!parsed || typeof parsed !== 'object') return null;
    const { name, action } = parsed as { name?: unknown; action?: unknown };
    if (typeof name !== 'string' || typeof action !== 'string') return null;
    return parsed as ServerEvent;
  } catch {
    return null;
  }
}

const COMMAND_DONE = new Set(['completed', 'failed', 'aborted']);

/** Query keys to invalidate for a finished command. */
function keysForFinishedCommand(cmd: Command): QueryKey[] {
  const keys: QueryKey[] = [queryKeys.system.tasks];
  switch (cmd.name) {
    case 'DuplicateScan':
    case 'TargetedScan':
      keys.push(queryKeys.duplicates.all, queryKeys.scans.all, queryKeys.history.all);
      break;
    case 'ProcessQueue':
      keys.push(
        queryKeys.queue.all,
        queryKeys.actions.all,
        queryKeys.history.all,
        queryKeys.duplicates.all,
      );
      break;
    case 'SyncLibraries':
      keys.push(queryKeys.libraries.all, queryKeys.mediaServers.all);
      break;
    case 'CheckHealth':
      keys.push(queryKeys.health);
      break;
    case 'Backup':
      keys.push(queryKeys.system.backups);
      break;
    case 'Housekeeping':
      keys.push(queryKeys.history.all, queryKeys.commands.all);
      break;
    case 'CleanRecycleBin':
      keys.push(queryKeys.actions.all);
      break;
  }
  return keys;
}

/** Which query keys an event makes stale. */
export function eventInvalidations(event: ServerEvent): QueryKey[] {
  switch (event.name) {
    case 'duplicate': {
      const keys: QueryKey[] = [queryKeys.duplicates.lists, queryKeys.duplicates.stats];
      const id = event.resource?.id;
      if (id != null) keys.push(queryKeys.duplicates.detail(id));
      return keys;
    }
    case 'queue':
      return [queryKeys.queue.all, queryKeys.actions.all];
    case 'history':
      return [queryKeys.history.all];
    case 'command': {
      const keys: QueryKey[] = [queryKeys.commands.list];
      const cmd = event.resource;
      if (cmd && COMMAND_DONE.has(cmd.status)) keys.push(...keysForFinishedCommand(cmd));
      return keys;
    }
    case 'health':
      // Array payloads are written straight into the cache; otherwise refetch.
      return Array.isArray(event.resource) ? [] : [queryKeys.health];
    case 'task':
      return [queryKeys.system.tasks];
    case 'scan':
      if (event.action === 'progress') return [];
      return [queryKeys.scans.all, queryKeys.duplicates.stats];
    case 'settings':
      return [queryKeys.config.all, queryKeys.system.status, queryKeys.health];
    default:
      return [];
  }
}

/** Applies direct cache writes for events that carry the full resource. */
function applyEventToCache(qc: QueryClient, event: ServerEvent): void {
  if (event.name === 'health' && Array.isArray(event.resource)) {
    qc.setQueryData(queryKeys.health, event.resource);
  } else if (event.name === 'command' && event.resource && typeof event.resource.id === 'number') {
    qc.setQueryData(queryKeys.commands.detail(event.resource.id), event.resource);
  }
}

// ---------------------------------------------------------------------------
// Connection hook
// ---------------------------------------------------------------------------

const INVALIDATE_DEBOUNCE_MS = 300;

/** Owns the EventSource. Use through `ServerEventsProvider`; exported for special cases/tests. */
export function useServerEventsConnection(enabled: boolean) {
  const qc = useQueryClient();
  const [connection, setConnection] = useState<ConnectionState>(enabled ? 'connecting' : 'closed');
  const [scanProgress, setScanProgress] = useState<ScanProgress | null>(null);
  const listeners = useRef(new Set<ServerEventListener>());

  const subscribe = useCallback((listener: ServerEventListener) => {
    listeners.current.add(listener);
    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  useEffect(() => {
    if (!enabled || typeof EventSource === 'undefined') {
      setConnection('closed');
      return;
    }

    let source: EventSource | null = null;
    let attempt = 0;
    let everOpened = false;
    let disposed = false;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    const pending = new Map<string, QueryKey>();

    const flush = () => {
      flushTimer = undefined;
      const keys = [...pending.values()];
      pending.clear();
      for (const queryKey of keys) void qc.invalidateQueries({ queryKey });
    };

    const queueInvalidation = (keys: QueryKey[]) => {
      for (const k of keys) pending.set(JSON.stringify(k), k);
      if (pending.size > 0 && flushTimer === undefined) {
        flushTimer = setTimeout(flush, INVALIDATE_DEBOUNCE_MS);
      }
    };

    const handleEvent = (event: ServerEvent) => {
      applyEventToCache(qc, event);
      queueInvalidation(eventInvalidations(event));

      if (event.name === 'scan') {
        if (event.action === 'progress' && event.resource) {
          const { message, scanId } = event.resource;
          setScanProgress({ message, scanId, at: Date.now() });
        } else if (event.resource && (event.resource as ScanRun).status !== 'running') {
          setScanProgress(null);
        }
      } else if (
        event.name === 'command' &&
        event.resource &&
        (event.resource.name === 'DuplicateScan' || event.resource.name === 'TargetedScan') &&
        COMMAND_DONE.has(event.resource.status)
      ) {
        setScanProgress(null);
      }

      listeners.current.forEach((l) => {
        try {
          l(event);
        } catch (err) {
          console.error('server event listener failed', err);
        }
      });
    };

    const connect = () => {
      if (disposed) return;
      setConnection(everOpened ? 'reconnecting' : 'connecting');
      // Authenticated by the session cookie (same origin); never a credential in the URL.
      const es = new EventSource(apiUrl('/events'), { withCredentials: true });
      source = es;

      es.onopen = () => {
        const wasReconnect = everOpened || attempt > 0;
        attempt = 0;
        everOpened = true;
        setConnection('open');
        if (wasReconnect) {
          // We may have missed events while disconnected: refresh everything but the bootstrap.
          void qc.invalidateQueries({
            predicate: (q) => q.queryKey[0] !== queryKeys.initialize[0],
          });
        }
      };

      es.onmessage = (msg: MessageEvent<string>) => {
        const event = parseServerEvent(msg.data);
        if (event) handleEvent(event);
      };

      es.onerror = () => {
        // Take over reconnection so we control the backoff (and recover from CLOSED states).
        es.close();
        if (source === es) source = null;
        if (disposed) return;
        // EventSource does not expose the status: probe with an authenticated request, whose
        // 401 (an expired or revoked session) sends the app to the login page.
        void api.get('/system/status').catch(() => undefined);
        setConnection('reconnecting');
        const delay = backoffDelay(attempt);
        attempt += 1;
        reconnectTimer = setTimeout(connect, delay);
      };
    };

    connect();

    return () => {
      disposed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (flushTimer) clearTimeout(flushTimer);
      source?.close();
      source = null;
    };
  }, [enabled, qc]);

  return { connection, scanProgress, subscribe };
}

// ---------------------------------------------------------------------------
// Provider + consumer hooks
// ---------------------------------------------------------------------------

export interface ServerEventsProviderProps {
  children?: ReactNode;
  /** Set false to skip connecting (e.g. tests). Default true. */
  enabled?: boolean;
}

export function ServerEventsProvider({ children, enabled = true }: ServerEventsProviderProps) {
  const { connection, scanProgress, subscribe } = useServerEventsConnection(enabled);
  const health = useHealth();
  const value = useMemo<ServerEventsValue>(
    () => ({ connection, scanProgress, health: health.data, subscribe }),
    [connection, scanProgress, health.data, subscribe],
  );
  return createElement(ServerEventsContext.Provider, { value }, children);
}

/** Connection state, latest scan progress and health from the live event stream. */
export function useServerEvents(): ServerEventsValue {
  return useContext(ServerEventsContext);
}

/**
 * Calls `listener` for every server event (optionally only for one resource name).
 * The latest listener is always used; no need to memoize it.
 */
export function useServerEvent(listener: ServerEventListener, name?: ServerEvent['name']): void {
  const { subscribe } = useServerEvents();
  const ref = useRef(listener);
  useEffect(() => {
    ref.current = listener;
  });
  useEffect(
    () =>
      subscribe((event) => {
        if (!name || event.name === name) ref.current(event);
      }),
    [subscribe, name],
  );
}
