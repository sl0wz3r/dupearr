/**
 * Pure helpers of Settings → Media Management (docs/ARCHITECTURE.md §4.4, docs/DECISIONS.md D4/D6).
 */
import { isApiError, rootPropertyName } from '@/api/client';
import type { DeletionMethod, PathMapping, Settings } from '@/api/types';
import type { BadgeKind } from '@/components/ui';
import { humanize } from '@/lib/constants';
import { comparablePath, isAbsolutePath, isFilesystemRoot, isSameOrInside } from './validation';

/** "7 days", "1 day 12 hours", "12 hours", "0 hours". */
export function formatHours(hours: number): string {
  const h = Math.max(0, Math.round(hours));
  const days = Math.floor(h / 24);
  const rest = h % 24;
  const parts: string[] = [];
  if (days > 0) parts.push(`${days} day${days === 1 ? '' : 's'}`);
  if (rest > 0 || days === 0) parts.push(`${rest} hour${rest === 1 ? '' : 's'}`);
  return parts.join(' ');
}

/** How each deletion method removes a file, and whether that can be undone. */
export function methodInfo(method: DeletionMethod, recycleBinPath: string): { text: string; badge: string; kind: BadgeKind } {
  switch (method) {
    case 'arr':
      return {
        text: 'Via the owning Radarr/Sonarr (permanent unless its recycle bin is set).',
        badge: 'Permanent unless *arr recycle bin',
        kind: 'warning',
      };
    case 'plex':
      return {
        text: 'Via the Plex API (permanent; requires “Allow media deletion” in Plex and the server owner’s token). Not used for Jellyfin: Dupearr never deletes through Jellyfin, its copies only go through Radarr/Sonarr or into Dupearr’s recycle bin.',
        badge: 'Permanent',
        kind: 'danger',
      };
    case 'filesystem':
      return {
        text: 'Moves the file to Dupearr’s recycle bin (requires path mappings), or deletes it when no recycle bin is set.',
        badge: recycleBinPath.trim() ? 'Recoverable (recycle bin)' : 'Permanent (no recycle bin)',
        kind: recycleBinPath.trim() ? 'success' : 'danger',
      };
    default:
      return { text: '', badge: '', kind: 'default' };
  }
}

/**
 * Warnings of the full-disc settings (docs/research/disc-structures.md §6.7): disc removal needs a
 * recycle bin and disc detection, and a disc as the only kept copy leaves Plex nothing to play.
 */
export function discSettingsWarnings(
  s: Pick<Settings, 'detectDiscs' | 'allowDiscRemoval' | 'keepPlayableCopy' | 'recycleBinPath' | 'deletionMethods'>,
): { allowDiscRemoval: string[]; keepPlayableCopy: string[] } {
  const allow: string[] = [];
  if (s.allowDiscRemoval) {
    if (!(s.recycleBinPath ?? '').trim()) {
      allow.push('No recycle bin is set: every disc removal is refused until you set one below.');
    }
    if (!(s.deletionMethods ?? []).includes('filesystem')) {
      allow.push('Discs are only removed by the filesystem method, which is not in your deletion methods, so disc removals may be refused.');
    }
    if (!s.detectDiscs) allow.push('Disc detection is off, so no disc is found to remove.');
  }
  const keep: string[] = [];
  if (!s.keepPlayableCopy) {
    keep.push(
      'A disc can become the only kept copy: Plex then has nothing to play, and Radarr/Sonarr may consider the title missing and download it again.',
    );
  }
  return { allowDiscRemoval: allow, keepPlayableCopy: keep };
}

/** Settings fields that exist in the Media Management form (for mapping server validation errors). */
export const MEDIA_MANAGEMENT_FIELDS: readonly (keyof Settings)[] = [
  'dryRun',
  'mode',
  'stableScansRequired',
  'scanIntervalMinutes',
  'minAgeHours',
  'maxDeletionsPerRun',
  'maxBytesPerRunGb',
  'deletionMethods',
  'arrRescanAfterDelete',
  'unmonitorWhenKeeperElsewhere',
  'addExclusionWhenKeeperElsewhere',
  'recycleBinPath',
  'recycleBinCleanupDays',
  'refreshPlexAfterDelete',
  'cleanupPlexStaleEntries',
  'treatEditionsAsDistinct',
  'treat3DAsDistinct',
  'languageVariantsAsDistinct',
  'differentArrInstancesIntentional',
  'durationTolerancePercent',
  'durationToleranceMinutes',
  'maxGroupSize',
  'historyRetentionDays',
  'detectDiscs',
  'allowDiscRemoval',
  'keepPlayableCopy',
];

