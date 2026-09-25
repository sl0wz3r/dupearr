import { clsx } from 'clsx';
import { ArrowLeft, ExternalLink, Server } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage } from '@/api/client';
import {
  useCreateMediaServer,
  useDeleteMediaServer,
  useMediaServers,
  useTestMediaServer,
  useUpdateMediaServer,
} from '@/api/hooks/useMediaServers';
import type {
  Id,
  MediaServer,
  MediaServerInput,
  MediaServerKind,
  MediaServerStorage,
  MediaServerTestResult,
} from '@/api/types';
import { Alert, Badge, Button, ConfirmDialog, FormGroup, Select, Switch, TextInput, useToast } from '@/components/ui';
import { MEDIA_SERVER_KIND_LABELS, labelOf } from '@/lib/constants';
import { ProviderCard } from './ConnectionCard';
import { ConnectionModal, DetailList, SaveErrorAlert, TestResult } from './ConnectionModal';
import {
  addressChanged,
  hasErrors,
  isMaskedSecret,
  looksLikeJwt,
  mergeFieldErrors,
  plexUrlCheck,
  secretAtNewAddressMessage,
  summarizeSaveError,
  trimTrailingSlash,
  validateHttpUrl,
  type FieldErrors,
} from './connectionUtils';
import { JellyfinServerForm } from './JellyfinServerForm';
import { FIELDS, STORAGE_OPTIONS, showStorageField } from './mediaServerShared';
import { PlexSignIn } from './PlexSignIn';
import type { PlexServerSelection } from './plexSignInMachine';
import { SecretInput } from './SecretInput';

/** Editable fields of a media server. */
export interface MediaServerFormValues {
  name: string;
  url: string;
  token: string;
  verifyTls: boolean;
  enabled: boolean;
  machineIdentifier: string;
  /** docs/DECISIONS.md D11: "" (or absent) = same storage as the other servers, "separate" = its own. */
  storage?: MediaServerStorage;
}

export { showStorageField };

const PLEX_TOKEN_HELP_URL = 'https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/';

/** Initial form values for a new server or an existing one (token stays masked). */
export function mediaServerFormValues(server: MediaServer | null): MediaServerFormValues {
  return {
    name: server?.name ?? 'Plex',
    url: server?.url ?? '',
    token: server?.token ?? '',
    verifyTls: server?.verifyTls ?? true,
    enabled: server?.enabled ?? true,
    machineIdentifier: server?.machineIdentifier ?? '',
    storage: server?.storage ?? '',
  };
}

/**
 * Client-side validation (the server validates again). `saved` is the stored server when editing:
 * its masked token may only be kept while the URL still addresses the same host.
 */
export function validateMediaServer(
  values: MediaServerFormValues,
  saved?: Pick<MediaServer, 'url'> | null,
): FieldErrors {
  const errors: FieldErrors = {};
  if (!values.name.trim()) errors.name = ['Name is required'];
  const urlError = validateHttpUrl(values.url, { extra: plexUrlCheck });
  if (urlError) errors.url = [urlError];
  if (!values.token.trim()) {
    errors.token = ['Token is required — use “Sign in with Plex” or paste an X-Plex-Token'];
  } else if (saved && isMaskedSecret(values.token) && addressChanged(saved.url, values.url)) {
    errors.token = [secretAtNewAddressMessage('token')];
  }
  return errors;
}

/**
 * Machine identifier the form now points at when it differs from the saved one: from the last
 * successful test, else from a "Sign in with Plex" selection. null when unknown or unchanged.
 */
export function changedMachineId(
  savedMachineId: string | null | undefined,
  testedMachineId: string | null | undefined,
  formMachineId: string | null | undefined,
): string | null {
  if (!savedMachineId) return null;
  const candidate = testedMachineId || formMachineId || '';
  return candidate && candidate !== savedMachineId ? candidate : null;
}

/**
 * Request body for create/update/test (masked tokens are sent back unchanged to keep the stored
 * one). The storage setting is sent when its field is shown (withStorage); otherwise the server
 * keeps what it stored.
 */
export function mediaServerPayload(values: MediaServerFormValues, id?: Id, withStorage = false): MediaServerInput {
  return {
    ...(id ? { id } : {}),
    name: values.name.trim(),
    kind: 'plex',
    url: trimTrailingSlash(values.url),
    token: isMaskedSecret(values.token) ? values.token : values.token.trim(),
    verifyTls: values.verifyTls,
    enabled: values.enabled,
    ...(values.machineIdentifier ? { machineIdentifier: values.machineIdentifier } : {}),
    ...(withStorage ? { storage: values.storage ?? '' } : {}),
  };
}

