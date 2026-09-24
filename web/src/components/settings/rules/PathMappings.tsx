/**
 * Path mappings (Settings → Media Management): translate a path prefix as Plex or an *arr sees it
 * into the path Dupearr sees, so the filesystem deletion method, hardlink detection and keeper
 * verification can reach the files. Saved immediately (not part of the settings form).
 * POST/PUT may return a non-fatal `X-Dupearr-Warning` (e.g. local path missing) — shown as an alert.
 */
import { ArrowRight, FolderSync, Pencil, Plus, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import {
  useArrInstances,
  useDeletePathMapping,
  useLibraries,
  useMediaServers,
  usePathMappings,
  useSavePathMapping,
} from '@/api/hooks';
import type { ArrInstance, Id, Library, MediaServer, PathMapping, PathMappingInput, PathSourceType } from '@/api/types';
import { SettingsSection } from '@/components/page';
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  FormGroup,
  IconButton,
  Modal,
  Select,
  Table,
  TextInput,
  useToast,
  type TableColumn,
} from '@/components/ui';
import { ARR_KIND_LABELS, MEDIA_SERVER_KIND_LABELS, labelOf } from '@/lib/constants';
import { groupFieldErrors, isAbsolutePath, isFilesystemRoot } from './validation';

// ---------------------------------------------------------------------------
// Source helpers
// ---------------------------------------------------------------------------

/** Encodes a mapping source as a single select value: "server:1" / "arr:2". */
export function sourceKey(type: PathSourceType | string, id: Id): string {
  return `${type}:${id}`;
}

/** Parses a source select value; null when malformed. */
export function parseSourceKey(key: string): { sourceType: PathSourceType; sourceId: Id } | null {
  const m = /^(server|arr):(\d+)$/.exec(key);
  if (!m) return null;
  const id = Number(m[2]);
  return Number.isSafeInteger(id) && id > 0 ? { sourceType: m[1] as PathSourceType, sourceId: id } : null;
}

/** Display name of a mapping's source ("Plex Server (Plex)", "Radarr 4K (Radarr)"). */
export function sourceName(
  m: Pick<PathMapping, 'sourceType' | 'sourceId'>,
  servers: readonly MediaServer[],
  arrInstances: readonly ArrInstance[],
): { name: string; kind: string; known: boolean } {
  if (m.sourceType === 'server') {
    const s = servers.find((x) => x.id === m.sourceId);
    return s
      ? { name: s.name, kind: labelOf(MEDIA_SERVER_KIND_LABELS, s.kind), known: true }
      : { name: `Media server #${m.sourceId}`, kind: 'Media server', known: false };
  }
  const a = arrInstances.find((x) => x.id === m.sourceId);
  return a
    ? { name: a.name, kind: labelOf(ARR_KIND_LABELS, a.kind), known: true }
    : { name: `*arr instance #${m.sourceId}`, kind: '*arr', known: false };
}

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------

export interface PathMappingDraft {
  id?: Id;
  /** sourceKey() value, "" when not chosen. */
  source: string;
  remotePath: string;
  localPath: string;
}

export function draftFromMapping(m: PathMapping | null): PathMappingDraft {
  if (!m) return { source: '', remotePath: '', localPath: '' };
  return { id: m.id, source: sourceKey(m.sourceType, m.sourceId), remotePath: m.remotePath, localPath: m.localPath };
}

type DraftErrors = Partial<Record<'source' | 'remotePath' | 'localPath' | 'general', string[]>>;

/**
 * Client-side checks of a mapping. Filesystem removals are confined to the local side of the
 * mappings (docs/DECISIONS.md D6/D8), so a local path that is a whole filesystem root would lift
 * that guard.
 */
