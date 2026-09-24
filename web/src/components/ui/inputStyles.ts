import { clsx } from 'clsx';

/** Shared look of text-like inputs (TextInput, NumberInput, Select, textarea). */
export function inputClasses(opts: { invalid?: boolean; className?: string } = {}): string {
  return clsx(
    'h-9 w-full min-w-0 rounded border bg-input px-3 text-sm text-fg-strong placeholder:text-subtle',
    'transition-[border-color,box-shadow] duration-100 outline-none',
    'focus:border-accent focus:ring-2 focus:ring-accent/25',
    'disabled:cursor-not-allowed disabled:opacity-60 read-only:bg-card-alt',
    opts.invalid ? 'border-danger focus:border-danger focus:ring-danger/25' : 'border-input-border',
    opts.className,
  );
}
