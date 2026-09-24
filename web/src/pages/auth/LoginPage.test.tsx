import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AuthStatus } from '@/api/types';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import LoginPage from './LoginPage';

function setup(statuses: AuthStatus[]) {
  let i = 0;
  const mock = mockFetch(({ method, path }) => {
    if (method === 'GET' && path === '/api/v1/auth/status') {
      const body = statuses[Math.min(i, statuses.length - 1)];
      i += 1;
      return jsonResponse(body);
    }
    return undefined;
  });
  renderWithProviders(<LoginPage />, { route: '/login?returnUrl=%2Fduplicates' });
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('<LoginPage>', () => {
  it('shows the sign-in form for Forms authentication', async () => {
    setup([{ setupRequired: false, authenticationMethod: 'Forms', authenticated: false }]);
    expect(await screen.findByLabelText('Username')).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
  });

  // r2-outbound-web#4: a persistent session is opt-in.
  it('does not remember the session unless asked to', async () => {
    const user = userEvent.setup();
    const { requests } = setup([{ setupRequired: false, authenticationMethod: 'Forms', authenticated: false }]);
    const remember = await screen.findByLabelText('Remember me');
    expect(remember).not.toBeChecked();
    await user.type(screen.getByLabelText('Username'), 'admin');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/login')).toBe(true));
    const login = requests.find((r) => r.method === 'POST' && r.path === '/login')!;
    expect(login.body).toMatchObject({ username: 'admin', rememberMe: false });
  });

  // r2-outbound-web#4: under External, "Continue" would loop back here (the request was not relayed
  // by the trusted proxy): explain instead.
  it('explains External authentication instead of a "Continue" button that loops back here', async () => {
    setup([{ setupRequired: false, authenticationMethod: 'External', authenticated: false }]);
    expect(await screen.findByRole('heading', { name: 'Open Dupearr through your reverse proxy' })).toBeInTheDocument();
    expect(screen.getByText('DUPEARR__AUTH__TRUSTEDPROXIES')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /continue/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Check Again' })).toBeInTheDocument();
  });

  it('explains the None host rule instead of a "Continue" button that loops back here', async () => {
    const user = userEvent.setup();
    const { requests } = setup([{ setupRequired: false, authenticationMethod: 'None', authenticated: false }]);

    expect(await screen.findByRole('heading', { name: 'Open Dupearr by its local address' })).toBeInTheDocument();
    expect(screen.getByText(/only accepts requests to its IP address/)).toBeInTheDocument();
    // The current address is named, and the ways out are listed.
    expect(screen.getByText(window.location.host)).toBeInTheDocument();
    expect(screen.getByText(/open Dupearr by its IP address or local host name/)).toBeInTheDocument();
    expect(screen.getByText('Forms')).toBeInTheDocument();
    expect(screen.getByText('External')).toBeInTheDocument();
    expect(screen.getByText('DUPEARR__AUTH__METHOD')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /continue/i })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument();

    // "Check Again" re-asks the server instead of navigating away.
    const before = requests.filter((r) => r.path === '/api/v1/auth/status').length;
    await user.click(screen.getByRole('button', { name: 'Check Again' }));
    await waitFor(() =>
      expect(requests.filter((r) => r.path === '/api/v1/auth/status').length).toBeGreaterThan(before),
    );
  });

  it('leaves the page once the server trusts the request under None', async () => {
    const user = userEvent.setup();
    setup([
      { setupRequired: false, authenticationMethod: 'None', authenticated: false },
      { setupRequired: false, authenticationMethod: 'None', authenticated: true },
    ]);
    await user.click(await screen.findByRole('button', { name: 'Check Again' }));
    await waitFor(() =>
      expect(screen.queryByRole('heading', { name: 'Open Dupearr by its local address' })).not.toBeInTheDocument(),
    );
  });
});
