import { clsx } from 'clsx';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import type { DuplicateGroupSummary, Id } from '@/api/types';
import { ByteSize, Checkbox, GroupStatusBadge, RelativeTime } from '@/components/ui';
import { groupTitle } from './duplicateUtils';
import { groupLibraryNames } from './DuplicatesTable';
import { FileChips } from './FileChips';
import { FlagIcons } from './FlagIcons';
import { GroupRowActions, type GroupRowActionHandlers } from './GroupRowActions';
import { PosterImage } from './PosterImage';

export interface DuplicateCardsProps extends GroupRowActionHandlers {
  rows: readonly DuplicateGroupSummary[];
  /** "posters" = poster grid; "list" = compact cards (mobile replacement for the table). */
  layout: 'posters' | 'list';
  selectable: boolean;
  selectedIds: ReadonlySet<Id>;
  onToggleSelect: (id: Id, selected: boolean) => void;
  libraryNames?: ReadonlyMap<Id, string>;
  loading?: boolean;
  emptyState?: ReactNode;
}

/** Poster grid / mobile card list of duplicate groups — same information as the table. */
export function DuplicateCards({
  rows,
  layout,
  selectable,
  selectedIds,
  onToggleSelect,
  libraryNames,
  loading,
  emptyState,
  ...actions
}: DuplicateCardsProps) {
  if (rows.length === 0) return <>{emptyState}</>;

  return (
    <ul
      aria-label="Duplicate groups"
      aria-busy={loading || undefined}
      className={clsx(
        'm-0 list-none p-0',
        layout === 'posters'
          ? 'grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 2xl:grid-cols-8'
          : 'flex flex-col gap-2',
        loading && 'opacity-60',
      )}
    >
      {rows.map((g) => {
        const title = groupTitle(g);
        const selected = selectedIds.has(g.id);
        const libraries = groupLibraryNames(g, libraryNames).join(', ');
        const checkbox = selectable && (
          <Checkbox
            aria-label={`Select ${title}`}
            checked={selected}
            onChange={(checked) => onToggleSelect(g.id, checked)}
          />
        );

        if (layout === 'posters') {
          return (
            <li
              key={g.id}
              className={clsx(
                'relative flex flex-col overflow-hidden rounded border bg-card transition-colors',
                selected ? 'border-accent bg-row-selected' : 'border-border hover:border-border-strong',
              )}
            >
              <div className="relative">
                <Link to={`/duplicate/${g.id}`} className="block" tabIndex={-1} aria-hidden>
                  <PosterImage serverId={g.serverId} thumb={g.thumb} mediaType={g.mediaType} className="w-full rounded-none" />
                </Link>
                <span className="pointer-events-none absolute top-1.5 left-1.5">
                  <GroupStatusBadge status={g.status} title={g.statusReason || undefined} />
                </span>
              </div>
              {checkbox && <span className="absolute top-1.5 right-1.5 rounded bg-card/90 p-1 shadow">{checkbox}</span>}
              <div className="flex min-w-0 flex-1 flex-col gap-1 p-2">
                <Link
                  to={`/duplicate/${g.id}`}
                  title={title}
                  className="line-clamp-2 text-sm leading-snug font-medium text-fg-strong no-underline hover:text-accent-soft"
                >
                  {title}
                </Link>
                {libraries && <div className="truncate text-xs text-muted">{libraries}</div>}
                <FileChips files={g.files} max={3} />
                <div className="mt-auto flex items-center justify-between gap-2 pt-1 text-xs">
                  <ByteSize bytes={g.reclaimableBytes} className="text-warning tabular-nums" />
                  <FlagIcons flags={g.flags} size={13} />
                </div>
                <div className="flex items-center justify-between gap-1">
                  <RelativeTime date={g.lastSeenAt} className="truncate text-[11px] text-subtle" />
                  <GroupRowActions group={g} {...actions} />
                </div>
              </div>
            </li>
          );
        }

        return (
          <li
            key={g.id}
            className={clsx(
              'flex items-start gap-2.5 rounded border bg-card p-2',
              selected ? 'border-accent bg-row-selected' : 'border-border',
            )}
          >
            {checkbox && <span className="pt-1">{checkbox}</span>}
            <Link to={`/duplicate/${g.id}`} tabIndex={-1} aria-hidden className="shrink-0">
              <PosterImage serverId={g.serverId} thumb={g.thumb} mediaType={g.mediaType} width={80} height={120} className="w-12" />
            </Link>
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <Link
                to={`/duplicate/${g.id}`}
                className="text-sm leading-snug font-medium break-words text-fg-strong no-underline hover:text-accent-soft"
              >
                {title}
              </Link>
              <div className="flex flex-wrap items-center gap-1.5">
                <GroupStatusBadge status={g.status} title={g.statusReason || undefined} />
                <FlagIcons flags={g.flags} size={13} />
              </div>
              <FileChips files={g.files} />
              <div className="flex flex-wrap items-center gap-x-2 text-xs text-muted">
                {libraries && <span className="truncate">{libraries}</span>}
                <ByteSize bytes={g.reclaimableBytes} className="text-warning" />
                <RelativeTime date={g.lastSeenAt} />
              </div>
            </div>
            <GroupRowActions group={g} {...actions} className="flex-col" />
          </li>
        );
      })}
    </ul>
  );
}
