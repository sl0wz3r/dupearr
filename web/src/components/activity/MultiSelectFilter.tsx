import { clsx } from 'clsx';
import { ChevronDown, ListFilter } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import { Button, Checkbox } from '@/components/ui';

export interface MultiSelectOption<V extends string> {
  value: V;
  label: string;
}

export interface MultiSelectFilterProps<V extends string> {
  /** Name of the filtered attribute, e.g. "Event type". */
  label: string;
  options: readonly MultiSelectOption<V>[];
  /** Selected values; empty = no filter ("All"). */
  value: readonly V[];
  onChange: (value: V[]) => void;
  /** Popover alignment relative to the button (default "right"). */
  align?: 'left' | 'right';
  className?: string;
}

/**
 * Dropdown of checkboxes for multi-value filters (event types, action statuses). An empty selection
 * means "all". Closes on Esc and outside clicks.
 */
export function MultiSelectFilter<V extends string>({
  label,
  options,
  value,
  onChange,
  align = 'right',
  className,
}: MultiSelectFilterProps<V>) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const panelId = useId();
  const selected = new Set(value);
  const active = value.length > 0;

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    // Focus the first checkbox so keyboard users land inside the panel.
    const frame = requestAnimationFrame(() => {
      panelRef.current?.querySelector<HTMLInputElement>('input')?.focus();
    });
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      cancelAnimationFrame(frame);
    };
  }, [open]);

  const toggle = (v: V, checked: boolean) => {
    const next = options.map((o) => o.value).filter((o) => (o === v ? checked : selected.has(o)));
    onChange(next);
  };

  const summary = !active
    ? 'All'
    : value.length === 1
      ? (options.find((o) => o.value === value[0])?.label ?? value[0])
      : `${value.length} selected`;

  return (
    <div
      ref={rootRef}
      className={clsx('relative inline-block', className)}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation();
          setOpen(false);
          buttonRef.current?.focus();
        }
      }}
    >
      <Button
        ref={buttonRef}
        size="sm"
        icon={ListFilter}
        variant={active ? 'primary' : 'default'}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen((o) => !o)}
        trailing={<ChevronDown aria-hidden width={14} height={14} className="opacity-70" />}
      >
        <span>
          {label}: <span className="font-semibold">{summary}</span>
        </span>
      </Button>
      {open && (
        <div
          ref={panelRef}
          id={panelId}
          role="group"
          aria-label={`${label} filter`}
          className={clsx(
            'absolute z-30 mt-1 w-64 rounded border border-border-strong bg-card p-2 shadow-popover',
            align === 'right' ? 'right-0' : 'left-0',
          )}
        >
          <div className="flex items-center justify-between gap-2 border-b border-border px-1 pb-2">
            <span className="text-xs font-semibold tracking-wide text-muted uppercase">{label}</span>
            <button
              type="button"
              className="text-xs text-accent-soft hover:underline disabled:cursor-not-allowed disabled:opacity-50"
              disabled={!active}
              onClick={() => onChange([])}
            >
              Show all
            </button>
          </div>
          <div className="max-h-72 overflow-y-auto pt-2">
            {options.map((o) => (
              <div key={o.value} className="rounded px-1 py-1 hover:bg-row-hover">
                <Checkbox
                  checked={selected.has(o.value)}
                  onChange={(checked) => toggle(o.value, checked)}
                  label={o.label}
                  className="w-full"
                />
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
