import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { jsonResponse, mockFetch, renderWithProviders } from '@/components/activity/testUtils';
import SetupPage from './SetupPage';

function setup(setupStatus = 200, setupBody: unknown = {}, envForced?: Record<string, string>) {
  const mock = mockFetch(({ method, path }) => {
    if (method === 'GET' && path === '/api/v1/auth/status') {
      return jsonResponse({ setupRequired: true, authenticationMethod: 'Forms', authenticated: false, envForced });
    }
    if (method === 'POST' && path === '/api/v1/auth/setup') return jsonResponse(setupBody, setupStatus);
    if (method === 'POST' && path === '/login') return jsonResponse({});
    return undefined;
  });
  renderWithProviders(<SetupPage />, { route: '/setup' });
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

async function fill(user: ReturnType<typeof userEvent.setup>, code: string) {
  const codeField = await screen.findByLabelText('Setup Code');
  if (code) await user.type(codeField, code);
  await user.type(screen.getByLabelText('Username'), 'admin');
  await user.type(screen.getByLabelText('Password'), 'long-enough-pw');
  await user.type(screen.getByLabelText('Password Confirmation'), 'long-enough-pw');
  await user.click(screen.getByRole('button', { name: 'Save and Continue' }));
}

// r2-outbound-web#4: first-run setup needs the one-time code Dupearr prints in its log (SEC-003).
describe('<SetupPage>', () => {
  it('sends the setup code with the account and signs in without remembering the session', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await fill(user, ' P25CC-ABCDE ');
    await waitFor(() => expect(requests.some((r) => r.method === 'POST' && r.path === '/login')).toBe(true));
    const body = requests.find((r) => r.path === '/api/v1/auth/setup')!.body;
    expect(body).toMatchObject({ username: 'admin', setupCode: 'P25CC-ABCDE', authenticationMethod: 'Forms' });
    expect(requests.find((r) => r.path === '/login')!.body).toMatchObject({ rememberMe: false });
  });

  it('requires the setup code before sending anything', async () => {
    const user = userEvent.setup();
    const { requests } = setup();
    await fill(user, '');
    expect(await screen.findByText(/Enter the setup code/)).toBeInTheDocument();
    expect(requests.some((r) => r.method === 'POST')).toBe(false);
  });

  it('shows a refused setup code on the setup code field', async () => {
    const user = userEvent.setup();
    setup(403, { message: 'Enter the setup code that Dupearr printed in its log at startup (for Docker: docker logs <container>)' });
    await fill(user, 'WRONG');
    expect(await screen.findByText(/The setup code is wrong or has expired/)).toBeInTheDocument();
    expect(screen.getByLabelText('Setup Code')).toHaveAttribute('aria-invalid', 'true');
  });

  // GAP-07: a requirement an environment variable forces is shown read-only and sent as it is,
  // never offered as a choice the save would discard.
  it('shows an environment-forced authentication requirement read-only', async () => {
    const user = userEvent.setup();
    const { requests } = setup(200, {}, { authenticationRequired: 'DisabledForLocalAddresses' });
    const select = await screen.findByLabelText('Authentication Required');
    await waitFor(() => expect(select).toBeDisabled());
    expect(select).toHaveValue('DisabledForLocalAddresses');
    expect(screen.getByText(/DUPEARR__AUTH__REQUIRED/)).toBeInTheDocument();
    await fill(user, 'P25CC-ABCDE');
    await waitFor(() => expect(requests.some((r) => r.path === '/api/v1/auth/setup')).toBe(true));
    expect(requests.find((r) => r.path === '/api/v1/auth/setup')!.body).toMatchObject({
      authenticationRequired: 'DisabledForLocalAddresses',
    });
  });

  it('explains a refusal for a non-local client', async () => {
    const user = userEvent.setup();
    setup(403, { message: 'First-run setup is only available from the local network' });
    await fill(user, 'P25CC-ABCDE');
    expect(await screen.findByText(/can only be set up from a local network address/)).toBeInTheDocument();
  });
});
