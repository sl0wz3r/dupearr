import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { ArrInstance, Criterion, Library } from '@/api/types';
import { CriteriaBuilder, criterionRowKeys } from './CriteriaBuilder';
import { schemaMap } from './criteria';
import { ProfileExplanation } from './ProfileExplanation';
import { TEST_SCHEMA } from './test-utils';

const SMAP = schemaMap(TEST_SCHEMA);
const LIBS = [
  { id: 1, title: 'Movies' },
  { id: 2, title: 'Movies 4K' },
] as Library[];
const ARR = [
  { id: 7, name: 'Radarr 4K', kind: 'radarr' },
  { id: 8, name: 'Radarr', kind: 'radarr' },
] as ArrInstance[];

const DEFAULT_CHAIN: Criterion[] = [
  { type: 'health', enabled: true },
  { type: 'resolution', enabled: true, order: ['2160', '1440', '1080', '720', '576', '480', 'sd'] },
  { type: 'date_added', enabled: true, direction: 'higher' },
];

/** Stateful harness: builder + live explanation, like the profile editor. */
function Harness({
  initial,
  onChange,
  expanded = true,
}: {
  initial: Criterion[];
  onChange?: (c: Criterion[]) => void;
  expanded?: boolean;
}) {
  const [criteria, setCriteria] = useState(initial);
  return (
    <>
      <ProfileExplanation
        draft={{ criteria, keepCount: 1, keepPer: '', protections: [] }}
        schema={SMAP}
        libraries={LIBS}
        arrInstances={ARR}
      />
      <CriteriaBuilder
        criteria={criteria}
        schema={SMAP}
        libraries={LIBS}
        arrInstances={ARR}
        initiallyExpanded={expanded}
        onChange={(next) => {
          setCriteria(next);
          onChange?.(next);
        }}
      />
    </>
  );
}

function rowTypes(): string[] {
  const list = screen.getByRole('list', { name: 'Criteria' });
  return Array.from(list.children).map((li) => li.getAttribute('data-type') ?? '');
}

function row(label: string): HTMLElement {
  return screen.getByRole('listitem', { name: label });
}

function explanation(): string {
  const region = screen.getByRole('region', { name: 'How this profile decides' });
  return region.querySelector('[data-part="criteria"]')?.textContent ?? '';
}

