/**
 * Full-disc backups on the Duplicates pages (docs/research/disc-structures.md §6): recognising paths
 * inside a disc structure or named like a disc clip (the TypeScript twin of internal/disc.IsDiscPath
 * / IsClipName) and the client-side checks of a disc removal. A disc (BDMV / VIDEO_TS / ISO …, or the
 * clips of a disc stored loose in the movie folder) is hundreds of files that make up ONE copy; it is
 * only ever removed as a whole — manual approval, filesystem method, into the recycle bin — and no
 * single file of it is ever removed on its own.
 *
 * Loose clips are why this matters: a flattened Blu-ray backup keeps its numbered STREAM clips
 * (`00174.m2ts`, `00175.m2ts` …) directly in the movie folder, and Plex lists each one as a separate
 * version of the movie. Removing one "duplicate" clip deletes part of the movie.
 *
 * Defense in depth only: the server re-checks every rule. No React here (unit-tested in
 * disc.test.ts).
 */
import type {
  DiscInfo,
  DiscType,
  DuplicateGroupSummaryFile,
  GroupFile,
  MediaPart,
  MediaType,
  MediaVersion,
  Settings,
} from '@/api/types';
import { DISC_CLIP_SET_TYPES } from '@/lib/constants';
import { formatClipCount, formatFileCount } from '@/lib/format';

// ---------------------------------------------------------------------------
// Paths inside a disc structure (research §6.2, "Tier 1")
// ---------------------------------------------------------------------------

/** A path component that belongs to a disc structure (BDMV/, VIDEO_TS/, CERTIFICATE/ …). */
const DISC_DIR = /(?:^|\/)(?:BDMV|BDAV|VIDEO_TS|HVDVD_TS|AVCHD|AACS|CERTIFICATE|MAKEMKV|BDSVM|SLYVM|ANYVM|ADV_OBJ)(?:\/|$)/i;
/**
 * What a file manager appends to a clashing copy's name when two discs are flattened into one
 * folder: "00004.1", "00800 (2)", "00800 - Copy", "00800 2", "00800_1" (internal/disc copyMarkers).
 * A year in parentheses ("00800 (2019)"), a fourth digit or a word never match.
 */
const COPY_MARKERS = String.raw`(?:[ ._-]+(?:copy|\(\d{1,3}\)|\d{1,3}))*`;
/** Disc marker files and the file names of a flat DVD (VIDEO_TS.IFO, VTS_01_1.VOB …, copies included). */
const DISC_FILE = new RegExp(
  String.raw`(?:^|\/)(?:VIDEO_TS${COPY_MARKERS}\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]${COPY_MARKERS}\.(?:IFO|BUP|VOB)|index\.bdmv|MovieObject\.bdmv|INDEX\.BDM|MOVIEOBJ\.BDM|discatt\.dat)$`,
  'i',
);
/** Extensions that only exist inside a disc structure. */
const DISC_EXT = /\.(?:mpls|clpi|bdmv|bdm|mpl|cpi|ssif|ifo|bup|evo)$/i;

// ---------------------------------------------------------------------------
// Disc clip names (wherever the file lies)
// ---------------------------------------------------------------------------

/** Blu-ray / AVCHD STREAM clip: five digits (+ copy markers) + .m2ts / .mts / .m2t ("00800.m2ts"). */
const BLURAY_CLIP_NAME = new RegExp(String.raw`^\d{5}${COPY_MARKERS}\.(?:m2ts|mts|m2t)$`, 'i');
/** DVD title-set video and the VIDEO_TS menu files ("VTS_01_1.VOB", "VIDEO_TS.IFO", copies included). */
const DVD_CLIP_NAME = new RegExp(String.raw`^(?:VTS_\d{2}_\d${COPY_MARKERS}\.VOB|VIDEO_TS${COPY_MARKERS}\.(?:VOB|IFO|BUP))$`, 'i');

/** Which disc a clip name belongs to. */
export type ClipKind = 'bluray' | 'dvd';

/** The last component of a path (either separator); the input itself when it has none. */
export function baseName(path: string | null | undefined): string {
  if (!path) return '';
  const p = path.replace(/[\\/]+$/, '');
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return i === -1 ? p : p.slice(i + 1);
}

/** The folder of a path, without the last component ("" when there is none). */
function dirName(path: string): string {
  const p = path.replace(/\\/g, '/');
  const i = p.lastIndexOf('/');
  return i === -1 ? '' : p.slice(0, i);
}

/**
 * `bluray` for a Blu-ray/AVCHD STREAM clip name ("00800.m2ts", any case), `dvd` for a DVD
 * title-set/menu file ("VTS_01_1.VOB", "VIDEO_TS.IFO"), else null. A path is reduced to its
 * base name.
 */
