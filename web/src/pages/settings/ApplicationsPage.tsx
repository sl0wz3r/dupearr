import { Clapperboard, History, Plus, Tv } from 'lucide-react';
import { useState } from 'react';
import { Link } from 'react-router';
import { errorMessage } from '@/api/client';
import { useArrInstances } from '@/api/hooks/useArr';
import { useMediaServers } from '@/api/hooks/useMediaServers';
import { useTautulliInstances } from '@/api/hooks/useTautulli';
import type { ArrInstance, TautulliInstance } from '@/api/types';
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
import { ArrInstanceModal, linksConfirmedFor } from '@/components/settings/connections/ArrInstanceModal';
import { ConnectionCard } from '@/components/settings/connections/ConnectionCard';
import { TautulliModal } from '@/components/settings/connections/TautulliModal';
import { WebhookInfo } from '@/components/settings/connections/WebhookInfo';
import { AddCard, Alert, Badge, Button, CardGrid, LoadingIndicator } from '@/components/ui';
import { ARR_KIND_LABELS, labelOf } from '@/lib/constants';

type ModalState = { instance: ArrInstance | null } | null;
type TautulliModalState = { instance: TautulliInstance | null } | null;

/**
 * Settings → Applications at `/settings/applications`: Radarr/Sonarr instance cards with an
 * add/edit modal (Test shows version + recycle bin status), the Tautulli connections that provide
 * play history (docs/DECISIONS.md D10), and the optional webhook URLs. Path mappings live under
 * Settings → Media Management.
 */
export default function ApplicationsPage() {
  const instances = useArrInstances();
  const tautullis = useTautulliInstances();
  const servers = useMediaServers();
  const [modal, setModal] = useState<ModalState>(null);
  const [tautulliModal, setTautulliModal] = useState<TautulliModalState>(null);
  const list = instances.data ?? [];
  const serverList = servers.data ?? [];
  const serverName = (id: number) => serverList.find((s) => s.id === id)?.name ?? `Media server ${id}`;
  // Links confirmed without an enabled server count as not confirmed (docs/DECISIONS.md D11).
  const enabledLinks = (a: ArrInstance) => (a.serverIds ?? []).filter((id) => serverList.some((s) => s.id === id && s.enabled));

  return (
    <PageContent title="Applications">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={Plus} label="Add" onClick={() => setModal({ instance: null })} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader
          title="Applications"
          subtitle="Connect Radarr and Sonarr to enrich files with quality data and delete through them, and Tautulli for play history."
        />

        <SettingsSection title="Applications">
          {instances.isPending ? (
            <LoadingIndicator message="Loading applications…" />
          ) : instances.isError ? (
            <Alert
              kind="error"
              title="Unable to load applications"
              actions={
                <Button size="sm" onClick={() => void instances.refetch()}>
                  Retry
                </Button>
              }
            >
              {errorMessage(instances.error)}
            </Alert>
          ) : (
            <CardGrid>
              {list.map((a) => (
                <ConnectionCard
                  key={a.id}
                  name={a.name}
                  icon={a.kind === 'sonarr' ? Tv : Clapperboard}
                  subtitle={<span className="font-mono text-xs">{a.url}</span>}
                  enabled={a.enabled}
                  onClick={() => setModal({ instance: a })}
                  badges={
                    <>
                      <Badge kind="accent" outline>
                        {labelOf(ARR_KIND_LABELS, a.kind)}
                      </Badge>
                      {(a.tags ?? []).map((t) => (
                        <Badge key={t} kind="info" outline>
                          {t}
                        </Badge>
                      ))}
                      {serverList.length >= 2 && !linksConfirmedFor({ ...a, serverIds: enabledLinks(a) }) && (
                        <Badge kind="warning" outline title="Choose the media servers this application feeds">
                          Media servers not confirmed
                        </Badge>
                      )}
                    </>
                  }
                />
              ))}
              <AddCard label="Add application" onClick={() => setModal({ instance: null })} />
            </CardGrid>
          )}
          <p className="mt-3 mb-0 text-sm text-muted">
            Paths that differ between Radarr/Sonarr, Plex and Dupearr are translated with path mappings under{' '}
            <Link to="/settings/mediamanagement" className="text-accent-soft underline">
              Settings → Media Management
            </Link>
            .
          </p>
        </SettingsSection>

        <SettingsSection title="Watch history">
          <p className="mt-0 mb-3 text-sm text-muted">
            Optional. Connect Tautulli to rank copies by their play history with the Played and Last played profile
            criteria. A copy whose history is unknown or cannot be read always counts as unknown — never as “not
            played”.
          </p>
          {/* The media servers are needed too: each connection names its server, and the modal picks one. */}
          {tautullis.isPending || servers.isPending ? (
            <LoadingIndicator message="Loading Tautulli connections…" />
          ) : tautullis.isError || servers.isError ? (
            <Alert
              kind="error"
              title={tautullis.isError ? 'Unable to load Tautulli connections' : 'Unable to load media servers'}
              actions={
                <Button
                  size="sm"
                  onClick={() => {
                    if (tautullis.isError) void tautullis.refetch();
                    if (servers.isError) void servers.refetch();
                  }}
                >
                  Retry
                </Button>
              }
            >
              {errorMessage(tautullis.isError ? tautullis.error : servers.error)}
            </Alert>
          ) : (
            <CardGrid>
              {(tautullis.data ?? []).map((t) => (
                <ConnectionCard
                  key={t.id}
                  name={t.name}
                  icon={History}
                  subtitle={<span className="font-mono text-xs">{t.url}</span>}
                  enabled={t.enabled}
                  onClick={() => setTautulliModal({ instance: t })}
                  badges={
                    <>
                      <Badge kind="accent" outline>
                        Tautulli
                      </Badge>
                      <Badge kind="info" outline>
                        {serverName(t.serverId)}
                      </Badge>
                    </>
                  }
                />
              ))}
              <AddCard label="Add Tautulli" onClick={() => setTautulliModal({ instance: null })} />
            </CardGrid>
          )}
        </SettingsSection>

        <SettingsSection title="Webhook (optional)">
          <WebhookInfo sources={['radarr', 'sonarr']} />
        </SettingsSection>
      </PageBody>

      {modal && <ArrInstanceModal instance={modal.instance} onClose={() => setModal(null)} />}
      {tautulliModal && (
        <TautulliModal instance={tautulliModal.instance} servers={serverList} onClose={() => setTautulliModal(null)} />
      )}
    </PageContent>
  );
}
