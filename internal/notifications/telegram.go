package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// DefaultTelegramAPI is the Telegram Bot API base URL.
const DefaultTelegramAPI = "https://api.telegram.org"

// telegramMaxText is Telegram's 4096-character message limit (after entity parsing), minus a
// margin for our markup.
const telegramMaxText = 3900

type telegramRequest struct {
	ChatID              string                     `json:"chat_id"`
	Text                string                     `json:"text"`
	ParseMode           string                     `json:"parse_mode"`
	MessageThreadID     int64                      `json:"message_thread_id,omitempty"`
	DisableNotification bool                       `json:"disable_notification,omitempty"`
	LinkPreviewOptions  telegramLinkPreviewOptions `json:"link_preview_options"`
}

type telegramLinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// telegramHTML renders m for parse_mode=HTML; every user-supplied string is escaped.
func telegramHTML(m Message) string {
	var b strings.Builder
	title := truncate(m.Title, 256)
	b.WriteString("<b>")
	b.WriteString(html.EscapeString(severityEmoji(m.Severity) + title))
	b.WriteString("</b>")
	budget := telegramMaxText - utf8.RuneCountInString(title) - 20
	if m.Body != "" {
		body := truncate(m.Body, min(budget, 3000))
		budget -= utf8.RuneCountInString(body)
		b.WriteString("\n")
		b.WriteString(html.EscapeString(body))
	}
	for i, f := range m.Fields {
		name, value := truncate(f.Name, 128), truncate(f.Value, 512)
		cost := utf8.RuneCountInString(name) + utf8.RuneCountInString(value) + 4
		if cost > budget-100 {
			b.WriteString("\n<i>")
			b.WriteString(formatOmitted(len(m.Fields) - i))
			b.WriteString("</i>")
			break
		}
		budget -= cost
		if i == 0 {
			b.WriteString("\n")
		}
		b.WriteString("\n")
		if name != "" {
			b.WriteString("<b>")
			b.WriteString(html.EscapeString(name))
			b.WriteString(":</b> ")
		}
		b.WriteString(html.EscapeString(value))
	}
	if u := m.absURL(); u != "" && utf8.RuneCountInString(u) < budget-40 {
		b.WriteString("\n\n<a href=\"")
		b.WriteString(html.EscapeString(u))
		b.WriteString("\">")
		b.WriteString(linkLabel)
		b.WriteString("</a>")
	}
	return b.String()
}

// sendTelegram calls sendMessage of the Bot API.
func (s *Service) sendTelegram(ctx context.Context, set *settings, m Message) error {
	token := set.str("botToken")
	if !reTelegramToken.MatchString(token) {
		// Never interpolate an unchecked token into the request path.
		return errors.New("invalid bot token")
	}
	payload := telegramRequest{
		ChatID:              set.str("chatId"),
		Text:                telegramHTML(m),
		ParseMode:           "HTML",
		DisableNotification: set.boolean("sendSilently"),
		LinkPreviewOptions:  telegramLinkPreviewOptions{IsDisabled: true},
	}
	if id, ok := set.num("topicId"); ok && id > 0 {
		payload.MessageThreadID = id
	}
	req, err := jsonRequest(strings.TrimRight(s.telegramAPI, "/")+"/bot"+token+"/sendMessage", payload)
	if err != nil {
		return err
	}
	req.fixedEndpoint = true
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	var tr telegramResponse
	desc := res.jsonField("description")
	if res.ok2xx() {
		// The Bot API always answers JSON with "ok"; anything else (e.g. a proxy's HTML page)
		// is not a confirmed delivery.
		if err := json.Unmarshal(res.body, &tr); err != nil {
			return errors.New("telegram API error: unexpected non-JSON response")
		}
		if !tr.OK {
			return fmt.Errorf("telegram API error: %s", firstNonEmpty(cleanLine(desc), "request not accepted"))
		}
		return nil
	}
	return res.statusError(desc)
}
