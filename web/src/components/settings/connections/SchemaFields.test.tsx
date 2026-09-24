import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import type { ProviderSchema } from '@/api/types';
import { PreferencesProvider, PREFERENCES_STORAGE_KEY } from '@/app/preferences';
import { MASKED_SECRET } from '@/lib/constants';
import type { FieldErrors } from './connectionUtils';
import { initialSettings, toSubmitSettings, type SettingsValues } from './notificationForm';
import { SchemaFields } from './SchemaFields';

const SCHEMA: ProviderSchema = {
  kind: 'discord',
  name: 'Discord',
  infoUrl: 'https://support.discord.com/hc/en-us/articles/228383668',
  fields: [
    {
      name: 'webhookUrl',
      label: 'Webhook URL',
      type: 'url',
      required: true,
      secret: true,
      helpText: 'Server Settings → Integrations → Webhooks',
    },
    { name: 'username', label: 'Username', type: 'text', helpText: 'Overrides the webhook name' },
    { name: 'avatarUrl', label: 'Avatar URL', type: 'url', advanced: true },
    { name: 'priority', label: 'Priority', type: 'number', default: 5 },
    { name: 'mentionEveryone', label: 'Mention @everyone', type: 'checkbox' },
    { name: 'format', label: 'Format', type: 'select', options: ['plain', 'markdown'], default: 'markdown' },
    { name: 'template', label: 'Template', type: 'textarea' },
    { name: 'password', label: 'SMTP Password', type: 'password' },
  ],
};

/** Stored config as the API returns it: secrets masked. */
const STORED = {
  webhookUrl: MASKED_SECRET,
  username: 'Dupearr',
  priority: 3,
  mentionEveryone: true,
  format: 'plain',
  template: 'Hello',
  password: MASKED_SECRET,
  extraKey: 'kept',
};

let latest: SettingsValues = {};

function Harness({ initial, errors }: { initial: SettingsValues; errors?: FieldErrors }) {
  const [values, setValues] = useState(initial);
  latest = values;
  return (
    <SchemaFields
      fields={SCHEMA.fields}
      values={values}
      errors={errors}
      onChange={(name, value) => setValues((v) => ({ ...v, [name]: value }))}
    />
  );
}

function renderFields(initial: SettingsValues, opts: { showAdvanced?: boolean; errors?: FieldErrors } = {}) {
  if (opts.showAdvanced) window.localStorage.setItem(PREFERENCES_STORAGE_KEY, JSON.stringify({ showAdvanced: true }));
  return render(
    <PreferencesProvider>
      <Harness initial={initial} errors={opts.errors} />
    </PreferencesProvider>,
  );
}

