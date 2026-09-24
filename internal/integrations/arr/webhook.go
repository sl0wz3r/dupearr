package arr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// maxWebhookBytes caps a webhook body. Real payloads are a few KB; Sonarr's batch
// "import complete" variant grows with the episode count.
const maxWebhookBytes = 4 << 20

// WebhookPayload is a Radarr/Sonarr webhook (Settings → Connect → Webhook). Field names match the
// *arr JSON case-insensitively, so encoding/json decodes it without tags.
type WebhookPayload struct {
	EventType    string
	InstanceName string
	IsUpgrade    bool
	Movie        *struct {
		ID         int64
		Title      string
		Year       int
		TmdbID     int
		ImdbID     string
		FolderPath string
	}
	Series *struct {
		ID     int64
		Title  string
		TvdbID int
		ImdbID string
		Path   string
	}
	Episodes []struct {
		ID                          int64
		SeasonNumber, EpisodeNumber int
	}
}

// Aliases of the anonymous WebhookPayload field types (identical types), used to build them.
type (
	webhookMovie = struct {
		ID         int64
		Title      string
		Year       int
		TmdbID     int
		ImdbID     string
		FolderPath string
	}
	webhookSeries = struct {
		ID     int64
		Title  string
		TvdbID int
		ImdbID string
		Path   string
	}
	webhookEpisode = struct {
		ID                          int64
		SeasonNumber, EpisodeNumber int
	}
)

// Kind reports which *arr sent the payload: Radarr payloads carry "movie", Sonarr payloads
// "series". It is "" when neither is present.
func (p *WebhookPayload) Kind() models.ArrKind {
	switch {
	case p == nil:
		return ""
	case p.Movie != nil:
		return models.ArrRadarr
	case p.Series != nil:
		return models.ArrSonarr
	}
	return ""
}

// ParseWebhook decodes a webhook body. Decoding is tolerant: unknown fields are ignored, key case
// does not matter, numbers sent as strings (and vice versa) are accepted, and a movie/series/
// episode of an unexpected shape is skipped. Malformed JSON is rejected.
// The body must be a JSON object with a non-empty eventType and at most 4 MiB.
func ParseWebhook(body io.Reader) (*WebhookPayload, error) {
	if body == nil {
		return nil, errors.New("arr: webhook body is empty")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxWebhookBytes+1))
	if err != nil {
		return nil, fmt.Errorf("arr: reading webhook body: %w", err)
	}
	if len(data) > maxWebhookBytes {
		return nil, fmt.Errorf("arr: webhook body exceeds %d bytes", maxWebhookBytes)
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(data) == 0 {
		return nil, errors.New("arr: webhook body is empty")
	}
	if data[0] != '{' {
		return nil, errors.New("arr: webhook body is not a JSON object")
	}
	var w wireWebhook
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("arr: decoding webhook payload: %w", err)
	}
	p := &WebhookPayload{
		EventType:    strings.TrimSpace(string(w.EventType)),
		InstanceName: strings.TrimSpace(string(w.InstanceName)),
		IsUpgrade:    bool(w.IsUpgrade),
	}
	if p.EventType == "" {
		return nil, errors.New("arr: webhook payload has no eventType")
	}
	var m wireMovie
	if decodeObject(w.Movie, &m) {
		p.Movie = &webhookMovie{
			ID:         int64(m.ID),
			Title:      strings.TrimSpace(string(m.Title)),
			Year:       int(m.Year),
			TmdbID:     int(m.TmdbID),
			ImdbID:     strings.TrimSpace(string(m.ImdbID)),
			FolderPath: strings.TrimSpace(string(m.FolderPath)),
		}
	}
	var s wireSeries
	if decodeObject(w.Series, &s) {
		p.Series = &webhookSeries{
			ID:     int64(s.ID),
			Title:  strings.TrimSpace(string(s.Title)),
			TvdbID: int(s.TvdbID),
			ImdbID: strings.TrimSpace(string(s.ImdbID)),
			Path:   strings.TrimSpace(string(s.Path)),
		}
	}
	var episodes []json.RawMessage
	if raw := bytes.TrimSpace(w.Episodes); len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &episodes) == nil {
		for _, raw := range episodes {
			var e wireEpisode
			if decodeObject(raw, &e) {
				p.Episodes = append(p.Episodes, webhookEpisode{
					ID:            int64(e.ID),
					SeasonNumber:  int(e.SeasonNumber),
					EpisodeNumber: int(e.EpisodeNumber),
				})
			}
		}
	}
	return p, nil
}

