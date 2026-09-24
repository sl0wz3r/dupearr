/**
 * Pure helpers for Activity → Queue/History: history event icons, defensive parsing of the
 * free-form history `data` payload, and action eligibility rules.
 */
import {
  Ban,
  Check,
  CircleAlert,
  CircleCheck,
  CircleX,
  Copy,
  Eye,
  FlaskConical,
  Pencil,
  RotateCcw,
  SearchCheck,
  ShieldAlert,
  SkipForward,
  ThumbsUp,
  Trash,
  type LucideIcon,
} from 'lucide-react';
import type { Action, HistoryEventType } from '@/api/types';

/** Icon shown in the type badge of each history event. */
export const HISTORY_EVENT_ICONS: Record<HistoryEventType, LucideIcon> = {
  scanCompleted: SearchCheck,
  scanFailed: CircleAlert,
  groupDetected: Copy,
  groupApproved: ThumbsUp,
  groupIgnored: Ban,
  groupUnignored: Eye,
  groupResolved: CircleCheck,
  fileDeleted: Trash,
  fileDeleteDryRun: FlaskConical,
  fileDeleteFailed: CircleX,
  fileSkipped: SkipForward,
  fileRestored: RotateCcw,
  overrideChanged: Pencil,
  security: ShieldAlert,
};

/** Fallback icon for event types this UI does not know yet. */
export const UNKNOWN_EVENT_ICON: LucideIcon = Check;

/** Key facts extracted from a history event's `data` (all optional; shapes vary per event). */
export interface HistoryDataSummary {
  paths: string[];
  method?: string;
  permanent?: boolean;
  recyclePath?: string;
  dryRun?: boolean;
  size?: number;
  message?: string;
}

/**
 * Normalizes the raw `data` value: JSON strings are parsed, and null/undefined/""/{}/[] become
 * `null` ("nothing to show"). Never throws.
 */
export function normalizeHistoryData(data: unknown): unknown {
  let value = data;
  if (typeof value === 'string') {
    const text = value.trim();
    if (!text) return null;
    try {
      value = JSON.parse(text);
    } catch {
      return text;
    }
  }
  if (value === null || value === undefined) return null;
  if (Array.isArray(value)) return value.length > 0 ? value : null;
  if (typeof value === 'object') return Object.keys(value as object).length > 0 ? value : null;
  return value;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function stringList(value: unknown): string[] {
  if (typeof value === 'string') return value ? [value] : [];
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === 'string' && v !== '');
}

/** Extracts paths/method/permanent/… from a history `data` payload (unknown keys are ignored). */
export function summarizeHistoryData(data: unknown): HistoryDataSummary {
  const rec = asRecord(normalizeHistoryData(data));
  if (!rec) return { paths: [] };
  const paths = [...stringList(rec.paths), ...stringList(rec.path)];
  const summary: HistoryDataSummary = { paths: [...new Set(paths)] };
  if (typeof rec.method === 'string' && rec.method) summary.method = rec.method;
  if (typeof rec.permanent === 'boolean') summary.permanent = rec.permanent;
  if (typeof rec.recyclePath === 'string' && rec.recyclePath) summary.recyclePath = rec.recyclePath;
  if (typeof rec.dryRun === 'boolean') summary.dryRun = rec.dryRun;
  if (typeof rec.size === 'number' && Number.isFinite(rec.size)) summary.size = rec.size;
  if (typeof rec.message === 'string' && rec.message) summary.message = rec.message;
  return summary;
}

/** Pretty-printed JSON (2 spaces) of any value; falls back to String() for unserializable input. */
export function prettyJson(value: unknown): string {
  if (typeof value === 'string') return value;
  try {
    const out = JSON.stringify(value, null, 2);
    return out === undefined ? String(value) : out;
  } catch {
    return String(value);
  }
}

/**
 * Whether an action can be undone from the UI: only successful, real (not dry-run) filesystem
 * removals that were moved to Dupearr's recycle bin. Everything else is refused (safety first).
 */
export function canRestoreAction(action: Action): boolean {
  return (
    action.status === 'succeeded' &&
    action.method === 'filesystem' &&
    !action.dryRun &&
    !action.permanent &&
    typeof action.recyclePath === 'string' &&
    action.recyclePath.trim() !== ''
  );
}

/** Whether a queue item can still be cancelled (only pending actions; running ones cannot). */
export function canCancelQueueItem(action: Action): boolean {
  return action.status === 'pending';
}

/** Milliseconds between two ISO timestamps, or null when either is missing/invalid/negative. */
export function elapsedMs(start: string | null | undefined, end: string | number | null | undefined): number | null {
  if (!start || end === null || end === undefined || end === '') return null;
  const s = new Date(start).getTime();
  const e = typeof end === 'number' ? end : new Date(end).getTime();
  if (!Number.isFinite(s) || !Number.isFinite(e)) return null;
  // Go zero times ("0001-01-01…") mean unset.
  if (new Date(s).getUTCFullYear() <= 1) return null;
  const diff = e - s;
  return diff >= 0 ? diff : null;
}
