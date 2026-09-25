/**
 * Links into Radarr/Sonarr ("Open in Radarr", Activity → Queue) and helpers for the queue entries
 * that defer a group. The server builds the links from the instance's validated address; the UI
 * checks them again before rendering one as an href (a restored backup is not trusted).
 */
import type { ArrItemRef, ArrKind, ArrLink, ArrQueueEntry, Id } from '@/api/types';
import { ARR_KIND_LABELS, labelOf } from '@/lib/constants';

/** `url` when it is an absolute http(s) URL without credentials, else undefined. */
export function safeHref(url: string | null | undefined): string | undefined {
  if (typeof url !== 'string' || !/^https?:\/\//i.test(url)) return undefined;
  try {
    const u = new URL(url);
    if ((u.protocol !== 'http:' && u.protocol !== 'https:') || !u.hostname || u.username || u.password) return undefined;
    return u.href;
  } catch {
    return undefined;
  }
}

/** The link of an *arr item (instance + movie/series id), if the server sent one. */
export function arrLinkFor(links: readonly ArrLink[] | null | undefined, instanceId: Id, itemId: Id): ArrLink | undefined {
  return (links ?? []).find((l) => l.instanceId === instanceId && l.itemId === itemId);
}

/** The application's name for an *arr kind ("Radarr", "Sonarr"). */
export function arrAppName(kind: ArrKind | string | undefined): string {
  return kind ? labelOf(ARR_KIND_LABELS, kind) : '*arr';
}

/**
 * A completed download the *arr will not import by itself: it stays in the *arr's queue — and keeps
 * the duplicate deferred — until someone removes it there (or imports it by hand). That is a
 * completed entry with a warning or error (the *arr's queue offers Manual Import for completed +
 * warning, Radarr v5.28 QueueRow.tsx) or in state importBlocked. A plain importPending (status ok)
 * is the short, normal step before the *arr imports it on its own.
 */
export function isStuckImport(entry: ArrQueueEntry): boolean {
  if ((entry.status ?? '').toLowerCase() !== 'completed') return false;
  const tracked = (entry.trackedDownloadStatus ?? '').toLowerCase();
  const state = (entry.trackedDownloadState ?? '').toLowerCase();
  return tracked === 'warning' || tracked === 'error' || state === 'importblocked';
}

/** The items of a group that have queue entries (the ones deferring it). */
export function queuedArrItems(items: readonly ArrItemRef[] | null | undefined): ArrItemRef[] {
  return (items ?? []).filter((it) => (it.queueCount ?? 0) > 0 || (it.queue ?? []).length > 0);
}
