import { clsx } from 'clsx';
import { RotateCcw, Save, Settings2 } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useBlocker } from 'react-router';
import { useShowAdvanced } from '@/app/preferences';
import { Alert } from '@/components/ui/Alert';
import { Button } from '@/components/ui/Button';
import { ConfirmDialog } from '@/components/ui/ConfirmDialog';
import { ToolbarButton } from './Page';

// ---------------------------------------------------------------------------
// SettingsSection
// ---------------------------------------------------------------------------

export interface SettingsSectionProps {
  title: ReactNode;
  description?: ReactNode;
  /** Hidden unless "Show Advanced" is on (or `revealed`). */
  advanced?: boolean;
  /** Show an advanced section anyway, e.g. while one of its fields has a validation error. */
  revealed?: boolean;
  /** Right side of the legend (e.g. an "Add" button). */
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}

/** *arr "FieldSet": titled group of FormGroups / cards on a settings page. */
export function SettingsSection({
  title,
  description,
  advanced,
  revealed = false,
  actions,
  children,
  className,
}: SettingsSectionProps) {
  const [showAdvanced] = useShowAdvanced();
  if (advanced && !showAdvanced && !revealed) return null;
  return (
    <section className={clsx('mb-8', className)}>
      <div className="mb-3 flex items-end justify-between gap-3 border-b border-border pb-1.5">
        <div>
          <h2 className={clsx('m-0 text-xl font-light', advanced ? 'text-warning' : 'text-fg-strong')}>{title}</h2>
          {description && <div className="mt-0.5 text-sm text-muted">{description}</div>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Show Advanced toggle
// ---------------------------------------------------------------------------

/** Toolbar button toggling the persisted "Show Advanced" preference. */
export function AdvancedSettingsToggle() {
  const [showAdvanced, setShowAdvanced] = useShowAdvanced();
  return (
    <ToolbarButton
      icon={Settings2}
      label={showAdvanced ? 'Hide Advanced' : 'Show Advanced'}
      active={showAdvanced}
      onClick={() => setShowAdvanced(!showAdvanced)}
    />
  );
}

// ---------------------------------------------------------------------------
// Form state
// ---------------------------------------------------------------------------

function stableStringify(v: unknown): string {
  return JSON.stringify(v, (_k, val: unknown) =>
    val && typeof val === 'object' && !Array.isArray(val)
      ? Object.fromEntries(Object.entries(val as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b)))
      : val,
  );
}

export interface SettingsForm<T> {
  /** Current (edited) values; undefined until the server value loads. */
  values: T | undefined;
  /** Set one field. */
  setField: <K extends keyof T>(key: K, value: T[K]) => void;
  /** Shallow-merge a patch. */
  patch: (patch: Partial<T>) => void;
  /** Replace all values. */
  setValues: (values: T) => void;
  /** True when values differ from the last server value. */
  dirty: boolean;
  /** Revert to the server value. */
  reset: () => void;
  /** After a successful save: adopt `saved` (e.g. the PUT response) as both baseline and values. */
  markSaved: (saved: T) => void;
}

/**
 * Local edit state for a settings form backed by a query result. Re-syncs when the server value
 * changes and the form isn't dirty (e.g. after save or an SSE `settings` event).
 * @example
 * const { data } = useSettings();
 * const form = useSettingsForm(data);
 * <Switch checked={form.values?.dryRun ?? true} onChange={(v) => form.setField('dryRun', v)} />
 * <SaveBar dirty={form.dirty} saving={update.isPending} onReset={form.reset}
 *   onSave={() => update.mutate(form.values!, { onSuccess: form.markSaved })} />
 */
export function useSettingsForm<T extends object>(serverValue: T | undefined): SettingsForm<T> {
  const [values, setValuesState] = useState<T | undefined>(serverValue);
  const [baseline, setBaseline] = useState<T | undefined>(serverValue);
  const serverKey = serverValue === undefined ? undefined : stableStringify(serverValue);
  const [lastServerKey, setLastServerKey] = useState<string | undefined>(serverKey);

  // Adopt new server values (derived-state pattern, compared by content so unstable object
  // identities can't loop); keep local edits when the form is dirty.
  if (serverKey !== lastServerKey) {
    setLastServerKey(serverKey);
    if (serverValue !== undefined) {
      const wasDirty =
        values !== undefined && baseline !== undefined && stableStringify(values) !== stableStringify(baseline);
      setBaseline(serverValue);
      if (!wasDirty) setValuesState(serverValue);
    }
  }

  const dirty = useMemo(
    () => values !== undefined && baseline !== undefined && stableStringify(values) !== stableStringify(baseline),
    [values, baseline],
  );

  const setField = useCallback(<K extends keyof T>(key: K, value: T[K]) => {
    setValuesState((prev) => (prev ? { ...prev, [key]: value } : prev));
  }, []);
  const patch = useCallback((p: Partial<T>) => {
    setValuesState((prev) => (prev ? { ...prev, ...p } : prev));
  }, []);
  const setValues = useCallback((v: T) => setValuesState(v), []);
  const reset = useCallback(() => setValuesState(baseline), [baseline]);
  const markSaved = useCallback((saved: T) => {
    setBaseline(saved);
    setValuesState(saved);
  }, []);

  return { values, setField, patch, setValues, dirty, reset, markSaved };
}

// ---------------------------------------------------------------------------
// Unsaved changes guard + save bar
// ---------------------------------------------------------------------------

/**
 * Blocks in-app navigation (confirm dialog) and tab close (beforeunload) while `dirty`.
 * Returns the dialog element to render.
 */
export function useUnsavedChangesGuard(dirty: boolean): ReactNode {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname,
  );

  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [dirty]);

  return (
    <ConfirmDialog
      open={blocker.state === 'blocked'}
      title="Unsaved Changes"
      message="You have unsaved changes. Leave this page and discard them?"
      confirmLabel="Discard Changes"
      cancelLabel="Stay"
      kind="danger"
      onConfirm={() => blocker.proceed?.()}
      onCancel={() => blocker.reset?.()}
    />
  );
}

