import { Clapperboard, Plus, Tv } from 'lucide-react';
import { useState } from 'react';
import { Link } from 'react-router';
import { errorMessage } from '@/api/client';
import { useArrInstances } from '@/api/hooks/useArr';
import type { ArrInstance } from '@/api/types';
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
import { ArrInstanceModal } from '@/components/settings/connections/ArrInstanceModal';
import { ConnectionCard } from '@/components/settings/connections/ConnectionCard';
import { WebhookInfo } from '@/components/settings/connections/WebhookInfo';
import { AddCard, Alert, Badge, Button, CardGrid, LoadingIndicator } from '@/components/ui';
import { ARR_KIND_LABELS, labelOf } from '@/lib/constants';

type ModalState = { instance: ArrInstance | null } | null;

/**
 * Settings → Applications at `/settings/applications`: Radarr/Sonarr instance cards with an
 * add/edit modal (Test shows version + recycle bin status), and the optional webhook URLs.
 * Path mappings live under Settings → Media Management.
 */
export default function ApplicationsPage() {
  const instances = useArrInstances();
  const [modal, setModal] = useState<ModalState>(null);
  const list = instances.data ?? [];

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
          subtitle="Connect Radarr and Sonarr to enrich files with quality data and delete through them."
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

        <SettingsSection title="Webhook (optional)">
          <WebhookInfo sources={['radarr', 'sonarr']} />
        </SettingsSection>
      </PageBody>

      {modal && <ArrInstanceModal instance={modal.instance} onClose={() => setModal(null)} />}
    </PageContent>
  );
}
