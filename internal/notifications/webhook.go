package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookPayload is the JSON body of the generic webhook provider.
type WebhookPayload struct {
	EventType    string         `json:"eventType"` // PascalCase, *arr convention: "DuplicatesFound", "Test", …
	Title        string         `json:"title"`
	Message      string         `json:"message"`
	Severity     string         `json:"severity"` // info | warning | error
	Fields       []WebhookField `json:"fields"`
	URL          string         `json:"url"`
	InstanceName string         `json:"instanceName,omitempty"`
	Timestamp    string         `json:"timestamp"` // RFC3339 UTC
}

// WebhookField is one {name, value} entry of WebhookPayload.Fields.
type WebhookField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// buildWebhookPayload renders m for the generic webhook.
func buildWebhookPayload(m Message, instance string, now time.Time) WebhookPayload {
	fields := make([]WebhookField, 0, len(m.Fields))
	for _, f := range m.Fields {
		fields = append(fields, WebhookField{Name: f.Name, Value: f.Value})
	}
	return WebhookPayload{
		EventType:    eventType(m.Event),
		Title:        m.Title,
		Message:      m.Body,
		Severity:     m.Severity,
		Fields:       fields,
		URL:          m.URL,
		InstanceName: instance,
		Timestamp:    now.UTC().Format(time.RFC3339),
	}
}

// sendWebhook sends the JSON payload with the configured method, basic auth and custom headers.
func (s *Service) sendWebhook(ctx context.Context, set *settings, m Message) error {
	method := set.str("method")
	if method != http.MethodPut {
		method = http.MethodPost
	}
	hdr, err := parseHeaders(set.str("headers"))
	if err != nil {
		return fmt.Errorf("invalid headers: %w", err)
	}
	body, err := json.Marshal(buildWebhookPayload(m, s.instance(), s.now()))
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	if user := set.str("username"); user != "" {
		hdr.Set("Authorization", basicAuth(user, set.str("password")))
	}
	res, err := s.do(ctx, httpRequest{
		method:      method,
		url:         set.str("url"),
		body:        body,
		contentType: "application/json",
		header:      hdr,
	})
	if err != nil {
		return err
	}
	if !res.ok2xx() {
		return res.statusError("")
	}
	return nil
}