/** Labels of every Settings field as the settings pages show them (Media Management / General). */
export const SETTINGS_FIELD_LABELS: Readonly<Record<keyof Settings, string>> = {
  dryRun: 'Dry Run',
  mode: 'Approval Mode',
  scanIntervalMinutes: 'Scan Interval',
  minAgeHours: 'Minimum Age',
  maxDeletionsPerRun: 'Max Deletions per Run',
  deletionMethods: 'Deletion Methods',
  arrRescanAfterDelete: 'Rescan *arr After Delete',
  unmonitorWhenKeeperElsewhere: 'Unmonitor When Kept Elsewhere',
  addExclusionWhenKeeperElsewhere: 'Add Exclusion When Kept Elsewhere',
  recycleBinPath: 'Recycle Bin',
  recycleBinCleanupDays: 'Recycle Bin Cleanup',
  refreshPlexAfterDelete: 'Refresh Plex After Delete',
  cleanupPlexStaleEntries: 'Clean Up Stale Plex Entries',
  treatEditionsAsDistinct: 'Editions Are Distinct',
  treat3DAsDistinct: '3D Is Distinct',
  languageVariantsAsDistinct: 'Language Variants Are Distinct',
  differentArrInstancesIntentional: 'Separate *arr Instances Are Intentional',
  maxGroupSize: 'Max Group Size',
  stableScansRequired: 'Stable Scans Required',
  maxBytesPerRunGb: 'Max Size per Run',
  durationTolerancePercent: 'Duration Tolerance',
  durationToleranceMinutes: 'Duration Tolerance (Minutes)',
  historyRetentionDays: 'History Retention',
  backupIntervalDays: 'Backup Interval',
  backupRetentionDays: 'Backup Retention',
  detectDiscs: 'Detect Full-Disc Backups',
  allowDiscRemoval: 'Allow Removing Full Discs',
  keepPlayableCopy: 'Always Keep a Plex-Playable Copy',
};

/** The Settings field a server property name refers to (ignoring case and any `[i]`/`.x` suffix). */
export function settingsFieldOf(propertyName: string | null | undefined): keyof Settings | undefined {
  const root = rootPropertyName(propertyName).toLowerCase();
  if (!root) return undefined;
  return (Object.keys(SETTINGS_FIELD_LABELS) as (keyof Settings)[]).find((k) => k.toLowerCase() === root);
}

/** Label for errors about a Settings property ("Max Size per Run"); undefined for other properties. */
export function settingsFieldLabel(propertyName: string): string | undefined {
  const field = settingsFieldOf(propertyName);
  return field ? SETTINGS_FIELD_LABELS[field] : undefined;
}

export interface SettingsErrorMapping {
  /** Errors per field shown on the page. */
  fields: Partial<Record<keyof Settings, string[]>>;
  /** Everything else, labelled ("Backup Interval: Must be …"), for the page's error summary. */
  other: string[];
}

/**
 * Maps the validation errors of a failed `PUT /config/settings` (400 `[{propertyName,
 * errorMessage}]`, property names = the settings' camelCase JSON names) to the fields `shown` on
 * the page. Names are matched ignoring case and index/path suffixes ("deletionMethods[1]").
 * Errors on other or unknown properties land in `other`, prefixed with their label.
 */
export function mapSettingsErrors(error: unknown, shown: readonly (keyof Settings)[]): SettingsErrorMapping {
  const out: SettingsErrorMapping = { fields: {}, other: [] };
  if (!isApiError(error) || !error.isValidation) return out;
  for (const v of error.validationErrors) {
    const field = settingsFieldOf(v.propertyName);
    if (field && shown.includes(field)) {
      (out.fields[field] ??= []).push(v.errorMessage);
      continue;
    }
    const label = field ? SETTINGS_FIELD_LABELS[field] : humanize(rootPropertyName(v.propertyName));
    const named = label && v.errorMessage.toLowerCase().includes(label.toLowerCase());
    out.other.push(label && !named ? `${label}: ${v.errorMessage}` : v.errorMessage);
  }
  return out;
}

type SettingsFieldType = 'boolean' | 'number' | 'string' | 'array';

/**
 * JSON type of every `Settings` field. The server merges `PUT /config/settings` onto the stored
 * settings (absent fields keep their values), but the page still refuses to save an incomplete
 * object: it would mean the page shows values it did not receive.
 */
