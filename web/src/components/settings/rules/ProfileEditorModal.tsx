/**
 * Create / edit a decision profile: name, default flag, keep options, the criteria builder with a
 * live explanation, and protections. Server validation errors (400) are mapped onto the fields;
 * deleting the default profile is refused by the server (409) and the message is shown.
 */
import { Copy, Save, Trash2 } from 'lucide-react';
import { useMemo, useRef, useState, type ReactNode } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { useCreateProfile, useDeleteProfile, useUpdateProfile } from '@/api/hooks';
import type { ArrInstance, Library, Profile, ProfileSchema } from '@/api/types';
import { Alert, Button, ConfirmDialog, FormGroup, Modal, NumberInput, Select, Switch, TextInput, useToast } from '@/components/ui';
import { CriteriaBuilder } from './CriteriaBuilder';
import {
  countProfileErrors,
  draftFromTemplate,
  emptyProfileErrors,
  hasProfileErrors,
  keepPerLabel,
  keepPerValues,
  mapProfileServerErrors,
  mergeProfileErrors,
  normalizeProfile,
  protectionTypes,
  schemaMap,
  requiresWatchHistory,
  toProfileInput,
  validateProfileDraft,
  type ProfileDraft,
  type ProfileErrors,
} from './criteria';
import { ProfileExplanation } from './ProfileExplanation';
import { ProtectionsEditor } from './ProtectionsEditor';

export interface ProfileEditorModalProps {
  open: boolean;
  /** Profile (or template copy) to edit; an `id` means it already exists on the server. */
  initial: ProfileDraft;
  /** Whether the stored profile is the default (the draft's flag may change while editing). */
  storedIsDefault?: boolean;
  schema: ProfileSchema | undefined;
  libraries?: readonly Library[];
  arrInstances?: readonly ArrInstance[];
  /**
   * Whether an enabled Tautulli connection exists (undefined = not known yet). Without one, the
   * play-history criteria never decide (every history is unknown and ties).
   */
  watchHistoryConnected?: boolean;
  /** Names of existing profiles (used to name clones). */
  existingNames?: readonly string[];
  onClose: () => void;
  /** Opens a new editor with a copy of this profile. */
  onClone?: (draft: ProfileDraft) => void;
}

function EditorSection({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="mt-6 first:mt-0">
      <div className="mb-3 border-b border-border pb-1.5">
        <h3 className="m-0 text-lg font-light text-fg-strong">{title}</h3>
        {description && <div className="mt-0.5 text-sm text-muted">{description}</div>}
      </div>
      {children}
    </section>
  );
}

/** Message for a failed delete (409 = default profile / still in use). */
export function deleteErrorMessage(e: unknown): string {
  if (isApiError(e) && e.status === 409) {
    const generic = !e.message || /^(conflict|request failed)/i.test(e.message);
    const base = generic ? 'This profile cannot be deleted — it is the default profile.' : e.message;
    return e.description ? `${base} ${e.description}` : base;
  }
  return errorMessage(e, 'Could not delete the profile');
}

