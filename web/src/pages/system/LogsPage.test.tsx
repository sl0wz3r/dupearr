import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import LogsPage from './LogsPage';

const LOG = [
  '2026-09-22 10:00:00.1|Info|Scanner|Scan started',
  '2026-09-22 10:00:01.1|Warn|Plex|Item 123 has no guids',
  '2026-09-22 10:00:02.1|Error|Executor|Delete failed',
  '  at executor.remove',
  '2026-09-22 10:00:03.1|Debug|Scanner|Scan completed',
  '',
].join('\n');

function setup() {
  const mock = mockFetch(({ method, path }) => {
    if (method === 'GET' && path === '/api/v1/log/file') {
      return jsonResponse([
        { filename: 'dupearr.txt', lastWriteTime: '2026-09-22T10:00:03Z', size: 2048 },
        { filename: 'dupearr.0.txt', lastWriteTime: '2026-09-21T10:00:00Z', size: 1_048_576 },
      ]);
    }
    if (method === 'GET' && path === '/api/v1/log/file/dupearr.txt') {
      return new Response(LOG, { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
    }
    return undefined;
  });
  renderWithProviders(<LogsPage />);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<LogsPage>', () => {
  it('lists log files with sizes and credential-free download links (session cookie)', async () => {
    setup();
    expect(await screen.findByRole('button', { name: 'dupearr.txt' })).toBeInTheDocument();
    expect(screen.getByText('1.0 MiB')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download dupearr.0.txt' })).toHaveAttribute(
      'href',
      '/api/v1/log/file/dupearr.0.txt',
    );
  });

  it('views a file with level-coloured lines and filters them by search', async () => {
    const user = userEvent.setup();
    const { requests } = setup();

    await user.click(await screen.findByRole('button', { name: 'View dupearr.txt' }));
    const dialog = await screen.findByRole('dialog', { name: 'dupearr.txt' });
    const log = await within(dialog).findByRole('log');

    await waitFor(() => expect(within(log).getByText(/Delete failed/)).toBeInTheDocument());
    expect(requests.some((r) => r.path === '/api/v1/log/file/dupearr.txt')).toBe(true);
    const levelOf = (text: RegExp) => within(log).getByText(text).closest('[data-level]')?.getAttribute('data-level');
    expect(levelOf(/Scan started/)).toBe('info');
    expect(levelOf(/has no guids/)).toBe('warn');
    expect(levelOf(/Delete failed/)).toBe('error');
    // Continuation (stack trace) lines inherit the level of the entry above.
    expect(levelOf(/at executor\.remove/)).toBe('error');
    expect(within(dialog).getByText('5 lines')).toBeInTheDocument();

    await user.type(within(dialog).getByRole('searchbox', { name: 'Search log' }), 'scanner');
    await waitFor(() => expect(within(log).queryByText(/Delete failed/)).not.toBeInTheDocument());
    expect(within(log).getByText(/started/)).toBeInTheDocument();
    expect(within(log).getByText(/completed/)).toBeInTheDocument();
    expect(within(log).getAllByText('Scanner')).toHaveLength(2); // highlighted matches
    expect(within(dialog).getByText('2 of 5 lines')).toBeInTheDocument();

    await user.clear(within(dialog).getByRole('searchbox', { name: 'Search log' }));
    await user.type(within(dialog).getByRole('searchbox', { name: 'Search log' }), 'no such text');
    expect(await within(log).findByText('No matching lines.')).toBeInTheDocument();

    expect(within(dialog).getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      '/api/v1/log/file/dupearr.txt',
    );
  });
});
