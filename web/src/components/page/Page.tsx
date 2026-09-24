import { clsx } from 'clsx';
import type { LucideIcon } from 'lucide-react';
import { useEffect, type ReactNode } from 'react';
import { Link } from 'react-router';
import { useAppInfo } from '@/app/AppInfo';
import { Spinner } from '@/components/ui/Spinner';

// ---------------------------------------------------------------------------
// PageContent — outer wrapper of every routed page
// ---------------------------------------------------------------------------

export interface PageContentProps {
  /** Document title ("<title> - <instanceName>"). */
  title: string;
  children: ReactNode;
  className?: string;
}

/**
 * Root of a page: sets the document title and lays out `PageToolbar` (fixed) + `PageBody` (scrolls).
 * @example
 * <PageContent title="Queue">
 *   <PageToolbar>…</PageToolbar>
 *   <PageBody><PageHeader title="Queue" />…</PageBody>
 * </PageContent>
 */
export function PageContent({ title, children, className }: PageContentProps) {
  const info = useAppInfo();
  const instanceName = info?.instanceName || 'Dupearr';
  useEffect(() => {
    document.title = title ? `${title} - ${instanceName}` : instanceName;
  }, [title, instanceName]);
  return <div className={clsx('flex h-full min-h-0 flex-col', className)}>{children}</div>;
}

/** Scrollable content area under the toolbar. */
export function PageBody({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={clsx('min-h-0 flex-1 overflow-y-auto', className)}>
      <div className="mx-auto w-full max-w-[1800px] px-4 py-5 sm:px-5">{children}</div>
    </div>
  );
}

export interface PageHeaderProps {
  title: ReactNode;
  subtitle?: ReactNode;
  /** Right-aligned content (badges, secondary actions). */
  actions?: ReactNode;
  className?: string;
}

/** Page heading inside PageBody. */
export function PageHeader({ title, subtitle, actions, className }: PageHeaderProps) {
  return (
    <div className={clsx('mb-5 flex flex-wrap items-end justify-between gap-3', className)}>
      <div className="min-w-0">
        <h1 className="m-0 truncate text-2xl font-light text-fg-strong">{title}</h1>
        {subtitle && <div className="mt-1 text-sm text-muted">{subtitle}</div>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Toolbar
// ---------------------------------------------------------------------------

/** *arr page toolbar (icon-over-label buttons). Put `PageToolbarSection`s inside. */
export function PageToolbar({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      role="toolbar"
      className={clsx(
        'scrollbar-none flex h-[60px] shrink-0 items-stretch justify-between gap-2 overflow-x-auto border-b border-border bg-toolbar px-2 text-toolbar-fg sm:px-3',
        className,
      )}
    >
      {children}
    </div>
  );
}

export interface PageToolbarSectionProps {
  align?: 'left' | 'right';
  children?: ReactNode;
  className?: string;
}

export function PageToolbarSection({ align = 'left', children, className }: PageToolbarSectionProps) {
  return (
    <div className={clsx('flex items-stretch', align === 'right' && 'ml-auto justify-end', className)}>
      {children}
    </div>
  );
}

/** Vertical divider between toolbar button groups. */
export function ToolbarSeparator() {
  return <div aria-hidden className="mx-1.5 my-3 w-px shrink-0 bg-border-strong/70 sm:mx-2.5" />;
}

export interface ToolbarButtonProps {
  icon: LucideIcon;
  label: string;
  onClick?: () => void;
  /** Render as a router link instead of a button. */
  to?: string;
  /** Spinner instead of the icon (and disabled). */
  loading?: boolean;
  /** Spin the icon (e.g. "Scanning…") without disabling. */
  spinning?: boolean;
  disabled?: boolean;
  /** Highlighted (toggled on / active filter). */
  active?: boolean;
  /** Icon colour tint. */
  kind?: 'default' | 'danger' | 'success' | 'warning';
  title?: string;
}

const KIND_ICON = {
  default: '',
  danger: 'text-danger',
  success: 'text-success',
  warning: 'text-warning',
} as const;

/**
 * *arr toolbar button: icon above a small label.
 * @example <ToolbarButton icon={RefreshCw} label="Scan Now" spinning={scanning} onClick={scan} />
 */
export function ToolbarButton({
  icon: Icon,
  label,
  onClick,
  to,
  loading = false,
  spinning = false,
  disabled = false,
  active = false,
  kind = 'default',
  title,
}: ToolbarButtonProps) {
  const cls = clsx(
    'flex min-w-[56px] shrink-0 flex-col items-center justify-center gap-1 rounded px-1.5 no-underline transition-colors sm:min-w-[64px] sm:px-2',
    'hover:text-accent-soft disabled:cursor-not-allowed disabled:opacity-45 disabled:hover:text-toolbar-fg',
    active ? 'text-accent-soft' : 'text-toolbar-fg',
  );
  const content = (
    <>
      {loading ? (
        <Spinner size={20} className="text-current" />
      ) : (
        <Icon aria-hidden width={20} height={20} className={clsx(KIND_ICON[kind], spinning && 'animate-spin')} />
      )}
      <span className="max-w-[88px] truncate text-[11px] leading-tight">{label}</span>
    </>
  );
  if (to && !disabled) {
    return (
      <Link to={to} className={cls} title={title ?? label} aria-label={label}>
        {content}
      </Link>
    );
  }
  return (
    <button
      type="button"
      className={cls}
      onClick={onClick}
      disabled={disabled || loading}
      title={title ?? label}
      aria-label={label}
      aria-pressed={active || undefined}
    >
      {content}
    </button>
  );
}
