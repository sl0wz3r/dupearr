import { clsx } from 'clsx';

export interface PathListProps {
  paths: readonly string[] | null | undefined;
  className?: string;
  /** Max width utility of the visible path (default "max-w-[28rem]"). */
  maxWidthClass?: string;
}

/** Last path segment ("/movies/Heat (1995)/Heat.mkv" → "Heat.mkv"); handles "\" separators. */
export function baseName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, '');
  const idx = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
  return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
}

/**
 * File paths of an action: the first path truncated (file name first, so it stays visible), a
 * "+N" count for stacked parts, and every full path in the native tooltip. A native `title` is
 * used on purpose: CSS tooltips are clipped by (and add scrollbars to) the tables' overflow
 * containers. Screen readers get the full paths as text.
 */
export function PathList({ paths, className, maxWidthClass = 'max-w-[28rem]' }: PathListProps) {
  const list = (paths ?? []).filter((p) => typeof p === 'string' && p !== '');
  if (list.length === 0) return <span className={clsx('text-muted', className)}>-</span>;
  const first = list[0]!;
  const name = baseName(first);
  const dir = first.slice(0, Math.max(0, first.length - name.length));
  const extra = list.length - 1;

  return (
    <span
      title={list.join('\n')}
      className={clsx('flex min-w-0 items-baseline gap-1.5 font-mono text-xs', maxWidthClass, className)}
    >
      <span className="min-w-0 truncate">
        <span className="text-fg-strong">{name}</span>
        {dir && <span className="ml-1.5 text-muted">{dir}</span>}
      </span>
      {extra > 0 && (
        <span className="shrink-0 rounded-sm bg-card-hover px-1 text-[11px] text-muted">
          <span aria-hidden>+{extra}</span>
          <span className="sr-only">
            and {extra} more: {list.slice(1).join(', ')}
          </span>
        </span>
      )}
    </span>
  );
}
