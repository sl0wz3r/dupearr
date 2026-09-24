import { describe, expect, it } from 'vitest';
import { detectLineLevel, levelKind, levelLabel, levelRank, normalizeLevel, parseLogLines } from './logLevels';

describe('normalizeLevel', () => {
  it.each([
    ['INFO', 'info'],
    ['Information', 'info'],
    ['warning', 'warn'],
    ['WRN', 'warn'],
    ['err', 'error'],
    ['Trace', 'trace'],
    ['DEBUG', 'debug'],
    ['panic', 'fatal'],
    ['Fatal', 'fatal'],
    ['', null],
    ['nonsense', null],
    [null, null],
    [undefined, null],
  ] as const)('%s → %s', (raw, expected) => {
    expect(normalizeLevel(raw)).toBe(expected);
  });
});

describe('detectLineLevel', () => {
  it.each([
    ['2026-09-22 10:00:00.1|Info|Scanner|Scan started', 'info'],
    ['2026-09-22 10:00:00.1|Warn|Executor|Keeper missing', 'warn'],
    ['2026-09-22 10:00:00.1|Error|Executor|Delete failed', 'error'],
    ['2026-09-22 10:00:00.1|Fatal|Main|Crash', 'fatal'],
    ['time=2026-09-22T10:00:00Z level=DEBUG msg="fetching" component=Plex', 'debug'],
    ['time=2026-09-22T10:00:00Z level="WARN" msg=x', 'warn'],
    ['{"time":"2026-09-22T10:00:00Z","level":"ERROR","msg":"boom"}', 'error'],
    ['[Info] Scanner: done', 'info'],
    ['2026-09-22 10:00:00 [ERROR] database locked', 'error'],
    ['2026/09/22 10:00:00 WARN something odd', 'warn'],
    ['INFO: starting', 'info'],
    ['   at executor.remove (executor.go:120)', null],
    ['', null],
    ['Information about the Scanner|Info', null],
  ] as const)('%s → %s', (line, expected) => {
    expect(detectLineLevel(line)).toBe(expected);
  });

  it('does not match level words inside other words', () => {
    expect(detectLineLevel('level=INFORMATIONAL msg=x')).toBeNull();
    expect(detectLineLevel('|Errors|')).toBeNull();
  });
});

describe('parseLogLines', () => {
  it('numbers lines, inherits levels for continuation lines and drops the trailing newline', () => {
    const text = [
      '2026-09-22 10:00:00.1|Info|Scanner|Scan started',
      '2026-09-22 10:00:01.1|Error|Executor|Delete failed',
      '  at executor.remove',
      '',
      '2026-09-22 10:00:02.1|Info|Scanner|Scan completed',
      '',
    ].join('\r\n');
    const lines = parseLogLines(text);
    expect(lines.map((l) => [l.number, l.level])).toEqual([
      [1, 'info'],
      [2, 'error'],
      [3, 'error'],
      [4, 'error'],
      [5, 'info'],
    ]);
    expect(lines[2]?.text).toBe('  at executor.remove');
  });

  it('handles empty content and lines before any level', () => {
    expect(parseLogLines('')).toEqual([]);
    expect(parseLogLines('banner\nINFO ready')).toEqual([
      { number: 1, text: 'banner', level: null },
      { number: 2, text: 'INFO ready', level: 'info' },
    ]);
  });
});

describe('level display helpers', () => {
  it('maps levels to badge kinds, labels and ranks', () => {
    expect(levelKind('error')).toBe('danger');
    expect(levelKind('FATAL')).toBe('danger');
    expect(levelKind('warning')).toBe('warning');
    expect(levelKind('info')).toBe('info');
    expect(levelKind('debug')).toBe('default');
    expect(levelKind('whatever')).toBe('default');

    expect(levelLabel('WARNING')).toBe('Warn');
    expect(levelLabel('custom')).toBe('Custom');
    expect(levelLabel('')).toBe('-');

    expect(levelRank('trace')).toBeLessThan(levelRank('info'));
    expect(levelRank('error')).toBeLessThan(levelRank('fatal'));
    expect(levelRank('unknown')).toBe(-1);
  });
});
