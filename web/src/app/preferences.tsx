/**
 * Per-browser UI preferences (Settings → UI), persisted in localStorage under `dupearr.ui`.
 * index.html reads `theme` from the same key before first paint.
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { LongDateFormat, ShortDateFormat, TimeFormat } from '@/lib/format';
import { readJson, writeJson } from '@/lib/storage';

export type Theme = 'dark' | 'light';

export interface UiPreferences {
  theme: Theme;
  /** Show settings flagged "advanced". */
  showAdvanced: boolean;
  /** Show "5 minutes ago" instead of absolute dates. */
  showRelativeDates: boolean;
  shortDateFormat: ShortDateFormat;
  longDateFormat: LongDateFormat;
  timeFormat: TimeFormat;
  /** Desktop sidebar collapsed. */
  sidebarCollapsed: boolean;
}

export const PREFERENCES_STORAGE_KEY = 'dupearr.ui';

export const DEFAULT_PREFERENCES: UiPreferences = {
  theme: 'dark',
  showAdvanced: false,
  showRelativeDates: true,
  shortDateFormat: 'MMM D YYYY',
  longDateFormat: 'dddd, MMMM D YYYY',
  timeFormat: 'h(:mm)a',
  sidebarCollapsed: false,
};

function loadPreferences(): UiPreferences {
  const stored = readJson<Partial<UiPreferences> | null>(PREFERENCES_STORAGE_KEY, null);
  const merged = { ...DEFAULT_PREFERENCES, ...(stored && typeof stored === 'object' ? stored : {}) };
  if (merged.theme !== 'dark' && merged.theme !== 'light') merged.theme = 'dark';
  return merged;
}

export function applyTheme(theme: Theme): void {
  if (typeof document === 'undefined') return;
  document.documentElement.setAttribute('data-theme', theme);
  const meta = document.querySelector('meta[name="theme-color"]');
  meta?.setAttribute('content', theme === 'dark' ? '#2a2a2a' : '#3a3f51');
}

interface PreferencesContextValue {
  preferences: UiPreferences;
  setPreference: <K extends keyof UiPreferences>(key: K, value: UiPreferences[K]) => void;
  setPreferences: (patch: Partial<UiPreferences>) => void;
  resetPreferences: () => void;
}

const PreferencesContext = createContext<PreferencesContextValue | null>(null);

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const [preferences, setState] = useState<UiPreferences>(loadPreferences);

  useEffect(() => {
    applyTheme(preferences.theme);
  }, [preferences.theme]);

  const setPreferences = useCallback((patch: Partial<UiPreferences>) => {
    setState((prev) => {
      const next = { ...prev, ...patch };
      writeJson(PREFERENCES_STORAGE_KEY, next);
      return next;
    });
  }, []);

  const setPreference = useCallback(
    <K extends keyof UiPreferences>(key: K, value: UiPreferences[K]) => {
      setPreferences({ [key]: value } as Partial<UiPreferences>);
    },
    [setPreferences],
  );

  const resetPreferences = useCallback(() => {
    writeJson(PREFERENCES_STORAGE_KEY, DEFAULT_PREFERENCES);
    setState(DEFAULT_PREFERENCES);
  }, []);

  const value = useMemo(
    () => ({ preferences, setPreference, setPreferences, resetPreferences }),
    [preferences, setPreference, setPreferences, resetPreferences],
  );
  return <PreferencesContext.Provider value={value}>{children}</PreferencesContext.Provider>;
}

const fallback: PreferencesContextValue = {
  preferences: DEFAULT_PREFERENCES,
  setPreference: () => {},
  setPreferences: () => {},
  resetPreferences: () => {},
};

/** All UI preferences + setters (works without a provider in tests, returning defaults). */
export function usePreferences(): PreferencesContextValue {
  return useContext(PreferencesContext) ?? fallback;
}

/** `[theme, setTheme, toggleTheme]` */
export function useTheme(): [Theme, (theme: Theme) => void, () => void] {
  const { preferences, setPreference } = usePreferences();
  const setTheme = useCallback((t: Theme) => setPreference('theme', t), [setPreference]);
  const toggle = useCallback(
    () => setPreference('theme', preferences.theme === 'dark' ? 'light' : 'dark'),
    [preferences.theme, setPreference],
  );
  return [preferences.theme, setTheme, toggle];
}

/** `[showAdvanced, setShowAdvanced]` — persisted "Show Advanced" toggle for settings pages. */
export function useShowAdvanced(): [boolean, (value: boolean) => void] {
  const { preferences, setPreference } = usePreferences();
  const set = useCallback((v: boolean) => setPreference('showAdvanced', v), [setPreference]);
  return [preferences.showAdvanced, set];
}
