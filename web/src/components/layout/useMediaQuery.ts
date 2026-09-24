import { useSyncExternalStore } from 'react';

/** Subscribes to a CSS media query (false during SSR / when matchMedia is unavailable). */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (cb) => {
      if (typeof window === 'undefined' || !window.matchMedia) return () => {};
      const mql = window.matchMedia(query);
      mql.addEventListener('change', cb);
      return () => mql.removeEventListener('change', cb);
    },
    () => (typeof window !== 'undefined' && window.matchMedia ? window.matchMedia(query).matches : false),
    () => false,
  );
}

/** Below Tailwind's `md` breakpoint. */
export function useIsMobile(): boolean {
  return useMediaQuery('(max-width: 767.98px)');
}
