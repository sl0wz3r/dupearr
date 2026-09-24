// Package disc recognises full-disc backups — Blu-ray / UHD Blu-ray (BDMV), DVD-Video
// (VIDEO_TS or flat VIDEO_TS.IFO + VTS_*.VOB files), HD DVD, AVCHD, BDAV and .iso/.img disc
// images — and reads the metadata of their main feature, so that a disc (hundreds of files) is
// handled as ONE copy of a movie. Design basis: docs/research/disc-structures.md (§4 layouts
// and the Blu-ray reader spec, §6 the Dupearr design and its edge-case table).
//
// # What it offers
//
//   - Pure path checks, usable on server, *arr and local paths alike (any separators, any
//     case): IsDiscPath (the fail-closed "Tier 1" guard: a path inside a disc structure must
//     never be removed on its own — also a numbered STREAM clip such as "00800.m2ts" lying
//     loose in a movie folder), RootOf (the disc root of such a path), IsImagePath,
//     IsDiscEntryName, IsSetFolderName, IsExtrasFolderName, DetectDir and RootHash; for loose
//     clip sets (flattened backups, docs/DECISIONS.md D9 "Loose clip sets") IsClipName,
//     IsDVDClipName, IsLooseSetFileName, LooseClipKind, IsLooseClipPath and ClipSetHash.
//   - Detect: the discs rooted in one folder (the folder itself — including its loose clips,
//     type BlurayClips —, "Disc N"/"CDn" sub-folders, macOS .dvdmedia bundles and *.iso/*.img
//     files, stacked into multi-disc sets), with the
//     exact entries each disc owns (OwnedEntries: BDMV/, CERTIFICATE/ …, never a sibling
//     movie file, artwork, NFO, subtitles or extras).
//   - Inspect: file count, sizes, hardlink-aware reclaimable bytes and a fingerprint of the
//     owned entries (Measure), plus the main feature: for Blu-ray a pure-Go reader of
//     index.bdmv, PLAYLIST/*.mpls and CLIPINF/*.clpi with libbluray's main-title rules; for
//     DVD the IFO files of the largest title set; nothing for images and protect-only kinds.
//
// # Safety model
//
// The package only reads. Every folder is opened through an os.Root after an Lstat check, so
// nothing outside it can be reached, and entries are never followed when they are symbolic
// links (they are counted as irregular instead, and a symlinked disc structure is reported
// with ErrSymlink). Mount points below a disc are not crossed. Directory listings, the number
// of visited entries, the walk depth, the size of each metadata file and the total metadata
// read per disc are bounded (Options), and the context is checked throughout. Every binary
// parser is bounds-checked (see reader) and fuzz-tested; malformed input yields an error,
// never a panic.
//
// Problems with the disc itself (incomplete, unreadable, an unclear multi-disc set, several
// structures in one folder, limits exceeded, symlinks) are reported in Disc.Err rather than as
// a failed call: callers must treat such a disc as protected (see Disc.Removable). Detection
// may miss a disc; IsDiscPath must not, which is why it is a pure, conservative path rule.
package disc