export function clipKind(nameOrPath: string | null | undefined): ClipKind | null {
  const name = baseName(nameOrPath);
  if (BLURAY_CLIP_NAME.test(name)) return 'bluray';
  if (DVD_CLIP_NAME.test(name)) return 'dvd';
  return null;
}

/**
 * True when the base name is a disc clip name (the TypeScript twin of internal/disc.IsClipName and
 * IsDVDClipName): `^\d{5}\.(m2ts|mts|m2t)$`, `^VTS_\d{2}_\d\.VOB$` or `^VIDEO_TS\.(VOB|IFO|BUP)$`,
 * case-insensitive, each also with copy markers before the extension ("00800 (1).m2ts").
 * `Heat.m2ts`, `0800.m2ts` or `1917.m2ts` are not.
 */
export function isClipName(nameOrPath: string | null | undefined): boolean {
  return clipKind(nameOrPath) !== null;
}

/**
 * True for a clip-named file that lies loose in a folder, outside a BDMV/STREAM (or VIDEO_TS/ …)
 * structure: `/data/Movies/Elemental (2023)/00174.m2ts`.
 */
export function isLooseClipPath(path: string | null | undefined): boolean {
  if (!path || !isClipName(path)) return false;
  return !DISC_DIR.test(dirName(path));
}

/**
 * True when `path` (any OS separators, any case) is a file of a disc: inside a disc structure
 * (`…/BDMV/STREAM/00800.m2ts`, `…/VIDEO_TS/VTS_01_1.VOB`) or named like a disc clip wherever it
 * lies (`…/Elemental (2023)/00174.m2ts`). A standalone `Movie.m2ts` is not.
 */
export function isDiscPath(path: string | null | undefined): boolean {
  if (!path) return false;
  const p = path.replace(/\\/g, '/');
  return DISC_DIR.test(p) || DISC_FILE.test(p) || DISC_EXT.test(p) || isClipName(p);
}

// ---------------------------------------------------------------------------
// Versions
// ---------------------------------------------------------------------------

/** The disc facts of a version (null for a regular file). */
export function discOf(v: MediaVersion | null | undefined): DiscInfo | null {
  return v?.disc ?? null;
}

/** True when the version is a full-disc backup (a loose clip set included). */
export function isDiscVersion(v: MediaVersion | null | undefined): boolean {
  return !!v?.disc;
}

/** True for the loose clip set kinds (`bluray_clips`, `dvd_clips`). */
export function isClipSetType(type: DiscType | string | null | undefined): boolean {
  return !!type && (DISC_CLIP_SET_TYPES as readonly string[]).includes(type);
}

/** True when the disc is a set of loose clips (00800.m2ts … / VTS_01_1.VOB … in the movie folder). */
export function isClipSet(disc: Pick<DiscInfo, 'type'> | null | undefined): boolean {
  return isClipSetType(disc?.type);
}

function partsOf(v: MediaVersion | null | undefined): MediaPart[] {
  return Array.isArray(v?.parts) ? v.parts : [];
}

/** Server-side and local paths of the parts (for the per-file disc guard). */
function allPartPaths(v: MediaVersion | null | undefined): string[] {
  const out: string[] = [];
  for (const p of partsOf(v)) {
    if (p?.path) out.push(p.path);
    if (p?.localPath) out.push(p.localPath);
  }
  return out;
}

/** The parts of a version whose file is named like a disc clip (server or local path). */
export function clipParts(v: MediaVersion | null | undefined): MediaPart[] {
  return partsOf(v).filter((p) => isClipName(p?.path) || isClipName(p?.localPath));
}

/**
 * Clips of a loose clip set: the server's count, else the clip-named parts (0 for anything else).
 */
export function clipCountOf(v: MediaVersion | null | undefined): number {
  const disc = v?.disc;
  if (!disc || !isClipSet(disc)) return 0;
  const n = disc.clipCount;
  if (typeof n === 'number' && Number.isFinite(n) && n > 0) return n;
  return clipParts(v).length;
}

/**
 * The main clip of a loose clip set (its attributes are the version's): the server's main feature
 * when it names a clip, else the longest clip part (then the largest). "" when unknown.
 */
export function mainClipName(v: MediaVersion | null | undefined): string {
  const disc = v?.disc;
  if (!disc || !isClipSet(disc)) return '';
  if (disc.mainFeature && isClipName(disc.mainFeature)) return baseName(disc.mainFeature);
  let best: MediaPart | null = null;
  for (const p of clipParts(v)) {
    const d = Number.isFinite(p.duration) ? p.duration : 0;
    const s = Number.isFinite(p.size) ? p.size : 0;
    if (!best || d > best.duration || (d === best.duration && s > best.size)) best = { ...p, duration: d, size: s };
  }
  return best ? baseName(best.path || best.localPath) : '';
}

