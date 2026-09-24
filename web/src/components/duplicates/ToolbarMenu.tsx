import { clsx } from 'clsx';
import { Check, type LucideIcon } from 'lucide-react';
import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { ToolbarButton } from '@/components/page';

export interface ToolbarMenuItem<V extends string = string> {
  value: V;
  label: string;
  description?: string;
}

export interface ToolbarMenuProps<V extends string = string> {
  icon: LucideIcon;
  label: string;
  items: readonly ToolbarMenuItem<V>[];
  /** Currently selected value (checked item), if any. */
  value?: V | null;
  onSelect: (value: V) => void;
  /** Highlights the toolbar button (e.g. a non-default filter). */
  active?: boolean;
  /** Accessible name of the menu (default = label). */
  menuLabel?: string;
}

interface Position {
  top: number;
  right: number;
}

/**
 * *arr-style toolbar dropdown (e.g. the Filter menu): a ToolbarButton opening a radio menu.
 * Rendered in a portal (the toolbar scrolls horizontally and would clip it); closes on Esc,
 * outside click, selection, resize and scroll. Arrow keys / Home / End move between items.
 */
export function ToolbarMenu<V extends string = string>({
  icon,
  label,
  items,
  value,
  onSelect,
  active,
  menuLabel,
}: ToolbarMenuProps<V>) {
  const [position, setPosition] = useState<Position | null>(null);
  const anchorRef = useRef<HTMLSpanElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const open = position !== null;

  const close = useCallback((restoreFocus: boolean) => {
    setPosition(null);
    if (restoreFocus) anchorRef.current?.querySelector<HTMLElement>('button, a')?.focus();
  }, []);

  const toggle = () => {
    if (open) {
      close(false);
      return;
    }
    const rect = anchorRef.current?.getBoundingClientRect();
    const viewportWidth = typeof window !== 'undefined' ? window.innerWidth : 0;
    setPosition(rect ? { top: rect.bottom + 4, right: Math.max(8, viewportWidth - rect.right) } : { top: 64, right: 8 });
  };

  // ToolbarButton doesn't take ARIA props: annotate its <button> so it is announced as a menu button.
  useEffect(() => {
    const button = anchorRef.current?.querySelector('button');
    if (!button) return;
    button.setAttribute('aria-haspopup', 'menu');
    button.setAttribute('aria-expanded', String(open));
    if (open) button.setAttribute('aria-controls', menuId);
    else button.removeAttribute('aria-controls');
  }, [open, menuId]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: MouseEvent) => {
      const target = e.target as Node | null;
      if (!target) return;
      if (menuRef.current?.contains(target) || anchorRef.current?.contains(target)) return;
      close(false);
    };
    const onDismiss = () => close(false);
    const onScroll = (e: Event) => {
      if (menuRef.current && e.target instanceof Node && menuRef.current.contains(e.target)) return;
      close(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    window.addEventListener('resize', onDismiss);
    window.addEventListener('scroll', onScroll, true);
    const frame = requestAnimationFrame(() => {
      const items = menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]');
      const checked = menuRef.current?.querySelector<HTMLElement>('[aria-checked="true"]');
      (checked ?? items?.[0])?.focus();
    });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener('mousedown', onPointerDown);
      window.removeEventListener('resize', onDismiss);
      window.removeEventListener('scroll', onScroll, true);
    };
  }, [open, close]);

  const onMenuKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const nodes = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitemradio"]') ?? []);
    const idx = nodes.indexOf(document.activeElement as HTMLElement);
    let next: HTMLElement | undefined;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    if (e.key === 'Tab') {
      close(false);
      return;
    }
    if (e.key === 'ArrowDown') next = nodes[(idx + 1) % nodes.length];
    else if (e.key === 'ArrowUp') next = nodes[(idx - 1 + nodes.length) % nodes.length];
    else if (e.key === 'Home') next = nodes[0];
    else if (e.key === 'End') next = nodes[nodes.length - 1];
    if (next) {
      e.preventDefault();
      next.focus();
    }
  };

  return (
    <>
      <span ref={anchorRef} className="flex">
        <ToolbarButton icon={icon} label={label} onClick={toggle} active={active} />
      </span>
      {open &&
        typeof document !== 'undefined' &&
        createPortal(
          <div
            ref={menuRef}
            id={menuId}
            role="menu"
            aria-label={menuLabel ?? label}
            onKeyDown={onMenuKeyDown}
            style={{ position: 'fixed', top: position.top, right: position.right }}
            className="z-50 max-h-[70dvh] min-w-52 overflow-y-auto rounded border border-border-strong bg-card py-1 shadow-popover"
          >
            {items.map((item) => {
              const checked = item.value === value;
              return (
                <button
                  key={item.value}
                  type="button"
                  role="menuitemradio"
                  aria-checked={checked}
                  tabIndex={-1}
                  onClick={() => {
                    onSelect(item.value);
                    close(true);
                  }}
                  className={clsx(
                    'flex w-full items-start gap-2 px-3 py-1.5 text-left text-sm outline-none',
                    'hover:bg-row-hover focus-visible:bg-row-hover',
                    checked ? 'text-accent-soft' : 'text-fg',
                  )}
                >
                  <Check aria-hidden width={15} height={15} className={clsx('mt-0.5 shrink-0', !checked && 'invisible')} />
                  <span className="min-w-0">
                    <span className="block">{item.label}</span>
                    {item.description && <span className="block text-xs text-muted">{item.description}</span>}
                  </span>
                </button>
              );
            })}
          </div>,
          document.body,
        )}
    </>
  );
}
