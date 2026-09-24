import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { GroupFile, WatchInfo } from '@/api/types';
import { ComparisonTable } from './ComparisonTable';
import { criterionScores } from './comparison';
import { makeFile } from './testing';

// docs/DECISIONS.md D10 (issue #5): the "Plays" row. An unknown or unreadable history is shown as
// unknown with its reason, never as "not watched", and highlights nothing.

const known = (plays: number, users: number, lastPlayed?: string): WatchInfo => ({
  source: 'tautulli',
  sourceName: 'Tautulli',
  status: 'known',
  plays,
  users,
  lastPlayed,
  since: '2025-03-01T20:00:00Z',
});
const unknown = (reason: string): WatchInfo => ({ source: 'tautulli', sourceName: 'Tautulli', status: 'unknown', reason, plays: 0, users: 0 });
const failed = (reason: string): WatchInfo => ({ source: 'tautulli', sourceName: 'Tautulli', status: 'failed', reason, plays: 0, users: 0 });

function file(id: number, ratingKey: string, watch: WatchInfo | null | undefined, over: Partial<GroupFile> = {}): GroupFile {
  return makeFile({
    id,
    rank: id,
    decision: id === 1 ? 'keep' : 'remove',
    values: {
      played: 'engine value',
      last_played: 'engine value',
    },
    ...over,
    version: { key: `plex:1:${id}`, mediaId: id, ratingKey, watch },
  });
}

function renderTable(files: GroupFile[]) {
  render(<ComparisonTable files={files} overrideValue={() => 'auto'} onOverride={() => {}} />);
  const table = screen.getByRole('table', { name: 'Copy comparison' });
  const row = table.querySelector<HTMLElement>('tr[data-row="plays"]');
  return { table, row };
}

function cells(row: HTMLElement | null): HTMLElement[] {
  return row ? Array.from(row.querySelectorAll<HTMLElement>('td')) : [];
}

describe('<ComparisonTable> Plays row', () => {
  it('renders every state with its wording and highlights only a comparable winner', () => {
    const { table, row } = renderTable([
      file(1, '10', known(3, 2, '2026-09-20T21:00:00Z'), { decidingCriterion: '' }),
      file(2, '20', known(0, 0), { decidingCriterion: 'played' }),
    ]);
    expect(row).not.toBeNull();
    const [a, b] = cells(row);
    expect(a).toHaveTextContent('3 plays · 2 users');
    expect(a).toHaveTextContent(/last played/);
    expect(b).toHaveTextContent('No plays recorded');
    // The UTC day, like the engine's reason ("no plays recorded since 2025-03-01"), in every zone.
    expect(b).toHaveTextContent('since Mar 1 2025');
    expect(a).toHaveAttribute('data-best', 'true');
    expect(b).not.toHaveAttribute('data-best');
    expect(row).toHaveAttribute('data-deciding', 'true');
    // The Plays row covers both criteria: no generic "Played" / "Last Played" rows.
    expect(table.querySelector('tr[data-row="criterion-played"]')).toBeNull();
    expect(table.querySelector('tr[data-row="criterion-last_played"]')).toBeNull();
  });

  it('shows unknown, unreadable and missing histories as unknown with the reason, without a highlight', () => {
    const { row } = renderTable([
      file(1, '10', known(3, 2, '2026-09-20T21:00:00Z')),
      file(2, '20', unknown('added before the recorded history starts (2025-03-01)')),
      file(3, '30', failed('request failed: GET /api/v2 (get_history) returned HTTP 503')),
      file(4, '40', null),
    ]);
    const [, b, c, d] = cells(row);
    expect(b).toHaveTextContent('Unknown');
    expect(b).toHaveTextContent('added before the recorded history starts');
    expect(c).toHaveTextContent('Unknown');
    expect(c).toHaveTextContent('Tautulli could not be read: request failed');
    expect(d).toHaveTextContent('Unknown');
    expect(d).toHaveTextContent('No watch-history source');
    for (const cell of cells(row)) {
      expect(cell).not.toHaveAttribute('data-best');
      expect(cell.textContent?.toLowerCase()).not.toMatch(/not watched|unwatched|never watched|not played/);
    }
  });

  it('notes that versions of one Plex item share their plays, and highlights nothing', () => {
    const shared = known(2, 1, '2026-09-19T21:00:00Z');
    const { row } = renderTable([file(1, '10', shared), file(2, '10', shared)]);
    for (const cell of cells(row)) {
      expect(cell).toHaveTextContent('Same Plex item: plays are shared by its versions');
      expect(cell).not.toHaveAttribute('data-best');
    }
  });

  it('is hidden without any play history, and adds no generic rows for the new values', () => {
    const { table, row } = renderTable([file(1, '10', undefined), file(2, '20', undefined)]);
    expect(row).toBeNull();
    expect(table.querySelector('tr[data-row="criterion-played"]')).toBeNull();
    expect(table.querySelector('tr[data-row="criterion-last_played"]')).toBeNull();
  });

  it('is shown when a play-history criterion decided even without data (older servers)', () => {
    const { row } = renderTable([file(1, '10', undefined), file(2, '20', undefined, { decidingCriterion: 'last_played' })]);
    expect(row).not.toBeNull();
  });
});

describe('criterionScores (play history)', () => {
  it('scores played and last played for known histories only', () => {
    const files = [
      file(1, '10', known(3, 2, '2026-09-20T21:00:00Z')),
      file(2, '20', known(0, 0)),
      file(3, '30', known(1, 1, '2026-01-02T21:00:00Z')),
    ];
    expect(criterionScores('played', files)).toEqual([1, 0, 1]);
    expect(criterionScores('last_played', files)).toEqual([Date.parse('2026-09-20T21:00:00Z'), 0, Date.parse('2026-01-02T21:00:00Z')]);
  });

  it('ties a copy without plays with plays from before its date added', () => {
    const fresh = { ...known(0, 0), since: '2026-09-01T00:00:00Z' };
    const old = [file(1, '10', known(3, 2, '2026-08-20T21:00:00Z')), file(2, '20', fresh)];
    expect(criterionScores('played', old)).toEqual([null, null]);
    expect(criterionScores('last_played', old)).toEqual([null, null]);
    const later = [file(1, '10', known(3, 2, '2026-09-20T21:00:00Z')), file(2, '20', fresh)];
    expect(criterionScores('played', later)).toEqual([1, 0]);
  });

  it('never scores when a history is unknown, failed or missing, or all copies are one item', () => {
    for (const other of [unknown('x'), failed('y'), null, undefined]) {
      const files = [file(1, '10', known(3, 2, '2026-09-20T21:00:00Z')), file(2, '20', other)];
      expect(criterionScores('played', files)).toEqual([null, null]);
      expect(criterionScores('last_played', files)).toEqual([null, null]);
    }
    const shared = known(3, 2, '2026-09-20T21:00:00Z');
    expect(criterionScores('played', [file(1, '10', shared), file(2, '10', shared)])).toEqual([null, null]);
  });
});
