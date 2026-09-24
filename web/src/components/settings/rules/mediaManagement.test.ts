import { describe, expect, it } from 'vitest';
import { ApiError } from '@/api/client';
import type { Settings } from '@/api/types';
import {
  MEDIA_MANAGEMENT_FIELDS,
  SETTINGS_FIELD_LABELS,
  SETTINGS_FIELD_TYPES,
  SETTINGS_NUMBER_LIMITS,
  mapSettingsErrors,
  numberSettingProblem,
  settingsFieldLabel,
  formatHours,
  incompleteSettingsFields,
  isCompleteSettings,
  discSettingsWarnings,
  methodInfo,
  recycleBinProblems,
  validateSettings,
} from './mediaManagement';

/** models.DefaultSettings() */
const DEFAULT_SETTINGS: Settings = {
  dryRun: true,
  mode: 'manual',
  scanIntervalMinutes: 360,
  minAgeHours: 168,
  maxDeletionsPerRun: 25,
  deletionMethods: ['arr', 'plex', 'filesystem'],
  arrRescanAfterDelete: true,
  unmonitorWhenKeeperElsewhere: true,
  addExclusionWhenKeeperElsewhere: false,
  recycleBinPath: '',
  recycleBinCleanupDays: 7,
  refreshPlexAfterDelete: true,
  cleanupPlexStaleEntries: true,
  treatEditionsAsDistinct: true,
  treat3DAsDistinct: true,
  languageVariantsAsDistinct: true,
  differentArrInstancesIntentional: true,
  maxGroupSize: 4,
  stableScansRequired: 2,
  maxBytesPerRunGb: 500,
  durationTolerancePercent: 10,
  durationToleranceMinutes: 5,
  historyRetentionDays: 90,
  backupIntervalDays: 7,
  backupRetentionDays: 28,
  detectDiscs: true,
  allowDiscRemoval: false,
  keepPlayableCopy: true,
};

const BASE = DEFAULT_SETTINGS;

describe('formatHours', () => {
  it.each([
    [0, '0 hours'],
    [1, '1 hour'],
    [12, '12 hours'],
    [24, '1 day'],
    [36, '1 day 12 hours'],
    [168, '7 days'],
    [-5, '0 hours'],
  ])('%d → %s', (h, expected) => {
    expect(formatHours(h)).toBe(expected);
  });
});

describe('methodInfo', () => {
  it('labels permanence per method', () => {
    expect(methodInfo('plex', '').kind).toBe('danger');
    expect(methodInfo('arr', '').text).toMatch(/permanent unless its recycle bin is set/);
    expect(methodInfo('filesystem', '').badge).toMatch(/Permanent/);
    expect(methodInfo('filesystem', '/data/.dupearr-recycle').kind).toBe('success');
  });
});

describe('settings completeness', () => {
  it('accepts a complete Settings object', () => {
    expect(incompleteSettingsFields(BASE)).toEqual([]);
    expect(isCompleteSettings(BASE)).toBe(true);
  });

  it('knows every Settings field', () => {
    expect(Object.keys(SETTINGS_FIELD_TYPES).sort()).toEqual(Object.keys(BASE).sort());
  });

  it.each([
    ['an empty object', {}, Object.keys(BASE).length],
    ['null', null, Object.keys(BASE).length],
    ['an array', [], Object.keys(BASE).length],
  ])('rejects %s', (_name, value, missing) => {
    expect(incompleteSettingsFields(value)).toHaveLength(missing);
    expect(isCompleteSettings(value)).toBe(false);
  });

  it('flags missing and mistyped fields', () => {
    const { dryRun: _omit, ...withoutDryRun } = BASE;
    expect(incompleteSettingsFields(withoutDryRun)).toEqual(['dryRun']);
    expect(incompleteSettingsFields({ ...BASE, dryRun: 'false' })).toEqual(['dryRun']);
    expect(incompleteSettingsFields({ ...BASE, maxDeletionsPerRun: Number.NaN })).toEqual(['maxDeletionsPerRun']);
    expect(incompleteSettingsFields({ ...BASE, deletionMethods: null })).toEqual(['deletionMethods']);
  });
});

