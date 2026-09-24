import { clsx } from 'clsx';
import type { LucideIcon } from 'lucide-react';
import type { ComponentProps, ReactNode } from 'react';
import { Link, type LinkProps } from 'react-router';
import { Spinner } from './Spinner';

export type ButtonVariant = 'primary' | 'default' | 'danger' | 'success' | 'warning' | 'ghost';
export type ButtonSize = 'sm' | 'md' | 'lg';

const VARIANTS: Record<ButtonVariant, string> = {
  primary: 'bg-accent text-accent-fg border-accent hover:bg-accent-hover hover:border-accent-hover',
  default: 'bg-card text-fg border-border-strong hover:bg-card-hover hover:text-fg-strong',
  danger: 'bg-danger text-white border-danger hover:brightness-110',
  success: 'bg-success text-white border-success hover:brightness-110',
  warning: 'bg-warning text-black border-warning hover:brightness-110',
  ghost: 'bg-transparent text-fg border-transparent hover:bg-row-hover hover:text-fg-strong',
};

const SIZES: Record<ButtonSize, string> = {
  sm: 'h-7 px-2.5 text-xs gap-1.5',
  md: 'h-9 px-3.5 text-sm gap-2',
  lg: 'h-11 px-5 text-base gap-2',
};

const ICON_SIZES: Record<ButtonSize, number> = { sm: 14, md: 16, lg: 18 };

/** Shared class builder (also used by LinkButton). */
export function buttonClasses({
  variant = 'default',
  size = 'md',
  fullWidth = false,
  className,
}: {
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  className?: string;
}): string {
  return clsx(
    'inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded border font-medium',
    'transition-[background-color,border-color,color,filter] duration-100 select-none no-underline',
    'disabled:cursor-not-allowed disabled:opacity-55 aria-disabled:cursor-not-allowed aria-disabled:opacity-55',
    VARIANTS[variant],
    SIZES[size],
    fullWidth && 'w-full',
    className,
  );
}

export interface ButtonProps extends ComponentProps<'button'> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Shows a spinner and disables the button. */
  loading?: boolean;
  /** Leading icon (a lucide icon component). */
  icon?: LucideIcon;
  /** Trailing content (e.g. a count badge). */
  trailing?: ReactNode;
  fullWidth?: boolean;
}

/**
 * Button. Defaults to `type="button"` (pass `type="submit"` in forms).
 * @example <Button variant="primary" icon={Save} loading={saving} onClick={save}>Save</Button>
 */
export function Button({
  variant = 'default',
  size = 'md',
  loading = false,
  icon: Icon,
  trailing,
  fullWidth,
  disabled,
  className,
  children,
  type = 'button',
  ...rest
}: ButtonProps) {
  const iconSize = ICON_SIZES[size];
  return (
    <button
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={buttonClasses({ variant, size, fullWidth, className })}
      {...rest}
    >
      {loading ? (
        <Spinner size={iconSize} className="text-current" label="Working" />
      ) : (
        Icon && <Icon width={iconSize} height={iconSize} aria-hidden />
      )}
      {children}
      {trailing}
    </button>
  );
}

export interface LinkButtonProps extends LinkProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: LucideIcon;
  fullWidth?: boolean;
}

/** A react-router Link styled as a Button. */
export function LinkButton({
  variant = 'default',
  size = 'md',
  icon: Icon,
  fullWidth,
  className,
  children,
  ...rest
}: LinkButtonProps) {
  const iconSize = ICON_SIZES[size];
  return (
    <Link className={buttonClasses({ variant, size, fullWidth, className: className as string })} {...rest}>
      {Icon && <Icon width={iconSize} height={iconSize} aria-hidden />}
      {children}
    </Link>
  );
}
