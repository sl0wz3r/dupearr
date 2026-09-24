/**
 * Protections of a decision profile: rules whose matching copies are never removed (they are still
 * ranked, so a better unprotected copy is kept too). Types: path_glob, library, arr_instance,
 * arr_tag (docs/DECISIONS.md D4).
 */
import { Plus, ShieldCheck, Trash2 } from 'lucide-react';
import type { ArrInstance, Library, Protection, ProtectionType } from '@/api/types';
import { Button, IconButton, Select, TextInput } from '@/components/ui';
import { ARR_KIND_LABELS, labelOf } from '@/lib/constants';
import { DEFAULT_KEEP_TAG, defaultProtectionValue, protectionTypeLabel } from './criteria';
import { Field, FieldErrors } from './Field';
import { validateGlob } from './validation';

/**
 * Hint for a path_glob value that probably does not do what the user expects: a plain word without
 * "/" or wildcards only matches a file whose whole name is that word (the engine matches file names
 * for patterns without "/"), not a folder. Null when the pattern looks intentional.
 */
export function pathGlobHint(value: string): string | null {
  const v = value.trim();
  if (!v || v.includes('/') || v.includes('\\') || /[*?[{]/.test(v)) return null;
  return `Matches only files named exactly “${v}”. To protect a folder, include a slash, e.g. ${v}/** or /data/media/movies/${v}.`;
}

export interface ProtectionsEditorProps {
  protections: readonly Protection[];
  onChange: (next: Protection[]) => void;
  /** Types offered in the type select. */
  types: readonly ProtectionType[];
  libraries?: readonly Library[];
  arrInstances?: readonly ArrInstance[];
  /** Per-row errors keyed by index. */
  errors?: Record<number, string[]>;
  listErrors?: readonly string[];
  /** Flag empty values too (after a save attempt). */
  showRequired?: boolean;
}

const HELP: Record<string, string> = {
  path_glob:
    'Matched against every file path (as the media server reports it and as mapped locally), ignoring case. With a “/” it matches paths: a folder such as /data/media/movies/Criterion protects everything inside it, Keep/** matches a Keep folder at any depth. Without a “/” it matches file names only, e.g. *Remux*.',
  library: 'Every copy in this library is kept.',
  arr_instance: 'Every copy tracked by this Radarr/Sonarr instance is kept.',
  arr_tag: `Tag a movie or series with this label in Radarr/Sonarr to keep all of its copies (default “${DEFAULT_KEEP_TAG}”).`,
};

function ValueInput({
  id,
  protection,
  index,
  libraries,
  arrInstances,
  invalid,
  onChange,
}: {
  id: string;
  protection: Protection;
  index: number;
  libraries: readonly Library[];
  arrInstances: readonly ArrInstance[];
  invalid: boolean;
  onChange: (value: string) => void;
}) {
  const aria = `Protection ${index + 1} value`;
  const { type, value } = protection;

  if (type === 'library' || type === 'arr_instance') {
    const options =
      type === 'library'
        ? libraries.map((l) => ({ value: String(l.id), label: l.title || `Library ${l.id}` }))
        : arrInstances.map((a) => ({ value: String(a.id), label: `${a.name} (${labelOf(ARR_KIND_LABELS, a.kind)})` }));
    if (value && !options.some((o) => o.value === value)) {
      options.push({ value, label: type === 'library' ? `Unknown library (#${value})` : `Unknown instance (#${value})` });
    }
    return (
      <Select
        id={id}
        aria-label={aria}
        invalid={invalid}
        placeholder={
          options.length === 0
            ? type === 'library'
              ? 'No libraries configured'
              : 'No *arr instances configured'
            : type === 'library'
              ? 'Choose a library…'
              : 'Choose an instance…'
        }
        options={options}
        value={value}
        onChange={onChange}
      />
    );
  }

  return (
    <TextInput
      id={id}
      aria-label={aria}
      invalid={invalid}
      value={value}
      className={type === 'path_glob' ? 'font-mono' : undefined}
      placeholder={type === 'arr_tag' ? DEFAULT_KEEP_TAG : type === 'path_glob' ? '/data/media/movies/Keep/**' : ''}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

export function ProtectionsEditor({
  protections,
  onChange,
  types,
  libraries = [],
  arrInstances = [],
  errors = {},
  listErrors,
  showRequired = false,
}: ProtectionsEditorProps) {
  const typeOptions = types.map((t) => ({ value: t, label: protectionTypeLabel(t) }));
  const patch = (i: number, p: Partial<Protection>) =>
    onChange(protections.map((x, j) => (j === i ? { ...x, ...p } : x)));

  return (
    <div className="flex flex-col gap-3">
      <FieldErrors messages={listErrors} />
      {protections.length === 0 ? (
        <div className="flex items-center gap-2 rounded border border-dashed border-border-strong px-3 py-2.5 text-sm text-muted">
          <ShieldCheck aria-hidden width={16} height={16} className="shrink-0" />
          No protections. Protected copies are always kept — they still take part in ranking.
        </div>
      ) : (
        <ul aria-label="Protections" className="m-0 flex list-none flex-col gap-2 p-0">
          {protections.map((p, i) => {
            const local =
              p.value.trim() === ''
                ? showRequired
                  ? 'A value is required'
                  : null
                : p.type === 'path_glob'
                  ? validateGlob(p.value)
                  : null;
            // The parent may pass the same client-side message after a save attempt: show it once.
            const rowErrors = [...new Set([...(local ? [local] : []), ...(errors[i] ?? [])])];
            return (
              <li key={i} className="rounded border border-border bg-card-alt px-3 py-2.5">
                <div className="flex items-start gap-2">
                  <div className="grid min-w-0 flex-1 grid-cols-1 gap-3 sm:grid-cols-[200px_1fr]">
                    <Field label="Type">
                      {(id) => (
                        <Select
                          id={id}
                          aria-label={`Protection ${i + 1} type`}
                          options={typeOptions}
                          value={p.type}
                          onChange={(t) => patch(i, { type: t, value: defaultProtectionValue(t) })}
                        />
                      )}
                    </Field>
                    <Field label="Value" help={HELP[p.type]}>
                      {(id) => (
                        <ValueInput
                          id={id}
                          protection={p}
                          index={i}
                          libraries={libraries}
                          arrInstances={arrInstances}
                          invalid={rowErrors.length > 0}
                          onChange={(value) => patch(i, { value })}
                        />
                      )}
                    </Field>
                  </div>
                  <span className="mt-6">
                    <IconButton
                      icon={Trash2}
                      label={`Remove protection ${i + 1}`}
                      variant="danger"
                      size="sm"
                      onClick={() => onChange(protections.filter((_, j) => j !== i))}
                    />
                  </span>
                </div>
                <FieldErrors messages={rowErrors} className="mt-2" />
                {p.type === 'path_glob' && rowErrors.length === 0 && pathGlobHint(p.value) && (
                  <div className="mt-2 text-xs leading-snug text-warning">{pathGlobHint(p.value)}</div>
                )}
              </li>
            );
          })}
        </ul>
      )}
      <div>
        <Button
          size="sm"
          icon={Plus}
          onClick={() => onChange([...protections, { type: 'arr_tag', value: DEFAULT_KEEP_TAG }])}
        >
          Add Protection
        </Button>
      </div>
    </div>
  );
}
