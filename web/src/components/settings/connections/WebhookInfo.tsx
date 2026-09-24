import { RefreshCw, Webhook } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage, getUrlBase, normalizeUrlBase } from '@/api/client';
import { useHostConfig, useRegenerateWebhookToken } from '@/api/hooks/useSettings';
import { useAppInfo } from '@/app/AppInfo';
import { Alert, Button, ConfirmDialog, CopyButton, FormGroup, TextInput, useToast } from '@/components/ui';
import { trimTrailingSlash, validateHttpUrl } from './connectionUtils';

export type WebhookSource = 'radarr' | 'sonarr' | 'plex';

interface WebhookSourceInfo {
  label: string;
  /** Where to paste the URL in the other app. */
  where: string;
  /** Triggers to tick. */
  triggers: readonly string[];
  note?: string;
}

export const WEBHOOK_SOURCES: Record<WebhookSource, WebhookSourceInfo> = {
  radarr: {
    label: 'Radarr',
    where: 'Radarr → Settings → Connect → + → Webhook (method POST)',
    triggers: ['On File Import', 'On File Upgrade', 'On Rename', 'On Movie File Delete'],
    note: 'Older Radarr versions call the first two “On Import” and “On Upgrade”. Add the webhook to every Radarr instance (including 4K).',
  },
  sonarr: {
    label: 'Sonarr',
    where: 'Sonarr → Settings → Connect → + → Webhook (method POST)',
    triggers: ['On File Import', 'On File Upgrade', 'On Rename', 'On Episode File Delete'],
    note: 'Older Sonarr versions call the first two “On Import” and “On Upgrade”. Add the webhook to every Sonarr instance.',
  },
  plex: {
    label: 'Plex',
    where: 'Plex Web → Account Settings → Webhooks → Add Webhook',
    triggers: ['New content added to a library (library.new)'],
    note: 'Plex webhooks require an active Plex Pass on the server owner account.',
  },
};

/** Path of an address without trailing slashes ("" for none or an unparseable address). */
function addressPath(base: string): string {
  try {
    return new URL(base.trim()).pathname.replace(/\/+$/, '');
  } catch {
    return '';
  }
}

/**
 * Inbound webhook URL: `{base}{urlBase}/api/v1/webhook/{source}?apikey=<webhook token>`. `base` is
 * the address the other application uses to reach Dupearr (scheme + host + port). A base that
 * already ends with the URL base (e.g. copied from the browser) doesn't get it twice.
 *
 * `token` must be the **webhook token** (HostConfig.webhookToken), never the master API key: the
 * URL is stored in Radarr/Sonarr (whose API and backups return it unmasked) and in the plex.tv
 * account, and the webhook token can do nothing but queue scans.
 */
export function webhookUrl(base: string, urlBase: string, source: WebhookSource, token: string): string {
  const prefix = normalizeUrlBase(urlBase);
  let root = trimTrailingSlash(base);
  if (prefix && addressPath(root).toLowerCase() === prefix.toLowerCase()) {
    root = new URL(root).origin;
  }
  return `${root}${prefix}/api/v1/webhook/${source}?apikey=${encodeURIComponent(token)}`;
}

/** The same URL with the token hidden, for display. */
export function maskWebhookUrl(url: string): string {
  return url.replace(/([?&]apikey=)[^&]*/i, '$1••••••••');
}

export interface WebhookInfoProps {
  sources: readonly WebhookSource[];
  className?: string;
}

/**
 * "Webhook (optional)" panel: the URL(s) to paste into Radarr/Sonarr/Plex so they trigger targeted
 * scans right after imports/upgrades/deletes. They carry the webhook token (only revealed through
 * the copy button), which can only queue scans — never the master API key.
 */