describe('recycleBinProblems', () => {
  const mappings = [{ localPath: '/data/media/movies' }, { localPath: '/data/media/tv/' }];

  it.each([
    ['', [], []],
    ['/data/.dupearr-recycle', [], []],
    ['/data/media/movies/.dupearr-recycle', [], []],
    ['recycle', [/absolute path/], []],
    ['/', [/filesystem root/], []],
    ['C:\\', [/filesystem root/], []],
    ['/data', [/must not contain the media folder \/data\/media\/movies/, /must not contain the media folder \/data\/media\/tv/], []],
    ['/data/media/movies', [/must not contain the media folder \/data\/media\/movies/], []],
    ['/DATA/Media/Movies/', [/must not contain the media folder \/data\/media\/movies/], []],
    ['/data/media/tv/recycle', [], [/inside the media folder \/data\/media\/tv/]],
    ['/data/media/movies-recycle', [], []],
  ] as const)('%j', (bin, errors, warnings) => {
    const got = recycleBinProblems(bin, mappings);
    expect(got.errors).toHaveLength(errors.length);
    errors.forEach((re, i) => expect(got.errors[i]).toMatch(re));
    expect(got.warnings).toHaveLength(warnings.length);
    warnings.forEach((re, i) => expect(got.warnings[i]).toMatch(re));
  });
});

