import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { useChangeGuard } from './useChangeGuard';

describe('useChangeGuard', () => {
  const setup = (open: boolean, fingerprint: string) =>
    renderHook(({ open, fingerprint }) => useChangeGuard(open, fingerprint), {
      initialProps: { open, fingerprint },
    });

  it('never reports a change while closed', () => {
    const { result, rerender } = setup(false, 'a');
    expect(result.current.changed).toBe(false);
    rerender({ open: false, fingerprint: 'b' });
    expect(result.current.changed).toBe(false);
  });

  it('captures the fingerprint in the render that opens the dialog', () => {
    const { result, rerender } = setup(false, 'a');
    // Content and open flag change together (e.g. a row dialog receiving its group).
    rerender({ open: true, fingerprint: 'b' });
    expect(result.current.changed).toBe(false);
    rerender({ open: true, fingerprint: 'b' });
    expect(result.current.changed).toBe(false);
  });

  it('reports a change until acknowledged, then tracks the new baseline', () => {
    const { result, rerender } = setup(true, 'a');
    rerender({ open: true, fingerprint: 'b' });
    expect(result.current.changed).toBe(true);

    act(() => result.current.acknowledge());
    expect(result.current.changed).toBe(false);

    rerender({ open: true, fingerprint: 'c' });
    expect(result.current.changed).toBe(true);
    // Going back to the acknowledged content clears it again.
    rerender({ open: true, fingerprint: 'b' });
    expect(result.current.changed).toBe(false);
  });

  it('resets when the dialog is closed and reopened', () => {
    const { result, rerender } = setup(true, 'a');
    rerender({ open: true, fingerprint: 'b' });
    expect(result.current.changed).toBe(true);
    rerender({ open: false, fingerprint: 'b' });
    expect(result.current.changed).toBe(false);
    rerender({ open: true, fingerprint: 'c' });
    expect(result.current.changed).toBe(false);
  });

  it('keeps the snapshot captured at open until a change is acknowledged', () => {
    const { result, rerender } = renderHook(
      ({ open, fingerprint, snapshot }) => useChangeGuard(open, fingerprint, snapshot),
      { initialProps: { open: false, fingerprint: 'a', snapshot: 'sig-a' } },
    );
    rerender({ open: true, fingerprint: 'b', snapshot: 'sig-b' });
    expect(result.current.snapshot).toBe('sig-b');

    // Live update while open: the reviewed snapshot stays until the user acknowledges.
    rerender({ open: true, fingerprint: 'c', snapshot: 'sig-c' });
    expect(result.current.changed).toBe(true);
    expect(result.current.snapshot).toBe('sig-b');
    act(() => result.current.acknowledge());
    expect(result.current.changed).toBe(false);
    expect(result.current.snapshot).toBe('sig-c');

    // Closing and reopening captures afresh.
    rerender({ open: false, fingerprint: 'd', snapshot: 'sig-d' });
    rerender({ open: true, fingerprint: 'e', snapshot: 'sig-e' });
    expect(result.current.snapshot).toBe('sig-e');
  });
});
