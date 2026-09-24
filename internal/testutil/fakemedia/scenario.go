package fakemedia

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Remote (container-side) paths every fake server sees (TRaSH-style Docker layout).
const (
	// RemoteRoot corresponds to [Env.Dir] locally.
	RemoteRoot = "/data"
	// RemoteMediaRoot corresponds to [Env.MediaRoot] locally. Part.File / Show.Folder values are
	// relative to it.
	RemoteMediaRoot = RemoteRoot + "/media"
)

// Library types (Plex section types).
const (
	LibraryMovie = "movie"
	LibraryShow  = "show"
)

// *arr kinds.
const (
	KindRadarr = "radarr"
	KindSonarr = "sonarr"
)

// Instance names used by [Base] and the built-in scenarios.
const (
	InstanceRadarr   = "radarr"
	InstanceRadarr4K = "radarr4k"
	InstanceSonarr   = "sonarr"
)

// Section keys and directories (relative to the media root) used by [Base].
const (
	SectionMovies   = "1"
	SectionMovies4K = "2"
	SectionTV       = "3"

	DirMovies   = "movies"
	DirMovies4K = "movies4k"
	DirTV       = "tv"
)

// Default credentials and identities used by [Base]. They are fake values for a local fake.
const (
	DefaultPlexToken         = "fAkEpLeXtOkEn0000001"
	DefaultMachineIdentifier = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c"
	DefaultPlexVersion       = "1.42.1.10060-4e8b05daf"
	DefaultRadarrAPIKey      = "fa4e0000000000000000000000007878"
	DefaultRadarr4KAPIKey    = "fa4e0000000000000000000000007879"
	DefaultSonarrAPIKey      = "fa4e0000000000000000000000008989"
	DefaultRadarrVersion     = "6.4.4.10685"
	DefaultSonarrVersion     = "4.0.20.3014"
	// DefaultKeepTag is the *arr tag label Dupearr treats as "protect" (docs/DECISIONS.md D3).
	DefaultKeepTag = "dupearr-keep"
)

// Scenario is a declarative description of the fake world: one Plex server with its libraries and
// items, and any number of Radarr/Sonarr instances. Build one with [Base] (or [Default],
// [Minimal], [Empty]) and append content; [New]/[Start] validate it.
type Scenario struct {
	Name        string
	Description string
	Server      PlexServer
	Libraries   []Library
	Movies      []Movie
	Shows       []Show
	Instances   []Instance
}

// PlexServer describes the fake Plex Media Server.
type PlexServer struct {
	FriendlyName      string
	MachineIdentifier string
	Version           string
	Token             string // the owner's X-Plex-Token
	Owner             string // myPlexUsername
	// AllowMediaDeletion is Plex's "Allow media deletion" setting (absent from GET / when false).
	AllowMediaDeletion bool
	// AutoEmptyTrash is Plex's "Empty trash automatically after every scan": when true, refreshes
	// remove media whose files vanished instead of keeping them listed as unavailable.
	AutoEmptyTrash bool
}

// Library is a Plex library section.
type Library struct {
	Key   string   // section key, digits (e.g. "1")
	Title string   // e.g. "Movies"
	Type  string   // LibraryMovie | LibraryShow
	Dirs  []string // locations, relative to the media root (e.g. "movies")
	// Scanner is the library's scanner: "" or ScannerMovie / ScannerSeries (Plex's defaults, which
	// skip full-disc backups), or ScannerMovieDiscImage / ScannerSeriesDiscImage (the legacy
	// disc-image scanners; only the movie one exposes discs, see Disc).
	Scanner string
}

// Movie is a Plex movie item (one metadata item with one or more versions).
type Movie struct {
	RatingKey string // "" = assigned automatically
	Section   string // library key
	Title     string
	Year      int
	Edition   string // Plex editionTitle
	GUID      string // "" = plex://movie/<hash>
	TmdbID    int
	ImdbID    string
	Versions  []Version
	// Discs are full-disc backups in the movie's folder (see Disc). Plex's default scanner skips
	// them; a movie may consist of discs only (it then has no Plex item unless its library uses
	// the disc-image scanner).
	Discs []Disc
	// ClipSets are flattened disc backups: loose clips directly in a movie folder (see ClipSet).
	// Plex's default scanner lists every listed clip as its own version of this item.
	ClipSets []ClipSet
	// Arr lists *arr movies related to this item. Instances that track a version are added
	// automatically; list them here to set folder, monitoring or tags, or to add a movie that
	// tracks no version (or a different movie, see Version.TrackedTmdbID).
	Arr []ArrMovie
}

