import { RotateCcw } from 'lucide-react';
import type { RestoreChange, RestoreSummary } from '@/api/types';
import { Alert, Badge, Button, Modal } from '@/components/ui';

/** Labels of the settings a restore reports (backup.RestoreChange.setting). */
const SETTING_LABELS: Record<string, string> = {
  dryRun: 'Dry Run',
  mode: 'Mode',
  deletionMethods: 'Deletion Methods',
  recycleBinPath: 'Recycle Bin',
  recycleBinCleanupDays: 'Recycle Bin Cleanup (days)',
  minAgeHours: 'Minimum Age (hours)',
  maxDeletionsPerRun: 'Max Deletions per Run',
  maxBytesPerRunGb: 'Max GB per Run',
  stableScansRequired: 'Stable Scans Required',
  detectDiscs: 'Detect Full-Disc Backups',
  allowDiscRemoval: 'Allow Removing Full Discs',
  keepPlayableCopy: 'Always Keep a Plex-Playable Copy',
  historyRetentionDays: 'History Retention (days)',
  logLevel: 'Log Level',
  logSizeLimit: 'Log File Size Limit (MB)',
  mediaServers: 'Media Servers',
  arrInstances: 'Applications',
  tautulliInstances: 'Tautulli (Watch History)',
  pathMappings: 'Path Mappings',
  notifications: 'Notifications',
  notificationDestinations: 'Notification Destinations',
  users: 'User Account',
  webhookToken: 'Webhook Token',
  apiKey: 'API Key',
  authenticationMethod: 'Authentication',
  authenticationRequired: 'Authentication Required',
  trustedProxies: 'Trusted Proxies',
  allowedHosts: 'Allowed Hosts',
  bindAddress: 'Bind Address',
  port: 'Port',
  urlBase: 'URL Base',
  enableSsl: 'SSL',
  sslPort: 'SSL Port',
  sslCertPath: 'SSL Certificate',
  sslKeyPath: 'SSL Key',
};

/** Settings whose restored value decides which files are removed and how. */
const REMOVAL_SETTINGS = new Set([
  'dryRun',
  'mode',
  'deletionMethods',
  'recycleBinPath',
  'recycleBinCleanupDays',
  'minAgeHours',
  'maxDeletionsPerRun',
  'maxBytesPerRunGb',
  'stableScansRequired',
  'detectDiscs',
  'allowDiscRemoval',
  'keepPlayableCopy',
  'mediaServers',
  'arrInstances',
  // Play history ranks copies when a profile uses Played / Last played (docs/DECISIONS.md D10).
  'tautulliInstances',
  'pathMappings',
]);

export function restoreSettingLabel(setting: string): string {
  return SETTING_LABELS[setting] ?? setting;
}

function Value({ value }: { value: string }) {
  if (!value) return <span className="text-muted">—</span>;
  return <span className="font-mono text-xs break-all whitespace-pre-wrap">{value}</span>;
}

function ChangeRow({ change }: { change: RestoreChange }) {
  return (
    <tr className="border-t border-border align-top">
      <th scope="row" className="py-2 pr-3 text-left text-sm font-semibold text-fg-strong">
        {restoreSettingLabel(change.setting)}
        {REMOVAL_SETTINGS.has(change.setting) && (
          <div className="mt-0.5 text-xs font-normal text-warning">decides removals</div>
        )}
      </th>
      <td className="py-2 pr-3">
        <Value value={change.current} />
      </td>
      <td className="py-2 pr-3">
        <Value value={change.backup} />
      </td>
      <td className="py-2">
        {change.applied ? <Badge kind="warning">From backup</Badge> : <Badge kind="info">Kept</Badge>}
        {change.message && <div className="mt-1 text-xs text-muted">{change.message}</div>}
      </td>
    </tr>
  );
}

export interface RestoreReviewDialogProps {
  /** The staged restore's summary (null: closed). */
  summary: RestoreSummary | null;
  /** Name of the backup (stored backup or uploaded file). */
  name?: string;
  confirming: boolean;
  discarding: boolean;
  error?: string | null;
  onConfirm: () => void;
  onDiscard: () => void;
}

/**
 * Review step of a restore: the backup is staged (nothing replaced yet) and the settings and
 * connections that differ are listed — above all those that decide which files are removed — before
 * the admin confirms (Dupearr restarts to apply it) or discards it.
 */
export function RestoreReviewDialog({ summary, name, confirming, discarding, error, onConfirm, onDiscard }: RestoreReviewDialogProps) {
  const changes = summary?.changes ?? [];
  const removal = changes.filter((c) => REMOVAL_SETTINGS.has(c.setting));
  const other = changes.filter((c) => !REMOVAL_SETTINGS.has(c.setting));
  const busy = confirming || discarding;
  return (
    <Modal
      open={summary !== null}
      onClose={() => {
        if (!busy) onDiscard();
      }}
      title="Review Restore"
      size="lg"
      closeOnBackdrop={false}
      hideCloseButton={busy}
      footer={
        <>
          <Button onClick={onDiscard} loading={discarding} disabled={confirming}>
            Discard
          </Button>
          <Button variant="warning" icon={RotateCcw} onClick={onConfirm} loading={confirming} disabled={discarding}>
            Restore and Restart
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <p className="m-0">
          {name ? (
            <>
              <strong className="font-mono text-xs break-all text-fg-strong">{name}</strong> is ready to be restored.
            </>
          ) : (
            'The backup is ready to be restored.'
          )}{' '}
          Nothing has been replaced yet: review what changes, then restore (Dupearr restarts) or discard it.
        </p>
        {error && <Alert kind="error">{error}</Alert>}
        <Alert kind="info" title="Dry run stays on">
          After a restore, Dupearr does not remove files until you turn dry run off again in Settings → Media
          Management — check the restored connections, path mappings and deletion settings first. Your current
          authentication, API key, account, webhook token and listener are kept.
        </Alert>
        {(summary?.cancelledRemovals ?? 0) + (summary?.interruptedRemovals ?? 0) + (summary?.reopenedGroups ?? 0) > 0 && (
          <p className="m-0 text-sm text-muted">
            {summary?.cancelledRemovals ?? 0} queued and {summary?.interruptedRemovals ?? 0} running removal(s) of the
            backup are cancelled, and {summary?.reopenedGroups ?? 0} queued duplicate(s) go back to approval.
          </p>
        )}
        {changes.length === 0 ? (
          <p className="m-0 text-sm text-muted">The backup&apos;s settings and connections match the current ones.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted">
                  <th scope="col" className="pb-1 pr-3 font-semibold">Setting</th>
                  <th scope="col" className="pb-1 pr-3 font-semibold">Current</th>
                  <th scope="col" className="pb-1 pr-3 font-semibold">Backup</th>
                  <th scope="col" className="pb-1 font-semibold">After the restore</th>
                </tr>
              </thead>
              <tbody>
                {[...removal, ...other].map((c) => (
                  <ChangeRow key={c.setting} change={c} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Modal>
  );
}
