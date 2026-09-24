/**
 * Pure helpers for the decision-profile editor (docs/ARCHITECTURE.md §4.3, docs/DECISIONS.md D4–D5):
 * criterion defaults, schema lookups with graceful fallbacks, type-specific wording, the live
 * plain-English explanation, client-side validation and mapping of server validation errors.
 *
 * Everything here is framework-free so it can be unit tested directly.
 */
import type {
  ArrInstance,
  Criterion,
  CriterionKind,
  CriterionSchema,
  CriterionType,
  Direction,
  KeepPer,
  Library,
  PatternScore,
  Profile,
  ProfileInput,
  ProfileSchema,
  Protection,
  ProtectionType,
  SchemaOption,
  ValidationFailure,
} from '@/api/types';
import {
  AUDIO_FORMAT_LABELS,
  AUDIO_FORMAT_ORDER,
  CONTAINER_LABELS,
  CRITERION_KIND_BY_TYPE,
  CRITERION_TYPE_LABELS,
  DYNAMIC_RANGE_LABELS,
  DYNAMIC_RANGE_SHORT_LABELS,
  KEEP_PER_LABELS,
  PROTECTION_TYPE_LABELS,
  RESOLUTION_LABELS,
  RESOLUTION_ORDER,
  SOURCE_LABELS,
  SOURCE_ORDER,
  VIDEO_CODEC_LABELS,
  VIDEO_CODEC_ORDER,
  labelOf,
  toOptions,
} from '@/lib/constants';
import { splitPropertyPath, validateGlob, validateRegex } from './validation';

// ---------------------------------------------------------------------------
// Draft type
// ---------------------------------------------------------------------------

/** What the editor edits: a profile body without server-managed timestamps. */
export type ProfileDraft = ProfileInput;

/** Default tag protecting *arr items from removal (docs/DECISIONS.md D4). */
export const DEFAULT_KEEP_TAG = 'dupearr-keep';

/** Every protection type the UI knows about (arr_tag per docs/DECISIONS.md D4). */
export const KNOWN_PROTECTION_TYPES: readonly ProtectionType[] = ['path_glob', 'library', 'arr_instance', 'arr_tag'];

const KNOWN_KINDS: readonly CriterionKind[] = ['ordered', 'numeric', 'boolean', 'patterns'];

/** Editor flavour of a criterion row; "none" = no parameters (health, unknown kinds). */
export type EditorKind = CriterionKind | 'none';

// ---------------------------------------------------------------------------
// Schema lookups (with fallbacks for older / partial servers)
// ---------------------------------------------------------------------------

export type SchemaMap = ReadonlyMap<CriterionType, CriterionSchema>;

export function schemaMap(schema: ProfileSchema | undefined | null): SchemaMap {
  return new Map((schema?.criteria ?? []).map((c) => [c.type, c]));
}

/** Which editor a criterion row shows. Health and Played never have parameters. */
export function editorKind(type: CriterionType, schema?: CriterionSchema): EditorKind {
  if (type === 'health' || type === 'played') return 'none';
  const kind = schema?.kind ?? CRITERION_KIND_BY_TYPE[type];
  return kind && KNOWN_KINDS.includes(kind) ? kind : 'none';
}

export function criterionLabel(type: CriterionType, schema?: CriterionSchema): string {
  return schema?.label || labelOf(CRITERION_TYPE_LABELS, type);
}

/** Dynamic range default per docs/DECISIONS.md D5 (DV without fallback ranks below HLG). */
const DYNAMIC_RANGE_DEFAULT = ['dv_hdr10', 'hdr10plus', 'hdr10', 'hlg', 'dv', 'sdr'];
/**
 * Container default per docs/DECISIONS.md D5; a standalone `.m2ts` (outside a disc structure) ranks
 * after the common containers and before the unknown ones.
 */
const CONTAINER_DEFAULT = ['mkv', 'mp4', 'm4v', 'm2ts', 'other', 'avi', 'ts'];

const FALLBACK_ORDER: Partial<Record<CriterionType, readonly string[]>> = {
  resolution: RESOLUTION_ORDER,
  dynamic_range: DYNAMIC_RANGE_DEFAULT,
  source: SOURCE_ORDER,
  video_codec: VIDEO_CODEC_ORDER,
  audio_format: AUDIO_FORMAT_ORDER,
  container: CONTAINER_DEFAULT,
  library: [],
};

const FALLBACK_LABELS: Partial<Record<CriterionType, Readonly<Record<string, string>>>> = {
  resolution: RESOLUTION_LABELS,
  dynamic_range: DYNAMIC_RANGE_LABELS,
  source: SOURCE_LABELS,
  video_codec: VIDEO_CODEC_LABELS,
  audio_format: AUDIO_FORMAT_LABELS,
  container: CONTAINER_LABELS,
};

