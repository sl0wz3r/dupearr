import { clsx } from 'clsx';
import { LoaderCircle } from 'lucide-react';

export type SpinnerSize = 'xs' | 'sm' | 'md' | 'lg' | 'xl';

const SIZES: Record<SpinnerSize, number> = { xs: 12, sm: 16, md: 20, lg: 32, xl: 48 };

export interface SpinnerProps {
  size?: SpinnerSize | number;
  className?: string;
  /** Accessible label (default "Loading"). */
  label?: string;
}

/** Spinning loader icon. */
export function Spinner({ size = 'md', className, label = 'Loading' }: SpinnerProps) {
  const px = typeof size === 'number' ? size : SIZES[size];
  return (
    <LoaderCircle
      role="status"
      aria-label={label}
      width={px}
      height={px}
      className={clsx('animate-spin text-accent', className)}
    />
  );
}

export interface LoadingIndicatorProps {
  /** Optional text under the spinner. */
  message?: string;
  className?: string;
  /** Fill the available height (page-level loading). */
  fill?: boolean;
}

/** Centered spinner block for loading pages/sections. */
export function LoadingIndicator({ message, className, fill = false }: LoadingIndicatorProps) {
  return (
    <div
      className={clsx(
        'flex flex-col items-center justify-center gap-3 py-12 text-muted',
        fill && 'h-full min-h-60',
        className,
      )}
    >
      <Spinner size="lg" />
      {message && <div className="text-sm">{message}</div>}
    </div>
  );
}
