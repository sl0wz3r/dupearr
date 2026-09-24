import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { Library, Profile, Protection } from '@/api/types';
import { KNOWN_PROTECTION_TYPES, schemaMap } from './criteria';
import { ProfileCard } from './ProfileCards';
import { ProtectionsEditor, pathGlobHint } from './ProtectionsEditor';
import { TEST_SCHEMA } from './test-utils';

function Harness({ initial, onChange }: { initial: Protection[]; onChange?: (p: Protection[]) => void }) {
  const [protections, setProtections] = useState(initial);
  return (
    <ProtectionsEditor
      protections={protections}
      types={KNOWN_PROTECTION_TYPES}
      libraries={[{ id: 4, title: 'Movies 4K' } as Library]}
      onChange={(next) => {
        setProtections(next);
        onChange?.(next);
      }}
    />
  );
}

describe('pathGlobHint', () => {
  it.each([
    ['', null],
    ['Keep', /only files named exactly “Keep”/],
    ['  Criterion  ', /only files named exactly “Criterion”/],
    ['*Remux*', null],
    ['keep?.mkv', null],
    ['Keep/**', null],
    ['/data/media/movies/Keep', null],
    ['C:\\Media\\Keep', null],
    ['[Kk]eep', null],
  ] as const)('%j', (value, expected) => {
    const got = pathGlobHint(value);
    if (expected === null) expect(got).toBeNull();
    else expect(got).toMatch(expected);
  });
});

describe('<ProtectionsEditor>', () => {
  it('adds an arr_tag protection with the default keep tag', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={[]} onChange={onChange} />);
    await user.click(screen.getByRole('button', { name: 'Add Protection' }));
    expect(onChange).toHaveBeenLastCalledWith([{ type: 'arr_tag', value: 'dupearr-keep' }]);
  });

  it('explains the engine glob semantics and flags a bare folder name that would protect nothing', async () => {
    const user = userEvent.setup();
    render(<Harness initial={[{ type: 'path_glob', value: '' }]} />);
    expect(screen.getByText(/Without a “\/” it matches file names only/)).toBeInTheDocument();

    const value = screen.getByRole('textbox', { name: 'Protection 1 value' });
    await user.type(value, 'Keep');
    expect(screen.getByText(/Matches only files named exactly “Keep”/)).toBeInTheDocument();
    await user.type(value, '/**');
    expect(screen.queryByText(/Matches only files named exactly/)).not.toBeInTheDocument();
  });

  it('resets the value to the type default when the type changes', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={[{ type: 'path_glob', value: '/data/keep/**' }]} onChange={onChange} />);
    await user.selectOptions(screen.getByRole('combobox', { name: 'Protection 1 type' }), 'arr_tag');
    expect(onChange).toHaveBeenLastCalledWith([{ type: 'arr_tag', value: 'dupearr-keep' }]);
    await user.selectOptions(screen.getByRole('combobox', { name: 'Protection 1 type' }), 'library');
    expect(onChange).toHaveBeenLastCalledWith([{ type: 'library', value: '' }]);
  });
});

describe('<ProfileCard>', () => {
  it('renders repeated criterion labels without duplicate React keys', () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {});
    const profile = {
      id: 3,
      name: 'Patterns',
      isDefault: false,
      criteria: [
        { type: 'filename_score', enabled: true, patterns: [{ pattern: 'a', score: 1, regex: false, caseSensitive: false }] },
        { type: 'filename_score', enabled: true, patterns: [{ pattern: 'b', score: 1, regex: false, caseSensitive: false }] },
      ],
      keepCount: 1,
      keepPer: '',
      protections: [],
      createdAt: '',
      updatedAt: '',
    } as Profile;
    render(<ProfileCard profile={profile} schema={schemaMap(TEST_SCHEMA)} onEdit={() => {}} />);
    expect(screen.getByText('1. Filename Score')).toBeInTheDocument();
    expect(screen.getByText('2. Filename Score')).toBeInTheDocument();
    expect(error.mock.calls.flat().join(' ')).not.toMatch(/same key/);
    error.mockRestore();
  });
});
