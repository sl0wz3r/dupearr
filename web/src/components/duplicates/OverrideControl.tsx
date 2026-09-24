import { clsx } from 'clsx';
import { useRef, type KeyboardEvent } from 'react';
import type { Decision } from '@/api/types';
import { Spinner } from '@/components/ui';

/** "auto" = no override (the profile decides). */
export type OverrideValue = 'auto' | Decision;

const OPTIONS: { value: OverrideValue; label: string; title: string }[] = [
  { value: 'auto', label: 'Auto', title: 'Let the profile decide' },
  { value: 'keep', label: 'Keep', title: 'Always keep this copy' },
  { value: 'remove', label: 'Remove', title: 'Remove this copy' },
];

const CHECKED: Record<OverrideValue, string> = {
  auto: 'bg-accent text-accent-fg',
  keep: 'bg-success text-white',
  remove: 'bg-danger text-white',
};

export interface OverrideControlProps {
  value: OverrideValue;
  onChange: (value: OverrideValue) => void;
  /** Accessible name, e.g. "Override for copy #2". */
  label: string;
  /** Locks the whole control (e.g. removals already queued). */
  disabled?: boolean;
  /** Per-option lock with the reason as tooltip (e.g. `remove` on a protected copy). */
  disabledOptions?: Partial<Record<OverrideValue, string>>;
  /** Request in flight: shows a spinner and blocks further changes. */
  pending?: boolean;
  /** Tooltip explaining why the control is disabled. */
  title?: string;
}

/**
 * Segmented Auto / Keep / Remove override (an ARIA radio group with roving focus: arrow keys move
 * and select).
 */
export function OverrideControl({
  value,
  onChange,
  label,
  disabled = false,
  disabledOptions,
  pending = false,
  title,
}: OverrideControlProps) {
  const refs = useRef<Partial<Record<OverrideValue, HTMLButtonElement | null>>>({});
  const locked = disabled || pending;
  const enabled = OPTIONS.filter((o) => !disabledOptions?.[o.value]);

  const select = (next: OverrideValue) => {
    if (locked || next === value || disabledOptions?.[next]) return;
    onChange(next);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (locked || enabled.length === 0) return;
    const idx = Math.max(0, enabled.findIndex((o) => o.value === value));
    let next: OverrideValue | undefined;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = enabled[(idx + 1) % enabled.length]?.value;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = enabled[(idx - 1 + enabled.length) % enabled.length]?.value;
    if (next) {
      e.preventDefault();
      refs.current[next]?.focus();
      select(next);
    }
  };

  return (
    <div className="inline-flex items-center gap-1.5" title={title}>
      <div
        role="radiogroup"
        aria-label={label}
        aria-disabled={locked || undefined}
        onKeyDown={onKeyDown}
        className={clsx(
          'inline-flex overflow-hidden rounded border border-border-strong text-[11px] font-semibold',
          locked && 'opacity-60',
        )}
      >
        {OPTIONS.map((o) => {
          const checked = o.value === value;
          const reason = disabledOptions?.[o.value];
          return (
            <button
              key={o.value}
              ref={(el) => {
                refs.current[o.value] = el;
              }}
              type="button"
              role="radio"
              aria-checked={checked}
              tabIndex={checked ? 0 : -1}
              disabled={locked || !!reason}
              title={reason ?? o.title}
              onClick={() => select(o.value)}
              className={clsx(
                'px-2 py-0.5 transition-colors not-first:border-l not-first:border-border-strong',
                'disabled:cursor-not-allowed focus-visible:relative focus-visible:outline-2 focus-visible:outline-accent',
                checked ? CHECKED[o.value] : 'bg-card text-muted hover:bg-card-hover hover:text-fg',
                reason && !checked && 'opacity-50',
              )}
            >
              {o.label}
            </button>
          );
        })}
      </div>
      {pending && <Spinner size="xs" label="Saving override" />}
    </div>
  );
}
