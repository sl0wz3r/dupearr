import { useCallback, useState } from 'react';
import { readJson, writeJson } from '@/lib/storage';

/**
 * useState persisted to localStorage (per-browser conveniences such as a remembered filter).
 * Stored values failing `isValid` are ignored; storage failures never throw (lib/storage).
 */
export function useStoredState<T>(
  key: string,
  initial: T,
  isValid: (value: unknown) => value is T,
): [T, (value: T) => void] {
  const [value, setValue] = useState<T>(() => {
    const stored = readJson<unknown>(key, undefined);
    return isValid(stored) ? stored : initial;
  });
  const set = useCallback(
    (next: T) => {
      setValue(next);
      writeJson(key, next);
    },
    [key],
  );
  return [value, set];
}
