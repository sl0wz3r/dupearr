import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { HealthCheck, SystemStatus } from '@/api/types';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import { REPOSITORY_URL } from '@/components/system/systemFormat';
import StatusPage from './StatusPage';

const STATUS: SystemStatus = {
  appName: 'Dupearr',
  instanceName: 'Dupearr',
  version: '0.1.0',
  commit: '0123456789abcdef0123456789abcdef01234567',
  buildDate: '2026-09-20T12:00:00Z',
  startTime: '2026-09-22T10:00:00Z',
  uptimeSeconds: 3600,
  osName: 'linux',
  osArch: 'amd64',
  goVersion: 'go1.25.1',
  isDocker: true,
  dataDirectory: '/config',
  configFile: '/config/config.xml',
  databaseFile: '/config/dupearr.db',
  databaseSize: 12_582_912,
  urlBase: '',
  authentication: 'Forms',
  dryRun: true,
  mode: 'manual',
};

function setup(health: HealthCheck[]) {
  const mock = mockFetch(({ method, path }) => {
    if (method === 'GET' && path === '/api/v1/system/status') return jsonResponse(STATUS);
    if (method === 'GET' && path === '/api/v1/health') return jsonResponse(health);
    if (method === 'GET' && path === '/api/v1/command') return jsonResponse([]);
    if (method === 'POST' && path === '/api/v1/health/check') {
      return jsonResponse({ id: 3, name: 'CheckHealth', body: {}, status: 'queued', trigger: 'manual', queued: '' });
    }
    return undefined;
  });
  renderWithProviders(<StatusPage />);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<StatusPage>', () => {
  it('shows the about details', async () => {
    setup([]);
    expect(await screen.findByText('0.1.0')).toBeInTheDocument();
    expect(screen.getByText('0123456789')).toHaveAttribute('title', STATUS.commit);
    expect(screen.getByText('go1.25.1')).toBeInTheDocument();
    expect(screen.getByText('linux / amd64')).toBeInTheDocument();
    expect(screen.getByText('/config/dupearr.db')).toBeInTheDocument();
    expect(screen.getByText('12.0 MiB')).toBeInTheDocument();
    expect(screen.getByText('(none)')).toBeInTheDocument();
    expect(screen.getByText('Forms (Login Page)')).toBeInTheDocument();
    expect(screen.getByText('Manual')).toBeInTheDocument();
    expect(screen.getByText('Enabled')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Source/ })).toHaveAttribute('href', REPOSITORY_URL);
  });

  it('shows the all-good state when there are no issues', async () => {
    setup([]);
    expect(await screen.findByText('No issues with your configuration.')).toBeInTheDocument();
  });

  it('lists issues by severity with safe wiki links only', async () => {
    setup([
      { source: 'DryRunCheck', type: 'notice', message: 'Dry run is enabled' },
      {
        source: 'AuthCheck',
        type: 'warning',
        message: 'Authentication is disabled',
        wikiUrl: 'javascript:alert(document.cookie)',
      },
      {
        source: 'PlexConnectivityCheck',
        type: 'error',
        message: 'Unable to reach Plex',
        wikiUrl: 'https://wiki.example/dupearr/system#unable-to-reach-plex',
      },
    ]);
    const list = await screen.findByRole('list', { name: 'Health issues' });
    const items = within(list).getAllByRole('listitem');
    expect(items.map((li) => li.textContent)).toEqual([
      expect.stringContaining('Unable to reach Plex'),
      expect.stringContaining('Authentication is disabled'),
      expect.stringContaining('Dry run is enabled'),
    ]);
    expect(within(items[0]!).getByRole('img', { name: 'Error' })).toBeInTheDocument();
    expect(within(items[0]!).getByRole('link')).toHaveAttribute(
      'href',
      'https://wiki.example/dupearr/system#unable-to-reach-plex',
    );
    expect(within(items[0]!).getByRole('link')).toHaveAttribute('rel', 'noopener noreferrer');
    // A javascript: URL is never rendered as a link.
    expect(within(items[1]!).queryByRole('link')).not.toBeInTheDocument();
  });

  it('runs a health check', async () => {
    const user = userEvent.setup();
    const { requests } = setup([]);
    await user.click(await screen.findByRole('button', { name: 'Check Now' }));
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/api/v1/health/check')).toBe(true));
  });
});
