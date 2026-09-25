import { clsx } from 'clsx';
import { ExternalLink, type LucideIcon } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { Badge, Card } from '@/components/ui';
import { safeExternalUrl } from './connectionUtils';

export interface ConnectionCardProps {
  /** Connection name (card title). */
  name: string;
  /** Provider icon shown before the name. */
  icon?: LucideIcon;
  /** First line under the title (e.g. the URL or provider name). */
  subtitle?: ReactNode;
  /** Extra body content (details lines). */
  children?: ReactNode;
  /** Extra badges after the Enabled/Disabled badge. */
  badges?: ReactNode;
  enabled: boolean;
  /** Opens the edit modal. */
  onClick: () => void;
  className?: string;
}

/**
 * *arr "connection card" (Settings → Media Servers / Applications / Connect): name, a subtitle,
 * detail lines and a badge row. The whole card is clickable (Enter/Space too) and opens the editor.
 */
export function ConnectionCard({ name, icon: Icon, subtitle, children, badges, enabled, onClick, className }: ConnectionCardProps) {
  return (
    <Card
      onClick={onClick}
      className={clsx('flex flex-col', !enabled && 'opacity-75', className)}
      bodyClassName="flex min-h-24 flex-1 flex-col gap-2"
      title={
        <span className="flex min-w-0 items-center gap-2" title={name}>
          {Icon && <Icon aria-hidden width={18} height={18} className="shrink-0 text-accent-soft" />}
          <span className="truncate">{name || 'Unnamed'}</span>
        </span>
      }
    >
      {subtitle && <div className="truncate text-sm text-muted">{subtitle}</div>}
      {children}
      <div className="mt-auto flex flex-wrap items-center gap-1.5 pt-1">
        <Badge kind={enabled ? 'success' : 'default'} outline>
          {enabled ? 'Enabled' : 'Disabled'}
        </Badge>
        {badges}
      </div>
    </Card>
  );
}

export interface ProviderCardProps {
  name: string;
  description?: ReactNode;
  icon?: LucideIcon;
  /** Optional documentation link (opened in a new tab; only http/https URLs are rendered). */
  infoUrl?: string | null;
  onSelect: () => void;
  className?: string;
}

/**
 * Selectable provider tile in "Add …" modals (Radarr / Sonarr, Discord / Slack / …). The info link
 * sits beside (not inside) the select button so the markup stays valid and keyboard friendly.
 */
export function ProviderCard({ name, description, icon: Icon, infoUrl, onSelect, className }: ProviderCardProps) {
  const href = safeExternalUrl(infoUrl);
  // The aria-label replaces the name computed from the button's content, so the description (which
  // may carry what choosing the provider means, e.g. Jellyfin's read-only rules) is linked instead.
  const descriptionId = useId();
  return (
    <div
      className={clsx(
        'relative flex flex-col rounded border border-border bg-card-alt transition-colors hover:border-accent hover:bg-card-hover',
        className,
      )}
    >
      <button
        type="button"
        onClick={onSelect}
        className="flex flex-1 items-start gap-3 rounded p-4 text-left focus-visible:outline-2 focus-visible:outline-accent"
        aria-label={`Add ${name}`}
        aria-describedby={description ? descriptionId : undefined}
      >
        {Icon && <Icon aria-hidden width={28} height={28} className="mt-0.5 shrink-0 text-accent-soft" />}
        <span className="min-w-0">
          <span className="block text-base font-semibold text-fg-strong">{name}</span>
          {description && (
            <span id={descriptionId} className="mt-0.5 block text-sm text-muted">
              {description}
            </span>
          )}
        </span>
      </button>
      {href && (
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center gap-1 self-end px-4 pb-3 text-xs text-muted hover:text-accent-soft"
          aria-label={`More info about ${name} (opens in a new tab)`}
        >
          More info
          <ExternalLink aria-hidden width={12} height={12} />
        </a>
      )}
    </div>
  );
}