/** Default order of an ordered criterion (schema first, then the built-in default). */
export function defaultOrder(type: CriterionType, schema?: CriterionSchema): string[] {
  if (schema?.defaultOrder && schema.defaultOrder.length > 0) return [...schema.defaultOrder];
  return [...(FALLBACK_ORDER[type] ?? [])];
}

/**
 * All selectable values of an ordered criterion. `library` values are Dupearr library ids (as
 * strings) taken from the configured libraries; values in `current` that are unknown (e.g. a
 * deleted library) are kept so they can still be removed.
 */
export function orderedOptions(
  type: CriterionType,
  schema: CriterionSchema | undefined,
  libraries: readonly Library[] = [],
  current: readonly string[] = [],
): SchemaOption[] {
  let options: SchemaOption[];
  if (type === 'library') {
    options = libraries.map((l) => ({ value: String(l.id), label: l.title || `Library ${l.id}` }));
  } else if (schema?.options && schema.options.length > 0) {
    options = [...schema.options];
  } else {
    const labels = FALLBACK_LABELS[type];
    options = labels ? toOptions(labels, FALLBACK_ORDER[type] as string[] | undefined) : [];
  }
  const known = new Set(options.map((o) => o.value));
  for (const v of current) {
    if (!known.has(v)) {
      options.push({ value: v, label: type === 'library' ? `Unknown library (${v})` : v });
      known.add(v);
    }
  }
  return options;
}

/** Protection types offered in the editor: the schema's plus every known type (arr_tag included). */
export function protectionTypes(schema: ProfileSchema | undefined | null): ProtectionType[] {
  const out: ProtectionType[] = [...KNOWN_PROTECTION_TYPES];
  for (const t of schema?.protectionTypes ?? []) if (!out.includes(t)) out.push(t);
  return out;
}

export function keepPerValues(schema: ProfileSchema | undefined | null): KeepPer[] {
  const values = schema?.keepPer && schema.keepPer.length > 0 ? [...schema.keepPer] : (['', 'resolution', 'dynamic_range'] as KeepPer[]);
  return values.includes('') ? values : ['', ...values];
}

export function keepPerLabel(value: KeepPer | string): string {
  return value === '' ? KEEP_PER_LABELS[''] : labelOf(KEEP_PER_LABELS, value);
}

export function protectionTypeLabel(type: ProtectionType | string): string {
  return labelOf(PROTECTION_TYPE_LABELS, type);
}

// ---------------------------------------------------------------------------
// Direction wording
// ---------------------------------------------------------------------------

export interface DirectionWording {
  /** Option label in the Direction select. */
  higher: string;
  lower: string;
  /** Noun phrase used in the explanation ("the newer file"). */
  higherPhrase: string;
  lowerPhrase: string;
}

const DIRECTION_WORDING: Partial<Record<CriterionType, DirectionWording>> = {
  date_added: { higher: 'Newer', lower: 'Older', higherPhrase: 'the newer file', lowerPhrase: 'the older file' },
  file_size: { higher: 'Larger', lower: 'Smaller', higherPhrase: 'the larger file', lowerPhrase: 'the smaller file' },
  video_bitrate: {
    higher: 'Higher bitrate',
    lower: 'Lower bitrate',
    higherPhrase: 'the higher video bitrate',
    lowerPhrase: 'the lower video bitrate',
  },
  audio_channels: {
    higher: 'More channels',
    lower: 'Fewer channels',
    higherPhrase: 'more audio channels',
    lowerPhrase: 'fewer audio channels',
  },
  bit_depth: {
    higher: 'Higher bit depth',
    lower: 'Lower bit depth',
    higherPhrase: 'the higher bit depth',
    lowerPhrase: 'the lower bit depth',
  },
  custom_format_score: {
    higher: 'Higher score',
    lower: 'Lower score',
    higherPhrase: 'the higher custom format score',
    lowerPhrase: 'the lower custom format score',
  },
  audio_track_count: {
    higher: 'More audio tracks',
    lower: 'Fewer audio tracks',
    higherPhrase: 'more audio tracks',
    lowerPhrase: 'fewer audio tracks',
  },
  subtitle_track_count: {
    higher: 'More subtitle tracks',
    lower: 'Fewer subtitle tracks',
    higherPhrase: 'more subtitle tracks',
    lowerPhrase: 'fewer subtitle tracks',
  },
  // Fixed direction (the server refuses "lower"): only the "higher" wording is ever shown.
  last_played: {
    higher: 'More recently played',
    lower: 'Less recently played',
    higherPhrase: 'the most recently played file',
    lowerPhrase: 'the most recently played file',
  },
};

/** Type-specific wording for a numeric criterion's direction (e.g. date_added → Newer/Older). */
export function directionWording(type: CriterionType, schema?: CriterionSchema): DirectionWording {
  const known = DIRECTION_WORDING[type];
  if (known) return known;
  const noun = criterionLabel(type, schema).toLowerCase();
  return {
    higher: 'Higher is better',
    lower: 'Lower is better',
    higherPhrase: `the higher ${noun}`,
    lowerPhrase: `the lower ${noun}`,
  };
}

