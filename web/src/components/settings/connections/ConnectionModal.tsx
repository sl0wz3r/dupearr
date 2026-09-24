import { FlaskConical, Save, Trash2 } from 'lucide-react';
import { useId, useState, type ReactNode } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { Alert, Button, ConfirmDialog, Modal, Spinner, type ModalSize } from '@/components/ui';

export interface ConnectionModalProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  size?: ModalSize;
  children: ReactNode;
  /** Save handler; also runs when Enter is pressed in a field. Omit to hide Save (e.g. a picker step). */
  onSave?: () => void;
  saving?: boolean;
  saveDisabled?: boolean;
  /** Test handler; omit to hide the Test button. */
  onTest?: () => void;
  testing?: boolean;
  /** Delete handler (edit mode) — runs after the user confirms. Omit to hide Delete. */
  onDelete?: () => void;
  deleting?: boolean;
  /** Confirmation dialog title/message for Delete. */
  deleteTitle?: string;
  deleteMessage?: ReactNode;
  /** Extra content on the left of the footer (e.g. a "Back" button). */
  footerStart?: ReactNode;
}

/**
 * Standard *arr connection editor: body in a <form> (Enter saves), footer with
 * Delete (left, confirmed) and Test / Cancel / Save (right). Closing is blocked while a save or
 * delete is in flight so results aren't lost.
 */
export function ConnectionModal({
  open,
  onClose,
  title,
  size = 'lg',
  children,
  onSave,
  saving = false,
  saveDisabled = false,
  onTest,
  testing = false,
  onDelete,
  deleting = false,
  deleteTitle = 'Delete',
  deleteMessage = 'Are you sure you want to delete this connection?',
  footerStart,
}: ConnectionModalProps) {
  const formId = useId();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const busy = saving || deleting;
  const close = () => {
    if (!busy) onClose();
  };

  return (
    <>
      <Modal
        open={open}
        onClose={close}
        title={title}
        size={size}
        closeOnBackdrop={!busy}
        footerStart={
          <>
            {onDelete && (
              <Button variant="danger" icon={Trash2} onClick={() => setConfirmDelete(true)} disabled={busy}>
                Delete
              </Button>
            )}
            {footerStart}
          </>
        }
        footer={
          <>
            {onTest && (
              <Button icon={FlaskConical} onClick={onTest} loading={testing} disabled={busy}>
                Test
              </Button>
            )}
            <Button onClick={close} disabled={busy}>
              Cancel
            </Button>
            {onSave && (
              <Button
                type="submit"
                form={formId}
                variant="primary"
                icon={Save}
                loading={saving}
                disabled={saveDisabled || deleting}
              >
                Save
              </Button>
            )}
          </>
        }
      >
        <form
          id={formId}
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (onSave && !busy && !saveDisabled) onSave();
          }}
        >
          {children}
        </form>
      </Modal>
      {onDelete && (
        <ConfirmDialog
          open={confirmDelete}
          title={deleteTitle}
          message={deleteMessage}
          confirmLabel="Delete"
          kind="danger"
          loading={deleting}
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() => {
            setConfirmDelete(false);
            onDelete();
          }}
        />
      )}
    </>
  );
}

export interface TestResultProps {
  /** A test is running. */
  pending: boolean;
  /** Failure of the last test (ApiError or any thrown value). */
  error?: unknown;
  /** Rendered (as a success alert) when the last test succeeded. */
  success?: ReactNode;
  /** Title of the success alert. */
  successTitle?: ReactNode;
  className?: string;
}

/** Inline result of the Test button (live region so screen readers announce it). */
export function TestResult({ pending, error, success, successTitle = 'Test succeeded', className }: TestResultProps) {
  let content: ReactNode = null;
  if (pending) {
    content = (
      <div className="flex items-center gap-2 text-sm text-muted">
        <Spinner size="sm" label="Testing" />
        Testing connection…
      </div>
    );
  } else if (error) {
    const description = isApiError(error) && error.description !== error.message ? error.description : undefined;
    content = (
      <Alert kind="error" title="Test failed">
        <div className="break-words">{errorMessage(error, 'Unknown error')}</div>
        {description && <div className="mt-1 text-muted">{description}</div>}
      </Alert>
    );
  } else if (success) {
    content = (
      <Alert kind="success" title={successTitle}>
        {success}
      </Alert>
    );
  }
  return (
    <div aria-live="polite" className={className}>
      {content}
    </div>
  );
}

export interface SaveErrorAlertProps {
  /** General (non-field) error messages of the last save. */
  messages: readonly string[];
  /** When set, a "Save anyway" button retries with forceSave. */
  onForceSave?: () => void;
  forcing?: boolean;
  className?: string;
}

/** Save failure summary with an optional "Save anyway" (skip the connection test) action. */
export function SaveErrorAlert({ messages, onForceSave, forcing = false, className }: SaveErrorAlertProps) {
  if (messages.length === 0) return null;
  return (
    <Alert
      kind="error"
      title="Unable to save"
      className={className}
      actions={
        onForceSave ? (
          <Button size="sm" variant="warning" onClick={onForceSave} loading={forcing}>
            Save anyway
          </Button>
        ) : undefined
      }
    >
      {messages.map((m, i) => (
        <div key={i} className="break-words">
          {m}
        </div>
      ))}
      {onForceSave && (
        <div className="mt-1 text-muted">
          “Save anyway” skips the connection test. Dupearr will keep retrying and report problems under System → Status.
        </div>
      )}
    </Alert>
  );
}

/** Small "label: value" grid used in test results and cards. */
export function DetailList({ items }: { items: readonly { label: string; value: ReactNode }[] }) {
  const shown = items.filter((i) => i.value !== null && i.value !== undefined && i.value !== '');
  if (shown.length === 0) return null;
  return (
    <dl className="m-0 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sm">
      {shown.map((i) => (
        <div key={i.label} className="contents">
          <dt className="text-muted">{i.label}</dt>
          <dd className="m-0 min-w-0 break-words text-fg">{i.value}</dd>
        </div>
      ))}
    </dl>
  );
}
