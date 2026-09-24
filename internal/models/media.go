// Package models holds the domain types shared by every Dupearr package.
// Keep this package free of I/O and heavy logic; small pure helpers are fine.
package models

import "time"

// MediaType is the kind of content a duplicate group represents.
type MediaType string

const (
	MediaTypeMovie   MediaType = "movie"
	MediaTypeEpisode MediaType = "episode"
)

// Resolution tiers (normalized). See docs/ARCHITECTURE.md §4.1 for derivation.
const (
	Res2160 = "2160"
	Res1440 = "1440"
	Res1080 = "1080"
	Res720  = "720"
	Res576  = "576"
	Res480  = "480"
	ResSD   = "sd"
)

// DynamicRange is the normalized HDR format of a version.
type DynamicRange string

const (
	DRDolbyVisionHDR10 DynamicRange = "dv_hdr10"  // DV with HDR10 base layer (P7, P8.1)
	DRDolbyVision      DynamicRange = "dv"        // DV without HDR10 fallback (P5)
	DRHDR10Plus        DynamicRange = "hdr10plus" // HDR10+
	DRHDR10            DynamicRange = "hdr10"
	DRHLG              DynamicRange = "hlg"
	DRSDR              DynamicRange = "sdr"
)

// Normalized video codecs.
const (
	VCodecAV1   = "av1"
	VCodecHEVC  = "hevc"
	VCodecH264  = "h264"
	VCodecVC1   = "vc1"
	VCodecMPEG2 = "mpeg2"
	VCodecMPEG4 = "mpeg4"
	VCodecVP9   = "vp9"
	VCodecOther = "other"
)

// Normalized audio formats (primary track), best-first in the default order.
const (
	AudioTrueHDAtmos = "truehd_atmos"
	AudioTrueHD      = "truehd"
	AudioDTSX        = "dtsx"
	AudioDTSHDMA     = "dts_hd_ma"
	AudioDTSHDHRA    = "dts_hd_hra"
	AudioEAC3Atmos   = "eac3_atmos"
	AudioFLAC        = "flac"
	AudioPCM         = "pcm"
	AudioDTS         = "dts"
	AudioEAC3        = "eac3"
	AudioAC3         = "ac3"
	AudioAAC         = "aac"
	AudioOpus        = "opus"
	AudioMP3         = "mp3"
	AudioOther       = "other"
)

// Normalized release sources.
const (
	SourceRemux = "remux"
	// SourceDisc is a full-disc backup (BDMV / VIDEO_TS / ISO …, see DiscInfo): the same audio and
	// video as a remux, but not playable in Plex. Ranked right after remux by default.
	SourceDisc    = "disc"
	SourceBluray  = "bluray"
	SourceWebDL   = "webdl"
	SourceWebRip  = "webrip"
	SourceHDTV    = "hdtv"
	SourceDVD     = "dvd"
	SourceSDTV    = "sdtv"
	SourceUnknown = "unknown"
)

// AudioTrack is one audio stream of a version.
type AudioTrack struct {
	Format       string `json:"format"`       // normalized, see Audio* constants
	Codec        string `json:"codec"`        // raw codec from the media server (e.g. "truehd", "dca")
	Profile      string `json:"profile"`      // raw profile (e.g. "ma", "dts-hd ma")
	Channels     int    `json:"channels"`     // e.g. 8 for 7.1
	Language     string `json:"language"`     // display language, e.g. "English"
	LanguageCode string `json:"languageCode"` // ISO 639-2/B when available, e.g. "eng"
	Title        string `json:"title"`
	Default      bool   `json:"default"`
	Atmos        bool   `json:"atmos"`
}

// SubtitleTrack is one subtitle stream of a version.
type SubtitleTrack struct {
	Codec        string `json:"codec"`
	Language     string `json:"language"`
	LanguageCode string `json:"languageCode"`
	Forced       bool   `json:"forced"`
	External     bool   `json:"external"` // sidecar file (Plex stream with key/external)
}

// MediaPart is one physical file of a version (stacked media has several).
type MediaPart struct {
	ID        int64  `json:"id"`        // Plex Part id
	Path      string `json:"path"`      // path as the media server sees it
	LocalPath string `json:"localPath"` // path as Dupearr sees it ("" when unmapped/not mounted)
	Size      int64  `json:"size"`      // bytes
	Duration  int64  `json:"duration"`  // milliseconds
	// Exists/Accessible come from Plex checkFiles=1; nil when unknown.
	Exists     *bool `json:"exists,omitempty"`
	Accessible *bool `json:"accessible,omitempty"`
	// SharedWith lists other media-server item ids (rating keys) that reference this same file,
	// e.g. the other episode(s) of a multi-episode file.
	SharedWith []string `json:"sharedWith,omitempty"`
	// LinkCount is the hardlink count when LocalPath could be stat'ed (0 = unknown).
	LinkCount int `json:"linkCount,omitempty"`
	// Inode is "<device>:<inode>" when LocalPath could be stat'ed (TEXT: values can exceed int64).
	Inode string `json:"inode,omitempty"`
}

