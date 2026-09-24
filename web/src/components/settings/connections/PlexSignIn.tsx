import { clsx } from 'clsx';
import { CircleCheck, ExternalLink, LogIn, RefreshCw, Server, TriangleAlert } from 'lucide-react';
import { useCallback, useEffect, useId, useReducer, useRef, useState } from 'react';
import { errorMessage, isApiError } from '@/api/client';
import { useCreatePlexPin, usePlexPin, usePlexServers } from '@/api/hooks/useMediaServers';
import type { PlexConnection, PlexServer } from '@/api/types';
import { Alert, Badge, Button, Spinner } from '@/components/ui';
import {
  formatCountdown,
  isTrustedPlexAuthUrl,
  PLEX_POPUP_NAME,
  PLEX_SIGN_IN_INITIAL,
  plexServerToken,
  plexSignInReducer,
  popupFeatures,
  rankConnections,
  remainingMs,
  sortPlexServers,
  toSelection,
  type PlexServerSelection,
  type PlexSignInState,
} from './plexSignInMachine';

export interface PlexSignInController {
  state: PlexSignInState;
  /** Opens the popup (must run inside a click handler) and creates a PIN. */
  start: () => void;
  /** Abandons the current attempt and closes the popup. */
  cancel: () => void;
  /** Back to idle from any step (e.g. "Start over"). */
  reset: () => void;
}

/**
 * Drives the plex.tv PIN sign-in: opens a popup synchronously (popup blockers only allow windows
 * opened during a user gesture), creates the PIN, navigates the popup to plex.tv, polls the PIN
 * until it is claimed, times out after 5 minutes and closes the popup when done.
 */
export function usePlexSignIn(): PlexSignInController {
  const [state, dispatch] = useReducer(plexSignInReducer, PLEX_SIGN_IN_INITIAL);
  const createPin = useCreatePlexPin();
  const popupRef = useRef<Window | null>(null);
  /** Incremented on every start/cancel/reset so late async results of old attempts are dropped. */
  const attemptRef = useRef(0);

  const waiting = state.step === 'waiting';
  const pin = usePlexPin(waiting ? state.pinId : null, waiting);

  const closePopup = useCallback(() => {
    const w = popupRef.current;
    popupRef.current = null;
    try {
      if (w && !w.closed) w.close();
    } catch {
      // cross-origin or already gone: nothing to do
    }
  }, []);

  // PIN claimed → token.
  const pinData = waiting ? pin.data : undefined;
  useEffect(() => {
    if (pinData?.authenticated && pinData.authToken) {
      dispatch({ type: 'authenticated', token: pinData.authToken });
      closePopup();
    }
  }, [pinData, closePopup]);

  // Permanent polling failures (expired/unknown PIN → 4xx) end the attempt; transient ones retry.
  const pollError = waiting ? pin.error : null;
  useEffect(() => {
    if (pollError && isApiError(pollError) && pollError.status >= 400 && pollError.status < 500) {
      dispatch({ type: 'failed', message: `Plex sign-in failed: ${errorMessage(pollError)}` });
      closePopup();
    }
  }, [pollError, closePopup]);

  // 5 minute timeout.
  const startedAt = waiting ? state.startedAt : null;
  useEffect(() => {
    if (startedAt === null) return;
    const timer = setTimeout(() => {
      dispatch({ type: 'timeout' });
      closePopup();
    }, remainingMs(startedAt, Date.now()));
    return () => clearTimeout(timer);
  }, [startedAt, closePopup]);

  // Never leave a dangling popup behind; results arriving after unmount are dropped.
  useEffect(
    () => () => {
      attemptRef.current += 1;
      closePopup();
    },
    [closePopup],
  );

  const { mutate } = createPin;
  const inFlight = state.step === 'creating' || state.step === 'waiting';

  const start = useCallback(() => {
    if (inFlight) return;
    const attempt = ++attemptRef.current;

    let popup: Window | null = null;
    try {
      popup = window.open('', PLEX_POPUP_NAME, popupFeatures());
    } catch {
      popup = null;
    }
    if (popup) {
      // plex.tv must not be able to script or navigate this tab (reverse tabnabbing).
      try {
        popup.opener = null;
      } catch {
        // ignore
      }
      try {
        popup.document.title = 'Sign in with Plex';
        popup.document.body.textContent = 'Loading Plex sign-in…';
      } catch {
        // popup re-used from an earlier attempt and already cross-origin
      }
    }
    popupRef.current = popup;
    dispatch({ type: 'start' });

    mutate(undefined, {
      onSuccess: (created) => {
        if (attempt !== attemptRef.current) return;
        if (!created || !isTrustedPlexAuthUrl(created.authUrl)) {
          closePopup();
          dispatch({
            type: 'failed',
            message: 'Dupearr returned an unexpected sign-in address, so the Plex popup was not opened.',
          });
          return;
        }
        const w = popupRef.current;
        let blocked = !w || w.closed;
        if (w && !blocked) {
          try {
            w.location.href = created.authUrl;
          } catch {
            blocked = true;
          }
        }
        dispatch({
          type: 'pinCreated',
          pinId: created.id,
          authUrl: created.authUrl,
          popupBlocked: blocked,
          now: Date.now(),
        });
      },
      onError: (e) => {
        if (attempt !== attemptRef.current) return;
        closePopup();
        dispatch({ type: 'failed', message: errorMessage(e, 'Could not start Plex sign-in') });
      },
    });
  }, [inFlight, mutate, closePopup]);

  const cancel = useCallback(() => {
    attemptRef.current += 1;
    closePopup();
    dispatch({ type: 'cancel' });
  }, [closePopup]);

  const reset = useCallback(() => {
    attemptRef.current += 1;
    closePopup();
    dispatch({ type: 'reset' });
  }, [closePopup]);

  return { state, start, cancel, reset };
}

