import { useIsCommandActive } from '@/api/hooks';
import { Alert } from '@/components/ui';

/**
 * Safety notes shown before a backup restore (stored backup or uploaded file):
 * - removals queued or running when the backup was made are cancelled, and dry run stays on;
 * - a removal run (ProcessQueue) in progress would be interrupted by the restart.
 */
export function RestoreSafetyNotes() {
  const processing = useIsCommandActive('ProcessQueue');
  return (
    <>
      {processing && (
        <Alert kind="error" title="Removals are running right now">
          Restoring restarts Dupearr and interrupts the removals in progress. Wait until Process Queue has
          finished (System → Tasks) before restoring.
        </Alert>
      )}
      <p className="m-0 text-xs text-muted">
        Removals that were queued when the backup was made are cancelled, and dry run stays on after the restore until
        you have reviewed the restored settings and turn it off.
      </p>
    </>
  );
}
