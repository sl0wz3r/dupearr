import { clsx } from 'clsx';
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Button } from './Button';
import { IconButton } from './IconButton';
import { Select, type SelectOption } from './Select';

export interface OrderedListEditorProps {
  /** Current order, best first. */
  value: readonly string[];
  onChange: (next: string[]) => void;
  /** All possible values (label lookup + the "add" dropdown). */
  options: readonly SelectOption[];
  /** Allow adding values not yet in the list (default true). */
  allowAdd?: boolean;
  /** Allow removing values (default true). */
  allowRemove?: boolean;
  /** Minimum number of items that must remain (default 0). */
  minItems?: number;
  /** Placeholder of the add dropdown. */
  addPlaceholder?: string;
  /** Shown when the list is empty. */
  emptyText?: string;
  disabled?: boolean;
  /** Accessible name for the list, e.g. "Resolution order". */
  'aria-label'?: string;
  className?: string;
}

/** Moves the item at `from` by `delta` (−1 up / +1 down); returns a new array (unchanged if out of range). */
export function moveItem<T>(list: readonly T[], from: number, delta: number): T[] {
  const to = from + delta;
  if (from < 0 || from >= list.length || to < 0 || to >= list.length) return [...list];
  const next = [...list];
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item as T);
  return next;
}

/**
 * Reorderable list of chips (rank number, up/down, remove) with an "add" dropdown of the remaining
 * options. Used for criterion orders (resolution, source, …) and deletion methods.
 * @example
 * <OrderedListEditor value={c.order ?? []} options={schema.options ?? []} onChange={(order) => update({ ...c, order })} />
 */
export function OrderedListEditor({
  value,
  onChange,
  options,
  allowAdd = true,
  allowRemove = true,
  minItems = 0,
  addPlaceholder = 'Add…',
  emptyText = 'Nothing selected',
  disabled = false,
  className,
  ...aria
}: OrderedListEditorProps) {
  const [toAdd, setToAdd] = useState('');
  const labels = useMemo(() => new Map(options.map((o) => [o.value, o.label])), [options]);
  const remaining = options.filter((o) => !value.includes(o.value));
  const canRemove = allowRemove && value.length > minItems;

  const add = (v: string) => {
    if (!v || value.includes(v)) return;
    onChange([...value, v]);
    setToAdd('');
  };

  return (
    <div className={clsx('flex flex-col gap-2', className)}>
      {value.length === 0 ? (
        <div className="rounded border border-dashed border-border-strong px-3 py-2 text-sm text-muted">
          {emptyText}
        </div>
      ) : (
        <ol aria-label={aria['aria-label']} className="m-0 flex list-none flex-col gap-1.5 p-0">
          {value.map((v, i) => {
            const label = labels.get(v) ?? v;
            return (
              <li
                key={v}
                data-value={v}
                className="flex items-center gap-2 rounded border border-border bg-card-alt py-1 pr-1 pl-2"
              >
                <span
                  aria-hidden
                  className="flex size-5 shrink-0 items-center justify-center rounded-sm bg-accent/15 text-[11px] font-semibold text-accent-soft"
                >
                  {i + 1}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm text-fg-strong">{label}</span>
                <IconButton
                  icon={ArrowUp}
                  label={`Move ${label} up`}
                  size="xs"
                  disabled={disabled || i === 0}
                  onClick={() => onChange(moveItem(value, i, -1))}
                />
                <IconButton
                  icon={ArrowDown}
                  label={`Move ${label} down`}
                  size="xs"
                  disabled={disabled || i === value.length - 1}
                  onClick={() => onChange(moveItem(value, i, 1))}
                />
                {allowRemove && (
                  <IconButton
                    icon={X}
                    label={`Remove ${label}`}
                    size="xs"
                    variant="danger"
                    disabled={disabled || !canRemove}
                    onClick={() => onChange(value.filter((x) => x !== v))}
                  />
                )}
              </li>
            );
          })}
        </ol>
      )}

      {allowAdd && remaining.length > 0 && (
        <div className="flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <Select
              aria-label={addPlaceholder}
              options={remaining}
              value={toAdd}
              placeholder={addPlaceholder}
              disabled={disabled}
              onChange={setToAdd}
            />
          </div>
          <Button icon={Plus} disabled={disabled || !toAdd} onClick={() => add(toAdd)}>
            Add
          </Button>
        </div>
      )}
    </div>
  );
}