export interface MediaServerModalProps {
  /** Server to edit; null = add a new one. */
  server: MediaServer | null;
  /** Kind of a new server (default Plex); an existing server keeps its own. */
  kind?: MediaServerKind;
  onClose: () => void;
  /** Back to the server picker (new servers only). */
  onBack?: () => void;
}

/**
 * Add/edit a media server: a Jellyfin server gets {@link JellyfinServerForm}, anything else the Plex
 * form ("Sign in with Plex" or manual URL + token, Test, Save (anyway), Delete).
 */
export function MediaServerModal({ server, kind, onClose, onBack }: MediaServerModalProps) {
  if ((server?.kind ?? kind) === 'jellyfin') {
    return <JellyfinServerForm server={server} onClose={onClose} onBack={onBack} />;
  }
  return <PlexServerForm server={server} onClose={onClose} onBack={onBack} />;
}

/** The kinds a new media server can be (docs/DECISIONS.md D12: Jellyfin is read-only). */
const SERVER_PROVIDERS: readonly { kind: MediaServerKind; description: string; infoUrl: string }[] = [
  {
    kind: 'plex',
    description: 'Removes through Radarr/Sonarr, Plex or the recycle bin',
    infoUrl: 'https://www.plex.tv/',
  },
  {
    kind: 'jellyfin',
    description: 'Version 12.1 or later. Read-only: removals only through Radarr/Sonarr or the recycle bin, by manual approval',
    infoUrl: 'https://jellyfin.org/',
  },
];

