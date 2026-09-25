/**
 * What the Plex and Jellyfin server forms share (MediaServerModal, JellyfinServerForm).
 */
import type { MediaServer, MediaServerStorage } from '@/api/types';

/** Fields the server may report validation errors for. */
export const FIELDS = ['name', 'url', 'token', 'verifyTls', 'enabled', 'storage'] as const;

export const STORAGE_OPTIONS: readonly { value: MediaServerStorage; label: string }[] = [
  { value: '', label: 'Same storage as the other servers (compare paths)' },
  { value: 'separate', label: "Separate storage (another host, a friend's server)" },
];

/** A Plex server (a server stored before kinds existed has none and is Plex). */
export function isPlexServer(s: Pick<MediaServer, 'kind'>): boolean {
  return s.kind !== 'jellyfin';
}

/**
 * The Storage setting only matters with several media servers: shown when another server exists
 * (or this one is already declared separate, so it can be undone).
 */
export function showStorageField(server: MediaServer | null, servers: readonly MediaServer[] | undefined): boolean {
  if (server?.storage === 'separate') return true;
  return (servers ?? []).some((s) => s.id !== server?.id);
}
