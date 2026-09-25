/**
 * Add/edit a Tautulli connection: the play history of one Plex server, used by the Played / Last
 * played profile criteria (docs/DECISIONS.md D10). Dupearr only reads it; an unknown or unreadable
 * history never counts as "not played".
 */
import { useId, useState } from 'react';
import { errorMessage } from '@/api/client';
import { useCreateTautulli, useDeleteTautulli, useTestTautulli, useUpdateTautulli } from '@/api/hooks/useTautulli';
import type { Id, MediaServer, TautulliInstance, TautulliInstanceInput, TautulliTestResult } from '@/api/types';
import { Alert, FormGroup, Select, Switch, TextInput, useToast } from '@/components/ui';
import { usePreferences } from '@/app/preferences';
import { formatUtcDate } from '@/lib/format';
import { ConnectionModal, DetailList, SaveErrorAlert, TestResult } from './ConnectionModal';
import {
  addressChanged,
  hasErrors,
  isMaskedSecret,
  mergeFieldErrors,
  secretAtNewAddressMessage,
  summarizeSaveError,
  trimTrailingSlash,
  validateHttpUrl,
  type FieldErrors,
} from './connectionUtils';
import { isPlexServer } from './mediaServerShared';
import { SecretInput } from './SecretInput';

export interface TautulliFormValues {
  name: string;
  serverId: Id | null;
  url: string;
  apiKey: string;
  verifyTls: boolean;
  enabled: boolean;
}

const FIELDS = ['name', 'serverId', 'url', 'apiKey', 'verifyTls', 'enabled'] as const;

/** Oldest Tautulli that reads the API key from the X-Api-Key header (never sent in the URL). */
export const TAUTULLI_MIN_VERSION = '2.18.0';

export function tautulliFormValues(instance: TautulliInstance | null, servers: readonly MediaServer[]): TautulliFormValues {
  // Tautulli monitors Plex only: a Jellyfin server is never offered (the API refuses it).
  const plex = servers.filter(isPlexServer);
  return {
    name: instance?.name ?? 'Tautulli',
    serverId: instance?.serverId ?? (plex.length === 1 ? plex[0]!.id : null),
    url: instance?.url ?? '',
    apiKey: instance?.apiKey ?? '',
    verifyTls: instance?.verifyTls ?? true,
    enabled: instance?.enabled ?? true,
  };
}

/** Tautulli's own API path in the URL is a common mistake. */
function tautulliUrlCheck(url: URL): string | null {
  if (/\/api(\/v\d+)?\/?$/i.test(url.pathname)) {
    return 'Do not include "/api/v2" — enter the base address (and HTTP root if any), e.g. http://tautulli:8181';
  }
  return null;
}

/**
 * Client-side validation (the server validates again). `saved` is the stored connection when
 * editing: its masked API key may only be kept while the URL still addresses the same host.
 */
export function validateTautulli(values: TautulliFormValues, saved?: Pick<TautulliInstance, 'url'> | null): FieldErrors {
  const errors: FieldErrors = {};
  if (!values.name.trim()) errors.name = ['Name is required'];
  if (!values.serverId) errors.serverId = ['Choose the Plex server this Tautulli monitors'];
  const urlError = validateHttpUrl(values.url, { extra: tautulliUrlCheck });
  if (urlError) errors.url = [urlError];
  if (!values.apiKey.trim()) errors.apiKey = ['API key is required'];
  else if (saved && isMaskedSecret(values.apiKey) && addressChanged(saved.url, values.url)) {
    errors.apiKey = [secretAtNewAddressMessage('API key')];
  }
  return errors;
}

export function tautulliPayload(values: TautulliFormValues, id?: Id): TautulliInstanceInput {
  return {
    ...(id ? { id } : {}),
    name: values.name.trim(),
    serverId: values.serverId ?? 0,
    url: trimTrailingSlash(values.url),
    apiKey: isMaskedSecret(values.apiKey) ? values.apiKey : values.apiKey.trim(),
    verifyTls: values.verifyTls,
    enabled: values.enabled,
  };
}

export interface TautulliModalProps {
  /** Connection to edit; null = add. */
  instance: TautulliInstance | null;
  /** Configured media servers (the Plex server Tautulli monitors is chosen from them). */
  servers: readonly MediaServer[];
  onClose: () => void;
}