// ArrFileInfo is the *arr enrichment for a version (nil when no *arr tracks the file).
type ArrFileInfo struct {
	InstanceID          int64     `json:"instanceId"`
	InstanceName        string    `json:"instanceName"`
	Kind                ArrKind   `json:"kind"`
	FileID              int64     `json:"fileId"`     // moviefile / episodefile id
	ItemID              int64     `json:"itemId"`     // movieId / seriesId
	EpisodeIDs          []int64   `json:"episodeIds"` // Sonarr episodes referencing the file
	ItemPath            string    `json:"itemPath"`   // movie/series folder as the *arr sees it
	Monitored           bool      `json:"monitored"`
	QualityName         string    `json:"qualityName"` // e.g. "Bluray-2160p"
	QualitySource       string    `json:"qualitySource"`
	QualityResolution   int       `json:"qualityResolution"`
	QualityModifier     string    `json:"qualityModifier"` // e.g. "remux"
	CustomFormats       []string  `json:"customFormats"`
	CustomFormatScore   *int      `json:"customFormatScore,omitempty"`
	ReleaseGroup        string    `json:"releaseGroup"`
	Edition             string    `json:"edition"`
	Languages           []string  `json:"languages"`
	DynamicRangeType    string    `json:"dynamicRangeType"` // raw mediaInfo.videoDynamicRangeType
	Tags                []string  `json:"tags"`             // movie/series tag labels (e.g. "dupearr-keep")
	QualityCutoffNotMet bool      `json:"qualityCutoffNotMet"`
	SceneName           string    `json:"sceneName"`
	DateAdded           time.Time `json:"dateAdded"`
}

// Disc kinds (DiscInfo.Type): the string values of internal/disc.Type.
const (
	DiscBluray    = "bluray"     // BDMV Blu-ray
	DiscUHDBluray = "uhd_bluray" // BDMV Ultra HD Blu-ray (index.bdmv version 0300)
	DiscDVD       = "dvd"        // DVD-Video (VIDEO_TS/ or flat VIDEO_TS.IFO + VTS_*.VOB)
	DiscHDDVD     = "hddvd"      // HD DVD (HVDVD_TS/): always protected
	DiscAVCHD     = "avchd"      // camcorder AVCHD: always protected
	DiscBDAV      = "bdav"       // Blu-ray recorder BDAV/: always protected
	DiscISO       = "iso"        // an .iso/.img disc image (one file; attributes unknown)
	// DiscBlurayClips is a flattened Blu-ray backup: the numbered STREAM clips ("00174.m2ts" …) lie
	// loose in a folder, and Plex lists each as a separate version. Every clip-named file of the
	// folder (plus loose .mpls/.clpi/.bdmv files) is ONE version; no clip is removed on its own.
	DiscBlurayClips = "bluray_clips"
	// DiscDVDClips is the DVD equivalent: loose VTS_NN_N.VOB / VIDEO_TS.* files Plex lists.
	DiscDVDClips = "dvd_clips"
)

// IsLooseClipDisc reports a loose clip set kind (DiscBlurayClips, DiscDVDClips).
func IsLooseClipDisc(kind string) bool { return kind == DiscBlurayClips || kind == DiscDVDClips }

// Disc origins (DiscInfo.Origin).
const (
	// DiscOriginFilesystem: Dupearr found the disc on disk next to a Plex item. Plex's default
	// scanners skip disc structures, so Plex does not list it; the version key is
	// "disc:<serverID>:<hex SHA-1 of the normalized local root>" (DiscKeyPrefix).
	DiscOriginFilesystem = "filesystem"
	// DiscOriginPlex: a custom Plex scanner exposes the disc's files (one Part per BDMV/STREAM
	// clip, or IFO + VOB) as one Plex version; it keeps its "plex:" key and parts. A loose clip set
	// (DiscBlurayClips) Plex lists — one version per clip — is origin "plex" too; its merged
	// version is keyed "disc:<serverID>:<disc.ClipSetHash(folder)>".
	DiscOriginPlex = "plex"
)

// DiscKeyPrefix starts the key of a disc version found on disk ("disc:<serverID>:<hash>").
const DiscKeyPrefix = "disc:"

