import { clsx } from 'clsx';
import { ArrowLeft } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage } from '@/api/client';
import {
  useCreateMediaServer,
  useDeleteMediaServer,
  useMediaServers,
  useTestMediaServer,
  useUpdateMediaServer,
} from '@/api/hooks/useMediaServers';
import type { Id, MediaServer, MediaServerInput, MediaServerStorage, MediaServerTestResult } from '@/api/types';
import { Alert, Badge, Button, FormGroup, Select, Switch, TextInput, useToast } from '@/components/ui';
import { ConnectionModal, DetailList, SaveErrorAlert, TestResult } from './ConnectionModal';
import {
  addressChanged,
  hasErrors,
  isMaskedSecret,
  jellyfinUrlCheck,
  mergeFieldErrors,
  secretAtNewAddressMessage,
  summarizeSaveError,
  trimTrailingSlash,
  validateHttpUrl,
  type FieldErrors,
} from './connectionUtils';
import { FIELDS, STORAGE_OPTIONS, showStorageField } from './mediaServerShared';
import { SecretInput } from './SecretInput';

/**
 * Jellyfin 12.1+ as a media server (docs/DECISIONS.md D12). Dupearr only reads from Jellyfin: it
 * never deletes through it, its copies are only removed through Radarr/Sonarr or into Dupearr's
 * recycle bin, by a person's approval of one duplicate at a time, and only while every copy has a
 * path mapping. The API key is an administrator key, sent only in a header.
 */

/** Editable fields of a Jellyfin server. */
export interface JellyfinFormValues {
  name: string;
  url: string;
  /** The API key (the server's "token" property). */
  apiKey: string;
  verifyTls: boolean;
  enabled: boolean;
  storage?: MediaServerStorage;
}

/** Initial form values for a new server or an existing one (the key stays masked). */
export function jellyfinFormValues(server: MediaServer | null): JellyfinFormValues {
  return {
    name: server?.name ?? 'Jellyfin',
    url: server?.url ?? '',
    apiKey: server?.token ?? '',
    verifyTls: server?.verifyTls ?? true,
    enabled: server?.enabled ?? true,
    storage: server?.storage ?? '',
  };
}

/**
 * Client-side validation (the server validates again): a stored (masked) key is only kept while the
 * URL still addresses the same host.
 */
export function validateJellyfinServer(values: JellyfinFormValues, saved?: Pick<MediaServer, 'url'> | null): FieldErrors {
  const errors: FieldErrors = {};
  if (!values.name.trim()) errors.name = ['Name is required'];
  const urlError = validateHttpUrl(values.url, { extra: jellyfinUrlCheck });
  if (urlError) errors.url = [urlError];
  if (!values.apiKey.trim()) {
    errors.token = ['API key is required — create one in Jellyfin → Dashboard → API Keys'];
  } else if (saved && isMaskedSecret(values.apiKey) && addressChanged(saved.url, values.url)) {
    errors.token = [secretAtNewAddressMessage('API key')];
  }
  return errors;
}

/** Request body for create/update/test (a masked key is sent back unchanged to keep the stored one). */
export function jellyfinPayload(values: JellyfinFormValues, id?: Id, withStorage = false): MediaServerInput {
  return {
    ...(id ? { id } : {}),
    name: values.name.trim(),
    kind: 'jellyfin',
    url: trimTrailingSlash(values.url),
    token: isMaskedSecret(values.apiKey) ? values.apiKey : values.apiKey.trim(),
    verifyTls: values.verifyTls,
    enabled: values.enabled,
    ...(withStorage ? { storage: values.storage ?? '' } : {}),
  };
}

