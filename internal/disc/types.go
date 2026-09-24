package disc

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Type is the kind of a disc structure. The string values are the ones of models.DiscInfo.Type.
type Type string

const (
	// Bluray is a BDMV Blu-ray structure (index.bdmv version 0100/0200/0240).
	Bluray Type = "bluray"
	// UHDBluray is a BDMV Ultra HD Blu-ray structure (index.bdmv version 0300).
	UHDBluray Type = "uhd_bluray"
	// DVD is DVD-Video: a VIDEO_TS folder, or VIDEO_TS.IFO + VTS_NN_N.VOB files directly in the
	// disc root ("flat", Disc.Flat).
	DVD Type = "dvd"
	// HDDVD is an HD DVD structure (HVDVD_TS/). Protect-only.
	HDDVD Type = "hddvd"
	// AVCHD is a camcorder AVCHD structure (AVCHD/ or PRIVATE/AVCHD/, or a BDMV/ holding
	// INDEX.BDM). Protect-only.
	AVCHD Type = "avchd"
	// ISO is a disc image file (.iso or .img). Root is the file itself.
	ISO Type = "iso"
	// BDAV is a Blu-ray recorder structure (BDAV/). Protect-only. Additive to the shared
	// contract, which lists the other six values.
	BDAV Type = "bdav"
	// BlurayClips is a flattened Blu-ray backup: the numbered STREAM clips ("00800.m2ts",
	// IsClipName) lie loose in a folder, without BDMV/, possibly next to loose .mpls/.clpi/.bdmv
	// files. Every clip-named file of the folder (plus that metadata) is ONE version; no clip is
	// ever removed on its own (docs/DECISIONS.md D9 "Loose clip sets").
	BlurayClips Type = "bluray_clips"
	// DVDClips is the DVD equivalent reported for Plex-listed loose VOB files (VTS_01_1.VOB,
	// VIDEO_TS.VOB …). On disk such a folder is a flat DVD (DVD, Disc.Flat).
	DVDClips Type = "dvd_clips"
)

// Valid reports whether t is one of the Type constants.
func (t Type) Valid() bool {
	switch t {
	case Bluray, UHDBluray, DVD, HDDVD, AVCHD, ISO, BDAV, BlurayClips, DVDClips:
		return true
	}
	return false
}

// Loose reports a loose clip set (BlurayClips, DVDClips): the clip-named files of one folder.
func (t Type) Loose() bool { return t == BlurayClips || t == DVDClips }

// ProtectOnly reports whether discs of this kind are only ever shown and protected, never
// grouped for removal (docs/research/disc-structures.md §6.10 #6: home video, recorder and
// legacy formats no media server plays).
func (t Type) ProtectOnly() bool {
	switch t {
	case HDDVD, AVCHD, BDAV:
		return true
	}
	return !t.Valid()
}

// Label is a short human-readable name, e.g. "UHD Blu-ray (BDMV)".
func (t Type) Label() string {
	switch t {
	case Bluray:
		return "Blu-ray (BDMV)"
	case UHDBluray:
		return "UHD Blu-ray (BDMV)"
	case DVD:
		return "DVD (VIDEO_TS)"
	case HDDVD:
		return "HD DVD (HVDVD_TS)"
	case AVCHD:
		return "AVCHD"
	case ISO:
		return "Disc image (ISO)"
	case BDAV:
		return "Blu-ray recording (BDAV)"
	case BlurayClips:
		return "Blu-ray clips (loose .m2ts)"
	case DVDClips:
		return "DVD files (loose VOB)"
	}
	return "Unknown disc"
}

// priority orders the structure kinds when one folder holds several (the first is reported).
func (t Type) priority() int {
	switch t {
	case UHDBluray, Bluray:
		return 0
	case DVD:
		return 1
	case HDDVD:
		return 2
	case AVCHD:
		return 3
	case BDAV:
		return 4
	case BlurayClips:
		return 5
	}
	return 6
}

