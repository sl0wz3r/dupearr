import { ArrowLeft, Bell } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage } from '@/api/client';
import {
  useCreateNotification,
  useDeleteNotification,
  useNotificationSchema,
  useNotificationTriggers,
  useTestNotification,
  useUpdateNotification,
} from '@/api/hooks/useNotifications';
import type {
  NotificationConfig,
  NotificationConfigInput,
  NotificationKind,
  NotificationTrigger,
  NotificationTriggerOption,
  ProviderSchema,
} from '@/api/types';
import { Alert, Button, Checkbox, FormGroup, LoadingIndicator, Switch, TextInput, useToast } from '@/components/ui';
import { labelOf, NOTIFICATION_KIND_LABELS, NOTIFICATION_TRIGGER_LABELS, toOptions } from '@/lib/constants';
import { ProviderCard } from './ConnectionCard';
import { ConnectionModal, SaveErrorAlert, TestResult } from './ConnectionModal';
import { apiFieldErrors, hasErrors, mergeFieldErrors, summarizeSaveError, type FieldErrors } from './connectionUtils';
import {
  canHoldStoredSecret,
  defaultTriggers,
  initialSettings,
  isAddressField,
  maskedSecretsAtNewAddress,
  splitSettingsErrors,
  storedSecretReentryMessage,
  toSubmitSettings,
  validateSettings,
  withReentryHints,
  type SettingsValues,
} from './notificationForm';
import { SchemaFields } from './SchemaFields';

const TOP_FIELDS = ['name', 'enabled', 'triggers', 'kind'] as const;

/** Trigger checkboxes: server list, falling back to the built-in labels; keeps unknown stored ones. */
export function triggerOptions(
  fromServer: readonly NotificationTriggerOption[] | null | undefined,
  selected: readonly string[],
): { value: string; label: string }[] {
  const base: { value: string; label: string }[] =
    fromServer && fromServer.length > 0 ? [...fromServer] : toOptions(NOTIFICATION_TRIGGER_LABELS);
  for (const t of selected) {
    if (!base.some((o) => o.value === t)) base.push({ value: t, label: labelOf(NOTIFICATION_TRIGGER_LABELS, t) });
  }
  return base;
}

export interface NotificationModalProps {
  /** Connection to edit; null = add (starts with the provider picker). */
  config: NotificationConfig | null;
  onClose: () => void;
}

/** Add/edit a notification connection (Settings → Connect). */
export function NotificationModal({ config, onClose }: NotificationModalProps) {
  const schemas = useNotificationSchema();
  const [kind, setKind] = useState<NotificationKind | null>(config?.kind ?? null);

  if (!kind) {
    return (
      <ConnectionModal open onClose={onClose} title="Add Connection" size="lg">
        {schemas.isPending ? (
          <LoadingIndicator message="Loading providers…" />
        ) : schemas.isError ? (
          <Alert
            kind="error"
            title="Unable to load notification providers"
            actions={
              <Button size="sm" onClick={() => void schemas.refetch()}>
                Retry
              </Button>
            }
          >
            {errorMessage(schemas.error)}
          </Alert>
        ) : (schemas.data ?? []).length === 0 ? (
          <Alert kind="info">No notification providers are available.</Alert>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {(schemas.data ?? []).map((p) => (
              <ProviderCard
                key={p.kind}
                name={p.name || labelOf(NOTIFICATION_KIND_LABELS, p.kind)}
                icon={Bell}
                infoUrl={p.infoUrl}
                onSelect={() => setKind(p.kind)}
              />
            ))}
          </div>
        )}
      </ConnectionModal>
    );
  }

  if (schemas.isPending) {
    return (
      <ConnectionModal open onClose={onClose} title="Connection">
        <LoadingIndicator message="Loading provider settings…" />
      </ConnectionModal>
    );
  }

  const schema = (schemas.data ?? []).find((s) => s.kind === kind) ?? null;
  return (
    <NotificationForm
      config={config}
      kind={kind}
      schema={schema}
      schemaError={schemas.isError ? schemas.error : null}
      onClose={onClose}
      onBack={config ? undefined : () => setKind(null)}
    />
  );
}

