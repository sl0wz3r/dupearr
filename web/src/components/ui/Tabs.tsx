import { clsx } from 'clsx';
import type { LucideIcon } from 'lucide-react';
import { useId, useRef, type KeyboardEvent, type ReactNode } from 'react';

export interface TabItem<T extends string = string> {
  id: T;
  label: ReactNode;
  icon?: LucideIcon;
  /** Small count/badge after the label. */
  badge?: ReactNode;
  disabled?: boolean;
}

export interface TabsProps<T extends string = string> {
  tabs: readonly TabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  className?: string;
  /** Accessible name of the tab list. */
  'aria-label'?: string;
}

/** DOM id of a tab panel, for `<TabPanel>` / aria-controls wiring. */
function panelId(base: string, id: string) {
  return `${base}-panel-${id}`;
}

/**
 * Controlled tab strip (arrow keys / Home / End). Render the active content yourself, optionally
 * inside `<TabPanel>`.
 * @example
 * <Tabs tabs={[{id:'info',label:'Info'},{id:'files',label:'Files'}]} value={tab} onChange={setTab} />
 */
export function Tabs<T extends string = string>({ tabs, value, onChange, className, ...aria }: TabsProps<T>) {
  const base = useId();
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const enabled = tabs.filter((t) => !t.disabled);
    const idx = enabled.findIndex((t) => t.id === value);
    let next: TabItem<T> | undefined;
    if (e.key === 'ArrowRight') next = enabled[(idx + 1) % enabled.length];
    else if (e.key === 'ArrowLeft') next = enabled[(idx - 1 + enabled.length) % enabled.length];
    else if (e.key === 'Home') next = enabled[0];
    else if (e.key === 'End') next = enabled[enabled.length - 1];
    if (next) {
      e.preventDefault();
      onChange(next.id);
      refs.current[next.id]?.focus();
    }
  };

  return (
    <div
      role="tablist"
      aria-label={aria['aria-label']}
      onKeyDown={onKeyDown}
      className={clsx('scrollbar-none flex gap-1 overflow-x-auto border-b border-border', className)}
    >
      {tabs.map((t) => {
        const active = t.id === value;
        const Icon = t.icon;
        return (
          <button
            key={t.id}
            ref={(el) => {
              refs.current[t.id] = el;
            }}
            type="button"
            role="tab"
            id={`${base}-tab-${t.id}`}
            aria-selected={active}
            aria-controls={panelId(base, t.id)}
            tabIndex={active ? 0 : -1}
            disabled={t.disabled}
            onClick={() => onChange(t.id)}
            className={clsx(
              '-mb-px inline-flex items-center gap-2 border-b-2 px-3.5 py-2 text-sm whitespace-nowrap transition-colors',
              'disabled:cursor-not-allowed disabled:opacity-50',
              active
                ? 'border-accent text-fg-strong'
                : 'border-transparent text-muted hover:border-border-strong hover:text-fg',
            )}
          >
            {Icon && <Icon aria-hidden width={15} height={15} />}
            {t.label}
            {t.badge !== undefined && t.badge !== null && (
              <span className="rounded-full bg-card-hover px-1.5 text-[11px] leading-4 text-muted">{t.badge}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

export interface TabPanelProps {
  children: ReactNode;
  className?: string;
}

/** Wrapper for the active tab's content. */
export function TabPanel({ children, className }: TabPanelProps) {
  return (
    <div role="tabpanel" className={clsx('pt-4', className)}>
      {children}
    </div>
  );
}
