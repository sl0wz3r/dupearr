import { clsx } from 'clsx';
import { useEffect, useState, type ComponentProps } from 'react';
import { inputClasses } from './inputStyles';

export interface NumberInputProps
  extends Omit<ComponentProps<'input'>, 'value' | 'onChange' | 'type' | 'min' | 'max' | 'step'> {
  value: number | null | undefined;
  /** Called with the parsed number (or null when empty and `allowEmpty`). */
  onChange: (value: number | null) => void;
  min?: number;
  max?: number;
  step?: number;
  /** Allow an empty value (→ null). Default false: empty reverts to the last value on blur. */
  allowEmpty?: boolean;
  /** Only integers (default true). */
  integer?: boolean;
  /** Unit shown on the right, e.g. "minutes", "%", "MB". */
  unit?: string;
  invalid?: boolean;
}

function clamp(n: number, min?: number, max?: number): number {
  if (min !== undefined && n < min) return min;
  if (max !== undefined && n > max) return max;
  return n;
}

/**
 * Numeric input that keeps a string draft while typing and commits parsed numbers.
 * Values are clamped to min/max on blur.
 * @example <NumberInput value={s.minAgeHours} onChange={(v) => set('minAgeHours', v ?? 0)} min={0} unit="hours" />
 */
export function NumberInput({
  value,
  onChange,
  min,
  max,
  step = 1,
  allowEmpty = false,
  integer = true,
  unit,
  invalid,
  className,
  onBlur,
  ...rest
}: NumberInputProps) {
  const [draft, setDraft] = useState(value === null || value === undefined ? '' : String(value));

  // Sync external changes (e.g. form reset).
  useEffect(() => {
    setDraft((d) => {
      const parsed = d.trim() === '' ? null : Number(d);
      return parsed === (value ?? null) ? d : value === null || value === undefined ? '' : String(value);
    });
  }, [value]);

  const parse = (s: string): number | null => {
    if (s.trim() === '' || s === '-') return null;
    const n = integer ? Number.parseInt(s, 10) : Number.parseFloat(s);
    return Number.isFinite(n) ? n : null;
  };

  return (
    <div className="relative flex w-full items-center">
      <input
        type="number"
        inputMode={integer ? 'numeric' : 'decimal'}
        value={draft}
        min={min}
        max={max}
        step={step}
        aria-invalid={invalid || undefined}
        className={inputClasses({
          invalid,
          className: clsx(
            '[appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none',
            unit && 'pr-20',
            className,
          ),
        })}
        onChange={(e) => {
          const s = e.target.value;
          setDraft(s);
          const n = parse(s);
          if (n !== null) onChange(n);
          else if (allowEmpty && s.trim() === '') onChange(null);
        }}
        onBlur={(e) => {
          const n = parse(draft);
          if (n === null) {
            if (allowEmpty) {
              setDraft('');
              onChange(null);
            } else {
              setDraft(value === null || value === undefined ? '' : String(value));
            }
          } else {
            const c = clamp(n, min, max);
            setDraft(String(c));
            if (c !== value) onChange(c);
          }
          onBlur?.(e);
        }}
        {...rest}
      />
      {unit && (
        <span className="pointer-events-none absolute right-3 max-w-[4.5rem] truncate text-xs text-muted">
          {unit}
        </span>
      )}
    </div>
  );
}
