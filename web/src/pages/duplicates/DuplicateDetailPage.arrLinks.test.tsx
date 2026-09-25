import { screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ArrFileInfo, DuplicateGroupDetail } from '@/api/types';
import { GiB, LIBRARIES, makeFile, makeGroup, makeSettings, mockApi, renderPage } from '@/components/duplicates/testing';
import DuplicateDetailPage from './DuplicateDetailPage';

afterEach(() => {
  vi.unstubAllGlobals();
});

const ARR: ArrFileInfo = {
  instanceId: 1,
  instanceName: 'Radarr',
  kind: 'radarr',
  fileId: 5,
  itemId: 9,
  episodeIds: null,
  itemPath: '/data/movies/Toy Story 5 (2026)',
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

// Toy Story 5: Radarr downloaded a release it will not import ("Not an upgrade…"), so the group
// stays deferred while the entry is in Radarr's queue.
const GROUP: DuplicateGroupDetail = makeGroup({
  title: 'Toy Story 5',
  year: 2026,
  status: 'deferred',
  statusReason:
    'The *arr has an active download or import for this title (Radarr: "Toy.Story.5.2026.2160p.WEB-DL" Downloaded - Waiting to Import — Not an upgrade for existing movie file)',
  flags: ['arr_queue_busy'],
  files: [
    makeFile({ id: 11, rank: 1, decision: 'keep', engineDecision: 'keep', version: { key: 'plex:1:1', mediaId: 1, arr: ARR } }),
    makeFile({
      id: 12,
      rank: 2,
      decision: 'remove',
      engineDecision: 'remove',
      version: { key: 'plex:1:2', mediaId: 2, parts: [{ id: 2, path: '/data/movies/Toy Story 5 (2026)/ts5.1080p.mkv', localPath: '', size: 5 * GiB, duration: 1 }] },
    }),
  ],
  arrItems: [
    {
      instanceId: 1,
      instanceName: 'Radarr',
      kind: 'radarr',
      itemId: 9,
      titleSlug: '1084244',
      queueCount: 1,
      queue: [
        {
          title: 'Toy.Story.5.2026.2160p.WEB-DL',
          status: 'completed',
          trackedDownloadState: 'importPending',
          trackedDownloadStatus: 'warning',
          label: 'Downloaded - Waiting to Import',
          messages: ['Not an upgrade for existing movie file'],
        },
      ],
    },
  ],
  arrLinks: [
    {
      instanceId: 1,
      instanceName: 'Radarr',
      kind: 'radarr',
      itemId: 9,
      itemUrl: 'http://radarr:7878/movie/1084244',
      queueUrl: 'http://radarr:7878/activity/queue',
    },
  ],
});

describe('<DuplicateDetailPage> *arr links', () => {
  it('explains a queue deferral and links to Radarr from the notice and the comparison', async () => {
    mockApi([
      { path: '/duplicate/1', respond: GROUP },
      { path: '/library', respond: LIBRARIES },
      { path: '/config/settings', respond: makeSettings() },
      { path: '/profile/1', respond: { id: 1, name: 'Keep Highest Quality', isDefault: true, criteria: [], keepCount: 1, keepPer: '', protections: [] } },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    expect(await screen.findByRole('heading', { level: 1, name: 'Toy Story 5' })).toBeInTheDocument();
    // The reason names the entry.
    expect(screen.getAllByText(/Downloaded - Waiting to Import — Not an upgrade for existing movie file/).length).toBeGreaterThan(0);

    const notice = screen.getByRole('alert');
    expect(notice).toHaveTextContent('Deferred by the Radarr/Sonarr download queue');
    expect(notice).toHaveTextContent('Radarr downloaded this release but will not import it automatically.');
    expect(within(notice).getByRole('link', { name: 'Open queue' })).toHaveAttribute('href', 'http://radarr:7878/activity/queue');
    expect(within(notice).getByRole('link', { name: 'Open in Radarr' })).toHaveAttribute('href', 'http://radarr:7878/movie/1084244');

    const arrRow = screen.getByRole('table', { name: 'Copy comparison' }).querySelector<HTMLElement>('tr[data-row="arr"]');
    if (!arrRow) throw new Error('no *arr row');
    const open = within(arrRow).getByRole('link', { name: 'Open in Radarr' });
    expect(open).toHaveAttribute('href', 'http://radarr:7878/movie/1084244');
    expect(open).toHaveAttribute('target', '_blank');
    expect(open).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('shows no notice for a group the queue does not defer', async () => {
    mockApi([
      { path: '/duplicate/1', respond: { ...GROUP, status: 'pending', statusReason: '', flags: [] } },
      { path: '/library', respond: LIBRARIES },
      { path: '/config/settings', respond: makeSettings() },
    ]);
    renderPage(<DuplicateDetailPage />, { route: '/duplicate/1', path: '/duplicate/:id' });
    expect(await screen.findByRole('heading', { level: 1, name: 'Toy Story 5' })).toBeInTheDocument();
    expect(screen.queryByText('Deferred by the Radarr/Sonarr download queue')).not.toBeInTheDocument();
  });
});
