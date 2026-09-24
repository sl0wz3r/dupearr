package notifications

import (
	"context"
	"errors"
	"net/url"
)

// ntfyMaxMessageBytes stays under ntfy's 4096-byte limit (larger bodies become attachments).
const ntfyMaxMessageBytes = 3800

type ntfyPayload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int64    `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// ntfySeverityTag maps severities onto ntfy emoji tags.
func ntfySeverityTag(severity string) string {
	switch severity {
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "rotating_light"
	default:
		return ""
	}
}

// sendNtfy publishes via ntfy's JSON API: POST to the server root with the topic in the body.
func (s *Service) sendNtfy(ctx context.Context, set *settings, m Message) error {
	base := set.str("serverUrl")
	if base == "" {
		base = DefaultNtfyServer
	}
	payload := ntfyPayload{
		Topic:   set.str("topic"),
		Title:   truncate(m.Title, 256),
		Message: truncateBytes(plainText(m), ntfyMaxMessageBytes),
		Click:   m.absURL(),
	}
	if p, ok := set.num("priority"); ok && p >= 1 && p <= 5 {
		payload.Priority = p
	}
	if t := ntfySeverityTag(m.Severity); t != "" {
		payload.Tags = append(payload.Tags, t)
	}
	payload.Tags = append(payload.Tags, set.list("tags")...)

	u, err := url.Parse(base)
	if err != nil {
		return errors.New("invalid server URL")
	}
	req, err := jsonRequest(withTrailingSlash(u).String(), payload)
	if err != nil {
		return err
	}
	if token := set.str("accessToken"); token != "" {
		req.header.Set("Authorization", "Bearer "+token)
	} else if user := set.str("username"); user != "" {
		req.header.Set("Authorization", basicAuth(user, set.str("password")))
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	if !res.ok2xx() {
		return res.statusError(res.jsonField("error"))
	}
	return nil
}
