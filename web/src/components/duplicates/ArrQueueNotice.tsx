import { ExternalLink } from 'lucide-react';
import type { ReactNode } from 'react';
import type { ArrItemRef, ArrLink, DuplicateGroup, GroupStatus } from '@/api/types';
import { Alert } from '@/components/ui';
import { formatNumber } from '@/lib/format';
import { arrAppName, arrLinkFor, isStuckImport, queuedArrItems, safeHref } from './arrLinks';

function OutLink({ href, children }: { href: string | undefined; children: ReactNode }) {
  if (!href) return null;
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center gap-1 rounded-sm border border-border-strong px-1.5 py-0.5 text-xs text-fg no-underline hover:border-accent hover:text-accent-soft"
    >
      {children}
      <ExternalLink aria-hidden width={11} height={11} />
    </a>
  );
}

function QueueItem({ item, link, deferred }: { item: ArrItemRef; link: ArrLink | undefined; deferred: boolean }) {
  const app = arrAppName(item.kind);
  const name = link?.instanceName || item.instanceName || app;
  const entries = item.queue ?? [];
  const count = Math.max(item.queueCount ?? 0, entries.length);
  const more = count - entries.length;
  const stuck = entries.some(isStuckImport);
  return (
    <li className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-semibold text-fg-strong">{name}</span>
        <span className="text-muted">
          {count === 1 ? '1 queue entry' : `${formatNumber(count)} queue entries`}
        </span>
        <OutLink href={safeHref(link?.queueUrl)}>Open queue</OutLink>
        <OutLink href={safeHref(link?.itemUrl)}>Open in {name}</OutLink>
      </div>
      {entries.length > 0 && (
        <ul className="m-0 flex flex-col gap-1 pl-5" aria-label={`${name} queue entries`}>
          {entries.map((e, i) => (
            <li key={`${i}-${e.title}`}>
              {e.title && <span className="font-mono break-all text-fg-strong">{e.title}</span>}
              {(e.label || e.status) && <span className="text-fg"> — {e.label || e.status}</span>}
              {((e.messages ?? []).length > 0 || e.errorMessage) && (
                <ul className="m-0 pl-4 text-muted">
                  {(e.messages ?? []).map((m) => (
                    <li key={m} className="break-words">
                      {m}
                    </li>
                  ))}
                  {e.errorMessage && <li className="break-words">{e.errorMessage}</li>}
                </ul>
              )}
            </li>
          ))}
          {more > 0 && <li className="text-muted">{more === 1 ? '1 more entry' : `${formatNumber(more)} more entries`}</li>}
        </ul>
      )}
      {stuck && (
        <p className="m-0">
          {app} downloaded this release but will not import it automatically. It stays in {app}&rsquo;s queue —
          and {deferred ? 'this duplicate stays deferred' : 'Dupearr removes nothing of this title'} — until you
          remove it there: in {app}&rsquo;s Activity → Queue, <em>Remove from queue</em> with Removal Method{' '}
          <em>Remove from Download Client</em> (or <em>Ignore Download</em> to keep the download) and, so it is not
          grabbed again, Blocklist Release <em>Blocklist Only</em>. Use <em>Manual Import</em> only if you want this
          release instead of the file {app} has now: importing replaces that file.
        </p>
      )}
      {item.kind === 'sonarr' && (
        <p className="m-0 text-muted">
          Sonarr&rsquo;s queue is checked per series: any queue entry of the series defers every duplicate of the series.
        </p>
      )}
    </li>
  );
}

/**
 * Statuses the notice is not shown for although the flag is set: nothing of the group is to be
 * removed (protected), a person set it aside (ignored) or it is done (resolved).
 */
const QUIET_STATUSES: readonly GroupStatus[] = ['protected', 'ignored', 'resolved'];

export interface ArrQueueNoticeProps {
  group: Pick<DuplicateGroup, 'status' | 'flags' | 'arrItems'>;
  /** Links to the group's Radarr/Sonarr items (GET /api/v1/duplicate/{id}). */
  arrLinks?: readonly ArrLink[] | null;
}

/**
 * Why the *arr's download queue holds a group (flag arr_queue_busy): the queue entries the last
 * scan saw, links to the *arr's queue and item page, and what to do about a download the *arr will
 * not import. The scan sets the flag whatever the group's status, so the wording follows the
 * status: "deferred" only for a deferred group; a group in review (or queued) is told that nothing
 * of it is removed while the entry is queued; quiet statuses show nothing.
 */
export function ArrQueueNotice({ group, arrLinks }: ArrQueueNoticeProps) {
  if (!(group.flags ?? []).includes('arr_queue_busy') || QUIET_STATUSES.includes(group.status)) return null;
  const deferred = group.status === 'deferred';
  const items = queuedArrItems(group.arrItems);
  const stuck = items.some((it) => (it.queue ?? []).some(isStuckImport));
  // A group stored before queue entries were recorded: link every queue of its *arr items.
  const queueLinks = items.length === 0 ? [...new Map((arrLinks ?? []).map((l) => [l.instanceId, l])).values()] : [];
  return (
    <Alert
      kind={stuck ? 'warning' : 'info'}
      title={deferred ? 'Deferred by the Radarr/Sonarr download queue' : 'In the Radarr/Sonarr download queue'}
    >
      <p className="m-0">
        Dupearr removes nothing of a title while its Radarr/Sonarr has a download or import queued for it: the import could
        replace or delete files.
        {deferred && ' The duplicate is checked again on the next scan after the entry has left the queue.'}
      </p>
      {items.length > 0 ? (
        <ul className="m-0 mt-2 flex list-none flex-col gap-2 p-0" aria-label="Queue entries">
          {items.map((it) => (
            <QueueItem
              key={`${it.instanceId}-${it.itemId}`}
              item={it}
              link={arrLinkFor(arrLinks, it.instanceId, it.itemId)}
              deferred={deferred}
            />
          ))}
        </ul>
      ) : (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <span className="text-muted">Re-scan this group to see the queue entries.</span>
          {queueLinks.map((l) => (
            <OutLink key={l.instanceId} href={safeHref(l.queueUrl)}>
              Open {l.instanceName || arrAppName(l.kind)} queue
            </OutLink>
          ))}
        </div>
      )}
    </Alert>
  );
}