// ArrMovie is a Radarr movie entry.
type ArrMovie struct {
	Instance string
	TmdbID   int    // 0 = the Plex movie's
	ImdbID   string // "" = the Plex movie's (when TmdbID is 0)
	Title    string // "" = the Plex movie's
	Year     int    // 0 = the Plex movie's
	// Folder is the movie folder relative to the media root; "" = directory of the tracked
	// version's file (or of the movie's first version).
	Folder      string
	Unmonitored bool
	Tags        []string // tag labels (must exist on the instance)
}

// Show is a Plex TV show with its episodes.
type Show struct {
	RatingKey string // "" = automatic
	Section   string
	Title     string
	Year      int
	GUID      string
	TvdbID    int
	TmdbID    int
	ImdbID    string
	Folder    string // series folder relative to the media root (Sonarr's series path)
	Episodes  []Episode
	Arr       []ArrSeries // Sonarr series entries (instances tracking a version are added automatically)
	// Discs are full-disc backups inside the show folder (e.g. a season folder holding BDMV/):
	// never exposed by Plex, never tracked (TV discs are protect-only).
	Discs []Disc
}

// ArrSeries is a Sonarr series entry.
type ArrSeries struct {
	Instance    string
	Unmonitored bool
	Tags        []string
}

// Episode is a Plex episode.
type Episode struct {
	RatingKey   string
	Season      int
	Episode     int
	Title       string
	GUID        string
	TvdbID      int // episode-level ids (Plex Guid[] of the episode)
	TmdbID      int
	Unmonitored bool // Sonarr episode monitoring
	Versions    []Version
}

// Version is one Plex Media element (one copy) plus its *arr tracking.
type Version struct {
	Parts     []Part // ≥1; several = stacked (cd1/cd2)
	Video     Video
	Audio     []Audio
	Subtitles []Subtitle
	// Unanalyzed simulates a file Plex has not analyzed yet: no width/height/codecs/bitrate and no
	// streams (Dupearr flags it "unanalyzed").
	Unanalyzed  bool
	Container   string // "" = from the first part's extension
	DurationMs  int64  // 0 = 2 h for movies, 45 min for episodes
	BitrateKbps int    // overall; 0 = derived from size and duration
	// Optimized marks a Plex Optimized Version (proxyType 42 + target); put its file under a
	// "Plex Versions" folder like Plex does.
	Optimized       bool
	OptimizedTarget string        // default "Optimized for Mobile"
	Age             time.Duration // how long ago Plex added it (0 = 30 days)

	// Tracked names the *arr instance that tracks this file ("" = untracked).
	Tracked string
	// TrackedTmdbID makes Radarr track the file under a different movie than the Plex item's
	// (e.g. a suspect merge of two films). The movie must be listed in Movie.Arr.
	TrackedTmdbID       int
	Quality             string // *arr quality name (e.g. "Remux-2160p"); "" = parsed from the file name
	ReleaseGroup        string
	Edition             string // Radarr edition of the tracked file
	SceneName           string
	CustomFormats       []string
	CustomFormatScore   int
	QualityCutoffNotMet bool
}

// Part is one file of a version.
type Part struct {
	File       string // relative to the media root, forward slashes
	Size       int64  // bytes (the file is truncated to this size: sparse)
	DurationMs int64  // 0 = version duration split evenly over the parts
	// LinkTo creates this file as a hard link of another scenario file (relative path) instead of
	// a new file.
	LinkTo string
	// Missing leaves the file absent on disk (Plex still lists it; exists=false with checkFiles).
	Missing bool
}

// Video describes the video stream.
type Video struct {
	Codec          string // Plex codec: hevc, h264, av1, vc1, mpeg2video, mpeg4
	Profile        string // e.g. "main 10", "high"
	Width, Height  int
	BitDepth       int
	FrameRate      float64 // e.g. 23.976
	BitrateKbps    int     // video stream bitrate (0 = 85% of the overall bitrate)
	ColorPrimaries string  // bt709, bt2020
	ColorSpace     string  // bt709, bt2020nc
	ColorTrc       string  // bt709, smpte2084 (PQ), arib-std-b67 (HLG)
	DOVIProfile    int     // 0 = no Dolby Vision
	DOVILevel      int
	DOVIBLCompatID int  // 1 = HDR10 compatible (P8.1), 6 = P7, 0 = none (P5)
	HDR10Plus      bool // adds "HDR10+" to the display titles (UNVERIFIED Plex label)
}

// Audio describes an audio stream.
type Audio struct {
	Codec        string // Plex codec: truehd, eac3, ac3, dca, aac, flac, pcm, opus, mp3
	Profile      string // e.g. "ma" for DTS-HD MA
	Channels     int
	LanguageCode string // ISO 639-2 (eng, deu, fra, …)
	Title        string // e.g. "TrueHD Atmos 7.1"
	Atmos        bool
	Default      bool
	BitrateKbps  int
}

// Subtitle describes a subtitle stream.
type Subtitle struct {
	Codec        string // srt, pgs, ass
	LanguageCode string
	Forced       bool
	External     bool // sidecar file
}