// Errors reported in Disc.Err (wrapped with details) or returned by Detect/Measure. Test with
// errors.Is. Any non-nil Disc.Err means the disc must be protected.
var (
	// ErrIncomplete: a disc structure lacks required parts (e.g. BDMV/ without index.bdmv, no
	// playlist or no clip, a main-feature clip that is missing, a zero-byte image) — typically
	// a backup that is still being written or a partial copy.
	ErrIncomplete = errors.New("disc structure is incomplete")
	// ErrUnreadable: the disc metadata (index.bdmv, every playlist, the IFO files) could not be
	// parsed.
	ErrUnreadable = errors.New("disc metadata could not be read")
	// ErrSetUnclear: the "Disc N"/"CDn" folders or images of a multi-disc set are not numbered
	// 1..N without gaps, mix disc kinds, or sit next to a disc rooted in the folder itself.
	ErrSetUnclear = errors.New("multi-disc set is unclear")
	// ErrMixedLayout: one folder holds several disc structures (e.g. BDMV/ and VIDEO_TS/).
	ErrMixedLayout = errors.New("several disc structures share one folder")
	// ErrSymlink: a disc structure, image or folder is a symbolic link (never followed).
	ErrSymlink = errors.New("symbolic link (never followed)")
	// ErrLimit: a configured bound (Options) was exceeded.
	ErrLimit = errors.New("disc exceeds the configured limits")
	// ErrInsideDisc: Detect was asked to look into a folder that lies inside a disc structure
	// (e.g. BDMV/BACKUP); detect its disc root instead (RootOf, DetectDir).
	ErrInsideDisc = errors.New("folder lies inside a disc structure")
	// ErrChanged: a folder or file was replaced while it was being read.
	ErrChanged = errors.New("disc changed while it was read")
	// ErrFollowSymlinks: Options.FollowSymlinks is set; the package never follows symlinks.
	ErrFollowSymlinks = errors.New("following symbolic links is not supported")
	// ErrNotAbsolute: Detect and Measure need absolute local paths.
	ErrNotAbsolute = errors.New("path is not absolute")
)

// Default bounds (Options zero values).
const (
	DefaultMaxFiles         = 50000    // directory entries visited per Measure/Inspect
	DefaultMaxDepth         = 16       // folder levels below an owned entry
	DefaultMaxDirEntries    = 20000    // entries read from one folder listing
	DefaultMaxPlaylists     = 4000     // BDMV/PLAYLIST/*.mpls files parsed per disc
	DefaultMaxMetadataBytes = 64 << 20 // bytes of metadata files read per disc
)

// Options bounds the work of Detect, Inspect and Measure. Zero or negative values select the
// defaults above.
type Options struct {
	// MaxFiles caps the directory entries (files and folders) visited below the owned entries
	// of one disc; beyond it the disc gets ErrLimit.
	MaxFiles int
	// MaxDepth caps the folder depth below an owned entry (BDMV/BACKUP/PLAYLIST is depth 2).
	MaxDepth int
	// MaxDirEntries caps the entries read from any single folder listing.
	MaxDirEntries int
	// MaxPlaylists caps the .mpls files parsed per Blu-ray disc.
	MaxPlaylists int
	// MaxMetadataBytes caps the bytes of metadata files (index.bdmv, .mpls, .clpi, .IFO) read
	// per disc.
	MaxMetadataBytes int64
	// FollowSymlinks must stay false: the package never follows symbolic links, and Detect
	// refuses (ErrFollowSymlinks) when it is set. It exists so the contract is explicit.
	FollowSymlinks bool
	// KnownPlaylists optionally lists playlist ids (e.g. "00800") known to be the main feature
	// of a disc; they win libbluray's "known playlist" tie-break (both > 30 min, same chapters,
	// video and audio class). Empty by default.
	KnownPlaylists []string
}

