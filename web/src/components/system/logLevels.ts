/**
 * Log level helpers for System → Events and the log file viewer. Log line formats are detected
 * defensively (the *arr pipe layout, slog text/JSON and bracketed levels are all recognized).
 */
import type { LogLevel } from '@/api/types';
import type { StatusKind } from '@/lib/constants';

/** Levels a log line/entry can have (`fatal` also appears in *arr-style logs). */
export type LineLevel = LogLevel | 'fatal';

/** Minimum-level choices of System → Events, least to most severe. */
export const LOG_LEVELS: readonly LogLevel[] = ['trace', 'debug', 'info', 'warn', 'error'];

const RANK: Record<LineLevel, number> = { trace: 0, debug: 1, info: 2, warn: 3, error: 4, fatal: 5 };

/** Severity rank (trace 0 … fatal 5); unknown → -1. */
export function levelRank(level: string | null | undefined): number {
  const n = normalizeLevel(level);
  return n ? RANK[n] : -1;
}

/** Maps level spellings to a canonical level ("WARNING" → warn, "information" → info, "panic" → fatal). */
export function normalizeLevel(raw: string | null | undefined): LineLevel | null {
  if (!raw) return null;
  const v = raw.trim().toLowerCase();
  switch (v) {
    case 'trace':
    case 'trc':
    case 'verbose':
      return 'trace';
    case 'debug':
    case 'dbg':
      return 'debug';
    case 'info':
    case 'inf':
    case 'information':
    case 'notice':
      return 'info';
    case 'warn':
    case 'wrn':
    case 'warning':
      return 'warn';
    case 'error':
    case 'err':
    case 'eror':
      return 'error';
    case 'fatal':
    case 'ftl':
    case 'panic':
    case 'critical':
    case 'crit':
      return 'fatal';
    default:
      return null;
  }
}

const LEVEL_WORD = 'trace|debug|info|information|notice|warn|warning|error|err|fatal|panic|critical|verbose';
const PATTERNS: RegExp[] = [
  // *arr layout: "2026-09-22 10:00:00.1|Info|Scanner|message"
  new RegExp(`\\|(${LEVEL_WORD})\\|`, 'i'),
  // slog text handler: "time=… level=INFO msg=…"
  new RegExp(`\\blevel=["']?(${LEVEL_WORD})\\b`, 'i'),
  // slog JSON handler: {"level":"INFO",…}
  new RegExp(`"level"\\s*:\\s*"(${LEVEL_WORD})"`, 'i'),
  // Console style: "[Info] Scanner: …" / "[ERROR] …" (optionally after a timestamp)
  new RegExp(`^(?:\\S+\\s+){0,2}\\[(${LEVEL_WORD})\\]`, 'i'),
  // Bare upper-case prefix (Go log/slog default handler): "2026/09/22 10:00:00 INFO …", "WARN: …".
  // Case-sensitive so ordinary text such as "Information about…" is not mistaken for a level.
  /^(?:\d[\d/:.T\-+Z]*\s+){0,2}(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL|PANIC)\b[:\s]/,
];

/** Level of a single log line, or null when none is recognizable (e.g. a stack-trace line). */
export function detectLineLevel(line: string): LineLevel | null {
  for (const re of PATTERNS) {
    const m = re.exec(line);
    if (m?.[1]) {
      const level = normalizeLevel(m[1]);
      if (level) return level;
    }
  }
  return null;
}

export interface LogLine {
  /** 1-based line number in the file. */
  number: number;
  text: string;
  /** Detected level; continuation lines (exceptions) inherit the previous line's level. */
  level: LineLevel | null;
}

/** Splits file content into lines with levels. A trailing newline does not produce an empty line. */
export function parseLogLines(content: string): LogLine[] {
  if (!content) return [];
  const raw = content.replace(/\r\n?/g, '\n').split('\n');
  if (raw.length > 0 && raw[raw.length - 1] === '') raw.pop();
  let previous: LineLevel | null = null;
  return raw.map((text, i) => {
    const detected = detectLineLevel(text);
    if (detected) previous = detected;
    return { number: i + 1, text, level: detected ?? previous };
  });
}

/** Text colour of a log line by level. */
export const LINE_LEVEL_CLASS: Record<LineLevel, string> = {
  trace: 'text-subtle',
  debug: 'text-muted',
  info: 'text-fg',
  warn: 'text-warning',
  error: 'text-danger',
  fatal: 'text-danger font-semibold',
};

/** Badge colour of a level (unknown levels are neutral). */
export function levelKind(level: string | null | undefined): StatusKind {
  switch (normalizeLevel(level)) {
    case 'info':
      return 'info';
    case 'warn':
      return 'warning';
    case 'error':
    case 'fatal':
      return 'danger';
    default:
      return 'default';
  }
}

/** Display label of a level ("warn" → "Warn", unknown values title-cased). */
export function levelLabel(level: string | null | undefined): string {
  const n = normalizeLevel(level);
  if (n) return n.charAt(0).toUpperCase() + n.slice(1);
  const raw = (level ?? '').trim();
  return raw ? raw.charAt(0).toUpperCase() + raw.slice(1).toLowerCase() : '-';
}
