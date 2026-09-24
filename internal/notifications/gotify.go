package notifications

import (
	"context"
	"errors"
	"net/url"
)

type gotifyPayload struct {
	Title    string         `json:"title"`
	Message  string         `json:"message"`
	Priority int64          `json:"priority"`
	Extras   map[string]any `json:"extras"`
}

// sendGotify posts to {serverUrl}/message, authenticating with the X-Gotify-Key header (never a
// query parameter, which would end up in proxy logs).
func (s *Service) sendGotify(ctx context.Context, set *settings, m Message) error {
	base, err := url.Parse(set.str("serverUrl"))
	if err != nil {
		return errors.New("invalid server URL")
	}
	endpoint := base.JoinPath("message").String()
	priority, ok := set.num("priority")
	if !ok {
		priority = 5
	}
	extras := map[string]any{
		"client::display": map[string]any{"contentType": "text/plain"},
	}
	if u := m.absURL(); u != "" {
		extras["client::notification"] = map[string]any{"click": map[string]any{"url": u}}
	}
	req, err := jsonRequest(endpoint, gotifyPayload{
		Title:    truncate(m.Title, 256),
		Message:  truncate(plainText(m), 8000),
		Priority: priority,
		Extras:   extras,
	})
	if err != nil {
		return err
	}
	req.header.Set("X-Gotify-Key", set.str("appToken"))
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	if !res.ok2xx() {
		return res.statusError(res.jsonField("errorDescription", "error"))
	}
	return nil
}
