import { clsx } from 'clsx';
import type { LucideIcon } from 'lucide-react';
import type { ComponentProps } from 'react';
import { Spinner } from './Spinner';

export type IconButtonVariant = 'ghost' | 'default' | 'danger' | 'primary';
export type IconButtonSize = 'xs' | 'sm' | 'md' | 'lg';

const VARIANTS: Record<IconButtonVariant, string> = {
  ghost: 'text-muted hover:text-fg-strong hover:bg-row-hover',
  default: 'text-fg bg-card border border-border-strong hover:bg-card-hover',
  danger: 'text-muted hover:text-danger hover:bg-danger/10',
  primary: 'text-accent-soft hover:text-accent-fg hover:bg-accent',
};

const SIZES: Record<IconButtonSize, { box: string; icon: number }> = {
  xs: { box: 'size-6', icon: 13 },
  sm: { box: 'size-7', icon: 15 },
  md: { box: 'size-8', icon: 17 },
  lg: { box: 'size-10', icon: 20 },
};

export interface IconButtonProps extends Omit<ComponentProps<'button'>, 'children'> {
  icon: LucideIcon;
  /** Required accessible name; also used as the tooltip (title). */
  label: string;
  variant?: IconButtonVariant;
  size?: IconButtonSize;
  loading?: boolean;
  /** Rotate the icon continuously (e.g. refresh while fetching). */
  spinning?: boolean;
}

/**
 * Square icon-only button.
 * @example <IconButton icon={Trash} label="Delete" variant="danger" onClick={…} />
 */
export function IconButton({
  icon: Icon,
  label,
  variant = 'ghost',
  size = 'md',
  loading = false,
  spinning = false,
  disabled,
  className,
  type = 'button',
  title,
  ...rest
}: IconButtonProps) {
  const s = SIZES[size];
  return (
    <button
      type={type}
      aria-label={label}
      title={title ?? label}
      disabled={disabled || loading}
      className={clsx(
        'inline-flex shrink-0 items-center justify-center rounded transition-colors duration-100',
        'disabled:cursor-not-allowed disabled:opacity-50',
        VARIANTS[variant],
        s.box,
        className,
      )}
      {...rest}
    >
      {loading ? (
        <Spinner size={s.icon} className="text-current" />
      ) : (
        <Icon width={s.icon} height={s.icon} aria-hidden className={clsx(spinning && 'animate-spin')} />
      )}
    </button>
  );
}