// DiscInfo describes a full-disc backup (docs/research/disc-structures.md): a Blu-ray / DVD folder
// structure (hundreds of files) or a disc image that together make up ONE version of a movie. A
// disc is only ever removed as a whole — its OwnedEntries moved into Dupearr's recycle bin, by
// manual approval, when settings.AllowDiscRemoval — and no file inside it is ever removed on its
// own by any method.
type DiscInfo struct {
	Type      string `json:"type"`      // Disc* kind
	Root      string `json:"root"`      // disc root as the media server sees it (folder holding BDMV/ …, or the image file); a set: its first disc
	LocalRoot string `json:"localRoot"` // the same as Dupearr sees it ("" when no path mapping covers it)
	Discs     int    `json:"discs"`     // discs in a multi-disc set ("Disc 1", "Disc 2" …); 1 = single disc
	FileCount int    `json:"fileCount"` // regular files under the disc's owned entries
	// MainFeature is the main title relative to its disc root, e.g. "BDMV/PLAYLIST/00800.mpls" or
	// "VIDEO_TS/VTS_01_0.IFO" ("" when unknown).
	MainFeature string `json:"mainFeature,omitempty"`
	Readable    bool   `json:"readable"` // the main feature's metadata was read without any problem
	Origin      string `json:"origin"`   // DiscOrigin*

	// Additive to the shared contract (the integration owns these types).

	// Roots and LocalRoots list every disc of a set in disc order (Roots[0] == Root).
	Roots      []string `json:"roots,omitempty"`
	LocalRoots []string `json:"localRoots,omitempty"`
	// OwnedEntries are the local absolute paths a whole-disc removal moves (BDMV/, CERTIFICATE/ …,
	// a "Disc N" folder, the image file): never the movie folder, a sibling video, artwork, NFO,
	// subtitles or extras. Empty when Dupearr cannot reach the disc.
	OwnedEntries []string `json:"ownedEntries,omitempty"`
	// TotalBytes is the size of every file under OwnedEntries (what a removal moves; counted
	// against maxBytesPerRunGb), FeatureBytes the size of the main feature's clips (what ranking
	// compares — never the whole disc, whose menus and extras would always win) and FreedBytes the
	// bytes a removal frees (hardlinked files free nothing).
	TotalBytes      int64 `json:"totalBytes"`
	FeatureBytes    int64 `json:"featureBytes"`
	FreedBytes      int64 `json:"freedBytes"`
	HardlinkedFiles int   `json:"hardlinkedFiles,omitempty"`
	// Fingerprint identifies the owned entries' file tree (paths, sizes, modification times) when
	// the disc was inspected; the executor re-measures the disc right before moving it and refuses
	// when it changed.
	Fingerprint string `json:"fingerprint,omitempty"`
	// NewestModTime is the newest modification time of any file or folder of the disc.
	NewestModTime time.Time `json:"newestModTime,omitzero"`
	Is3D          bool      `json:"is3d,omitempty"`       // BDMV/STREAM/SSIF holds files
	Alternates    int       `json:"alternates,omitempty"` // other feature-length cuts on the disc
	// Removable reports whether the disc itself allows a whole-disc removal: inspected, complete,
	// readable metadata, no symlinks, devices or mount points inside, not a protect-only kind. The
	// settings (AllowDiscRemoval …) apply on top.
	Removable bool `json:"removable"`
	// Problem explains why the disc could not be read or verified completely ("" when fine).
	Problem string `json:"problem,omitempty"`
	// TrackedClip is the file inside the disc an *arr tracks (its path as the *arr sees it): the
	// *arr file must never be deleted through the *arr (flag disc_tracked_clip).
	TrackedClip string `json:"trackedClip,omitempty"`
	// PlexItems lists other Plex items (rating keys) that expose files of this disc: removing the
	// disc would take their files too, so the disc is protected.
	PlexItems []string `json:"plexItems,omitempty"`

	// Loose clip sets (DiscBlurayClips, DiscDVDClips; docs/DECISIONS.md D9 "Loose clip sets").

	// ClipCount is the number of clip-named files of the set (the Plex-listed clips, or every clip
	// in the folder when Dupearr can read it).
	ClipCount int `json:"clipCount,omitempty"`
	// MainClip is the file name of the main-feature candidate: the longest clip Plex lists (its
	// attributes describe the version), e.g. "00800.m2ts". MainFeature carries the same name when
	// no loose playlist could be read.
	MainClip string `json:"mainClip,omitempty"`
	// PlexMediaIDs are the Plex media ids merged into this one version (Plex lists every loose clip
	// as a version of its own); the executor treats them as part of the set.
	PlexMediaIDs []int64 `json:"mediaIds,omitempty"`
}

// IsImage reports a disc image (.iso/.img: one file).
func (d *DiscInfo) IsImage() bool { return d != nil && d.Type == DiscISO }

