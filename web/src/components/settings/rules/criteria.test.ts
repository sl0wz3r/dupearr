import { describe, expect, it } from 'vitest';
import type { ArrInstance, Criterion, Library } from '@/api/types';
import {
  blankDraft,
  countProfileErrors,
  criterionSummary,
  defaultOrder,
  describeCriterion,
  directionWording,
  draftFromTemplate,
  editorKind,
  explainCriteria,
  explainKeep,
  explainProtections,
  fixedDirection,
  hasProfileErrors,
  keepPerValues,
  keepSummary,
  mapProfileServerErrors,
  minDeltaUnit,
  newCriterion,
  normalizeProfile,
  orderedOptions,
  protectionTypes,
  requiresWatchHistory,
  schemaMap,
  summarizeCriteria,
  supportsMinDelta,
  templateDescription,
  toProfileInput,
  uniqueName,
  validatePattern,
  validateProfileDraft,
  type ProfileDraft,
} from './criteria';
import { TEST_SCHEMA } from './test-utils';

const SMAP = schemaMap(TEST_SCHEMA);
const LIBS = [
  { id: 1, title: 'Movies' },
  { id: 2, title: 'Movies 4K' },
] as Library[];
const ARR = [{ id: 7, name: 'Radarr 4K', kind: 'radarr' }] as ArrInstance[];
const CTX = { schema: SMAP, libraries: LIBS, arrInstances: ARR };

function draft(p: Partial<ProfileDraft> = {}): ProfileDraft {
  return { name: 'Test', isDefault: false, criteria: [], keepCount: 1, keepPer: '', protections: [], ...p };
}

describe('schema lookups', () => {
  it('derives the editor kind, forcing health to have no parameters', () => {
    expect(editorKind('health', SMAP.get('health'))).toBe('none');
    expect(editorKind('resolution', SMAP.get('resolution'))).toBe('ordered');
    expect(editorKind('file_size')).toBe('numeric'); // fallback without schema
    expect(editorKind('filename_score')).toBe('patterns');
    expect(editorKind('resolution', { ...SMAP.get('resolution')!, kind: 'weird' as never })).toBe('none');
  });

  it('builds ordered options from the schema, libraries and unknown current values', () => {
    expect(orderedOptions('resolution', SMAP.get('resolution')).map((o) => o.value)).toEqual([
      '2160',
      '1440',
      '1080',
      '720',
      '576',
      '480',
      'sd',
    ]);
    // No schema entry → built-in labels.
    expect(orderedOptions('source', undefined)[0]).toEqual({ value: 'remux', label: 'Remux' });
    // A full disc ranks right after a remux; a standalone M2TS is its own container.
    expect(orderedOptions('source', undefined)[1]).toEqual({ value: 'disc', label: 'Full disc (BDMV/VIDEO_TS/ISO)' });
    expect(defaultOrder('source').slice(0, 3)).toEqual(['remux', 'disc', 'bluray']);
    expect(orderedOptions('container', undefined)).toContainEqual({ value: 'm2ts', label: 'M2TS' });
    expect(defaultOrder('container')).toEqual(['mkv', 'mp4', 'm4v', 'm2ts', 'other', 'avi', 'ts']);
    expect(orderedOptions('library', undefined, LIBS, ['1', '99'])).toEqual([
      { value: '1', label: 'Movies' },
      { value: '2', label: 'Movies 4K' },
      { value: '99', label: 'Unknown library (99)' },
    ]);
  });

  it('always offers arr_tag protections and the empty keepPer', () => {
    expect(protectionTypes(TEST_SCHEMA)).toEqual(['path_glob', 'library', 'arr_instance', 'arr_tag']);
    expect(protectionTypes(undefined)).toContain('arr_tag');
    expect(keepPerValues({ ...TEST_SCHEMA, keepPer: ['resolution'] })).toEqual(['', 'resolution']);
    expect(keepPerValues(undefined)).toEqual(['', 'resolution', 'dynamic_range']);
  });
});

