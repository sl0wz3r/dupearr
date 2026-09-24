/**
 * "Add Exclusion" modal (Settings → Exclusions). Kinds: group_key, path_prefix, library,
 * title_regex — each with a value helper and client-side checks; server 400s map onto the fields.
 */
import { useState } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { useCreateExclusion } from '@/api/hooks';
import type { ExclusionInput, ExclusionKind, Library } from '@/api/types';
import { Alert, Button, FormGroup, Modal, Select, TextInput, useToast } from '@/components/ui';
import { EXCLUSION_KIND_LABELS, toOptions } from '@/lib/constants';
import { groupFieldErrors, isAbsolutePath, validateRegex } from './validation';

export const EXCLUSION_KINDS: readonly ExclusionKind[] = ['group_key', 'path_prefix', 'library', 'title_regex'];

/** Explanation + placeholder of each exclusion kind. */
export const EXCLUSION_KIND_HELP: Record<ExclusionKind, { help: string; placeholder: string }> = {
  group_key: {
    help: 'The stable key of one duplicate group (shown on its page), e.g. movie:tmdb:603. Tip: “Ignore” on a duplicate group can add this for you.',
    placeholder: 'movie:tmdb:603',
  },
  path_prefix: {
    help: 'Every file whose path starts with this folder is left out — either the path the media server reports or its mapped local path (case-insensitive).',
    placeholder: '/data/media/movies/Collections/',
  },
  library: {
    help: 'Every item in this library is left out of duplicate detection.',
    placeholder: '',
  },
  title_regex: {
    help: 'Movie or show titles matching this regular expression are left out (Go RE2 syntax, always case-insensitive).',
    placeholder: '^star wars',
  },
};

/** Client-side check of an exclusion value; null when valid. */
export function validateExclusion(kind: ExclusionKind, value: string): string | null {
  const v = value.trim();
  switch (kind) {
    case 'group_key':
      if (!v) return 'A group key is required';
      // Keys are matched exactly (case-sensitive) by the engine and always start in lower case.
      return /^(movie|episode|plex):\S+$/.test(v)
        ? null
        : 'Group keys look like movie:tmdb:603, episode:tvdb:349232 or plex:1:12345 (lower case, no spaces)';
    case 'path_prefix':
      if (!v) return 'A path is required';
      return isAbsolutePath(v) ? null : 'Use an absolute path, e.g. /data/media/movies/Collections/';
    case 'library':
      return v ? null : 'Choose a library';
    case 'title_regex':
      return validateRegex(v);
    default:
      return v ? null : 'A value is required';
  }
}

type Errors = Partial<Record<'kind' | 'value' | 'title' | 'reason' | 'general', string[]>>;

export interface ExclusionModalProps {
  open: boolean;
  libraries: readonly Library[];
  onClose: () => void;
}

export function ExclusionModal({ open, libraries, onClose }: ExclusionModalProps) {
  const create = useCreateExclusion();
  const toast = useToast();
  const [kind, setKind] = useState<ExclusionKind>('path_prefix');
  const [value, setValue] = useState('');
  const [title, setTitle] = useState('');
  const [reason, setReason] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [serverErrors, setServerErrors] = useState<Errors>({});

  const localError = attempted ? validateExclusion(kind, value) : null;
  const valueErrors = [...(localError ? [localError] : []), ...(serverErrors.value ?? [])];
  const libraryOptions = libraries.map((l) => ({ value: String(l.id), label: l.title || `Library ${l.id}` }));

  const submit = () => {
    setAttempted(true);
    setServerErrors({});
    if (validateExclusion(kind, value)) return;
    const lib = kind === 'library' ? libraries.find((l) => String(l.id) === value) : undefined;
    const body: ExclusionInput = {
      kind,
      // Trimmed like the engine does before matching, so what is stored is what is used.
      value: value.trim(),
      ...(title.trim() || lib ? { title: title.trim() || lib?.title } : {}),
      ...(reason.trim() ? { reason: reason.trim() } : {}),
    };
    create.mutate(body, {
      onSuccess: () => {
        toast.success('Exclusion added');
        onClose();
      },
      onError: (e) => {
        if (isApiError(e) && e.isValidation) {
          const f = groupFieldErrors(e.validationErrors, ['kind', 'value', 'title', 'reason']);
          setServerErrors({ kind: f.kind, value: f.value, title: f.title, reason: f.reason, general: f[''] });
        } else {
          setServerErrors({ general: [errorMessage(e, 'Could not add the exclusion')] });
        }
      },
    });
  };

  const help = EXCLUSION_KIND_HELP[kind];

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Add Exclusion"
      footer={
        <>
          <Button onClick={onClose} disabled={create.isPending}>
            Cancel
          </Button>
          <Button variant="primary" loading={create.isPending} onClick={submit}>
            Save
          </Button>
        </>
      }
    >
      {serverErrors.general && serverErrors.general.length > 0 && (
        <Alert kind="error" className="mb-4">
          {serverErrors.general.join('\n')}
        </Alert>
      )}
      <FormGroup label="Kind" htmlFor="exclusion-kind" errors={serverErrors.kind}>
        <Select<ExclusionKind>
          id="exclusion-kind"
          options={toOptions(EXCLUSION_KIND_LABELS, [...EXCLUSION_KINDS])}
          value={kind}
          onChange={(k) => {
            setKind(k);
            setValue('');
            setServerErrors({});
          }}
        />
      </FormGroup>
      <FormGroup label="Value" htmlFor="exclusion-value" helpText={help.help} errors={valueErrors}>
        {kind === 'library' ? (
          <Select
            id="exclusion-value"
            placeholder={libraryOptions.length === 0 ? 'No libraries configured' : 'Choose a library…'}
            options={libraryOptions}
            value={value}
            invalid={valueErrors.length > 0}
            onChange={setValue}
          />
        ) : (
          <TextInput
            id="exclusion-value"
            className="font-mono"
            value={value}
            placeholder={help.placeholder}
            invalid={valueErrors.length > 0}
            onChange={(e) => setValue(e.target.value)}
          />
        )}
      </FormGroup>
      <FormGroup
        label="Title"
        htmlFor="exclusion-title"
        helpText="Optional display name, e.g. the movie title."
        errors={serverErrors.title}
      >
        <TextInput id="exclusion-title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
      </FormGroup>
      <FormGroup label="Reason" htmlFor="exclusion-reason" helpText="Optional note explaining why." errors={serverErrors.reason}>
        <TextInput id="exclusion-reason" value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </FormGroup>
    </Modal>
  );
}
