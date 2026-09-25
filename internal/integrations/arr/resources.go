package arr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Wire types: the subset of the Radarr/Sonarr API v3 resources Dupearr reads
// (docs/research/arr-api.md §2–§3). The *arr omits null fields, so absent keys decode to zero
// values; fields whose absence must be distinguishable from zero are pointers.

// namedRef is {id, name} (languages, custom formats).
type namedRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// qualityDef is quality.quality. Sonarr has no modifier.
type qualityDef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
	Modifier   string `json:"modifier"`
}

// qualityModel is {quality, revision}.
type qualityModel struct {
	Quality qualityDef `json:"quality"`
}

// mediaInfoResource is MediaInfoResource (same shape in Radarr and Sonarr).
type mediaInfoResource struct {
	AudioChannels         float64 `json:"audioChannels"`
	AudioCodec            string  `json:"audioCodec"`
	AudioLanguages        string  `json:"audioLanguages"` // slash-joined, e.g. "eng/fre"
	VideoBitDepth         int     `json:"videoBitDepth"`
	VideoBitrate          int64   `json:"videoBitrate"`
	VideoCodec            string  `json:"videoCodec"`
	VideoDynamicRange     string  `json:"videoDynamicRange"`
	VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
	Resolution            string  `json:"resolution"` // "WIDTHxHEIGHT"
	RunTime               string  `json:"runTime"`
	Subtitles             string  `json:"subtitles"` // slash-joined
}

// fileResource is the shared subset of MovieFileResource and EpisodeFileResource.
type fileResource struct {
	ID           int64         `json:"id"`
	MovieID      int64         `json:"movieId"`
	SeriesID     int64         `json:"seriesId"`
	SeasonNumber int           `json:"seasonNumber"`
	RelativePath string        `json:"relativePath"`
	Path         string        `json:"path"`
	Size         int64         `json:"size"`
	DateAdded    arrTime       `json:"dateAdded"`
	SceneName    string        `json:"sceneName"`
	ReleaseGroup string        `json:"releaseGroup"`
	Edition      string        `json:"edition"` // Radarr only
	Languages    []namedRef    `json:"languages"`
	Quality      *qualityModel `json:"quality"`
	// CustomFormats / CustomFormatScore are absent from the movie list's embedded movieFile, and are
	// always treated as unknown there (embeddedMovieFile).
	CustomFormats []namedRef `json:"customFormats"`
	// CustomFormatScore is int? in Radarr ≥ 5.22.4 (null = unknown) and a misleading 0 in the
	// embedded movieFile of older Radarr versions; Sonarr always sends an int.
	CustomFormatScore   *int               `json:"customFormatScore"`
	MediaInfo           *mediaInfoResource `json:"mediaInfo"`
	QualityCutoffNotMet bool               `json:"qualityCutoffNotMet"`
}

// movieResource is the subset of Radarr's MovieResource.
type movieResource struct {
	ID          int64         `json:"id"`
	Title       string        `json:"title"`
	Year        int           `json:"year"`
	TmdbID      int           `json:"tmdbId"`
	ImdbID      string        `json:"imdbId"`
	Path        string        `json:"path"`
	HasFile     *bool         `json:"hasFile"`
	MovieFileID int64         `json:"movieFileId"` // the file Radarr tracks (0 = none); authoritative
	Monitored   bool          `json:"monitored"`
	Tags        []int64       `json:"tags"`
	MovieFile   *fileResource `json:"movieFile"`
	// TitleSlug names the movie's page in Radarr's web UI (/movie/<titleSlug>): the TMDB id as a
	// string in Radarr v5 and v6 (MovieResource.cs). Only used for links.
	TitleSlug string `json:"titleSlug"`
}

// trackedFileID returns the id of the file Radarr tracks for m, or 0 when it has none.
// movieFileId is authoritative; the embedded movieFile is the fallback for payloads without it.
func (m *movieResource) trackedFileID() int64 {
	if m.MovieFileID > 0 {
		return m.MovieFileID
	}
	if m.HasFile != nil && *m.HasFile && m.MovieFile != nil && m.MovieFile.ID > 0 {
		return m.MovieFile.ID
	}
	return 0
}

// seriesResource is the subset of Sonarr's SeriesResource.
type seriesResource struct {
	ID         int64   `json:"id"`
	Title      string  `json:"title"`
	Year       int     `json:"year"`
	TvdbID     int     `json:"tvdbId"`
	ImdbID     string  `json:"imdbId"`
	Path       string  `json:"path"`
	Monitored  bool    `json:"monitored"`
	Tags       []int64 `json:"tags"`
	Statistics *struct {
		EpisodeFileCount *int `json:"episodeFileCount"`
	} `json:"statistics"`
	// TitleSlug names the series' page in Sonarr's web UI (/series/<titleSlug>), e.g. "the-expanse"
	// (from Sonarr's metadata, so it cannot be derived from an id). Only used for links.
	TitleSlug string `json:"titleSlug"`
}

// mayHaveFiles reports whether the series can have episode files (unknown statistics = maybe).
func (s *seriesResource) mayHaveFiles() bool {
	return s.Statistics == nil || s.Statistics.EpisodeFileCount == nil || *s.Statistics.EpisodeFileCount > 0
}

