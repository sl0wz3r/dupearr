import { clsx } from 'clsx';
import { CircleAlert } from 'lucide-react';
import { useId, type ReactNode } from 'react';

export interface FieldProps {
  /** Visible label (linked to the control via `htmlFor`). */
  label: ReactNode;
  /** Render prop receiving the generated control id. */
  children: (id: string) => ReactNode;
  help?: ReactNode;
  error?: string | null;
  className?: string;
}

/**
 * Compact stacked label + control used inside criterion / protection rows, where the two-column
 * FormGroup would be too wide.
 */
export function Field({ label, children, help, error, className }: FieldProps) {
  const id = useId();
  return (
    <div className={clsx('flex min-w-0 flex-col gap-1', className)}>
      <label htmlFor={id} className="text-xs font-semibold text-muted">
        {label}
      </label>
      {children(id)}
      {help && <div className="text-xs leading-snug text-muted">{help}</div>}
      {error && <FieldError message={error} />}
    </div>
  );
}

/** Red inline error line (role="alert"). */
export function FieldError({ message, className }: { message: string; className?: string }) {
  return (
    <div role="alert" className={clsx('flex items-start gap-1.5 text-xs leading-snug text-danger', className)}>
      <CircleAlert aria-hidden width={13} height={13} className="mt-px shrink-0" />
      <span>{message}</span>
    </div>
  );
}

/** Stack of FieldErrors (renders nothing for an empty list). */
export function FieldErrors({ messages, className }: { messages?: readonly string[] | null; className?: string }) {
  if (!messages || messages.length === 0) return null;
  return (
    <div className={clsx('flex flex-col gap-1', className)}>
      {messages.map((m, i) => (
        <FieldError key={i} message={m} />
      ))}
    </div>
  );
}
