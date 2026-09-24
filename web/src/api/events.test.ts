import { describe, expect, it } from 'vitest';
import { backoffDelay, eventInvalidations, parseServerEvent } from './events';
import { queryKeys } from './queryKeys';
import type { Command } from './types';

describe('server events', () => {
  it('parses valid payloads and rejects junk', () => {
    expect(parseServerEvent('{"name":"queue","action":"updated","resource":{}}')).toMatchObject({ name: 'queue' });
    expect(parseServerEvent('not json')).toBeNull();
    expect(parseServerEvent('{"action":"updated"}')).toBeNull();
  });

  it('maps events to the query keys they invalidate', () => {
    expect(eventInvalidations({ name: 'duplicate', action: 'updated', resource: { id: 7 } as never })).toEqual([
      queryKeys.duplicates.lists,
      queryKeys.duplicates.stats,
      queryKeys.duplicates.detail(7),
    ]);
    expect(eventInvalidations({ name: 'health', action: 'updated', resource: [] })).toEqual([]);
    expect(eventInvalidations({ name: 'scan', action: 'progress', resource: { message: 'x', scanId: 1 } })).toEqual(
      [],
    );
    expect(eventInvalidations({ name: 'settings', action: 'updated' })).toContainEqual(queryKeys.config.all);
  });

  it('refreshes dependent data when a command finishes', () => {
    const cmd = { id: 1, name: 'DuplicateScan', status: 'completed' } as Command;
    const keys = eventInvalidations({ name: 'command', action: 'updated', resource: cmd });
    expect(keys).toContainEqual(queryKeys.duplicates.all);
    expect(keys).toContainEqual(queryKeys.system.tasks);

    const running = eventInvalidations({
      name: 'command',
      action: 'updated',
      resource: { ...cmd, status: 'started' },
    });
    expect(running).toEqual([queryKeys.commands.list]);
  });

  it('backs off exponentially up to 30s', () => {
    const mid = () => 0.5; // no jitter
    expect(backoffDelay(0, mid)).toBe(1000);
    expect(backoffDelay(3, mid)).toBe(8000);
    expect(backoffDelay(10, mid)).toBe(30_000);
  });
});