// episodeResource is the subset of Sonarr's EpisodeResource.
type episodeResource struct {
	ID            int64 `json:"id"`
	SeriesID      int64 `json:"seriesId"`
	SeasonNumber  int   `json:"seasonNumber"`
	EpisodeNumber int   `json:"episodeNumber"`
	EpisodeFileID int64 `json:"episodeFileId"` // 0 = no file
	Monitored     bool  `json:"monitored"`
}

// tagResource is TagResource.
type tagResource struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// queuePage is PagingResource<QueueResource> (only the fields Dupearr needs).
type queuePage struct {
	Page         int           `json:"page"`
	PageSize     int           `json:"pageSize"`
	TotalRecords int           `json:"totalRecords"`
	Records      []queueRecord `json:"records"`
}

// queueRecord is the subset of QueueResource Dupearr reads (Radarr/Sonarr
// src/*.Api.V3/Queue/QueueResource.cs). Only the item ids are decoded strictly: they decide which
// groups are deferred. The other fields only describe the entry to a person, so they are decoded
// leniently: an unexpected value is dropped instead of failing the queue read, which would send
// every group of the media type to review.
type queueRecord struct {
	MovieID  int64 `json:"movieId"`
	SeriesID int64 `json:"seriesId"`
	// Title is the release title; Status (QueueStatus), TrackedDownloadState and
	// TrackedDownloadStatus are camelCase enum names (STJson.cs).
	Title                 looseString `json:"title"`
	Status                looseString `json:"status"`
	TrackedDownloadState  looseString `json:"trackedDownloadState"`
	TrackedDownloadStatus looseString `json:"trackedDownloadStatus"`
	// StatusMessages is [{title, messages[]}] (TrackedDownloadStatusMessage), read best effort.
	StatusMessages json.RawMessage `json:"statusMessages"`
	ErrorMessage   looseString     `json:"errorMessage"`
}

// statusMessage is one element of QueueResource.statusMessages.
type statusMessage struct {
	Title    looseString     `json:"title"`
	Messages json.RawMessage `json:"messages"`
}

// looseString decodes a JSON string; any other JSON value (a number, null, an object) decodes to
// "" instead of failing the surrounding value.
type looseString string

// UnmarshalJSON implements json.Unmarshaler.
func (s *looseString) UnmarshalJSON(b []byte) error {
	// Display-only text: a value of another type is dropped, never an error.
	var v string
	if json.Unmarshal(b, &v) != nil {
		v = ""
	}
	*s = looseString(v)
	return nil
}

// mediaManagementResource is the subset of MediaManagementConfigResource.
type mediaManagementResource struct {
	RecycleBin            string `json:"recycleBin"`
	RecycleBinCleanupDays int    `json:"recycleBinCleanupDays"`
}

// statusResource is the subset of SystemResource.
type statusResource struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
}

// commandResource is the subset of CommandResource that Rescan checks: the answer to
// POST /api/v3/command and the entries of GET /api/v3/command.
type commandResource struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // queued|started|completed|failed|aborted|cancelled|orphaned
	Body   *struct {
		MovieID  *int64 `json:"movieId"`
		SeriesID *int64 `json:"seriesId"`
	} `json:"body"`
}

// itemID returns the movie id (Radarr) or series id (Sonarr) the command targets, or nil when the
// command has none (i.e. it targets every item).
func (c *commandResource) itemID(kind models.ArrKind) *int64 {
	if c.Body == nil {
		return nil
	}
	if kind == models.ArrSonarr {
		return c.Body.SeriesID
	}
	return c.Body.MovieID
}

// Request bodies. Field names are exact: the *arr silently ignores unknown properties, and a
// command without its id field targets EVERY item (docs/research/arr-api.md §2.5). Never add
// omitempty to the id or monitored fields.

type rescanMovieCommand struct {
	Name    string `json:"name"` // "RescanMovie"
	MovieID int64  `json:"movieId"`
}

type rescanSeriesCommand struct {
	Name     string `json:"name"` // "RescanSeries"
	SeriesID int64  `json:"seriesId"`
}

type movieEditorBody struct {
	MovieIDs  []int64 `json:"movieIds"`
	Monitored bool    `json:"monitored"`
}

type episodeMonitorBody struct {
	EpisodeIDs []int64 `json:"episodeIds"`
	Monitored  bool    `json:"monitored"`
}

type radarrExclusionBody struct {
	TmdbID     int    `json:"tmdbId"`
	MovieTitle string `json:"movieTitle"`
	MovieYear  int    `json:"movieYear"`
}

type sonarrExclusionBody struct {
	TvdbID int    `json:"tvdbId"`
	Title  string `json:"title"`
}

// arrTime decodes *arr timestamps strictly: null/"" = unknown (zero), RFC 3339 (with or without
// fractional seconds) or a zone-less "2006-01-02T15:04:05" (taken as UTC). Anything else is an
// error rather than a silently zero date, because dates feed the min-age safety guard.
type arrTime struct{ time.Time }

// UnmarshalJSON implements json.Unmarshaler.
func (t *arrTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("arr: timestamp must be a string: %w", err)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	if v, err := time.Parse(time.RFC3339Nano, s); err == nil {
		t.Time = v.UTC()
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if v, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("arr: unrecognized timestamp %s", strconv.Quote(s))
}