// Instance is a Radarr or Sonarr instance.
type Instance struct {
	Name         string // identifier used by Version.Tracked etc. (e.g. "radarr4k")
	Kind         string // KindRadarr | KindSonarr
	InstanceName string // system/status instanceName (e.g. "Radarr4K")
	Version      string
	APIKey       string
	URLBase      string   // "" or "/radarr"
	RootFolders  []string // relative to the media root
	// RecycleBin is the *arr recycle bin as the *arr sees it (e.g. /data/recycle/radarr); "" makes
	// deletes permanent. Paths outside RemoteRoot make deletes fail with 500.
	RecycleBin            string
	RecycleBinCleanupDays int
	// AutoUnmonitor is autoUnmonitorPreviouslyDownloadedMovies / …Episodes.
	AutoUnmonitor bool
	Tags          []string // tag labels, ids assigned 1..n
}

// ---------------------------------------------------------------------------
// Builders and presets
// ---------------------------------------------------------------------------

// Base returns a scenario with the standard server (media deletion allowed), three libraries
// ("Movies" → movies, "Movies 4K" → movies4k, "TV Shows" → tv) and three instances (radarr,
// radarr4k, sonarr), but no content.
func Base(name string) *Scenario {
	return &Scenario{
		Name: name,
		Server: PlexServer{
			FriendlyName:       "Fake Plex",
			MachineIdentifier:  DefaultMachineIdentifier,
			Version:            DefaultPlexVersion,
			Token:              DefaultPlexToken,
			Owner:              "fakeowner",
			AllowMediaDeletion: true,
		},
		Libraries: []Library{
			{Key: SectionMovies, Title: "Movies", Type: LibraryMovie, Dirs: []string{DirMovies}},
			{Key: SectionMovies4K, Title: "Movies 4K", Type: LibraryMovie, Dirs: []string{DirMovies4K}},
			{Key: SectionTV, Title: "TV Shows", Type: LibraryShow, Dirs: []string{DirTV}},
		},
		Instances: []Instance{
			{
				Name: InstanceRadarr, Kind: KindRadarr, InstanceName: "Radarr", Version: DefaultRadarrVersion,
				APIKey: DefaultRadarrAPIKey, RootFolders: []string{DirMovies}, RecycleBinCleanupDays: 7,
				Tags: []string{DefaultKeepTag},
			},
			{
				Name: InstanceRadarr4K, Kind: KindRadarr, InstanceName: "Radarr4K", Version: DefaultRadarrVersion,
				APIKey: DefaultRadarr4KAPIKey, RootFolders: []string{DirMovies4K},
				RecycleBin: RemoteRoot + "/recycle/radarr4k", RecycleBinCleanupDays: 7,
				Tags: []string{"4k", DefaultKeepTag},
			},
			{
				Name: InstanceSonarr, Kind: KindSonarr, InstanceName: "Sonarr", Version: DefaultSonarrVersion,
				APIKey: DefaultSonarrAPIKey, RootFolders: []string{DirTV},
				RecycleBin: RemoteRoot + "/recycle/sonarr", RecycleBinCleanupDays: 7,
				Tags: []string{DefaultKeepTag},
			},
		},
	}
}

// AddMovie appends a movie and returns s (for chaining).
func (s *Scenario) AddMovie(m Movie) *Scenario {
	s.Movies = append(s.Movies, m)
	return s
}

// AddShow appends a show and returns s.
func (s *Scenario) AddShow(sh Show) *Scenario {
	s.Shows = append(s.Shows, sh)
	return s
}

// Instance returns the instance named name, or nil.
func (s *Scenario) Instance(name string) *Instance {
	for i := range s.Instances {
		if s.Instances[i].Name == name {
			return &s.Instances[i]
		}
	}
	return nil
}

// GiB returns n gibibytes in bytes.
func GiB(n float64) int64 { return int64(n * (1 << 30)) }

// MiB returns n mebibytes in bytes.
func MiB(n float64) int64 { return int64(n * (1 << 20)) }

// Mins returns n minutes in milliseconds (Plex durations are milliseconds).
func Mins(n float64) int64 { return int64(n * 60_000) }

// DolbyVisionUHD is a 3840x2160 10-bit HEVC Dolby Vision video stream. Profile 8 (BL compat 1) and
// 7 (BL compat 6) carry an HDR10 base layer (dv_hdr10); profile 5 has none (dv).
func DolbyVisionUHD(profile int) Video {
	v := HDR10UHD()
	v.DOVIProfile = profile
	v.DOVILevel = 6
	switch profile {
	case 7:
		v.DOVIBLCompatID = 6
	case 5:
		v.DOVIBLCompatID = 0
		// Profile 5 uses IPTPQc2; Plex reports no HDR10 transfer for the base layer.
		v.ColorTrc, v.ColorPrimaries, v.ColorSpace = "", "", ""
	default:
		v.DOVIBLCompatID = 1
	}
	return v
}