/**
 * Unit of a criterion's absolute minimum difference: "points" for custom format scores, "days" for
 * Last played (the schema's `minDeltaUnit` when the server names one).
 */
export function minDeltaUnit(type: CriterionType, schema?: CriterionSchema): string {
  if (schema?.minDeltaUnit) return schema.minDeltaUnit;
  if (type === 'last_played') return 'days';
  return type === 'custom_format_score' ? 'points' : '';
}

/** Whether the row offers the absolute "Min delta" field. */
export function supportsMinDelta(type: CriterionType, schema?: CriterionSchema): boolean {
  return type === 'custom_format_score' || type === 'last_played' || !!schema?.minDeltaUnit;
}

/** Largest minimum difference (days) the server accepts for Last played. */
export const MAX_LAST_PLAYED_DAYS = 3650;

/**
 * Criteria whose direction is fixed: Last played always prefers the more recently played copy
 * (preferring the unplayed one would make a play a reason to remove, docs/DECISIONS.md D10).
 */
export function fixedDirection(type: CriterionType): boolean {
  return type === 'last_played';
}

/** Whether a criterion ranks by play history, which needs a Tautulli connection. */
export function requiresWatchHistory(type: CriterionType, schema?: CriterionSchema): boolean {
  return schema?.requiresWatchHistory ?? (type === 'played' || type === 'last_played');
}

// ---------------------------------------------------------------------------
// Criterion defaults / normalization
// ---------------------------------------------------------------------------

/** Tolerances recommended by docs/DECISIONS.md D5 for newly added criteria. */
const DEFAULT_TOLERANCE: Partial<Record<CriterionType, number>> = { video_bitrate: 15, file_size: 5 };

/** A new, enabled criterion with sensible defaults for its kind. */
export function newCriterion(type: CriterionType, schema?: CriterionSchema): Criterion {
  const kind = editorKind(type, schema);
  const c: Criterion = { type, enabled: true };
  if (kind === 'ordered') c.order = defaultOrder(type, schema);
  if (kind === 'numeric') {
    c.direction = schema?.defaultDirection === 'lower' ? 'lower' : 'higher';
    const tol = DEFAULT_TOLERANCE[type];
    if (tol !== undefined && (schema?.supportsTolerance ?? true)) c.tolerancePercent = tol;
    if (supportsMinDelta(type, schema)) c.minDelta = type === 'last_played' ? 30 : 10;
  }
  if (kind === 'patterns') c.patterns = [];
  return c;
}

/** A blank pattern row for the filename score editor. */
export function newPattern(): PatternScore {
  return { pattern: '', score: 10, regex: false, caseSensitive: false };
}

/** Default value of a protection when its type is (re)selected. */
export function defaultProtectionValue(type: ProtectionType | string): string {
  return type === 'arr_tag' ? DEFAULT_KEEP_TAG : '';
}

/** Tolerates null slices from the server and fills missing fields. */
export function normalizeProfile(p: Partial<Profile> | ProfileDraft): ProfileDraft {
  return {
    ...(p.id ? { id: p.id } : {}),
    name: p.name ?? '',
    isDefault: !!p.isDefault,
    criteria: (p.criteria ?? []).map((c) => ({
      ...c,
      ...(c.order ? { order: [...c.order] } : {}),
      ...(c.patterns ? { patterns: c.patterns.map((x) => ({ ...x })) } : {}),
    })),
    keepCount: p.keepCount && p.keepCount >= 1 ? p.keepCount : 1,
    keepPer: (p.keepPer ?? '') as KeepPer,
    protections: (p.protections ?? []).map((x) => ({ ...x })),
  };
}

/** A copy of a template (or existing profile) ready to be created as a new profile. */
export function draftFromTemplate(template: Profile | ProfileDraft, existingNames: readonly string[]): ProfileDraft {
  const base = normalizeProfile(template);
  delete base.id;
  return { ...base, isDefault: false, name: uniqueName(base.name || 'New Profile', existingNames) };
}

/** "Name", "Name (2)", "Name (3)"… — first one not in `existing` (case-insensitive). */
export function uniqueName(name: string, existing: readonly string[]): string {
  const taken = new Set(existing.map((n) => n.trim().toLowerCase()));
  const base = name.trim() || 'New Profile';
  if (!taken.has(base.toLowerCase())) return base;
  for (let i = 2; i < 1000; i++) {
    const candidate = `${base} (${i})`;
    if (!taken.has(candidate.toLowerCase())) return candidate;
  }
  return `${base} (${Date.now()})`;
}

/**
 * Body sent to the server: trimmed name/protection values, integer keep count, and optional
 * fields that are empty dropped (they are `omitempty` on the Go side).
 */