/** Details of a successful Jellyfin connection test. */
export function JellyfinTestDetails({ result }: { result: MediaServerTestResult }) {
  return (
    <DetailList
      items={[
        { label: 'Server', value: result.friendlyName },
        { label: 'Product', value: result.product ?? 'Jellyfin Server' },
        { label: 'Version', value: result.version },
        {
          label: 'Server ID',
          value: result.machineIdentifier ? <code className="text-xs">{result.machineIdentifier}</code> : '',
        },
        {
          label: 'Administrator',
          value: result.administrator ? (
            <Badge kind="success" outline>
              ✓ API key or administrator
            </Badge>
          ) : (
            <Badge kind="warning" outline>
              No
            </Badge>
          ),
        },
      ]}
    />
  );
}

/**
 * The warnings of a successful test: removals disabled (path substitutions, an unreadable
 * configuration), and a saved server whose URL now reaches another Jellyfin server (the API refuses
 * to save that).
 */
export function JellyfinTestWarnings({
  result,
  previousMachineId,
  className,
}: {
  result: MediaServerTestResult;
  previousMachineId?: string;
  className?: string;
}) {
  const changedServer =
    !!previousMachineId && !!result.machineIdentifier && previousMachineId !== result.machineIdentifier;
  if (!result.removalsDisabled && !changedServer) return null;
  return (
    <div className={clsx('flex flex-col gap-2', className)}>
      {result.removalsDisabled && (
        <Alert kind="warning" title="Removals from this server are disabled">
          {result.removalsDisabled}. Dupearr removes nothing this server lists until this is fixed, and while path
          substitutions are set it does not read its libraries at all.
        </Alert>
      )}
      {changedServer && (
        <Alert kind="warning" title="Different Jellyfin server">
          This URL reaches a different Jellyfin server (server ID {result.machineIdentifier}) than the one saved before (
          {previousMachineId}). Saving is refused: add it as a new media server instead.
        </Alert>
      )}
    </div>
  );
}

