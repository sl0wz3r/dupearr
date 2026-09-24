package plex

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Item fetches one item's detail (checkFiles=1, includeGuids=1, skipRefresh=1) and maps it to a
// MediaItem with all Versions (streams → normalized attributes via mediainfo). For episodes it
// also fills ShowIDs (grandparent show guids, cached per client) plus Season/Episode/ShowTitle.
// Optimized versions are returned with OptimizedVersion=true (callers skip them).
//
// The caller must set ServerID/LibraryID on the item and ServerID/LibraryID/Key
// ("plex:<serverID>:<mediaID>") on every version: this client does not know Dupearr's server
// id, so those fields are left zero/"". SectionKey and LibraryTitle are filled when Plex reports
// them. Only movies and episodes are accepted; any other item type is an error. Part
// Exists/Accessible are tri-state: nil when Plex did not report them (unknown, never "missing").
// An episode whose season Plex does not report (hidden seasons) gets Season -1, never 0, so it
// cannot be mistaken for a special; a missing episode number stays 0.
func (c *Client) Item(ctx context.Context, ratingKey string) (*models.MediaItem, error) {
	if !validKey(ratingKey) {
		return nil, fmt.Errorf("%w: rating key %q", ErrInvalidArgument, ratingKey)
	}
	m, mc, err := c.fetchMetadata(ctx, ratingKey, url.Values{
		"checkFiles":   {"1"},
		"includeGuids": {"1"},
		"skipRefresh":  {"1"},
	})
	if err != nil {
		return nil, err
	}
	if rk := m.RatingKey.String(); rk != "" && rk != ratingKey {
		return nil, fmt.Errorf("plex: GET /library/metadata/%s returned item %q", ratingKey, rk)
	}
	typ := models.MediaType(strings.ToLower(m.Type.String()))
	if typ != models.MediaTypeMovie && typ != models.MediaTypeEpisode {
		return nil, fmt.Errorf("plex: item %s has unsupported type %q (want movie or episode)", ratingKey, m.Type.String())
	}

	item := &models.MediaItem{
		SectionKey:   firstNonEmpty(m.LibrarySectionID.String(), mc.LibrarySectionID.String()),
		LibraryTitle: firstNonEmpty(m.LibrarySectionTitle.String(), mc.LibrarySectionTitle.String()),
		RatingKey:    ratingKey,
		MediaType:    typ,
		Title:        m.Title.String(),
		Year:         m.Year.Int(),
		ExternalIDs:  externalIDs(m.GUID.String(), m.Guids),
		ShowIDs:      map[string]string{},
		EditionTitle: m.EditionTitle.String(),
		Thumb:        m.Thumb.String(),
		AddedAt:      unixTime(m.AddedAt),
		Versions:     make([]models.MediaVersion, 0, len(m.Media)),
	}
	if typ == models.MediaTypeEpisode {
		item.ShowTitle = m.GrandparentTitle.String()
		item.Season = seasonNumber(m)
		item.Episode = episodeNumber(m)
		// The show poster is a better thumbnail for the UI than an episode still.
		item.Thumb = firstNonEmpty(m.GrandparentThumb.String(), m.Thumb.String())
		ids, err := c.showIDsFor(ctx, m)
		if err != nil {
			return nil, err
		}
		item.ShowIDs = ids
	}
	// Titles whose words are not edition markers ("The Final Cut (2004)", an episode called
	// "The Director's Cut", the show "Extended Family").
	titles := []string{m.Title.String(), m.OriginalTitle.String()}
	if typ == models.MediaTypeEpisode {
		titles = append(titles, m.GrandparentTitle.String())
	}
	for i := range m.Media {
		item.Versions = append(item.Versions, mapVersion(item, &m.Media[i], titles))
	}
	return item, nil
}