describe('SchemaFields', () => {
  it('renders every field type with the stored values', () => {
    renderFields(initialSettings(SCHEMA, STORED));

    const webhook = screen.getByLabelText(/webhook url/i);
    expect(webhook).toHaveAttribute('type', 'password');
    expect(webhook).toHaveValue(MASKED_SECRET);
    expect(screen.getByLabelText(/username/i)).toHaveValue('Dupearr');
    expect(screen.getByLabelText(/priority/i)).toHaveValue(3);
    expect(screen.getByLabelText(/mention @everyone/i)).toBeChecked();
    expect(screen.getByLabelText(/format/i)).toHaveValue('plain');
    expect(screen.getByLabelText(/template/i)).toHaveValue('Hello');
    const password = screen.getByLabelText(/smtp password/i);
    expect(password).toHaveAttribute('type', 'password');
    expect(password).toHaveValue(MASKED_SECRET);

    expect(screen.getByText(/overrides the webhook name/i)).toBeInTheDocument();
    // Masked secrets explain that the stored value is hidden.
    expect(screen.getAllByText(/stored value hidden/i)).toHaveLength(2);
    // Required marker on the required field only.
    expect(screen.getAllByText('*')).toHaveLength(1);
  });

  it('hides advanced fields unless Show Advanced is on', () => {
    renderFields(initialSettings(SCHEMA, STORED));
    expect(screen.queryByLabelText(/avatar url/i)).not.toBeInTheDocument();
  });

  it('shows advanced fields when Show Advanced is on', () => {
    renderFields(initialSettings(SCHEMA, STORED), { showAdvanced: true });
    expect(screen.getByLabelText(/avatar url/i)).toBeInTheDocument();
  });

  it('shows an advanced field that has an error even when Show Advanced is off', () => {
    renderFields(initialSettings(SCHEMA, STORED), { errors: { avatarUrl: ['Avatar URL must be a full URL'] } });
    expect(screen.getByLabelText(/avatar url/i)).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('Avatar URL must be a full URL');
  });

  it('preserves untouched masked secrets (and unknown keys) in the payload', async () => {
    const user = userEvent.setup();
    renderFields(initialSettings(SCHEMA, STORED));
    await user.clear(screen.getByLabelText(/username/i));
    await user.type(screen.getByLabelText(/username/i), '  Bot  ');

    const payload = toSubmitSettings(SCHEMA, latest);
    expect(payload.webhookUrl).toBe(MASKED_SECRET);
    expect(payload.password).toBe(MASKED_SECRET);
    expect(payload.username).toBe('Bot');
    expect(payload.priority).toBe(3);
    expect(payload.extraKey).toBe('kept');
  });

  it('replaces the mask with what the user types instead of appending to it', async () => {
    const user = userEvent.setup();
    renderFields(initialSettings(SCHEMA, STORED));
    const webhook = screen.getByLabelText(/webhook url/i);
    await user.click(webhook);
    await user.type(webhook, 'https://discord.com/api/webhooks/1/abc');
    expect(latest.webhookUrl).toBe('https://discord.com/api/webhooks/1/abc');
    expect(webhook).toHaveValue('https://discord.com/api/webhooks/1/abc');
    expect(screen.getAllByText(/stored value hidden/i)).toHaveLength(1);

    // Deleting from the mask clears it rather than leaving partial stars.
    const password = screen.getByLabelText(/smtp password/i);
    await user.click(password);
    await user.keyboard('{End}{Backspace}');
    expect(latest.password).toBe('');
  });

  it('new connections start from schema defaults', () => {
    renderFields(initialSettings(SCHEMA, null));
    expect(screen.getByLabelText(/webhook url/i)).toHaveValue('');
    expect(screen.getByLabelText(/priority/i)).toHaveValue(5);
    expect(screen.getByLabelText(/format/i)).toHaveValue('markdown');
    expect(screen.getByLabelText(/mention @everyone/i)).not.toBeChecked();
    expect(screen.queryByText(/stored value hidden/i)).not.toBeInTheDocument();
  });

  it('renders field errors under the field', () => {
    renderFields(initialSettings(SCHEMA, null), { errors: { webhookUrl: ['Webhook URL is required'] } });
    expect(screen.getByRole('alert')).toHaveTextContent('Webhook URL is required');
    expect(screen.getByLabelText(/webhook url/i)).toHaveAttribute('aria-invalid', 'true');
  });
});

describe('SchemaFields — secret textarea (Apprise URLs)', () => {
  const FIELDS: ProviderSchema['fields'] = [
    { name: 'urls', label: 'Apprise URLs', type: 'textarea', secret: true, helpText: 'One per line' },
  ];

  function SecretHarness({ initial }: { initial: SettingsValues }) {
    const [values, setValues] = useState(initial);
    latest = values;
    return <SchemaFields fields={FIELDS} values={values} onChange={(n, v) => setValues((s) => ({ ...s, [n]: v }))} />;
  }

  it('is a multi-line field, so a pasted list keeps its line breaks', async () => {
    const user = userEvent.setup();
    render(<SecretHarness initial={{ urls: '' }} />);
    const field = screen.getByLabelText(/apprise urls/i);
    expect(field.tagName).toBe('TEXTAREA');
    await user.click(field);
    await user.paste('discord://id/token\ntgram://bot/chat');
    expect(latest.urls).toBe('discord://id/token\ntgram://bot/chat');
    expect(toSubmitSettings({ kind: 'apprise', name: 'Apprise', fields: FIELDS }, latest).urls).toBe(
      'discord://id/token\ntgram://bot/chat',
    );
  });

  it('keeps the stored mask until replaced, and typing replaces it', async () => {
    const user = userEvent.setup();
    render(<SecretHarness initial={{ urls: MASKED_SECRET }} />);
    const field = screen.getByLabelText(/apprise urls/i);
    expect(field).toHaveValue(MASKED_SECRET);
    expect(field).toHaveAttribute('data-secret', 'true');
    expect(screen.getByText(/stored value hidden/i)).toBeInTheDocument();
    expect(toSubmitSettings({ kind: 'apprise', name: 'Apprise', fields: FIELDS }, latest).urls).toBe(MASKED_SECRET);

    await user.click(field);
    await user.keyboard('{End}');
    await user.paste('ntfy://topic');
    expect(latest.urls).toBe('ntfy://topic');
  });

  it('hides the characters until Show is pressed', async () => {
    const user = userEvent.setup();
    render(<SecretHarness initial={{ urls: 'discord://id/token' }} />);
    const field = screen.getByLabelText(/apprise urls/i);
    expect(field.className).toContain('[-webkit-text-security:disc]');
    await user.click(screen.getByRole('button', { name: 'Show' }));
    expect(field.className).not.toContain('[-webkit-text-security:disc]');
    expect(screen.getByRole('button', { name: 'Hide' })).toBeInTheDocument();
  });
});
