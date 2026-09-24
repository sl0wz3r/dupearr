import { clsx } from 'clsx';
import { Check, Minus } from 'lucide-react';
import { useEffect, useRef, type ChangeEvent, type ComponentProps, type ReactNode } from 'react';

export interface CheckboxProps extends Omit<ComponentProps<'input'>, 'type' | 'onChange' | 'checked' | 'size' | 'ref'> {
  checked: boolean;
  /** Tri-state "some selected" look (e.g. table header). */
  indeterminate?: boolean;
  onChange: (checked: boolean, event: ChangeEvent<HTMLInputElement>) => void;
  /** Optional inline label to the right. */
  label?: ReactNode;
  /** Secondary text under the label. */
  description?: ReactNode;
}

/**
 * Styled native checkbox (keyboard + screen-reader friendly).
 * @example <Checkbox checked={rememberMe} onChange={setRememberMe} label="Remember me" />
 */
export function Checkbox({
  checked,
  indeterminate = false,
  onChange,
  label,
  description,
  className,
  disabled,
  id,
  ...rest
}: CheckboxProps) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate;
  }, [indeterminate]);

  const on = checked || indeterminate;
  const box = (
    <span className="relative inline-flex size-4 shrink-0 items-center justify-center">
      <input
        ref={ref}
        id={id}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked, e)}
        className="peer absolute inset-0 m-0 cursor-pointer appearance-none rounded-sm opacity-0 disabled:cursor-not-allowed"
        {...rest}
      />
      <span
        aria-hidden
        className={clsx(
          'pointer-events-none flex size-4 items-center justify-center rounded-sm border transition-colors',
          on ? 'border-accent bg-accent text-accent-fg' : 'border-input-border bg-input',
          'peer-focus-visible:outline-2 peer-focus-visible:outline-offset-1 peer-focus-visible:outline-accent',
          disabled && 'opacity-50',
        )}
      >
        {indeterminate ? (
          <Minus width={12} height={12} strokeWidth={3} />
        ) : checked ? (
          <Check width={12} height={12} strokeWidth={3} />
        ) : null}
      </span>
    </span>
  );

  if (!label) return <span className={clsx('inline-flex', className)}>{box}</span>;

  return (
    <label
      className={clsx(
        'inline-flex cursor-pointer items-start gap-2 select-none',
        disabled && 'cursor-not-allowed opacity-70',
        className,
      )}
    >
      <span className="mt-0.5">{box}</span>
      <span className="flex flex-col">
        <span className="text-fg">{label}</span>
        {description && <span className="text-xs text-muted">{description}</span>}
      </span>
    </label>
  );
}