interface NotificationFormProps {
  config: NotificationConfig | null;
  kind: NotificationKind;
  schema: ProviderSchema | null;
  schemaError: unknown;
  onClose: () => void;
  onBack?: () => void;
}

function NotificationForm({ config, kind, schema, schemaError, onClose, onBack }: NotificationFormProps) {
  const idPrefix = useId();
  const toast = useToast();
  const triggers = useNotificationTriggers();
  const providerName = schema?.name || labelOf(NOTIFICATION_KIND_LABELS, kind);

  const [name, setName] = useState(config?.name ?? providerName);
  const [enabled, setEnabled] = useState(config?.enabled ?? true);
  const [settings, setSettings] = useState<SettingsValues>(() => initialSettings(schema, config?.settings));
  /** null = not chosen yet (new connection) → defaults from the trigger list once it loads. */
  const [selectedTriggers, setSelectedTriggers] = useState<string[] | null>(config ? [...(config.triggers ?? [])] : null);
  const [clientErrors, setClientErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<unknown>(null);

  const create = useCreateNotification();
  const update = useUpdateNotification();
  const remove = useDeleteNotification();
  const test = useTestNotification();

  const chosenTriggers = selectedTriggers ?? (triggers.data ? defaultTriggers(triggers.data) : []);
  const options = triggerOptions(triggers.data, chosenTriggers);
  const saving = create.isPending || update.isPending;

  // Server errors: settings fields (settings.x / x) vs top-level fields vs general messages.
  const rawServerErrors = apiFieldErrors(saveError);
  const [serverSettingsErrors, otherServerErrors] = splitSettingsErrors(rawServerErrors, schema);
  const settingsKeys = Object.keys(rawServerErrors).filter((k) => !(k in otherServerErrors));
  const summary = summarizeSaveError(saveError, [...TOP_FIELDS, ...settingsKeys]);
  const errors = mergeFieldErrors(clientErrors, summary.fields);
  // A failed Test reports its validation errors (e.g. a stored secret that can't be reused at a
  // new address) on the fields too, not only in the test result.
  const [testSettingsErrors] = splitSettingsErrors(apiFieldErrors(test.error), schema);
  const settingsErrors = mergeFieldErrors(
    Object.fromEntries(Object.entries(clientErrors).filter(([k]) => k.startsWith('settings.')).map(([k, v]) => [k.slice(9), v])),
    withReentryHints(schema, settings, mergeFieldErrors(serverSettingsErrors, testSettingsErrors)),
  );

  const clearErrors = (key: string) => {
    setClientErrors((e) => (e[key] ? { ...e, [key]: [] } : e));
    setSaveError(null);
    test.reset();
  };

  const payload = (): NotificationConfigInput => ({
    ...(config ? { id: config.id } : {}),
    name: name.trim(),
    kind,
    settings: schema ? toSubmitSettings(schema, settings) : settings,
    triggers: chosenTriggers as NotificationTrigger[],
    enabled,
  });

  const validate = (requireName: boolean): boolean => {
    const errs: FieldErrors = {};
    if (requireName && !name.trim()) errs.name = ['Name is required'];
    for (const [k, v] of Object.entries(validateSettings(schema, settings))) errs[`settings.${k}`] = v;
    if (config) {
      for (const k of maskedSecretsAtNewAddress(schema, config.settings, settings)) {
        const field = schema?.fields.find((f) => f.name === k);
        if (field) errs[`settings.${k}`] = [storedSecretReentryMessage(field)];
      }
    }
    setClientErrors(errs);
    return !hasErrors(errs);
  };

  const runTest = () => {
    if (!validate(false)) return;
    test.mutate(payload());
  };

  const save = () => {
    if (!validate(true)) return;
    setSaveError(null);
    const body = payload();
    const onSuccess = (saved: NotificationConfig) => {
      toast.success(config ? 'Connection saved' : 'Connection added', saved?.name || body.name);
      onClose();
    };
    const onError = (e: unknown) => setSaveError(e);
    if (config) update.mutate({ ...body, id: config.id }, { onSuccess, onError });
    else create.mutate(body, { onSuccess, onError });
  };

  const del = () => {
    if (!config) return;
    remove.mutate(config.id, {
      onSuccess: () => {
        toast.success('Connection deleted', config.name);
        onClose();
      },
      onError: (e) => toast.error('Unable to delete connection', errorMessage(e)),
    });
  };

  const id = (n: string) => `${idPrefix}-${n}`;
  const toggleTrigger = (value: string, on: boolean) => {
    const next = on ? [...chosenTriggers.filter((t) => t !== value), value] : chosenTriggers.filter((t) => t !== value);
    // Keep the server's order for a stable payload.
    const order = options.map((o) => o.value);
    next.sort((a, b) => order.indexOf(a) - order.indexOf(b));
    setSelectedTriggers(next);
    clearErrors('triggers');
  };

  return (
    <ConnectionModal
      open
      onClose={onClose}
      title={config ? `Edit Connection — ${config.name}` : `Add Connection — ${providerName}`}
      onSave={save}
      saving={saving}
      onTest={schema ? runTest : undefined}
      testing={test.isPending}
      onDelete={config ? del : undefined}
      deleting={remove.isPending}
      deleteTitle="Delete Connection"
      deleteMessage={
        <>
          Delete the connection <strong>{config?.name}</strong>?
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
      {!schema && (
        <Alert kind="warning" title={`Unknown provider “${kind}”`} className="mb-3">
          {schemaError
            ? `The provider list could not be loaded (${errorMessage(schemaError)}).`
            : 'This Dupearr version has no settings form for this provider.'}{' '}
          Its stored settings are kept unchanged; you can rename, enable/disable or delete it.
        </Alert>
      )}

      <FormGroup label="Name" htmlFor={id('name')} errors={errors.name}>
        <TextInput
          id={id('name')}
          value={name}
          maxLength={100}
          invalid={!!errors.name?.length}
          onChange={(e) => {
            setName(e.target.value);
            clearErrors('name');
          }}
        />
      </FormGroup>

      <FormGroup label="Enabled" htmlFor={id('enabled')}>
        <Switch
          id={id('enabled')}
          checked={enabled}
          aria-label="Enabled"
          onChange={(v) => {
            setEnabled(v);
            clearErrors('enabled');
          }}
        />
      </FormGroup>

      <FormGroup
        label="Notification Triggers"
        errors={errors.triggers}
        warning={chosenTriggers.length === 0 ? 'No triggers selected — this connection will never notify.' : undefined}
      >
        {triggers.isPending ? (
          <LoadingIndicator className="py-2" />
        ) : (
          <div className="flex flex-col gap-2 pt-1.5" role="group" aria-label="Notification triggers">
            {options.map((o) => (
              <Checkbox
                key={o.value}
                label={o.label}
                checked={chosenTriggers.includes(o.value)}
                onChange={(c) => toggleTrigger(o.value, c)}
              />
            ))}
          </div>
        )}
      </FormGroup>

      {schema && (
        <SchemaFields
          fields={schema.fields}
          values={settings}
          errors={settingsErrors}
          disabled={saving}
          onChange={(field, value) => {
            setSettings((s) => ({ ...s, [field]: value }));
            clearErrors(`settings.${field}`);
            // "Enter the secret again" errors depend on the address fields too.
            const changed = schema.fields.find((f) => f.name === field);
            if (changed && isAddressField(changed)) {
              const secrets = schema.fields.filter(canHoldStoredSecret).map((f) => `settings.${f.name}`);
              setClientErrors((e) => Object.fromEntries(Object.entries(e).filter(([k]) => !secrets.includes(k))));
            }
          }}
        />
      )}

      <TestResult
        className="mt-3"
        pending={test.isPending}
        error={test.error}
        successTitle="Test notification sent"
        success={test.isSuccess ? `Check ${providerName} for the test message.` : null}
      />

      <SaveErrorAlert className="mt-3" messages={summary.messages} />
    </ConnectionModal>
  );
}
