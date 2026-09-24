import {
  Activity,
  Copy,
  Laptop,
  Settings,
  type LucideIcon,
} from 'lucide-react';

export interface NavChild {
  title: string;
  to: string;
}

export interface NavItem {
  title: string;
  icon: LucideIcon;
  /** Where the parent link points (first child for sections). */
  to: string;
  /** Path prefix that marks the section active. */
  match: string;
  children?: NavChild[];
  /** Which sidebar badge to show. */
  badge?: 'duplicates' | 'queue' | 'health';
}

/** Sidebar navigation (mirrors the router in src/router.tsx). */
export const NAVIGATION: NavItem[] = [
  {
    title: 'Duplicates',
    icon: Copy,
    to: '/',
    match: '/',
    badge: 'duplicates',
  },
  {
    title: 'Activity',
    icon: Activity,
    to: '/activity/queue',
    match: '/activity',
    badge: 'queue',
    children: [
      { title: 'Queue', to: '/activity/queue' },
      { title: 'History', to: '/activity/history' },
    ],
  },
  {
    title: 'Settings',
    icon: Settings,
    to: '/settings/mediaservers',
    match: '/settings',
    children: [
      { title: 'Media Servers', to: '/settings/mediaservers' },
      { title: 'Applications', to: '/settings/applications' },
      { title: 'Profiles', to: '/settings/profiles' },
      { title: 'Media Management', to: '/settings/mediamanagement' },
      { title: 'Exclusions', to: '/settings/exclusions' },
      { title: 'Connect', to: '/settings/connect' },
      { title: 'General', to: '/settings/general' },
      { title: 'UI', to: '/settings/ui' },
    ],
  },
  {
    title: 'System',
    icon: Laptop,
    to: '/system/status',
    match: '/system',
    badge: 'health',
    children: [
      { title: 'Status', to: '/system/status' },
      { title: 'Tasks', to: '/system/tasks' },
      { title: 'Backup', to: '/system/backup' },
      { title: 'Logs', to: '/system/logs' },
      { title: 'Events', to: '/system/events' },
    ],
  },
];

/** Is `item` the active section for `pathname`? ("/" only matches the duplicates pages.) */
export function isSectionActive(item: NavItem, pathname: string): boolean {
  if (item.match === '/') return pathname === '/' || pathname.startsWith('/duplicate/');
  return pathname === item.match || pathname.startsWith(`${item.match}/`);
}
