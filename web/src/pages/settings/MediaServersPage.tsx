import { Plus, Server } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '@/api/client';
import { useLibraries, useMediaServers } from '@/api/hooks/useMediaServers';
import { useProfiles } from '@/api/hooks/useProfiles';
import type { MediaServer } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  SettingsSection,
  ToolbarButton,
} from '@/components/page';
import { ConnectionCard } from '@/components/settings/connections/ConnectionCard';
import { LibrariesTable } from '@/components/settings/connections/LibrariesTable';
import { MediaServerModal } from '@/components/settings/connections/MediaServerModal';
import { WebhookInfo } from '@/components/settings/connections/WebhookInfo';
import { AddCard, Alert, Badge, Button, CardGrid, LoadingIndicator } from '@/components/ui';

/** Which modal is open: a new server, or the server being edited. */
type ModalState = { server: MediaServer | null } | null;

/**
 * Settings → Media Servers at `/settings/mediaservers`: Plex server cards (+ add/edit modal with
 * "Sign in with Plex"), the libraries of every server (enable, profile, scope group, sync) and the
 * optional Plex webhook URL.
 */
export default function MediaServersPage() {
  const servers = useMediaServers();
  const libraries = useLibraries();
  const profiles = useProfiles();
  const [modal, setModal] = useState<ModalState>(null);

  const list = servers.data ?? [];
  const libraryCounts = new Map<number, { total: number; enabled: number }>();
  for (const lib of libraries.data ?? []) {
    const c = libraryCounts.get(lib.serverId) ?? { total: 0, enabled: 0 };
    c.total += 1;
    if (lib.enabled) c.enabled += 1;
    libraryCounts.set(lib.serverId, c);
  }

  return (
    <PageContent title="Media Servers">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={Plus} label="Add Server" onClick={() => setModal({ server: null })} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="Media Servers" subtitle="Connect Plex and choose which libraries Dupearr scans." />

        <SettingsSection title="Media Servers">
          {servers.isPending ? (
            <LoadingIndicator message="Loading media servers…" />
          ) : servers.isError ? (
            <Alert
              kind="error"
              title="Unable to load media servers"
              actions={
                <Button size="sm" onClick={() => void servers.refetch()}>
                  Retry
                </Button>
              }
            >
              {errorMessage(servers.error)}
            </Alert>
          ) : (
            <CardGrid>
              {list.map((s) => {
                const counts = libraryCounts.get(s.id);
                return (
                  <ConnectionCard
                    key={s.id}
                    name={s.name}
                    icon={Server}
                    subtitle={<span className="font-mono text-xs">{s.url}</span>}
                    enabled={s.enabled}
                    onClick={() => setModal({ server: s })}
                    badges={
                      <>
                        <Badge kind="accent" outline>
                          Plex
                        </Badge>
                        <Badge kind="default" outline>
                          {counts
                            ? `${counts.enabled}/${counts.total} ${counts.total === 1 ? 'library' : 'libraries'}`
                            : 'No libraries'}
                        </Badge>
                      </>
                    }
                  >
                    {s.machineIdentifier && (
                      <div className="truncate text-xs text-muted" title={s.machineIdentifier}>
                        Machine ID: <span className="font-mono">{s.machineIdentifier}</span>
                      </div>
                    )}
                  </ConnectionCard>
                );
              })}
              <AddCard label="Add media server" onClick={() => setModal({ server: null })} />
            </CardGrid>
          )}
        </SettingsSection>

        {list.length > 0 && (
          <SettingsSection
            title="Libraries"
            description="Choose which libraries are scanned and which decision profile each one uses. Libraries with the same scope group are compared with each other, e.g. 'movies' for Movies + Movies 4K."
          >
            {profiles.isError && (
              <Alert kind="warning" className="mb-3">
                Unable to load profiles ({errorMessage(profiles.error)}); only “Default” is available.
              </Alert>
            )}
            {list.map((s) => (
              <LibrariesTable key={s.id} server={s} profiles={profiles.data ?? []} />
            ))}
          </SettingsSection>
        )}

        {list.length > 0 && (
          <SettingsSection title="Webhook (optional)" advanced>
            <WebhookInfo sources={['plex']} />
          </SettingsSection>
        )}
      </PageBody>

      {modal && <MediaServerModal server={modal.server} onClose={() => setModal(null)} />}
    </PageContent>
  );
}