describe('<CriteriaBuilder>', () => {
  it('renders rows in order with labels, descriptions and a live explanation', () => {
    render(<Harness initial={DEFAULT_CHAIN} />);
    expect(rowTypes()).toEqual(['health', 'resolution', 'date_added']);
    expect(within(row('Resolution')).getByText('Video resolution tier (width first).')).toBeInTheDocument();
    expect(explanation()).toBe(
      'Keep the healthiest file; then the highest resolution (2160p > 1440p > 1080p > …); if still tied, prefer the newer file.',
    );
  });

  it('reorders with up/down buttons and disables them at the ends', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={DEFAULT_CHAIN} onChange={onChange} />);

    expect(screen.getByRole('button', { name: 'Move File Health up' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move Date Added down' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Move Date Added up' }));
    expect(rowTypes()).toEqual(['health', 'date_added', 'resolution']);
    expect(onChange).toHaveBeenLastCalledWith([DEFAULT_CHAIN[0], DEFAULT_CHAIN[2], DEFAULT_CHAIN[1]]);
    expect(explanation()).toMatch(/^Keep the healthiest file; then the newer file; if still tied, prefer the highest resolution/);

    await user.click(screen.getByRole('button', { name: 'Move File Health down' }));
    expect(rowTypes()).toEqual(['date_added', 'health', 'resolution']);
    // Health not first → recommendation shown on its row.
    expect(within(row('File Health')).getByText(/Recommended as the first criterion/)).toBeInTheDocument();
  });

  it('enables/disables rows and removes them', async () => {
    const user = userEvent.setup();
    render(<Harness initial={DEFAULT_CHAIN} />);

    await user.click(screen.getByRole('switch', { name: 'Enable Resolution' }));
    expect(screen.getByRole('switch', { name: 'Enable Resolution' })).toHaveAttribute('aria-checked', 'false');
    expect(explanation()).toBe('Keep the healthiest file; if still tied, prefer the newer file.');

    await user.click(screen.getByRole('button', { name: 'Remove Date Added' }));
    expect(rowTypes()).toEqual(['health', 'resolution']);
    expect(explanation()).toBe('Keep the healthiest file.');
  });

  it('adds only unused criteria, putting health first', async () => {
    const user = userEvent.setup();
    render(<Harness initial={DEFAULT_CHAIN.slice(1)} />);

    const picker = screen.getByRole('combobox', { name: 'Add criterion' });
    const offered = within(picker)
      .getAllByRole('option')
      .map((o) => (o as HTMLOptionElement).value)
      .filter(Boolean);
    expect(offered).not.toContain('resolution');
    expect(offered).not.toContain('date_added');
    expect(offered).toContain('health');
    // Criteria needing *arr data are marked in the picker.
    expect(within(picker).getByRole('option', { name: 'Custom Format Score (requires *arr)' })).toBeInTheDocument();

    const add = screen.getByRole('button', { name: 'Add Criterion' });
    expect(add).toBeDisabled();
    await user.selectOptions(picker, 'health');
    await user.click(add);
    expect(rowTypes()).toEqual(['health', 'resolution', 'date_added']);

    await user.selectOptions(screen.getByRole('combobox', { name: 'Add criterion' }), 'file_size');
    await user.click(screen.getByRole('button', { name: 'Add Criterion' }));
    expect(rowTypes()).toEqual(['health', 'resolution', 'date_added', 'file_size']);
    // Defaults for file size: larger wins with a 5% tolerance.
    expect(explanation()).toMatch(/if still tied, prefer the larger file \(differences under 5% count as a tie\)\.$/);
  });

  it('collapses rows to a one-line summary and expands them on demand', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        expanded={false}
        initial={[...DEFAULT_CHAIN, { type: 'file_size', enabled: true, direction: 'lower', tolerancePercent: 5 }]}
      />,
    );
    const resolution = row('Resolution');
    expect(within(resolution).queryByRole('list', { name: 'Resolution order' })).not.toBeInTheDocument();
    expect(within(resolution).getByText('2160p > 1440p > 1080p > 720p > …')).toBeInTheDocument();
    expect(within(row('File Size')).getByText('Smaller · 5% tolerance')).toBeInTheDocument();
    // Health has no parameters → nothing to expand.
    expect(within(row('File Health')).queryByRole('button', { name: /File Health options/ })).not.toBeInTheDocument();

    const toggle = within(resolution).getByRole('button', { name: 'Show Resolution options' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    await user.click(toggle);
    expect(within(resolution).getByRole('list', { name: 'Resolution order' })).toBeInTheDocument();
    expect(within(resolution).getByRole('button', { name: 'Hide Resolution options' })).toHaveAttribute('aria-expanded', 'true');

    await user.click(screen.getByRole('button', { name: 'Expand all' }));
    expect(within(row('Date Added')).getByLabelText('Prefer')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Collapse all' }));
    expect(within(row('Date Added')).queryByLabelText('Prefer')).not.toBeInTheDocument();

    // Newly added criteria open expanded.
    await user.selectOptions(screen.getByRole('combobox', { name: 'Add criterion' }), 'arr_managed');
    await user.click(screen.getByRole('button', { name: 'Add Criterion' }));
    expect(within(row('Managed by *arr')).getByLabelText('Tracked by')).toBeInTheDocument();
  });

  it('shows an empty state without criteria', () => {
    render(<Harness initial={[]} />);
    expect(screen.getByText('No criteria yet')).toBeInTheDocument();
    expect(explanation()).toMatch(/No criteria are enabled/);
  });

  describe('ordered editor', () => {
    it('reorders values and resets to the default order', async () => {
      const user = userEvent.setup();
      render(<Harness initial={DEFAULT_CHAIN} />);
      const r = row('Resolution');
      const reset = within(r).getByRole('button', { name: 'Reset to default order' });
      expect(reset).toBeDisabled();

      await user.click(within(r).getByRole('button', { name: 'Move 1080p up' }));
      await user.click(within(r).getByRole('button', { name: 'Move 1080p up' }));
      const order = within(within(r).getByRole('list', { name: 'Resolution order' }))
        .getAllByRole('listitem')
        .map((li) => li.getAttribute('data-value'));
      expect(order.slice(0, 3)).toEqual(['1080', '2160', '1440']);
      // No longer the natural order → "preferred", not "highest".
      expect(explanation()).toMatch(/then the preferred resolution \(1080p > 2160p > 1440p > …\)/);

      expect(reset).toBeEnabled();
      await user.click(reset);
      expect(explanation()).toMatch(/then the highest resolution \(2160p > 1440p > 1080p > …\)/);
    });

    it('warns that values removed from the list rank last instead of being ignored', async () => {
      const user = userEvent.setup();
      render(<Harness initial={DEFAULT_CHAIN} />);
      const r = row('Resolution');
      expect(r.querySelector('[data-part="unlisted"]')).toBeNull();
      await user.click(within(r).getByRole('button', { name: 'Remove 2160p (4K)' }));
      expect(r.querySelector('[data-part="unlisted"]')?.textContent).toBe('Not listed, so ranked last: 2160p (4K).');
      await user.selectOptions(within(r).getByRole('combobox', { name: 'Add resolution value…' }), '2160');
      await user.click(within(r).getByRole('button', { name: 'Add' }));
      expect(r.querySelector('[data-part="unlisted"]')).toBeNull();
    });

    it('offers the full-disc source and the M2TS container (server schema or built-in fallback)', async () => {
      const user = userEvent.setup();
      render(
        <Harness
          initial={[
            { type: 'source', enabled: true, order: ['remux', 'bluray', 'webdl'] },
            { type: 'container', enabled: true, order: ['mkv', 'mp4'] },
          ]}
        />,
      );
      const source = row('Source');
      const addSource = within(source).getByRole('combobox', { name: 'Add source value…' });
      expect(within(addSource).getByRole('option', { name: 'Full disc (BDMV/VIDEO_TS/ISO)' })).toHaveValue('disc');
      await user.selectOptions(addSource, 'disc');
      await user.click(within(source).getByRole('button', { name: 'Add' }));
      await user.click(within(source).getByRole('button', { name: 'Move Full disc (BDMV/VIDEO_TS/ISO) up' }));
      await user.click(within(source).getByRole('button', { name: 'Move Full disc (BDMV/VIDEO_TS/ISO) up' }));
      const order = within(within(source).getByRole('list', { name: 'Source order' }))
        .getAllByRole('listitem')
        .map((li) => li.getAttribute('data-value'));
      expect(order).toEqual(['remux', 'disc', 'bluray', 'webdl']);
      expect(explanation()).toMatch(/Keep the preferred source \(Remux > Full disc > Blu-ray > …\)/);

      const container = row('Container');
      const addContainer = within(container).getByRole('combobox', { name: 'Add container value…' });
      expect(within(addContainer).getByRole('option', { name: 'M2TS' })).toHaveValue('m2ts');
    });

    it('ranks libraries by title', async () => {
      const user = userEvent.setup();
      render(<Harness initial={[{ type: 'library', enabled: true, order: ['2'] }]} />);
      const r = row('Library');
      await user.selectOptions(within(r).getByRole('combobox', { name: 'Add library value…' }), '1');
      await user.click(within(r).getByRole('button', { name: 'Add' }));
      expect(explanation()).toBe('Keep the preferred library (Movies 4K > Movies).');
    });
  });

  describe('numeric editor', () => {
    it('uses type-specific direction wording', async () => {
      const user = userEvent.setup();
      render(<Harness initial={DEFAULT_CHAIN} />);
      const r = row('Date Added');
      const prefer = within(r).getByLabelText('Prefer');
      expect(within(prefer).getAllByRole('option').map((o) => o.textContent)).toEqual(['Newer', 'Older']);
      await user.selectOptions(prefer, 'lower');
      expect(explanation()).toMatch(/if still tied, prefer the older file\.$/);
      // date_added has no tolerance in the schema.
      expect(within(r).queryByLabelText('Tolerance')).not.toBeInTheDocument();
    });

    it('edits tolerance and min delta, and badges criteria that need an *arr', async () => {
      const user = userEvent.setup();
      render(
        <Harness
          initial={[
            { type: 'file_size', enabled: true, direction: 'higher' },
            { type: 'custom_format_score', enabled: true, direction: 'higher', minDelta: 10 },
          ]}
        />,
      );
      const size = row('File Size');
      expect(within(size).getAllByRole('option').map((o) => o.textContent)).toEqual(['Larger', 'Smaller']);
      const tol = within(size).getByLabelText('Tolerance');
      await user.type(tol, '7.5');
      expect(explanation()).toMatch(/^Keep the larger file \(differences under 7\.5% count as a tie\)/);

      const cf = row('Custom Format Score');
      expect(within(cf).getByText('requires *arr')).toBeInTheDocument();
      expect(within(size).queryByText('requires *arr')).not.toBeInTheDocument();
      const delta = within(cf).getByLabelText('Min delta');
      await user.clear(delta);
      await user.type(delta, '25');
      expect(explanation()).toMatch(/custom format score \(same \*arr instance only; differences under 25 points count as a tie\)/);
    });
  });

  describe('boolean editors', () => {
    it('picks an optional *arr instance for arr_managed', async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      render(<Harness initial={[{ type: 'arr_managed', enabled: true }]} onChange={onChange} />);
      const select = within(row('Managed by *arr')).getByLabelText('Tracked by');
      expect(within(select).getAllByRole('option').map((o) => o.textContent)).toEqual(['Any *arr', 'Radarr 4K', 'Radarr']);
      expect(explanation()).toBe('Keep a file tracked by Radarr/Sonarr.');

      await user.selectOptions(select, '7');
      expect(onChange).toHaveBeenLastCalledWith([{ type: 'arr_managed', enabled: true, value: '7' }]);
      expect(explanation()).toBe('Keep a file tracked by Radarr 4K.');

      await user.selectOptions(select, '');
      expect(onChange).toHaveBeenLastCalledWith([{ type: 'arr_managed', enabled: true, value: undefined }]);
    });

    it('picks a common audio language or a custom code', async () => {
      const user = userEvent.setup();
      render(<Harness initial={[{ type: 'audio_language', enabled: true }]} />);
      const r = row('Audio Language');
      await user.selectOptions(within(r).getByLabelText('Language'), 'jpn');
      expect(explanation()).toBe('Keep a file with a Japanese audio track.');

      await user.selectOptions(within(r).getByLabelText('Language'), '__other__');
      const code = within(r).getByLabelText('Language code');
      expect(code).toHaveValue('');
      await user.type(code, 'q1');
      expect(within(r).getByText('Use a 2- or 3-letter code')).toBeInTheDocument();
      await user.clear(code);
      await user.type(code, 'TLH');
      expect(code).toHaveValue('tlh');
      expect(within(r).queryByText('Use a 2- or 3-letter code')).not.toBeInTheDocument();
      expect(explanation()).toBe('Keep a file with a tlh audio track.');
    });
  });

  describe('patterns editor', () => {
    it('adds, validates and removes patterns', async () => {
      const user = userEvent.setup();
      const onChange = vi.fn();
      render(<Harness initial={[{ type: 'filename_score', enabled: true, patterns: [] }]} onChange={onChange} />);
      const r = row('Filename Score');

      await user.click(within(r).getByRole('button', { name: 'Add Pattern' }));
      const pattern = within(r).getByRole('textbox', { name: 'Pattern 1' });
      // A fresh empty row is not flagged before a save attempt.
      expect(within(r).queryByRole('alert')).not.toBeInTheDocument();

      await user.type(pattern, '**/[[Remux'); // "[[" types a literal "["
      expect(within(r).getByText('Unclosed "[" in pattern')).toBeInTheDocument();
      await user.clear(pattern);
      await user.type(pattern, '**/*Remux*');
      expect(within(r).queryByRole('alert')).not.toBeInTheDocument();
      expect(explanation()).toBe('Keep the highest filename score (+10 **/*Remux*).');

      // Regex mode validates as Go RE2.
      await user.click(within(r).getByRole('checkbox', { name: 'Regex for pattern 1' }));
      expect(within(r).getByText(/^Invalid regular expression/)).toBeInTheDocument();
      await user.clear(pattern);
      await user.type(pattern, 'remux(?!ed)');
      expect(within(r).getByText(/Look-around/)).toBeInTheDocument();
      await user.clear(pattern);
      await user.type(pattern, '(?i)remux');
      expect(within(r).queryByRole('alert')).not.toBeInTheDocument();

      await user.click(within(r).getByRole('checkbox', { name: 'Case-sensitive pattern 1' }));
      const score = within(r).getByRole('spinbutton', { name: 'Score for pattern 1' });
      await user.clear(score);
      await user.type(score, '-5');
      expect(onChange).toHaveBeenLastCalledWith([
        {
          type: 'filename_score',
          enabled: true,
          patterns: [{ pattern: '(?i)remux', score: -5, regex: true, caseSensitive: true }],
        },
      ]);
      expect(explanation()).toBe('Keep the highest filename score (−5 (?i)remux).');

      await user.click(within(r).getByRole('button', { name: 'Remove pattern 1' }));
      expect(within(r).queryByRole('textbox', { name: 'Pattern 1' })).not.toBeInTheDocument();
      expect(explanation()).toBe('Keep the highest filename score (no patterns yet).');
    });
  });

  it('shows server errors on the matching row', () => {
    render(
      <CriteriaBuilder
        criteria={DEFAULT_CHAIN}
        schema={SMAP}
        onChange={() => {}}
        errors={{ resolution: ['Order must not be empty'] }}
        listErrors={['Too many criteria']}
      />,
    );
    expect(within(row('Resolution')).getByText('Order must not be empty')).toBeInTheDocument();
    expect(within(row('File Health')).queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByText('Too many criteria')).toBeInTheDocument();
  });

  it('keeps unique row keys for a repeated filename_score step', async () => {
    expect(
      criterionRowKeys([
        { type: 'filename_score', enabled: true },
        { type: 'health', enabled: true },
        { type: 'filename_score', enabled: true },
      ]),
    ).toEqual(['filename_score', 'health', 'filename_score#2']);

    const user = userEvent.setup();
    const onChange = vi.fn();
    const error = vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <Harness
        initial={[
          { type: 'filename_score', enabled: true, patterns: [{ pattern: 'a', score: 1, regex: false, caseSensitive: false }] },
          { type: 'filename_score', enabled: true, patterns: [{ pattern: 'b', score: 2, regex: false, caseSensitive: false }] },
        ]}
        onChange={onChange}
      />,
    );
    const rows = within(screen.getByRole('list', { name: 'Criteria' })).getAllByRole('listitem');
    expect(rows).toHaveLength(2);
    await user.click(within(rows[1]!).getByRole('button', { name: 'Move Filename Score up' }));
    expect(onChange.mock.calls.at(-1)?.[0].map((c: Criterion) => c.patterns?.[0]?.pattern)).toEqual(['b', 'a']);
    // No duplicate-key warnings from React.
    expect(error.mock.calls.flat().join(' ')).not.toMatch(/same key/);
    error.mockRestore();
  });
});