export const SETTINGS_FIELD_TYPES: Readonly<Record<keyof Settings, SettingsFieldType>> = {
  dryRun: 'boolean',
  mode: 'string',
  scanIntervalMinutes: 'number',
  minAgeHours: 'number',
  maxDeletionsPerRun: 'number',
  deletionMethods: 'array',
  arrRescanAfterDelete: 'boolean',
  unmonitorWhenKeeperElsewhere: 'boolean',
  addExclusionWhenKeeperElsewhere: 'boolean',
  recycleBinPath: 'string',
  recycleBinCleanupDays: 'number',
  refreshPlexAfterDelete: 'boolean',
  cleanupPlexStaleEntries: 'boolean',
  treatEditionsAsDistinct: 'boolean',
  treat3DAsDistinct: 'boolean',
  languageVariantsAsDistinct: 'boolean',
  differentArrInstancesIntentional: 'boolean',
  maxGroupSize: 'number',
  stableScansRequired: 'number',
  maxBytesPerRunGb: 'number',
  durationTolerancePercent: 'number',
  durationToleranceMinutes: 'number',
  historyRetentionDays: 'number',
  backupIntervalDays: 'number',
  backupRetentionDays: 'number',
  detectDiscs: 'boolean',
  allowDiscRemoval: 'boolean',
  keepPlayableCopy: 'boolean',
};

/**
 * Settings fields that are missing or have the wrong JSON type (e.g. the server returned `{}` or
 * an older/partial object). Empty when `s` is a complete `Settings`.
 */
export function incompleteSettingsFields(s: unknown): (keyof Settings)[] {
  if (!s || typeof s !== 'object' || Array.isArray(s)) return Object.keys(SETTINGS_FIELD_TYPES) as (keyof Settings)[];
  const obj = s as Record<string, unknown>;
  return (Object.entries(SETTINGS_FIELD_TYPES) as [keyof Settings, SettingsFieldType][])
    .filter(([key, type]) => {
      const v = obj[key];
      if (type === 'array') return !Array.isArray(v);
      if (type === 'number') return typeof v !== 'number' || !Number.isFinite(v);
      return typeof v !== type;
    })
    .map(([key]) => key);
}

/** True when `s` has every Settings field with the right JSON type. */
export function isCompleteSettings(s: unknown): s is Settings {
  return incompleteSettingsFields(s).length === 0;
}

/** Last path segment ("/data/.dupearr-recycle" → ".dupearr-recycle"). */
function baseName(p: string): string {
  const parts = comparablePath(p).split('/');
  return parts[parts.length - 1] ?? '';
}

export interface PathProblems {
  errors: string[];
  warnings: string[];
}

/**
 * Safety checks of the recycle bin against the mapped media folders (local side of the path
 * mappings). Files in the bin are permanently purged after `recycleBinCleanupDays`, so the bin must
 * never be a filesystem root or contain a media folder. A bin inside a media folder only warns:
 * Plex/*arr may scan it unless it is a dot-folder (Dupearr writes a `.plexignore`).
 */
export function recycleBinProblems(recycleBinPath: string, mappings: readonly Pick<PathMapping, 'localPath'>[] = []): PathProblems {
  const out: PathProblems = { errors: [], warnings: [] };
  const bin = recycleBinPath.trim();
  if (!bin) return out;
  if (!isAbsolutePath(bin)) {
    out.errors.push('Use an absolute path, e.g. /data/.dupearr-recycle');
    return out;
  }
  if (isFilesystemRoot(bin)) {
    out.errors.push('The recycle bin cannot be a filesystem root — use a dedicated folder such as /data/.dupearr-recycle.');
    return out;
  }
  const seen = new Set<string>();
  for (const m of mappings) {
    const local = (m.localPath ?? '').trim();
    if (!local || seen.has(comparablePath(local))) continue;
    seen.add(comparablePath(local));
    if (isSameOrInside(bin, local)) {
      out.errors.push(
        `The recycle bin must not contain the media folder ${local} (path mapping): its cleanup permanently deletes what is inside. Use a dedicated folder such as /data/.dupearr-recycle.`,
      );
    } else if (isSameOrInside(local, bin) && !baseName(bin).startsWith('.')) {
      out.warnings.push(
        `The recycle bin is inside the media folder ${local}: Plex or the *arrs may pick up removed files. Prefer a hidden folder (e.g. .dupearr-recycle) or one outside your library folders.`,
      );
    }
  }
  return out;
}

export interface ValidateSettingsOptions {
  /** Path mappings (for the recycle bin checks). */
  mappings?: readonly Pick<PathMapping, 'localPath'>[];
}

