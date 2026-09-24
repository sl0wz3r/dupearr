/**
 * Routes (React Router v7, library mode). Every page is its own lazily-loaded chunk so agents can
 * implement pages in parallel: src/pages/<area>/<Name>Page.tsx (default export).
 */
import { lazy } from 'react';
import { createBrowserRouter, Navigate, type RouteObject } from 'react-router';
import { getUrlBase } from '@/api/client';
import { AuthGate } from '@/app/AuthGate';
import { RouteErrorPage } from '@/components/ErrorBoundary';
import { AppLayout } from '@/components/layout/AppLayout';

const DuplicatesPage = lazy(() => import('@/pages/duplicates/DuplicatesPage'));
const DuplicateDetailPage = lazy(() => import('@/pages/duplicates/DuplicateDetailPage'));

const QueuePage = lazy(() => import('@/pages/activity/QueuePage'));
const HistoryPage = lazy(() => import('@/pages/activity/HistoryPage'));

const MediaServersPage = lazy(() => import('@/pages/settings/MediaServersPage'));
const ApplicationsPage = lazy(() => import('@/pages/settings/ApplicationsPage'));
const ProfilesPage = lazy(() => import('@/pages/settings/ProfilesPage'));
const MediaManagementPage = lazy(() => import('@/pages/settings/MediaManagementPage'));
const ExclusionsPage = lazy(() => import('@/pages/settings/ExclusionsPage'));
const ConnectPage = lazy(() => import('@/pages/settings/ConnectPage'));
const GeneralPage = lazy(() => import('@/pages/settings/GeneralPage'));
const UiPage = lazy(() => import('@/pages/settings/UiPage'));

const StatusPage = lazy(() => import('@/pages/system/StatusPage'));
const TasksPage = lazy(() => import('@/pages/system/TasksPage'));
const BackupPage = lazy(() => import('@/pages/system/BackupPage'));
const LogsPage = lazy(() => import('@/pages/system/LogsPage'));
const EventsPage = lazy(() => import('@/pages/system/EventsPage'));

const LoginPage = lazy(() => import('@/pages/auth/LoginPage'));
const SetupPage = lazy(() => import('@/pages/auth/SetupPage'));
const NotFoundPage = lazy(() => import('@/pages/NotFoundPage'));

export const routes: RouteObject[] = [
  { path: '/login', element: <LoginPage />, errorElement: <RouteErrorPage /> },
  { path: '/setup', element: <SetupPage />, errorElement: <RouteErrorPage /> },
  {
    element: (
      <AuthGate>
        <AppLayout />
      </AuthGate>
    ),
    errorElement: <RouteErrorPage />,
    children: [
      { index: true, element: <DuplicatesPage /> },
      { path: 'duplicate/:id', element: <DuplicateDetailPage /> },
      {
        path: 'activity',
        children: [
          { index: true, element: <Navigate to="queue" replace /> },
          { path: 'queue', element: <QueuePage /> },
          { path: 'history', element: <HistoryPage /> },
        ],
      },
      {
        path: 'settings',
        children: [
          { index: true, element: <Navigate to="mediaservers" replace /> },
          { path: 'mediaservers', element: <MediaServersPage /> },
          { path: 'applications', element: <ApplicationsPage /> },
          { path: 'profiles', element: <ProfilesPage /> },
          { path: 'mediamanagement', element: <MediaManagementPage /> },
          { path: 'exclusions', element: <ExclusionsPage /> },
          { path: 'connect', element: <ConnectPage /> },
          { path: 'general', element: <GeneralPage /> },
          { path: 'ui', element: <UiPage /> },
        ],
      },
      {
        path: 'system',
        children: [
          { index: true, element: <Navigate to="status" replace /> },
          { path: 'status', element: <StatusPage /> },
          { path: 'tasks', element: <TasksPage /> },
          { path: 'backup', element: <BackupPage /> },
          { path: 'logs', element: <LogsPage /> },
          { path: 'events', element: <EventsPage /> },
        ],
      },
      ...devRoutes(),
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

/** Development-only pages (eliminated from production builds). */
function devRoutes(): RouteObject[] {
  if (!import.meta.env.DEV) return [];
  const UiKitPage = lazy(() => import('@/pages/dev/UiKitPage'));
  return [{ path: '_dev/ui', element: <UiKitPage /> }];
}

export function createAppRouter() {
  return createBrowserRouter(routes, { basename: getUrlBase() || '/' });
}