/**
 * The first path of a NON-disc version that is a file of a disc — inside a disc structure (e.g. an
 * *arr-tracked `BDMV/STREAM/00800.m2ts`) or a loose clip (`…/Elemental (2023)/00174.m2ts`) — or
 * null. Removing such a file would break the disc.
 */
export function discMemberPath(v: MediaVersion | null | undefined): string | null {
  if (!v || v.disc) return null;
  return allPartPaths(v).find(isDiscPath) ?? null;
}

/**
 * Why this copy must never be marked for removal on its own, or null: it is a single file of a
 * disc (see {@link discMemberPath}). Used by the approval check and the per-copy override.
 */
export function discMemberBlocker(v: MediaVersion | null | undefined): string | null {
  const path = discMemberPath(v);
  if (!path) return null;
  if (isLooseClipPath(path)) {
    const what =
      clipKind(path) === 'dvd' ? 'a DVD stored as loose VOB files' : 'a Blu-ray stored as loose .m2ts clips';
    return `“${path}” is one file of ${what}. A movie can span several clips, so clips are never removed one by one — that would break the movie. Set this copy to Keep and re-scan: the clips of the folder are then shown as one copy.`;
  }
  return `“${path}” is a file inside a disc folder (BDMV, VIDEO_TS …). Files of a disc are never removed one by one — that would break the disc. Set this copy to Keep and re-scan.`;
}

/**
 * Why the override of this copy cannot be set to `value`, or null: a single file of a disc is
 * never marked for removal (the server refuses it too).
 */
export function overrideBlocker(file: Pick<GroupFile, 'version'>, value: string): string | null {
  return value === 'remove' ? discMemberBlocker(file.version) : null;
}

/**
 * `path` relative to the disc root ("BDMV/STREAM/00800.m2ts" under "/movies/Heat (1995)"); a path
 * outside the root is returned unchanged. Either separator is accepted.
 */
export function discRelativePath(root: string, path: string): string {
  const base = root.replace(/[\\/]+$/, '');
  if (base && path.length > base.length + 1 && path.startsWith(base) && /[\\/]/.test(path.charAt(base.length))) {
    return path.slice(base.length + 1);
  }
  return path;
}

/**
 * What removing a disc does, for the approval dialog: "The whole disc folder (312 files) will be
 * moved to the recycle bin". An ISO image is one file; a multi-disc set moves every disc; a loose
 * clip set moves every clip of its folder.
 */
export function discRemovalNote(disc: Pick<DiscInfo, 'type' | 'fileCount' | 'discs'> & { clipCount?: number }): string {
  if (disc.type === 'iso') return 'The ISO image will be moved to the recycle bin.';
  const files = formatFileCount(disc.fileCount);
  if (isClipSet(disc)) {
    const clips = formatClipCount(disc.clipCount);
    // "125 clips · 127 files" — the file count only adds something when metadata files move too.
    const filesToo = !clips || (disc.fileCount ?? 0) > (disc.clipCount ?? 0);
    const counts = [clips, filesToo ? files : ''].filter(Boolean).join(' · ');
    const what = disc.type === 'dvd_clips' ? 'Every loose DVD file (VIDEO_TS.*, VTS_*)' : 'Every loose Blu-ray clip (00800.m2ts …)';
    return `${what} of this folder${counts ? ` (${counts})` : ''} will be moved to the recycle bin.`;
  }
  const what = disc.discs > 1 ? `Every disc folder of this ${disc.discs}-disc set` : 'The whole disc folder';
  return `${what}${files ? ` (${files})` : ''} will be moved to the recycle bin.`;
}

/** The note shown under the removal note: what a disc removal never touches. */
export const DISC_REMOVAL_KEEPS_NOTE =
  'Only the disc itself moves (BDMV/, CERTIFICATE/ … or the .iso). Other video files, artwork, .nfo and subtitles in the same folder stay. Until the recycle bin is cleaned up, the disc can be restored from this group’s Actions list.';

/** The same for a loose clip set. */
export const CLIP_SET_REMOVAL_KEEPS_NOTE =
  'Only the clip files (00800.m2ts … / VTS_01_1.VOB …) and the loose disc metadata files (.mpls, .clpi, .bdmv, .ifo) of this folder move. Other video files (an MKV or MP4), artwork, .nfo and subtitles stay. Until the recycle bin is cleaned up, the clips can be restored from this group’s Actions list.';

/** {@link DISC_REMOVAL_KEEPS_NOTE} or {@link CLIP_SET_REMOVAL_KEEPS_NOTE}. */
export function discRemovalKeepsNote(disc: Pick<DiscInfo, 'type'>): string {
  return isClipSet(disc) ? CLIP_SET_REMOVAL_KEEPS_NOTE : DISC_REMOVAL_KEEPS_NOTE;
}

// ---------------------------------------------------------------------------
// Approval
// ---------------------------------------------------------------------------

