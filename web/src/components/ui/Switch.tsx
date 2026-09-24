import { clsx } from 'clsx';
import { useId, type ReactNode } from 'react';

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** Visible label to the right of the switch. */
  label?: ReactNode;
  /** Secondary text under the label. */
  description?: ReactNode;
  disabled?: boolean;
  id?: string;
  /** Required when there is no visible label. */
  'aria-label'?: string;
  size?: 'sm' | 'md';
  className?: string;
}

/**
 * On/off toggle (role="switch").
 * @example <Switch checked={s.dryRun} onChange={(v) => set('dryRun', v)} label="Dry run" />
 */
export function Switch({
  checked,
  onChange,
  label,
  description,
  disabled,
  id,
  size = 'md',
  className,
  ...aria
}: SwitchProps) {
  const autoId = useId();
  const switchId = id ?? autoId;
  const track = size === 'sm' ? 'h-4 w-7' : 'h-5 w-9';
  const thumb = size === 'sm' ? 'size-3 data-[on=true]:translate-x-3' : 'size-4 data-[on=true]:translate-x-4';

  const button = (
    <button
      id={switchId}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={aria['aria-label']}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={clsx(
        'relative inline-flex shrink-0 cursor-pointer items-center rounded-full border p-px transition-colors duration-150',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'border-accent bg-accent' : 'border-input-border bg-input',
        track,
      )}
    >
      <span
        data-on={checked}
        className={clsx(
          'inline-block rounded-full shadow transition-transform duration-150',
          checked ? 'bg-white' : 'bg-muted',
          thumb,
        )}
      />
    </button>
  );

  if (!label && !description) return <span className={clsx('inline-flex', className)}>{button}</span>;

  return (
    <div className={clsx('inline-flex items-start gap-2.5', className)}>
      <span className="mt-0.5 inline-flex">{button}</span>
      <label htmlFor={switchId} className={clsx('flex cursor-pointer flex-col', disabled && 'cursor-not-allowed')}>
        {label && <span className="text-fg">{label}</span>}
        {description && <span className="text-xs text-muted">{description}</span>}
      </label>
    </div>
  );
}