describe('validateSettings', () => {
  it('accepts the defaults', () => {
    expect(validateSettings(BASE)).toEqual({});
  });

  it('requires a deletion method, an absolute recycle bin and ≥1 stable scan in auto mode', () => {
    expect(
      validateSettings({ ...BASE, deletionMethods: [], recycleBinPath: 'recycle', mode: 'auto', stableScansRequired: 0 }),
    ).toEqual({
      deletionMethods: ['Choose at least one deletion method'],
      recycleBinPath: ['Use an absolute path, e.g. /data/.dupearr-recycle'],
      stableScansRequired: ['Must be at least 1'],
    });
    expect(validateSettings({ ...BASE, deletionMethods: null as never })).toHaveProperty('deletionMethods');
    // Hidden in manual mode: not checked there.
    expect(validateSettings({ ...BASE, stableScansRequired: 0 })).toEqual({});
  });

  it('never lets the circuit breakers or the minimum age be switched off', () => {
    const e = validateSettings({
      ...BASE,
      maxDeletionsPerRun: 0,
      maxBytesPerRunGb: 0,
      minAgeHours: -1,
      recycleBinCleanupDays: -1,
    });
    expect(Object.keys(e).sort()).toEqual(['maxBytesPerRunGb', 'maxDeletionsPerRun', 'minAgeHours', 'recycleBinCleanupDays']);
    expect(e.maxDeletionsPerRun).toEqual(['Must be at least 1 — this limit cannot be turned off']);
    expect(validateSettings({ ...BASE, maxDeletionsPerRun: Number.NaN })).toHaveProperty('maxDeletionsPerRun');
    expect(validateSettings({ ...BASE, minAgeHours: 0 })).toEqual({});
  });

  it('accepts 0 where the server does (keep forever / off)', () => {
    expect(validateSettings({ ...BASE, recycleBinCleanupDays: 0, historyRetentionDays: 0, scanIntervalMinutes: 0 })).toEqual({});
    expect(numberSettingProblem('backupIntervalDays', 0)).toBeNull();
    expect(numberSettingProblem('backupRetentionDays', 0)).toBeNull();
  });

  it("applies the server's upper limits and the 15-minute scan interval", () => {
    expect(validateSettings({ ...BASE, mode: 'auto', stableScansRequired: 100 })).toEqual({});
    expect(validateSettings({ ...BASE, mode: 'auto', stableScansRequired: 101 })).toEqual({
      stableScansRequired: ['Must be between 1 and 100'],
    });
    expect(
      validateSettings({
        ...BASE,
        maxDeletionsPerRun: 10_001,
        maxBytesPerRunGb: 1_000_001,
        minAgeHours: 87_601,
        recycleBinCleanupDays: 3651,
        maxGroupSize: 101,
        durationTolerancePercent: 100.5,
        durationToleranceMinutes: 601,
        historyRetentionDays: 36_501,
        scanIntervalMinutes: 525_601,
      }),
    ).toEqual({
      maxDeletionsPerRun: ['Must be between 1 and 10000'],
      maxBytesPerRunGb: ['Must be between 1 and 1000000'],
      minAgeHours: ['Must be between 0 and 87600'],
      recycleBinCleanupDays: ['Must be between 0 and 3650'],
      maxGroupSize: ['Must be between 2 and 100'],
      durationTolerancePercent: ['Must be between 0 and 100'],
      durationToleranceMinutes: ['Must be between 0 and 600'],
      historyRetentionDays: ['Must be between 0 and 36500'],
      scanIntervalMinutes: ['Must be between 0 and 525600'],
    });
    expect(validateSettings({ ...BASE, scanIntervalMinutes: 10 })).toEqual({
      scanIntervalMinutes: ['Must be 0 (scheduled scans off) or at least 15 minutes'],
    });
    expect(validateSettings({ ...BASE, scanIntervalMinutes: 15, maxGroupSize: 2, durationTolerancePercent: 12.5 })).toEqual({});
    expect(validateSettings({ ...BASE, maxGroupSize: 1 })).toEqual({ maxGroupSize: ['Must be between 2 and 100'] });
    expect(validateSettings({ ...BASE, historyRetentionDays: 1.5 })).toEqual({ historyRetentionDays: ['Must be a whole number'] });
    expect(numberSettingProblem('backupIntervalDays', 366)).toBe('Must be between 0 and 365');
    expect(numberSettingProblem('backupRetentionDays', 3651)).toBe('Must be between 0 and 3650');
  });

  it('has server limits for every numeric setting', () => {
    const numeric = (Object.entries(SETTINGS_FIELD_TYPES) as [string, string][])
      .filter(([, type]) => type === 'number')
      .map(([k]) => k)
      .sort();
    expect(Object.keys(SETTINGS_NUMBER_LIMITS).sort()).toEqual(numeric);
  });

  it('checks the recycle bin against the path mappings', () => {
    expect(validateSettings({ ...BASE, recycleBinPath: '/data' }, { mappings: [{ localPath: '/data/media' }] })).toEqual({
      recycleBinPath: [expect.stringMatching(/must not contain the media folder \/data\/media/)],
    });
    expect(validateSettings({ ...BASE, recycleBinPath: '/data/.dupearr-recycle' }, { mappings: [{ localPath: '/data/media' }] })).toEqual(
      {},
    );
  });
});