export function toProfileInput(draft: ProfileDraft): ProfileDraft {
  return {
    ...(draft.id ? { id: draft.id } : {}),
    name: draft.name.trim(),
    isDefault: draft.isDefault,
    keepCount: Math.max(1, Math.round(draft.keepCount || 1)),
    keepPer: draft.keepPer,
    criteria: draft.criteria.map((c) => {
      const out: Criterion = { ...c };
      if (!out.value) delete out.value;
      if (!out.tolerancePercent) delete out.tolerancePercent;
      if (!out.minDelta) delete out.minDelta;
      return out;
    }),
    protections: draft.protections.map((p) => ({ type: p.type, value: p.value.trim() })),
  };
}

// ---------------------------------------------------------------------------
// Languages (audio_language criterion)
// ---------------------------------------------------------------------------

export interface LanguageOption {
  /** ISO 639-2/B code as Plex reports it in `languageCode` (e.g. "eng"). */
  code: string;
  name: string;
}

/** Common audio languages (ISO 639-2/B, the form Plex uses for `languageCode`). */
export const COMMON_LANGUAGES: readonly LanguageOption[] = [
  { code: 'eng', name: 'English' },
  { code: 'spa', name: 'Spanish' },
  { code: 'fre', name: 'French' },
  { code: 'ger', name: 'German' },
  { code: 'ita', name: 'Italian' },
  { code: 'por', name: 'Portuguese' },
  { code: 'dut', name: 'Dutch' },
  { code: 'swe', name: 'Swedish' },
  { code: 'nor', name: 'Norwegian' },
  { code: 'dan', name: 'Danish' },
  { code: 'fin', name: 'Finnish' },
  { code: 'pol', name: 'Polish' },
  { code: 'cze', name: 'Czech' },
  { code: 'hun', name: 'Hungarian' },
  { code: 'gre', name: 'Greek' },
  { code: 'tur', name: 'Turkish' },
  { code: 'rus', name: 'Russian' },
  { code: 'ukr', name: 'Ukrainian' },
  { code: 'heb', name: 'Hebrew' },
  { code: 'ara', name: 'Arabic' },
  { code: 'hin', name: 'Hindi' },
  { code: 'tha', name: 'Thai' },
  { code: 'vie', name: 'Vietnamese' },
  { code: 'ind', name: 'Indonesian' },
  { code: 'chi', name: 'Chinese' },
  { code: 'jpn', name: 'Japanese' },
  { code: 'kor', name: 'Korean' },
];

/** Language codes are 2 (ISO 639-1) or 3 (ISO 639-2) letters. */
export function isLanguageCode(value: string): boolean {
  return /^[a-z]{2,3}$/i.test(value.trim());
}

export function languageName(code: string | undefined): string {
  if (!code) return '';
  const hit = COMMON_LANGUAGES.find((l) => l.code === code.toLowerCase());
  return hit ? hit.name : code;
}

// ---------------------------------------------------------------------------
// Explanation
// ---------------------------------------------------------------------------

/** Context used to name libraries / *arr instances in explanations. */
export interface ExplainContext {
  schema: SchemaMap;
  libraries?: readonly Library[];
  arrInstances?: readonly ArrInstance[];
}

const ORDERED_NOUNS: Partial<Record<CriterionType, string>> = {
  resolution: 'resolution',
  dynamic_range: 'dynamic range',
  source: 'source',
  video_codec: 'video codec',
  audio_format: 'audio format of the best track',
  container: 'container',
  library: 'library',
};

/** Compact value label for chains: "2160p", "DV HDR10", "HEVC" (parenthetical details dropped). */
function shortValueLabel(type: CriterionType, value: string, options: readonly SchemaOption[]): string {
  if (type === 'resolution') return value === 'sd' ? 'SD' : /^\d+$/.test(value) ? `${value}p` : value;
  if (type === 'dynamic_range') {
    const short = (DYNAMIC_RANGE_SHORT_LABELS as Record<string, string>)[value];
    if (short) return short;
  }
  const label = options.find((o) => o.value === value)?.label ?? value;
  // Long labels lose a trailing "(…)" detail; short ones like "AVC (H.264)" stay informative.
  return label.length > 14 ? label.replace(/\s*\([^)]*\)\s*$/, '') || label : label;
}

function chain(type: CriterionType, order: readonly string[], options: readonly SchemaOption[], max = 3): string {
  const shown = order.slice(0, max).map((v) => shortValueLabel(type, v, options));
  if (order.length > max) shown.push('…');
  return shown.join(' > ');
}

/** True when `order` ranks values in the same relative order as `reference` (a "best first" list). */
function followsOrder(order: readonly string[], reference: readonly string[]): boolean {
  let last = -1;
  for (const v of order) {
    const i = reference.indexOf(v);
    if (i < 0 || i < last) return false;
    last = i;
  }
  return order.length > 0;
}

function formatNumber(n: number): string {
  return Number.isInteger(n) ? String(n) : String(Number(n.toFixed(2)));
}

type NameContext = Pick<ExplainContext, 'libraries' | 'arrInstances'>;

function instanceName(id: string, ctx: NameContext): string {
  const inst = ctx.arrInstances?.find((a) => String(a.id) === id);
  return inst ? inst.name : `*arr instance #${id}`;
}

