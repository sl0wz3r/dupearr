import { useQueryClient } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { useMemo, useState, type ReactNode } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { usePathMappings, useSettings, useUpdateSettings } from '@/api/hooks';
import { queryKeys } from '@/api/queryKeys';
import type { DeletionMethod, Mode, Settings } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  SaveBar,
  SettingsSection,
  ToolbarButton,
  useSettingsForm,
} from '@/components/page';
import { PathMappingsSection } from '@/components/settings/rules/PathMappings';
import {
  MEDIA_MANAGEMENT_FIELDS,
  MIN_SCAN_INTERVAL_MINUTES,
  SETTINGS_NUMBER_LIMITS,
  formatHours,
  incompleteSettingsFields,
  isCompleteSettings,
  mapSettingsErrors,
  discSettingsWarnings,
  methodInfo,
  recycleBinProblems,
  validateSettings,
  type NumericSettingsField,
} from '@/components/settings/rules/mediaManagement';
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  FormGroup,
  LoadingIndicator,
  NumberInput,
  OrderedListEditor,
  Select,
  Switch,
  TextInput,
  useToast,
} from '@/components/ui';
import { DELETION_METHODS, DELETION_METHOD_LABELS, MODE_LABELS, labelOf, toOptions } from '@/lib/constants';
import { formatInterval } from '@/lib/format';

interface NumberSettingProps {
  field: NumericSettingsField;
  label: string;
  value: number;
  onChange: (v: number) => void;
  errors: string[];
  help?: ReactNode;
  warning?: ReactNode;
  unit?: string;
  advanced?: boolean;
}

/** A numeric setting; its range is the server's (SETTINGS_NUMBER_LIMITS). */
function NumberSetting({ field, label, value, onChange, errors, help, warning, unit, advanced }: NumberSettingProps) {
  const id = `mm-${String(field)}`;
  const { min, max, integer } = SETTINGS_NUMBER_LIMITS[field];
  return (
    <FormGroup label={label} htmlFor={id} helpText={help} warning={warning} errors={errors} advanced={advanced} width="sm">
      <NumberInput
        id={id}
        value={value}
        min={min}
        max={max}
        unit={unit}
        integer={integer}
        step={integer ? 1 : 0.5}
        invalid={errors.length > 0}
        onChange={(v) => {
          if (v !== null) onChange(v);
        }}
      />
    </FormGroup>
  );
}

interface SwitchSettingProps {
  field: keyof Settings;
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  errors: string[];
  help?: ReactNode;
  warning?: ReactNode;
  advanced?: boolean;
}

function SwitchSetting({ field, label, checked, onChange, errors, help, warning, advanced }: SwitchSettingProps) {
  const id = `mm-${String(field)}`;
  return (
    <FormGroup label={label} htmlFor={id} helpText={help} warning={warning} errors={errors} advanced={advanced}>
      <span className="inline-flex pt-1.5">
        <Switch id={id} checked={checked} onChange={onChange} aria-label={label} />
      </span>
    </FormGroup>
  );
}

/** Several warnings under one field, one per line (undefined when there are none). */
function warningLines(lines: readonly string[]): ReactNode {
  if (lines.length === 0) return undefined;
  return (
    <span className="flex flex-col gap-1">
      {lines.map((w) => (
        <span key={w}>{w}</span>
      ))}
    </span>
  );
}

/** Structural equality for settings values (primitives and string arrays). */
function sameSettingValue(a: unknown, b: unknown): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b);
}

/**
 * Settings → Media Management at `/settings/mediamanagement`: safety (dry run, approval mode,
 * limits), deletion methods and post-delete behaviour, detection options and path mappings.
 *
 * Only the fields the user actually edited are taken from the form; every other field always shows
 * (and is saved with) the latest server value. The server can change settings while the page is
 * open (SSE `settings` event → refetch); re-sending a stale copy would e.g. silently turn dry run
 * back off after someone switched it on elsewhere.
 */
