/** Bootstrap info (initialize.json) available to every authenticated page. */
import { createContext, useContext, type ReactNode } from 'react';
import type { InitializeResponse } from '@/api/types';

const AppInfoContext = createContext<InitializeResponse | null>(null);

export function AppInfoProvider({ value, children }: { value: InitializeResponse; children: ReactNode }) {
  return <AppInfoContext.Provider value={value}>{children}</AppInfoContext.Provider>;
}

/** initialize.json data (null outside the authenticated app, e.g. on /login). */
export function useAppInfo(): InitializeResponse | null {
  return useContext(AppInfoContext);
}