function libraryName(id: string, ctx: NameContext): string {
  const lib = ctx.libraries?.find((l) => String(l.id) === id);
  return lib ? lib.title : `library #${id}`;
}

/** Noun phrase describing what a criterion prefers ("the newer file", "the highest resolution (2160p > …)"). */
export function describeCriterion(c: Criterion, ctx: ExplainContext): string {
  const schema = ctx.schema.get(c.type);
  const kind = editorKind(c.type, schema);
  if (c.type === 'health') return 'the healthiest file';
  if (c.type === 'played') return 'a file that has been played (Tautulli; an unknown play history is a tie)';
  if (c.type === 'last_played') {
    const days = c.minDelta && c.minDelta > 0 ? formatNumber(c.minDelta) : '';
    return days
      ? `the most recently played file (plays less than ${days} days apart count as a tie)`
      : 'the most recently played file (an unknown play history is a tie)';
  }

  switch (kind) {
    case 'ordered': {
      const order = c.order ?? [];
      const options = orderedOptions(c.type, schema, ctx.libraries, order);
      const noun = ORDERED_NOUNS[c.type] ?? criterionLabel(c.type, schema).toLowerCase();
      if (order.length === 0) return `the preferred ${noun} (no values chosen yet)`;
      const listed = c.type === 'library' ? order.map((id) => libraryName(id, ctx)) : null;
      const values = listed ? listed.slice(0, 3).join(' > ') + (listed.length > 3 ? ' > …' : '') : chain(c.type, order, options);
      if (c.type === 'resolution' && followsOrder(order, RESOLUTION_ORDER)) return `the highest resolution (${values})`;
      return `the preferred ${noun} (${values})`;
    }
    case 'numeric': {
      const w = directionWording(c.type, schema);
      let phrase = c.direction === 'lower' ? w.lowerPhrase : w.higherPhrase;
      const notes: string[] = [];
      if (c.type === 'video_bitrate') notes.push('same codec only');
      if (c.type === 'custom_format_score') notes.push('same *arr instance only');
      if (c.tolerancePercent && c.tolerancePercent > 0) {
        notes.push(`differences under ${formatNumber(c.tolerancePercent)}% count as a tie`);
      }
      if (c.minDelta && c.minDelta > 0) {
        const unit = minDeltaUnit(c.type, schema);
        notes.push(`differences under ${formatNumber(c.minDelta)}${unit ? ` ${unit}` : ''} count as a tie`);
      }
      if (notes.length > 0) phrase += ` (${notes.join('; ')})`;
      return phrase;
    }
    case 'boolean': {
      if (c.type === 'arr_managed') {
        return c.value ? `a file tracked by ${instanceName(c.value, ctx)}` : 'a file tracked by Radarr/Sonarr';
      }
      if (c.type === 'audio_language') {
        const name = languageName(c.value);
        return c.value
          ? `a file with ${/^[aeiou]/i.test(name) ? 'an' : 'a'} ${name} audio track`
          : 'a file with the chosen audio language (none chosen yet)';
      }
      return `a file matching “${criterionLabel(c.type, schema)}”`;
    }
    case 'patterns': {
      const patterns = (c.patterns ?? []).filter((p) => p.pattern.trim());
      if (patterns.length === 0) return 'the highest filename score (no patterns yet)';
      const shown = patterns
        .slice(0, 2)
        .map((p) => `${p.score >= 0 ? '+' : '−'}${Math.abs(p.score)} ${p.pattern}`)
        .join(', ');
      return `the highest filename score (${shown}${patterns.length > 2 ? ', …' : ''})`;
    }
    default:
      return `the better ${criterionLabel(c.type, schema).toLowerCase()}`;
  }
}

/** Built-in final tiebreak (always appended by the engine, docs/DECISIONS.md D5). */
export const FINAL_TIEBREAK =
  'Remaining ties go to the *arr-managed file, then the larger file, then the older file, then the lower Plex media id.';

/**
 * One sentence explaining the enabled criteria in order, e.g.
 * "Keep the healthiest file; then the highest resolution (2160p > 1440p > 1080p > …); if still tied,
 * prefer the newer file."
 */
export function explainCriteria(criteria: readonly Criterion[], ctx: ExplainContext): string {
  const parts = explainCriteriaParts(criteria, ctx);
  return parts.length === 1 && !criteria.some((c) => c.enabled) ? parts[0]! : `${parts.join('; ')}.`;
}

/**
 * The clauses of `explainCriteria` ("Keep the healthiest file", "then …", "if still tied, prefer …"),
 * for rendering one clause per line. Without enabled criteria: a single complete sentence.
 */