describe('mapSettingsErrors', () => {
  const validation = (errors: { propertyName: string; errorMessage: string }[]) =>
    new ApiError('Validation failed', { status: 400, validationErrors: errors });

  it('has a label for every settings field', () => {
    expect(Object.keys(SETTINGS_FIELD_LABELS).sort()).toEqual(Object.keys(SETTINGS_FIELD_TYPES).sort());
  });

  it('maps camelCase properties to the shown fields and labels the rest', () => {
    const mapped = mapSettingsErrors(
      validation([
        { propertyName: 'maxBytesPerRunGb', errorMessage: 'Must be between 1 and 1000000' },
        { propertyName: 'StableScansRequired', errorMessage: 'Must be between 1 and 100' },
        { propertyName: 'deletionMethods[0]', errorMessage: 'Unknown deletion method' },
        { propertyName: 'backupIntervalDays', errorMessage: 'Must be between 0 and 365' },
        { propertyName: 'backupRetentionDays', errorMessage: 'Backup Retention must be set' },
        { propertyName: 'somethingNew', errorMessage: 'Nope' },
        { propertyName: '', errorMessage: 'General problem' },
      ]),
      ['maxBytesPerRunGb', 'stableScansRequired', 'deletionMethods'],
    );
    expect(mapped.fields).toEqual({
      maxBytesPerRunGb: ['Must be between 1 and 1000000'],
      stableScansRequired: ['Must be between 1 and 100'],
      deletionMethods: ['Unknown deletion method'],
    });
    expect(mapped.other).toEqual([
      'Backup Interval: Must be between 0 and 365',
      'Backup Retention must be set',
      'Something New: Nope',
      'General problem',
    ]);
  });

  it('ignores non-validation errors', () => {
    expect(mapSettingsErrors(new ApiError('Boom', { status: 500 }), ['dryRun'])).toEqual({ fields: {}, other: [] });
    expect(mapSettingsErrors(null, ['dryRun'])).toEqual({ fields: {}, other: [] });
  });

  it('labels settings properties for other pages', () => {
    expect(settingsFieldLabel('MaxBytesPerRunGB')).toBe('Max Size per Run');
    expect(settingsFieldLabel('port')).toBeUndefined();
  });
});

describe('full-disc settings', () => {
  it('are part of the Media Management form, labelled and typed', () => {
    for (const field of ['detectDiscs', 'allowDiscRemoval', 'keepPlayableCopy'] as const) {
      expect(MEDIA_MANAGEMENT_FIELDS).toContain(field);
      expect(SETTINGS_FIELD_TYPES[field]).toBe('boolean');
    }
    expect(SETTINGS_FIELD_LABELS.allowDiscRemoval).toBe('Allow Removing Full Discs');
    // An older server without the disc fields: the page refuses to save rather than guess them.
    const { detectDiscs: _d, allowDiscRemoval: _a, keepPlayableCopy: _k, ...older } = BASE;
    expect(incompleteSettingsFields(older)).toEqual(['detectDiscs', 'allowDiscRemoval', 'keepPlayableCopy']);
  });

  it('maps server errors on the disc settings to their fields', () => {
    const err = new ApiError('Validation failed', {
      status: 400,
      validationErrors: [{ propertyName: 'allowDiscRemoval', errorMessage: 'Needs a recycle bin' }],
    });
    expect(mapSettingsErrors(err, MEDIA_MANAGEMENT_FIELDS).fields.allowDiscRemoval).toEqual(['Needs a recycle bin']);
  });

  it('has no warnings by default (discs protected, a playable copy kept)', () => {
    expect(discSettingsWarnings(BASE)).toEqual({ allowDiscRemoval: [], keepPlayableCopy: [] });
  });

  it('warns when disc removal is on without a recycle bin, the filesystem method or detection', () => {
    const w = discSettingsWarnings({ ...BASE, allowDiscRemoval: true, recycleBinPath: ' ', deletionMethods: ['arr'], detectDiscs: false });
    expect(w.allowDiscRemoval).toEqual([
      'No recycle bin is set: every disc removal is refused until you set one below.',
      'Discs are only removed by the filesystem method, which is not in your deletion methods, so disc removals may be refused.',
      'Disc detection is off, so no disc is found to remove.',
    ]);
    expect(
      discSettingsWarnings({ ...BASE, allowDiscRemoval: true, recycleBinPath: '/data/.dupearr-recycle' }).allowDiscRemoval,
    ).toEqual([]);
  });

  it('warns when a disc may become the only kept copy', () => {
    expect(discSettingsWarnings({ ...BASE, keepPlayableCopy: false }).keepPlayableCopy[0]).toMatch(/Plex then has nothing to play/);
  });
});
