package plex

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Wire DTOs for PMS JSON responses (Accept: application/json). Every scalar is a flex type and
// every element array a list[T]: encoding/json also matches keys case-insensitively, so an
// attribute such as "directory": true could otherwise be folded onto the Directory element
// array and fail the whole decode.

// list is a lenient JSON element array: a JSON array decodes normally, a single object becomes
// a one-element list, and any other value (null, bool, string, number) is ignored.
type list[T any] []T

func (l *list[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	switch b[0] {
	case '[':
		var items []T
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		*l = items
	case '{':
		var one T
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*l = list[T]{one}
	}
	return nil
}

// containerResponse is the top-level {"MediaContainer": {...}} envelope. MediaContainer is nil
// when the body has no such object (a legacy "_children" PMS, a proxy's JSON error page …): such
// a response must never be read as an empty listing or "nothing is playing".
type containerResponse struct {
	MediaContainer *containerDTO `json:"MediaContainer"`
}

type containerDTO struct {
	Size      flexInt  `json:"size"`
	TotalSize *flexInt `json:"totalSize"`
	Offset    flexInt  `json:"offset"`

	// GET / and GET /identity
	MachineIdentifier  flexString `json:"machineIdentifier"`
	Version            flexString `json:"version"`
	FriendlyName       flexString `json:"friendlyName"`
	AllowMediaDeletion *flexBool  `json:"allowMediaDeletion"`

	LibrarySectionID    flexString `json:"librarySectionID"`
	LibrarySectionTitle flexString `json:"librarySectionTitle"`

	Directory list[directoryDTO] `json:"Directory"`
	Metadata  list[metadataDTO]  `json:"Metadata"`
}

type directoryDTO struct {
	Key              flexString        `json:"key"`
	Type             flexString        `json:"type"`
	Title            flexString        `json:"title"`
	UUID             flexString        `json:"uuid"`
	Location         list[locationDTO] `json:"Location"`
	Refreshing       flexBool          `json:"refreshing"`
	ScannedAt        flexInt           `json:"scannedAt"`
	ContentChangedAt flexInt           `json:"contentChangedAt"`
}

type locationDTO struct {
	ID   flexInt    `json:"id"`
	Path flexString `json:"path"`
}

// guidDTO is one Guid[] element: {"id": "imdb://tt0088763"} (a bare string is accepted too).
type guidDTO struct {
	ID string
}

func (g *guidDTO) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var v struct {
			ID flexString `json:"id"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		g.ID = v.ID.String()
		return nil
	}
	g.ID = scalarText(b)
	return nil
}

type metadataDTO struct {
	RatingKey            flexString     `json:"ratingKey"`
	Key                  flexString     `json:"key"`
	Type                 flexString     `json:"type"`
	GUID                 flexString     `json:"guid"`
	Guids                list[guidDTO]  `json:"Guid"`
	Title                flexString     `json:"title"`
	Year                 flexInt        `json:"year"`
	EditionTitle         flexString     `json:"editionTitle"`
	OriginalTitle        flexString     `json:"originalTitle"`
	Index                *flexInt       `json:"index"`       // episode number; nil = not reported
	ParentIndex          *flexInt       `json:"parentIndex"` // season number; nil = not reported
	ParentRatingKey      flexString     `json:"parentRatingKey"`
	GrandparentRatingKey flexString     `json:"grandparentRatingKey"`
	GrandparentTitle     flexString     `json:"grandparentTitle"`
	GrandparentGUID      flexString     `json:"grandparentGuid"`
	LibrarySectionID     flexString     `json:"librarySectionID"`
	LibrarySectionTitle  flexString     `json:"librarySectionTitle"`
	AddedAt              flexInt        `json:"addedAt"`
	Duration             flexInt        `json:"duration"`
	Thumb                flexString     `json:"thumb"`
	ParentThumb          flexString     `json:"parentThumb"`
	GrandparentThumb     flexString     `json:"grandparentThumb"`
	Media                list[mediaDTO] `json:"Media"`
}

type mediaDTO struct {
	ID              flexInt       `json:"id"`
	Duration        flexInt       `json:"duration"`
	Bitrate         flexInt       `json:"bitrate"`
	Width           flexInt       `json:"width"`
	Height          flexInt       `json:"height"`
	AspectRatio     flexFloat     `json:"aspectRatio"`
	AudioChannels   flexInt       `json:"audioChannels"`
	AudioCodec      flexString    `json:"audioCodec"`
	AudioProfile    flexString    `json:"audioProfile"`
	VideoCodec      flexString    `json:"videoCodec"`
	VideoProfile    flexString    `json:"videoProfile"`
	VideoResolution flexString    `json:"videoResolution"`
	VideoFrameRate  flexString    `json:"videoFrameRate"`
	Container       flexString    `json:"container"`
	ProxyType       flexInt       `json:"proxyType"`
	Target          flexString    `json:"target"`
	Title           flexString    `json:"title"`
	Parts           list[partDTO] `json:"Part"`
}

type partDTO struct {
	ID         flexInt         `json:"id"`
	Key        flexString      `json:"key"`
	File       flexString      `json:"file"`
	Size       flexInt         `json:"size"`
	Duration   flexInt         `json:"duration"`
	Container  flexString      `json:"container"`
	Accessible *flexBool       `json:"accessible"` // only with checkFiles=1; nil = unknown
	Exists     *flexBool       `json:"exists"`     // only with checkFiles=1; nil = unknown
	Streams    list[streamDTO] `json:"Stream"`
}

// Stream types (Stream.streamType).
const (
	streamVideo    = 1
	streamAudio    = 2
	streamSubtitle = 3
)

type streamDTO struct {
	ID                   flexInt    `json:"id"`
	StreamType           flexInt    `json:"streamType"`
	Index                *flexInt   `json:"index"`
	Codec                flexString `json:"codec"`
	Profile              flexString `json:"profile"`
	Bitrate              flexInt    `json:"bitrate"`
	BitDepth             flexInt    `json:"bitDepth"`
	Width                flexInt    `json:"width"`
	Height               flexInt    `json:"height"`
	FrameRate            flexFloat  `json:"frameRate"`
	ColorPrimaries       flexString `json:"colorPrimaries"`
	ColorTrc             flexString `json:"colorTrc"`
	ColorSpace           flexString `json:"colorSpace"`
	DOVIPresent          *flexBool  `json:"DOVIPresent"`
	DOVIProfile          *flexInt   `json:"DOVIProfile"`
	DOVIBLCompatID       *flexInt   `json:"DOVIBLCompatID"`
	DisplayTitle         flexString `json:"displayTitle"`
	ExtendedDisplayTitle flexString `json:"extendedDisplayTitle"`
	Title                flexString `json:"title"`
	Language             flexString `json:"language"`
	LanguageCode         flexString `json:"languageCode"`
	LanguageTag          flexString `json:"languageTag"`
	Channels             flexInt    `json:"channels"`
	Default              *flexBool  `json:"default"`
	Selected             *flexBool  `json:"selected"`
	Forced               *flexBool  `json:"forced"`
	External             *flexBool  `json:"external"`
	Key                  flexString `json:"key"`
	Format               flexString `json:"format"`

	// HDR10Plus is set when any attribute whose name mentions HDR10+ ("HDR10PlusPresent", …) is
	// truthy. PMS 1.43.4 added HDR10+ detection but the attribute name is unverified
	// (docs/research/plex-api.md §8.3), so it is matched by name.
	HDR10Plus bool `json:"-"`
}

func (s *streamDTO) UnmarshalJSON(b []byte) error {
	type plain streamDTO // no methods: avoids recursion
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = streamDTO(p)
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) == nil {
		for k, v := range raw {
			lk := strings.ToLower(k)
			if (strings.Contains(lk, "hdr10plus") || strings.Contains(lk, "hdr10+")) && parseFlexBool(v) {
				s.HDR10Plus = true
			}
		}
	}
	return nil
}

func (s *streamDTO) isDefault() bool { return s.Default != nil && bool(*s.Default) }
