import { useRef, type ReactNode, type RefObject } from 'react';
import { Button, type ButtonVariant } from './Button';
import { Modal } from './Modal';

export interface ConfirmDialogProps {
  open: boolean;
  title: ReactNode;
  /** Body text / content. */
  message: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Colour of the confirm button (default "danger"). */
  kind?: Extract<ButtonVariant, 'danger' | 'primary' | 'success' | 'warning'>;
  /** Shows a spinner on the confirm button and blocks closing. */
  loading?: boolean;
  /** Optional extra content under the message (e.g. an "add exclusion" checkbox). */
  children?: ReactNode;
  /** Element focused when the dialog opens (default: the Cancel button). */
  initialFocusRef?: RefObject<HTMLElement | null>;
}

/**
 * Confirmation modal. The Cancel button gets initial focus (safe default for destructive actions).
 * @example <ConfirmDialog open={!!toDelete} title="Delete Backup" message="Are you sure?" onConfirm={del} onCancel={close} />
 */
export function ConfirmDialog({
  open,
  title,
  message,
  onConfirm,
  onCancel,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  kind = 'danger',
  loading = false,
  children,
  initialFocusRef,
}: ConfirmDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  return (
    <Modal
      open={open}
      onClose={() => {
        if (!loading) onCancel();
      }}
      title={title}
      size="sm"
      initialFocusRef={initialFocusRef ?? cancelRef}
      footer={
        <>
          <Button ref={cancelRef} onClick={onCancel} disabled={loading}>
            {cancelLabel}
          </Button>
          <Button variant={kind} onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-fg">{message}</div>
      {children && <div className="mt-4">{children}</div>}
    </Modal>
  );
}
