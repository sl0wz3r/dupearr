package plex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxWebhookBody    = 16 << 20 // whole request (the multipart form may carry a JPEG thumb)
	maxWebhookPayload = 1 << 20  // the JSON payload itself
)

// WebhookPayload is a Plex webhook payload (Plex Pass webhooks, multipart field "payload").
// Note: Plex sends Metadata.librarySectionID as a JSON number; it is decoded through an internal
// DTO and exposed as a string.
type WebhookPayload struct {
	Event    string
	Server   struct{ UUID, Title string }
	Metadata struct{ RatingKey, Type, Title, GrandparentTitle, LibrarySectionID string }

	// Additive fields.
	User, Owner          bool   // payload "user" / "owner" flags
	LibrarySectionType   string // Metadata.librarySectionType ("movie", "show", "artist", …)
	GrandparentRatingKey string // Metadata.grandparentRatingKey (episodes: the show)
	GUID                 string // Metadata.guid
	// ExternalIDs holds "tmdb"/"imdb"/"tvdb" from Metadata.Guid[] (or a legacy agent guid) and
	// "plex" (the plex:// GUID), like ItemRef.ExternalIDs. Never nil.
	ExternalIDs map[string]string
}

type webhookDTO struct {
	Event  flexString `json:"event"`
	User   flexBool   `json:"user"`
	Owner  flexBool   `json:"owner"`
	Server struct {
		UUID  flexString `json:"uuid"`
		Title flexString `json:"title"`
	} `json:"Server"`
	Metadata struct {
		RatingKey            flexString `json:"ratingKey"`
		Type                 flexString `json:"type"`
		Title                flexString `json:"title"`
		GrandparentTitle     flexString `json:"grandparentTitle"`
		GrandparentRatingKey flexString `json:"grandparentRatingKey"`
		LibrarySectionID     flexString `json:"librarySectionID"`
		LibrarySectionType   flexString `json:"librarySectionType"`
		GUID                 flexString `json:"guid"`
		// Guids must be declared: encoding/json matches keys case-insensitively, so without it
		// the "Guid" element array would be folded onto (and blank) the "guid" string above.
		Guids list[guidDTO] `json:"Guid"`
	} `json:"Metadata"`
}

// ParseWebhook parses a Plex webhook request: multipart/form-data with the JSON in the form field
// "payload" (other parts, such as the "thumb" JPEG, are skipped without buffering), an
// application/x-www-form-urlencoded "payload" field, or a raw application/json body. The request
// body is capped at 16 MiB and the payload at 1 MiB. The body is consumed.
func ParseWebhook(r *http.Request) (*WebhookPayload, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("plex: webhook: empty request")
	}
	body := http.MaxBytesReader(nil, r.Body, maxWebhookBody)
	defer body.Close()

	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var payload []byte
	var err error
	switch {
	case mt == "multipart/form-data":
		payload, err = multipartPayload(body, params["boundary"])
	case mt == "application/x-www-form-urlencoded":
		var raw []byte
		if raw, err = readLimited(body, maxWebhookPayload*4); err == nil {
			var vals url.Values
			if vals, err = url.ParseQuery(string(raw)); err == nil {
				payload = []byte(vals.Get("payload"))
			}
		}
	case mt == "" || mt == "application/json" || strings.HasSuffix(mt, "+json") || mt == "text/plain":
		payload, err = readLimited(body, maxWebhookPayload)
	default:
		return nil, fmt.Errorf("plex: webhook: unsupported content type %q", mt)
	}
	if err != nil {
		return nil, fmt.Errorf("plex: webhook: read body: %w", err)
	}
	p := trimBody(payload)
	if len(p) == 0 {
		return nil, errors.New(`plex: webhook: missing "payload"`)
	}
	var d webhookDTO
	if err := json.Unmarshal(p, &d); err != nil {
		return nil, fmt.Errorf("plex: webhook: invalid payload JSON: %w", err)
	}
	if d.Event.String() == "" {
		return nil, errors.New("plex: webhook: payload has no event")
	}
	out := &WebhookPayload{
		Event:                d.Event.String(),
		User:                 bool(d.User),
		Owner:                bool(d.Owner),
		LibrarySectionType:   d.Metadata.LibrarySectionType.String(),
		GrandparentRatingKey: d.Metadata.GrandparentRatingKey.String(),
		GUID:                 d.Metadata.GUID.String(),
		ExternalIDs:          externalIDs(d.Metadata.GUID.String(), d.Metadata.Guids),
	}
	out.Server.UUID = d.Server.UUID.String()
	out.Server.Title = d.Server.Title.String()
	out.Metadata.RatingKey = d.Metadata.RatingKey.String()
	out.Metadata.Type = d.Metadata.Type.String()
	out.Metadata.Title = d.Metadata.Title.String()
	out.Metadata.GrandparentTitle = d.Metadata.GrandparentTitle.String()
	out.Metadata.LibrarySectionID = d.Metadata.LibrarySectionID.String()
	return out, nil
}

// multipartPayload streams the parts and returns the first "payload" field.
func multipartPayload(body io.Reader, boundary string) ([]byte, error) {
	if boundary == "" {
		return nil, errors.New("multipart request without boundary")
	}
	mr := multipart.NewReader(body, boundary)
	var payload []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if payload == nil && part.FormName() == "payload" {
			b, rerr := readLimited(part, maxWebhookPayload)
			_ = part.Close()
			if rerr != nil {
				return nil, rerr
			}
			payload = b
			if payload == nil {
				payload = []byte{}
			}
			continue
		}
		_, _ = io.Copy(io.Discard, part) // e.g. the "thumb" JPEG
		_ = part.Close()
	}
	return payload, nil
}