describe('<CriteriaBuilder> health guard', () => {
  it.each([
    ['missing', [{ type: 'resolution', enabled: true, order: ['2160'] }] as Criterion[]],
    ['disabled', [{ type: 'health', enabled: false }, { type: 'resolution', enabled: true, order: ['2160'] }] as Criterion[]],
  ])('warns when File Health is %s', (_name, criteria) => {
    render(<CriteriaBuilder criteria={criteria} schema={SMAP} onChange={() => {}} />);
    expect(screen.getByText('File Health is not active')).toBeInTheDocument();
  });

  it('does not warn when File Health is enabled, or when the server has no health criterion', async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Harness initial={[{ type: 'resolution', enabled: true, order: ['2160'] }]} />);
    expect(screen.getByText('File Health is not active')).toBeInTheDocument();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Add criterion' }), 'health');
    await user.click(screen.getByRole('button', { name: 'Add Criterion' }));
    expect(screen.queryByText('File Health is not active')).not.toBeInTheDocument();
    expect(rowTypes()[0]).toBe('health');
    unmount();

    const noHealth = schemaMap({ ...TEST_SCHEMA, criteria: TEST_SCHEMA.criteria.filter((c) => c.type !== 'health') });
    render(<CriteriaBuilder criteria={[{ type: 'resolution', enabled: true, order: ['2160'] }]} schema={noHealth} onChange={() => {}} />);
    expect(screen.queryByText('File Health is not active')).not.toBeInTheDocument();
  });
});

