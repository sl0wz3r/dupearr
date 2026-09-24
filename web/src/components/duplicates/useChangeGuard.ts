import { useCallback, useState } from 'react';

export interface ChangeGuard<T = undefined> {
  /**
   * True when what the dialog confirms (its fingerprint) changed since it was opened or since
   * the user last acknowledged a change. Confirm buttons must stay disabled while it is true.
   */
  changed: boolean;
  /** The user reviewed the updated content: accept the current fingerprint as the new baseline. */
  acknowledge: () => void;
  /**
   * The `snapshot` passed when the dialog opened, or when the user last acknowledged a change —
   * i.e. the state the user actually reviewed (e.g. the group signatures an approval sends back).
   */
  snapshot: T;
}

interface GuardState<T> {
  open: boolean;
  baseline: string;
  snapshot: T;
}

/**
 * Guards a confirmation dialog against its content changing underneath the user.
 *
 * The duplicates pages refresh live (SSE invalidations, re-scans, other users), so the list of
 * copies an approval dialog shows can change while it is open — and the server acts on its
 * *current* decisions, not on what the user read. The fingerprint (and an optional `snapshot` of
 * what the confirmation will send, such as group signatures) is captured when the dialog opens,
 * in the same render (no effect lag); any later fingerprint difference reports `changed` until the
 * user acknowledges it, which also re-captures the snapshot. Closing the dialog resets the guard.
 */
export function useChangeGuard(open: boolean, fingerprint: string): ChangeGuard;
export function useChangeGuard<T>(open: boolean, fingerprint: string, snapshot: T): ChangeGuard<T>;
export function useChangeGuard<T>(open: boolean, fingerprint: string, snapshot?: T): ChangeGuard<T | undefined> {
  const [state, setState] = useState<GuardState<T | undefined>>({ open, baseline: fingerprint, snapshot });

  // Derived-state reset on open/close (React's documented "adjust state while rendering").
  let current = state;
  if (state.open !== open) {
    current = { open, baseline: fingerprint, snapshot };
    setState(current);
  }

  const acknowledge = useCallback(
    () => setState({ open, baseline: fingerprint, snapshot }),
    [open, fingerprint, snapshot],
  );

  return { changed: open && current.baseline !== fingerprint, acknowledge, snapshot: current.snapshot };
}
