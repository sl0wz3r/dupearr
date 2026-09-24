import { ArrowLeft, Clapperboard, Tv } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage } from '@/api/client';
import {
  useCreateArrInstance,
  useDeleteArrInstance,
  useTestArrInstance,
  useUpdateArrInstance,
} from '@/api/hooks/useArr';
import type { ArrInstance, ArrInstanceInput, ArrKind, ArrTestResult, Id } from '@/api/types';
import { Alert, Button, FormGroup, Switch, TextInput, useToast } from '@/components/ui';
import { ARR_KIND_LABELS, labelOf } from '@/lib/constants';
import { ProviderCard } from './ConnectionCard';
import { ConnectionModal, DetailList, SaveErrorAlert, TestResult } from './ConnectionModal';
import {
  addressChanged,
  arrUrlCheck,
  hasErrors,
  isMaskedSecret,
  mergeFieldErrors,
  secretAtNewAddressMessage,
  summarizeSaveError,
  trimTrailingSlash,
  validateHttpUrl,
  type FieldErrors,
} from './connectionUtils';
import { SecretInput } from './SecretInput';
import { TagsInput } from './TagsInput';

interface ArrProvider {
  kind: ArrKind;
  description: string;
  icon: typeof Tv;
  defaultPort: number;
  infoUrl: string;
}

export const ARR_PROVIDERS: readonly ArrProvider[] = [
  {
    kind: 'radarr',
    description: 'Movies',
    icon: Clapperboard,
    defaultPort: 7878,
    infoUrl: 'https://wiki.servarr.com/radarr',
  },
  {
    kind: 'sonarr',
    description: 'TV series',
    icon: Tv,
    defaultPort: 8989,
    infoUrl: 'https://wiki.servarr.com/sonarr',
  },
];

function providerOf(kind: ArrKind): ArrProvider {
  return ARR_PROVIDERS.find((p) => p.kind === kind) ?? ARR_PROVIDERS[0]!;
}

export interface ArrFormValues {
  name: string;
  url: string;
  apiKey: string;
  verifyTls: boolean;
  enabled: boolean;
  tags: string[];
}

const FIELDS = ['name', 'url', 'apiKey', 'verifyTls', 'enabled', 'tags'] as const;

export function arrFormValues(instance: ArrInstance | null, kind: ArrKind): ArrFormValues {
  return {
    name: instance?.name ?? labelOf(ARR_KIND_LABELS, kind),
    url: instance?.url ?? '',
    apiKey: instance?.apiKey ?? '',
    verifyTls: instance?.verifyTls ?? true,
    enabled: instance?.enabled ?? true,
    tags: [...(instance?.tags ?? [])],
  };
}

/**
 * Client-side validation (the server validates again). `saved` is the stored instance when
 * editing: its masked API key may only be kept while the URL still addresses the same host.
 */
export function validateArr(values: ArrFormValues, saved?: Pick<ArrInstance, 'url'> | null): FieldErrors {
  const errors: FieldErrors = {};
  if (!values.name.trim()) errors.name = ['Name is required'];
  const urlError = validateHttpUrl(values.url, { extra: arrUrlCheck });
  if (urlError) errors.url = [urlError];
  if (!values.apiKey.trim()) errors.apiKey = ['API key is required'];
  else if (saved && isMaskedSecret(values.apiKey) && addressChanged(saved.url, values.url)) {
    errors.apiKey = [secretAtNewAddressMessage('API key')];
  }
  return errors;
}

/** *arr API keys are 32 hex characters; anything else is probably a copy/paste mistake (warning only). */
export function looksLikeArrApiKey(value: string): boolean {
  const key = value.trim();
  return !key || isMaskedSecret(key) || /^[A-Za-z0-9]{16,64}$/.test(key);
}

export function arrPayload(values: ArrFormValues, kind: ArrKind, id?: Id): ArrInstanceInput {
  return {
    ...(id ? { id } : {}),
    name: values.name.trim(),
    kind,
    url: trimTrailingSlash(values.url),
    apiKey: isMaskedSecret(values.apiKey) ? values.apiKey : values.apiKey.trim(),
    verifyTls: values.verifyTls,
    enabled: values.enabled,
    tags: values.tags,
  };
}

export interface ArrInstanceModalProps {
  /** Instance to edit; null = add (starts with the Radarr/Sonarr picker). */
  instance: ArrInstance | null;
  onClose: () => void;
}