// HDR10UHD is a 3840x2160 10-bit HEVC HDR10 (PQ) video stream.
func HDR10UHD() Video {
	return Video{
		Codec: "hevc", Profile: "main 10", Width: 3840, Height: 2160, BitDepth: 10, FrameRate: 23.976,
		ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", ColorTrc: "smpte2084",
	}
}

// HDR10PlusUHD is HDR10UHD with HDR10+ dynamic metadata.
func HDR10PlusUHD() Video {
	v := HDR10UHD()
	v.HDR10Plus = true
	return v
}

// HLGUHD is a 3840x2160 10-bit HEVC HLG video stream.
func HLGUHD() Video {
	v := HDR10UHD()
	v.ColorTrc = "arib-std-b67"
	return v
}

// FHD is a 1920x1080 SDR video stream with the given Plex codec (h264, hevc, av1, vc1).
func FHD(codec string) Video { return sdrVideo(codec, 1920, 1080) }

// HD is a 1280x720 SDR video stream.
func HD(codec string) Video { return sdrVideo(codec, 1280, 720) }

// SD is an SDR video stream with the given dimensions (e.g. 720x480 mpeg4).
func SD(codec string, width, height int) Video { return sdrVideo(codec, width, height) }

func sdrVideo(codec string, w, h int) Video {
	v := Video{
		Codec: codec, Width: w, Height: h, BitDepth: 8, FrameRate: 23.976,
		ColorPrimaries: "bt709", ColorSpace: "bt709", ColorTrc: "bt709",
	}
	switch codec {
	case "h264":
		v.Profile = "high"
	case "hevc":
		v.Profile = "main"
	case "mpeg4":
		v.Profile = "advanced simple"
		v.ColorPrimaries, v.ColorSpace, v.ColorTrc = "", "", ""
	}
	return v
}

// TrueHDAtmos is a 7.1 Dolby TrueHD Atmos track.
func TrueHDAtmos(lang string) Audio {
	return Audio{Codec: "truehd", Channels: 8, LanguageCode: lang, Title: "TrueHD Atmos 7.1", Atmos: true, Default: true, BitrateKbps: 4600}
}

// TrueHD is a lossless Dolby TrueHD track without Atmos.
func TrueHD(lang string, channels int) Audio {
	return Audio{Codec: "truehd", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 3900}
}

// DTSHDMA is a DTS-HD Master Audio track (codec dca, profile ma).
func DTSHDMA(lang string, channels int) Audio {
	return Audio{Codec: "dca", Profile: "ma", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 3500}
}

// DTS is a core DTS track.
func DTS(lang string, channels int) Audio {
	return Audio{Codec: "dca", Profile: "dts", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 1509}
}

// EAC3 is a Dolby Digital Plus track.
func EAC3(lang string, channels int) Audio {
	return Audio{Codec: "eac3", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 640}
}

// EAC3Atmos is a Dolby Digital Plus Atmos track (JOC).
func EAC3Atmos(lang string) Audio {
	return Audio{Codec: "eac3", Channels: 6, LanguageCode: lang, Title: "DDP 5.1 Atmos", Atmos: true, Default: true, BitrateKbps: 768}
}

// AC3 is a Dolby Digital track.
func AC3(lang string, channels int) Audio {
	return Audio{Codec: "ac3", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 448}
}

// AAC is an AAC-LC track.
func AAC(lang string, channels int) Audio {
	return Audio{Codec: "aac", Profile: "lc", Channels: channels, LanguageCode: lang, Default: true, BitrateKbps: 192}
}

// NonDefault returns a copy of a with Default=false (for secondary tracks).
func NonDefault(a Audio) Audio {
	a.Default = false
	return a
}

// Sub is an embedded SRT subtitle.
func Sub(lang string) Subtitle { return Subtitle{Codec: "srt", LanguageCode: lang} }

// PGS is an embedded PGS (Blu-ray) subtitle.
func PGS(lang string) Subtitle { return Subtitle{Codec: "pgs", LanguageCode: lang} }

// ForcedSub is a forced embedded SRT subtitle.
func ForcedSub(lang string) Subtitle { return Subtitle{Codec: "srt", LanguageCode: lang, Forced: true} }

// ExternalSub is a sidecar SRT subtitle.
func ExternalSub(lang string) Subtitle {
	return Subtitle{Codec: "srt", LanguageCode: lang, External: true}
}

// language describes an ISO 639-2 code.
type language struct {
	Name string // English display name (Plex "language", *arr language name)
	Tag  string // BCP-47 / ISO 639-1 (Plex "languageTag")
	ID   int    // *arr language id
}

