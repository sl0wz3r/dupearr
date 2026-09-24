import { clsx } from 'clsx';
import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from 'lucide-react';
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';

export type ToastKind = 'info' | 'success' | 'warning' | 'error';

export interface ToastOptions {
  kind?: ToastKind;
  title: ReactNode;
  message?: ReactNode;
  /** ms before auto-dismiss (default 5000; errors 8000; 0 = sticky). */
  duration?: number;
}

interface ToastItem extends ToastOptions {
  id: number;
}

interface ToastContextValue {
  show: (toast: ToastOptions) => number;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

const ICONS = { info: Info, success: CircleCheck, warning: TriangleAlert, error: CircleAlert } as const;
const ICON_COLOR = { info: 'text-info', success: 'text-success', warning: 'text-warning', error: 'text-danger' } as const;
const BORDER = {
  info: 'border-l-info',
  success: 'border-l-success',
  warning: 'border-l-warning',
  error: 'border-l-danger',
} as const;

/** Mount once near the root (App does). */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), []);

  const show = useCallback(
    (toast: ToastOptions) => {
      const id = nextId.current++;
      setToasts((t) => [...t.slice(-4), { ...toast, id }]);
      const duration = toast.duration ?? (toast.kind === 'error' ? 8000 : 5000);
      if (duration > 0) setTimeout(() => dismiss(id), duration);
      return id;
    },
    [dismiss],
  );

  const value = useMemo(() => ({ show, dismiss }), [show, dismiss]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        aria-live="polite"
        className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2"
      >
        {toasts.map((t) => {
          const kind = t.kind ?? 'info';
          const Icon = ICONS[kind];
          return (
            <div
              key={t.id}
              role={kind === 'error' ? 'alert' : 'status'}
              className={clsx(
                'pointer-events-auto flex items-start gap-3 rounded border border-l-4 border-border bg-card px-4 py-3 shadow-popover',
                BORDER[kind],
              )}
            >
              <Icon aria-hidden width={18} height={18} className={clsx('mt-0.5 shrink-0', ICON_COLOR[kind])} />
              <div className="min-w-0 flex-1">
                <div className="font-semibold text-fg-strong">{t.title}</div>
                {t.message && <div className="mt-0.5 text-sm break-words text-fg">{t.message}</div>}
              </div>
              <button
                type="button"
                aria-label="Dismiss"
                onClick={() => dismiss(t.id)}
                className="-mr-1 shrink-0 rounded p-0.5 text-muted hover:text-fg-strong"
              >
                <X width={16} height={16} aria-hidden />
              </button>
            </div>
          );
        })}
      </div>
    </ToastContext.Provider>
  );
}

/**
 * Transient notifications.
 * @example const toast = useToast(); toast.success('Saved'); toast.error('Test failed', errorMessage(e));
 */
export function useToast() {
  const ctx = useContext(ToastContext);
  return useMemo(() => {
    const show = ctx?.show ?? (() => 0);
    return {
      show,
      dismiss: ctx?.dismiss ?? (() => {}),
      info: (title: ReactNode, message?: ReactNode) => show({ kind: 'info', title, message }),
      success: (title: ReactNode, message?: ReactNode) => show({ kind: 'success', title, message }),
      warning: (title: ReactNode, message?: ReactNode) => show({ kind: 'warning', title, message }),
      error: (title: ReactNode, message?: ReactNode) => show({ kind: 'error', title, message }),
    };
  }, [ctx]);
}
