import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { ArrFileInfo, ArrLink, GroupFile } from '@/api/types';
import { ComparisonTable } from './ComparisonTable';
import { makeFile } from './testing';

const ARR: ArrFileInfo = {
  instanceId: 1,
  instanceName: 'Radarr 4K',
  kind: 'radarr',
  fileId: 5,
  itemId: 7,
  episodeIds: null,
  itemPath: '/data/movies/Heat (1995)',
  monitored: true,
  qualityName: 'Remux-2160p',
  qualitySource: 'bluray',
  qualityResolution: 2160,
  qualityModifier: 'remux',
  customFormats: [],
  customFormatScore: 0,
  releaseGroup: '',
  edition: '',
  languages: [],
  dynamicRangeType: '',
  tags: [],
  qualityCutoffNotMet: false,
  sceneName: '',
  dateAdded: '2026-01-01T00:00:00Z',
};

const FILES: GroupFile[] = [
  makeFile({ id: 1, rank: 1, decision: 'keep', version: { key: 'plex:1:1', mediaId: 1, arr: ARR } }),
  makeFile({ id: 2, rank: 2, decision: 'remove', version: { key: 'plex:1:2', mediaId: 2, arr: null } }),
];

function arrRow(links?: ArrLink[] | null) {
  const { unmount } = render(<ComparisonTable files={FILES} overrideValue={() => 'auto'} onOverride={() => {}} arrLinks={links} />);
  const row = screen.getByRole('table', { name: 'Copy comparison' }).querySelector<HTMLElement>('tr[data-row="arr"]');
  if (!row) throw new Error('no *arr row');
  return { row, unmount };
}

describe('<ComparisonTable> *arr row', () => {
  it('links a tracked copy to its movie in Radarr, opening a new tab without a referrer', () => {
    const { row } = arrRow([
      { instanceId: 1, instanceName: 'Radarr 4K', kind: 'radarr', itemId: 7, itemUrl: 'https://radarr.example.com/movie/949', queueUrl: 'https://radarr.example.com/activity/queue' },
    ]);
    const link = within(row).getByRole('link', { name: 'Open in Radarr 4K' });
    expect(link).toHaveAttribute('href', 'https://radarr.example.com/movie/949');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(within(row).getAllByRole('link')).toHaveLength(1);
  });

  it('names the instance as it is called now, not as it was when scanned', () => {
    const { row } = arrRow([
      { instanceId: 1, instanceName: 'Radarr UHD', kind: 'radarr', itemId: 7, itemUrl: 'https://radarr.example.com/movie/949', queueUrl: 'https://radarr.example.com/activity/queue' },
    ]);
    expect(within(row).getByRole('link', { name: 'Open in Radarr UHD' })).toHaveAttribute('href', 'https://radarr.example.com/movie/949');
  });

  it('shows no link without one, for another item, or for an unsafe address', () => {
    for (const links of [
      undefined,
      [],
      [{ instanceId: 1, instanceName: 'Radarr 4K', kind: 'radarr' as const, itemId: 8, itemUrl: 'https://radarr.example.com/movie/1', queueUrl: 'https://radarr.example.com/activity/queue' }],
      [{ instanceId: 1, instanceName: 'Radarr 4K', kind: 'radarr' as const, itemId: 7, queueUrl: 'https://radarr.example.com/activity/queue' }],
      [{ instanceId: 1, instanceName: 'Radarr 4K', kind: 'radarr' as const, itemId: 7, itemUrl: 'javascript:alert(1)', queueUrl: '' }],
    ]) {
      const { row, unmount } = arrRow(links);
      expect(within(row).queryByRole('link')).not.toBeInTheDocument();
      unmount();
    }
  });
});
