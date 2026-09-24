/**
 * Decision-profile criteria builder: an ordered tiebreaker chain of criterion rows (reorder with
 * up/down, enable/disable, per-kind parameters, remove) plus an "add criterion" picker of the
 * types not used yet. docs/ARCHITECTURE.md §4.3, docs/DECISIONS.md D4–D5.
 */
import { clsx } from 'clsx';
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  ChevronsDownUp,
  ChevronsUpDown,
  ListOrdered,
  Plus,
  Trash2,
} from 'lucide-react';
import { useState } from 'react';
import type { ArrInstance, Criterion, CriterionSchema, CriterionType, Library } from '@/api/types';
import { moveItem } from '@/components/ui/OrderedListEditor';
import { Alert, Badge, Button, EmptyState, IconButton, Select, Switch } from '@/components/ui';
import { CRITERION_TYPE_LABELS } from '@/lib/constants';
import { criterionLabel, criterionSummary, editorKind, newCriterion, type SchemaMap } from './criteria';
import { BooleanCriterionEditor, NumericCriterionEditor, OrderedCriterionEditor, PatternsEditor } from './CriterionEditors';
import { FieldErrors } from './Field';

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

export interface CriterionRowProps {
  criterion: Criterion;
  index: number;
  count: number;
  schema?: CriterionSchema;
  libraries: readonly Library[];
  arrInstances: readonly ArrInstance[];
  errors?: readonly string[];
  showRequired?: boolean;
  /** Parameters visible (rows with errors are always expanded). */
  expanded?: boolean;
  onToggle?: () => void;
  /** Collapsed one-line summary of the parameters. */
  summary?: string;
  onChange: (next: Criterion) => void;
  onMove: (delta: -1 | 1) => void;
  onRemove: () => void;
}

