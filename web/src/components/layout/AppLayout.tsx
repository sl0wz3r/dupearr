import { clsx } from 'clsx';
import { Suspense, useEffect, useState } from 'react';
import { Outlet, useLocation } from 'react-router';
import { usePreferences } from '@/app/preferences';
import { ErrorBoundary } from '@/components/ErrorBoundary';
import { LoadingIndicator } from '@/components/ui/Spinner';
import { DryRunBanner } from './DryRunBanner';
import { Header } from './Header';
import { Sidebar } from './Sidebar';
import { useIsMobile } from './useMediaQuery';

/**
 * Authenticated shell: Header on top, collapsible Sidebar (drawer on mobile), dry-run banner and
 * the routed page (lazy-loaded) filling the rest.
 */
export function AppLayout() {
  const isMobile = useIsMobile();
  const { preferences, setPreference } = usePreferences();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const location = useLocation();

  // Close the drawer when switching to desktop or navigating.
  useEffect(() => {
    if (!isMobile) setDrawerOpen(false);
  }, [isMobile]);
  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

  // Esc closes the drawer.
  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrawerOpen(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [drawerOpen]);

  const sidebarVisible = isMobile ? drawerOpen : !preferences.sidebarCollapsed;
  const toggleSidebar = () => {
    if (isMobile) setDrawerOpen((o) => !o);
    else setPreference('sidebarCollapsed', !preferences.sidebarCollapsed);
  };

  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-page">
      <a
        href="#main"
        className="sr-only z-50 rounded bg-accent px-3 py-2 text-white focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
      </a>
      <Header onToggleSidebar={toggleSidebar} sidebarOpen={sidebarVisible} />
      <div className="relative flex min-h-0 flex-1">
        {/* Desktop sidebar */}
        {!isMobile && (
          <aside
            className={clsx(
              'shrink-0 overflow-hidden transition-[width] duration-200',
              sidebarVisible ? 'w-[210px]' : 'w-0',
            )}
          >
            <div className="h-full w-[210px]">
              <Sidebar />
            </div>
          </aside>
        )}

        {/* Mobile drawer */}
        {isMobile && (
          <>
            <div
              aria-hidden
              onClick={() => setDrawerOpen(false)}
              className={clsx(
                'absolute inset-0 z-30 bg-overlay transition-opacity duration-200',
                drawerOpen ? 'opacity-100' : 'pointer-events-none opacity-0',
              )}
            />
            <aside
              aria-hidden={!drawerOpen}
              inert={!drawerOpen}
              className={clsx(
                'absolute inset-y-0 left-0 z-40 w-[240px] max-w-[85vw] shadow-popover transition-transform duration-200',
                drawerOpen ? 'translate-x-0' : '-translate-x-full',
              )}
            >
              <Sidebar onNavigate={() => setDrawerOpen(false)} />
            </aside>
          </>
        )}

        <main id="main" className="flex min-w-0 flex-1 flex-col overflow-hidden">
          <DryRunBanner />
          <div className="min-h-0 flex-1">
            <ErrorBoundary resetKey={location.pathname}>
              <Suspense fallback={<LoadingIndicator fill />}>
                <Outlet />
              </Suspense>
            </ErrorBoundary>
          </div>
        </main>
      </div>
    </div>
  );
}
