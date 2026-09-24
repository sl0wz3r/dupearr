import { Lightbulb } from 'lucide-react';
import type { ArrInstance, Library } from '@/api/types';
import {
  FINAL_TIEBREAK,
  explainCriteriaParts,
  explainKeep,
  explainProtections,
  type ProfileDraft,
  type SchemaMap,
} from './criteria';

export interface ProfileExplanationProps {
  draft: Pick<ProfileDraft, 'criteria' | 'keepCount' | 'keepPer' | 'protections'>;
  schema: SchemaMap;
  libraries?: readonly Library[];
  arrInstances?: readonly ArrInstance[];
}

/** Live plain-English summary of how a profile picks the copy to keep. */
export function ProfileExplanation({ draft, schema, libraries, arrInstances }: ProfileExplanationProps) {
  const ctx = { schema, libraries, arrInstances };
  const protections = explainProtections(draft.protections, ctx);
  const parts = explainCriteriaParts(draft.criteria, ctx);
  const anyEnabled = draft.criteria.some((c) => c.enabled);
  return (
    <section
      aria-label="How this profile decides"
      className="rounded border border-accent/40 bg-accent/5 px-4 py-3 text-sm text-fg"
    >
      <div className="mb-1.5 flex items-center gap-2 font-semibold text-fg-strong">
        <Lightbulb aria-hidden width={16} height={16} className="text-accent-soft" />
        How this profile decides
      </div>
      {/* One clause per line; the text content still reads as one sentence. */}
      <p data-part="criteria" className="m-0 leading-relaxed">
        {anyEnabled
          ? parts.map((part, i) => (
              <span key={i} className={i === 0 ? 'block' : 'block pl-4'}>
                {part}
                {i === parts.length - 1 ? '.' : '; '}
              </span>
            ))
          : parts[0]}
      </p>
      <p className="m-0 mt-1 text-xs text-muted">{FINAL_TIEBREAK}</p>
      <p data-part="keep" className="m-0 mt-2">
        {explainKeep(draft.keepCount, draft.keepPer)}
      </p>
      {protections && (
        <p data-part="protections" className="m-0 mt-1">
          {protections}
        </p>
      )}
    </section>
  );
}
