import { clsx } from 'clsx';
import type { ComponentProps, ReactNode } from 'react';
import { inputClasses } from './inputStyles';

export interface TextInputProps extends Omit<ComponentProps<'input'>, 'prefix'> {
  /** Red border + aria-invalid. */
  invalid?: boolean;
  /** Content rendered inside the field on the left (icon/text). */
  prefix?: ReactNode;
  /** Content rendered inside the field on the right (icon/button/unit). */
  suffix?: ReactNode;
  /** Class for the outer wrapper when prefix/suffix are used. */
  wrapperClassName?: string;
}

/**
 * Text input (all native props supported, incl. `ref`).
 * @example <TextInput value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://plex:32400" />
 */
export function TextInput({
  invalid,
  prefix,
  suffix,
  className,
  wrapperClassName,
  type = 'text',
  ...rest
}: TextInputProps) {
  if (!prefix && !suffix) {
    return (
      <input type={type} aria-invalid={invalid || undefined} className={inputClasses({ invalid, className })} {...rest} />
    );
  }
  return (
    <div className={clsx('relative flex w-full items-center', wrapperClassName)}>
      {prefix && (
        <span className="pointer-events-none absolute left-2.5 flex items-center text-muted">{prefix}</span>
      )}
      <input
        type={type}
        aria-invalid={invalid || undefined}
        className={inputClasses({ invalid, className: clsx(prefix && 'pl-8', suffix && 'pr-10', className) })}
        {...rest}
      />
      {suffix && <span className="absolute right-1 flex items-center text-muted">{suffix}</span>}
    </div>
  );
}

export interface TextAreaProps extends ComponentProps<'textarea'> {
  invalid?: boolean;
}

/** Multi-line text input. */
export function TextArea({ invalid, className, rows = 4, ...rest }: TextAreaProps) {
  return (
    <textarea
      rows={rows}
      aria-invalid={invalid || undefined}
      className={inputClasses({ invalid, className: clsx('h-auto py-2 leading-normal', className) })}
      {...rest}
    />
  );
}
