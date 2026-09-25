/** The optional External URL of a Radarr/Sonarr instance (only used for "Open in Radarr/Sonarr" links). */
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ArrInstance } from '@/api/types';
import { MASKED_SECRET } from '@/lib/constants';
import { ArrInstanceModal, arrPayload, validateArr, type ArrFormValues } from './ArrInstanceModal';

type UseState = typeof import('react').useState;
type Opts = { onSuccess?: (data: unknown, vars: unknown) => void; onError?: (e: unknown) => void };

const mocks = vi.hoisted(() => {
  const calls: Record<string, unknown[]> = {};
  /** A useMutation stand-in that records its variables and succeeds. */
  const mutation = (name: string, useState: UseState) => () => {
    const [state, setState] = useState<{ data?: unknown }>({});
    return {
      ...state,
      error: null,
      isPending: false,
      isSuccess: state.data !== undefined,
      reset: () => setState({}),
      mutate: (vars: unknown, opts?: Opts) => {
        (calls[name] ??= []).push(vars);
        setState({ data: {} });
        opts?.onSuccess?.({}, vars);
      },
    };
  };
  return { calls, mutation };
});

vi.mock('@/api/hooks/useArr', async () => {
  const { useState } = await import('react');
  return {
    useCreateArrInstance: mocks.mutation('createArr', useState),
    useUpdateArrInstance: mocks.mutation('updateArr', useState),
    useDeleteArrInstance: mocks.mutation('deleteArr', useState),
    useTestArrInstance: mocks.mutation('testArr', useState),
  };
});

vi.mock('@/api/hooks/useMediaServers', () => ({
  useMediaServers: () => ({ data: [], isPending: false, isError: false, error: null }),
}));

beforeEach(() => {
  for (const k of Object.keys(mocks.calls)) delete mocks.calls[k];
});

const RADARR: ArrInstance = {
  id: 7,
  name: 'Radarr',
  kind: 'radarr',
  url: 'http://radarr:7878',
  externalUrl: 'https://radarr.example.com',
  apiKey: MASKED_SECRET,
  verifyTls: true,
  enabled: true,
  tags: [],
  createdAt: '',
  updatedAt: '',
};

/** Lets the modal's initial-focus animation frame run so it doesn't steal focus mid-typing. */
async function settle() {
  await act(() => new Promise((resolve) => setTimeout(resolve, 40)));
}

describe('External URL', () => {
  const values: ArrFormValues = { name: 'Radarr', url: 'http://radarr:7878', apiKey: '0123456789abcdef0123456789abcdef', verifyTls: true, enabled: true, tags: [] };

  it('is optional and validated like a URL when set', () => {
    expect(validateArr({ ...values, externalUrl: '' })).toEqual({});
    expect(validateArr({ ...values, externalUrl: '  ' })).toEqual({});
    expect(validateArr({ ...values, externalUrl: 'https://radarr.example.com/radarr' })).toEqual({});
    expect(validateArr({ ...values, externalUrl: 'radarr.example.com' })).toEqual({ externalUrl: ['External URL must start with http:// or https://'] });
    expect(validateArr({ ...values, externalUrl: 'javascript:alert(1)' }).externalUrl).toHaveLength(1);
    expect(validateArr({ ...values, externalUrl: 'https://user:pw@radarr.example.com' })).toEqual({
      externalUrl: ['External URL must not contain a user name or password'],
    });
  });

  it('is the start page, not a page of Radarr or its API copied from the browser', () => {
    for (const page of [
      'https://radarr.example.com/movie/603',
      'https://radarr.example.com/radarr/activity/queue',
      'https://radarr.example.com/settings/general/',
      'https://radarr.example.com/api/v3',
      'https://sonarr.example.com/series/the-expanse',
    ]) {
      expect(validateArr({ ...values, externalUrl: page }).externalUrl, page).toEqual([
        'Enter the address of the start page (with its URL base, if any), not of a page in it, e.g. https://radarr.example.com',
      ]);
    }
    for (const base of ['https://radarr.example.com/', 'https://media.example.com/movies', 'https://example.com/radarr4k']) {
      expect(validateArr({ ...values, externalUrl: base }), base).toEqual({});
    }
  });

  it('is sent trimmed, and "" when cleared', () => {
    expect(arrPayload({ ...values, externalUrl: ' https://radarr.example.com/ ' }, 'radarr')).toMatchObject({ externalUrl: 'https://radarr.example.com' });
    expect(arrPayload({ ...values, externalUrl: '' }, 'radarr')).toMatchObject({ externalUrl: '' });
    expect(arrPayload(values, 'radarr')).not.toHaveProperty('externalUrl');
  });

  it('is edited in the modal and saved with the masked key', async () => {
    const user = userEvent.setup();
    render(<ArrInstanceModal instance={RADARR} onClose={vi.fn()} />);
    await settle();
    const field = screen.getByLabelText('External URL');
    expect(field).toHaveValue('https://radarr.example.com');
    expect(screen.getByText(/used only for the "Open in Radarr" links/)).toBeInTheDocument();

    await user.clear(field);
    await user.type(field, 'ftp://radarr.example.com');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(screen.getByText('External URL must start with http:// or https://')).toBeInTheDocument();
    expect(mocks.calls.updateArr).toBeUndefined();

    await user.clear(field);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(mocks.calls.updateArr).toEqual([
      { instance: expect.objectContaining({ id: 7, url: 'http://radarr:7878', externalUrl: '', apiKey: MASKED_SECRET }), forceSave: false },
    ]);
  });
});
