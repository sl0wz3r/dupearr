import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { moveItem, OrderedListEditor } from './OrderedListEditor';

const OPTIONS = [
  { value: '2160', label: '2160p (4K)' },
  { value: '1080', label: '1080p' },
  { value: '720', label: '720p' },
  { value: 'sd', label: 'SD' },
];

/** Stateful harness so the component behaves like it does in a form. */
function Harness({ initial, onChange }: { initial: string[]; onChange?: (v: string[]) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <OrderedListEditor
      aria-label="Resolution order"
      value={value}
      options={OPTIONS}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

function order(): string[] {
  const list = screen.getByRole('list', { name: 'Resolution order' });
  return within(list)
    .getAllByRole('listitem')
    .map((li) => li.getAttribute('data-value') ?? '');
}

describe('moveItem', () => {
  it('moves items up and down and ignores out-of-range moves', () => {
    expect(moveItem(['a', 'b', 'c'], 2, -1)).toEqual(['a', 'c', 'b']);
    expect(moveItem(['a', 'b', 'c'], 0, 1)).toEqual(['b', 'a', 'c']);
    expect(moveItem(['a', 'b', 'c'], 0, -1)).toEqual(['a', 'b', 'c']);
    expect(moveItem(['a', 'b', 'c'], 2, 1)).toEqual(['a', 'b', 'c']);
  });

  it('does not mutate its input', () => {
    const input = ['a', 'b'];
    moveItem(input, 0, 1);
    expect(input).toEqual(['a', 'b']);
  });
});

describe('<OrderedListEditor>', () => {
  it('renders items in order with their labels and rank', () => {
    render(<Harness initial={['1080', '2160']} />);
    expect(order()).toEqual(['1080', '2160']);
    const items = screen.getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('1');
    expect(items[0]).toHaveTextContent('1080p');
    expect(items[1]).toHaveTextContent('2160p (4K)');
  });

  it('moves an item up and down', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={['2160', '1080', '720']} onChange={onChange} />);

    await user.click(screen.getByRole('button', { name: 'Move 720p up' }));
    expect(order()).toEqual(['2160', '720', '1080']);
    expect(onChange).toHaveBeenLastCalledWith(['2160', '720', '1080']);

    await user.click(screen.getByRole('button', { name: 'Move 2160p (4K) down' }));
    expect(order()).toEqual(['720', '2160', '1080']);
  });

  it('disables moving the first item up and the last item down', () => {
    render(<Harness initial={['2160', '1080']} />);
    expect(screen.getByRole('button', { name: 'Move 2160p (4K) up' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move 1080p down' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move 2160p (4K) down' })).toBeEnabled();
  });

  it('removes items and adds remaining options', async () => {
    const user = userEvent.setup();
    render(<Harness initial={['2160', '1080']} />);

    await user.click(screen.getByRole('button', { name: 'Remove 2160p (4K)' }));
    expect(order()).toEqual(['1080']);

    const select = screen.getByRole('combobox', { name: 'Add…' });
    // Only values not already in the list are offered.
    const offered = within(select)
      .getAllByRole('option')
      .map((o) => (o as HTMLOptionElement).value)
      .filter(Boolean);
    expect(offered).toEqual(['2160', '720', 'sd']);

    await user.selectOptions(select, 'sd');
    await user.click(screen.getByRole('button', { name: 'Add' }));
    expect(order()).toEqual(['1080', 'sd']);
  });

  it('respects minItems', () => {
    render(
      <OrderedListEditor value={['2160']} options={OPTIONS} minItems={1} onChange={() => {}} aria-label="x" />,
    );
    expect(screen.getByRole('button', { name: 'Remove 2160p (4K)' })).toBeDisabled();
  });
});
