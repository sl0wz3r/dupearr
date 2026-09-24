import { clsx } from 'clsx';
import { Inbox, type LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';

export interface EmptyStateProps {
  icon?: LucideIcon;
  title: ReactNode;
  description?: ReactNode;
  /** Call to action (buttons/links). */
  action?: ReactNode;
  className?: string;
  /** Smaller padding for use inside cards/tables. */
  compact?: boolean;
}

/**
 * Centered "nothing here" message.
 * @example <EmptyState icon={Copy} title="No duplicates" description="Run a scan" action={<Button>Scan Now</Button>} />
 */
export function EmptyState({ icon: Icon = Inbox, title, description, action, className, compact }: EmptyStateProps) {
  return (
    <div
      className={clsx(
        'flex flex-col items-center justify-center text-center',
        compact ? 'gap-2 px-4 py-8' : 'gap-3 px-6 py-16',
        className,
      )}
    >
      <div className="flex size-14 items-center justify-center rounded-full bg-card-hover text-muted">
        <Icon aria-hidden width={28} height={28} />
      </div>
      <div className="text-lg text-fg-strong">{title}</div>
      {description && <div className="max-w-lg text-sm text-muted">{description}</div>}
      {action && <div className="mt-2 flex flex-wrap items-center justify-center gap-2">{action}</div>}
    </div>
  );
}