export function explainCriteriaParts(criteria: readonly Criterion[], ctx: ExplainContext): string[] {
  const enabled = criteria.filter((c) => c.enabled);
  if (enabled.length === 0) return ['No criteria are enabled, so only the built-in tiebreak decides.'];
  return enabled.map((c, i) => {
    const phrase = describeCriterion(c, ctx);
    if (i === 0) return `Keep ${phrase}`;
    if (i === enabled.length - 1) return `if still tied, prefer ${phrase}`;
    return `then ${phrase}`;
  });
}

/**
 * One-line summary of a criterion's parameters for a collapsed row ("2160p > 1440p > 1080p > …",
 * "Larger · 5% tolerance", "Radarr 4K", "2 patterns"); "" when it has none.
 */
export function criterionSummary(c: Criterion, ctx: ExplainContext): string {
  const schema = ctx.schema.get(c.type);
  switch (editorKind(c.type, schema)) {
    case 'ordered': {
      const order = c.order ?? [];
      if (order.length === 0) return 'No values ranked';
      if (c.type === 'library') {
        const names = order.map((id) => libraryName(id, ctx));
        return names.slice(0, 4).join(' > ') + (names.length > 4 ? ' > …' : '');
      }
      return chain(c.type, order, orderedOptions(c.type, schema, ctx.libraries, order), 4);
    }
    case 'numeric': {
      const w = directionWording(c.type, schema);
      const parts = [c.direction === 'lower' && !fixedDirection(c.type) ? w.lower : w.higher];
      if (c.tolerancePercent && c.tolerancePercent > 0) parts.push(`${formatNumber(c.tolerancePercent)}% tolerance`);
      if (c.minDelta && c.minDelta > 0) {
        const unit = minDeltaUnit(c.type, schema);
        parts.push(`min delta ${formatNumber(c.minDelta)}${unit ? ` ${unit}` : ''}`);
      }
      return parts.join(' · ');
    }
    case 'boolean':
      if (c.type === 'arr_managed') return c.value ? instanceName(c.value, ctx) : 'Any *arr';
      if (c.type === 'audio_language') return c.value ? `${languageName(c.value)} (${c.value})` : 'No language chosen';
      return '';
    case 'patterns': {
      const n = (c.patterns ?? []).length;
      return n === 0 ? 'No patterns' : `${n} pattern${n === 1 ? '' : 's'}`;
    }
    default:
      return '';
  }
}

/** Explains keepCount / keepPer. */
export function explainKeep(keepCount: number, keepPer: KeepPer | string): string {
  const n = Math.max(1, Math.round(keepCount || 1));
  const copies = n === 1 ? 'copy' : `${n} copies`;
  if (keepPer === 'resolution') {
    return `Splits each group by resolution first and keeps the best ${copies} of each — e.g. the best 4K AND the best 1080p.`;
  }
  if (keepPer === 'dynamic_range') {
    return `Splits each group by dynamic range first and keeps the best ${copies} of each — e.g. the best HDR AND the best SDR copy.`;
  }
  return n === 1
    ? 'Keeps the single best copy; every other copy is proposed for removal.'
    : `Keeps the best ${n} copies; every other copy is proposed for removal.`;
}

/** Human description of one protection rule. */
export function describeProtection(p: Protection, ctx: NameContext): string {
  const value = p.value.trim();
  switch (p.type) {
    case 'library':
      return value ? `files in the ${libraryName(value, ctx)} library` : 'files in a library (none chosen)';
    case 'arr_instance':
      return value ? `files tracked by ${instanceName(value, ctx)}` : 'files tracked by an *arr (none chosen)';
    case 'arr_tag':
      return value ? `items tagged “${value}” in Radarr/Sonarr` : 'items with a Radarr/Sonarr tag (none entered)';
    case 'path_glob':
      return value ? `paths matching ${value}` : 'paths matching a pattern (none entered)';
    default:
      return `${protectionTypeLabel(p.type)}: ${value}`;
  }
}

/** Sentence listing the protections, or null when there are none. */
export function explainProtections(protections: readonly Protection[], ctx: NameContext): string | null {
  if (protections.length === 0) return null;
  return `Never removes ${protections.map((p) => describeProtection(p, ctx)).join('; ')}.`;
}

// ---------------------------------------------------------------------------
// Card summaries / templates
// ---------------------------------------------------------------------------

/** Labels of the first `n` enabled criteria plus how many more there are. */
export function summarizeCriteria(
  criteria: readonly Criterion[] | null | undefined,
  schema: SchemaMap,
  n = 3,
): { labels: string[]; more: number } {
  const enabled = (criteria ?? []).filter((c) => c.enabled);
  return { labels: enabled.slice(0, n).map((c) => criterionLabel(c.type, schema.get(c.type))), more: Math.max(0, enabled.length - n) };
}

/** Short keep summary: "Keep best 1", "Keep best 2 per resolution". */
export function keepSummary(keepCount: number, keepPer: KeepPer | string): string {
  const n = Math.max(1, Math.round(keepCount || 1));
  const per = keepPer === 'resolution' ? ' per resolution' : keepPer === 'dynamic_range' ? ' per dynamic range' : '';
  return `Keep best ${n}${per}`;
}

