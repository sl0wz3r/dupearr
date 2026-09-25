import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { GroupFile, GroupFlag, OtherListing } from '@/api/types';
import { GROUP_FLAG_DESCRIPTIONS, GROUP_FLAG_KIND, GROUP_FLAG_LABELS } from '@/lib/constants';
import { ComparisonTable } from './ComparisonTable';
import { FilterBar, MULTI_SERVER_FLAGS } from './FilterBar';
import { GROUP_FLAG_ICONS } from './FlagIcons';
import type { DuplicateFilters } from './listState';
import { makeFile } from './testing';

// Several Plex servers (docs/DECISIONS.md D11): the UI shows nothing new with one server.

const listing = (over: Partial<OtherListing> = {}): OtherListing => ({
  serverId: 2,
  serverName: 'Plex B',
  libraryId: 20,
  libraryTitle: 'Movies 1080p',
  ratingKey: '900',
  mediaId: 9001,
  versionKey: 'plex:2:9001',
  itemTitle: 'Heat (1995)',
  path: '/movies/Heat.mkv',
  match: 'same_file',
  itemKeepsAnother: null,
  keptByGroup: null,
  ...over,
});

function file(id: number, other?: OtherListing[], over: Partial<GroupFile> = {}): GroupFile {
  return makeFile({
    id,
    rank: id,
    decision: id === 1 ? 'keep' : 'remove',
    ...over,
    version: { key: `plex:1:${id}`, mediaId: id, otherServers: other },
  });
}

function row(): HTMLElement | null {
  return screen.getByRole('table', { name: 'Copy comparison' }).querySelector<HTMLElement>('tr[data-row="other-servers"]');
}

describe('<ComparisonTable> Other servers row', () => {
  it('is absent with one server', () => {
    render(<ComparisonTable files={[file(1), file(2)]} overrideValue={() => 'auto'} onOverride={() => {}} />);
    expect(row()).toBeNull();
  });

  // Backend-shaped data: the engine keeps a copy that is another server's only one (protected,
  // itemKeepsAnother null on kept copies) and reports false only for a removed copy whose listing
  // may be the same file (review).
  it('names the other server, its library and the match, and shows the protection', () => {
    render(
      <ComparisonTable
        files={[
          file(1, [listing({ hint: 'map Plex B’s folders' })], {
            decision: 'keep',
            protected: true,
            protectedReason: 'the only copy of "Heat (1995)" on Plex B (Movies 1080p)',
          }),
          file(2, [listing({ match: 'possibly_same', versionKey: 'plex:2:9002', libraryTitle: 'Movies 4K', itemKeepsAnother: false })]),
          file(3, [listing({ versionKey: 'plex:2:9003', itemKeepsAnother: true })]),
        ]}
        overrideValue={() => 'auto'}
        onOverride={() => {}}
      />,
    );
    const [a, b, c] = Array.from(row()!.querySelectorAll<HTMLElement>('td'));
    expect(a).toHaveTextContent('Also listed by Plex B → Movies 1080p (same file)');
    expect(a).toHaveTextContent('map Plex B’s folders');
    expect(within(a!).queryByText(/only copy there/)).toBeNull();
    expect(screen.getByText('the only copy of "Heat (1995)" on Plex B (Movies 1080p)')).toBeInTheDocument();
    expect(b).toHaveTextContent('Also listed by Plex B → Movies 4K (possibly the same file)');
    const maybe = within(b!).getByText('Maybe its only copy there');
    expect(maybe.closest('[title]')).toHaveAttribute(
      'title',
      'If it is the same file, removing this copy would leave that server without a copy',
    );
    expect(within(c!).getByText('Keeps another copy there')).toBeInTheDocument();
  });
});

describe('<FilterBar> multi-server flags', () => {
  const base: DuplicateFilters = { status: [] };
  const flagOptions = () => {
    const select = screen.getByRole('combobox', { name: /flag/i });
    return Array.from(select.querySelectorAll('option')).map((o) => o.getAttribute('value'));
  };

  it('hides the other_server_* flags with one server', () => {
    render(<FilterBar filters={{ ...base }} onChange={() => {}} onReset={() => {}} serverCount={1} />);
    for (const f of MULTI_SERVER_FLAGS) expect(flagOptions()).not.toContain(f);
    expect(flagOptions()).toContain('same_file');
  });

  it('offers them with two servers, or while one is selected', () => {
    const { unmount } = render(<FilterBar filters={{ ...base }} onChange={() => {}} onReset={() => {}} serverCount={2} />);
    for (const f of MULTI_SERVER_FLAGS) expect(flagOptions()).toContain(f);
    unmount();
    render(<FilterBar filters={{ ...base, flag: 'other_server_keeps' }} onChange={() => {}} onReset={() => {}} serverCount={1} />);
    expect(flagOptions()).toContain('other_server_keeps');
    expect(flagOptions()).not.toContain('other_server_unread');
  });
});

describe('group flags', () => {
  it('every flag has a label, a description, a kind and an icon', () => {
    for (const f of Object.keys(GROUP_FLAG_LABELS) as GroupFlag[]) {
      expect(GROUP_FLAG_DESCRIPTIONS[f], f).toBeTruthy();
      expect(GROUP_FLAG_KIND[f], f).toBeTruthy();
      expect(GROUP_FLAG_ICONS[f], f).toBeTruthy();
    }
    for (const f of MULTI_SERVER_FLAGS) expect(GROUP_FLAG_LABELS[f]).toBeTruthy();
  });
});