var languages = map[string]language{
	"eng": {"English", "en", 1},
	"fra": {"French", "fr", 2},
	"fre": {"French", "fr", 2},
	"spa": {"Spanish", "es", 3},
	"deu": {"German", "de", 4},
	"ger": {"German", "de", 4},
	"ita": {"Italian", "it", 5},
	"dan": {"Danish", "da", 6},
	"nld": {"Dutch", "nl", 7},
	"dut": {"Dutch", "nl", 7},
	"jpn": {"Japanese", "ja", 8},
	"rus": {"Russian", "ru", 11},
	"por": {"Portuguese", "pt", 18},
	"kor": {"Korean", "ko", 21},
	"swe": {"Swedish", "sv", 20},
	"nor": {"Norwegian", "no", 15},
}

func lookupLanguage(code string) language {
	if l, ok := languages[strings.ToLower(code)]; ok {
		return l
	}
	if code == "" {
		return language{Name: "Unknown", Tag: "", ID: 0}
	}
	return language{Name: code, Tag: "", ID: 0}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

var (
	reSectionKey = regexp.MustCompile(`^[0-9]+$`)
	reRatingKey  = regexp.MustCompile(`^[0-9A-Za-z]+$`)
)

// validRel reports whether p is a clean, relative, forward-slash path without "..".
func validRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

// under reports whether rel is inside dir (both relative, forward slashes).
func under(rel, dir string) bool {
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// Validate checks the scenario for inconsistencies (unknown sections/instances, invalid paths,
// duplicate ids, *arr tracking that the real apps could not represent, …).
func (s *Scenario) Validate() error {
	if s == nil {
		return errors.New("fakemedia: nil scenario")
	}
	v := &validator{
		s: s, sections: map[string]*Library{}, instances: map[string]*Instance{}, files: map[string]Part{},
		rks: map[string]bool{}, trackedMovies: map[string]*Movie{}, arrShows: map[string]string{},
		discOwned: map[string]string{}, discRoots: map[string]string{},
		clipFiles: map[string]string{}, clipFolders: map[string]string{},
	}
	v.run()
	if len(v.errs) == 0 {
		return nil
	}
	return fmt.Errorf("fakemedia: invalid scenario %q: %w", s.Name, errors.Join(v.errs...))
}

type validator struct {
	s         *Scenario
	errs      []error
	sections  map[string]*Library
	instances map[string]*Instance
	files     map[string]Part // rel → first part using it
	rks       map[string]bool
	// trackedMovies maps "instance|tmdb" → the movie whose version that Radarr movie tracks, across
	// movies (Radarr holds one file per movie, even when Plex splits it into several items).
	trackedMovies map[string]*Movie
	// arrShows maps "instance|tvdb" → the show using that Sonarr series (one series per tvdb id).
	arrShows map[string]string
	// discOwned maps each disc-owned entry (media-root relative) → the disc it belongs to;
	// discRoots maps each disc root → its disc (one disc per root).
	discOwned, discRoots map[string]string
	// clipFiles maps each loose clip set file (media-root relative) → its set; clipFolders maps each
	// clip set folder → its set (one set per folder).
	clipFiles, clipFolders map[string]string
}

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) run() {
	s := v.s
	if s.Server.Token == "" {
		v.addf("server: token is required")
	}
	if s.Server.MachineIdentifier == "" {
		v.addf("server: machine identifier is required")
	}
	for i := range s.Libraries {
		l := &s.Libraries[i]
		switch {
		case !reSectionKey.MatchString(l.Key):
			v.addf("library %q: key must be digits", l.Key)
		case v.sections[l.Key] != nil:
			v.addf("library %q: duplicate key", l.Key)
		}
		if l.Title == "" {
			v.addf("library %q: title is required", l.Key)
		}
		if l.Type != LibraryMovie && l.Type != LibraryShow {
			v.addf("library %q: type must be %q or %q", l.Key, LibraryMovie, LibraryShow)
		}
		if len(l.Dirs) == 0 {
			v.addf("library %q: at least one directory is required", l.Key)
		}
		for _, d := range l.Dirs {
			if !validRel(d) {
				v.addf("library %q: invalid directory %q", l.Key, d)
			}
		}
		switch {
		case l.Scanner == "":
		case l.Type == LibraryMovie && (l.Scanner == ScannerMovie || l.Scanner == ScannerMovieDiscImage):
		case l.Type == LibraryShow && (l.Scanner == ScannerSeries || l.Scanner == ScannerSeriesDiscImage):
		default:
			v.addf("library %q: unknown scanner %q for a %s library", l.Key, l.Scanner, l.Type)
		}
		v.sections[l.Key] = l
	}
	for i := range s.Instances {
		in := &s.Instances[i]
		switch {
		case in.Name == "" || strings.EqualFold(in.Name, "plex"):
			v.addf("instance %q: invalid name", in.Name)
		case v.instances[in.Name] != nil:
			v.addf("instance %q: duplicate name", in.Name)
		}
		if in.Kind != KindRadarr && in.Kind != KindSonarr {
			v.addf("instance %q: kind must be %q or %q", in.Name, KindRadarr, KindSonarr)
		}
		if in.APIKey == "" {
			v.addf("instance %q: api key is required", in.Name)
		}
		if in.URLBase != "" && (!strings.HasPrefix(in.URLBase, "/") || strings.HasSuffix(in.URLBase, "/") || !validRel(in.URLBase[1:])) {
			v.addf("instance %q: url base must look like /name", in.Name)
		}
		if in.RecycleBin != "" && !strings.HasPrefix(in.RecycleBin, "/") {
			v.addf("instance %q: recycle bin must be an absolute path", in.Name)
		}
		for _, d := range in.RootFolders {
			if !validRel(d) {
				v.addf("instance %q: invalid root folder %q", in.Name, d)
			}
		}
		seen := map[string]bool{}
		for _, t := range in.Tags {
			if t == "" || seen[strings.ToLower(t)] {
				v.addf("instance %q: empty or duplicate tag %q", in.Name, t)
			}
			seen[strings.ToLower(t)] = true
		}
		v.instances[in.Name] = in
	}
	for i := range s.Movies {
		v.movie(&s.Movies[i])
	}
	for i := range s.Shows {
		v.show(&s.Shows[i])
	}
	v.links()
	v.discOverlaps()
	v.clipOverlaps()
}

func (v *validator) ratingKey(owner, rk string) {
	if rk == "" {
		return
	}
	if !reRatingKey.MatchString(rk) {
		v.addf("%s: rating key %q must match ^[0-9A-Za-z]+$", owner, rk)
		return
	}
	if v.rks[rk] {
		v.addf("%s: duplicate rating key %q", owner, rk)
	}
	v.rks[rk] = true
}

func (v *validator) instance(owner, name, kind string) *Instance {
	in := v.instances[name]
	if in == nil {
		v.addf("%s: unknown *arr instance %q", owner, name)
		return nil
	}
	if in.Kind != kind {
		v.addf("%s: instance %q is a %s, not a %s", owner, name, in.Kind, kind)
		return nil
	}
	return in
}

func (v *validator) tags(owner string, in *Instance, tags []string) {
	if in == nil {
		return
	}
	for _, t := range tags {
		found := false
		for _, it := range in.Tags {
			if strings.EqualFold(it, t) {
				found = true
			}
		}
		if !found {
			v.addf("%s: tag %q is not defined on instance %q", owner, t, in.Name)
		}
	}
}

func (v *validator) section(owner, key, typ string) *Library {
	l := v.sections[key]
	if l == nil {
		v.addf("%s: unknown section %q", owner, key)
		return nil
	}
	if l.Type != typ {
		v.addf("%s: section %q is a %s library", owner, key, l.Type)
		return nil
	}
	return l
}

func (v *validator) version(owner string, lib *Library, ver *Version) {
	if len(ver.Parts) == 0 {
		v.addf("%s: a version needs at least one part", owner)
	}
	if ver.DurationMs < 0 || ver.BitrateKbps < 0 {
		v.addf("%s: negative duration or bitrate", owner)
	}
	if !ver.Unanalyzed && (ver.Video.Width < 0 || ver.Video.Height < 0) {
		v.addf("%s: negative video dimensions", owner)
	}
	for _, p := range ver.Parts {
		po := fmt.Sprintf("%s: part %q", owner, p.File)
		if !validRel(p.File) {
			v.addf("%s: invalid relative path", po)
			continue
		}
		if p.Size <= 0 {
			v.addf("%s: size must be > 0", po)
		}
		if lib != nil {
			ok := false
			for _, d := range lib.Dirs {
				if under(p.File, d) && p.File != d {
					ok = true
				}
			}
			if !ok {
				v.addf("%s: not inside a location of section %q", po, lib.Key)
			}
		}
		if prev, ok := v.files[p.File]; ok {
			if prev.Size != p.Size || prev.LinkTo != p.LinkTo || prev.Missing != p.Missing {
				v.addf("%s: the same file is declared with different size/link/missing settings", po)
			}
		} else {
			v.files[p.File] = p
		}
	}
}

func (v *validator) links() {
	for rel, p := range v.files {
		if p.LinkTo == "" {
			continue
		}
		target, ok := v.files[p.LinkTo]
		switch {
		case p.LinkTo == rel:
			v.addf("part %q: cannot hard link to itself", rel)
		case !ok:
			v.addf("part %q: link target %q is not a scenario file", rel, p.LinkTo)
		case target.LinkTo != "" || target.Missing:
			v.addf("part %q: link target %q must be a regular, present file", rel, p.LinkTo)
		case p.Missing:
			v.addf("part %q: a hard link cannot be missing", rel)
		case target.Size != p.Size:
			v.addf("part %q: hard link size differs from its target", rel)
		}
	}
}

func (v *validator) movie(m *Movie) {
	owner := fmt.Sprintf("movie %q", m.Title)
	if m.Title == "" {
		v.addf("movie in section %q: title is required", m.Section)
	}
	v.ratingKey(owner, m.RatingKey)
	lib := v.section(owner, m.Section, LibraryMovie)
	if len(m.Versions) == 0 && len(m.Discs) == 0 && len(m.ClipSets) == 0 {
		v.addf("%s: at least one version is required (or a full-disc backup in Discs or ClipSets)", owner)
	}
	v.discs(owner, lib, m.Discs, "")
	type key struct {
		inst string
		tmdb int
	}
	tracked := map[key]int{}
	declared := map[key]*ArrMovie{}
	for i := range m.Arr {
		a := &m.Arr[i]
		in := v.instance(owner, a.Instance, KindRadarr)
		v.tags(owner, in, a.Tags)
		k := key{a.Instance, a.TmdbID}
		if k.tmdb == 0 {
			k.tmdb = m.TmdbID
		}
		if k.tmdb <= 0 {
			v.addf("%s: *arr movie on %q needs a tmdb id", owner, a.Instance)
		}
		if declared[k] != nil {
			v.addf("%s: *arr movie tmdb %d on %q declared twice", owner, k.tmdb, a.Instance)
		}
		declared[k] = a
		if a.Folder != "" && !validRel(a.Folder) {
			v.addf("%s: invalid *arr folder %q", owner, a.Folder)
		}
	}
	for i := range m.Versions {
		ver := &m.Versions[i]
		vo := fmt.Sprintf("%s version %d", owner, i+1)
		v.version(vo, lib, ver)
		if ver.Tracked == "" {
			if ver.TrackedTmdbID != 0 {
				v.addf("%s: TrackedTmdbID without Tracked", vo)
			}
			continue
		}
		v.instance(vo, ver.Tracked, KindRadarr)
		if len(ver.Parts) > 1 {
			v.addf("%s: Radarr cannot track multi-part (stacked) versions", vo)
		}
		if ver.Optimized {
			v.addf("%s: an optimized version cannot be *arr-tracked", vo)
		}
		k := key{ver.Tracked, ver.TrackedTmdbID}
		if k.tmdb == 0 {
			k.tmdb = m.TmdbID
		} else if declared[k] == nil {
			v.addf("%s: TrackedTmdbID %d must be declared in Movie.Arr", vo, k.tmdb)
		}
		if k.tmdb <= 0 {
			v.addf("%s: tracked movies need a tmdb id", vo)
		}
		tracked[k]++
		if tracked[k] > 1 {
			v.addf("%s: instance %q already tracks a file for tmdb %d (Radarr holds one file per movie)", vo, ver.Tracked, k.tmdb)
		}
		gk := fmt.Sprintf("%s|%d", ver.Tracked, k.tmdb)
		if other, ok := v.trackedMovies[gk]; ok && other != m {
			v.addf("%s: instance %q already tracks a file for tmdb %d in movie %q (Radarr holds one file per movie)", vo, ver.Tracked, k.tmdb, other.Title)
		}
		v.trackedMovies[gk] = m
		if a := declared[k]; a != nil && a.Folder != "" && len(ver.Parts) > 0 && !under(ver.Parts[0].File, a.Folder) {
			v.addf("%s: tracked file is outside the *arr movie folder %q", vo, a.Folder)
		}
	}
	for i := range m.Discs {
		d := &m.Discs[i]
		if d.Tracked == "" {
			continue
		}
		do := fmt.Sprintf("%s disc %q", owner, d.Root)
		v.instance(do, d.Tracked, KindRadarr)
		k := key{d.Tracked, m.TmdbID}
		if k.tmdb <= 0 {
			v.addf("%s: tracked movies need a tmdb id", do)
		}
		tracked[k]++
		if tracked[k] > 1 {
			v.addf("%s: instance %q already tracks a file for tmdb %d (Radarr holds one file per movie)", do, d.Tracked, k.tmdb)
		}
		gk := fmt.Sprintf("%s|%d", d.Tracked, k.tmdb)
		if other, ok := v.trackedMovies[gk]; ok && other != m {
			v.addf("%s: instance %q already tracks a file for tmdb %d in movie %q (Radarr holds one file per movie)", do, d.Tracked, k.tmdb, other.Title)
		}
		v.trackedMovies[gk] = m
		if a := declared[k]; a != nil && a.Folder != "" && !under(d.Root, a.Folder) {
			v.addf("%s: tracked disc is outside the *arr movie folder %q", do, a.Folder)
		}
	}
	for i := range m.ClipSets {
		cs := &m.ClipSets[i]
		for _, inst := range v.clipSet(owner, lib, cs) {
			co := fmt.Sprintf("%s clip set %q", owner, cs.Folder)
			k := key{inst, m.TmdbID}
			if k.tmdb <= 0 {
				v.addf("%s: tracked movies need a tmdb id", co)
			}
			tracked[k]++
			if tracked[k] > 1 {
				v.addf("%s: instance %q already tracks a file for tmdb %d (Radarr holds one file per movie)", co, inst, k.tmdb)
			}
			gk := fmt.Sprintf("%s|%d", inst, k.tmdb)
			if other, ok := v.trackedMovies[gk]; ok && other != m {
				v.addf("%s: instance %q already tracks a file for tmdb %d in movie %q (Radarr holds one file per movie)", co, inst, k.tmdb, other.Title)
			}
			v.trackedMovies[gk] = m
			if a := declared[k]; a != nil && a.Folder != "" && !under(cs.Folder, a.Folder) {
				v.addf("%s: tracked clip is outside the *arr movie folder %q", co, a.Folder)
			}
		}
	}
}

func (v *validator) show(sh *Show) {
	owner := fmt.Sprintf("show %q", sh.Title)
	if sh.Title == "" {
		v.addf("show in section %q: title is required", sh.Section)
	}
	v.ratingKey(owner, sh.RatingKey)
	lib := v.section(owner, sh.Section, LibraryShow)
	if !validRel(sh.Folder) {
		v.addf("%s: folder %q is invalid (required, relative to the media root)", owner, sh.Folder)
	} else if lib != nil {
		ok := false
		for _, d := range lib.Dirs {
			if under(sh.Folder, d) && sh.Folder != d {
				ok = true
			}
		}
		if !ok {
			v.addf("%s: folder is not inside a location of section %q", owner, lib.Key)
		}
	}
	v.discs(owner, lib, sh.Discs, sh.Folder)
	usesArr := len(sh.Arr) > 0
	seenArr := map[string]bool{}
	for _, a := range sh.Arr {
		in := v.instance(owner, a.Instance, KindSonarr)
		v.tags(owner, in, a.Tags)
		if seenArr[a.Instance] {
			v.addf("%s: series declared twice on %q", owner, a.Instance)
		}
		seenArr[a.Instance] = true
	}
	type epKey struct{ s, e int }
	type covKey struct {
		inst string
		ep   epKey
	}
	seenEp := map[epKey]bool{}
	covered := map[covKey]string{} // → file
	for i := range sh.Episodes {
		ep := &sh.Episodes[i]
		eo := fmt.Sprintf("%s S%02dE%02d", owner, ep.Season, ep.Episode)
		v.ratingKey(eo, ep.RatingKey)
		if ep.Season < 0 || ep.Episode < 1 {
			v.addf("%s: invalid season/episode number", eo)
		}
		k := epKey{ep.Season, ep.Episode}
		if seenEp[k] {
			v.addf("%s: duplicate episode", eo)
		}
		seenEp[k] = true
		if len(ep.Versions) == 0 {
			v.addf("%s: at least one version is required", eo)
		}
		for j := range ep.Versions {
			ver := &ep.Versions[j]
			vo := fmt.Sprintf("%s version %d", eo, j+1)
			v.version(vo, lib, ver)
			if len(ver.Parts) > 0 && sh.Folder != "" && !under(ver.Parts[0].File, sh.Folder) {
				v.addf("%s: file is outside the show folder %q", vo, sh.Folder)
			}
			if ver.TrackedTmdbID != 0 {
				v.addf("%s: TrackedTmdbID is Radarr-only", vo)
			}
			if ver.Tracked == "" {
				continue
			}
			usesArr = true
			v.instance(vo, ver.Tracked, KindSonarr)
			if len(ver.Parts) != 1 {
				v.addf("%s: Sonarr tracks single-part files only", vo)
				continue
			}
			ck := covKey{ver.Tracked, k}
			if f, ok := covered[ck]; ok && f != ver.Parts[0].File {
				v.addf("%s: instance %q already links another file to this episode (one file per episode)", vo, ver.Tracked)
			}
			covered[ck] = ver.Parts[0].File
		}
	}
	if usesArr && sh.TvdbID <= 0 {
		v.addf("%s: Sonarr series need a tvdb id", owner)
	}
	if sh.TvdbID > 0 {
		insts := map[string]bool{}
		for _, a := range sh.Arr {
			insts[a.Instance] = true
		}
		for _, ep := range sh.Episodes {
			for _, ver := range ep.Versions {
				if ver.Tracked != "" {
					insts[ver.Tracked] = true
				}
			}
		}
		for inst := range insts {
			k := fmt.Sprintf("%s|%d", inst, sh.TvdbID)
			if other, ok := v.arrShows[k]; ok {
				v.addf("%s: instance %q already has a series with tvdb %d (show %q)", owner, inst, sh.TvdbID, other)
				continue
			}
			v.arrShows[k] = sh.Title
		}
	}
}
