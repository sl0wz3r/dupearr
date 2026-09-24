import { clsx } from 'clsx';
import { CircleAlert, CircleCheck, Info, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';
import { Button } from './Button';

export type AlertKind = 'info' | 'warning' | 'error' | 'success';

const STYLES: Record<AlertKind, { box: string; icon: LucideIcon; iconClass: string }> = {
  info: { box: 'border-info/50 bg-info/10', icon: Info, iconClass: 'text-info' },
  warning: { box: 'border-warning/50 bg-warning/10', icon: TriangleAlert, iconClass: 'text-warning' },
  error: { box: 'border-danger/50 bg-danger/10', icon: CircleAlert, iconClass: 'text-danger' },
  success: { box: 'border-success/50 bg-success/10', icon: CircleCheck, iconClass: 'text-success' },
};

export interface AlertProps {
  kind?: AlertKind;
  title?: ReactNode;
  children?: ReactNode;
  /** Right-aligned actions (buttons/links). */
  actions?: ReactNode;
  /** Shows a × button. */
  onDismiss?: () => void;
  className?: string;
}

/**
 * Inline message box.
 * @example <Alert kind="warning" title="Dry run">Nothing will be deleted.</Alert>
 */
export function Alert({ kind = 'info', title, children, actions, onDismiss, className }: AlertProps) {
  const s = STYLES[kind];
  const Icon = s.icon;
  return (
    <div
      role={kind === 'error' || kind === 'warning' ? 'alert' : 'status'}
      className={clsx('flex items-start gap-3 rounded border px-4 py-3 text-fg', s.box, className)}
    >
      <Icon aria-hidden width={18} height={18} className={clsx('mt-0.5 shrink-0', s.iconClass)} />
      <div className="min-w-0 flex-1">
        {title && <div className="font-semibold text-fg-strong">{title}</div>}
        {children && <div className={clsx('text-sm', title && 'mt-0.5')}>{children}</div>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      {onDismiss && (
        <button
          type="button"
          aria-label="Dismiss"
          onClick={onDismiss}
          className="-mr-1 shrink-0 rounded p-0.5 text-muted hover:text-fg-strong"
        >
          <X width={16} height={16} aria-hidden />
        </button>
      )}
    </div>
  );
}

export interface LoadErrorAlertProps {
  /** e.g. "Unable to load backups". */
  title: ReactNode;
  /** The error message (e.g. `errorMessage(query.error)`). */
  message?: ReactNode;
  /** Shows a Retry button (e.g. `() => void query.refetch()`). */
  onRetry?: () => void;
  /** Retry in progress (spinner on the button). */
  retrying?: boolean;
  className?: string;
}

/**
 * The standard "could not load" state of a page or panel: an error alert with a Retry button.
 * When nothing was loaded, render it instead of the (empty) list so the page never claims there
 * is nothing to show.
 */
export function LoadErrorAlert({ title, message, onRetry, retrying = false, className }: LoadErrorAlertProps) {
  return (
    <Alert
      kind="error"
      title={title}
      className={className}
      actions={
        onRetry ? (
          <Button size="sm" loading={retrying} onClick={onRetry}>
            Retry
          </Button>
        ) : undefined
      }
    >
      {message}
    </Alert>
  );
}
