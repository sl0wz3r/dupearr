import { clsx } from 'clsx';
import { useId, type ReactNode } from 'react';

export type TooltipSide = 'top' | 'bottom' | 'left' | 'right';

const SIDES: Record<TooltipSide, string> = {
  top: 'bottom-full left-1/2 mb-1.5 -translate-x-1/2',
  bottom: 'top-full left-1/2 mt-1.5 -translate-x-1/2',
  left: 'right-full top-1/2 mr-1.5 -translate-y-1/2',
  right: 'left-full top-1/2 ml-1.5 -translate-y-1/2',
};

export interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
  side?: TooltipSide;
  /** Max width utility (default "max-w-xs"). */
  maxWidthClass?: string;
  className?: string;
  /** Render nothing extra when false/empty content. */
  disabled?: boolean;
}

/**
 * Lightweight CSS tooltip shown on hover and keyboard focus (wraps children in an inline span).
 * @example <Tooltip content="Decided by resolution"><Info width={14} /></Tooltip>
 */
export function Tooltip({ content, children, side = 'top', maxWidthClass = 'max-w-xs', className, disabled }: TooltipProps) {
  const id = useId();
  if (disabled || content === null || content === undefined || content === '') return <>{children}</>;
  return (
    <span className={clsx('group/tooltip relative inline-flex', className)} aria-describedby={id}>
      {children}
      <span
        id={id}
        role="tooltip"
        className={clsx(
          'pointer-events-none invisible absolute z-40 w-max rounded border border-border-strong bg-header px-2.5 py-1.5',
          'text-xs leading-snug font-normal text-header-fg opacity-0 shadow-popover transition-opacity duration-100',
          'group-focus-within/tooltip:visible group-focus-within/tooltip:opacity-100 group-hover/tooltip:visible group-hover/tooltip:opacity-100',
          'whitespace-normal',
          maxWidthClass,
          SIDES[side],
        )}
      >
        {content}
      </span>
    </span>
  );
}
