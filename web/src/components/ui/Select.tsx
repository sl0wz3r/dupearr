import { clsx } from 'clsx';
import { ChevronDown } from 'lucide-react';
import type { ComponentProps } from 'react';
import { inputClasses } from './inputStyles';

export interface SelectOption<V extends string = string> {
  value: V;
  label: string;
  disabled?: boolean;
  /** Optional group heading (options with the same group are rendered in an <optgroup>). */
  group?: string;
}

export interface SelectProps<V extends string = string>
  extends Omit<ComponentProps<'select'>, 'value' | 'onChange' | 'children'> {
  options: readonly SelectOption<V>[];
  value: V | '' | null | undefined;
  onChange: (value: V) => void;
  /** Shown as a first, empty option (value ""). */
  placeholder?: string;
  invalid?: boolean;
}

/**
 * Styled native select (string values; convert numbers at the call site).
 * @example <Select options={toOptions(LOG_LEVEL_LABELS)} value={cfg.logLevel} onChange={(v) => set('logLevel', v)} />
 */
export function Select<V extends string = string>({
  options,
  value,
  onChange,
  placeholder,
  invalid,
  className,
  ...rest
}: SelectProps<V>) {
  const groups = new Map<string, SelectOption<V>[]>();
  const ungrouped: SelectOption<V>[] = [];
  for (const o of options) {
    if (o.group) {
      const list = groups.get(o.group) ?? [];
      list.push(o);
      groups.set(o.group, list);
    } else {
      ungrouped.push(o);
    }
  }
  const renderOption = (o: SelectOption<V>) => (
    <option key={o.value} value={o.value} disabled={o.disabled}>
      {o.label}
    </option>
  );

  return (
    <div className="relative w-full">
      <select
        value={value ?? ''}
        aria-invalid={invalid || undefined}
        onChange={(e) => onChange(e.target.value as V)}
        className={inputClasses({ invalid, className: clsx('cursor-pointer appearance-none pr-9', className) })}
        {...rest}
      >
        {placeholder !== undefined && (
          <option value="" disabled={value !== '' && value !== null && value !== undefined}>
            {placeholder}
          </option>
        )}
        {ungrouped.map(renderOption)}
        {[...groups.entries()].map(([group, opts]) => (
          <optgroup key={group} label={group}>
            {opts.map(renderOption)}
          </optgroup>
        ))}
      </select>
      <ChevronDown
        aria-hidden
        width={16}
        height={16}
        className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-muted"
      />
    </div>
  );
}
