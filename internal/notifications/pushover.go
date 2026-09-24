package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultPushoverAPI is the Pushover message endpoint.
const DefaultPushoverAPI = "https://api.pushover.net/1/messages.json"

// Pushover limits (https://pushover.net/api#limits).
const (
	pushoverMaxMessage = 1024
	pushoverMaxTitle   = 250
	pushoverMaxURL     = 512
)

type pushoverResponse struct {
	Status int      `json:"status"`
	Errors []string `json:"errors"`
}

// pushoverHTML renders body and fields with Pushover's HTML subset (html=1), within 1024 chars.
func pushoverHTML(m Message) string {
	var b strings.Builder
	budget := pushoverMaxMessage - 40
	if m.Body != "" {
		body := html.EscapeString(truncate(m.Body, 600))
		budget -= utf8.RuneCountInString(body)
		b.WriteString(body)
	}
	for i, f := range m.Fields {
		var line string
		switch {
		case f.Name == "":
			line = html.EscapeString(truncate(f.Value, 200))
		default:
			line = "<b>" + html.EscapeString(truncate(f.Name, 64)) + ":</b> " + html.EscapeString(truncate(f.Value, 200))
		}
		cost := utf8.RuneCountInString(line) + 2
		if cost > budget {
			b.WriteString("\n<i>" + formatOmitted(len(m.Fields)-i) + "</i>")
			break
		}
		budget -= cost
		if b.Len() > 0 {
			if i == 0 {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return html.EscapeString(m.Title)
	}
	return b.String()
}

// sendPushover posts a message to the Pushover API (form-encoded).
func (s *Service) sendPushover(ctx context.Context, set *settings, m Message) error {
	form := url.Values{}
	form.Set("token", set.str("appToken"))
	form.Set("user", set.str("userKey"))
	form.Set("title", truncate(m.Title, pushoverMaxTitle))
	form.Set("message", pushoverHTML(m))
	form.Set("html", "1")
	form.Set("timestamp", strconv.FormatInt(s.now().Unix(), 10))
	if u := m.absURL(); u != "" && len(u) <= pushoverMaxURL {
		form.Set("url", u)
		form.Set("url_title", linkLabel)
	}
	if d := set.list("devices"); len(d) > 0 {
		form.Set("device", strings.Join(d, ","))
	}
	if snd := set.str("sound"); snd != "" {
		form.Set("sound", snd)
	}
	priority, _ := set.num("priority")
	form.Set("priority", strconv.FormatInt(priority, 10))
	if priority == 2 {
		retry, ok := set.num("retry")
		if !ok || retry < 30 {
			retry = 60
		}
		expire, ok := set.num("expire")
		if !ok || expire < 1 || expire > 10800 {
			expire = 3600
		}
		form.Set("retry", strconv.FormatInt(retry, 10))
		form.Set("expire", strconv.FormatInt(expire, 10))
	}

	res, err := s.do(ctx, httpRequest{
		method:        http.MethodPost,
		url:           s.pushoverAPI,
		body:          []byte(form.Encode()),
		contentType:   "application/x-www-form-urlencoded",
		fixedEndpoint: true,
	})
	if err != nil {
		return err
	}
	var pr pushoverResponse
	_ = json.Unmarshal(res.body, &pr)
	detail := strings.Join(pr.Errors, "; ")
	if !res.ok2xx() {
		return res.statusError(detail)
	}
	if pr.Status != 1 {
		return fmt.Errorf("pushover API error: %s", firstNonEmpty(cleanLine(detail), "request not accepted"))
	}
	return nil
}