/** Add/edit a Radarr/Sonarr instance. */
export function ArrInstanceModal({ instance, onClose }: ArrInstanceModalProps) {
  const [kind, setKind] = useState<ArrKind | null>(instance?.kind ?? null);
  if (!kind) {
    return (
      <ConnectionModal open onClose={onClose} title="Add Application" size="md">
        <p className="mt-0 mb-3 text-sm text-muted">
          Dupearr reads quality, custom formats and tags from Radarr/Sonarr and deletes tracked files through them.
        </p>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {ARR_PROVIDERS.map((p) => (
            <ProviderCard
              key={p.kind}
              name={labelOf(ARR_KIND_LABELS, p.kind)}
              description={p.description}
              icon={p.icon}
              infoUrl={p.infoUrl}
              onSelect={() => setKind(p.kind)}
            />
          ))}
        </div>
      </ConnectionModal>
    );
  }
  return <ArrInstanceForm instance={instance} kind={kind} onClose={onClose} onBack={instance ? undefined : () => setKind(null)} />;
}

function ArrInstanceForm({
  instance,
  kind,
  onClose,
  onBack,
}: {
  instance: ArrInstance | null;
  kind: ArrKind;
  onClose: () => void;
  onBack?: () => void;
}) {
  const idPrefix = useId();
  const toast = useToast();
  const provider = providerOf(kind);
  const kindLabel = labelOf(ARR_KIND_LABELS, kind);
  const [values, setValues] = useState<ArrFormValues>(() => arrFormValues(instance, kind));
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);

  const create = useCreateArrInstance();
  const update = useUpdateArrInstance();
  const remove = useDeleteArrInstance();
  const test = useTestArrInstance();

  const saving = create.isPending || update.isPending;
  const summary = summarizeSaveError(saveError, FIELDS);
  const errors = mergeFieldErrors(clientErrors, summary.fields);
  const id = (name: string) => `${idPrefix}-${name}`;

  const set = <K extends keyof ArrFormValues>(key: K, value: ArrFormValues[K]) => {
    setValues((v) => ({ ...v, [key]: value }));
    setClientErrors((e) => {
      let next = e[key] ? { ...e, [key]: [] } : e;
      // The "re-enter the API key" error depends on the URL too.
      if (key === 'url' && next.apiKey?.length) next = { ...next, apiKey: [] };
      return next;
    });
    setSaveError(null);
    if (key === 'url' || key === 'apiKey' || key === 'verifyTls') test.reset();
  };

  const runTest = () => {
    const errs = validateArr(values, instance);
    delete errs.name;
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    test.mutate(arrPayload(values, kind, instance?.id));
  };

  const save = (forceSave = false) => {
    const errs = validateArr(values, instance);
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    setSaveError(null);
    const payload = arrPayload(values, kind, instance?.id);
    const onSuccess = (saved: ArrInstance) => {
      toast.success(instance ? `${kindLabel} saved` : `${kindLabel} added`, saved?.name || payload.name);
      onClose();
    };
    const onError = (e: unknown) => setSaveError(e);
    if (instance) update.mutate({ instance: { ...payload, id: instance.id }, forceSave }, { onSuccess, onError });
    else create.mutate({ instance: payload, forceSave }, { onSuccess, onError });
  };

  const del = () => {
    if (!instance) return;
    remove.mutate(instance.id, {
      onSuccess: () => {
        toast.success(`${kindLabel} deleted`, instance.name);
        onClose();
      },
      onError: (e) => toast.error(`Unable to delete ${kindLabel}`, errorMessage(e)),
    });
  };

  return (
    <ConnectionModal
      open
      onClose={onClose}
      title={instance ? `Edit ${kindLabel} — ${instance.name}` : `Add ${kindLabel}`}
      onSave={() => save(false)}
      saving={saving}
      onTest={runTest}
      testing={test.isPending}
      onDelete={instance ? del : undefined}
      deleting={remove.isPending}
      deleteTitle={`Delete ${kindLabel}`}
      deleteMessage={
        <>
          Delete <strong>{instance?.name}</strong>? Dupearr stops reading from and deleting through this instance.
          Nothing is changed in {kindLabel}.
        </>
      }
      footerStart={
        onBack ? (
          <Button icon={ArrowLeft} onClick={onBack} disabled={saving}>
            Back
          </Button>
        ) : undefined
      }
    >
      <FormGroup label="Name" htmlFor={id('name')} errors={errors.name}>
        <TextInput
          id={id('name')}
          value={values.name}
          onChange={(e) => set('name', e.target.value)}
          invalid={!!errors.name?.length}
          maxLength={100}
        />
      </FormGroup>

      <FormGroup
        label="URL"
        htmlFor={id('url')}
        errors={errors.url}
        helpText={`Include the URL base if ${kindLabel} runs under one, e.g. http://192.168.1.10:${provider.defaultPort}/${kind}.`}
      >
        <TextInput
          id={id('url')}
          value={values.url}
          onChange={(e) => set('url', e.target.value)}
          placeholder={`http://${kind}:${provider.defaultPort}`}
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          invalid={!!errors.url?.length}
        />
      </FormGroup>

      <FormGroup
        label="API Key"
        htmlFor={id('apiKey')}
        errors={errors.apiKey}
        warning={
          looksLikeArrApiKey(values.apiKey)
            ? undefined
            : `That doesn't look like a ${kindLabel} API key (usually 32 letters and digits).`
        }
        helpText={`${kindLabel} → Settings → General → Security → API Key`}
      >
        <SecretInput
          id={id('apiKey')}
          value={values.apiKey}
          onChange={(k) => set('apiKey', k)}
          invalid={!!errors.apiKey?.length}
        />
      </FormGroup>

      <FormGroup
        label="Verify TLS"
        htmlFor={id('verifyTls')}
        advanced
        helpText="Verify the HTTPS certificate. Turn off only for self-signed certificates."
      >
        <Switch id={id('verifyTls')} checked={values.verifyTls} onChange={(v) => set('verifyTls', v)} aria-label="Verify TLS" />
      </FormGroup>

      <FormGroup label="Enabled" htmlFor={id('enabled')} helpText="Disabled instances are neither read nor used for deletions.">
        <Switch id={id('enabled')} checked={values.enabled} onChange={(v) => set('enabled', v)} aria-label="Enabled" />
      </FormGroup>

      <FormGroup label="Tags" htmlFor={id('tags')} errors={errors.tags} helpText="Optional labels for this instance, e.g. 4k.">
        <TagsInput id={id('tags')} value={values.tags} onChange={(tags) => set('tags', tags)} />
      </FormGroup>

      <TestResult
        className="mt-3"
        pending={test.isPending}
        error={test.error}
        successTitle={`Connected to ${test.data?.appName || kindLabel}`}
        success={test.data ? <ArrTestDetails result={test.data} /> : null}
      />
      {test.data && !test.isPending && <ArrTestWarnings className="mt-2" result={test.data} kind={kind} />}

      <SaveErrorAlert
        className="mt-3"
        messages={summary.messages}
        onForceSave={summary.canForce ? () => save(true) : undefined}
        forcing={saving}
      />
    </ConnectionModal>
  );
}

