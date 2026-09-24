import { Bell, Plus } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '@/api/client';
import { useNotifications, useNotificationSchema, useNotificationTriggers } from '@/api/hooks/useNotifications';
import type { NotificationConfig } from '@/api/types';
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
import { NotificationModal } from '@/components/settings/connections/NotificationModal';
import { triggersSummary } from '@/components/settings/connections/notificationForm';
import { AddCard, Alert, Badge, Button, CardGrid, LoadingIndicator } from '@/components/ui';
import { labelOf, NOTIFICATION_KIND_LABELS, NOTIFICATION_TRIGGER_LABELS } from '@/lib/constants';

type ModalState = { config: NotificationConfig | null } | null;

/**
 * Settings → Connect at `/settings/connect`: notification connection cards and an add/edit modal
 * whose provider fields are rendered from GET /notification/schema.
 */
export default function ConnectPage() {
  const notifications = useNotifications();
  const schemas = useNotificationSchema();
  const triggers = useNotificationTriggers();
  const [modal, setModal] = useState<ModalState>(null);
  const list = notifications.data ?? [];

  const providerName = (kind: string) =>
    schemas.data?.find((s) => s.kind === kind)?.name || labelOf(NOTIFICATION_KIND_LABELS, kind);
  const triggerLabel = (value: string) =>
    triggers.data?.find((t) => t.value === value)?.label || labelOf(NOTIFICATION_TRIGGER_LABELS, value);

  return (
    <PageContent title="Connect">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={Plus} label="Add" onClick={() => setModal({ config: null })} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader
          title="Connect"
          subtitle="Get notified about new duplicates, deletions, failures and health issues."
        />
        <SettingsSection title="Connections">
          {notifications.isPending ? (
            <LoadingIndicator message="Loading connections…" />
          ) : notifications.isError ? (
            <Alert
              kind="error"
              title="Unable to load connections"
              actions={
                <Button size="sm" onClick={() => void notifications.refetch()}>
                  Retry
                </Button>
              }
            >
              {errorMessage(notifications.error)}
            </Alert>
          ) : (
            <CardGrid>
              {list.map((n) => (
                <ConnectionCard
                  key={n.id}
                  name={n.name}
                  icon={Bell}
                  subtitle={providerName(n.kind)}
                  enabled={n.enabled}
                  onClick={() => setModal({ config: n })}
                  badges={
                    (n.triggers ?? []).length === 0 ? (
                      <Badge kind="warning" outline>
                        No triggers
                      </Badge>
                    ) : undefined
                  }
                >
                  {(n.triggers ?? []).length > 0 && (
                    <div className="text-xs text-muted" title={(n.triggers ?? []).map(triggerLabel).join(', ')}>
                      {triggersSummary(n.triggers, triggerLabel)}
                    </div>
                  )}
                </ConnectionCard>
              ))}
              <AddCard label="Add connection" onClick={() => setModal({ config: null })} />
            </CardGrid>
          )}
        </SettingsSection>
      </PageBody>

      {modal && <NotificationModal config={modal.config} onClose={() => setModal(null)} />}
    </PageContent>
  );
}
