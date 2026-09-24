/**
 * Pure helpers for the System pages: uptime formatting, safe external links, saving backup
 * downloads, log download URLs and project links.
 */
import { apiUrl } from '@/api/client';

/** Project home (the public GitHub repository). */
export const REPOSITORY_URL = 'https://github.com/sl0wz3r/dupearr';
/** Documentation folder of the repository. */
export const DOCS_URL = `${REPOSITORY_URL}/tree/main/docs`;
/** Issue tracker. */
export const ISSUES_URL = `${REPOSITORY_URL}/issues`;

/**
 * Uptime / long durations: 93784 → "1d 2h 3m", 3660 → "1h 1m", 45 → "45s", 0 → "0s".
 * Minutes are always shown once the duration reaches an hour; seconds only below a minute.
 */
export function formatUptime(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) return '';
  const total = Math.floor(seconds);
  if (total < 60) return `${total}s`;
  const days = Math.floor(total / 86_400);
  const hours = Math.floor((total % 86_400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (days > 0 || hours > 0) parts.push(`${hours}h`);
  parts.push(`${minutes}m`);
  return parts.join(' ');
}

/**
 * Seconds the process has been up at `now` (ms): from `startTime` when valid, else the reported
 * `uptimeSeconds` (which is only accurate at fetch time). Null when neither is usable.
 */
export function uptimeSecondsAt(
  startTime: string | null | undefined,
  uptimeSeconds: number | null | undefined,
  now: number,
): number | null {
  if (startTime) {
    const t = new Date(startTime).getTime();
    if (Number.isFinite(t) && new Date(t).getUTCFullYear() > 1 && t <= now) return Math.floor((now - t) / 1000);
  }
  if (typeof uptimeSeconds === 'number' && Number.isFinite(uptimeSeconds) && uptimeSeconds >= 0) return uptimeSeconds;
  return null;
}

/** Only http(s) URLs are rendered as links (health `wikiUrl` comes from the server). */
export function safeExternalUrl(value: string | null | undefined): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : null;
  } catch {
    return null;
  }
}

/** Short commit hash for display ("0123456789abcdef" → "0123456789"); "" for unknown. */
export function shortCommit(commit: string | null | undefined): string {
  const c = (commit ?? '').trim();
  if (!c || c.toLowerCase() === 'unknown') return '';
  return /^[0-9a-f]{11,}$/i.test(c) ? c.slice(0, 10) : c;
}

/**
 * Saves a downloaded Blob under name through a temporary object URL (same origin, no credential in
 * any URL). Never throws.
 */
export function saveBlob(blob: Blob, name: string): void {
  try {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Give the browser time to start the download before the URL is released.
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch {
    // Nothing to clean up: the download did not start.
  }
}

/** Download URL of a log file (`/api/v1/log/file/{name}`, authenticated by the session cookie). */
export function logFileDownloadUrl(filename: string): string {
  return apiUrl(`/log/file/${encodeURIComponent(filename)}`);
}
