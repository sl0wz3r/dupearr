/** Profile cards (Settings → Profiles) and the "Add Profile" template picker. */
import { FilePlus2, ShieldCheck } from 'lucide-react';
import type { Library, Profile } from '@/api/types';
import { Badge, Button, Card, Modal } from '@/components/ui';
import { keepSummary, summarizeCriteria, templateDescription, type SchemaMap } from './criteria';

// ---------------------------------------------------------------------------
// Card
// ---------------------------------------------------------------------------

export interface ProfileCardProps {
  profile: Profile;
  schema: SchemaMap;
  libraries?: readonly Library[];
  onEdit: () => void;
}

/** Libraries that use `profile` (explicitly, or implicitly as the default). */
export function librariesUsing(profile: Profile, libraries: readonly Library[]): Library[] {
  return libraries.filter((l) => l.profileId === profile.id || (l.profileId == null && profile.isDefault));
}

/** Connection-style card: name, Default badge, first 3 criteria, keep options, protections, libraries. */
export function ProfileCard({ profile, schema, libraries = [], onEdit }: ProfileCardProps) {
  const { labels, more } = summarizeCriteria(profile.criteria, schema, 3);
  const used = librariesUsing(profile, libraries);
  const protections = profile.protections?.length ?? 0;

  return (
    <Card
      onClick={onEdit}
      title={
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate">{profile.name}</span>
          {profile.isDefault && <Badge kind="primary">Default</Badge>}
        </span>
      }
    >
      {labels.length > 0 ? (
        <ol className="m-0 flex list-none flex-wrap gap-1.5 p-0">
          {labels.map((label, i) => (
            <li key={`${i}-${label}`}>
              <Badge kind="accent" outline>
                {i + 1}. {label}
              </Badge>
            </li>
          ))}
          {more > 0 && (
            <li>
              <Badge outline>+{more} more</Badge>
            </li>
          )}
        </ol>
      ) : (
        <div className="text-sm text-muted">No enabled criteria</div>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg">
        <span>{keepSummary(profile.keepCount, profile.keepPer)}</span>
        {protections > 0 && (
          <span className="inline-flex items-center gap-1 text-muted">
            <ShieldCheck aria-hidden width={14} height={14} />
            {protections} protection{protections === 1 ? '' : 's'}
          </span>
        )}
      </div>
      <div className="mt-1 truncate text-xs text-muted">
        {used.length > 0 ? `Used by ${used.map((l) => l.title).join(', ')}` : 'Not used by any library'}
      </div>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Template picker
// ---------------------------------------------------------------------------

export interface TemplatePickerModalProps {
  open: boolean;
  onClose: () => void;
  templates: readonly Profile[];
  schema: SchemaMap;
  /** Called with the chosen template, or null for "start from scratch". */
  onPick: (template: Profile | null) => void;
}

const OPTION_CLASS =
  'flex h-full w-full flex-col gap-1.5 rounded border border-border bg-card-alt px-4 py-3 text-left transition-colors hover:border-accent hover:bg-card-hover focus-visible:border-accent';

export function TemplatePickerModal({ open, onClose, templates, schema, onPick }: TemplatePickerModalProps) {
  return (
    <Modal open={open} onClose={onClose} title="Add Profile" size="lg" footer={<Button onClick={onClose}>Cancel</Button>}>
      <p className="mt-0 mb-4 text-sm text-muted">
        Start from a template — every criterion, option and protection can be changed afterwards.
      </p>
      <ul aria-label="Templates" className="m-0 grid list-none grid-cols-1 gap-3 p-0 sm:grid-cols-2">
        {templates.map((t, i) => {
          const { labels, more } = summarizeCriteria(t.criteria, schema, 3);
          return (
            <li key={`${t.name}-${i}`}>
              <button type="button" className={OPTION_CLASS} onClick={() => onPick(t)}>
                <span className="flex items-center gap-2 font-semibold text-fg-strong">
                  {t.name}
                  {t.isDefault && <Badge kind="success">Recommended</Badge>}
                </span>
                <span className="text-sm text-fg">{templateDescription(t, schema)}</span>
                <span className="text-xs text-muted">
                  {labels.join(' › ')}
                  {more > 0 ? ` › +${more}` : ''} · {keepSummary(t.keepCount, t.keepPer)}
                </span>
              </button>
            </li>
          );
        })}
        <li>
          <button type="button" className={OPTION_CLASS} onClick={() => onPick(null)}>
            <span className="flex items-center gap-2 font-semibold text-fg-strong">
              <FilePlus2 aria-hidden width={16} height={16} />
              Start from scratch
            </span>
            <span className="text-sm text-fg">Only the File Health check — add and order the other criteria yourself.</span>
          </button>
        </li>
      </ul>
      {templates.length === 0 && (
        <p className="mt-3 mb-0 text-sm text-muted">The server did not provide any templates.</p>
      )}
    </Modal>
  );
}
