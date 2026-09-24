import { clsx } from 'clsx';
import { CircleAlert, TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';
import { useShowAdvanced } from '@/app/preferences';

export interface FormGroupProps {
  label: ReactNode;
  /** id of the control, links the <label>. */
  htmlFor?: string;
  /** Help text under the control. */
  helpText?: ReactNode;
  /** Warning text under the control (orange). */
  warning?: ReactNode;
  /** Validation error(s) (red). Pass `apiError.errorsFor('fieldName')`. */
  errors?: string | readonly string[] | null;
  /**
   * Advanced setting: hidden unless "Show Advanced" is on (or it has errors to show); the label is
   * tinted when shown.
   */
  advanced?: boolean;
  /** Extra content next to the label (e.g. an info link). */
  labelSuffix?: ReactNode;
  /** Control width: "md" (default, ~640px max) or "full". */
  width?: 'sm' | 'md' | 'full';
  children: ReactNode;
  className?: string;
}

const WIDTHS = { sm: 'max-w-xs', md: 'max-w-2xl', full: 'max-w-none' } as const;

/**
 * *arr-style form row: label on the left (stacked on mobile), control + help/warning/errors on the right.
 * @example
 * <FormGroup label="URL" htmlFor="url" helpText="Including port" errors={err?.errorsFor('url')}>
 *   <TextInput id="url" value={url} onChange={…} />
 * </FormGroup>
 */
export function FormGroup({
  label,
  htmlFor,
  helpText,
  warning,
  errors,
  advanced = false,
  labelSuffix,
  width = 'md',
  children,
  className,
}: FormGroupProps) {
  const [showAdvanced] = useShowAdvanced();
  const errorList = errors ? (typeof errors === 'string' ? [errors] : errors) : [];
  // An error on a hidden advanced field would be invisible: show the field while it has one.
  if (advanced && !showAdvanced && errorList.length === 0) return null;

  return (
    <div
      className={clsx(
        'grid grid-cols-1 gap-x-6 gap-y-1.5 py-2.5 md:grid-cols-[minmax(160px,240px)_1fr] md:items-start',
        className,
      )}
    >
      <div className="flex items-center gap-2 md:justify-end md:pt-2 md:text-right">
        <label
          htmlFor={htmlFor}
          className={clsx('font-semibold', advanced ? 'text-warning' : 'text-fg-strong')}
          title={advanced ? 'Advanced setting' : undefined}
        >
          {label}
        </label>
        {labelSuffix}
      </div>
      <div className={clsx('min-w-0', WIDTHS[width])}>
        {children}
        {helpText && <div className="mt-1.5 text-[13px] leading-snug text-muted">{helpText}</div>}
        {warning && (
          <div className="mt-1.5 flex items-start gap-1.5 text-[13px] leading-snug text-warning">
            <TriangleAlert aria-hidden width={14} height={14} className="mt-0.5 shrink-0" />
            <span>{warning}</span>
          </div>
        )}
        {errorList.map((e, i) => (
          <div
            key={i}
            role="alert"
            className="mt-1.5 flex items-start gap-1.5 text-[13px] leading-snug text-danger"
          >
            <CircleAlert aria-hidden width={14} height={14} className="mt-0.5 shrink-0" />
            <span>{e}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