export interface SaveBarProps {
  dirty: boolean;
  saving?: boolean;
  onSave: () => void;
  /** Discard button (hidden when omitted). */
  onReset?: () => void;
  /** Error to show inside the bar (e.g. validation summary). */
  error?: ReactNode;
  /** Guard navigation while dirty (default true). */
  guard?: boolean;
}

/**
 * Sticky bottom bar that slides in while a settings form is dirty ("Save Changes" / "Discard"),
 * and guards navigation away from unsaved changes. Place it last inside PageBody.
 */
export function SaveBar({ dirty, saving = false, onSave, onReset, error, guard = true }: SaveBarProps) {
  const dialog = useUnsavedChangesGuard(guard && dirty);
  const visible = dirty || saving || !!error;
  return (
    <>
      {guard && dialog}
      <div
        aria-hidden={!visible}
        className={clsx(
          'sticky bottom-0 z-20 -mx-4 mt-6 transition-[opacity,transform] duration-150 sm:-mx-5',
          visible ? 'translate-y-0 opacity-100' : 'pointer-events-none translate-y-2 opacity-0',
        )}
      >
        {error && (
          <div className="px-4 pb-2 sm:px-5">
            <Alert kind="error">{error}</Alert>
          </div>
        )}
        <div className="flex items-center justify-end gap-3 border-t border-border bg-toolbar/95 px-4 py-3 backdrop-blur sm:px-5">
          <span className="mr-auto text-sm text-muted">{dirty ? 'You have unsaved changes' : 'Saving…'}</span>
          {onReset && (
            <Button icon={RotateCcw} onClick={onReset} disabled={!dirty || saving} tabIndex={visible ? 0 : -1}>
              Discard
            </Button>
          )}
          <Button
            variant="primary"
            icon={Save}
            onClick={onSave}
            loading={saving}
            disabled={!dirty}
            tabIndex={visible ? 0 : -1}
          >
            Save Changes
          </Button>
        </div>
      </div>
    </>
  );
}

/** Toolbar variant of the save action (like *arr's "Save Changes" toolbar button). */
export function SaveToolbarButton({ dirty, saving, onSave }: { dirty: boolean; saving?: boolean; onSave: () => void }) {
  return <ToolbarButton icon={Save} label="Save Changes" onClick={onSave} loading={saving} disabled={!dirty} />;
}
