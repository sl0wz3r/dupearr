import { describe, expect, it } from 'vitest';
import type { ArrLink } from '@/api/types';
import { arrLinkFor, isStuckImport, queuedArrItems, safeHref } from './arrLinks';

describe('safeHref', () => {
  it('keeps absolute http(s) URLs', () => {
    expect(safeHref('http://radarr:7878/radarr/movie/603')).toBe('http://radarr:7878/radarr/movie/603');
    expect(safeHref('https://radarr.example.com/activity/queue')).toBe('https://radarr.example.com/activity/queue');
  });

  it('refuses anything a browser could run or that carries credentials', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JAVASCRIPT:alert(1)',
      ' javascript:alert(1)',
      'data:text/html,<script>alert(1)</script>',
      'vbscript:x',
      '//radarr.example.com',
      '/movie/603',
      'ftp://radarr.example.com',
      'https://user:pw@radarr.example.com/movie/1',
      'https://user@radarr.example.com/movie/1',
      'http://',
      '',
      null,
      undefined,
    ]) {
      expect(safeHref(bad), String(bad)).toBeUndefined();
    }
  });
});

describe('arrLinkFor', () => {
  const links: ArrLink[] = [
    { instanceId: 1, instanceName: 'Radarr', kind: 'radarr', itemId: 9, itemUrl: 'http://r/movie/1', queueUrl: 'http://r/activity/queue' },
    { instanceId: 2, instanceName: 'Radarr 4K', kind: 'radarr', itemId: 9, queueUrl: 'http://r4k/activity/queue' },
  ];

  it('matches instance and item', () => {
    expect(arrLinkFor(links, 2, 9)?.instanceName).toBe('Radarr 4K');
    expect(arrLinkFor(links, 1, 8)).toBeUndefined();
    expect(arrLinkFor(null, 1, 9)).toBeUndefined();
  });
});

describe('isStuckImport', () => {
  it('is a completed download the *arr will not import by itself', () => {
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadState: 'importPending', trackedDownloadStatus: 'warning' })).toBe(true);
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadState: 'importBlocked', trackedDownloadStatus: 'ok' })).toBe(true);
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadStatus: 'error' })).toBe(true);
  });

  it('is not a download in progress, an import about to run or running, or a failure being processed', () => {
    expect(isStuckImport({ title: 'x', status: 'downloading', trackedDownloadState: 'downloading', trackedDownloadStatus: 'warning' })).toBe(false);
    // Radarr's normal step between the completed-download check and its automatic import.
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadState: 'importPending', trackedDownloadStatus: 'ok' })).toBe(false);
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadState: 'importing', trackedDownloadStatus: 'ok' })).toBe(false);
    expect(isStuckImport({ title: 'x', status: 'completed', trackedDownloadState: 'failedPending' })).toBe(false);
    expect(isStuckImport({ title: 'x' })).toBe(false);
  });
});

describe('queuedArrItems', () => {
  it('keeps the items with queue entries', () => {
    const items = queuedArrItems([
      { instanceId: 1, instanceName: 'Radarr', kind: 'radarr', itemId: 1 },
      { instanceId: 1, instanceName: 'Radarr', kind: 'radarr', itemId: 2, queueCount: 1 },
      { instanceId: 1, instanceName: 'Radarr', kind: 'radarr', itemId: 3, queue: [{ title: 'x' }] },
    ]);
    expect(items.map((it) => it.itemId)).toEqual([2, 3]);
    expect(queuedArrItems(undefined)).toEqual([]);
  });
});