export function ProfileEditorModal({
  open,
  initial,
  storedIsDefault = false,
  schema,
  libraries = [],
  arrInstances = [],
  watchHistoryConnected,
  existingNames = [],
  onClose,
  onClone,
}: ProfileEditorModalProps) {
  const toast = useToast();
  const create = useCreateProfile();
  const update = useUpdateProfile();
  const del = useDeleteProfile();

  const initialDraft = useMemo(() => normalizeProfile(initial), [initial]);
  const [draft, setDraft] = useState<ProfileDraft>(initialDraft);
  const [serverErrors, setServerErrors] = useState<ProfileErrors>(emptyProfileErrors);
  const [attempted, setAttempted] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);

  const smap = useMemo(() => schemaMap(schema), [schema]);
  const isNew = !initialDraft.id;
  const saving = create.isPending || update.isPending;
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(initialDraft), [draft, initialDraft]);

  const clientErrors = useMemo(() => validateProfileDraft(draft, smap), [draft, smap]);
  const errors = useMemo(
    () => mergeProfileErrors(attempted ? clientErrors : emptyProfileErrors(), serverErrors),
    [attempted, clientErrors, serverErrors],
  );
  const errorCount = countProfileErrors(errors);

  const patch = (p: Partial<ProfileDraft>) => setDraft((d) => ({ ...d, ...p }));

  // Top of the modal body: scrolled into view when saving fails so the summary is visible.
  const topRef = useRef<HTMLDivElement>(null);
  const revealErrors = () => {
    requestAnimationFrame(() => topRef.current?.scrollIntoView?.({ block: 'start', behavior: 'smooth' }));
  };

  const requestClose = () => {
    if (saving || del.isPending) return;
    if (dirty) setConfirmDiscard(true);
    else onClose();
  };

  const save = () => {
    setAttempted(true);
    setSubmitError(null);
    if (hasProfileErrors(clientErrors)) {
      revealErrors();
      return;
    }
    const body = toProfileInput(draft);
    setServerErrors(emptyProfileErrors());
    const handlers = {
      onSuccess: (saved: Profile | undefined) => {
        toast.success(isNew ? 'Profile created' : 'Profile saved', saved?.name || body.name);
        onClose();
      },
      onError: (e: unknown) => {
        if (isApiError(e) && e.isValidation) {
          setServerErrors(mapProfileServerErrors(e.validationErrors, body));
        } else {
          setSubmitError(errorMessage(e, 'Could not save the profile'));
        }
        revealErrors();
      },
    };
    if (body.id) update.mutate({ ...body, id: body.id }, handlers);
    else create.mutate(body, handlers);
  };

  const remove = () => {
    if (!initialDraft.id) return;
    del.mutate(initialDraft.id, {
      onSuccess: () => {
        toast.success('Profile deleted', initialDraft.name);
        setConfirmDelete(false);
        onClose();
      },
      onError: (e) => {
        setConfirmDelete(false);
        setSubmitError(deleteErrorMessage(e));
        revealErrors();
      },
    });
  };

  const keepPerOptions = keepPerValues(schema).map((v) => ({ value: v, label: keepPerLabel(v) }));

  return (
    <>
      <Modal
        open={open}
        onClose={requestClose}
        closeOnBackdrop={false}
        size="xl"
        title={isNew ? 'Add Profile' : `Edit Profile — ${initialDraft.name}`}
        footerStart={
          !isNew && (
            <>
              <Button
                variant="danger"
                icon={Trash2}
                disabled={storedIsDefault || saving}
                title={storedIsDefault ? 'The default profile cannot be deleted. Make another profile the default first.' : undefined}
                onClick={() => setConfirmDelete(true)}
              >
                Delete
              </Button>
              {onClone && (
                <Button
                  icon={Copy}
                  disabled={dirty || saving}
                  title={dirty ? 'Save or discard your changes first' : 'Create a copy of this profile'}
                  onClick={() => onClone(draftFromTemplate(draft, existingNames))}
                >
                  Clone
                </Button>
              )}
            </>
          )
        }
        footer={
          <>
            <Button onClick={requestClose} disabled={saving}>
              Cancel
            </Button>
            <Button variant="primary" icon={Save} loading={saving} onClick={save}>
              Save
            </Button>
          </>
        }
      >
        <div ref={topRef} />
        {submitError && (
          <Alert kind="error" className="mb-4" onDismiss={() => setSubmitError(null)}>
            {submitError}
          </Alert>
        )}
        {errorCount > 0 && (
          <Alert kind="error" className="mb-4" title={`Please fix ${errorCount === 1 ? '1 problem' : `${errorCount} problems`} before saving`}>
            {errors.general.length > 0 && (
              <ul className="m-0 list-disc pl-5">
                {errors.general.map((m, i) => (
                  <li key={i}>{m}</li>
                ))}
              </ul>
            )}
          </Alert>
        )}

        <EditorSection title="General">
          <FormGroup label="Name" htmlFor="profile-name" errors={errors.name}>
            <TextInput
              id="profile-name"
              value={draft.name}
              maxLength={100}
              invalid={errors.name.length > 0}
              placeholder="e.g. Keep Highest Quality"
              onChange={(e) => patch({ name: e.target.value })}
            />
          </FormGroup>
          <FormGroup
            label="Default"
            htmlFor="profile-default"
            errors={errors.isDefault}
            helpText={
              storedIsDefault
                ? 'This is the default profile. To change the default, turn this on in another profile.'
                : 'Used for every library without its own profile. Making this the default replaces the current default.'
            }
          >
            <span className="inline-flex pt-1.5">
              <Switch
                id="profile-default"
                checked={draft.isDefault}
                disabled={storedIsDefault}
                onChange={(v) => patch({ isDefault: v })}
              />
            </span>
          </FormGroup>
          <FormGroup
            label="Keep"
            htmlFor="profile-keep-count"
            errors={errors.keepCount}
            helpText="How many of the best copies to keep (in each partition when “Keep per” is set). The rest are proposed for removal."
            width="sm"
          >
            <NumberInput
              id="profile-keep-count"
              value={draft.keepCount}
              min={1}
              max={20}
              unit={draft.keepCount === 1 ? 'copy' : 'copies'}
              invalid={errors.keepCount.length > 0}
              onChange={(v) => patch({ keepCount: v ?? 1 })}
            />
          </FormGroup>
          <FormGroup
            label="Keep per"
            htmlFor="profile-keep-per"
            errors={errors.keepPer}
            helpText="Split copies into partitions first and keep the best in each — “Each resolution” keeps the best 4K AND the best 1080p."
          >
            <Select
              id="profile-keep-per"
              options={keepPerOptions}
              value={draft.keepPer}
              onChange={(v) => patch({ keepPer: v })}
            />
          </FormGroup>
        </EditorSection>

        <EditorSection
          title="Decision Criteria"
          description="Copies are compared criterion by criterion from the top; the first criterion that tells two copies apart decides. A copy with an unknown value ranks below one that has it, unless the criterion says an unknown value is a tie (an unknown play history always is)."
        >
          <div className="mb-3">
            <ProfileExplanation draft={draft} schema={smap} libraries={libraries} arrInstances={arrInstances} />
          </div>
          {watchHistoryConnected === false &&
            draft.criteria.some((c) => c.enabled && requiresWatchHistory(c.type, smap.get(c.type))) && (
              <Alert kind="info" className="mb-3" title="No Tautulli connection">
                Played and Last played rank by the play history Tautulli records. Until a Tautulli is connected in
                Settings → Applications, every copy&apos;s play history is unknown and these criteria never decide.
              </Alert>
            )}
          <CriteriaBuilder
            criteria={draft.criteria}
            onChange={(criteria) => patch({ criteria })}
            schema={smap}
            libraries={libraries}
            arrInstances={arrInstances}
            errors={errors.criterion}
            listErrors={errors.criteria}
            showRequired={attempted}
          />
        </EditorSection>

        <EditorSection
          title="Protections"
          description="Copies matching any protection are never removed. They still take part in ranking, so a better unprotected copy is kept as well."
        >
          <ProtectionsEditor
            protections={draft.protections}
            onChange={(protections) => {
              // Server errors are keyed by index: drop them when rows are added/removed.
              if (protections.length !== draft.protections.length) {
                setServerErrors((e) => ({ ...e, protection: {} }));
              }
              patch({ protections });
            }}
            types={protectionTypes(schema)}
            libraries={libraries}
            arrInstances={arrInstances}
            errors={errors.protection}
            listErrors={errors.protections}
            showRequired={attempted}
          />
        </EditorSection>
      </Modal>

      <ConfirmDialog
        open={confirmDelete}
        title="Delete Profile"
        message={
          <>
            Delete the profile <strong>{initialDraft.name}</strong>? Libraries using it fall back to the default profile
            and open duplicate groups are re-evaluated.
          </>
        }
        confirmLabel="Delete"
        loading={del.isPending}
        onConfirm={remove}
        onCancel={() => setConfirmDelete(false)}
      />
      <ConfirmDialog
        open={confirmDiscard}
        title="Discard Changes"
        message="You have unsaved changes to this profile. Discard them?"
        confirmLabel="Discard Changes"
        cancelLabel="Keep Editing"
        onConfirm={() => {
          setConfirmDiscard(false);
          onClose();
        }}
        onCancel={() => setConfirmDiscard(false)}
      />
    </>
  );
}
