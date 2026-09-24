import { useQueryClient } from '@tanstack/react-query';
import { Film, Folder, RefreshCw, Tv } from 'lucide-react';
import { useId, useState, type KeyboardEvent } from 'react';
import { errorMessage } from '@/api/client';
import { useServerLibraries, useSyncServerLibraries, useUpdateLibrary } from '@/api/hooks/useMediaServers';
import { queryKeys } from '@/api/queryKeys';
import type { Id, Library, MediaServer, Profile } from '@/api/types';
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  LoadingIndicator,
  Select,
  Spinner,
  Switch,
  Table,
  TextInput,
  useToast,
  type TableColumn,
} from '@/components/ui';
import { humanize } from '@/lib/constants';

const LIBRARY_TYPE_LABELS: Record<string, string> = { movie: 'Movies', show: 'TV Shows' };

/** "movie" → "Movies", "show" → "TV Shows", anything else humanized. */
export function libraryTypeLabel(type: string | null | undefined): string {
  if (!type) return 'Unknown';
  return LIBRARY_TYPE_LABELS[type] ?? humanize(type);
}

/** Profile <Select> options: "Default (<name>)" (= null) first, then every profile. */
export function profileOptions(profiles: readonly Profile[], current: Id | null): { value: string; label: string }[] {
  const def = profiles.find((p) => p.isDefault);
  const options = [
    { value: '', label: def ? `Default (${def.name})` : 'Default' },
    ...profiles.map((p) => ({ value: String(p.id), label: p.name })),
  ];
  if (current != null && !profiles.some((p) => p.id === current)) {
    options.push({ value: String(current), label: `Missing profile (#${current})` });
  }
  return options;
}

/** Scope group → hints for groups that can't cross-match (only one library, or mixed types). */
export function scopeGroupHints(libraries: readonly Library[]): Map<string, string> {
  const groups = new Map<string, Library[]>();
  for (const lib of libraries) {
    const g = lib.scopeGroup.trim();
    if (!g || !lib.enabled) continue;
    groups.set(g, [...(groups.get(g) ?? []), lib]);
  }
  const hints = new Map<string, string>();
  for (const [g, libs] of groups) {
    if (libs.length < 2) hints.set(g, 'No other enabled library shares this group yet.');
    else if (new Set(libs.map((l) => l.type)).size > 1) hints.set(g, 'Mixes movie and TV libraries; they never match.');
  }
  return hints;
}

export interface LibrariesTableProps {
  server: MediaServer;
  profiles: readonly Profile[];
}