/** Current time, re-rendered every `intervalMs` while `active`. */
function useTicker(active: boolean, intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [active, intervalMs]);
  return now;
}

export interface PlexSignInProps {
  /** Called when the user picks a server connection (fills URL + token in the form). */
  onSelect: (selection: PlexServerSelection) => void;
  disabled?: boolean;
  className?: string;
}

/** "Sign in with Plex" panel: PIN popup flow + server/connection picker. */
export function PlexSignIn({ onSelect, disabled = false, className }: PlexSignInProps) {
  const { state, start, cancel, reset } = usePlexSignIn();
  const now = useTicker(state.step === 'waiting');

  let body;
  switch (state.step) {
    case 'idle':
    case 'creating':
      body = (
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            icon={LogIn}
            onClick={start}
            loading={state.step === 'creating'}
            disabled={disabled}
          >
            Sign in with Plex
          </Button>
          <span className="text-sm text-muted">
            Recommended — signs in on plex.tv, creates a token for Dupearr and lists your servers and
            their connections.
          </span>
        </div>
      );
      break;
    case 'waiting':
      body = (
        <Alert
          kind="info"
          title="Waiting for Plex sign-in…"
          actions={
            <Button size="sm" onClick={cancel}>
              Cancel
            </Button>
          }
        >
          <div className="flex items-center gap-2">
            <Spinner size="sm" label="Waiting for Plex" />
            <span>
              Complete the sign-in in the Plex window ({formatCountdown(remainingMs(state.startedAt, now))} left).
            </span>
          </div>
          {state.popupBlocked && (
            <div className="mt-2">
              <span className="text-warning">Your browser blocked the popup. </span>
              <a
                href={state.authUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-accent-soft underline"
              >
                Open Plex sign-in
                <ExternalLink aria-hidden width={12} height={12} />
              </a>
            </div>
          )}
        </Alert>
      );
      break;
    case 'timeout':
      body = (
        <Alert
          kind="warning"
          title="Plex sign-in timed out"
          actions={
            <Button size="sm" icon={RefreshCw} onClick={start} disabled={disabled}>
              Try again
            </Button>
          }
        >
          No sign-in was completed within 5 minutes.
        </Alert>
      );
      break;
    case 'error':
      body = (
        <Alert
          kind="error"
          title="Plex sign-in failed"
          actions={
            <Button size="sm" icon={RefreshCw} onClick={start} disabled={disabled}>
              Try again
            </Button>
          }
        >
          {state.message}
        </Alert>
      );
      break;
    case 'authenticated':
      body = <PlexServerPicker token={state.token} onSelect={onSelect} onReset={reset} disabled={disabled} />;
      break;
  }

  return <div className={className}>{body}</div>;
}

// ---------------------------------------------------------------------------
// Server + connection picker
// ---------------------------------------------------------------------------

interface PlexServerPickerProps {
  token: string;
  onSelect: (selection: PlexServerSelection) => void;
  onReset: () => void;
  disabled?: boolean;
}

function PlexServerPicker({ token, onSelect, onReset, disabled }: PlexServerPickerProps) {
  const servers = usePlexServers(token);
  const groupName = useId();
  const [serverId, setServerId] = useState<string | null>(null);
  const [chosenUri, setChosenUri] = useState<string | null>(null);

  const header = (
    <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
      <div className="flex items-center gap-2 text-sm text-success">
        <CircleCheck aria-hidden width={16} height={16} />
        Signed in to Plex
      </div>
      <Button size="sm" variant="ghost" onClick={onReset}>
        Start over
      </Button>
    </div>
  );

  if (servers.isPending) {
    return (
      <div>
        {header}
        <div className="flex items-center gap-2 text-sm text-muted">
          <Spinner size="sm" label="Loading servers" />
          Loading your Plex servers…
        </div>
      </div>
    );
  }

  if (servers.isError) {
    return (
      <div>
        {header}
        <Alert
          kind="error"
          title="Could not load your Plex servers"
          actions={
            <Button size="sm" icon={RefreshCw} onClick={() => void servers.refetch()}>
              Retry
            </Button>
          }
        >
          {errorMessage(servers.error)}
        </Alert>
      </div>
    );
  }

  const list = sortPlexServers(servers.data);
  if (list.length === 0) {
    return (
      <div>
        {header}
        <Alert kind="warning" title="No Plex Media Servers found">
          This Plex account has no servers. Sign in with the account that owns your server, or enter the
          URL and token manually below.
        </Alert>
      </div>
    );
  }

  const selected = list.find((s) => s.clientIdentifier === serverId) ?? list[0]!;
  const connections = rankConnections(selected.connections);
  // Never the account token for a shared server; for an owned one only with a warning (SEC-033).
  const serverToken = plexServerToken(selected, token);

  return (
    <div>
      {header}
      <fieldset className="m-0 border-0 p-0">
        <legend className="mb-1.5 text-sm font-semibold text-fg-strong">Server</legend>
        <div className="flex flex-col gap-1.5">
          {list.map((s) => (
            <PlexServerOption
              key={s.clientIdentifier || s.name}
              server={s}
              name={groupName}
              checked={s === selected}
              onChange={() => {
                setServerId(s.clientIdentifier);
                setChosenUri(null);
              }}
            />
          ))}
        </div>
      </fieldset>

      {!selected.owned && (
        <Alert kind="warning" title="You don't own this server" className="mt-3">
          Only the server owner can delete media through Plex. Dupearr can still scan it and remove files
          through Radarr/Sonarr or the filesystem.
        </Alert>
      )}

      {!serverToken && (
        <Alert kind="error" title="No access token for this server" className="mt-3">
          plex.tv did not list an access token for this server. Dupearr will not send your plex.tv account
          token to a server you don&apos;t own, because it gives full control of your Plex account. Enter the
          server&apos;s URL and a token for this server manually below.
        </Alert>
      )}

      {serverToken?.source === 'account' && (
        <Alert kind="warning" title="Using your plex.tv account token" className="mt-3">
          plex.tv did not list a separate access token for this server, so Dupearr will save your plex.tv
          account token for it. That token gives full control of your Plex account and stays valid until you
          sign out of all devices on plex.tv. Only use a connection address you trust.
        </Alert>
      )}

      <div className="mt-3">
        <div className="mb-1.5 text-sm font-semibold text-fg-strong">Connection</div>
        {connections.length === 0 ? (
          <div className="text-sm text-muted">
            Plex lists no connections for this server. Enter its URL manually below.
          </div>
        ) : (
          <ul className="m-0 flex list-none flex-col gap-1.5 p-0" aria-label="Connections">
            {connections.map((c, i) => (
              <PlexConnectionRow
                key={`${c.uri}-${i}`}
                connection={c}
                recommended={i === 0 && !c.relay}
                chosen={chosenUri === c.uri}
                disabled={disabled || !serverToken}
                onUse={() => {
                  const selection = toSelection(selected, c, token);
                  if (!selection) return;
                  setChosenUri(c.uri);
                  onSelect(selection);
                }}
              />
            ))}
          </ul>
        )}
        <div className="mt-1.5 text-xs text-muted">
          Prefer a local, non-relay connection. If Dupearr runs in Docker next to Plex, the server&apos;s LAN
          address (e.g. http://192.168.1.10:32400) is usually the most reliable — you can edit the URL below.
        </div>
      </div>
    </div>
  );
}

function PlexServerOption({
  server,
  name,
  checked,
  onChange,
}: {
  server: PlexServer;
  name: string;
  checked: boolean;
  onChange: () => void;
}) {
  return (
    <label
      className={clsx(
        'flex cursor-pointer items-center gap-3 rounded border px-3 py-2',
        checked ? 'border-accent bg-accent/10' : 'border-border hover:bg-card-hover',
      )}
    >
      <input type="radio" name={name} checked={checked} onChange={onChange} className="accent-accent" />
      <Server aria-hidden width={16} height={16} className="shrink-0 text-muted" />
      <span className="min-w-0 flex-1 truncate text-fg-strong">{server.name || server.clientIdentifier}</span>
      {server.productVersion && <span className="hidden text-xs text-muted sm:inline">v{server.productVersion}</span>}
      {server.owned ? (
        <Badge kind="success" outline>
          Owner
        </Badge>
      ) : (
        <Badge kind="warning" outline icon={TriangleAlert} title="Only the server owner can delete media">
          Shared
        </Badge>
      )}
    </label>
  );
}

function PlexConnectionRow({
  connection: c,
  recommended,
  chosen,
  disabled,
  onUse,
}: {
  connection: PlexConnection;
  recommended: boolean;
  chosen: boolean;
  disabled?: boolean;
  onUse: () => void;
}) {
  const https = (c.protocol || '').toLowerCase() === 'https' || /^https:/i.test(c.uri);
  return (
    <li
      className={clsx(
        'flex flex-wrap items-center gap-2 rounded border px-3 py-2',
        chosen ? 'border-success bg-success/10' : 'border-border',
      )}
    >
      <code className="min-w-0 flex-1 text-xs break-all text-fg">{c.uri}</code>
      <span className="flex flex-wrap items-center gap-1">
        {recommended && <Badge kind="accent">Recommended</Badge>}
        <Badge kind={c.local ? 'success' : 'info'} outline>
          {c.local ? 'Local' : 'Remote'}
        </Badge>
        {c.relay && (
          <Badge kind="warning" outline title="Relay connections are bandwidth-limited; use only as a last resort">
            Relay
          </Badge>
        )}
        <Badge kind={https ? 'success' : 'default'} outline>
          {https ? 'HTTPS' : 'HTTP'}
        </Badge>
      </span>
      <Button size="sm" variant={chosen ? 'success' : recommended ? 'primary' : 'default'} onClick={onUse} disabled={disabled}>
        {chosen ? 'Selected' : 'Use'}
      </Button>
    </li>
  );
}
