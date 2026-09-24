import { clsx } from 'clsx';
import { X } from 'lucide-react';
import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { IconButton } from './IconButton';

export type ModalSize = 'sm' | 'md' | 'lg' | 'xl' | 'full';

const SIZES: Record<ModalSize, string> = {
  sm: 'sm:max-w-md',
  md: 'sm:max-w-xl',
  lg: 'sm:max-w-3xl',
  xl: 'sm:max-w-5xl',
  full: 'sm:max-w-[min(96vw,1400px)]',
};

const FOCUSABLE =
  'a[href], area[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), iframe, [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.hasAttribute('disabled') && el.getAttribute('aria-hidden') !== 'true' && el.tabIndex !== -1,
  );
}

let openModals = 0;

export interface ModalProps {
  open: boolean;
  /** Called on Esc, backdrop click and the close button. */
  onClose: () => void;
  title: ReactNode;
  /** Body content. */
  children?: ReactNode;
  /** Footer (usually buttons, right-aligned). */
  footer?: ReactNode;
  /** Left side of the footer (e.g. a Delete button in edit modals). */
  footerStart?: ReactNode;
  size?: ModalSize;
  /** Close when clicking the backdrop (default true). */
  closeOnBackdrop?: boolean;
  /** Element to focus when opened (defaults to the first focusable element in the body). */
  initialFocusRef?: RefObject<HTMLElement | null>;
  /** Hide the × button. */
  hideCloseButton?: boolean;
  className?: string;
  bodyClassName?: string;
}

/**
 * Accessible modal dialog: portal, focus trap, Esc to close, focus restore, scroll lock.
 * @example
 * <Modal open={open} onClose={close} title="Edit Radarr" footer={<Button onClick={save}>Save</Button>}>…</Modal>
 */
export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  footerStart,
  size = 'md',
  closeOnBackdrop = true,
  initialFocusRef,
  hideCloseButton = false,
  className,
  bodyClassName,
}: ModalProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });

  // Focus management + scroll lock.
  useEffect(() => {
    if (!open) return;
    const previouslyFocused = document.activeElement as HTMLElement | null;
    openModals += 1;
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const frame = requestAnimationFrame(() => {
      const dialog = dialogRef.current;
      if (!dialog) return;
      const target =
        initialFocusRef?.current ??
        dialog.querySelector<HTMLElement>('[data-autofocus]') ??
        focusableIn(dialog.querySelector('[data-modal-body]') ?? dialog)[0] ??
        dialog;
      target.focus();
    });

    return () => {
      cancelAnimationFrame(frame);
      openModals -= 1;
      if (openModals <= 0) document.body.style.overflow = prevOverflow;
      if (previouslyFocused && typeof previouslyFocused.focus === 'function') previouslyFocused.focus();
    };
  }, [open, initialFocusRef]);

  if (!open) return null;

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onCloseRef.current();
      return;
    }
    if (e.key !== 'Tab' || !dialogRef.current) return;
    e.stopPropagation(); // nested modals: only the innermost traps focus
    const items = focusableIn(dialogRef.current);
    if (items.length === 0) {
      e.preventDefault();
      dialogRef.current.focus();
      return;
    }
    const first = items[0]!;
    const last = items[items.length - 1]!;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === dialogRef.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-end justify-center bg-overlay p-0 sm:items-start sm:p-6 sm:pt-[8vh]"
      onMouseDown={(e) => {
        if (closeOnBackdrop && e.target === e.currentTarget) onCloseRef.current();
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        className={clsx(
          'flex max-h-[92dvh] w-full flex-col overflow-hidden rounded-t-lg border border-border bg-card shadow-popover outline-none',
          'sm:max-h-[84dvh] sm:rounded-lg',
          SIZES[size],
          className,
        )}
      >
        <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border px-5 py-3.5">
          <h2 id={titleId} className="m-0 truncate text-lg font-normal text-fg-strong">
            {title}
          </h2>
          {!hideCloseButton && <IconButton icon={X} label="Close" onClick={() => onCloseRef.current()} />}
        </div>
        <div data-modal-body className={clsx('min-h-0 flex-1 overflow-y-auto px-5 py-4', bodyClassName)}>
          {children}
        </div>
        {(footer || footerStart) && (
          <div className="flex shrink-0 flex-wrap items-center gap-2 border-t border-border bg-card-alt px-5 py-3">
            <div className="flex flex-1 items-center gap-2">{footerStart}</div>
            <div className="flex items-center gap-2">{footer}</div>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