export function TautulliModal({ instance, servers: allServers, onClose }: TautulliModalProps) {
  const servers = allServers.filter(isPlexServer);
  const idPrefix = useId();
  const toast = useToast();
  const [values, setValues] = useState<TautulliFormValues>(() => tautulliFormValues(instance, servers));
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);

  const create = useCreateTautulli();
  const update = useUpdateTautulli();
  const remove = useDeleteTautulli();
  const test = useTestTautulli();

  const saving = create.isPending || update.isPending;
  const summary = summarizeSaveError(saveError, FIELDS);
  const errors = mergeFieldErrors(clientErrors, summary.fields);
  const id = (name: string) => `${idPrefix}-${name}`;
  const server = servers.find((s) => s.id === values.serverId);

  const set = <K extends keyof TautulliFormValues>(key: K, value: TautulliFormValues[K]) => {
    setValues((v) => ({ ...v, [key]: value }));
    setClientErrors((e) => {
      let next = e[key] ? { ...e, [key]: [] } : e;
      if (key === 'url' && next.apiKey?.length) next = { ...next, apiKey: [] };
      return next;
    });
    setSaveError(null);
    if (key === 'url' || key === 'apiKey' || key === 'verifyTls' || key === 'serverId') test.reset();
  };

  const runTest = () => {
    const errs = validateTautulli(values, instance);
    delete errs.name;
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    test.mutate(tautulliPayload(values, instance?.id));
  };

  const save = (forceSave = false) => {
    const errs = validateTautulli(values, instance);
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    setSaveError(null);
    const payload = tautulliPayload(values, instance?.id);
    const onSuccess = (saved: TautulliInstance) => {
      toast.success(instance ? 'Tautulli saved' : 'Tautulli added', saved?.name || payload.name);
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
        toast.success('Tautulli deleted', instance.name);
        onClose();
      },
      onError: (e) => toast.error('Unable to delete Tautulli', errorMessage(e)),
    });
  };

  return (
    <ConnectionModal
      open
      onClose={onClose}
      title={instance ? `Edit Tautulli — ${instance.name}` : 'Add Tautulli'}
      onSave={() => save(false)}
      saving={saving}
      onTest={runTest}
      testing={test.isPending}
      onDelete={instance ? del : undefined}
      deleting={remove.isPending}
      deleteTitle="Delete Tautulli"
      deleteMessage={
        <>
          Delete <strong>{instance?.name}</strong>? The Played and Last played criteria can no longer tell copies apart
          on this server (every play history becomes unknown). Nothing is changed in Tautulli.
        </>
      }
    >
      <p className="mt-0 mb-3 text-sm text-muted">
        Dupearr reads the play history Tautulli records for each Plex item (every user) to rank copies with the Played
        and Last played criteria. Requires Tautulli {TAUTULLI_MIN_VERSION} or later: the API key is only ever sent in a
        header, never in the URL.
      </p>

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
        label="Plex Server"
        htmlFor={id('serverId')}
        errors={errors.serverId}
        helpText="The Plex server this Tautulli monitors. Each scan checks that Tautulli still reports this server."
      >
        <Select
          id={id('serverId')}
          placeholder={servers.length === 0 ? 'Add a Plex server first' : 'Choose a Plex server…'}
          options={servers.map((s) => ({ value: String(s.id), label: s.name }))}
          value={values.serverId ? String(values.serverId) : ''}
          onChange={(v) => set('serverId', v ? Number(v) : null)}
        />
      </FormGroup>

      <FormGroup
        label="URL"
        htmlFor={id('url')}
        errors={errors.url}
        helpText="Include the HTTP root if Tautulli runs under one, e.g. http://192.168.1.10:8181/tautulli."
      >
        <TextInput
          id={id('url')}
          value={values.url}
          onChange={(e) => set('url', e.target.value)}
          placeholder="http://tautulli:8181"
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
        helpText="Tautulli → Settings → Web Interface → API (enable the API, then copy the key)"
      >
        <SecretInput id={id('apiKey')} value={values.apiKey} onChange={(k) => set('apiKey', k)} invalid={!!errors.apiKey?.length} />
      </FormGroup>

      <FormGroup
        label="Verify TLS"
        htmlFor={id('verifyTls')}
        advanced
        helpText="Verify the HTTPS certificate. Turn off only for self-signed certificates."
      >
        <Switch id={id('verifyTls')} checked={values.verifyTls} onChange={(v) => set('verifyTls', v)} aria-label="Verify TLS" />
      </FormGroup>

      <FormGroup label="Enabled" htmlFor={id('enabled')} helpText="A disabled connection is not read; every play history is then unknown.">
        <Switch id={id('enabled')} checked={values.enabled} onChange={(v) => set('enabled', v)} aria-label="Enabled" />
      </FormGroup>

      <TestResult
        className="mt-3"
        pending={test.isPending}
        error={test.error}
        successTitle="Connected to Tautulli"
        success={test.data ? <TautulliTestDetails result={test.data} serverName={server?.name} /> : null}
      />

      <SaveErrorAlert
        className="mt-3"
        messages={summary.messages}
        onForceSave={summary.canForce ? () => save(true) : undefined}
        forcing={saving}
      />
    </ConnectionModal>
  );
}

/** Version, monitored server and history coverage of a successful test. */
export function TautulliTestDetails({ result, serverName }: { result: TautulliTestResult; serverName?: string }) {
  const { preferences } = usePreferences();
  const gaps = result.librariesWithoutHistory.length > 0 || result.usersWithoutHistory > 0;
  return (
    <>
      <DetailList
        items={[
          { label: 'Version', value: result.version },
          {
            label: 'Monitors',
            value: (
              <>
                {result.pmsName || serverName || 'the Plex server'}{' '}
                {result.serverMatches && <span className="text-success">✓</span>}
              </>
            ),
          },
          {
            label: 'History since',
            value: result.historySince ? (
              formatUtcDate(result.historySince, preferences)
            ) : (
              <span className="text-muted">No plays recorded yet</span>
            ),
          },
        ]}
      />
      {gaps && (
        <Alert kind="info" className="mt-2" title="Some plays are not recorded">
          {result.librariesWithoutHistory.length > 0 && (
            <div>Libraries without history: {result.librariesWithoutHistory.join(', ')}.</div>
          )}
          {result.usersWithoutHistory > 0 && (
            <div>
              {result.usersWithoutHistory === 1 ? '1 user has' : `${result.usersWithoutHistory} users have`} “Keep History”
              turned off.
            </div>
          )}
          <div className="mt-1">
            There, a copy without recorded plays counts as unknown (a tie), never as “no plays recorded”.
          </div>
        </Alert>
      )}
    </>
  );
}