export function WebhookInfo({ sources, className }: WebhookInfoProps) {
  const info = useAppInfo();
  const host = useHostConfig();
  const regenerate = useRegenerateWebhookToken();
  const toast = useToast();
  const baseId = useId();
  const origin = typeof window === 'undefined' ? '' : window.location.origin;
  const [base, setBase] = useState(origin);
  const [confirmRegenerate, setConfirmRegenerate] = useState(false);
  const token = host.data?.webhookToken ?? '';
  const urlBase = info?.urlBase ?? getUrlBase();
  const baseError = validateHttpUrl(base, { label: 'Address' });
  const basePath = baseError ? '' : addressPath(base);
  const baseHasPath = basePath !== '' && basePath.toLowerCase() !== normalizeUrlBase(urlBase).toLowerCase();

  return (
    <div className={className}>
      <p className="mt-0 mb-3 text-sm text-muted">
        Optional: let the other application notify Dupearr right after imports, upgrades, renames and deletions so
        the affected title is re-checked immediately. Scheduled scans keep running either way (webhooks sent while
        Dupearr is down are lost).
      </p>
      <FormGroup
        label="Dupearr address"
        htmlFor={baseId}
        errors={baseError}
        warning={baseHasPath ? 'Enter only scheme, host and port — the URL base is added automatically.' : undefined}
        helpText="As seen from the other application, e.g. http://dupearr:3873 on the same Docker network."
      >
        <TextInput
          id={baseId}
          value={base}
          onChange={(e) => setBase(e.target.value)}
          inputMode="url"
          spellCheck={false}
          invalid={!!baseError}
        />
      </FormGroup>
      {!token && !host.isPending && (
        <Alert kind="warning" className="mb-3">
          The webhook token could not be loaded{host.error ? `: ${errorMessage(host.error)}` : ''}; reload the page to show
          the webhook URLs.
        </Alert>
      )}
      <p className="mt-0 mb-3 text-xs text-muted">
        The URLs carry Dupearr&apos;s webhook token, which can only queue scans — not the API key. If you used an older
        webhook URL with the API key, replace it and then regenerate the API key in Settings → General.
      </p>
      <div className="flex flex-col gap-3">
        {sources.map((source) => {
          const meta = WEBHOOK_SOURCES[source];
          const url = baseError || !token ? '' : webhookUrl(base, urlBase, source, token);
          return (
            <div key={source} className="rounded border border-border bg-card-alt p-3">
              <div className="mb-1.5 flex items-center gap-2 font-semibold text-fg-strong">
                <Webhook aria-hidden width={16} height={16} className="text-accent-soft" />
                {meta.label}
              </div>
              <div className="mb-2 text-sm text-muted">{meta.where}</div>
              <div className="flex items-center gap-2">
                <code
                  className="min-w-0 flex-1 rounded border border-border bg-input px-2 py-1.5 font-mono text-xs break-all text-fg"
                  aria-label={`${meta.label} webhook URL (token hidden)`}
                >
                  {url ? maskWebhookUrl(url) : '—'}
                </code>
                {url && <CopyButton value={url} label={`Copy ${meta.label} webhook URL`} />}
              </div>
              <div className="mt-2 text-sm">
                <span className="text-muted">Triggers: </span>
                <span className="text-fg">{meta.triggers.join(', ')}</span>
              </div>
              {meta.note && <div className="mt-1 text-xs text-muted">{meta.note}</div>}
            </div>
          );
        })}
      </div>
      <div className="mt-3">
        <Button
          size="sm"
          icon={RefreshCw}
          onClick={() => setConfirmRegenerate(true)}
          loading={regenerate.isPending}
          disabled={!token}
        >
          Regenerate webhook token
        </Button>
      </div>
      <ConfirmDialog
        open={confirmRegenerate}
        title="Regenerate Webhook Token"
        message="Webhook URLs with the current token stop working immediately: update them in Radarr, Sonarr and Plex."
        confirmLabel="Regenerate"
        kind="warning"
        onConfirm={() => {
          setConfirmRegenerate(false);
          regenerate.mutate(undefined, {
            onSuccess: () => toast.success('Webhook token regenerated', 'Update the webhook URLs in Radarr, Sonarr and Plex.'),
            onError: (e) => toast.error('Unable to regenerate the webhook token', errorMessage(e)),
          });
        }}
        onCancel={() => setConfirmRegenerate(false)}
      />
    </div>
  );
}
