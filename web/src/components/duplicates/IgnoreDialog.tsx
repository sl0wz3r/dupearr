import { useEffect, useState } from 'react';
import { Checkbox, ConfirmDialog } from '@/components/ui';

export interface IgnoreDialogProps {
  open: boolean;
  /** Display title of the group. */
  title: string;
  loading?: boolean;
  onConfirm: (addExclusion: boolean) => void;
  onCancel: () => void;
}

/** Ignore one group, optionally adding an exclusion so future scans skip it entirely. */
export function IgnoreDialog({ open, title, loading = false, onConfirm, onCancel }: IgnoreDialogProps) {
  const [addExclusion, setAddExclusion] = useState(false);
  useEffect(() => {
    if (open) setAddExclusion(false);
  }, [open]);

  return (
    <ConfirmDialog
      open={open}
      title="Ignore duplicate"
      kind="primary"
      confirmLabel="Ignore"
      loading={loading}
      onConfirm={() => onConfirm(addExclusion)}
      onCancel={onCancel}
      message={
        <>
          Ignore <strong className="text-fg-strong">{title}</strong>? Nothing is deleted; the group stays ignored across
          scans until you unignore it.
        </>
      }
    >
      <Checkbox
        checked={addExclusion}
        onChange={setAddExclusion}
        disabled={loading}
        label="Also add an exclusion"
        description="Future scans skip this title entirely (manage it in Settings → Exclusions)."
      />
    </ConfirmDialog>
  );
}