const TEMPLATE_DESCRIPTIONS: Record<string, string> = {
  'keep highest quality':
    'Keeps the best-looking copy: a healthy file first, then resolution, HDR, source, custom format score, bitrate and audio. The recommended default.',
  'keep one per resolution':
    'The same quality rules, but keeps the best copy of each resolution — e.g. one 4K and one 1080p copy.',
  'save space':
    'Keeps a healthy copy at your preferred resolution in the most efficient codec (AV1/HEVC), then the smallest file.',
  'maximum compatibility':
    'Prefers copies every client can direct-play: H.264, SDR or Dolby Vision with an HDR10 fallback, AC3/EAC3 audio, MP4/MKV.',
  'trust my *arr':
    'Keeps the file your Radarr/Sonarr manages (then the higher custom format score), and falls back to the quality rules.',
};

/** Description of a built-in template (known names) or a generated summary of its rules. */
export function templateDescription(template: Profile | ProfileDraft, schema: SchemaMap): string {
  const known = TEMPLATE_DESCRIPTIONS[template.name.trim().toLowerCase()];
  if (known) return known;
  const { labels, more } = summarizeCriteria(template.criteria, schema, 3);
  const rules = labels.length > 0 ? `Decides by ${labels.join(', ')}${more > 0 ? ` and ${more} more` : ''}.` : 'No criteria.';
  return `${rules} ${keepSummary(template.keepCount, template.keepPer)}.`;
}

// ---------------------------------------------------------------------------
// Errors (client-side validation + server 400 mapping)
// ---------------------------------------------------------------------------

/** Validation messages grouped by where the editor shows them. */
export interface ProfileErrors {
  /** Errors that match no field (shown in a summary alert). */
  general: string[];
  name: string[];
  isDefault: string[];
  keepCount: string[];
  keepPer: string[];
  /** Errors about the criteria list as a whole. */
  criteria: string[];
  /** Per criterion row, keyed by type (types are unique within a profile). */
  criterion: Partial<Record<CriterionType, string[]>>;
  /** Errors about the protections list as a whole. */
  protections: string[];
  /** Per protection row, keyed by index. */
  protection: Record<number, string[]>;
}

export function emptyProfileErrors(): ProfileErrors {
  return {
    general: [],
    name: [],
    isDefault: [],
    keepCount: [],
    keepPer: [],
    criteria: [],
    criterion: {},
    protections: [],
    protection: {},
  };
}

export function hasProfileErrors(e: ProfileErrors): boolean {
  return (
    e.general.length +
      e.name.length +
      e.isDefault.length +
      e.keepCount.length +
      e.keepPer.length +
      e.criteria.length +
      e.protections.length >
      0 ||
    Object.values(e.criterion).some((v) => v && v.length > 0) ||
    Object.values(e.protection).some((v) => v.length > 0)
  );
}

/** Total number of messages (for the summary line). */
export function countProfileErrors(e: ProfileErrors): number {
  return (
    e.general.length +
    e.name.length +
    e.isDefault.length +
    e.keepCount.length +
    e.keepPer.length +
    e.criteria.length +
    e.protections.length +
    Object.values(e.criterion).reduce((n, v) => n + (v?.length ?? 0), 0) +
    Object.values(e.protection).reduce((n, v) => n + v.length, 0)
  );
}

/** Problems of one filename pattern (null when valid). */
export function validatePattern(p: PatternScore): string | null {
  if (!p.pattern.trim()) return 'Pattern is required';
  if (!Number.isFinite(p.score)) return 'Score must be a number';
  return p.regex ? validateRegex(p.pattern) : validateGlob(p.pattern);
}