describe('newCriterion', () => {
  it.each([
    ['resolution', { type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', '576', '480', 'sd'] }],
    ['video_bitrate', { type: 'video_bitrate', enabled: true, direction: 'higher', tolerancePercent: 15 }],
    ['file_size', { type: 'file_size', enabled: true, direction: 'higher', tolerancePercent: 5 }],
    ['custom_format_score', { type: 'custom_format_score', enabled: true, direction: 'higher', minDelta: 10 }],
    ['date_added', { type: 'date_added', enabled: true, direction: 'higher' }],
    ['filename_score', { type: 'filename_score', enabled: true, patterns: [] }],
    ['health', { type: 'health', enabled: true }],
    ['arr_managed', { type: 'arr_managed', enabled: true }],
  ] as const)('%s gets kind-appropriate defaults', (type, expected) => {
    expect(newCriterion(type, SMAP.get(type))).toEqual(expected);
  });

  it('uses the D5 dynamic range order when the schema has none', () => {
    expect(newCriterion('dynamic_range').order).toEqual(['dv_hdr10', 'hdr10plus', 'hdr10', 'hlg', 'dv', 'sdr']);
  });
});

describe('direction wording', () => {
  it.each([
    ['date_added', 'Newer', 'Older'],
    ['file_size', 'Larger', 'Smaller'],
    ['audio_channels', 'More channels', 'Fewer channels'],
    ['bit_depth', 'Higher bit depth', 'Lower bit depth'],
  ] as const)('%s → %s / %s', (type, higher, lower) => {
    const w = directionWording(type);
    expect(w.higher).toBe(higher);
    expect(w.lower).toBe(lower);
  });
});

describe('explanation', () => {
  const chain: Criterion[] = [
    { type: 'health', enabled: true },
    { type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', 'sd'] },
    { type: 'dynamic_range', enabled: false, order: ['sdr'] },
    { type: 'video_bitrate', enabled: true, direction: 'higher', tolerancePercent: 15 },
    { type: 'date_added', enabled: true, direction: 'higher' },
  ];

  it('describes the enabled chain in one sentence', () => {
    expect(explainCriteria(chain, CTX)).toBe(
      'Keep the healthiest file; then the highest resolution (2160p > 1440p > 1080p > …); ' +
        'then the higher video bitrate (same codec only; differences under 15% count as a tie); ' +
        'if still tied, prefer the newer file.',
    );
  });

  it('handles empty and single chains', () => {
    expect(explainCriteria([], CTX)).toMatch(/No criteria are enabled/);
    expect(explainCriteria([{ type: 'file_size', enabled: true, direction: 'lower' }], CTX)).toBe('Keep the smaller file.');
  });

  it.each<[Criterion, string]>([
    [{ type: 'resolution', enabled: true, order: ['1080', '2160'] }, 'the preferred resolution (1080p > 2160p)'],
    [{ type: 'resolution', enabled: true, order: [] }, 'the preferred resolution (no values chosen yet)'],
    [{ type: 'dynamic_range', enabled: true, order: ['hdr10', 'sdr'] }, 'the preferred dynamic range (HDR10 > SDR)'],
    [{ type: 'library', enabled: true, order: ['2', '1'] }, 'the preferred library (Movies 4K > Movies)'],
    [
      { type: 'custom_format_score', enabled: true, direction: 'higher', minDelta: 10 },
      'the higher custom format score (same *arr instance only; differences under 10 points count as a tie)',
    ],
    [{ type: 'arr_managed', enabled: true }, 'a file tracked by Radarr/Sonarr'],
    [{ type: 'arr_managed', enabled: true, value: '7' }, 'a file tracked by Radarr 4K'],
    [{ type: 'arr_managed', enabled: true, value: '8' }, 'a file tracked by *arr instance #8'],
    [{ type: 'audio_language', enabled: true, value: 'eng' }, 'a file with an English audio track'],
    [{ type: 'audio_language', enabled: true, value: 'tlh' }, 'a file with a tlh audio track'],
    [{ type: 'filename_score', enabled: true, patterns: [] }, 'the highest filename score (no patterns yet)'],
    [
      {
        type: 'filename_score',
        enabled: true,
        patterns: [
          { pattern: '**/*Remux*', score: 10, regex: false, caseSensitive: false },
          { pattern: '**/*.ts', score: -5, regex: false, caseSensitive: false },
          { pattern: 'x', score: 1, regex: true, caseSensitive: false },
        ],
      },
      'the highest filename score (+10 **/*Remux*, −5 **/*.ts, …)',
    ],
  ])('describes %j', (c, expected) => {
    expect(describeCriterion(c, CTX)).toBe(expected);
  });

  it('uses compact value labels in chains', () => {
    expect(
      describeCriterion({ type: 'dynamic_range', enabled: true, order: ['dv_hdr10', 'hdr10plus', 'hdr10', 'sdr'] }, CTX),
    ).toBe('the preferred dynamic range (DV HDR10 > HDR10+ > HDR10 > …)');
    // Short labels are kept whole (no schema → built-in labels); long ones lose a trailing "(…)".
    expect(describeCriterion({ type: 'video_codec', enabled: true, order: ['hevc', 'h264'] }, { schema: new Map() })).toBe(
      'the preferred video codec (HEVC (H.265) > AVC (H.264))',
    );
    const longLabels = new Map([
      [
        'source' as const,
        { ...SMAP.get('resolution')!, type: 'source' as const, options: [{ value: 'remux', label: 'Blu-ray Remux (lossless)' }] },
      ],
    ]);
    expect(describeCriterion({ type: 'source', enabled: true, order: ['remux'] }, { schema: longLabels })).toBe(
      'the preferred source (Blu-ray Remux)',
    );
  });

  it.each<[Criterion, string]>([
    [{ type: 'health', enabled: true }, ''],
    [{ type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', 'sd'] }, '2160p > 1440p > 1080p > 720p > …'],
    [{ type: 'resolution', enabled: true, order: [] }, 'No values ranked'],
    [{ type: 'library', enabled: true, order: ['1', '2'] }, 'Movies > Movies 4K'],
    [{ type: 'date_added', enabled: true, direction: 'lower' }, 'Older'],
    [{ type: 'file_size', enabled: true, direction: 'higher', tolerancePercent: 5 }, 'Larger · 5% tolerance'],
    [{ type: 'custom_format_score', enabled: true, direction: 'higher', minDelta: 10 }, 'Higher score · min delta 10 points'],
    [{ type: 'arr_managed', enabled: true }, 'Any *arr'],
    [{ type: 'arr_managed', enabled: true, value: '7' }, 'Radarr 4K'],
    [{ type: 'audio_language', enabled: true, value: 'ger' }, 'German (ger)'],
    [{ type: 'audio_language', enabled: true }, 'No language chosen'],
    [{ type: 'filename_score', enabled: true, patterns: [] }, 'No patterns'],
    [
      { type: 'filename_score', enabled: true, patterns: [{ pattern: 'a', score: 1, regex: false, caseSensitive: false }] },
      '1 pattern',
    ],
  ])('summarizes %j', (c, expected) => {
    expect(criterionSummary(c, CTX)).toBe(expected);
  });

  it('explains keep options and protections', () => {
    expect(explainKeep(1, '')).toMatch(/single best copy/);
    expect(explainKeep(2, '')).toMatch(/best 2 copies/);
    expect(explainKeep(1, 'resolution')).toMatch(/best 4K AND the best 1080p/);
    expect(explainKeep(1, 'dynamic_range')).toMatch(/dynamic range/);
    expect(explainProtections([], CTX)).toBeNull();
    expect(
      explainProtections(
        [
          { type: 'library', value: '2' },
          { type: 'arr_instance', value: '7' },
          { type: 'arr_tag', value: 'dupearr-keep' },
          { type: 'path_glob', value: '/data/keep/**' },
        ],
        CTX,
      ),
    ).toBe(
      'Never removes files in the Movies 4K library; files tracked by Radarr 4K; items tagged “dupearr-keep” in Radarr/Sonarr; paths matching /data/keep/**.',
    );
    // An empty tag protects nothing (and cannot be saved): never pretend the default tag applies.
    expect(explainProtections([{ type: 'arr_tag', value: '  ' }], CTX)).toBe(
      'Never removes items with a Radarr/Sonarr tag (none entered).',
    );
  });
});

describe('summaries and templates', () => {
  it('summarizes the first enabled criteria', () => {
    expect(
      summarizeCriteria(
        [
          { type: 'health', enabled: true },
          { type: 'resolution', enabled: false },
          { type: 'file_size', enabled: true },
          { type: 'date_added', enabled: true },
          { type: 'video_bitrate', enabled: true },
        ],
        SMAP,
      ),
    ).toEqual({ labels: ['File Health', 'File Size', 'Date Added'], more: 1 });
    expect(summarizeCriteria(null, SMAP)).toEqual({ labels: [], more: 0 });
    expect(keepSummary(2, 'resolution')).toBe('Keep best 2 per resolution');
    expect(keepSummary(0, '')).toBe('Keep best 1');
  });

  it('describes known templates and summarizes unknown ones', () => {
    expect(templateDescription(TEST_SCHEMA.templates[1]!, SMAP)).toMatch(/smallest file/);
    expect(templateDescription({ ...TEST_SCHEMA.templates[1]!, name: 'Custom' }, SMAP)).toBe(
      'Decides by File Health, File Size. Keep best 1.',
    );
  });

  it('copies templates as new, non-default profiles with unique names', () => {
    const copy = draftFromTemplate({ ...TEST_SCHEMA.templates[0]!, id: 3 }, ['keep highest quality']);
    expect(copy.id).toBeUndefined();
    expect(copy.isDefault).toBe(false);
    expect(copy.name).toBe('Keep Highest Quality (2)');
    expect(copy.criteria).toEqual(TEST_SCHEMA.templates[0]!.criteria);
    expect(copy.criteria).not.toBe(TEST_SCHEMA.templates[0]!.criteria);
    expect(uniqueName('A', ['A', 'a (2)'])).toBe('A (3)');
    expect(uniqueName('  ', [])).toBe('New Profile');
  });

  it('starts blank drafts with only the health criterion', () => {
    expect(blankDraft(SMAP, []).criteria).toEqual([{ type: 'health', enabled: true }]);
    expect(blankDraft(schemaMap({ ...TEST_SCHEMA, criteria: TEST_SCHEMA.criteria.filter((c) => c.type !== 'health') }), []).criteria).toEqual([]);
  });

  it('normalizes null slices and bad keep counts', () => {
    const n = normalizeProfile({ id: 4, name: 'X', criteria: null as never, protections: null as never, keepCount: 0 });
    expect(n).toEqual({ id: 4, name: 'X', isDefault: false, criteria: [], keepCount: 1, keepPer: '', protections: [] });
  });

  it('builds a clean request body', () => {
    expect(
      toProfileInput(
        draft({
          name: '  Mine ',
          keepCount: 2.4,
          criteria: [
            { type: 'arr_managed', enabled: true, value: '' },
            { type: 'file_size', enabled: true, direction: 'lower', tolerancePercent: 0, minDelta: 0 },
          ],
          protections: [{ type: 'arr_tag', value: ' keep ' }],
        }),
      ),
    ).toEqual({
      name: 'Mine',
      isDefault: false,
      keepCount: 2,
      keepPer: '',
      criteria: [
        { type: 'arr_managed', enabled: true },
        { type: 'file_size', enabled: true, direction: 'lower' },
      ],
      protections: [{ type: 'arr_tag', value: 'keep' }],
    });
  });
});

describe('validation', () => {
  it.each([
    [{ pattern: '', score: 1, regex: false, caseSensitive: false }, 'Pattern is required'],
    [{ pattern: '**/*Remux*', score: 1, regex: false, caseSensitive: false }, null],
    [{ pattern: '**/[abc', score: 1, regex: false, caseSensitive: false }, 'Unclosed "[" in pattern'],
    [{ pattern: '(unclosed', score: 1, regex: true, caseSensitive: false }, /Invalid regular expression/],
    [{ pattern: '(?=x)', score: 1, regex: true, caseSensitive: false }, /Look-around/],
    [{ pattern: '(?i)remux', score: 1, regex: true, caseSensitive: false }, null],
  ] as const)('validatePattern(%j)', (p, expected) => {
    const got = validatePattern(p);
    if (expected === null) expect(got).toBeNull();
    else if (typeof expected === 'string') expect(got).toBe(expected);
    else expect(got).toMatch(expected);
  });

  it('checks the whole draft', () => {
    const e = validateProfileDraft(
      draft({
        name: ' ',
        keepCount: 0,
        criteria: [
          { type: 'resolution', enabled: true, order: [] },
          { type: 'audio_language', enabled: true },
          { type: 'filename_score', enabled: true, patterns: [{ pattern: '(', score: 1, regex: true, caseSensitive: false }] },
          { type: 'file_size', enabled: true, direction: 'higher', tolerancePercent: 150 },
          { type: 'library', enabled: false, order: [] }, // disabled: not checked
        ],
        protections: [
          { type: 'path_glob', value: '' },
          { type: 'path_glob', value: '/a/{b' },
          { type: 'arr_tag', value: 'dupearr-keep' },
        ],
      }),
      SMAP,
    );
    expect(e.name).toEqual(['Name is required']);
    expect(e.keepCount).toEqual(['Keep count must be at least 1']);
    expect(e.criterion.resolution).toEqual(['Choose at least one value to rank']);
    expect(e.criterion.audio_language).toEqual(['Choose an audio language']);
    expect(e.criterion.filename_score).toEqual(['1 pattern is invalid']);
    expect(e.criterion.file_size).toEqual(['Tolerance must be between 0 and 100%']);
    expect(e.criterion.library).toBeUndefined();
    expect(e.protection).toEqual({ 0: ['A value is required'], 1: ['Unclosed "{" in pattern'] });
    expect(hasProfileErrors(e)).toBe(true);
    expect(countProfileErrors(e)).toBe(8);

    const ok = validateProfileDraft(draft({ criteria: [{ type: 'health', enabled: true }] }), SMAP);
    expect(hasProfileErrors(ok)).toBe(false);
  });

  it('checks filename patterns even when the step is disabled (the server does)', () => {
    const e = validateProfileDraft(
      draft({ criteria: [{ type: 'filename_score', enabled: false, patterns: [] }] }),
      SMAP,
    );
    expect(e.criterion.filename_score).toEqual(['Add at least one pattern']);
    const bad = validateProfileDraft(
      draft({
        criteria: [
          { type: 'filename_score', enabled: false, patterns: [{ pattern: '[x', score: 1, regex: false, caseSensitive: false }] },
        ],
      }),
      SMAP,
    );
    expect(bad.criterion.filename_score).toEqual(['1 pattern is invalid']);
  });

  it('flags repeated criterion types except filename_score (the engine allows several)', () => {
    const pattern = { pattern: '**/*Remux*', score: 5, regex: false, caseSensitive: false };
    const e = validateProfileDraft(
      draft({
        criteria: [
          { type: 'resolution', enabled: true, order: ['2160'] },
          { type: 'filename_score', enabled: true, patterns: [pattern] },
          { type: 'resolution', enabled: true, order: ['1080'] },
          { type: 'filename_score', enabled: true, patterns: [pattern] },
        ],
      }),
      SMAP,
    );
    expect(e.criterion.resolution).toEqual(['This criterion is listed more than once']);
    expect(e.criterion.filename_score).toBeUndefined();
  });

  it('maps server errors by path, re-keying criteria by the submitted type', () => {
    const submitted = draft({
      criteria: [
        { type: 'health', enabled: true },
        { type: 'resolution', enabled: true, order: [] },
      ],
      protections: [{ type: 'library', value: '9' }],
    });
    const e = mapProfileServerErrors(
      [
        { propertyName: 'Name', errorMessage: 'Name must be unique' },
        { propertyName: 'criteria[1].order', errorMessage: 'Order is empty' },
        { propertyName: 'Criteria.0', errorMessage: 'Health must be first' },
        { propertyName: 'criteria[5].type', errorMessage: 'Unknown criterion' },
        { propertyName: 'criteria', errorMessage: 'Duplicate criterion types' },
        { propertyName: 'protections[0].value', errorMessage: 'Library not found' },
        { propertyName: 'protections[3]', errorMessage: 'Out of range' },
        { propertyName: 'profile.keepCount', errorMessage: 'Too small' },
        { propertyName: '', errorMessage: 'Something else' },
        { propertyName: 'mystery', errorMessage: 'Mystery' },
      ],
      submitted,
    );
    expect(e.name).toEqual(['Name must be unique']);
    expect(e.criterion).toEqual({ resolution: ['Order is empty'], health: ['Health must be first'] });
    expect(e.criteria).toEqual(['Unknown criterion', 'Duplicate criterion types']);
    expect(e.protection).toEqual({ 0: ['Library not found'] });
    expect(e.protections).toEqual(['Out of range']);
    expect(e.keepCount).toEqual(['Too small']);
    expect(e.general).toEqual(['Something else', 'Mystery']);
  });
});

// docs/DECISIONS.md D10 (issue #5): play-history criteria.
describe('play-history criteria', () => {
  it('Played has no parameters and says an unknown history is a tie', () => {
    expect(editorKind('played', SMAP.get('played'))).toBe('none');
    expect(newCriterion('played', SMAP.get('played'))).toEqual({ type: 'played', enabled: true });
    expect(describeCriterion({ type: 'played', enabled: true }, CTX)).toBe(
      'a file that has been played (Tautulli; an unknown play history is a tie)',
    );
    expect(criterionSummary({ type: 'played', enabled: true }, CTX)).toBe('');
  });

  it('Last played has a fixed direction and a minimum difference in days (default 30)', () => {
    const schema = SMAP.get('last_played');
    expect(editorKind('last_played', schema)).toBe('numeric');
    expect(fixedDirection('last_played')).toBe(true);
    expect(fixedDirection('date_added')).toBe(false);
    expect(supportsMinDelta('last_played', schema)).toBe(true);
    expect(minDeltaUnit('last_played', schema)).toBe('days');
    expect(minDeltaUnit('last_played')).toBe('days');
    const c = newCriterion('last_played', schema);
    expect(c).toEqual({ type: 'last_played', enabled: true, direction: 'higher', minDelta: 30 });
    expect(describeCriterion(c, CTX)).toBe('the most recently played file (plays less than 30 days apart count as a tie)');
    expect(describeCriterion({ type: 'last_played', enabled: true, direction: 'higher' }, CTX)).toBe(
      'the most recently played file (an unknown play history is a tie)',
    );
    // A stored "lower" never shows as "less recently played": the server always prefers the newer play.
    expect(criterionSummary({ ...c, direction: 'lower' }, CTX)).toBe('More recently played · min delta 30 days');
  });

  it('flags both criteria as needing Tautulli (schema first, built-in fallback)', () => {
    expect(requiresWatchHistory('played', SMAP.get('played'))).toBe(true);
    expect(requiresWatchHistory('last_played')).toBe(true);
    expect(requiresWatchHistory('resolution', SMAP.get('resolution'))).toBe(false);
  });

  it('never words a copy as unwatched', () => {
    const text = explainCriteria(
      [
        { type: 'health', enabled: true },
        { type: 'played', enabled: true },
        { type: 'last_played', enabled: true, direction: 'higher', minDelta: 30 },
      ],
      CTX,
    ).toLowerCase();
    for (const bad of ['never watched', 'unwatched', 'not watched', 'not played']) expect(text).not.toContain(bad);
  });

  it('validates the minimum difference (at most 3650 days)', () => {
    const draft = (minDelta: number): ProfileDraft => ({
      name: 'P',
      isDefault: false,
      keepCount: 1,
      keepPer: '',
      protections: [],
      criteria: [{ type: 'last_played', enabled: true, direction: 'higher', minDelta }],
    });
    expect(validateProfileDraft(draft(3650), SMAP).criterion.last_played).toBeUndefined();
    // Created through the API without a direction: nothing to choose in the editor, nothing to fix.
    const noDirection = draft(30);
    delete noDirection.criteria[0]!.direction;
    expect(validateProfileDraft(noDirection, SMAP).criterion.last_played).toBeUndefined();
    expect(validateProfileDraft(draft(3651), SMAP).criterion.last_played).toEqual([
      'The minimum difference can be at most 3650 days',
    ]);
  });
});
