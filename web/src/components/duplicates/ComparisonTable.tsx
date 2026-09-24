import { clsx } from 'clsx';
import { Disc3, Lock, TriangleAlert } from 'lucide-react';
import type { ReactNode } from 'react';
import type { CriterionType, DiscInfo, GroupFile, Id, MediaPart, MediaVersion, Profile } from '@/api/types';
import { Badge, ByteSize, CopyButton, DecisionBadge, RelativeTime } from '@/components/ui';
import {
  ARR_KIND_LABELS,
  CLIP_SET_ORIGIN_DESCRIPTIONS,
  CLIP_SET_ORIGIN_LABELS,
  CRITERION_TYPE_LABELS,
  DECISION_LABELS,
  DISC_ORIGIN_DESCRIPTIONS,
  DISC_ORIGIN_LABELS,
  labelOf,
} from '@/lib/constants';
import {
  audioTrackLabel,
  containerLabel,
  dynamicRangeLabel,
  formatBitrate,
  formatClipCount,
  formatDimensions,
  formatDiscCount,
  formatDuration,
  formatFileCount,
  formatNumber,
  resolutionLabel,
  sourceLabel,
  videoCodecLabel,
} from '@/lib/format';
import { bestForCriterion, decidingCriteria } from './comparison';
import { clipCountOf, discMemberBlocker, discRelativePath, isClipName, isClipSet, mainClipName } from './disc';
import { DiscBadge } from './DiscBadge';
import { bestAudioTrack, isHardlinked, maxLinkCount, subtitleLanguages, versionSize } from './duplicateUtils';
import { OverrideControl, type OverrideValue } from './OverrideControl';

// ---------------------------------------------------------------------------
// Row model
// ---------------------------------------------------------------------------

interface RowContext {
  libraryNames?: ReadonlyMap<Id, string>;
}

interface RowSpec {
  id: string;
  label: string;
  /** Criteria this row represents (drives the "decided here" emphasis). */
  criteria: CriterionType[];
  render: (file: GroupFile, ctx: RowContext) => ReactNode;
  /** Indices of the best cells (defaults to the first criterion). */
  best?: (files: readonly GroupFile[], profile: Profile | null | undefined) => ReadonlySet<number>;
  /** Show the row only when this returns true. */
  visible?: (files: readonly GroupFile[], deciding: ReadonlySet<CriterionType>) => boolean;
}

/** Engine display value for a criterion ("" / missing → undefined). */
function engineValue(file: GroupFile, criterion: CriterionType): string | undefined {
  const v = file.values?.[criterion];
  return typeof v === 'string' && v.trim() !== '' ? v : undefined;
}

function Value({ primary, secondary }: { primary: ReactNode; secondary?: ReactNode }) {
  const empty = primary === undefined || primary === null || primary === '';
  return (
    <div className="min-w-0">
      <div className={clsx('break-words', empty ? 'text-subtle' : 'text-fg-strong')}>{empty ? '—' : primary}</div>
      {secondary ? <div className="mt-0.5 text-xs break-words text-muted">{secondary}</div> : null}
    </div>
  );
}

const firstNonEmpty = (...sets: ReadonlySet<number>[]): ReadonlySet<number> =>
  sets.find((s) => s.size > 0) ?? sets[sets.length - 1] ?? new Set<number>();

/** Part paths shown before "+N more" for a disc exposed by a custom Plex scanner (hundreds of parts). */
const MAX_DISC_PARTS_SHOWN = 25;

/**
 * Clip and file counts of a disc: "312 files · 2 discs"; a loose clip set leads with its clips
 * ("125 clips", plus " · 127 files" when metadata files belong to the set too).
 */
function discCounts(disc: DiscInfo, clips: number): string {
  const files = clips > 0 && disc.fileCount <= clips ? '' : formatFileCount(disc.fileCount);
  return [formatClipCount(clips), files, formatDiscCount(disc.discs)].filter(Boolean).join(' · ');
}

/**
 * Disc kind, origin, clip/file/disc counts, main clip (loose clip sets), main feature and the
 * "metadata unreadable" warning.
 */