/** "Add Media Server": pick Plex or Jellyfin, then its form. */
export function AddMediaServerModal({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState<MediaServerKind | null>(null);
  if (!kind) {
    return (
      <ConnectionModal open onClose={onClose} title="Add Media Server" size="md">
        <p className="mt-0 mb-3 text-sm text-muted">
          Dupearr reads the libraries of your media server to find the copies of a title.
        </p>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {SERVER_PROVIDERS.map((p) => (
            <ProviderCard
              key={p.kind}
              name={labelOf(MEDIA_SERVER_KIND_LABELS, p.kind)}
              description={p.description}
              icon={Server}
              infoUrl={p.infoUrl}
              onSelect={() => setKind(p.kind)}
            />
          ))}
        </div>
      </ConnectionModal>
    );
  }
  return <MediaServerModal server={null} kind={kind} onClose={onClose} onBack={() => setKind(null)} />;
}

/** Add/edit a Plex server: "Sign in with Plex" or manual URL + token, Test, Save (anyway), Delete. */
function PlexServerForm({ server, onClose, onBack }: Omit<MediaServerModalProps, 'kind'>) {
  const idPrefix = useId();
  const toast = useToast();
  const [values, setValues] = useState<MediaServerFormValues>(() => mediaServerFormValues(server));
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);
  const [signInOwned, setSignInOwned] = useState<boolean | null>(null);
  /** Save waiting for the "different Plex server" confirmation. */
  const [pendingSave, setPendingSave] = useState<{ forceSave: boolean } | null>(null);

  const create = useCreateMediaServer();
  const update = useUpdateMediaServer();
  const remove = useDeleteMediaServer();
  const test = useTestMediaServer();
  const servers = useMediaServers();
  const withStorage = showStorageField(server, servers.data);

  const saving = create.isPending || update.isPending;
  const summary = summarizeSaveError(saveError, FIELDS);
  const errors = mergeFieldErrors(clientErrors, summary.fields);

  const set = <K extends keyof MediaServerFormValues>(key: K, value: MediaServerFormValues[K]) => {
    const connectionChanged = key === 'url' || key === 'token';
    // A machine identifier is only sent while it is known to belong to this URL + token; after a
    // manual edit the server learns it again from its connection test.
    setValues((v) => ({
      ...v,
      [key]: value,
      ...(connectionChanged ? { machineIdentifier: '' } : {}),
    }));
    setClientErrors((e) => {
      let next = e[key] ? { ...e, [key]: [] } : e;
      // The "re-enter the token" error depends on the URL too.
      if (key === 'url' && next.token?.length) next = { ...next, token: [] };
      return next;
    });
    setSaveError(null);
    if (connectionChanged || key === 'verifyTls') test.reset();
  };

  const applySelection = (sel: PlexServerSelection) => {
    setValues((v) => ({
      ...v,
      // Keep a custom name; replace the default one with the server's name.
      name: !v.name.trim() || v.name === 'Plex' ? sel.name : v.name,
      url: sel.url,
      token: sel.token,
      machineIdentifier: sel.machineIdentifier,
    }));
    setClientErrors({});
    setSaveError(null);
    setSignInOwned(sel.owned);
    test.reset();
  };

  const validate = (): boolean => {
    const errs = validateMediaServer(values, server);
    setClientErrors(errs);
    return !hasErrors(errs);
  };

  const runTest = () => {
    const errs = validateMediaServer(values, server);
    // Name isn't needed to test.
    delete errs.name;
    setClientErrors(errs);
    if (hasErrors(errs)) return;
    test.mutate(mediaServerPayload(values, server?.id));
  };

  const newMachineId = changedMachineId(
    server?.machineIdentifier,
    test.data?.machineIdentifier,
    values.machineIdentifier,
  );

  const save = (forceSave = false, confirmed = false) => {
    if (!validate()) return;
    // Re-pointing a saved server at a different Plex machine invalidates its libraries and
    // duplicate groups (rating keys / media ids belong to the old server): confirm first.
    if (newMachineId && !confirmed) {
      setPendingSave({ forceSave });
      return;
    }
    setSaveError(null);
    const onSuccess = (saved: MediaServer) => {
      toast.success(server ? 'Media server saved' : 'Media server added', saved?.name || values.name);
      onClose();
    };
    const onError = (e: unknown) => setSaveError(e);
    const payload = mediaServerPayload(values, server?.id, withStorage);
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
  const jwt = !isMaskedSecret(values.token) && looksLikeJwt(values.token);

  return (
    <>
      <ConnectionModal
        open
        onClose={onClose}
        title={server ? `Edit Media Server — ${server.name}` : 'Add Media Server — Plex'}
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
        <section className="mb-4 rounded border border-border bg-card-alt p-4">
          <h3 className="m-0 mb-2 text-base font-normal text-fg-strong">
            {server ? 'Refresh the token with Plex' : 'Sign in with Plex'}
          </h3>
          <PlexSignIn onSelect={applySelection} disabled={saving} />
        </section>

        <div className="mb-1 text-sm text-muted">{server ? 'Connection details' : 'Or enter the details manually'}</div>

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
          helpText="Address Dupearr uses to reach Plex, including the port (default 32400)."
        >
          <TextInput
            id={id('url')}
            value={values.url}
            onChange={(e) => set('url', e.target.value)}
            placeholder="http://192.168.1.10:32400"
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            invalid={!!errors.url?.length}
          />
        </FormGroup>

        <FormGroup
          label="Token"
          htmlFor={id('token')}
          errors={errors.token}
          warning={
            jwt
              ? 'This looks like a short-lived token (expires after 7 days). Use “Sign in with Plex” for a long-lived one.'
              : undefined
          }
          helpText={
            <>
              X-Plex-Token — filled in by “Sign in with Plex”. Plex describes tokens copied from Plex Web as only valid
              temporarily; the sign-in creates a durable one for Dupearr.{' '}
              <a
                href={PLEX_TOKEN_HELP_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-0.5 text-accent-soft underline"
              >
                Finding a token
                <ExternalLink aria-hidden width={11} height={11} />
              </a>
            </>
          }
        >
          <SecretInput
            id={id('token')}
            value={values.token}
            onChange={(t) => set('token', t)}
            invalid={!!errors.token?.length}
          />
        </FormGroup>

        <FormGroup
          label="Verify TLS"
          htmlFor={id('verifyTls')}
          advanced
          helpText="Verify the server's HTTPS certificate. Turn off only for self-signed certificates."
        >
          <Switch
            id={id('verifyTls')}
            checked={values.verifyTls}
            onChange={(v) => set('verifyTls', v)}
            aria-label="Verify TLS"
          />
        </FormGroup>

        <FormGroup label="Enabled" htmlFor={id('enabled')} helpText="Disabled servers are not scanned.">
          <Switch
            id={id('enabled')}
            checked={values.enabled}
            onChange={(v) => set('enabled', v)}
            aria-label="Enabled"
          />
        </FormGroup>

        {withStorage && (
          <FormGroup
            label="Storage"
            htmlFor={id('storage')}
            errors={errors.storage}
            helpText={
              <>
                With several media servers, Dupearr never removes a file another server lists unless it can prove that
                server keeps a different copy. The raw paths of a server with <strong>separate storage</strong> are never
                compared with other servers&apos; paths, its versions are never matched to Radarr/Sonarr by raw path or
                name, and its copies never count as keepers for another server; mapped folders are still compared in
                Dupearr&apos;s own view. This removes the protection of its files that Dupearr cannot see through a path
                mapping: never declare a server on the same storage as separate.
              </>
            }
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

        {newMachineId && !test.data && (
          <Alert kind="warning" title="Different Plex server" className="mt-2">
            The selected server (machine ID {newMachineId}) is not the one saved before ({server?.machineIdentifier}).
            Libraries and duplicates found on the old server will no longer match.
          </Alert>
        )}

        {signInOwned === false && test.data?.owned !== false && (
          <Alert kind="warning" title="You don't own this server" className="mt-2">
            {NOT_OWNER_WARNING}.
          </Alert>
        )}

        <TestResult
          className="mt-3"
          pending={test.isPending}
          error={test.error}
          successTitle="Connected to Plex"
          success={test.data ? <MediaServerTestDetails result={test.data} /> : null}
        />
        {test.data && !test.isPending && (
          <MediaServerTestWarnings className="mt-2" result={test.data} previousMachineId={server?.machineIdentifier} />
        )}

        <SaveErrorAlert
          className="mt-3"
          messages={summary.messages}
          onForceSave={summary.canForce ? () => save(true) : undefined}
          forcing={saving}
        />
      </ConnectionModal>
      <ConfirmDialog
        open={pendingSave !== null}
        title="Switch to a different Plex server?"
        kind="warning"
        confirmLabel="Save"
        message={
          <>
            <p className="mt-0">
              <strong>{server?.name}</strong> was saved for Plex server{' '}
              <code className="text-xs">{server?.machineIdentifier}</code>, but these settings reach{' '}
              <code className="text-xs">{newMachineId}</code>.
            </p>
            <p className="mb-0">
              Its libraries and duplicates belong to the old server and will no longer match. To connect another Plex
              server, add it as a new media server instead.
            </p>
          </>
        }
        onCancel={() => setPendingSave(null)}
        onConfirm={() => {
          const pending = pendingSave;
          setPendingSave(null);
          if (pending) save(pending.forceSave, true);
        }}
      />
    </>
  );
}

/** How to enable Plex's "Allow media deletion" (docs/research/plex-api.md §5: off ⇒ attribute absent). */
export function PlexDeletionDisabledHelp() {
  return (
    <>
      Plex will refuse deletions from Dupearr. To allow them, open Plex Web → Settings → (your server) →{' '}
      <strong>Library</strong>, click <strong>Show Advanced</strong>, enable <strong>Allow media deletion</strong> and
      save. Only the server owner&apos;s token can delete media. Until then Dupearr can still remove files through
      Radarr/Sonarr or the filesystem.
    </>
  );
}

/** Warning shown when the token is known not to be the server owner's (docs/DECISIONS.md D2). */
export const NOT_OWNER_WARNING = "Only the server owner's token can delete media via Plex";

/** "Yes" / "No" / "Unknown" for the test result's `owned` (absent or null = unknown). */
export function ownerTokenLabel(owned: boolean | null | undefined): 'Yes' | 'No' | 'Unknown' {
  if (owned === true) return 'Yes';
  if (owned === false) return 'No';
  return 'Unknown';
}

/** Details of a successful connection test. */
export function MediaServerTestDetails({ result }: { result: MediaServerTestResult }) {
  const owner = ownerTokenLabel(result.owned);
  return (
    <DetailList
      items={[
        { label: 'Server', value: result.friendlyName },
        { label: 'Version', value: result.version },
        {
          label: 'Machine ID',
          value: result.machineIdentifier ? <code className="text-xs">{result.machineIdentifier}</code> : '',
        },
        {
          label: 'Media deletion',
          value: result.mediaDeletionAllowed ? (
            <Badge kind="success" outline>
              Allowed
            </Badge>
          ) : (
            <Badge kind="warning" outline>
              Disabled
            </Badge>
          ),
        },
        {
          label: 'Owner token',
          value: (
            <Badge
              kind={owner === 'Yes' ? 'success' : owner === 'No' ? 'warning' : 'default'}
              outline
              title={owner === 'Unknown' ? 'Plex did not say whether this token belongs to the server owner' : undefined}
            >
              {owner}
            </Badge>
          ),
        },
      ]}
    />
  );
}

/** Safety warnings derived from a successful test (deletion disabled, different server). */
export function MediaServerTestWarnings({
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
  const notOwner = result.owned === false;
  if (result.mediaDeletionAllowed && !changedServer && !notOwner) return null;
  return (
    <div className={clsx('flex flex-col gap-2', className)}>
      {notOwner && (
        <Alert kind="warning" title="Not the server owner's token">
          {NOT_OWNER_WARNING}. Plex will refuse deletions from Dupearr; removals can still go through Radarr/Sonarr or
          the filesystem. Sign in with the Plex account that owns this server to allow them.
        </Alert>
      )}
      {!result.mediaDeletionAllowed && (
        <Alert kind="warning" title="“Allow media deletion” is off">
          <PlexDeletionDisabledHelp />
        </Alert>
      )}
      {changedServer && (
        <Alert kind="warning" title="Different Plex server">
          This URL/token reaches a different server (machine ID {result.machineIdentifier}) than the one saved before (
          {previousMachineId}). Libraries and duplicates found on the old server will no longer match.
        </Alert>
      )}
    </div>
  );
}