/** Add/edit a Jellyfin server: URL + API key, Test, Save (anyway), Delete. */
export function JellyfinServerForm({
  server,
  onClose,
  onBack,
}: {
  server: MediaServer | null;
  onClose: () => void;
  onBack?: () => void;
}) {
  const idPrefix = useId();
  const toast = useToast();
  const [values, setValues] = useState<JellyfinFormValues>(() => jellyfinFormValues(server));
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);

  const create = useCreateMediaServer();
  const update = useUpdateMediaServer();
  const remove = useDeleteMediaServer();
  const test = useTestMediaServer();
  const servers = useMediaServers();
  const withStorage = showStorageField(server, servers.data);

  const saving = create.isPending || update.isPending;
  const summary = summarizeSaveError(saveError, FIELDS);
  const errors = mergeFieldErrors(clientErrors, summary.fields);

  const set = <K extends keyof JellyfinFormValues>(key: K, value: JellyfinFormValues[K]) => {
    setValues((v) => ({ ...v, [key]: value }));
    const field = key === 'apiKey' ? 'token' : key;
    setClientErrors((e) => {
      let next = e[field] ? { ...e, [field]: [] } : e;
      // The "enter the key again" error depends on the URL too.
      if (key === 'url' && next.token?.length) next = { ...next, token: [] };
      return next;
    });
    setSaveError(null);
    if (key === 'url' || key === 'apiKey' || key === 'verifyTls') test.reset();
  };

  const runTest = () => {
    const errs = validateJellyfinServer(values, server);
    delete errs.name; // not needed to test
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    test.mutate(jellyfinPayload(values, server?.id));
  };

  const save = (forceSave = false) => {
    const errs = validateJellyfinServer(values, server);
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    setSaveError(null);
    const onSuccess = (saved: MediaServer) => {
      toast.success(server ? 'Media server saved' : 'Media server added', saved?.name || values.name);
      onClose();
    };
    const onError = (e: unknown) => setSaveError(e);
    const payload = jellyfinPayload(values, server?.id, withStorage);
    if (server) {
      update.mutate({ server: { ...payload, id: server.id }, forceSave }, { onSuccess, onError });
    } else {
      create.mutate({ server: payload, forceSave }, { onSuccess, onError });
    }
  };

  const del = () => {
    if (!server) return;
    remove.mutate(server.id, {
      onSuccess: () => {
        toast.success('Media server deleted', server.name);
        onClose();
      },
      onError: (e) => toast.error('Unable to delete media server', errorMessage(e)),
    });
  };

  const id = (name: string) => `${idPrefix}-${name}`;

  return (
    <ConnectionModal
      open
      onClose={onClose}
      title={server ? `Edit Media Server — ${server.name}` : 'Add Media Server — Jellyfin'}
      onSave={() => save(false)}
      saving={saving}
      onTest={runTest}
      testing={test.isPending}
      onDelete={server ? del : undefined}
      deleting={remove.isPending}
      deleteTitle="Delete Media Server"
      deleteMessage={
        <>
          Delete <strong>{server?.name}</strong>? Dupearr stops scanning this server. No media files are touched.
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
      <Alert kind="info" title="Dupearr only reads from Jellyfin" className="mb-4">
        It never deletes anything through Jellyfin (its delete removes whole folders). Jellyfin copies are removed only
        through Radarr/Sonarr or into Dupearr&apos;s recycle bin, when you approve a duplicate on its own page, and only
        when every copy has a path mapping (Settings → Media Management), because Jellyfin never reports whether a file
        exists.
      </Alert>

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
        helpText="Address Dupearr uses to reach Jellyfin, including the port (default 8096) and any base URL."
      >
        <TextInput
          id={id('url')}
          value={values.url}
          onChange={(e) => set('url', e.target.value)}
          placeholder="http://jellyfin:8096"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          invalid={!!errors.url?.length}
        />
      </FormGroup>

      <FormGroup
        label="API Key"
        htmlFor={id('token')}
        errors={errors.token}
        helpText="Create an API key in Jellyfin → Dashboard → API Keys. It is an administrator key: Dupearr only reads with it (and needs that to see every playback session), sends it only in a request header and never deletes through Jellyfin. Revoke it there if your Dupearr data or a backup leaks."
      >
        <SecretInput
          id={id('token')}
          value={values.apiKey}
          onChange={(k) => set('apiKey', k)}
          invalid={!!errors.token?.length}
        />
      </FormGroup>

      <FormGroup
        label="Verify TLS"
        htmlFor={id('verifyTls')}
        advanced
        helpText="Verify the server's HTTPS certificate. Turn off only for self-signed certificates."
      >
        <Switch id={id('verifyTls')} checked={values.verifyTls} onChange={(v) => set('verifyTls', v)} aria-label="Verify TLS" />
      </FormGroup>

      <FormGroup label="Enabled" htmlFor={id('enabled')} helpText="Disabled servers are not scanned.">
        <Switch id={id('enabled')} checked={values.enabled} onChange={(v) => set('enabled', v)} aria-label="Enabled" />
      </FormGroup>

      {withStorage && (
        <FormGroup
          label="Storage"
          htmlFor={id('storage')}
          errors={errors.storage}
          helpText="With several media servers, Dupearr never removes a file another server lists unless it can prove that server keeps a different copy. Never declare a server on the same storage as separate."
        >
          <Select
            id={id('storage')}
            options={STORAGE_OPTIONS}
            value={values.storage}
            onChange={(v) => set('storage', v)}
            invalid={!!errors.storage?.length}
          />
        </FormGroup>
      )}

      <TestResult
        className="mt-3"
        pending={test.isPending}
        error={test.error}
        successTitle="Connected to Jellyfin"
        success={test.data ? <JellyfinTestDetails result={test.data} /> : null}
      />
      {test.data && !test.isPending && (
        <JellyfinTestWarnings className="mt-2" result={test.data} previousMachineId={server?.machineIdentifier} />
      )}

      <SaveErrorAlert
        className="mt-3"
        messages={summary.messages}
        onForceSave={summary.canForce ? () => save(true) : undefined}
        forcing={saving}
      />
    </ConnectionModal>
  );
}