export default function MediaManagementPage() {
  const query = useSettings();
  const update = useUpdateSettings();
  const mappingsQuery = usePathMappings();
  const queryClient = useQueryClient();
  const toast = useToast();

  // Tolerate a null list from the server (Go nil slice) so the editor always has an array.
  const serverValue = useMemo<Settings | undefined>(
    () => (query.data ? { ...query.data, deletionMethods: query.data.deletionMethods ?? [] } : undefined),
    [query.data],
  );
  const form = useSettingsForm(serverValue);
  /** Fields the user edited since the last load/save/discard. */
  const [touched, setTouched] = useState<ReadonlySet<keyof Settings>>(() => new Set());

  const edits = form.values;
  // An edit that equals the server value is no longer an edit: forget it, so a later server change
  // of that field is shown instead of resurrecting the old edit.
  if (serverValue && edits && touched.size > 0) {
    const settled = [...touched].filter((key) => sameSettingValue(edits[key], serverValue[key]));
    if (settled.length > 0) {
      const next = new Set(touched);
      for (const key of settled) next.delete(key);
      setTouched(next);
    }
  }
  const values = useMemo<Settings | undefined>(() => {
    if (!serverValue) return undefined;
    if (!edits || touched.size === 0) return serverValue;
    const out: Settings = { ...serverValue };
    for (const key of touched) (out as unknown as Record<string, unknown>)[key] = edits[key];
    return out;
  }, [serverValue, edits, touched]);
  const dirty =
    !!values && !!serverValue && [...touched].some((key) => !sameSettingValue(values[key], serverValue[key]));
  const incomplete = useMemo(() => (serverValue ? incompleteSettingsFields(serverValue) : []), [serverValue]);

  const [confirmDryRunOff, setConfirmDryRunOff] = useState(false);
  const [confirmAutoMode, setConfirmAutoMode] = useState(false);
  const [confirmDiscRemoval, setConfirmDiscRemoval] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);
  const [localErrors, setLocalErrors] = useState<Partial<Record<keyof Settings, string[]>>>({});

  const mappings = mappingsQuery.data ?? [];
  // Server validation errors (400 from PUT /config/settings) by field; the rest go to the save bar.
  const serverErrors = useMemo(() => mapSettingsErrors(saveError, MEDIA_MANAGEMENT_FIELDS), [saveError]);
  const fieldErrors = (field: keyof Settings): string[] => [
    ...(localErrors[field] ?? []),
    ...(serverErrors.fields[field] ?? []),
  ];

  const set = <K extends keyof Settings>(key: K, value: Settings[K]) => {
    form.setField(key, value);
    setTouched((prev) => (prev.has(key) ? prev : new Set(prev).add(key)));
    if (localErrors[key]) setLocalErrors((e) => ({ ...e, [key]: undefined }));
  };

  const save = () => {
    if (!values) return;
    setSaveError(null);
    // A PUT replaces the resource: never send an object with missing fields (they would be reset
    // to zero values on the server, e.g. dryRun=false).
    if (!isCompleteSettings(values)) return;
    const local = validateSettings(values, { mappings });
    setLocalErrors(local);
    if (Object.values(local).some((v) => v && v.length > 0)) return;
    const body = values;
    update.mutate(body, {
      onSuccess: (saved) => {
        const complete = isCompleteSettings(saved);
        form.markSaved(complete ? saved : body);
        setTouched(new Set());
        // An unexpected response body was written to the cache by the mutation: reload it.
        if (!complete) void queryClient.invalidateQueries({ queryKey: queryKeys.config.settings });
        toast.success('Settings saved');
      },
      onError: (e) => setSaveError(e),
    });
  };

  const reset = () => {
    form.reset();
    setTouched(new Set());
    setSaveError(null);
    setLocalErrors({});
  };

  const hasLocalErrors = Object.values(localErrors).some((v) => v && v.length > 0);
  let barError: ReactNode = null;
  if (incomplete.length > 0) {
    barError = 'Saving is disabled: the server returned incomplete settings. Refresh the page and try again.';
  } else if (hasLocalErrors) {
    barError = 'Some settings are invalid — fix the highlighted fields.';
  } else if (isApiError(saveError) && saveError.isValidation) {
    const unmatched = serverErrors.other;
    const highlighted = Object.keys(serverErrors.fields).length > 0;
    barError = (
      <>
        {highlighted ? 'Some settings are invalid — fix the highlighted fields.' : 'The server rejected these settings.'}
        {unmatched.length > 0 && (
          <ul className="m-0 mt-1 list-disc pl-5">
            {unmatched.map((m, i) => (
              <li key={i}>{m}</li>
            ))}
          </ul>
        )}
      </>
    );
  } else if (saveError) {
    barError = errorMessage(saveError, 'Could not save settings');
  }

  const methods = values?.deletionMethods ?? [];
  const recycle = values?.recycleBinPath ?? '';
  const recycleWarnings = [
    ...(!recycle.trim() && methods.includes('filesystem') ? ['No recycle bin: filesystem removals are permanent.'] : []),
    ...recycleBinProblems(recycle, mappings).warnings,
  ];
  const discWarnings = values ? discSettingsWarnings(values) : { allowDiscRemoval: [], keepPlayableCopy: [] };

  return (
    <PageContent title="Media Management">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={RefreshCw} label="Refresh" spinning={query.isFetching} onClick={() => void query.refetch()} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Media Management" subtitle="How and when Dupearr removes duplicate copies." />

        {query.error && !values ? (
          <Alert
            kind="error"
            title="Could not load settings"
            actions={
              <Button size="sm" onClick={() => void query.refetch()}>
                Retry
              </Button>
            }
          >
            {errorMessage(query.error)}
          </Alert>
        ) : !values ? (
          <LoadingIndicator message="Loading settings…" />
        ) : (
          <>
            {incomplete.length > 0 && (
              <Alert kind="error" title="Incomplete settings from the server" className="mb-6">
                These fields are missing or invalid: {incomplete.join(', ')}. Saving is disabled so they are not reset
                — check that the Dupearr server is up to date, then refresh.
              </Alert>
            )}
            <SettingsSection title="Safety" description="Nothing is ever deleted without passing these checks.">
              <FormGroup
                label="Dry Run"
                htmlFor="mm-dryRun"
                errors={fieldErrors('dryRun')}
                helpText={
                  values.dryRun
                    ? 'On: approved removals are only simulated and recorded as “dry run” — no file is touched.'
                    : undefined
                }
                warning={values.dryRun ? undefined : 'Dry run is off: approved removals really delete files.'}
              >
                <span className="inline-flex pt-1.5">
                  <Switch
                    id="mm-dryRun"
                    aria-label="Dry run"
                    checked={values.dryRun}
                    onChange={(on) => {
                      if (on) set('dryRun', true);
                      else setConfirmDryRunOff(true);
                    }}
                  />
                </span>
              </FormGroup>

              <FormGroup
                label="Approval Mode"
                htmlFor="mm-mode"
                errors={fieldErrors('mode')}
                helpText={
                  values.mode === 'auto'
                    ? 'Pending groups are approved automatically after each scan. Groups that need review are never auto-approved.'
                    : 'Every duplicate group waits for your approval on the Duplicates page.'
                }
                warning={
                  values.mode === 'auto' && !values.dryRun
                    ? 'Automatic mode with dry run off removes files without asking once a group is stable.'
                    : undefined
                }
              >
                <Select<Mode>
                  id="mm-mode"
                  options={toOptions(MODE_LABELS, ['manual', 'auto'])}
                  value={values.mode}
                  onChange={(v) => {
                    // Automatic mode with dry run off removes files without asking: confirm first.
                    if (v === 'auto' && values.mode !== 'auto' && !values.dryRun) setConfirmAutoMode(true);
                    else set('mode', v);
                  }}
                />
              </FormGroup>

              {values.mode === 'auto' && (
                <NumberSetting
                  field="stableScansRequired"
                  label="Stable Scans Required"
                  value={values.stableScansRequired}
                  onChange={(v) => set('stableScansRequired', v)}
                  errors={fieldErrors('stableScansRequired')}
                  unit="scans"
                  help={`A group must be seen identically in ${values.stableScansRequired} consecutive full scan${
                    values.stableScansRequired === 1 ? '' : 's'
                  } before it is approved automatically (webhook re-checks do not count).`}
                  warning={
                    values.stableScansRequired <= 1
                      ? 'With 1, a group can be approved as soon as it is first detected.'
                      : undefined
                  }
                />
              )}

              <NumberSetting
                field="scanIntervalMinutes"
                label="Scan Interval"
                value={values.scanIntervalMinutes}
                onChange={(v) => set('scanIntervalMinutes', v)}
                errors={fieldErrors('scanIntervalMinutes')}
                unit="minutes"
                help={
                  values.scanIntervalMinutes > 0
                    ? `Full scan every ${formatInterval(values.scanIntervalMinutes)} (at least ${MIN_SCAN_INTERVAL_MINUTES} minutes). 0 turns scheduled scans off.`
                    : 'Scheduled scans are off (0) — scan manually or via webhooks.'
                }
                warning={
                  values.scanIntervalMinutes === 0
                    ? 'Webhooks are missed while Dupearr or the *arr is down; periodic scans keep results current.'
                    : undefined
                }
              />
              <NumberSetting
                field="minAgeHours"
                label="Minimum Age"
                value={values.minAgeHours}
                onChange={(v) => set('minAgeHours', v)}
                errors={fieldErrors('minAgeHours')}
                unit="hours"
                help={
                  values.minAgeHours > 0
                    ? `Copies added less than ${formatHours(values.minAgeHours)} ago (or with an unknown date) are never removed; their group is deferred.`
                    : 'Off (0): copies can be removed as soon as they are detected.'
                }
                warning={values.minAgeHours === 0 ? 'Recently added files are not protected.' : undefined}
              />
              <NumberSetting
                field="maxDeletionsPerRun"
                label="Max Deletions per Run"
                value={values.maxDeletionsPerRun}
                onChange={(v) => set('maxDeletionsPerRun', v)}
                errors={fieldErrors('maxDeletionsPerRun')}
                unit="files"
                help="Circuit breaker: a queue run stops after removing this many files."
              />
              <NumberSetting
                field="maxBytesPerRunGb"
                label="Max Size per Run"
                value={values.maxBytesPerRunGb}
                onChange={(v) => set('maxBytesPerRunGb', v)}
                errors={fieldErrors('maxBytesPerRunGb')}
                unit="GB"
                help="Circuit breaker: a queue run stops after removing this much data."
              />
            </SettingsSection>

            <SettingsSection title="Deletion" description="How approved removals are carried out.">
              <FormGroup
                label="Deletion Methods"
                errors={fieldErrors('deletionMethods')}
                helpText="Tried from the top; the first method that can remove the file is used."
              >
                <OrderedListEditor
                  aria-label="Deletion methods"
                  value={methods}
                  options={toOptions(DELETION_METHOD_LABELS, [...DELETION_METHODS])}
                  minItems={1}
                  addPlaceholder="Add a deletion method…"
                  emptyText="No deletion methods — nothing can be removed"
                  onChange={(next) => set('deletionMethods', next as DeletionMethod[])}
                />
                <ul aria-label="Deletion method details" className="m-0 mt-3 flex list-none flex-col gap-2 p-0 text-[13px]">
                  {DELETION_METHODS.map((m) => {
                    const info = methodInfo(m, recycle);
                    const position = methods.indexOf(m);
                    return (
                      <li key={m} className={position < 0 ? 'opacity-60' : undefined}>
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-semibold text-fg-strong">{labelOf(DELETION_METHOD_LABELS, m)}</span>
                          <Badge kind={info.kind} outline>
                            {info.badge}
                          </Badge>
                          {position < 0 && <Badge outline>Not used</Badge>}
                        </div>
                        <div className="text-muted">{info.text}</div>
                      </li>
                    );
                  })}
                </ul>
              </FormGroup>

              <SwitchSetting
                field="arrRescanAfterDelete"
                label="Rescan *arr After Delete"
                checked={values.arrRescanAfterDelete}
                onChange={(v) => set('arrRescanAfterDelete', v)}
                errors={fieldErrors('arrRescanAfterDelete')}
                help="Rescan the movie/series in Radarr/Sonarr after a removal so it adopts the kept copy."
              />
              <SwitchSetting
                field="unmonitorWhenKeeperElsewhere"
                label="Unmonitor When Kept Elsewhere"
                checked={values.unmonitorWhenKeeperElsewhere}
                onChange={(v) => set('unmonitorWhenKeeperElsewhere', v)}
                errors={fieldErrors('unmonitorWhenKeeperElsewhere')}
                help="When the kept copy lives in another *arr instance or library, unmonitor the item in the instance that lost its file so it is not downloaded again."
              />
              <SwitchSetting
                field="addExclusionWhenKeeperElsewhere"
                label="Add Exclusion When Kept Elsewhere"
                checked={values.addExclusionWhenKeeperElsewhere}
                onChange={(v) => set('addExclusionWhenKeeperElsewhere', v)}
                errors={fieldErrors('addExclusionWhenKeeperElsewhere')}
                help="Also add an import list exclusion in that *arr so lists don't add the item back."
              />
              <FormGroup
                label="Recycle Bin"
                htmlFor="mm-recycleBinPath"
                errors={fieldErrors('recycleBinPath')}
                helpText={
                  <>
                    Where filesystem removals go. Use a folder on the same share as your media so moves are instant, e.g.{' '}
                    <code>/data/.dupearr-recycle</code>, outside your Plex and Jellyfin library folders. Leave empty to delete
                    permanently (Jellyfin copies then need Radarr/Sonarr&apos;s own recycle bin).
                  </>
                }
                warning={warningLines(recycleWarnings)}
              >
                <TextInput
                  id="mm-recycleBinPath"
                  className="font-mono"
                  value={values.recycleBinPath}
                  placeholder="/data/.dupearr-recycle"
                  invalid={fieldErrors('recycleBinPath').length > 0}
                  onChange={(e) => set('recycleBinPath', e.target.value)}
                />
              </FormGroup>
              <NumberSetting
                field="recycleBinCleanupDays"
                label="Recycle Bin Cleanup"
                value={values.recycleBinCleanupDays}
                onChange={(v) => set('recycleBinCleanupDays', v)}
                errors={fieldErrors('recycleBinCleanupDays')}
                unit="days"
                help={
                  values.recycleBinCleanupDays > 0
                    ? `Files moved to the recycle bin more than ${values.recycleBinCleanupDays} day${
                        values.recycleBinCleanupDays === 1 ? '' : 's'
                      } ago are permanently deleted by the Clean Recycle Bin task. 0 keeps them forever.`
                    : 'Keep forever (0): the Clean Recycle Bin task never deletes anything — empty the recycle bin yourself.'
                }
                warning={
                  values.recycleBinCleanupDays === 0 && recycle.trim()
                    ? 'The recycle bin grows until you empty it.'
                    : undefined
                }
              />
              <SwitchSetting
                field="refreshPlexAfterDelete"
                label="Refresh Plex After Delete"
                checked={values.refreshPlexAfterDelete}
                onChange={(v) => set('refreshPlexAfterDelete', v)}
                errors={fieldErrors('refreshPlexAfterDelete')}
                help="Refresh the item in Plex after a removal so the library reflects the change."
              />
              <SwitchSetting
                field="cleanupPlexStaleEntries"
                label="Clean Up Stale Plex Entries"
                checked={values.cleanupPlexStaleEntries}
                onChange={(v) => set('cleanupPlexStaleEntries', v)}
                errors={fieldErrors('cleanupPlexStaleEntries')}
                help="After an *arr or filesystem removal, remove the leftover “unavailable” version from Plex (only once its file is gone)."
              />
            </SettingsSection>

            <SettingsSection title="Detection" description="What counts as a duplicate.">
              <SwitchSetting
                field="treatEditionsAsDistinct"
                label="Editions Are Distinct"
                checked={values.treatEditionsAsDistinct}
                onChange={(v) => set('treatEditionsAsDistinct', v)}
                errors={fieldErrors('treatEditionsAsDistinct')}
                help="Director's Cut, Extended, IMAX… are separate versions, not duplicates of the theatrical cut."
              />
              <SwitchSetting
                field="treat3DAsDistinct"
                label="3D Is Distinct"
                checked={values.treat3DAsDistinct}
                onChange={(v) => set('treat3DAsDistinct', v)}
                errors={fieldErrors('treat3DAsDistinct')}
                help="3D copies (SBS, OU, BD3D) are not duplicates of 2D copies."
              />
              <SwitchSetting
                field="languageVariantsAsDistinct"
                label="Language Variants Are Distinct"
                checked={values.languageVariantsAsDistinct}
                onChange={(v) => set('languageVariantsAsDistinct', v)}
                errors={fieldErrors('languageVariantsAsDistinct')}
                help="Copies with completely different audio languages (e.g. an English and a dubbed German release) are not duplicates."
              />
              <SwitchSetting
                field="differentArrInstancesIntentional"
                label="Separate *arr Instances Are Intentional"
                checked={values.differentArrInstancesIntentional}
                onChange={(v) => set('differentArrInstancesIntentional', v)}
                errors={fieldErrors('differentArrInstancesIntentional')}
                help="Copies managed by different Radarr/Sonarr instances (e.g. a 4K + 1080p setup) are all kept and the group is marked protected; an extra copy no *arr tracks can still be removed."
              />
              <NumberSetting
                field="durationTolerancePercent"
                label="Duration Tolerance"
                value={values.durationTolerancePercent}
                onChange={(v) => set('durationTolerancePercent', v)}
                errors={fieldErrors('durationTolerancePercent')}
                unit="%"
                advanced
                help="Copies whose runtimes differ by more than the larger of this and the minutes below are flagged for review — they may be different content."
              />
              <NumberSetting
                field="durationToleranceMinutes"
                label="Duration Tolerance (Minutes)"
                value={values.durationToleranceMinutes}
                onChange={(v) => set('durationToleranceMinutes', v)}
                errors={fieldErrors('durationToleranceMinutes')}
                unit="minutes"
                advanced
              />
              <NumberSetting
                field="maxGroupSize"
                label="Max Group Size"
                value={values.maxGroupSize}
                onChange={(v) => set('maxGroupSize', v)}
                errors={fieldErrors('maxGroupSize')}
                unit="copies"
                advanced
                help="Groups with more copies than this are flagged for review (often a bad Plex match)."
              />
            </SettingsSection>

            <SettingsSection
              title="Full-Disc Backups"
              description="Blu-ray (BDMV), DVD (VIDEO_TS), HD DVD and AVCHD folders and ISO images, and Blu-ray clips (00800.m2ts …) or DVD VOB files stored loose in a movie folder: hundreds of files that together are one copy of a movie. A single clip is never removed on its own."
            >
              <SwitchSetting
                field="detectDiscs"
                label="Detect Full-Disc Backups"
                checked={values.detectDiscs}
                onChange={(v) => set('detectDiscs', v)}
                errors={fieldErrors('detectDiscs')}
                help={
                  values.detectDiscs
                    ? 'Looks next to each movie for disc folders and ISO images and shows each disc as one copy, ranked by its main feature. Plex’s default scanners skip discs and Radarr/Sonarr can’t track them, so this is how Dupearr sees them. Needs path mappings; the folders are only read.'
                    : 'Off: discs next to your movies are not shown. Files inside a disc folder are still never removed one by one.'
                }
              />
              <FormGroup
                label="Allow Removing Full Discs"
                htmlFor="mm-allowDiscRemoval"
                errors={fieldErrors('allowDiscRemoval')}
                helpText={
                  values.allowDiscRemoval
                    ? 'On: a disc that loses to a better copy can be removed — only when you approve its group yourself (never in automatic mode), only as a whole (BDMV/, CERTIFICATE/ … or the .iso), and only into Dupearr’s recycle bin. Other files in the folder (an MKV, artwork, .nfo, subtitles) stay.'
                    : 'Off (recommended): every disc is kept and shown as protected. Turning this on allows manual, whole-disc removals into the recycle bin — never automatic, never through Plex or Radarr/Sonarr. Requires a recycle bin.'
                }
                warning={warningLines(discWarnings.allowDiscRemoval)}
              >
                <span className="inline-flex pt-1.5">
                  <Switch
                    id="mm-allowDiscRemoval"
                    aria-label="Allow removing full discs"
                    checked={values.allowDiscRemoval}
                    onChange={(on) => {
                      if (on) setConfirmDiscRemoval(true);
                      else set('allowDiscRemoval', false);
                    }}
                  />
                </span>
              </FormGroup>
              <SwitchSetting
                field="keepPlayableCopy"
                label="Always Keep a Plex-Playable Copy"
                checked={values.keepPlayableCopy}
                onChange={(v) => set('keepPlayableCopy', v)}
                errors={fieldErrors('keepPlayableCopy')}
                help="Plex can’t play a disc folder. When every copy Dupearr would keep is a disc, the best regular video file is kept as well, so Plex still has something to play and Radarr/Sonarr keep tracking a file."
                warning={warningLines(discWarnings.keepPlayableCopy)}
              />
            </SettingsSection>

            <SettingsSection title="Retention" advanced revealed={fieldErrors('historyRetentionDays').length > 0}>
              <NumberSetting
                field="historyRetentionDays"
                label="History Retention"
                value={values.historyRetentionDays}
                onChange={(v) => set('historyRetentionDays', v)}
                errors={fieldErrors('historyRetentionDays')}
                unit="days"
                advanced
                help={
                  values.historyRetentionDays > 0
                    ? 'History events older than this are deleted by the Housekeeping task. 0 keeps history forever.'
                    : 'Keep forever (0): the Housekeeping task never deletes history events.'
                }
              />
            </SettingsSection>
          </>
        )}

        <PathMappingsSection />

        <SaveBar dirty={dirty} saving={update.isPending} onSave={save} onReset={reset} error={barError} />
      </PageBody>

      <ConfirmDialog
        open={confirmDryRunOff}
        title="Turn Off Dry Run?"
        kind="danger"
        confirmLabel="Turn Off Dry Run"
        cancelLabel="Keep Dry Run On"
        onCancel={() => setConfirmDryRunOff(false)}
        onConfirm={() => {
          setConfirmDryRunOff(false);
          set('dryRun', false);
        }}
        message={
          <div className="flex flex-col gap-2">
            <p className="m-0">
              With dry run off, <strong>approved removals really delete files</strong> — through Radarr/Sonarr, Plex or
              directly on disk, using the deletion methods on this page. Plex deletions, *arr deletions without an *arr
              recycle bin and filesystem deletions without a recycle bin cannot be undone.
            </p>
            {values?.mode === 'auto' && (
              <p className="m-0 text-warning">
                Automatic mode is on: stable pending groups will be removed without asking.
              </p>
            )}
            <p className="m-0">
              Review the pending duplicates and your profiles first. The change takes effect when you save.
            </p>
          </div>
        }
      />
      <ConfirmDialog
        open={confirmDiscRemoval}
        title="Allow Removing Full Discs?"
        kind="danger"
        confirmLabel="Allow Disc Removal"
        cancelLabel="Keep Discs Protected"
        onCancel={() => setConfirmDiscRemoval(false)}
        onConfirm={() => {
          setConfirmDiscRemoval(false);
          set('allowDiscRemoval', true);
        }}
        message={
          <div className="flex flex-col gap-2">
            <p className="m-0">
              A full disc — a Blu-ray <code>BDMV</code> folder, a DVD <code>VIDEO_TS</code> folder or an ISO image —
              can then be removed when a better copy of the same title exists:
            </p>
            <ul className="m-0 flex list-disc flex-col gap-1 pl-5">
              <li>
                <strong>Only when you approve its group yourself</strong> — automatic mode and scheduled runs never
                remove a disc.
              </li>
              <li>
                Only as a whole: Dupearr moves the disc’s own folders (<code>BDMV/</code>, <code>CERTIFICATE/</code> …)
                or the <code>.iso</code> into its recycle bin. An MKV next to it, artwork, .nfo and subtitles stay.
              </li>
              <li>Never through Plex or Radarr/Sonarr: they delete single files, which would break the disc.</li>
              <li>Only with a recycle bin: a removed disc can be restored until the recycle bin is cleaned up.</li>
            </ul>
            {!(values?.recycleBinPath ?? '').trim() && (
              <p className="m-0 text-warning">No recycle bin is set yet: disc removals are refused until you set one.</p>
            )}
            <p className="m-0">The change takes effect when you save.</p>
          </div>
        }
      />
      <ConfirmDialog
        open={confirmAutoMode}
        title="Turn On Automatic Mode?"
        kind="danger"
        confirmLabel="Use Automatic Mode"
        cancelLabel="Stay in Manual Mode"
        onCancel={() => setConfirmAutoMode(false)}
        onConfirm={() => {
          setConfirmAutoMode(false);
          set('mode', 'auto');
        }}
        message={
          <div className="flex flex-col gap-2">
            <p className="m-0">
              Dry run is off, so in automatic mode <strong>files are really deleted without asking</strong> once a
              duplicate group has been seen unchanged in {values?.stableScansRequired ?? 2} consecutive scans.
            </p>
            <p className="m-0">
              Groups flagged for review, protected copies and files younger than the minimum age are never removed
              automatically. The change takes effect when you save.
            </p>
          </div>
        }
      />
    </PageContent>
  );
}