/** Numeric Settings fields. */
export type NumericSettingsField = {
  [K in keyof Settings]-?: Settings[K] extends number ? K : never;
}[keyof Settings];

export interface NumberLimit {
  min: number;
  max: number;
  /** Whole numbers only (every field except the duration tolerance percentage). */
  integer: boolean;
  /** Message when the value is below `min` (default: "Must be between min and max"). */
  belowMin?: string;
}

/** Shortest scheduled full-scan interval in minutes; 0 turns scheduled scans off. */
export const MIN_SCAN_INTERVAL_MINUTES = 15;

/**
 * The server's limits of every numeric setting (internal/api/settings.go validateSettings). A 0
 * where allowed means: scan interval → scheduled scans off, minimum age → off, recycle bin cleanup
 * and history/backup retention → keep forever, backup interval → scheduled backups off.
 */
export const SETTINGS_NUMBER_LIMITS: Readonly<Record<NumericSettingsField, NumberLimit>> = {
  scanIntervalMinutes: { min: 0, max: 525_600, integer: true },
  minAgeHours: { min: 0, max: 87_600, integer: true, belowMin: 'Cannot be negative' },
  // Circuit breakers can never be switched off (docs/DECISIONS.md D6).
  maxDeletionsPerRun: {
    min: 1,
    max: 10_000,
    integer: true,
    belowMin: 'Must be at least 1 — this limit cannot be turned off',
  },
  maxBytesPerRunGb: {
    min: 1,
    max: 1_000_000,
    integer: true,
    belowMin: 'Must be at least 1 GB — this limit cannot be turned off',
  },
  recycleBinCleanupDays: { min: 0, max: 3650, integer: true },
  maxGroupSize: { min: 2, max: 100, integer: true },
  stableScansRequired: { min: 1, max: 100, integer: true, belowMin: 'Must be at least 1' },
  durationTolerancePercent: { min: 0, max: 100, integer: false },
  durationToleranceMinutes: { min: 0, max: 600, integer: true },
  historyRetentionDays: { min: 0, max: 36_500, integer: true },
  backupIntervalDays: { min: 0, max: 365, integer: true },
  backupRetentionDays: { min: 0, max: 3650, integer: true },
};

/** Why a numeric setting would be refused by the server (null when it is accepted). */
export function numberSettingProblem(field: NumericSettingsField, value: number): string | null {
  const limit = SETTINGS_NUMBER_LIMITS[field];
  const range = `Must be between ${limit.min} and ${limit.max}`;
  if (typeof value !== 'number' || !Number.isFinite(value)) return limit.belowMin ?? range;
  if (value < limit.min) return limit.belowMin ?? range;
  if (value > limit.max) return range;
  if (limit.integer && !Number.isInteger(value)) return 'Must be a whole number';
  if (field === 'scanIntervalMinutes' && value > 0 && value < MIN_SCAN_INTERVAL_MINUTES) {
    return `Must be 0 (scheduled scans off) or at least ${MIN_SCAN_INTERVAL_MINUTES} minutes`;
  }
  return null;
}

/** The numeric fields Media Management shows (the backup fields live on Settings → General). */
const MEDIA_MANAGEMENT_NUMBER_FIELDS: readonly NumericSettingsField[] = [
  'scanIntervalMinutes',
  'minAgeHours',
  'maxDeletionsPerRun',
  'maxBytesPerRunGb',
  'recycleBinCleanupDays',
  'durationTolerancePercent',
  'durationToleranceMinutes',
  'maxGroupSize',
  'historyRetentionDays',
];

/** Client-side checks before saving (the server re-validates). */
export function validateSettings(s: Settings, opts: ValidateSettingsOptions = {}): Partial<Record<keyof Settings, string[]>> {
  const e: Partial<Record<keyof Settings, string[]>> = {};
  if ((s.deletionMethods ?? []).length === 0) e.deletionMethods = ['Choose at least one deletion method'];
  const bin = recycleBinProblems(s.recycleBinPath ?? '', opts.mappings);
  if (bin.errors.length > 0) e.recycleBinPath = bin.errors;
  // Shown (and editable) only in automatic mode.
  const fields = s.mode === 'auto' ? [...MEDIA_MANAGEMENT_NUMBER_FIELDS, 'stableScansRequired' as const] : MEDIA_MANAGEMENT_NUMBER_FIELDS;
  for (const field of fields) {
    const problem = numberSettingProblem(field, s[field]);
    if (problem) e[field] = [problem];
  }
  return e;
}
