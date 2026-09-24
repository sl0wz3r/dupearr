package notifications

import (
	"context"
	"strings"
)

// Slack Block Kit limits (https://api.slack.com/reference/block-kit/blocks).
const (
	slackMaxHeader      = 150
	slackMaxSectionText = 3000
	slackMaxFieldText   = 2000
	slackFieldsPerBlock = 10
	slackMaxFieldBlocks = 4
	slackMaxFallback    = 3000
)

type slackPayload struct {
	Text   string       `json:"text"` // fallback for notifications and clients without blocks
	Blocks []slackBlock `json:"blocks"`
}

type slackText struct {
	Type string `json:"type"` // plain_text | mrkdwn
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"` // header | section | context
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

// slackEscape escapes the three characters Slack's mrkdwn reserves.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// slackLinkURL makes a URL safe inside <url|label>.
func slackLinkURL(u string) string {
	return strings.NewReplacer("|", "%7C", "<", "%3C", ">", "%3E", " ", "%20").Replace(u)
}

func slackSeverityLabel(severity string) string {
	switch severity {
	case SeverityWarning:
		return ":warning: Warning"
	case SeverityError:
		return ":rotating_light: Error"
	default:
		return ":information_source: Info"
	}
}

// buildSlackPayload renders m as Block Kit blocks with a plain-text fallback.
func buildSlackPayload(m Message, instance string) slackPayload {
	blocks := []slackBlock{{
		Type: "header",
		Text: &slackText{Type: "plain_text", Text: truncate(m.Title, slackMaxHeader)},
	}}
	if m.Body != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: truncate(slackEscape(truncate(m.Body, slackMaxSectionText-300)), slackMaxSectionText)},
		})
	}
	var fields []slackText
	for _, f := range m.Fields {
		name, value := slackEscape(truncate(f.Name, 200)), slackEscape(truncate(f.Value, slackMaxFieldText-500))
		var t string
		switch {
		case name == "":
			t = value
		case value == "":
			t = "*" + name + "*"
		default:
			t = "*" + name + "*\n" + value
		}
		fields = append(fields, slackText{Type: "mrkdwn", Text: truncate(t, slackMaxFieldText)})
	}
	for i := 0; i < len(fields); i += slackFieldsPerBlock {
		if i/slackFieldsPerBlock == slackMaxFieldBlocks {
			blocks = append(blocks, slackBlock{
				Type:     "context",
				Elements: []slackText{{Type: "mrkdwn", Text: formatOmitted(len(fields) - i)}},
			})
			break
		}
		blocks = append(blocks, slackBlock{Type: "section", Fields: fields[i:min(i+slackFieldsPerBlock, len(fields))]})
	}
	if u := m.absURL(); u != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: "<" + slackLinkURL(u) + "|" + linkLabel + ">"},
		})
	}
	blocks = append(blocks, slackBlock{
		Type:     "context",
		Elements: []slackText{{Type: "mrkdwn", Text: slackSeverityLabel(m.Severity) + " • " + slackEscape(instance)}},
	})

	fallback := m.Title
	if m.Body != "" {
		fallback += ": " + m.Body
	}
	return slackPayload{Text: truncate(slackEscape(fallback), slackMaxFallback), Blocks: blocks}
}

// sendSlack posts to a Slack incoming webhook (200 "ok" on success).
func (s *Service) sendSlack(ctx context.Context, set *settings, m Message) error {
	req, err := jsonRequest(set.str("webhookUrl"), buildSlackPayload(m, s.instance()))
	if err != nil {
		return err
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	if !res.ok2xx() {
		return res.statusError("")
	}
	return nil
}