// ToTargetedScan converts a webhook into a TargetedScan body; ok=false for irrelevant events (Test, Grab…).
//
// Relevant events: Download (import or upgrade, including Sonarr's batch import-complete variant),
// Rename, MovieFileDelete, EpisodeFileDelete, MovieDelete and SeriesDelete. Everything else (Test,
// Grab, MovieAdded/SeriesAdd, Health, HealthRestored, ApplicationUpdate,
// ManualInteractionRequired, unknown events) is ignored, as is a relevant event that carries no
// usable TMDb/TVDB/IMDb id (a scan without ids must never widen to the whole library).
func (p *WebhookPayload) ToTargetedScan() (models.TargetedScanBody, bool) {
	if p == nil {
		return models.TargetedScanBody{}, false
	}
	switch strings.ToLower(strings.TrimSpace(p.EventType)) {
	case "download", "importcomplete", "rename",
		"moviefiledelete", "episodefiledelete", "moviedelete", "seriesdelete":
	default:
		return models.TargetedScanBody{}, false
	}
	var b models.TargetedScanBody
	if m := p.Movie; m != nil {
		if m.TmdbID > 0 {
			b.TmdbID = m.TmdbID
		}
		b.ImdbID = normalizeIMDb(m.ImdbID)
	}
	if s := p.Series; s != nil {
		if s.TvdbID > 0 {
			b.TvdbID = s.TvdbID
		}
		if b.ImdbID == "" {
			b.ImdbID = normalizeIMDb(s.ImdbID)
		}
	}
	if b.TmdbID == 0 && b.TvdbID == 0 && b.ImdbID == "" {
		return models.TargetedScanBody{}, false
	}
	return b, true
}

// normalizeIMDb returns a lower-cased "tt<digits>" IMDb id, or "" when s is not one.
func normalizeIMDb(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 3 || !strings.HasPrefix(s, "tt") {
		return ""
	}
	for _, r := range s[2:] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Tolerant wire types
// ---------------------------------------------------------------------------

// wireWebhook is the envelope. Nested objects stay raw so that a field of an unexpected shape
// (e.g. "movie": "oops") is skipped instead of failing the whole payload or leaving a half-filled
// object behind.
type wireWebhook struct {
	EventType    flexString      `json:"eventType"`
	InstanceName flexString      `json:"instanceName"`
	IsUpgrade    flexBool        `json:"isUpgrade"`
	Movie        json.RawMessage `json:"movie"`
	Series       json.RawMessage `json:"series"`
	Episodes     json.RawMessage `json:"episodes"`
}

// decodeObject decodes raw into v when raw is a JSON object; it reports whether it did.
// The wire structs only hold flex scalars, so decoding an object cannot fail on types.
func decodeObject(raw json.RawMessage, v any) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, v) == nil
}

type wireMovie struct {
	ID         flexInt    `json:"id"`
	Title      flexString `json:"title"`
	Year       flexInt    `json:"year"`
	TmdbID     flexInt    `json:"tmdbId"`
	ImdbID     flexString `json:"imdbId"`
	FolderPath flexString `json:"folderPath"`
}

type wireSeries struct {
	ID     flexInt    `json:"id"`
	Title  flexString `json:"title"`
	TvdbID flexInt    `json:"tvdbId"`
	ImdbID flexString `json:"imdbId"`
	Path   flexString `json:"path"`
}

type wireEpisode struct {
	ID            flexInt `json:"id"`
	SeasonNumber  flexInt `json:"seasonNumber"`
	EpisodeNumber flexInt `json:"episodeNumber"`
}

// flexInt accepts a JSON number or a numeric string; anything else (null, garbage, fractions,
// values beyond int32 range) decodes to 0.
type flexInt int64

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	*f = 0
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return nil
		}
		s = strings.TrimSpace(s)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			*f = flexInt(n)
		}
		return nil
	}
	if x, err := strconv.ParseFloat(s, 64); err == nil && x == math.Trunc(x) && x >= math.MinInt32 && x <= math.MaxInt32 {
		*f = flexInt(int64(x))
	}
	return nil
}

// flexString accepts a JSON string, number or boolean (as text); null, objects and arrays decode
// to "".
type flexString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexString) UnmarshalJSON(b []byte) error {
	*f = ""
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if json.Unmarshal(b, &s) == nil {
			*f = flexString(s)
		}
	case '{', '[', 'n':
		// object, array or null
	default:
		*f = flexString(b)
	}
	return nil
}

// flexBool accepts true/false, "true"/"false" (any case) and 1/0; anything else is false.
type flexBool bool

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(b)), `"`))
	*f = flexBool(s == "true" || s == "1")
	return nil
}
