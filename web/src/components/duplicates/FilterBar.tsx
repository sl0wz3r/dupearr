import { clsx } from 'clsx';
import { ChevronDown, ChevronUp, Funnel, RotateCcw, Search, X } from 'lucide-react';
import { useState, type ReactNode } from 'react';
import type { DuplicateStats, GroupFlag, GroupStatus, Library, MediaType } from '@/api/types';
import { Select } from '@/components/ui';
import {
  GROUP_FLAG_LABELS,
  GROUP_STATUS_DESCRIPTIONS,
  GROUP_STATUS_KIND,
  GROUP_STATUS_LABELS,
  GROUP_STATUSES,
  OPEN_GROUP_STATUSES,
  type StatusKind,
} from '@/lib/constants';
import { formatNumber } from '@/lib/format';
import { hasActiveFilters, normalizeStatuses, sameStatuses, type DuplicateFilters } from './listState';

const DOT: Record<StatusKind, string> = {
  default: 'bg-neutral',
  primary: 'bg-primary',
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  info: 'bg-info',
  pink: 'bg-pink',
  inverse: 'bg-fg-strong',
};

function Chip({
  pressed,
  onClick,
  children,
  title,
}: {
  pressed: boolean;
  onClick: () => void;
  children: ReactNode;
  title?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      title={title}
      className={clsx(
        'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs whitespace-nowrap transition-colors',
        pressed
          ? 'border-accent bg-accent/15 text-fg-strong'
          : 'border-border-strong text-muted hover:border-accent/60 hover:text-fg',
      )}
    >
      {children}
    </button>
  );
}

function GroupLabel({ children }: { children: ReactNode }) {
  return <span className="mr-0.5 text-[11px] font-semibold tracking-wide text-subtle uppercase">{children}</span>;
}

const MEDIA_TYPES: { value: MediaType | ''; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'movie', label: 'Movies' },
  { value: 'episode', label: 'Episodes' },
];

/** Number of filters that differ from the defaults (status counts once). */
export function activeFilterCount(filters: DuplicateFilters): number {
  return (
    (sameStatuses(filters.status, OPEN_GROUP_STATUSES) ? 0 : 1) +
    (filters.mediaType ? 1 : 0) +
    (filters.libraryId ? 1 : 0) +
    (filters.flag ? 1 : 0) +
    (filters.search ? 1 : 0)
  );
}

export interface FilterBarProps {
  filters: DuplicateFilters;
  onChange: (patch: Partial<DuplicateFilters>) => void;
  /** Resets every filter (search included) to the defaults. */
  onReset: () => void;
  stats?: DuplicateStats;
  libraries?: readonly Library[];
  /** Start collapsed behind a "Filters" toggle (small screens). */
  collapsible?: boolean;
  className?: string;
}

function SearchChip({ term, onClear }: { term: string; onClear: () => void }) {
  return (
    <span className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-full border border-accent bg-accent/15 pr-1 pl-2.5 text-xs text-fg-strong">
      <Search aria-hidden width={12} height={12} className="shrink-0" />
      <span className="max-w-48 truncate">“{term}”</span>
      <button
        type="button"
        aria-label="Remove search filter"
        onClick={onClear}
        className="rounded-full p-0.5 text-muted hover:text-fg-strong"
      >
        <X aria-hidden width={12} height={12} />
      </button>
    </span>
  );
}

function ResetButton({ onReset }: { onReset: () => void }) {
  return (
    <button type="button" onClick={onReset} className="inline-flex items-center gap-1 text-xs text-accent-soft hover:underline">
      <RotateCcw aria-hidden width={12} height={12} />
      Reset filters
    </button>
  );
}

/**
 * Filter chips under the stats strip: status (multi-select), media type, library, flag and the
 * active search term. Changes are reported as patches; the page owns the URL state.
 */