function DiscCell({ version, disc }: { version: MediaVersion; disc: DiscInfo }) {
  const clipSet = isClipSet(disc);
  const clips = clipCountOf(version);
  const mainClip = mainClipName(version);
  const counts = discCounts(disc, clips);
  const originLabels = clipSet ? CLIP_SET_ORIGIN_LABELS : DISC_ORIGIN_LABELS;
  const originDescriptions: Record<string, string> = clipSet ? CLIP_SET_ORIGIN_DESCRIPTIONS : DISC_ORIGIN_DESCRIPTIONS;
  // A clip set's main feature is usually its main clip: shown once, as "Main clip".
  const mainFeature = disc.mainFeature && !(clipSet && isClipName(disc.mainFeature)) ? disc.mainFeature : '';
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap gap-1">
        <DiscBadge disc={{ ...disc, clipCount: clips || undefined }} />
        <Badge kind={disc.origin === 'plex' ? 'info' : 'default'} outline title={originDescriptions[disc.origin]}>
          {labelOf(originLabels, disc.origin)}
        </Badge>
      </div>
      {counts && <div className="text-xs text-muted">{counts}</div>}
      {mainClip && (
        <div className="text-xs text-muted" title="The longest clip: the quality shown is this clip’s">
          Main clip: <code className="font-mono break-all text-fg">{mainClip}</code>
        </div>
      )}
      {mainFeature && (
        <div className="text-xs text-muted">
          Main feature: <code className="font-mono break-all text-fg">{mainFeature}</code>
        </div>
      )}
      {!disc.readable && (
        <div
          className="flex items-start gap-1 text-xs text-warning"
          title={
            disc.type === 'iso'
              ? 'ISO images are not opened: only their size is known.'
              : 'The main feature could not be read: resolution, HDR, audio and duration are unknown.'
          }
        >
          <TriangleAlert aria-hidden width={12} height={12} className="mt-0.5 shrink-0" />
          <span>metadata unreadable — quality unknown</span>
        </div>
      )}
    </div>
  );
}

/** A path with its copy button (and the local path when it differs). */
function PathLine({ path, localPath }: { path: string; localPath?: string }) {
  return (
    <>
      <div className="flex items-start gap-1">
        <code className="min-w-0 flex-1 font-mono text-xs break-all text-fg">{path || '—'}</code>
        {path && <CopyButton value={path} size="xs" label="Copy path" />}
      </div>
      {localPath && localPath !== path && (
        <div className="font-mono text-[11px] break-all text-muted">local: {localPath}</div>
      )}
    </>
  );
}

/**
 * Paths of a disc: its root (what a removal moves from), then — collapsed — the parts a custom
 * Plex scanner exposes (often hundreds of BDMV/STREAM clips, shown relative to the root), capped at
 * {@link MAX_DISC_PARTS_SHOWN}.
 */
function DiscPaths({ disc, parts }: { disc: DiscInfo; parts: readonly MediaPart[] }) {
  const extra = parts.filter((p) => p.path && p.path !== disc.root);
  const shown = extra.slice(0, MAX_DISC_PARTS_SHOWN);
  const clipSet = isClipSet(disc);
  // A loose clip set's parts are its clips (one Plex version each, merged into this copy).
  const noun = clipSet && extra.every((p) => isClipName(p.path)) ? 'clip' : 'part';
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="text-[11px] font-semibold tracking-wide text-subtle uppercase">
        {clipSet ? 'Clip folder' : 'Disc root'}
      </div>
      <PathLine path={disc.root} localPath={disc.localRoot} />
      {extra.length > 0 && (
        <details className="text-xs text-muted">
          <summary className="cursor-pointer select-none hover:text-fg">
            {extra.length === 1 ? `1 ${noun}` : `${formatNumber(extra.length)} ${noun}s`}
            {disc.origin === 'plex' ? ' in Plex' : ''}
          </summary>
          <ul className="m-0 mt-1 flex list-none flex-col gap-0.5 p-0">
            {shown.map((p, i) => (
              <li key={`${i}:${p.path}`} className="font-mono text-[11px] break-all" title={p.path}>
                {discRelativePath(disc.root, p.path)}
              </li>
            ))}
            {extra.length > shown.length && <li>…and {formatNumber(extra.length - shown.length)} more</li>}
          </ul>
        </details>
      )}
    </div>
  );
}

