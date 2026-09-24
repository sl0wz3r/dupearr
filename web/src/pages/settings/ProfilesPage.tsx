import { Plus, RefreshCw } from 'lucide-react';
import { useMemo, useState } from 'react';
import { errorMessage } from '@/api/client';
import { useArrInstances, useLibraries, useProfileSchema, useProfiles } from '@/api/hooks';
import type { Profile } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  ToolbarButton,
} from '@/components/page';
import { blankDraft, draftFromTemplate, normalizeProfile, schemaMap, type ProfileDraft } from '@/components/settings/rules/criteria';
import { ProfileCard, TemplatePickerModal } from '@/components/settings/rules/ProfileCards';
import { ProfileEditorModal } from '@/components/settings/rules/ProfileEditorModal';
import { AddCard, Alert, Button, CardGrid, LoadingIndicator } from '@/components/ui';

interface EditorState {
  /** Remounts the editor for every open (fresh local state). */
  key: number;
  draft: ProfileDraft;
  storedIsDefault: boolean;
}

/** Default profile first, then by name. */
function sortProfiles(profiles: readonly Profile[]): Profile[] {
  return [...profiles].sort((a, b) =>
    a.isDefault === b.isDefault ? a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }) : a.isDefault ? -1 : 1,
  );
}

/**
 * Settings → Profiles at `/settings/profiles`: decision profile cards, "Add Profile" from a
 * template, and the editor (criteria builder, keep options, protections).
 */
export default function ProfilesPage() {
  const profiles = useProfiles();
  const schema = useProfileSchema();
  const libraries = useLibraries();
  const arr = useArrInstances();

  const [pickerOpen, setPickerOpen] = useState(false);
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [nextKey, setNextKey] = useState(1);

  const smap = useMemo(() => schemaMap(schema.data), [schema.data]);
  const list = useMemo(() => sortProfiles(profiles.data ?? []), [profiles.data]);
  const names = useMemo(() => list.map((p) => p.name), [list]);

  const openEditor = (draft: ProfileDraft, storedIsDefault = false) => {
    setEditor({ key: nextKey, draft, storedIsDefault });
    setNextKey((k) => k + 1);
  };

  const loading = profiles.isPending || schema.isPending;
  const loadError = profiles.error ?? schema.error;

  return (
    <PageContent title="Profiles">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={Plus} label="Add Profile" onClick={() => setPickerOpen(true)} disabled={!schema.data} />
          <ToolbarButton
            icon={RefreshCw}
            label="Refresh"
            spinning={profiles.isFetching}
            onClick={() => {
              void profiles.refetch();
              void schema.refetch();
            }}
          />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader
          title="Profiles"
          subtitle="Profiles decide which copy of a duplicate to keep. Each library uses its assigned profile, or the default one (assign profiles under Settings → Media Servers)."
        />
        {loadError ? (
          <Alert
            kind="error"
            title="Could not load profiles"
            actions={
              <Button
                size="sm"
                onClick={() => {
                  void profiles.refetch();
                  void schema.refetch();
                }}
              >
                Retry
              </Button>
            }
          >
            {errorMessage(loadError)}
          </Alert>
        ) : loading ? (
          <LoadingIndicator message="Loading profiles…" />
        ) : (
          <CardGrid>
            {list.map((p) => (
              <ProfileCard
                key={p.id}
                profile={p}
                schema={smap}
                libraries={libraries.data ?? []}
                onEdit={() => openEditor(normalizeProfile(p), p.isDefault)}
              />
            ))}
            <AddCard label="Add Profile" onClick={() => setPickerOpen(true)} />
          </CardGrid>
        )}
      </PageBody>

      <TemplatePickerModal
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        templates={schema.data?.templates ?? []}
        schema={smap}
        onPick={(template) => {
          setPickerOpen(false);
          openEditor(template ? draftFromTemplate(template, names) : blankDraft(smap, names));
        }}
      />

      {editor && (
        <ProfileEditorModal
          key={editor.key}
          open
          initial={editor.draft}
          storedIsDefault={editor.storedIsDefault}
          schema={schema.data}
          libraries={libraries.data ?? []}
          arrInstances={arr.data ?? []}
          existingNames={names}
          onClose={() => setEditor(null)}
          onClone={(draft) => openEditor(draft)}
        />
      )}
    </PageContent>
  );
}