/** The settings a disc removal depends on (null/undefined = not loaded yet). */
export type DiscSettings = Pick<Settings, 'allowDiscRemoval' | 'recycleBinPath' | 'keepPlayableCopy'>;

export interface DiscApprovalContext {
  /** Current settings; unknown settings block every disc removal (the safe assumption). */
  settings?: DiscSettings | null;
  /** Discs in TV libraries are always protected in v1. */
  mediaType?: MediaType;
}

/**
 * Why an approval must be blocked because of a disc, or null:
 *
 * - a regular copy whose file is a file of a disc (inside a disc structure, or a loose clip such as
 *   `00174.m2ts`) is marked for removal (files of a disc are never removed one by one — by any
 *   method);
 * - a disc is marked for removal while disc removal is not allowed, no recycle bin is set, the disc
 *   is not reachable by Dupearr (no path mapping), it belongs to a TV show, or the settings are
 *   unknown;
 * - every kept copy would be a disc (a loose clip set included: a feature can span clips, so Plex
 *   cannot reliably play it) while a regular copy is removed and "keep a Plex-playable copy" is on
 *   (or unknown: it defaults to on).
 */
export function discApprovalBlocker(files: readonly GroupFile[], ctx: DiscApprovalContext = {}): string | null {
  const removals = files.filter((f) => f.decision === 'remove');
  const keepers = files.filter((f) => f.decision === 'keep');

  for (const f of removals) {
    const member = discMemberBlocker(f.version);
    if (member) return member;
  }

  const discRemovals = removals.filter((f) => isDiscVersion(f.version));
  if (discRemovals.length > 0) {
    if (ctx.mediaType === 'episode') {
      return 'Full discs in TV libraries are always kept. Set the disc to Keep.';
    }
    const s = ctx.settings;
    if (!s) return 'The settings are not loaded, so this disc removal cannot be checked. Reload the page and try again.';
    if (!s.allowDiscRemoval) {
      return 'Removing full discs is turned off. Turn on “Allow Removing Full Discs” in Settings → Media Management, or set the disc to Keep.';
    }
    if (!(s.recycleBinPath ?? '').trim()) {
      return 'A full disc can only be moved to the recycle bin, and no recycle bin is set. Set one in Settings → Media Management first.';
    }
    const unmapped = discRemovals.find((f) => !(f.version.disc?.localRoot ?? '').trim());
    if (unmapped) {
      const root = unmapped.version.disc?.root || 'this disc';
      const what = isClipSet(unmapped.version.disc) ? 'the clips' : 'the disc';
      return `Dupearr cannot reach “${root}” (no path mapping), so it cannot move ${what} to the recycle bin. Add a path mapping or set the disc to Keep.`;
    }
    const unsafe = discRemovals.find((f) => f.version.disc?.removable === false);
    if (unsafe) {
      const why = (unsafe.version.disc?.problem ?? '').trim();
      return `This disc cannot be removed safely${why ? ` (${why})` : ''}. Set the disc to Keep.`;
    }
  }

  // A copy Plex can play whole: never a disc, and never a single file of one (a loose clip kept as
  // its own copy in a group stored before clips were merged into one set) — the server's rule.
  const notPlayable = (f: GroupFile) => isDiscVersion(f.version) || discMemberPath(f.version) !== null;
  const keepPlayable = ctx.settings?.keepPlayableCopy !== false;
  if (
    keepPlayable &&
    keepers.length > 0 &&
    keepers.every(notPlayable) &&
    removals.some((f) => !notPlayable(f))
  ) {
    if (keepers.some((f) => isClipSet(f.version.disc) || isLooseClipPath(discMemberPath(f.version)))) {
      return 'Every kept copy would be a full disc or a set of loose disc clips, which Plex cannot reliably play (a movie can span several clips). Keep a regular video file as well, or turn off “Always Keep a Plex-Playable Copy” in Settings → Media Management.';
    }
    return 'Every kept copy would be a full disc, which Plex cannot play. Keep a regular video file as well, or turn off “Always Keep a Plex-Playable Copy” in Settings → Media Management.';
  }
  return null;
}

/** True when a list row removes a full disc (such groups are approved from their detail page). */
export function removesDisc(files: readonly Pick<DuplicateGroupSummaryFile, 'decision' | 'disc'>[] | null | undefined): boolean {
  return (files ?? []).some((f) => f.decision === 'remove' && !!f.disc);
}

/**
 * True when a list row would remove a single file of a disc (a loose clip such as `00174.m2ts`, as
 * reported by the server's optional `discClip`). Such a group is never approved from the list.
 */
export function removesDiscClip(
  files: readonly (Pick<DuplicateGroupSummaryFile, 'decision'> & { discClip?: boolean })[] | null | undefined,
): boolean {
  return (files ?? []).some((f) => f.decision === 'remove' && f.discClip === true);
}
