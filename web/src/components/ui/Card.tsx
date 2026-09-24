import { clsx } from 'clsx';
import { Plus } from 'lucide-react';
import type { ReactNode } from 'react';

export interface CardProps {
  /** Header title. */
  title?: ReactNode;
  /** Header right side (buttons, badges). */
  actions?: ReactNode;
  children?: ReactNode;
  /** Makes the whole card clickable (renders a <button>-like div with keyboard support). */
  onClick?: () => void;
  /** Body padding (default true). */
  padded?: boolean;
  className?: string;
  bodyClassName?: string;
}

/**
 * Panel with an optional header. With `onClick` it becomes the *arr "connection card"
 * (Settings → Media Servers / Applications / Connect).
 */
export function Card({ title, actions, children, onClick, padded = true, className, bodyClassName }: CardProps) {
  const interactive = !!onClick;
  return (
    <div
      role={interactive ? 'button' : undefined}
      tabIndex={interactive ? 0 : undefined}
      onClick={onClick}
      onKeyDown={
        interactive
          ? (e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onClick?.();
              }
            }
          : undefined
      }
      className={clsx(
        'rounded border border-border bg-card shadow-sm',
        interactive && 'cursor-pointer transition-colors hover:border-border-strong hover:bg-card-hover',
        className,
      )}
    >
      {(title || actions) && (
        <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-2.5">
          <div className="min-w-0 truncate text-base text-fg-strong">{title}</div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={clsx(padded && 'p-4', bodyClassName)}>{children}</div>
    </div>
  );
}

export interface AddCardProps {
  onClick: () => void;
  /** Accessible label (default "Add"). */
  label?: string;
  className?: string;
}

/** Dashed "+" card placed after connection cards. */
export function AddCard({ onClick, label = 'Add', className }: AddCardProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={clsx(
        'flex min-h-24 items-center justify-center rounded border-2 border-dashed border-border-strong text-muted',
        'transition-colors hover:border-accent hover:text-accent',
        className,
      )}
    >
      <Plus aria-hidden width={32} height={32} />
    </button>
  );
}

/** Responsive grid for connection cards. */
export function CardGrid({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={clsx('grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4', className)}>
      {children}
    </div>
  );
}