describe('<PatternsEditor> help', () => {
  it('documents the engine glob semantics (file name without "/", full path with "/")', () => {
    render(<CriteriaBuilder criteria={[{ type: 'filename_score', enabled: true, patterns: [] }]} schema={SMAP} onChange={() => {}} initiallyExpanded />);
    const r = row('Filename Score');
    expect(r.textContent).toMatch(/A glob without a \/ matches the\s+file name/);
    expect(r.textContent).toMatch(/Regular expressions \(Go RE2 syntax\) match the full\s+path/);
  });
});

// docs/DECISIONS.md D10 (issue #5): play-history criteria in the builder.
describe('<CriteriaBuilder> play history', () => {
  it('badges Played and Last played as needing Tautulli, also in the add picker', () => {
    render(<Harness initial={[{ type: 'health', enabled: true }]} />);
    const picker = screen.getByRole('combobox', { name: 'Add criterion' });
    expect(within(picker).getByRole('option', { name: 'Played (requires Tautulli)' })).toBeInTheDocument();
    expect(within(picker).getByRole('option', { name: 'Last played (requires Tautulli)' })).toBeInTheDocument();
  });

  it('shows Last played without a direction and with a minimum difference in days', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <Harness
        initial={[
          { type: 'played', enabled: true },
          { type: 'last_played', enabled: true, direction: 'higher', minDelta: 30 },
        ]}
        onChange={onChange}
      />,
    );
    const played = row('Played');
    expect(within(played).getByText('requires Tautulli')).toBeInTheDocument();
    expect(within(played).queryByRole('button', { name: /options/ })).not.toBeInTheDocument();
    const last = row('Last played');
    expect(within(last).getByText('requires Tautulli')).toBeInTheDocument();
    expect(within(last).queryByLabelText('Prefer')).not.toBeInTheDocument();
    const days = within(last).getByLabelText('Minimum difference (days)');
    await user.clear(days);
    await user.type(days, '45');
    expect(onChange).toHaveBeenLastCalledWith([
      { type: 'played', enabled: true },
      { type: 'last_played', enabled: true, direction: 'higher', minDelta: 45 },
    ]);
    expect(explanation()).toMatch(
      /^Keep a file that has been played \(Tautulli; an unknown play history is a tie\); if still tied, prefer the most recently played file \(plays less than 45 days apart count as a tie\)/,
    );
    expect(within(last).getByText(/never counts as “not played”/)).toBeInTheDocument();
  });
});