/** Client-side checks run before saving (the server re-validates everything). */
export function validateProfileDraft(draft: ProfileDraft, schema: SchemaMap): ProfileErrors {
  const e = emptyProfileErrors();
  if (!draft.name.trim()) e.name.push('Name is required');
  if (!Number.isInteger(draft.keepCount) || draft.keepCount < 1) e.keepCount.push('Keep count must be at least 1');

  const seen = new Set<CriterionType>();
  for (const c of draft.criteria) {
    const msgs: string[] = [];
    // The engine allows several filename_score steps (e.g. a high-priority and a fallback pattern
    // set); every other type may appear only once.
    if (seen.has(c.type) && c.type !== 'filename_score') msgs.push('This criterion is listed more than once');
    seen.add(c.type);
    if (c.enabled) {
      const kind = editorKind(c.type, schema.get(c.type));
      if (kind === 'ordered' && (c.order ?? []).length === 0) msgs.push('Choose at least one value to rank');
      // A fixed direction (Last played) has no select to fix it with; the server defaults it.
      if (kind === 'numeric' && !fixedDirection(c.type) && c.direction !== 'higher' && c.direction !== 'lower') {
        msgs.push('Choose a direction');
      }
      if (kind === 'numeric' && c.tolerancePercent !== undefined && (c.tolerancePercent < 0 || c.tolerancePercent > 100)) {
        msgs.push('Tolerance must be between 0 and 100%');
      }
      if (kind === 'numeric' && c.minDelta !== undefined && c.minDelta < 0) msgs.push('Min delta cannot be negative');
      if (c.type === 'last_played' && (c.minDelta ?? 0) > MAX_LAST_PLAYED_DAYS) {
        msgs.push(`The minimum difference can be at most ${MAX_LAST_PLAYED_DAYS} days`);
      }
      if (c.type === 'audio_language') {
        if (!c.value?.trim()) msgs.push('Choose an audio language');
        else if (!isLanguageCode(c.value)) msgs.push('Use a 2- or 3-letter language code, e.g. eng');
      }
    }
    // The server validates patterns even for a disabled step (engine.ValidateProfile).
    if (editorKind(c.type, schema.get(c.type)) === 'patterns') {
      const patterns = c.patterns ?? [];
      if (patterns.length === 0) msgs.push('Add at least one pattern');
      const bad = patterns.map(validatePattern).filter((m): m is string => m !== null);
      if (bad.length > 0) msgs.push(`${bad.length} pattern${bad.length === 1 ? ' is' : 's are'} invalid`);
    }
    if (msgs.length > 0) e.criterion[c.type] = [...(e.criterion[c.type] ?? []), ...msgs];
  }

  draft.protections.forEach((p, i) => {
    const msgs: string[] = [];
    if (!p.value.trim()) msgs.push('A value is required');
    else if (p.type === 'path_glob') {
      const err = validateGlob(p.value);
      if (err) msgs.push(err);
    }
    if (msgs.length > 0) e.protection[i] = msgs;
  });
  return e;
}

/**
 * Maps a server 400 `[{propertyName, errorMessage}]` onto the editor. Indices in property names
 * (`criteria[2].order`, `Criteria.2.Order`, `protections[0].value`) refer to the submitted body,
 * so criterion errors are re-keyed by the submitted criterion's type.
 */
export function mapProfileServerErrors(failures: readonly ValidationFailure[], submitted: ProfileDraft): ProfileErrors {
  const e = emptyProfileErrors();
  for (const f of failures) {
    const path = splitPropertyPath(f.propertyName);
    if (path[0] === 'profile') path.shift();
    const [head, index] = path;
    const msg = f.errorMessage || 'Invalid value';
    switch (head) {
      case 'name':
        e.name.push(msg);
        break;
      case 'isDefault':
        e.isDefault.push(msg);
        break;
      case 'keepCount':
        e.keepCount.push(msg);
        break;
      case 'keepPer':
        e.keepPer.push(msg);
        break;
      case 'criteria': {
        const c = typeof index === 'number' ? submitted.criteria[index] : undefined;
        if (c) (e.criterion[c.type] ??= []).push(msg);
        else e.criteria.push(msg);
        break;
      }
      case 'protections': {
        if (typeof index === 'number' && index >= 0 && index < submitted.protections.length) {
          (e.protection[index] ??= []).push(msg);
        } else {
          e.protections.push(msg);
        }
        break;
      }
      default:
        e.general.push(msg);
    }
  }
  return e;
}

/** Direction as a typed value (unknown/missing → "higher"). */
export function asDirection(value: string | undefined): Direction {
  return value === 'lower' ? 'lower' : 'higher';
}

/** Combines two error sets (e.g. live client-side checks + the last server response). */
export function mergeProfileErrors(a: ProfileErrors, b: ProfileErrors): ProfileErrors {
  const criterion: Partial<Record<CriterionType, string[]>> = { ...a.criterion };
  for (const [type, msgs] of Object.entries(b.criterion) as [CriterionType, string[] | undefined][]) {
    if (msgs && msgs.length > 0) criterion[type] = [...(criterion[type] ?? []), ...msgs];
  }
  const protection: Record<number, string[]> = { ...a.protection };
  for (const [i, msgs] of Object.entries(b.protection)) {
    protection[Number(i)] = [...(protection[Number(i)] ?? []), ...msgs];
  }
  return {
    general: [...a.general, ...b.general],
    name: [...a.name, ...b.name],
    isDefault: [...a.isDefault, ...b.isDefault],
    keepCount: [...a.keepCount, ...b.keepCount],
    keepPer: [...a.keepPer, ...b.keepPer],
    criteria: [...a.criteria, ...b.criteria],
    criterion,
    protections: [...a.protections, ...b.protections],
    protection,
  };
}

/** An empty profile ("start from scratch"): only the health criterion (when the server knows it). */
export function blankDraft(schema: SchemaMap, existingNames: readonly string[]): ProfileDraft {
  const withHealth = schema.size === 0 || schema.has('health');
  return {
    name: uniqueName('New Profile', existingNames),
    isDefault: false,
    criteria: withHealth ? [newCriterion('health', schema.get('health'))] : [],
    keepCount: 1,
    keepPer: '',
    protections: [],
  };
}