// IsLooseClips reports a loose clip set (a flattened Blu-ray backup, or loose DVD files).
func (d *DiscInfo) IsLooseClips() bool { return d != nil && IsLooseClipDisc(d.Type) }

// MediaVersion is one copy of a movie/episode (one Plex Media element, or a full-disc backup).
type MediaVersion struct {
	// Identity
	Key              string `json:"key"` // "plex:<serverID>:<mediaID>", or "disc:<serverID>:<hash>" for a disc found on disk
	ServerID         int64  `json:"serverId"`
	LibraryID        int64  `json:"libraryId"` // Dupearr library id
	LibraryTitle     string `json:"libraryTitle"`
	SectionKey       string `json:"sectionKey"`       // Plex library section key
	RatingKey        string `json:"ratingKey"`        // Plex item (movie/episode) rating key
	MediaID          int64  `json:"mediaId"`          // Plex Media id
	ItemTitle        string `json:"itemTitle"`        // movie title or episode title
	DisplayTitle     string `json:"displayTitle"`     // e.g. "4K DoVi/HDR10 (HEVC Main 10)"
	OptimizedVersion bool   `json:"optimizedVersion"` // Plex Optimized Version — never a duplicate

	Parts []MediaPart `json:"parts"`

	// Normalized attributes
	Container      string          `json:"container"`
	DurationMs     int64           `json:"durationMs"`
	BitrateKbps    int             `json:"bitrateKbps"`      // overall
	VideoBitrate   int             `json:"videoBitrateKbps"` // video stream, 0 if unknown
	Width          int             `json:"width"`
	Height         int             `json:"height"`
	Resolution     string          `json:"resolution"` // Res* tier
	VideoCodec     string          `json:"videoCodec"` // VCodec*
	VideoProfile   string          `json:"videoProfile"`
	BitDepth       int             `json:"bitDepth"`
	FrameRate      string          `json:"frameRate"`
	DynamicRange   DynamicRange    `json:"dynamicRange"`
	DVProfile      int             `json:"dvProfile,omitempty"`
	AudioTracks    []AudioTrack    `json:"audioTracks"`
	SubtitleTracks []SubtitleTrack `json:"subtitleTracks"`
	Source         string          `json:"source"`  // Source*
	Edition        string          `json:"edition"` // normalized edition, "" = none/theatrical
	AddedAt        time.Time       `json:"addedAt"`

	Arr *ArrFileInfo `json:"arr,omitempty"`
	// Disc is set when the version is a full-disc backup (nil for a regular file).
	Disc *DiscInfo `json:"disc,omitempty"`
}

// IsDisc reports a full-disc backup version.
func (v *MediaVersion) IsDisc() bool { return v != nil && v.Disc != nil }

// TotalSize is the size of the version: the sum of all part sizes, or — for a full disc whose
// files were measured — every file of the disc (its parts are one entry per disc root, or the clips
// a custom Plex scanner lists, which are only part of the disc).
func (v *MediaVersion) TotalSize() int64 {
	if v.Disc != nil && v.Disc.TotalBytes > 0 {
		return v.Disc.TotalBytes
	}
	var n int64
	for _, p := range v.Parts {
		n += p.Size
	}
	return n
}

// PrimaryAudio returns the default audio track, or the first one, or nil.
func (v *MediaVersion) PrimaryAudio() *AudioTrack {
	for i := range v.AudioTracks {
		if v.AudioTracks[i].Default {
			return &v.AudioTracks[i]
		}
	}
	if len(v.AudioTracks) > 0 {
		return &v.AudioTracks[0]
	}
	return nil
}

// MediaItem is the content-level context of a candidate item (a Plex movie or episode),
// produced by the collector and consumed by the grouping engine.
type MediaItem struct {
	ServerID     int64             `json:"serverId"`
	LibraryID    int64             `json:"libraryId"`
	LibraryTitle string            `json:"libraryTitle"`
	SectionKey   string            `json:"sectionKey"`
	RatingKey    string            `json:"ratingKey"`
	MediaType    MediaType         `json:"mediaType"`
	Title        string            `json:"title"` // movie title or episode title
	Year         int               `json:"year"`
	ShowTitle    string            `json:"showTitle"` // episodes only
	Season       int               `json:"season"`
	Episode      int               `json:"episode"`
	ExternalIDs  map[string]string `json:"externalIds"` // "tmdb","imdb","tvdb" → id (movie or episode)
	ShowIDs      map[string]string `json:"showIds"`     // episodes: show-level ids
	EditionTitle string            `json:"editionTitle"`
	Thumb        string            `json:"thumb"` // Plex thumb path (for poster proxy)
	AddedAt      time.Time         `json:"addedAt"`
	Versions     []MediaVersion    `json:"versions"`
}