/** Version / instance / recycle bin of a successful test. */
export function ArrTestDetails({ result }: { result: ArrTestResult }) {
  return (
    <DetailList
      items={[
        { label: 'Application', value: result.appName },
        { label: 'Version', value: result.version },
        { label: 'Instance', value: result.instanceName },
        {
          label: 'Recycle bin',
          value: result.recycleBin ? (
            <>
              <code className="text-xs">{result.recycleBin}</code>
              {result.recycleBinCleanupDays > 0 && (
                <span className="text-muted"> (emptied after {result.recycleBinCleanupDays} days)</span>
              )}
            </>
          ) : (
            <span className="text-warning">Not configured</span>
          ),
        },
      ]}
    />
  );
}

/** Safety warnings of a successful test: permanent deletions, wrong application. */
export function ArrTestWarnings({ result, kind, className }: { result: ArrTestResult; kind: ArrKind; className?: string }) {
  const kindLabel = labelOf(ARR_KIND_LABELS, kind);
  const wrongApp = !!result.appName && result.appName.toLowerCase() !== kind;
  if (result.recycleBin && !wrongApp) return null;
  return (
    <div className={className}>
      {wrongApp && (
        <Alert kind="error" title={`This is ${result.appName}, not ${kindLabel}`} className="mb-2">
          Check the URL — Dupearr would read and delete files through the wrong application.
        </Alert>
      )}
      {!result.recycleBin && (
        <Alert kind="warning" title="Recycle bin not configured — deletions through this instance are PERMANENT">
          In {kindLabel} open Settings → Media Management (Show Advanced) and set a <strong>Recycling Bin</strong>{' '}
          folder so files deleted through {kindLabel} can be recovered.
        </Alert>
      )}
    </div>
  );
}