export function validatePathMappingDraft(d: PathMappingDraft): DraftErrors {
  const e: DraftErrors = {};
  if (!parseSourceKey(d.source)) e.source = ['Choose the media server or *arr instance whose paths you are mapping'];
  if (!d.remotePath.trim()) e.remotePath = ['Remote path is required'];
  else if (!isAbsolutePath(d.remotePath)) e.remotePath = ['Use an absolute path, e.g. /data/media/movies'];
  if (!d.localPath.trim()) e.localPath = ['Local path is required'];
  else if (!isAbsolutePath(d.localPath)) e.localPath = ['Use an absolute path, e.g. /data/media/movies'];
  else if (isFilesystemRoot(d.localPath)) {
    e.localPath = [
      'Map a specific media folder (e.g. /data/media), not the filesystem root — filesystem removals are only allowed inside mapped folders',
    ];
  }
  return e;
}

export interface PathMappingModalProps {
  open: boolean;
  initial: PathMappingDraft;
  servers: readonly MediaServer[];
  arrInstances: readonly ArrInstance[];
  libraries?: readonly Library[];
  onClose: () => void;
}

/** Add / edit one mapping. Stays open to show a server warning after saving. */
export function PathMappingModal({ open, initial, servers, arrInstances, libraries = [], onClose }: PathMappingModalProps) {
  const toast = useToast();
  const save = useSavePathMapping();
  const datalistId = useId();
  const [draft, setDraft] = useState<PathMappingDraft>(initial);
  const [attempted, setAttempted] = useState(false);
  const [serverErrors, setServerErrors] = useState<DraftErrors>({});
  const [warning, setWarning] = useState<string | null>(null);
  /** Created on the server but its id is unknown (empty response) — block a second POST. */
  const [savedWithoutId, setSavedWithoutId] = useState(false);

  const isNew = !initial.id;
  const localErrors = attempted ? validatePathMappingDraft(draft) : {};
  const errs = (k: keyof DraftErrors) => [...(localErrors[k] ?? []), ...(serverErrors[k] ?? [])];

  const sourceOptions = [
    ...servers.map((s) => ({
      value: sourceKey('server', s.id),
      label: `${s.name} (${labelOf(MEDIA_SERVER_KIND_LABELS, s.kind)})`,
      group: 'Media Servers',
    })),
    ...arrInstances.map((a) => ({
      value: sourceKey('arr', a.id),
      label: `${a.name} (${labelOf(ARR_KIND_LABELS, a.kind)})`,
      group: 'Applications',
    })),
  ];
  if (draft.source && !sourceOptions.some((o) => o.value === draft.source)) {
    const parsed = parseSourceKey(draft.source);
    if (parsed) {
      const n = sourceName(parsed, servers, arrInstances);
      sourceOptions.push({ value: draft.source, label: n.name, group: 'Unknown' });
    }
  }

  // Remote path suggestions: the selected media server's library folders.
  const parsedSource = parseSourceKey(draft.source);
  const suggestions =
    parsedSource?.sourceType === 'server'
      ? [
          ...new Set(
            libraries.filter((l) => l.serverId === parsedSource.sourceId).flatMap((l) => l.locations ?? []),
          ),
        ].sort()
      : [];

  const set = (p: Partial<PathMappingDraft>) => setDraft((d) => ({ ...d, ...p }));

  const submit = () => {
    setAttempted(true);
    setServerErrors({});
    const local = validatePathMappingDraft(draft);
    const parsed = parseSourceKey(draft.source);
    if (Object.keys(local).length > 0 || !parsed || savedWithoutId) return;
    const body: PathMappingInput = {
      ...(draft.id ? { id: draft.id } : {}),
      sourceType: parsed.sourceType,
      sourceId: parsed.sourceId,
      remotePath: draft.remotePath.trim(),
      localPath: draft.localPath.trim(),
    };
    save.mutate(body, {
      onSuccess: (res) => {
        const savedId = res.mapping?.id ?? draft.id;
        if (res.warning) {
          setWarning(res.warning);
          if (savedId) set({ id: savedId });
          else setSavedWithoutId(true);
          return;
        }
        toast.success(draft.id ? 'Path mapping saved' : 'Path mapping added');
        onClose();
      },
      onError: (e) => {
        if (isApiError(e) && e.isValidation) {
          const byField = groupFieldErrors(e.validationErrors, ['sourceType', 'sourceId', 'remotePath', 'localPath']);
          setServerErrors({
            source: [...(byField.sourceType ?? []), ...(byField.sourceId ?? [])],
            remotePath: byField.remotePath,
            localPath: byField.localPath,
            general: byField[''],
          });
        } else {
          setServerErrors({ general: [errorMessage(e, 'Could not save the path mapping')] });
        }
      },
    });
  };

  const general = serverErrors.general ?? [];

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={isNew ? 'Add Path Mapping' : 'Edit Path Mapping'}
      footer={
        <>
          <Button onClick={onClose} variant={warning ? 'primary' : 'default'} disabled={save.isPending}>
            {warning ? 'Done' : 'Cancel'}
          </Button>
          <Button
            variant={warning ? 'default' : 'primary'}
            loading={save.isPending}
            disabled={savedWithoutId}
            onClick={submit}
          >
            Save
          </Button>
        </>
      }
    >
      {warning && (
        <Alert kind="warning" title="Saved, with a warning" className="mb-4">
          {warning}
        </Alert>
      )}
      {general.length > 0 && (
        <Alert kind="error" className="mb-4">
          {general.join('\n')}
        </Alert>
      )}
      <FormGroup
        label="Source"
        htmlFor="pathmap-source"
        errors={errs('source')}
        helpText="The media server or *arr instance that reports the remote path."
      >
        <Select
          id="pathmap-source"
          placeholder={sourceOptions.length === 0 ? 'Add a media server or application first' : 'Choose a source…'}
          options={sourceOptions}
          value={draft.source}
          invalid={errs('source').length > 0}
          onChange={(v) => set({ source: v })}
        />
      </FormGroup>
      <FormGroup
        label="Remote Path"
        htmlFor="pathmap-remote"
        errors={errs('remotePath')}
        helpText="Folder as the source sees it (e.g. the library folder shown in Plex)."
      >
        <TextInput
          id="pathmap-remote"
          className="font-mono"
          list={suggestions.length > 0 ? datalistId : undefined}
          value={draft.remotePath}
          placeholder="/data/media/movies"
          invalid={errs('remotePath').length > 0}
          onChange={(e) => set({ remotePath: e.target.value })}
        />
        {suggestions.length > 0 && (
          <datalist id={datalistId}>
            {suggestions.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        )}
      </FormGroup>
      <FormGroup
        label="Local Path"
        htmlFor="pathmap-local"
        errors={errs('localPath')}
        helpText="The same folder as Dupearr sees it (inside the Dupearr container)."
      >
        <TextInput
          id="pathmap-local"
          className="font-mono"
          value={draft.localPath}
          placeholder="/data/media/movies"
          invalid={errs('localPath').length > 0}
          onChange={(e) => set({ localPath: e.target.value })}
        />
      </FormGroup>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Section
// ---------------------------------------------------------------------------

function PathExample() {
  return (
    <div className="mb-4 rounded border border-border bg-card-alt px-4 py-3 text-sm">
      <div className="mb-2 text-muted">
        Plex, Radarr/Sonarr and Dupearr usually run in separate containers and may see the same folder under different
        paths. A mapping tells Dupearr where a file reported by a source lives on its own filesystem. Needed for
        filesystem removals, the recycle bin, hardlink detection and on-disk keeper checks.
      </div>
      <div className="flex flex-wrap items-center gap-2" aria-label="Example mapping">
        <span className="rounded border border-border-strong bg-card px-2 py-1">
          <span className="text-muted">Plex sees </span>
          <code>/data/media/movies</code>
        </span>
        <ArrowRight aria-hidden width={16} height={16} className="text-accent-soft" />
        <span className="rounded border border-border-strong bg-card px-2 py-1">
          <span className="text-muted">Dupearr sees </span>
          <code>/data/media/movies</code>
        </span>
      </div>
      <div className="mt-2 text-xs text-muted">
        With the recommended single <code>/data</code> share mounted at the same path everywhere, both sides are
        identical — still add the mapping so Dupearr knows the folder is reachable. If Plex mounts the share at{' '}
        <code>/media</code> instead, map <code>/media/movies</code> → <code>/data/media/movies</code>.
      </div>
    </div>
  );
}

/** "Path Mappings" settings section: explanation, table, add/edit modal, delete confirmation. */
export function PathMappingsSection() {
  const mappings = usePathMappings();
  const servers = useMediaServers();
  const arr = useArrInstances();
  const libraries = useLibraries();
  const del = useDeletePathMapping();
  const toast = useToast();

  const [editing, setEditing] = useState<{ key: number; draft: PathMappingDraft } | null>(null);
  const [nextKey, setNextKey] = useState(1);
  const [toDelete, setToDelete] = useState<PathMapping | null>(null);

  const serverList = servers.data ?? [];
  const arrList = arr.data ?? [];

  const open = (m: PathMapping | null) => {
    setEditing({ key: nextKey, draft: draftFromMapping(m) });
    setNextKey((k) => k + 1);
  };

  const columns: TableColumn<PathMapping>[] = [
    {
      key: 'source',
      header: 'Source',
      render: (m) => {
        const n = sourceName(m, serverList, arrList);
        return (
          <span className="flex flex-wrap items-center gap-1.5">
            <span className="text-fg-strong">{n.name}</span>
            <Badge outline kind={n.known ? 'default' : 'warning'}>
              {n.kind}
            </Badge>
          </span>
        );
      },
    },
    { key: 'remotePath', header: 'Remote Path', render: (m) => <code className="break-all">{m.remotePath}</code> },
    { key: 'localPath', header: 'Local Path', render: (m) => <code className="break-all">{m.localPath}</code> },
    {
      key: 'actions',
      header: <span className="sr-only">Actions</span>,
      align: 'right',
      width: '90px',
      render: (m) => {
        const n = sourceName(m, serverList, arrList);
        return (
          <span className="inline-flex gap-1">
            <IconButton icon={Pencil} size="sm" label={`Edit mapping ${n.name} ${m.remotePath}`} onClick={() => open(m)} />
            <IconButton
              icon={Trash2}
              size="sm"
              variant="danger"
              label={`Delete mapping ${n.name} ${m.remotePath}`}
              onClick={() => setToDelete(m)}
            />
          </span>
        );
      },
    },
  ];

  return (
    <SettingsSection
      title="Path Mappings"
      description="Translate paths reported by Plex or Radarr/Sonarr into paths Dupearr can reach."
      actions={
        <Button size="sm" icon={Plus} onClick={() => open(null)}>
          Add Mapping
        </Button>
      }
    >
      <PathExample />
      {mappings.error ? (
        <Alert kind="error" title="Could not load path mappings">
          {errorMessage(mappings.error)}
        </Alert>
      ) : (
        <Table
          aria-label="Path mappings"
          columns={columns}
          rows={mappings.data ?? []}
          getRowId={(m) => m.id}
          loading={mappings.isPending}
          emptyState={
            <EmptyState
              compact
              icon={FolderSync}
              title="No path mappings"
              description="Without mappings Dupearr can still remove files through Radarr/Sonarr or Plex, but not directly on disk."
            />
          }
        />
      )}

      {editing && (
        <PathMappingModal
          key={editing.key}
          open
          initial={editing.draft}
          servers={serverList}
          arrInstances={arrList}
          libraries={libraries.data ?? []}
          onClose={() => setEditing(null)}
        />
      )}
      <ConfirmDialog
        open={!!toDelete}
        title="Delete Path Mapping"
        message={
          toDelete ? (
            <>
              Delete the mapping <code>{toDelete.remotePath}</code> → <code>{toDelete.localPath}</code>? Filesystem
              removals for these files will stop working until it is re-added.
            </>
          ) : null
        }
        confirmLabel="Delete"
        loading={del.isPending}
        onCancel={() => setToDelete(null)}
        onConfirm={() => {
          if (!toDelete) return;
          del.mutate(toDelete.id, {
            onSuccess: () => {
              toast.success('Path mapping deleted');
              setToDelete(null);
            },
            onError: (e) => {
              toast.error('Could not delete the path mapping', errorMessage(e));
              setToDelete(null);
            },
          });
        }}
      />
    </SettingsSection>
  );
}
