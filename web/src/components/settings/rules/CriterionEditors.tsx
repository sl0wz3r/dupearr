/**
 * Per-kind parameter editors of a decision-profile criterion row (ordered / numeric / boolean /
 * patterns). Health has no parameters. See docs/ARCHITECTURE.md §4.3 and docs/DECISIONS.md D5.
 */
import { Plus, RotateCcw, Trash2 } from 'lucide-react';
import { useState } from 'react';
import type { ArrInstance, Criterion, CriterionSchema, Library, PatternScore } from '@/api/types';
import { Button, Checkbox, IconButton, NumberInput, OrderedListEditor, Select, TextInput } from '@/components/ui';
import {
  COMMON_LANGUAGES,
  MAX_LAST_PLAYED_DAYS,
  asDirection,
  criterionLabel,
  defaultOrder,
  directionWording,
  fixedDirection,
  isLanguageCode,
  minDeltaUnit,
  newPattern,
  orderedOptions,
  supportsMinDelta,
  validatePattern,
} from './criteria';
import { Field, FieldError } from './Field';

export interface CriterionEditorProps {
  criterion: Criterion;
  schema?: CriterionSchema;
  onChange: (next: Criterion) => void;
  disabled?: boolean;
}

function sameOrder(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

// ---------------------------------------------------------------------------
// Ordered
// ---------------------------------------------------------------------------

export interface OrderedCriterionEditorProps extends CriterionEditorProps {
  libraries: readonly Library[];
}

/** Ranked list of values (best first) with a "reset to default" shortcut. */
export function OrderedCriterionEditor({ criterion, schema, onChange, libraries, disabled }: OrderedCriterionEditorProps) {
  const label = criterionLabel(criterion.type, schema);
  const order = criterion.order ?? [];
  const options = orderedOptions(criterion.type, schema, libraries, order);
  const defaults = defaultOrder(criterion.type, schema);
  const isLibrary = criterion.type === 'library';
  // The engine ranks a value missing from the list below every listed value (it is NOT ignored):
  // removing "2160p" makes 4K copies lose to everything listed.
  const unlisted = options.filter((o) => !order.includes(o.value));

  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs text-muted">
        Best first. Copies are compared by the position of their value in this list; values that are not listed rank
        below every listed value.
      </div>
      <OrderedListEditor
        aria-label={`${label} order`}
        value={order}
        options={options}
        minItems={1}
        disabled={disabled}
        addPlaceholder={`Add ${label.toLowerCase()} value…`}
        emptyText={isLibrary ? 'No libraries ranked yet' : 'No values ranked yet'}
        onChange={(next) => onChange({ ...criterion, order: next })}
      />
      {order.length > 0 && unlisted.length > 0 && (
        <div className="text-xs leading-snug text-warning" data-part="unlisted">
          Not listed, so ranked last: {unlisted.map((o) => o.label).join(', ')}.
        </div>
      )}
      {isLibrary && libraries.length === 0 && (
        <div className="text-xs text-muted">No libraries yet — add a media server and sync its libraries first.</div>
      )}
      {defaults.length > 0 && (
        <div>
          <Button
            size="sm"
            variant="ghost"
            icon={RotateCcw}
            disabled={disabled || sameOrder(order, defaults)}
            onClick={() => onChange({ ...criterion, order: defaults })}
          >
            Reset to default order
          </Button>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Numeric
// ---------------------------------------------------------------------------

/** Direction select (type-specific wording) + tolerance % + minimum delta. */
export function NumericCriterionEditor({ criterion, schema, onChange, disabled }: CriterionEditorProps) {
  const wording = directionWording(criterion.type, schema);
  // Without a schema entry, offer tolerance where the built-in templates use it.
  const tolerance =
    schema?.supportsTolerance ?? (criterion.type === 'video_bitrate' || criterion.type === 'file_size');
  const showTolerance = tolerance || (criterion.tolerancePercent ?? 0) > 0;
  const showMinDelta = supportsMinDelta(criterion.type, schema) || (criterion.minDelta ?? 0) > 0;
  const unit = minDeltaUnit(criterion.type, schema);
  // Last played always prefers the more recently played copy: no direction to choose.
  const fixed = fixedDirection(criterion.type);
  const days = unit === 'days';

  return (
    <div className="flex flex-col gap-2">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {!fixed && (
          <Field label="Prefer">
            {(id) => (
              <Select
                id={id}
                disabled={disabled}
                value={asDirection(criterion.direction)}
                options={[
                  { value: 'higher', label: wording.higher },
                  { value: 'lower', label: wording.lower },
                ]}
                onChange={(v) => onChange({ ...criterion, direction: v })}
              />
            )}
          </Field>
        )}
        {showTolerance && (
          <Field label="Tolerance" help="Values this close count as a tie and fall through to the next criterion.">
            {(id) => (
              <NumberInput
                id={id}
                disabled={disabled}
                value={criterion.tolerancePercent ?? null}
                min={0}
                max={100}
                integer={false}
                step={0.5}
                allowEmpty
                unit="%"
                onChange={(v) => onChange({ ...criterion, tolerancePercent: v ?? undefined })}
              />
            )}
          </Field>
        )}
        {showMinDelta && (
          <Field
            label={days ? 'Minimum difference (days)' : 'Min delta'}
            help={days ? 'Plays closer together than this count as a tie.' : 'Differences smaller than this count as a tie.'}
          >
            {(id) => (
              <NumberInput
                id={id}
                disabled={disabled}
                value={criterion.minDelta ?? null}
                min={0}
                max={days ? MAX_LAST_PLAYED_DAYS : undefined}
                integer={days}
                allowEmpty
                unit={unit || undefined}
                onChange={(v) => onChange({ ...criterion, minDelta: v ?? undefined })}
              />
            )}
          </Field>
        )}
      </div>
      {criterion.type === 'last_played' && (
        <div className="text-xs text-muted">
          Uses the play history Tautulli recorded for each Plex item. A copy whose history is unknown or could not be
          read ties with every copy; it never counts as “not played”.
        </div>
      )}
      {criterion.type === 'video_bitrate' && (
        <div className="text-xs text-muted">Only compared when both copies use the same video codec.</div>
      )}
      {criterion.type === 'custom_format_score' && (
        <div className="text-xs text-muted">
          Only compared when both copies are tracked by the same *arr instance and both scores are known.
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Boolean
// ---------------------------------------------------------------------------

export interface BooleanCriterionEditorProps extends CriterionEditorProps {
  arrInstances: readonly ArrInstance[];
}

const OTHER_LANGUAGE = '__other__';

/** Language picker: common languages + free-text code. */
function LanguagePicker({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (code: string) => void;
  disabled?: boolean;
}) {
  const known = COMMON_LANGUAGES.some((l) => l.code === value.toLowerCase());
  const [other, setOther] = useState(value !== '' && !known);
  const custom = other || (value !== '' && !known);
  const invalid = custom && value !== '' && !isLanguageCode(value);

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <Field label="Language">
        {(id) => (
          <Select
            id={id}
            disabled={disabled}
            placeholder="Choose a language…"
            value={custom ? OTHER_LANGUAGE : value.toLowerCase()}
            options={[
              ...COMMON_LANGUAGES.map((l) => ({ value: l.code, label: `${l.name} (${l.code})` })),
              { value: OTHER_LANGUAGE, label: 'Other (enter a code)…' },
            ]}
            onChange={(v) => {
              if (v === OTHER_LANGUAGE) {
                setOther(true);
                onChange(known ? '' : value);
              } else {
                setOther(false);
                onChange(v);
              }
            }}
          />
        )}
      </Field>
      {custom && (
        <Field
          label="Language code"
          help="ISO 639 code as Plex reports it, e.g. eng, fre, ger."
          error={invalid ? 'Use a 2- or 3-letter code' : null}
        >
          {(id) => (
            <TextInput
              id={id}
              disabled={disabled}
              value={value}
              maxLength={3}
              placeholder="e.g. eng"
              invalid={invalid}
              onChange={(e) => onChange(e.target.value.trim().toLowerCase())}
            />
          )}
        </Field>
      )}
    </div>
  );
}

/** arr_managed → optional *arr instance; audio_language → language code. */
export function BooleanCriterionEditor({ criterion, onChange, arrInstances, disabled }: BooleanCriterionEditorProps) {
  if (criterion.type === 'arr_managed') {
    const value = criterion.value ?? '';
    const options = [
      { value: '', label: 'Any *arr' },
      ...arrInstances.map((a) => ({ value: String(a.id), label: a.name })),
    ];
    if (value && !options.some((o) => o.value === value)) {
      options.push({ value, label: `Unknown instance (#${value})` });
    }
    return (
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Tracked by" help="A copy tracked by this *arr wins over one that is not.">
          {(id) => (
            <Select
              id={id}
              disabled={disabled}
              value={value}
              options={options}
              onChange={(v) => onChange({ ...criterion, value: v || undefined })}
            />
          )}
        </Field>
      </div>
    );
  }
  if (criterion.type === 'audio_language') {
    return (
      <LanguagePicker
        value={criterion.value ?? ''}
        disabled={disabled}
        onChange={(code) => onChange({ ...criterion, value: code || undefined })}
      />
    );
  }
  return null;
}

// ---------------------------------------------------------------------------
// Patterns (filename_score)
// ---------------------------------------------------------------------------

export interface PatternsEditorProps extends CriterionEditorProps {
  /** Also flag empty patterns (after a save attempt). */
  showRequired?: boolean;
}

/** Table of filename/path patterns with scores; the matching scores are summed. */
export function PatternsEditor({ criterion, onChange, disabled, showRequired = false }: PatternsEditorProps) {
  const patterns = criterion.patterns ?? [];
  const update = (next: PatternScore[]) => onChange({ ...criterion, patterns: next });
  const patch = (i: number, p: Partial<PatternScore>) =>
    update(patterns.map((x, j) => (j === i ? { ...x, ...p } : x)));

  return (
    <div className="flex flex-col gap-2">
      <div className="text-xs leading-snug text-muted">
        Scores of every matching pattern are added up; the higher total wins. A glob without a <code>/</code> matches the
        file name (e.g. <code>*Remux*</code> or <code>*.ts</code>); with a <code>/</code> it matches the full path (
        <code>**</code> spans folders, e.g. <code>**/Remux/**</code>). Regular expressions (Go RE2 syntax) match the full
        path. Matching ignores case unless “Case-sensitive” is ticked.
      </div>
      {patterns.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[520px] border-collapse text-sm">
            <thead>
              <tr className="text-left text-xs text-muted">
                <th className="py-1 pr-2 font-semibold">Pattern</th>
                <th className="w-28 py-1 pr-2 font-semibold">Score</th>
                <th className="w-16 py-1 pr-2 text-center font-semibold">Regex</th>
                <th className="w-24 py-1 pr-2 text-center font-semibold">Case-sensitive</th>
                <th className="w-8 py-1" aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {patterns.map((p, i) => {
                const n = i + 1;
                const error = validatePattern(p);
                const shown = error && (showRequired || p.pattern.trim() !== '') ? error : null;
                return (
                  <tr key={i} className="align-top">
                    <td className="py-1 pr-2">
                      <TextInput
                        aria-label={`Pattern ${n}`}
                        disabled={disabled}
                        value={p.pattern}
                        placeholder={p.regex ? 'remux' : '*Remux*'}
                        invalid={!!shown}
                        className="font-mono"
                        onChange={(e) => patch(i, { pattern: e.target.value })}
                      />
                      {shown && <FieldError className="mt-1" message={shown} />}
                    </td>
                    <td className="py-1 pr-2">
                      <NumberInput
                        aria-label={`Score for pattern ${n}`}
                        disabled={disabled}
                        value={p.score}
                        min={-10000}
                        max={10000}
                        onChange={(v) => patch(i, { score: v ?? 0 })}
                      />
                    </td>
                    <td className="py-1 pr-2 text-center">
                      <span className="inline-flex h-9 items-center">
                        <Checkbox
                          aria-label={`Regex for pattern ${n}`}
                          disabled={disabled}
                          checked={p.regex}
                          onChange={(checked) => patch(i, { regex: checked })}
                        />
                      </span>
                    </td>
                    <td className="py-1 pr-2 text-center">
                      <span className="inline-flex h-9 items-center">
                        <Checkbox
                          aria-label={`Case-sensitive pattern ${n}`}
                          disabled={disabled}
                          checked={p.caseSensitive}
                          onChange={(checked) => patch(i, { caseSensitive: checked })}
                        />
                      </span>
                    </td>
                    <td className="py-1">
                      <span className="inline-flex h-9 items-center">
                        <IconButton
                          icon={Trash2}
                          label={`Remove pattern ${n}`}
                          variant="danger"
                          size="sm"
                          disabled={disabled}
                          onClick={() => update(patterns.filter((_, j) => j !== i))}
                        />
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <div>
        <Button size="sm" icon={Plus} disabled={disabled} onClick={() => update([...patterns, newPattern()])}>
          Add Pattern
        </Button>
      </div>
    </div>
  );
}