func (o Options) normalized() Options {
	if o.MaxFiles <= 0 {
		o.MaxFiles = DefaultMaxFiles
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxDirEntries <= 0 {
		o.MaxDirEntries = DefaultMaxDirEntries
	}
	if o.MaxPlaylists <= 0 {
		o.MaxPlaylists = DefaultMaxPlaylists
	}
	if o.MaxMetadataBytes <= 0 {
		o.MaxMetadataBytes = DefaultMaxMetadataBytes
	}
	return o
}

// Feature describes one title of a disc: the main feature (Disc.Main), another long playlist
// (Disc.Alternates) or one disc of a set (Disc.DiscFeatures). Unknown values are zero/"".
type Feature struct {
	// Playlist identifies the title relative to its disc root, slash-separated, with the
	// names as they are on disk: "BDMV/PLAYLIST/00800.mpls" (or "BDMV/BACKUP/PLAYLIST/…" when
	// only the backup copy was readable), "VIDEO_TS/VTS_01_0.IFO", "VTS_01_0.IFO" (flat DVD).
	Playlist string
	// DurationMs is the play time: Blu-ray Σ(OUT−IN)/45 over the play items; DVD the longest
	// program chain of the title set; multi-disc sets the sum over the discs.
	DurationMs int64
	// Width and Height of the primary video (from the stream format, e.g. 3840×2160).
	Width, Height int
	// VideoCodec is a models.VCodec* value.
	VideoCodec string
	// FrameRate: "23.976", "24", "25", "29.97", "50" or "59.94".
	FrameRate string
	// BitDepth: 10 for HEVC (UHD Blu-ray mandates Main 10), 8 for the other disc codecs.
	BitDepth int
	// DynamicRange is dv_hdr10 (a Dolby Vision enhancement layer or DV stream; UHD Blu-ray DV
	// is dual-layer over HDR10), hdr10plus, hdr10 or sdr; "" when no video stream was found.
	DynamicRange models.DynamicRange
	// DVProfile is 7 when the playlist carries a Dolby Vision enhancement-layer stream.
	DVProfile int
	// AudioTracks are the primary audio streams in stream-number order (the first is the
	// Default). Blu-ray reports Channels 1/2 for mono/stereo and 0 (unknown) for multichannel;
	// Atmos and DTS:X are not recorded in the disc metadata. LanguageCode is ISO 639-2/B;
	// Language (the display name) is left to the caller.
	AudioTracks []models.AudioTrack
	// SubtitleTracks are the presentation-graphics (Blu-ray "pgs", text "textst") or
	// sub-picture (DVD "vobsub") streams.
	SubtitleTracks []models.SubtitleTrack
	// Chapters: Blu-ray entry marks; DVD chapters (PTTs) of the title.
	Chapters int
	// Clips is the number of distinct clips: Blu-ray angle-0 .m2ts files, DVD title VOBs.
	Clips int
	// ClipIDs lists them in play order: Blu-ray clip ids ("00800"), DVD VOB file names.
	ClipIDs []string
	// Bytes is the size of those clips — the "feature size" used for ranking (never the whole
	// disc, whose menus, extras and 3D interleaved files would always win).
	Bytes int64
}

// Disc is one full-disc backup: a single disc, or a multi-disc set ("Disc 1", "Disc 2" …)
// that together make up one copy of a movie. Detect fills the identity fields; Inspect adds
// sizes and the main feature.
type Disc struct {
	// Type of the (first) disc.
	Type Type
	// Root is the disc root as Dupearr sees it: the folder that contains BDMV/, VIDEO_TS/ …
	// (possibly the movie folder itself), or the image file. For a set, Roots[0].
	Root string
	// Roots lists every disc root of the set in disc order; [Root] for a single disc.
	Roots []string
	// OwnedEntries are the absolute paths the disc owns, sorted: at each disc root the
	// allowlisted structure entries (Blu-ray: BDMV, CERTIFICATE, AACS, MAKEMKV, BDSVM, SLYVM,
	// ANYVM, SNP, FilmIndex.xml, discatt.dat; DVD: VIDEO_TS, AUDIO_TS, JACKET_P, or only the
	// VIDEO_TS.*/VTS_* files of a flat DVD; HD DVD: HVDVD_TS, ADV_OBJ, AACS; AVCHD: AVCHD or
	// PRIVATE/AVCHD; BDAV: BDAV; a loose clip set: each clip-named file and loose .mpls/.clpi/
	// .bdmv/.ssif file), the image file, or — for a "Disc N"/.dvdmedia folder that holds nothing
	// else — the whole folder. Never the movie folder itself, a sibling video, artwork, NFO,
	// subtitles, extras or unknown entries. This is exactly what a whole-disc removal moves.
	OwnedEntries []string
	// FileCount and TotalSize: regular files under the owned entries (Inspect).
	FileCount int
	TotalSize int64
	// Main is the main feature (Inspect). nil for images and protect-only kinds (attributes
	// unknown by design) and when nothing could be read; it may be partially filled (sizes
	// only) when Err is set.
	Main *Feature
	// Err is non-nil when the disc is incomplete, unreadable, unclear or unsafe (see the Err*
	// values). Such a disc must be protected.
	Err error

	// Flat: a DVD whose VIDEO_TS.IFO/VTS_* files sit directly in the root; always true for a
	// BlurayClips set (its clips sit directly in the root).
	Flat bool
	// Is3D: BDMV/STREAM/SSIF holds files (a Blu-ray 3D disc).
	Is3D bool
	// Alternates are the other feature-length (> 30 min) playlists of a single disc whose clip
	// set differs from the main feature (several cuts on one disc), longest first, at most 16.
	Alternates []Feature
	// DiscFeatures are the main features of each disc of a set, aligned with Roots (nil for a
	// single disc). Main then holds their sum.
	DiscFeatures []Feature
	// FreedBytes counts the bytes of files with a single link (hardlinked files free nothing
	// when moved away); HardlinkedFiles counts the others.
	FreedBytes      int64
	HardlinkedFiles int
	// Irregular counts symlinks, devices, sockets, pipes and mount points below the owned
	// entries (never followed or crossed). A disc with any is not removable.
	Irregular int
	// NewestModTime is the newest modification time of any file or folder below the owned
	// entries.
	NewestModTime time.Time
	// Fingerprint identifies the owned entries' tree at Inspect time (Measure): compare it with
	// a fresh Measure right before acting on the disc.
	Fingerprint string

	opts      Options
	baseErr   error // Err as it was before the first Inspect (Detect's findings)
	inspected bool
}

// Discs is the number of discs in the set (1 for a single disc).
func (d *Disc) Discs() int {
	if d == nil || len(d.Roots) == 0 {
		return 1
	}
	return len(d.Roots)
}

// Readable reports whether the main-feature metadata was read without any problem.
func (d *Disc) Readable() bool { return d != nil && d.Err == nil && d.Main != nil }

// FeatureBytes is the size used for ranking: the main feature's clips, or the whole disc when
// they are unknown (images, protect-only kinds).
func (d *Disc) FeatureBytes() int64 {
	if d == nil {
		return 0
	}
	if d.Main != nil && d.Main.Bytes > 0 {
		return d.Main.Bytes
	}
	return d.TotalSize
}

// Owns reports whether the local path p is one of the disc's owned entries or lies inside
// one. Paths are compared after filepath.Clean, case-sensitively (Dupearr's local view).
func (d *Disc) Owns(p string) bool {
	if d == nil || p == "" {
		return false
	}
	p = filepath.Clean(p)
	for _, e := range d.OwnedEntries {
		e = filepath.Clean(e)
		if p == e || strings.HasPrefix(p, e+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Removable reports whether the disc itself allows a whole-disc removal, and why not. It does
// not apply the user's settings (allowDiscRemoval, manual approval, recycle bin): callers
// check those on top. Requires Inspect.
func (d *Disc) Removable() (bool, string) {
	switch {
	case d == nil:
		return false, "no disc"
	case d.Type.ProtectOnly():
		return false, d.Type.Label() + " backups are never removed"
	case d.Err != nil:
		return false, "the disc could not be verified completely: " + d.Err.Error()
	case !d.inspected:
		return false, "the disc has not been inspected"
	case d.Irregular > 0:
		return false, "the disc contains symbolic links, special files or mount points"
	case len(d.OwnedEntries) == 0 || d.FileCount == 0:
		return false, "the disc owns no files"
	}
	return true, ""
}