/** Libraries of one server: enable, profile and scope group edits save immediately; Sync re-reads Plex. */
export function LibrariesTable({ server, profiles }: LibrariesTableProps) {
  const toast = useToast();
  const qc = useQueryClient();
  const libraries = useServerLibraries(server.id);
  const sync = useSyncServerLibraries();
  const update = useUpdateLibrary();
  /** Optimistic edits per library until the PUT settles. */
  const [pending, setPending] = useState<Record<Id, Library>>({});
  const datalistId = useId();

  const rows = (libraries.data ?? []).map((l) => pending[l.id] ?? l);
  const hints = scopeGroupHints(rows);
  const knownGroups = [...new Set(rows.map((l) => l.scopeGroup.trim()).filter(Boolean))].sort();

  const save = async (next: Library) => {
    setPending((p) => ({ ...p, [next.id]: next }));
    try {
      const saved = await update.mutateAsync(next);
      const value = saved && typeof saved === 'object' && 'id' in saved ? saved : next;
      qc.setQueryData<Library[]>(queryKeys.mediaServers.libraries(server.id), (old) =>
        old?.map((l) => (l.id === value.id ? value : l)),
      );
    } catch (e) {
      toast.error(`Unable to update library “${next.title}”`, errorMessage(e));
    } finally {
      setPending((p) => {
        const rest = { ...p };
        delete rest[next.id];
        return rest;
      });
    }
  };

  const runSync = () =>
    sync.mutate(server.id, {
      onSuccess: (libs) =>
        toast.success('Libraries synced', `${libs?.length ?? 0} ${libs?.length === 1 ? 'library' : 'libraries'} on ${server.name}`),
      onError: (e) => toast.error('Unable to sync libraries', errorMessage(e)),
    });

  const columns: TableColumn<Library>[] = [
    {
      key: 'title',
      header: 'Library',
      render: (l) => {
        const Icon = l.type === 'show' ? Tv : l.type === 'movie' ? Film : Folder;
        return (
          <span className="flex items-center gap-2">
            <Icon aria-hidden width={16} height={16} className="shrink-0 text-muted" />
            <span className="font-medium text-fg-strong">{l.title}</span>
            {pending[l.id] && <Spinner size="xs" label="Saving" />}
          </span>
        );
      },
    },
    { key: 'type', header: 'Type', render: (l) => libraryTypeLabel(l.type), hideBelow: 'sm' },
    {
      key: 'locations',
      header: 'Locations',
      hideBelow: 'lg',
      render: (l) =>
        (l.locations ?? []).length === 0 ? (
          <span className="text-muted">—</span>
        ) : (
          <ul className="m-0 list-none p-0">
            {(l.locations ?? []).map((loc) => (
              <li key={loc} className="font-mono text-xs break-all text-fg">
                {loc}
              </li>
            ))}
          </ul>
        ),
    },
    {
      key: 'enabled',
      header: 'Enabled',
      align: 'center',
      width: '90px',
      render: (l) => (
        <Switch
          checked={l.enabled}
          onChange={(v) => void save({ ...l, enabled: v })}
          aria-label={`Scan ${l.title}`}
          disabled={!!pending[l.id]}
        />
      ),
    },
    {
      key: 'profile',
      header: 'Profile',
      width: '220px',
      render: (l) => (
        <Select
          aria-label={`Profile for ${l.title}`}
          options={profileOptions(profiles, l.profileId)}
          value={l.profileId == null ? '' : String(l.profileId)}
          disabled={!!pending[l.id]}
          onChange={(v) => {
            const profileId = v === '' ? null : Number(v);
            if (profileId !== l.profileId) void save({ ...l, profileId });
          }}
        />
      ),
    },
    {
      key: 'scopeGroup',
      header: 'Scope group',
      width: '220px',
      render: (l) => (
        <div>
          <ScopeGroupInput
            library={l}
            listId={datalistId}
            disabled={!!pending[l.id]}
            onCommit={(scopeGroup) => void save({ ...l, scopeGroup })}
          />
          {l.enabled && hints.get(l.scopeGroup.trim()) && (
            <div className="mt-1 text-xs text-warning">{hints.get(l.scopeGroup.trim())}</div>
          )}
        </div>
      ),
    },
  ];

  return (
    <div className="mb-6">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <h3 className="m-0 truncate text-base font-normal text-fg-strong">{server.name}</h3>
          {!server.enabled && (
            <Badge kind="default" outline>
              Server disabled
            </Badge>
          )}
        </div>
        <Button size="sm" icon={RefreshCw} onClick={runSync} loading={sync.isPending}>
          Sync
        </Button>
      </div>

      {libraries.isPending ? (
        <LoadingIndicator message="Loading libraries…" />
      ) : libraries.isError ? (
        <Alert
          kind="error"
          title="Unable to load libraries"
          actions={
            <Button size="sm" onClick={() => void libraries.refetch()}>
              Retry
            </Button>
          }
        >
          {errorMessage(libraries.error)}
        </Alert>
      ) : (
        <>
          <Table
            aria-label={`Libraries of ${server.name}`}
            columns={columns}
            rows={rows}
            getRowId={(l) => l.id}
            dense
            emptyState={
              <EmptyState
                compact
                icon={Folder}
                title="No libraries yet"
                description="Sync to load the movie and TV libraries from Plex."
                action={
                  <Button size="sm" icon={RefreshCw} onClick={runSync} loading={sync.isPending}>
                    Sync
                  </Button>
                }
              />
            }
          />
          <datalist id={datalistId}>
            {[...new Set([...knownGroups, 'movies', 'tv'])].map((g) => (
              <option key={g} value={g} />
            ))}
          </datalist>
        </>
      )}
    </div>
  );
}

/** Scope group text field: commits on blur / Enter, Escape reverts. */
function ScopeGroupInput({
  library,
  listId,
  disabled,
  onCommit,
}: {
  library: Library;
  listId: string;
  disabled?: boolean;
  onCommit: (value: string) => void;
}) {
  const [draft, setDraft] = useState(library.scopeGroup);
  const [lastValue, setLastValue] = useState(library.scopeGroup);
  // Adopt server-side changes (derived state; see React docs "adjusting state when a prop changes").
  if (library.scopeGroup !== lastValue) {
    setLastValue(library.scopeGroup);
    setDraft(library.scopeGroup);
  }

  const commit = () => {
    const value = draft.trim();
    if (value !== draft) setDraft(value);
    if (value !== library.scopeGroup.trim()) onCommit(value);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      commit();
    } else if (e.key === 'Escape') {
      e.stopPropagation();
      setDraft(library.scopeGroup);
    }
  };

  return (
    <TextInput
      aria-label={`Scope group for ${library.title}`}
      value={draft}
      list={listId}
      placeholder="None"
      maxLength={64}
      disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={onKeyDown}
      className="h-8"
    />
  );
}
