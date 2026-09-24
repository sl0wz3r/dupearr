import { clsx } from 'clsx';
import { NavLink, useLocation } from 'react-router';
import { useServerEvents } from '@/api/events';
import { useQueue } from '@/api/hooks/useActivity';
import { useDuplicateStats } from '@/api/hooks/useDuplicates';
import { useAppInfo } from '@/app/AppInfo';
import { healthIssues, worstHealth } from './HealthIndicator';
import { NAVIGATION, isSectionActive, type NavItem } from './navigation';

function Count({ value, kind }: { value: number; kind: 'warning' | 'danger' | 'info' | 'accent' }) {
  if (!value) return null;
  const color = {
    warning: 'bg-warning text-black',
    danger: 'bg-danger text-white',
    info: 'bg-info text-white',
    accent: 'bg-accent text-white',
  }[kind];
  return (
    <span className={clsx('ml-auto rounded-sm px-1.5 text-[11px] leading-[18px] font-semibold', color)}>
      {value > 999 ? '999+' : value}
    </span>
  );
}

function useBadges() {
  const stats = useDuplicateStats();
  const queue = useQueue({ page: 1, pageSize: 1 });
  const { health } = useServerEvents();
  const issues = healthIssues(health);
  const worst = worstHealth(issues);
  const needsAttention = (stats.data?.byStatus.pending ?? 0) + (stats.data?.byStatus.review ?? 0);
  return {
    duplicates: <Count value={needsAttention} kind="warning" />,
    queue: <Count value={queue.data?.totalRecords ?? 0} kind="accent" />,
    health: (
      <Count
        value={issues.length}
        kind={worst === 'error' ? 'danger' : worst === 'warning' ? 'warning' : 'info'}
      />
    ),
  } as const;
}

export interface SidebarProps {
  /** Called after a navigation click (closes the mobile drawer). */
  onNavigate?: () => void;
}

/** *arr sidebar: sections with icons; the active section expands its children. */
export function Sidebar({ onNavigate }: SidebarProps) {
  const { pathname } = useLocation();
  const badges = useBadges();
  const info = useAppInfo();

  const renderItem = (item: NavItem) => {
    const active = isSectionActive(item, pathname);
    const Icon = item.icon;
    return (
      <li key={item.title}>
        <NavLink
          to={item.to}
          end={item.to === '/'}
          onClick={onNavigate}
          className={clsx(
            'flex items-center gap-3 border-l-[3px] py-2.5 pr-4 pl-[21px] text-sm no-underline transition-colors',
            active
              ? 'border-accent bg-sidebar-active text-sidebar-accent'
              : 'border-transparent text-sidebar-fg hover:bg-sidebar-hover hover:text-sidebar-accent',
          )}
        >
          <Icon aria-hidden width={18} height={18} className="shrink-0" />
          <span className="truncate">{item.title}</span>
          {item.badge && badges[item.badge]}
        </NavLink>
        {active && item.children && (
          <ul className="m-0 list-none bg-sidebar-active p-0 pb-1">
            {item.children.map((child) => (
              <li key={child.to}>
                <NavLink
                  to={child.to}
                  onClick={onNavigate}
                  className={({ isActive }) =>
                    clsx(
                      'block border-l-[3px] border-accent py-1.5 pr-4 pl-[51px] text-[13px] no-underline transition-colors',
                      isActive ? 'text-sidebar-accent' : 'text-sidebar-fg/85 hover:text-sidebar-accent',
                    )
                  }
                >
                  {child.title}
                </NavLink>
              </li>
            ))}
          </ul>
        )}
      </li>
    );
  };

  return (
    <nav aria-label="Main" className="flex h-full flex-col bg-sidebar">
      <ul className="m-0 flex-1 list-none overflow-y-auto p-0 pt-1">{NAVIGATION.map(renderItem)}</ul>
      {info?.version && (
        <div className="shrink-0 px-6 py-3 text-[11px] text-sidebar-fg/50">
          {info.instanceName && info.instanceName !== 'Dupearr' ? `${info.instanceName} · ` : ''}v{info.version}
        </div>
      )}
    </nav>
  );
}
