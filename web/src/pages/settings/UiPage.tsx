import { RotateCcw } from 'lucide-react';
import { useId, useState } from 'react';
import { usePreferences, type Theme, type UiPreferences } from '@/app/preferences';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  SettingsSection,
  ToolbarButton,
} from '@/components/page';
import { ConfirmDialog, FormGroup, Select, Switch, useToast } from '@/components/ui';
import { DEFAULT_PAGE_SIZE, PAGE_SIZE_OPTIONS } from '@/lib/constants';
import { formatDateTokens, formatRelativeTime, type LongDateFormat, type ShortDateFormat, type TimeFormat } from '@/lib/format';

/**
 * `defaultPageSize` is not (yet) part of UiPreferences; it is stored in the same `dupearr.ui`
 * object (the provider keeps unknown keys) so list pages can adopt it once the type gains it.
 */
type UiPreferencesWithPageSize = UiPreferences & { defaultPageSize?: number };

/** Fixed sample (like *arr) so examples don't change while you look at them: Tue Mar 25 2014 17:30. */
const SAMPLE = new Date(2014, 2, 25, 17, 30, 0);

const SHORT_DATE_FORMATS: readonly ShortDateFormat[] = [
  'MMM D YYYY',
  'DD MMM YYYY',
  'MM/D/YYYY',
  'MM/DD/YYYY',
  'DD/MM/YYYY',
  'YYYY-MM-DD',
];
const LONG_DATE_FORMATS: readonly LongDateFormat[] = ['dddd, MMMM D YYYY', 'dddd, D MMMM YYYY'];
const TIME_FORMATS: readonly { value: TimeFormat; label: string }[] = [
  { value: 'h(:mm)a', label: `12 hour (${formatDateTokens(SAMPLE, 'h(:mm)a')})` },
  { value: 'HH:mm', label: `24 hour (${formatDateTokens(SAMPLE, 'HH:mm')})` },
];
const THEMES: readonly { value: Theme; label: string }[] = [
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
];

/** Selectable page sizes (the stored value is added when it isn't one of the presets). */
function pageSizeOptions(current: number): { value: string; label: string }[] {
  const sizes = [...new Set([...PAGE_SIZE_OPTIONS, current])].filter((n) => Number.isInteger(n) && n > 0).sort((a, b) => a - b);
  return sizes.map((n) => ({ value: String(n), label: `${n} per page` }));
}

/**
 * Settings → UI at `/settings/ui`: theme, date/time formats, relative dates, default page size and
 * the default of "Show Advanced". Stored per browser (localStorage) and applied immediately.
 */
export default function UiPage() {
  const { preferences, setPreference, setPreferences, resetPreferences } = usePreferences();
  const toast = useToast();
  const idPrefix = useId();
  const id = (n: string) => `${idPrefix}-${n}`;
  const [confirmReset, setConfirmReset] = useState(false);

  const stored = (preferences as UiPreferencesWithPageSize).defaultPageSize;
  const pageSize = typeof stored === 'number' && Number.isInteger(stored) && stored > 0 ? stored : DEFAULT_PAGE_SIZE;

  const setPageSize = (n: number) => {
    const patch: Partial<UiPreferences> & { defaultPageSize: number } = { defaultPageSize: n };
    setPreferences(patch);
  };

  return (
    <PageContent title="UI">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={RotateCcw} label="Reset" onClick={() => setConfirmReset(true)} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="UI" subtitle="Saved in this browser and applied immediately." />

        <SettingsSection title="Style">
          <FormGroup label="Theme" htmlFor={id('theme')} width="sm">
            <Select
              id={id('theme')}
              options={THEMES}
              value={preferences.theme}
              onChange={(t) => setPreference('theme', t)}
            />
          </FormGroup>
        </SettingsSection>

        <SettingsSection title="Dates">
          <FormGroup label="Short Date Format" htmlFor={id('shortDate')} width="sm">
            <Select
              id={id('shortDate')}
              options={SHORT_DATE_FORMATS.map((f) => ({ value: f, label: formatDateTokens(SAMPLE, f) }))}
              value={preferences.shortDateFormat}
              onChange={(f) => setPreference('shortDateFormat', f)}
            />
          </FormGroup>
          <FormGroup label="Long Date Format" htmlFor={id('longDate')} width="sm">
            <Select
              id={id('longDate')}
              options={LONG_DATE_FORMATS.map((f) => ({ value: f, label: formatDateTokens(SAMPLE, f) }))}
              value={preferences.longDateFormat}
              onChange={(f) => setPreference('longDateFormat', f)}
            />
          </FormGroup>
          <FormGroup label="Time Format" htmlFor={id('timeFormat')} width="sm">
            <Select
              id={id('timeFormat')}
              options={TIME_FORMATS}
              value={preferences.timeFormat}
              onChange={(f) => setPreference('timeFormat', f)}
            />
          </FormGroup>
          <FormGroup
            label="Show Relative Dates"
            htmlFor={id('relative')}
            helpText={`e.g. “${formatRelativeTime(new Date(Date.now() - 5 * 60_000))}” instead of the absolute date and time.`}
          >
            <Switch
              id={id('relative')}
              checked={preferences.showRelativeDates}
              onChange={(v) => setPreference('showRelativeDates', v)}
              aria-label="Show relative dates"
            />
          </FormGroup>
        </SettingsSection>

        <SettingsSection title="Lists">
          <FormGroup label="Default Page Size" htmlFor={id('pageSize')} helpText="Rows per page in paged lists." width="sm">
            <Select
              id={id('pageSize')}
              options={pageSizeOptions(pageSize)}
              value={String(pageSize)}
              onChange={(v) => setPageSize(Number(v))}
            />
          </FormGroup>
        </SettingsSection>

        <SettingsSection title="Settings Pages">
          <FormGroup
            label="Show Advanced Settings"
            htmlFor={id('advanced')}
            helpText="Show advanced settings (orange labels) by default. The “Show Advanced” toolbar button toggles the same preference."
          >
            <Switch
              id={id('advanced')}
              checked={preferences.showAdvanced}
              onChange={(v) => setPreference('showAdvanced', v)}
              aria-label="Show advanced settings"
            />
          </FormGroup>
        </SettingsSection>
      </PageBody>

      <ConfirmDialog
        open={confirmReset}
        title="Reset UI Settings"
        message="Restore the default theme, date formats and list settings in this browser?"
        confirmLabel="Reset"
        kind="warning"
        onCancel={() => setConfirmReset(false)}
        onConfirm={() => {
          setConfirmReset(false);
          resetPreferences();
          toast.success('UI settings reset');
        }}
      />
    </PageContent>
  );
}
