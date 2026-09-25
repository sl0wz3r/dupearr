import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { ArrItemRef, ArrLink } from '@/api/types';
import { ArrQueueNotice } from './ArrQueueNotice';

const STUCK: ArrItemRef = {
  instanceId: 1,
  instanceName: 'Radarr',
  kind: 'radarr',
  itemId: 9,
  titleSlug: '1084244',
  queueCount: 3,
  queue: [
    {
      title: 'Toy.Story.5.2026.2160p.WEB-DL',
      status: 'completed',
      trackedDownloadState: 'importPending',
      trackedDownloadStatus: 'warning',
      label: 'Downloaded - Waiting to Import',
      messages: ['Not an upgrade for existing movie file. Existing quality: Remux-2160p.'],
    },
    { title: 'Toy.Story.5.2026.1080p', status: 'warning', label: 'Download warning', errorMessage: 'The download is stalled' },
  ],
};

const LINKS: ArrLink[] = [
  {
    instanceId: 1,
    instanceName: 'Radarr',
    kind: 'radarr',
    itemId: 9,
    itemUrl: 'https://radarr.example.com/movie/1084244',
    queueUrl: 'https://radarr.example.com/activity/queue',
  },
];

describe('<ArrQueueNotice>', () => {
  it('renders nothing unless the *arr queue defers the group', () => {
    const { container } = render(<ArrQueueNotice group={{ status: 'deferred', flags: ['playing'], arrItems: [STUCK] }} arrLinks={LINKS} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('lists the queue entries with the links and explains a download the *arr will not import', () => {
    render(<ArrQueueNotice group={{ status: 'deferred', flags: ['arr_queue_busy'], arrItems: [STUCK] }} arrLinks={LINKS} />);
    const notice = screen.getByRole('alert');
    expect(notice).toHaveTextContent('Deferred by the Radarr/Sonarr download queue');
    expect(notice).toHaveTextContent('The duplicate is checked again on the next scan after the entry has left the queue.');
    expect(notice).toHaveTextContent('3 queue entries');
    const entries = within(notice).getByRole('list', { name: 'Radarr queue entries' });
    expect(entries).toHaveTextContent('Toy.Story.5.2026.2160p.WEB-DL — Downloaded - Waiting to Import');
    expect(entries).toHaveTextContent('Not an upgrade for existing movie file. Existing quality: Remux-2160p.');
    expect(entries).toHaveTextContent('Toy.Story.5.2026.1080p — Download warning');
    expect(entries).toHaveTextContent('The download is stalled');
    expect(entries).toHaveTextContent('1 more entry');
    expect(notice).toHaveTextContent(
      'Radarr downloaded this release but will not import it automatically. It stays in Radarr’s queue — and this duplicate stays deferred — until you remove it there: in Radarr’s Activity → Queue, Remove from queue with Removal Method Remove from Download Client (or Ignore Download to keep the download) and, so it is not grabbed again, Blocklist Release Blocklist Only. Use Manual Import only if you want this release instead of the file Radarr has now: importing replaces that file.',
    );
    expect(notice).not.toHaveTextContent('per series');

    const queue = within(notice).getByRole('link', { name: 'Open queue' });
    expect(queue).toHaveAttribute('href', 'https://radarr.example.com/activity/queue');
    expect(queue).toHaveAttribute('target', '_blank');
    expect(queue).toHaveAttribute('rel', 'noopener noreferrer');
    const item = within(notice).getByRole('link', { name: 'Open in Radarr' });
    expect(item).toHaveAttribute('href', 'https://radarr.example.com/movie/1084244');
    expect(item).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('explains the series-wide deferral of Sonarr and shows a download in progress without the import hint', () => {
    const sonarr: ArrItemRef = {
      instanceId: 3,
      instanceName: 'Sonarr',
      kind: 'sonarr',
      itemId: 7,
      queueCount: 1,
      queue: [{ title: 'Severance.S01E09.1080p', status: 'downloading', trackedDownloadState: 'downloading', label: 'Downloading' }],
    };
    render(<ArrQueueNotice group={{ status: 'deferred', flags: ['arr_queue_busy'], arrItems: [sonarr] }} arrLinks={[]} />);
    const notice = screen.getByRole('status');
    expect(notice).toHaveTextContent('1 queue entry');
    expect(notice).toHaveTextContent('Severance.S01E09.1080p — Downloading');
    expect(notice).toHaveTextContent('any queue entry of the series defers every duplicate of the series');
    expect(notice).not.toHaveTextContent('will not import it automatically');
    // No links without a server-built address.
    expect(within(notice).queryByRole('link')).not.toBeInTheDocument();
  });

  it('asks for a re-scan for a group stored before queue entries were recorded, linking the queue', () => {
    render(<ArrQueueNotice group={{ status: 'deferred', flags: ['arr_queue_busy'] }} arrLinks={LINKS} />);
    const notice = screen.getByRole('status');
    expect(notice).toHaveTextContent('Re-scan this group to see the queue entries.');
    expect(within(notice).getByRole('link', { name: 'Open Radarr queue' })).toHaveAttribute('href', 'https://radarr.example.com/activity/queue');
  });

  // The scan sets the flag whatever the status: a group in review (a fresh download Plex has not
  // analysed yet) keeps it, and so do protected or ignored groups.
  it('does not call a group in review deferred', () => {
    render(<ArrQueueNotice group={{ status: 'review', flags: ['arr_queue_busy'], arrItems: [STUCK] }} arrLinks={LINKS} />);
    const notice = screen.getByRole('alert');
    expect(notice).toHaveTextContent('In the Radarr/Sonarr download queue');
    expect(notice).toHaveTextContent('Dupearr removes nothing of a title while its Radarr/Sonarr has a download or import queued for it');
    expect(notice).not.toHaveTextContent(/deferred/i);
    expect(notice).not.toHaveTextContent('checked again on the next scan');
    expect(notice).toHaveTextContent('and Dupearr removes nothing of this title — until you remove it there');
    expect(within(notice).getByRole('link', { name: 'Open queue' })).toBeInTheDocument();
  });

  it('shows nothing for a protected, ignored or resolved group', () => {
    for (const status of ['protected', 'ignored', 'resolved'] as const) {
      const { container, unmount } = render(
        <ArrQueueNotice group={{ status, flags: ['arr_queue_busy'], arrItems: [STUCK] }} arrLinks={LINKS} />,
      );
      expect(container, status).toBeEmptyDOMElement();
      unmount();
    }
  });

  it('gives no import hint for a download the *arr is about to import by itself', () => {
    const pending: ArrItemRef = {
      ...STUCK,
      queueCount: 1,
      queue: [{ title: 'Toy.Story.5.2026.2160p.WEB-DL', status: 'completed', trackedDownloadState: 'importPending', trackedDownloadStatus: 'ok', label: 'Downloaded - Waiting to Import' }],
    };
    render(<ArrQueueNotice group={{ status: 'deferred', flags: ['arr_queue_busy'], arrItems: [pending] }} arrLinks={LINKS} />);
    const notice = screen.getByRole('status');
    expect(notice).toHaveTextContent('Toy.Story.5.2026.2160p.WEB-DL — Downloaded - Waiting to Import');
    expect(notice).not.toHaveTextContent('will not import it automatically');
  });

  it('never renders an unsafe href', () => {
    const bad: ArrLink[] = [{ ...LINKS[0]!, itemUrl: 'javascript:alert(1)', queueUrl: 'https://user:pw@radarr.example.com/activity/queue' }];
    render(<ArrQueueNotice group={{ status: 'deferred', flags: ['arr_queue_busy'], arrItems: [STUCK] }} arrLinks={bad} />);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
});