export function FilterBar({ filters, onChange, onReset, stats, libraries, collapsible = false, className }: FilterBarProps) {
  const [expanded, setExpanded] = useState(false);
  const selected = new Set(filters.status);
  const active = activeFilterCount(filters);

  if (collapsible && !expanded) {
    return (
      <div className={clsx('flex flex-wrap items-center gap-2', className)}>
        <button
          type="button"
          aria-expanded={false}
          onClick={() => setExpanded(true)}
          className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border-strong px-2.5 text-xs text-fg hover:border-accent/60"
        >
          <Funnel aria-hidden width={12} height={12} />
          Filters{active > 0 ? ` (${active})` : ''}
          <ChevronDown aria-hidden width={12} height={12} />
        </button>
        {filters.search && <SearchChip term={filters.search} onClear={() => onChange({ search: undefined })} />}
        {active > 0 && <ResetButton onReset={onReset} />}
      </div>
    );
  }

  const allStatuses = filters.status.length === 0;

  const toggleStatus = (s: GroupStatus) => {
    const base = allStatuses ? new Set<GroupStatus>() : new Set(selected);
    if (base.has(s)) base.delete(s);
    else base.add(s);
    // Empty selection = every status.
    onChange({ status: normalizeStatuses([...base]) });
  };

  const libraryOptions = [
    { value: '', label: 'All libraries' },
    ...[...(libraries ?? [])]
      .sort((a, b) => a.title.localeCompare(b.title))
      .map((l) => ({ value: String(l.id), label: l.title })),
  ];
  // A library id from the URL that isn't (or not yet) in the list must still show as selected.
  if (filters.libraryId && !libraryOptions.some((o) => o.value === String(filters.libraryId))) {
    libraryOptions.push({ value: String(filters.libraryId), label: `Library #${filters.libraryId}` });
  }
  const flagOptions = [
    { value: '', label: 'Any flag' },
    ...(Object.keys(GROUP_FLAG_LABELS) as GroupFlag[]).map((f) => ({ value: f, label: GROUP_FLAG_LABELS[f] })),
  ];

  return (
    <div className={clsx('flex flex-col gap-2', className)} aria-label="Filters" role="group">
      <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Status">
        <GroupLabel>Status</GroupLabel>
        <Chip pressed={allStatuses} onClick={() => onChange({ status: [] })} title="Show every status">
          All
        </Chip>
        <Chip
          pressed={sameStatuses(filters.status, OPEN_GROUP_STATUSES)}
          onClick={() => onChange({ status: [...OPEN_GROUP_STATUSES] })}
          title="Groups that still need attention (default)"
        >
          Open
        </Chip>
        {GROUP_STATUSES.map((s) => {
          const n = stats?.byStatus?.[s];
          return (
            <Chip
              key={s}
              pressed={!allStatuses && selected.has(s)}
              onClick={() => toggleStatus(s)}
              title={GROUP_STATUS_DESCRIPTIONS[s]}
            >
              <span aria-hidden className={clsx('size-2 rounded-full', DOT[GROUP_STATUS_KIND[s]])} />
              {GROUP_STATUS_LABELS[s]}
              {n !== undefined && n > 0 && <span className="text-subtle">{formatNumber(n)}</span>}
            </Chip>
          );
        })}
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-1.5" role="group" aria-label="Media type">
          <GroupLabel>Type</GroupLabel>
          {MEDIA_TYPES.map((t) => (
            <Chip
              key={t.value || 'all'}
              pressed={(filters.mediaType ?? '') === t.value}
              onClick={() => onChange({ mediaType: t.value || undefined })}
            >
              {t.label}
            </Chip>
          ))}
        </div>

        <label className="flex items-center gap-1.5">
          <GroupLabel>Library</GroupLabel>
          <span className="w-44">
            <Select
              aria-label="Library"
              options={libraryOptions}
              value={filters.libraryId ? String(filters.libraryId) : ''}
              onChange={(v) => onChange({ libraryId: v ? Number(v) : undefined })}
            />
          </span>
        </label>

        <label className="flex items-center gap-1.5">
          <GroupLabel>Flag</GroupLabel>
          <span className="w-44">
            <Select
              aria-label="Flag"
              options={flagOptions}
              value={filters.flag ?? ''}
              onChange={(v) => onChange({ flag: (v || undefined) as GroupFlag | undefined })}
            />
          </span>
        </label>

        {filters.search && <SearchChip term={filters.search} onClear={() => onChange({ search: undefined })} />}

        {hasActiveFilters(filters) && <ResetButton onReset={onReset} />}

        {collapsible && (
          <button
            type="button"
            aria-expanded
            onClick={() => setExpanded(false)}
            className="inline-flex items-center gap-1 text-xs text-muted hover:text-fg"
          >
            <ChevronUp aria-hidden width={12} height={12} />
            Hide filters
          </button>
        )}
      </div>
    </div>
  );
}