const ROWS: RowSpec[] = [
  {
    id: 'library',
    label: 'Library',
    criteria: ['library'],
    render: (f, ctx) => (
      <Value primary={ctx.libraryNames?.get(f.version.libraryId) || f.version.libraryTitle} secondary={engineValue(f, 'library')} />
    ),
  },
  {
    id: 'health',
    label: 'Health',
    criteria: ['health'],
    visible: (files, deciding) => deciding.has('health') || files.some((f) => engineValue(f, 'health')),
    render: (f) => <Value primary={engineValue(f, 'health')} />,
  },
  {
    id: 'disc',
    label: 'Full disc',
    criteria: [],
    visible: (files) => files.some((f) => !!f.version.disc || !!discMemberBlocker(f.version)),
    render: (f) =>
      f.version.disc ? (
        <DiscCell version={f.version} disc={f.version.disc} />
      ) : discMemberBlocker(f.version) ? (
        <Value primary="" secondary="One file of a disc — never removed on its own" />
      ) : (
        <Value primary="" secondary="Regular video file" />
      ),
  },
  {
    id: 'resolution',
    label: 'Resolution',
    criteria: ['resolution'],
    render: (f) => (
      <Value
        primary={engineValue(f, 'resolution') ?? resolutionLabel(f.version.resolution)}
        secondary={formatDimensions(f.version.width, f.version.height)}
      />
    ),
  },
  {
    id: 'dynamic_range',
    label: 'Dynamic range',
    criteria: ['dynamic_range'],
    render: (f) => (
      <Value
        primary={engineValue(f, 'dynamic_range') ?? dynamicRangeLabel(f.version.dynamicRange)}
        secondary={f.version.dvProfile ? `Dolby Vision profile ${f.version.dvProfile}` : undefined}
      />
    ),
  },
  {
    id: 'video',
    label: 'Video',
    criteria: ['video_codec', 'bit_depth'],
    render: (f) => (
      <Value
        primary={engineValue(f, 'video_codec') ?? videoCodecLabel(f.version.videoCodec)}
        secondary={[
          f.version.videoProfile,
          f.version.bitDepth ? `${f.version.bitDepth}-bit` : '',
          f.version.frameRate ? `${f.version.frameRate}` : '',
        ]
          .filter(Boolean)
          .join(' · ')}
      />
    ),
  },
  {
    id: 'video_bitrate',
    label: 'Video bitrate',
    criteria: ['video_bitrate'],
    render: (f) => {
      const own = formatBitrate(f.version.videoBitrateKbps);
      const overall = formatBitrate(f.version.bitrateKbps);
      return (
        <Value
          primary={engineValue(f, 'video_bitrate') ?? (own || overall)}
          secondary={own ? (overall ? `${overall} overall` : undefined) : overall ? 'overall (video stream unknown)' : undefined}
        />
      );
    },
  },
  {
    id: 'audio',
    label: 'Audio',
    criteria: ['audio_format', 'audio_channels', 'audio_track_count', 'audio_language'],
    best: (files, profile) =>
      firstNonEmpty(bestForCriterion('audio_format', files, profile), bestForCriterion('audio_channels', files, profile)),
    render: (f) => {
      const tracks = f.version.audioTracks ?? [];
      const best = bestAudioTrack(tracks);
      return (
        <div className="min-w-0">
          <Value primary={engineValue(f, 'audio_format') ?? audioTrackLabel(best)} />
          {tracks.length > 0 && (
            <details className="mt-0.5 text-xs text-muted">
              <summary className="cursor-pointer select-none hover:text-fg">
                {tracks.length === 1 ? '1 track' : `${formatNumber(tracks.length)} tracks`}
              </summary>
              <ul className="m-0 mt-1 flex list-none flex-col gap-0.5 p-0">
                {tracks.map((t, i) => (
                  <li key={i} className="break-words">
                    {[
                      audioTrackLabel(t) || t.codec || 'Unknown',
                      !t.language && !t.languageCode ? 'unknown language' : '',
                      t.default ? 'default' : '',
                      t.title,
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  </li>
                ))}
              </ul>
            </details>
          )}
        </div>
      );
    },
  },
  {
    id: 'subtitles',
    label: 'Subtitles',
    criteria: ['subtitle_track_count'],
    render: (f) => {
      const subs = f.version.subtitleTracks ?? [];
      const langs = subtitleLanguages(subs);
      const shown = langs.slice(0, 6);
      const forced = subs.filter((s) => s?.forced).length;
      const external = subs.filter((s) => s?.external).length;
      return (
        <Value
          primary={subs.length === 0 ? 'None' : formatNumber(subs.length)}
          secondary={
            subs.length > 0
              ? [
                  shown.join(', ') + (langs.length > shown.length ? ` +${langs.length - shown.length}` : ''),
                  forced ? `${forced} forced` : '',
                  external ? `${external} external` : '',
                ]
                  .filter(Boolean)
                  .join(' · ')
              : undefined
          }
        />
      );
    },
  },
  {
    id: 'container',
    label: 'Container',
    criteria: ['container'],
    render: (f) =>
      f.version.disc && !engineValue(f, 'container') ? (
        isClipSet(f.version.disc) ? (
          <Value primary={containerLabel(f.version.container)} secondary="loose disc clips" />
        ) : (
          <Value primary="" secondary="None — a full disc" />
        )
      ) : (
        <Value primary={engineValue(f, 'container') ?? containerLabel(f.version.container)} />
      ),
  },
  {
    id: 'size',
    label: 'Size',
    criteria: ['file_size'],
    render: (f) => {
      const parts = f.version.parts ?? [];
      const links = maxLinkCount(f.version);
      const disc = f.version.disc;
      const secondary = disc
        ? isClipSet(disc)
          ? ['every clip', discCounts(disc, clipCountOf(f.version))].filter(Boolean).join(' · ')
          : ['whole disc', formatFileCount(disc.fileCount)].filter(Boolean).join(' · ')
        : parts.length > 1
          ? `${formatNumber(parts.length)} parts`
          : undefined;
      return (
        <div className="min-w-0">
          <Value primary={<ByteSize bytes={versionSize(f.version)} />} secondary={secondary} />
          {isHardlinked(f.version) && (
            <div className="mt-0.5 text-xs text-warning" title={`${links} hardlinks to this file`}>
              hardlinked — won’t free space
            </div>
          )}
        </div>
      );
    },
  },
  {
    id: 'duration',
    label: 'Duration',
    criteria: [],
    render: (f) => <Value primary={formatDuration(f.version.durationMs)} />,
  },
  {
    id: 'source',
    label: 'Source',
    criteria: ['source'],
    render: (f) => <Value primary={engineValue(f, 'source') ?? sourceLabel(f.version.source)} />,
  },
  {
    id: 'edition',
    label: 'Edition',
    criteria: [],
    render: (f) => <Value primary={f.version.edition || f.version.arr?.edition || ''} />,
  },
  {
    id: 'arr',
    label: '*arr',
    criteria: ['arr_managed', 'custom_format_score'],
    best: (files, profile) =>
      firstNonEmpty(bestForCriterion('custom_format_score', files, profile), bestForCriterion('arr_managed', files, profile)),
    render: (f) => {
      const a = f.version.arr;
      if (!a) return <Value primary="" secondary="Not tracked by an *arr" />;
      const score = a.customFormatScore;
      return (
        <div className="flex min-w-0 flex-col gap-0.5">
          <div className="text-fg-strong">
            {a.instanceName || labelOf(ARR_KIND_LABELS, a.kind)}
            {a.qualityName && <span className="text-fg"> · {a.qualityName}</span>}
          </div>
          <div className="text-xs text-muted">
            {[
              `CF score ${score === null || score === undefined ? 'unknown' : formatNumber(score)}`,
              a.releaseGroup ? `Group: ${a.releaseGroup}` : '',
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
          <div className="flex flex-wrap gap-1">
            <Badge kind={a.monitored ? 'info' : 'default'} outline>
              {a.monitored ? 'Monitored' : 'Unmonitored'}
            </Badge>
            {a.qualityCutoffNotMet && (
              <Badge kind="warning" outline title="The *arr may upgrade again and recreate the duplicate">
                Cutoff unmet
              </Badge>
            )}
          </div>
        </div>
      );
    },
  },
  {
    id: 'added',
    label: 'Added',
    criteria: ['date_added'],
    render: (f) => <Value primary={<RelativeTime date={f.version.addedAt} />} />,
  },
  {
    id: 'paths',
    label: 'Path(s)',
    criteria: ['filename_score'],
    best: () => new Set<number>(),
    render: (f) => {
      const parts = f.version.parts ?? [];
      const score = engineValue(f, 'filename_score');
      const disc = f.version.disc;
      if (disc) {
        return (
          <div className="flex min-w-0 flex-col gap-1.5">
            <DiscPaths disc={disc} parts={parts} />
            {score && <div className="text-xs text-muted">Filename score: {score}</div>}
          </div>
        );
      }
      if (parts.length === 0) return <Value primary="" />;
      return (
        <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
          {parts.map((p, i) => (
            <li key={`${i}:${p.id ?? ''}:${p.path}`} className="min-w-0">
              <PathLine path={p.path} localPath={p.localPath} />
              <div className="mt-0.5 flex flex-wrap gap-1">
                {p.exists === false && (
                  <Badge kind="danger" icon={TriangleAlert} title="Plex reports this file as missing">
                    Missing
                  </Badge>
                )}
                {p.accessible === false && (
                  <Badge kind="warning" icon={TriangleAlert} title="Plex cannot read this file">
                    Not accessible
                  </Badge>
                )}
                {(p.sharedWith?.length ?? 0) > 0 && (
                  <Badge kind="warning" outline title={`Also used by: ${(p.sharedWith ?? []).join(', ')}`}>
                    Shared with {p.sharedWith!.length} other {p.sharedWith!.length === 1 ? 'item' : 'items'}
                  </Badge>
                )}
              </div>
            </li>
          ))}
          {score && <li className="text-xs text-muted">Filename score: {score}</li>}
        </ul>
      );
    },
  },
];

const COVERED = new Set<CriterionType>(ROWS.flatMap((r) => r.criteria));

// ---------------------------------------------------------------------------
// Table
// ---------------------------------------------------------------------------

export interface ComparisonTableProps {
  /** Files ordered by rank (see sortByRank). */
  files: readonly GroupFile[];
  profile?: Profile | null;
  libraryNames?: ReadonlyMap<Id, string>;
  /** Displayed override (may be optimistic). */
  overrideValue: (file: GroupFile) => OverrideValue;
  onOverride: (file: GroupFile, value: OverrideValue) => void;
  /** File whose override request is in flight. */
  pendingFileId?: Id | null;
  /** Reason overrides are locked (null/undefined = editable). */
  lockedReason?: string | null;
}

/**
 * Side-by-side comparison: one column per copy (by rank), one row per attribute. The best value
 * of each row is highlighted; rows whose criterion decided a removal are emphasized. Columns have
 * fixed widths (CSS vars, narrower on phones) so the table scrolls horizontally with a sticky
 * attribute column instead of squeezing copies.
 */
export function ComparisonTable({
  files,
  profile,
  libraryNames,
  overrideValue,
  onOverride,
  pendingFileId,
  lockedReason,
}: ComparisonTableProps) {
  const deciding = decidingCriteria(files);
  const decidingSet = new Set(deciding.keys());
  const ctx: RowContext = { libraryNames };

  // Criteria the engine reported that no fixed row covers (forward compatible).
  const extraCriteria = new Set<CriterionType>();
  for (const f of files) {
    for (const key of Object.keys(f.values ?? {}) as CriterionType[]) if (!COVERED.has(key)) extraCriteria.add(key);
  }
  for (const c of decidingSet) if (!COVERED.has(c)) extraCriteria.add(c);
  const extraRows: RowSpec[] = [...extraCriteria].map((c) => ({
    id: `criterion-${c}`,
    label: labelOf(CRITERION_TYPE_LABELS, c),
    criteria: [c],
    render: (f) => <Value primary={engineValue(f, c)} />,
  }));

  const rows = [...ROWS, ...extraRows].filter((r) => !r.visible || r.visible(files, decidingSet));

  return (
    <div className="overflow-x-auto rounded border border-border bg-card">
      <table
        aria-label="Copy comparison"
        className="min-w-full table-fixed border-separate border-spacing-0 text-sm [--file-col:15rem] [--label-col:7rem] sm:[--file-col:17rem] sm:[--label-col:9rem]"
        style={{ width: `calc(var(--label-col) + ${files.length} * var(--file-col))` }}
      >
        <colgroup>
          <col style={{ width: 'var(--label-col)' }} />
          {files.map((f) => (
            <col key={f.id} style={{ width: 'var(--file-col)' }} />
          ))}
        </colgroup>
        <thead>
          <tr>
            <th
              scope="col"
              className="sticky left-0 z-20 border-b border-border bg-card-alt px-3 py-2.5 text-left align-bottom text-xs font-semibold text-muted"
            >
              {files.length} copies
            </th>
            {files.map((f, i) => {
              const value = overrideValue(f);
              const disabledOptions: Partial<Record<OverrideValue, string>> = {};
              if (f.protected) disabledOptions.remove = `Protected${f.protectedReason ? `: ${f.protectedReason}` : ''}`;
              // A single file of a disc (a loose 00800.m2ts clip, BDMV/STREAM/…) is never marked for
              // removal on its own — client-side backup of the server's refusal.
              const member = discMemberBlocker(f.version);
              if (member) disabledOptions.remove = member;
              return (
                <th
                  key={f.id}
                  scope="col"
                  data-file-id={f.id}
                  data-decision={f.decision}
                  className={clsx(
                    'border-t-2 border-b border-l border-b-border border-l-border bg-card-alt px-3 py-2.5 text-left align-top font-normal',
                    f.decision === 'keep' ? 'border-t-success' : 'border-t-danger',
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs font-semibold text-muted">
                      #{f.rank > 0 ? f.rank : i + 1}
                    </span>
                    <div className="flex items-center gap-1">
                      {f.protected && (
                        <Badge kind="primary" icon={Lock} title={f.protectedReason || 'Protected by a profile rule'}>
                          Protected
                        </Badge>
                      )}
                      <DecisionBadge decision={f.decision} />
                    </div>
                  </div>
                  {f.version.displayTitle && (
                    <div className="mt-1 truncate text-xs text-muted" title={f.version.displayTitle}>
                      {f.version.displayTitle}
                    </div>
                  )}
                  {f.version.disc && (
                    <div className="mt-1">
                      <DiscBadge disc={{ ...f.version.disc, clipCount: clipCountOf(f.version) || undefined }} short />
                    </div>
                  )}
                  {member && (
                    <div className="mt-1 flex items-start gap-1 text-xs text-warning" data-testid="disc-member-note" title={member}>
                      <Disc3 aria-hidden width={12} height={12} className="mt-0.5 shrink-0" />
                      <span>One file of a disc — never removed on its own</span>
                    </div>
                  )}
                  {f.protected && f.protectedReason && (
                    <div className="mt-1 flex items-start gap-1 text-xs text-primary">
                      <Lock aria-hidden width={12} height={12} className="mt-0.5 shrink-0" />
                      <span>{f.protectedReason}</span>
                    </div>
                  )}
                  <div className="mt-2">
                    <OverrideControl
                      label={`Override for copy #${f.rank > 0 ? f.rank : i + 1}`}
                      value={value}
                      onChange={(v) => onOverride(f, v)}
                      pending={pendingFileId === f.id}
                      disabled={!!lockedReason || (pendingFileId != null && pendingFileId !== f.id)}
                      disabledOptions={disabledOptions}
                      title={lockedReason ?? undefined}
                    />
                  </div>
                  {f.override && (
                    <div className="mt-1 text-[11px] text-muted">
                      Overridden · profile says {labelOf(DECISION_LABELS, f.engineDecision)}
                    </div>
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const best = row.best
              ? row.best(files, profile)
              : row.criteria[0]
                ? bestForCriterion(row.criteria[0], files, profile)
                : new Set<number>();
            const decidedIds = new Set(row.criteria.flatMap((c) => deciding.get(c) ?? []));
            const emphasized = decidedIds.size > 0;
            return (
              <tr key={row.id} data-row={row.id} data-deciding={emphasized || undefined}>
                <th
                  scope="row"
                  className={clsx(
                    'sticky left-0 z-10 border-b border-border bg-card px-3 py-2 text-left align-top text-xs font-semibold whitespace-nowrap',
                    emphasized ? 'border-l-2 border-l-accent text-accent-soft' : 'text-muted',
                  )}
                  title={emphasized ? 'This criterion decided which copy is removed' : undefined}
                >
                  <span className="block">{row.label}</span>
                  {emphasized && (
                    <span className="mt-0.5 block text-[10px] font-semibold tracking-wide uppercase">Decided</span>
                  )}
                </th>
                {files.map((f, i) => {
                  const isBest = best.has(i);
                  const decidedHere = decidedIds.has(f.id);
                  return (
                    <td
                      key={f.id}
                      data-best={isBest || undefined}
                      data-decided={decidedHere || undefined}
                      className={clsx(
                        'border-b border-l border-border px-3 py-2 align-top',
                        isBest ? 'bg-success/10' : emphasized && 'bg-accent/5',
                      )}
                    >
                      <div className="flex items-start gap-1.5">
                        <div className="min-w-0 flex-1">{row.render(f, ctx)}</div>
                        {isBest && (
                          <span className="shrink-0 text-[10px] font-semibold text-success uppercase" title="Best value">
                            best
                          </span>
                        )}
                        {decidedHere && (
                          <span
                            className="shrink-0 text-[10px] font-semibold text-danger uppercase"
                            title="This criterion decided the removal of this copy"
                          >
                            lost
                          </span>
                        )}
                      </div>
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