/** One step of the tiebreaker chain. */
export function CriterionRow({
  criterion,
  index,
  count,
  schema,
  libraries,
  arrInstances,
  errors,
  showRequired,
  expanded = true,
  onToggle,
  summary,
  onChange,
  onMove,
  onRemove,
}: CriterionRowProps) {
  const label = criterionLabel(criterion.type, schema);
  const kind = editorKind(criterion.type, schema);
  const enabled = criterion.enabled;
  const hasErrors = !!errors && errors.length > 0;
  const collapsible = kind !== 'none' && !!onToggle;
  const open = !collapsible || expanded || hasErrors;

  let editor = null;
  if (kind === 'ordered') {
    editor = <OrderedCriterionEditor criterion={criterion} schema={schema} libraries={libraries} onChange={onChange} />;
  } else if (kind === 'numeric') {
    editor = <NumericCriterionEditor criterion={criterion} schema={schema} onChange={onChange} />;
  } else if (kind === 'boolean') {
    editor = (
      <BooleanCriterionEditor criterion={criterion} schema={schema} arrInstances={arrInstances} onChange={onChange} />
    );
  } else if (kind === 'patterns') {
    editor = <PatternsEditor criterion={criterion} schema={schema} onChange={onChange} showRequired={showRequired} />;
  }

  return (
    <li
      data-type={criterion.type}
      aria-label={label}
      className={clsx(
        'rounded border bg-card-alt',
        hasErrors ? 'border-danger/70' : 'border-border',
        !enabled && 'bg-card',
      )}
    >
      <div className="flex items-start gap-2 px-3 py-2.5">
        {collapsible ? (
          <IconButton
            icon={open ? ChevronDown : ChevronRight}
            label={`${open ? 'Hide' : 'Show'} ${label} options`}
            aria-expanded={open}
            size="xs"
            className="mt-0.5"
            disabled={hasErrors}
            onClick={onToggle}
          />
        ) : (
          <span aria-hidden className="size-6 shrink-0" />
        )}
        <span
          aria-hidden
          className={clsx(
            'mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-sm text-xs font-semibold',
            enabled ? 'bg-accent/15 text-accent-soft' : 'bg-card-hover text-muted',
          )}
        >
          {index + 1}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className={clsx('font-semibold', enabled ? 'text-fg-strong' : 'text-muted line-through')}>{label}</span>
            {(schema?.requiresArr ?? (criterion.type === 'custom_format_score' || criterion.type === 'arr_managed')) && (
              <Badge kind="info" outline title="Needs data from a connected Radarr/Sonarr instance">
                requires *arr
              </Badge>
            )}
            {!enabled && <Badge outline>Disabled</Badge>}
          </div>
          {schema?.description && <div className="mt-0.5 text-xs leading-snug text-muted">{schema.description}</div>}
          {!open && summary && (
            <button
              type="button"
              tabIndex={-1}
              onClick={onToggle}
              className="mt-1 block max-w-full truncate text-left text-xs text-fg hover:text-accent-soft"
            >
              {summary}
            </button>
          )}
          {criterion.type === 'health' && index > 0 && (
            <div className="mt-1 text-xs text-warning">
              Recommended as the first criterion so broken, unanalyzed or sample files never win.
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          <span className="mr-1 inline-flex">
            <Switch size="sm" checked={enabled} aria-label={`Enable ${label}`} onChange={(v) => onChange({ ...criterion, enabled: v })} />
          </span>
          <IconButton icon={ArrowUp} label={`Move ${label} up`} size="sm" disabled={index === 0} onClick={() => onMove(-1)} />
          <IconButton
            icon={ArrowDown}
            label={`Move ${label} down`}
            size="sm"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
          />
          <IconButton icon={Trash2} label={`Remove ${label}`} size="sm" variant="danger" onClick={onRemove} />
        </div>
      </div>
      {((open && editor) || hasErrors) && (
        <div
          className={clsx(
            'flex flex-col gap-2 border-t border-border px-3 py-2.5 sm:pl-[4.75rem]',
            !enabled && 'opacity-60',
          )}
        >
          {open && editor}
          <FieldErrors messages={errors} />
        </div>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Builder
// ---------------------------------------------------------------------------

export interface CriteriaBuilderProps {
  criteria: readonly Criterion[];
  onChange: (next: Criterion[]) => void;
  schema: SchemaMap;
  libraries?: readonly Library[];
  arrInstances?: readonly ArrInstance[];
  /** Per-row errors keyed by criterion type. */
  errors?: Partial<Record<CriterionType, string[]>>;
  /** Errors about the list as a whole. */
  listErrors?: readonly string[];
  /** Flag empty required values too (after a save attempt). */
  showRequired?: boolean;
  /** Start with every row's parameters visible (default: collapsed to a one-line summary). */
  initiallyExpanded?: boolean;
}

/**
 * Stable React keys for the rows: the type, suffixed with its occurrence for the rare repeated type
 * (the engine allows several filename_score steps), so keys stay unique and follow a row on reorder.
 */
export function criterionRowKeys(criteria: readonly Criterion[]): string[] {
  const seen = new Map<CriterionType, number>();
  return criteria.map((c) => {
    const n = seen.get(c.type) ?? 0;
    seen.set(c.type, n + 1);
    return n === 0 ? c.type : `${c.type}#${n + 1}`;
  });
}

/** All criterion types the add-picker can offer (schema order, else the built-in label order). */
function availableTypes(schema: SchemaMap): CriterionType[] {
  const fromSchema = [...schema.keys()];
  return fromSchema.length > 0 ? fromSchema : (Object.keys(CRITERION_TYPE_LABELS) as CriterionType[]);
}

export function CriteriaBuilder({
  criteria,
  onChange,
  schema,
  libraries = [],
  arrInstances = [],
  errors = {},
  listErrors,
  showRequired,
  initiallyExpanded = false,
}: CriteriaBuilderProps) {
  const [toAdd, setToAdd] = useState<CriterionType | ''>('');
  const [expanded, setExpanded] = useState<ReadonlySet<CriterionType>>(
    () => new Set(initiallyExpanded ? criteria.map((c) => c.type) : []),
  );
  const used = new Set(criteria.map((c) => c.type));
  const rowKeys = criterionRowKeys(criteria);
  const ctx = { schema, libraries, arrInstances };
  const collapsibleTypes = criteria
    .filter((c) => editorKind(c.type, schema.get(c.type)) !== 'none')
    .map((c) => c.type);
  const allExpanded = collapsibleTypes.length > 0 && collapsibleTypes.every((t) => expanded.has(t));
  // docs/DECISIONS.md D4: health is the first criterion so a broken, unanalyzed or sample file never
  // outranks a good copy (e.g. with "smaller file wins"). Warn when it is missing or switched off.
  const healthKnown = schema.size === 0 || schema.has('health');
  const healthOff = healthKnown && !criteria.some((c) => c.type === 'health' && c.enabled);

  const toggle = (type: CriterionType) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(type)) next.delete(type);
      else next.add(type);
      return next;
    });
  const unused = availableTypes(schema)
    .filter((t) => !used.has(t))
    .map((t) => {
      const s = schema.get(t);
      return { value: t, label: `${criterionLabel(t, s)}${s?.requiresArr ? ' (requires *arr)' : ''}` };
    });

  const add = () => {
    if (!toAdd || used.has(toAdd)) return;
    const c = newCriterion(toAdd, schema.get(toAdd));
    // Health belongs at the top of the chain (docs/DECISIONS.md D4).
    onChange(toAdd === 'health' ? [c, ...criteria] : [...criteria, c]);
    setExpanded((prev) => new Set(prev).add(toAdd));
    setToAdd('');
  };

  return (
    <div className="flex flex-col gap-3">
      <FieldErrors messages={listErrors} />
      {healthOff && (
        <Alert kind="warning" title="File Health is not active">
          Without it a broken, unanalyzed or sample file can be ranked above a good copy — and the good copy proposed
          for removal. Add or enable File Health as the first criterion.
        </Alert>
      )}
      {collapsibleTypes.length > 1 && (
        <div className="-mb-1 flex justify-end">
          <Button
            size="sm"
            variant="ghost"
            icon={allExpanded ? ChevronsDownUp : ChevronsUpDown}
            onClick={() => setExpanded(new Set(allExpanded ? [] : collapsibleTypes))}
          >
            {allExpanded ? 'Collapse all' : 'Expand all'}
          </Button>
        </div>
      )}
      {criteria.length === 0 ? (
        <div className="rounded border border-dashed border-border-strong">
          <EmptyState
            compact
            icon={ListOrdered}
            title="No criteria yet"
            description="Add criteria below. Copies are compared by the first criterion; the next one only breaks ties."
          />
        </div>
      ) : (
        <ol aria-label="Criteria" className="m-0 flex list-none flex-col gap-2 p-0">
          {criteria.map((c, i) => (
            <CriterionRow
              key={rowKeys[i]}
              criterion={c}
              index={i}
              count={criteria.length}
              schema={schema.get(c.type)}
              libraries={libraries}
              arrInstances={arrInstances}
              errors={errors[c.type]}
              showRequired={showRequired}
              expanded={expanded.has(c.type)}
              onToggle={() => toggle(c.type)}
              summary={criterionSummary(c, ctx)}
              onChange={(next) => onChange(criteria.map((x, j) => (j === i ? next : x)))}
              onMove={(delta) => onChange(moveItem(criteria, i, delta))}
              onRemove={() => onChange(criteria.filter((_, j) => j !== i))}
            />
          ))}
        </ol>
      )}
      {unused.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <div className="min-w-[220px] flex-1 sm:max-w-sm">
            <Select
              aria-label="Add criterion"
              placeholder="Choose a criterion to add…"
              options={unused}
              value={toAdd}
              onChange={(v) => setToAdd(v)}
            />
          </div>
          <Button icon={Plus} disabled={!toAdd} onClick={add}>
            Add Criterion
          </Button>
        </div>
      )}
    </div>
  );
}
