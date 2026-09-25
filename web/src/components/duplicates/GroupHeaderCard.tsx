import { ExternalLink } from 'lucide-react';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import type { DuplicateGroup, Id } from '@/api/types';
import { Badge, GroupFlagBadge, GroupStatusBadge, RelativeTime } from '@/components/ui';
import { GROUP_FLAG_DESCRIPTIONS, MEDIA_TYPE_LABELS, labelOf } from '@/lib/constants';
import { episodeCode, formatNumber } from '@/lib/format';
import { externalIdLinks, hasJellyfinCopy } from './duplicateUtils';
import { PosterImage } from './PosterImage';

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] font-semibold tracking-wide text-subtle uppercase">{label}</dt>
      <dd className="m-0 text-sm break-words text-fg">{children}</dd>
    </div>
  );
}

export interface GroupHeaderCardProps {
  group: DuplicateGroup;
  libraryNames?: ReadonlyMap<Id, string>;
  /** Name of the decision profile (when loaded). */
  profileName?: string;
}

/** Poster, title, status (+reason), flags with descriptions, libraries, external ids and timestamps. */
export function GroupHeaderCard({ group, libraryNames, profileName }: GroupHeaderCardProps) {
  const isEpisode = group.mediaType === 'episode';
  const heading = isEpisode ? group.showTitle || group.title || 'Untitled episode' : group.title || 'Untitled';
  const subtitle = isEpisode
    ? [episodeCode(group.season, group.episode), group.title].filter(Boolean).join(' – ')
    : group.year > 0
      ? String(group.year)
      : '';
  const libraries = [
    ...new Set(
      (group.libraryIds ?? [])
        .map((id) => libraryNames?.get(id))
        .concat((group.files ?? []).map((f) => f.version?.libraryTitle))
        .filter((n): n is string => !!n),
    ),
  ];
  const links = externalIdLinks(group.externalIds, group.mediaType);
  const flags = group.flags ?? [];

  return (
    <section aria-label="Duplicate" className="flex gap-4 rounded border border-border bg-card p-4">
      <PosterImage
        serverId={group.serverId}
        thumb={group.thumb}
        mediaType={group.mediaType}
        width={200}
        height={300}
        className="hidden w-28 sm:block md:w-36"
        alt=""
      />
      <div className="flex min-w-0 flex-1 flex-col gap-3">
        <div className="min-w-0">
          <h1 className="m-0 text-2xl leading-tight font-light break-words text-fg-strong">{heading}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted">
            <span>{labelOf(MEDIA_TYPE_LABELS, group.mediaType)}</span>
            {subtitle && (
              <>
                <span aria-hidden>·</span>
                <span className="text-fg">{subtitle}</span>
              </>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <GroupStatusBadge status={group.status} size="md" />
          {hasJellyfinCopy(group.files) && (
            <Badge kind="info" outline title="A copy is listed by a Jellyfin server (read-only)">
              Jellyfin
            </Badge>
          )}
          {/* A reason can quote a release title with no space in it: let it wrap instead of widening the page. */}
          {group.statusReason && <span className="min-w-0 text-sm break-words text-fg">{group.statusReason}</span>}
        </div>

        {flags.length > 0 && (
          <ul className="m-0 flex list-none flex-col gap-1.5 p-0" aria-label="Flags">
            {flags.map((flag) => (
              <li key={flag} className="flex flex-wrap items-baseline gap-2 text-sm">
                <GroupFlagBadge flag={flag} />
                {GROUP_FLAG_DESCRIPTIONS[flag] && <span className="text-muted">{GROUP_FLAG_DESCRIPTIONS[flag]}</span>}
              </li>
            ))}
          </ul>
        )}

        <dl className="m-0 grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3 lg:grid-cols-5">
          <Meta label={libraries.length === 1 ? 'Library' : 'Libraries'}>{libraries.join(', ') || '-'}</Meta>
          <Meta label="Profile">
            {profileName ? (
              <Link to="/settings/profiles" className="text-accent-soft no-underline hover:underline">
                {profileName}
              </Link>
            ) : (
              '-'
            )}
          </Meta>
          <Meta label="First seen">
            <RelativeTime date={group.firstSeenAt} />
          </Meta>
          <Meta label="Last seen">
            <RelativeTime date={group.lastSeenAt} />
          </Meta>
          <Meta label="Stable scans">
            <span title="Consecutive scans with the same decision (auto mode needs a stable decision)">
              {formatNumber(group.stableCount ?? 0)}
            </span>
          </Meta>
        </dl>

        {links.length > 0 && (
          <ul className="m-0 flex list-none flex-wrap gap-2 p-0" aria-label="External ids">
            {links.map((l) => (
              <li key={l.key}>
                {l.url ? (
                  <a
                    href={l.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 rounded-sm border border-border-strong px-1.5 py-0.5 text-xs text-fg no-underline hover:border-accent hover:text-accent-soft"
                  >
                    {l.label}
                    <span className="text-muted">{l.id}</span>
                    <ExternalLink aria-hidden width={11} height={11} />
                  </a>
                ) : (
                  <span className="inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-xs text-muted">
                    {l.label} {l.id}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