// fetchMetadata GETs /library/metadata/{rk} and returns its single Metadata element.
func (c *Client) fetchMetadata(ctx context.Context, ratingKey string, q url.Values) (*metadataDTO, *containerDTO, error) {
	escPath := "/library/metadata/" + url.PathEscape(ratingKey)
	mc, _, err := c.getContainer(ctx, escPath, q, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(mc.Metadata) == 0 {
		return nil, nil, fmt.Errorf("plex: GET %s: no metadata in response: %w", escPath, ErrNotFound)
	}
	return &mc.Metadata[0], mc, nil
}

// showIDsFor returns the show-level external ids of an episode: the grandparent show's Guid[]
// (fetched once per show and cached), its plex:// GUID (grandparentGuid) and, for legacy agents,
// the show id embedded in the episode guid (thetvdb://<show>/<season>/<episode>).
func (c *Client) showIDsFor(ctx context.Context, m *metadataDTO) (map[string]string, error) {
	ids := make(map[string]string)
	if grk := m.GrandparentRatingKey.String(); validKey(grk) {
		show, err := c.showExternalIDs(ctx, grk)
		if err != nil {
			return nil, fmt.Errorf("plex: show %s of episode %s: %w", grk, m.RatingKey.String(), err)
		}
		for k, v := range show {
			ids[k] = v
		}
	}
	if g := m.GrandparentGUID.String(); isPlexGUID(g) {
		if _, ok := ids["plex"]; !ok {
			ids["plex"] = g
		}
	}
	if k, v, hasPath, ok := parseGUID(m.GUID.String()); ok && hasPath {
		if _, exists := ids[k]; !exists {
			ids[k] = v
		}
	}
	return ids, nil
}

// showExternalIDs returns (a copy of) the cached external ids of a show, fetching them once.
func (c *Client) showExternalIDs(ctx context.Context, showRatingKey string) (map[string]string, error) {
	if v, ok := c.showIDs.Load(showRatingKey); ok {
		if cached, ok := v.(map[string]string); ok {
			return copyMap(cached), nil
		}
	}
	m, _, err := c.fetchMetadata(ctx, showRatingKey, url.Values{"includeGuids": {"1"}, "skipRefresh": {"1"}})
	if err != nil {
		return nil, err
	}
	ids := externalIDs(m.GUID.String(), m.Guids)
	c.showIDs.Store(showRatingKey, copyMap(ids))
	return ids, nil
}

// mapVersion maps one Media element of an item detail to a MediaVersion (Key/ServerID/LibraryID
// are left for the caller). titles are the item's titles, used to tell edition tokens in file
// names apart from title words.
func mapVersion(item *models.MediaItem, md *mediaDTO, titles []string) models.MediaVersion {
	v := models.MediaVersion{
		LibraryTitle:     item.LibraryTitle,
		SectionKey:       item.SectionKey,
		RatingKey:        item.RatingKey,
		MediaID:          int64(md.ID),
		ItemTitle:        item.Title,
		OptimizedVersion: isOptimized(md),
		Parts:            make([]models.MediaPart, 0, len(md.Parts)),
		DurationMs:       int64(md.Duration),
		BitrateKbps:      md.Bitrate.Int(),
		Width:            md.Width.Int(),
		Height:           md.Height.Int(),
		VideoProfile:     md.VideoProfile.String(),
		FrameRate:        md.VideoFrameRate.String(),
		AudioTracks:      []models.AudioTrack{},
		SubtitleTracks:   []models.SubtitleTrack{},
		AddedAt:          item.AddedAt,
	}

	var partDuration int64
	var streams list[streamDTO]
	partContainer := ""
	for _, p := range md.Parts {
		v.Parts = append(v.Parts, models.MediaPart{
			ID:         int64(p.ID),
			Path:       string(p.File),
			Size:       int64(p.Size),
			Duration:   int64(p.Duration),
			Exists:     boolPtr(p.Exists),
			Accessible: boolPtr(p.Accessible),
		})
		partDuration += int64(p.Duration)
		if len(streams) == 0 && len(p.Streams) > 0 {
			streams = p.Streams // stacked parts repeat the same tracks: use the first part's
		}
		if partContainer == "" {
			partContainer = p.Container.String()
		}
	}
	if v.DurationMs <= 0 {
		v.DurationMs = partDuration
	}
	firstFile := ""
	if len(v.Parts) > 0 {
		firstFile = v.Parts[0].Path
	}

	codec := md.VideoCodec.String()
	if vs := pickVideoStream(streams); vs != nil {
		if v.Width <= 0 {
			v.Width = vs.Width.Int()
		}
		if v.Height <= 0 {
			v.Height = vs.Height.Int()
		}
		if codec == "" {
			codec = vs.Codec.String()
		}
		if v.VideoProfile == "" {
			v.VideoProfile = vs.Profile.String()
		}
		if v.FrameRate == "" && vs.FrameRate > 0 {
			v.FrameRate = strconv.FormatFloat(float64(vs.FrameRate), 'f', -1, 64)
		}
		v.VideoBitrate = vs.Bitrate.Int()
		v.BitDepth = vs.BitDepth.Int()
		v.DisplayTitle = firstNonEmpty(vs.ExtendedDisplayTitle.String(), vs.DisplayTitle.String())
		doviProfile, blCompat := 0, 0
		if vs.DOVIProfile != nil {
			doviProfile = vs.DOVIProfile.Int()
		}
		if vs.DOVIBLCompatID != nil {
			blCompat = vs.DOVIBLCompatID.Int()
		}
		doviPresent := vs.DOVIPresent != nil && bool(*vs.DOVIPresent)
		v.DynamicRange, v.DVProfile = mediainfo.DynamicRange(vs.ColorTrc.String(), vs.ColorPrimaries.String(),
			doviPresent, doviProfile, blCompat, vs.HDR10Plus,
			vs.ExtendedDisplayTitle.String()+" "+vs.DisplayTitle.String())
	}
	// Without a video stream the dynamic range stays "" (unknown): it must not pass for SDR data.

	v.VideoCodec = mediainfo.VideoCodec(codec)
	v.Resolution = mediainfo.ResolutionTier(v.Width, v.Height, md.VideoResolution.String())
	v.Container = mediainfo.ContainerFromPath(firstFile, firstNonEmpty(md.Container.String(), partContainer))
	v.Source = mediainfo.SourceFromPath(firstFile)
	v.Edition = mediainfo.EditionWithTitles(firstFile, item.EditionTitle, "", titles...)
	if v.DisplayTitle == "" && v.Resolution != "" {
		v.DisplayTitle = mediainfo.ResolutionLabel(v.Resolution)
		if v.VideoCodec != "" {
			v.DisplayTitle += " (" + strings.ToUpper(v.VideoCodec) + ")"
		}
	}

	for i := range streams {
		s := &streams[i]
		switch int(s.StreamType) {
		case streamAudio:
			v.AudioTracks = append(v.AudioTracks, audioTrack(s))
		case streamSubtitle:
			v.SubtitleTracks = append(v.SubtitleTracks, models.SubtitleTrack{
				Codec:        firstNonEmpty(s.Codec.String(), s.Format.String()),
				Language:     s.Language.String(),
				LanguageCode: streamLanguageCode(s),
				Forced:       s.Forced != nil && bool(*s.Forced),
				External:     isExternalStream(s),
			})
		}
	}
	// No analyzed audio streams (unanalyzed file): fall back to the Media summary so the audio
	// criteria still have something to compare.
	if len(v.AudioTracks) == 0 && md.AudioCodec.String() != "" {
		f, atmos := mediainfo.AudioFormat(md.AudioCodec.String(), md.AudioProfile.String(), "", md.AudioChannels.Int())
		v.AudioTracks = append(v.AudioTracks, models.AudioTrack{
			Format:   f,
			Codec:    md.AudioCodec.String(),
			Profile:  md.AudioProfile.String(),
			Channels: md.AudioChannels.Int(),
			Atmos:    atmos,
		})
	}
	return v
}

func audioTrack(s *streamDTO) models.AudioTrack {
	title := strings.Join(nonEmpty(s.ExtendedDisplayTitle.String(), s.DisplayTitle.String(), s.Title.String()), " ")
	f, atmos := mediainfo.AudioFormat(s.Codec.String(), s.Profile.String(), title, s.Channels.Int())
	return models.AudioTrack{
		Format:       f,
		Codec:        s.Codec.String(),
		Profile:      s.Profile.String(),
		Channels:     s.Channels.Int(),
		Language:     s.Language.String(),
		LanguageCode: streamLanguageCode(s),
		Title:        s.Title.String(),
		Default:      s.isDefault(),
		Atmos:        atmos,
	}
}

// pickVideoStream returns the default video stream, else the first one, else nil.
func pickVideoStream(streams list[streamDTO]) *streamDTO {
	var first *streamDTO
	for i := range streams {
		s := &streams[i]
		if int(s.StreamType) != streamVideo {
			continue
		}
		if s.isDefault() {
			return s
		}
		if first == nil {
			first = s
		}
	}
	return first
}

// streamLanguageCode normalizes a stream's language to ISO 639-2/B from languageCode, then
// languageTag, then the display language.
func streamLanguageCode(s *streamDTO) string {
	for _, v := range []string{s.LanguageCode.String(), s.LanguageTag.String(), s.Language.String()} {
		if c := mediainfo.LanguageCode(v); c != "" {
			return c
		}
	}
	return ""
}

// isExternalStream reports a sidecar subtitle: an explicit external flag, or a stream key
// (/library/streams/{id}) without a container index.
func isExternalStream(s *streamDTO) bool {
	if s.External != nil {
		return bool(*s.External)
	}
	return s.Key.String() != "" && s.Index == nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
