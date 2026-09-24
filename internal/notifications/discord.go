package notifications

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Discord embed colours by severity.
const (
	discordColorInfo    = 0x3498DB
	discordColorWarning = 0xF39C12
	discordColorError   = 0xE74C3C
)

// Discord limits (https://discord.com/developers/docs/resources/message#embed-object-embed-limits).
const (
	discordMaxTitle      = 256
	discordDescCap       = 3000 // of 4096, leaving room for fields in the 6000 total
	discordMaxFields     = 25
	discordMaxFieldName  = 256
	discordMaxFieldValue = 1024
	discordMaxEmbedTotal = 6000
	discordMaxUsername   = 80
)

type discordPayload struct {
	Username        string                 `json:"username,omitempty"`
	AvatarURL       string                 `json:"avatar_url,omitempty"`
	Embeds          []discordEmbed         `json:"embeds"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

// discordAllowedMentions with an empty Parse list stops titles such as "@everyone" from pinging.
type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Footer      *discordFooter `json:"footer,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordFooter struct {
	Text string `json:"text"`
}

func discordColor(severity string) int {
	switch severity {
	case SeverityWarning:
		return discordColorWarning
	case SeverityError:
		return discordColorError
	default:
		return discordColorInfo
	}
}

// discordSpecial are the characters Discord's Markdown gives a meaning to (formatting, masked
// links [text](url), autolinks and <…> mentions/timestamps, headings, quotes, lists, spoilers).
const discordSpecial = "\\*_~`|>#[]()-+<:"

// discordEscape backslash-escapes text for an embed title, description or field so that
// upstream-controlled strings (Plex titles, release and file names, paths, error text) render
// literally: never as a masked phishing link, an autolink ("https\://…"), a mention, a heading or
// formatting. The embed URL stays the only link. Ordered-list markers ("1. ") are escaped too.
func discordEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	linePrefix, sawDigit := true, false // only blanks (and digits) so far on this line
	for _, r := range s {
		if r == '\n' {
			b.WriteRune(r)
			linePrefix, sawDigit = true, false
			continue
		}
		if linePrefix {
			switch {
			case r == ' ' || r == '\t':
				b.WriteRune(r)
				continue
			case r >= '0' && r <= '9':
				b.WriteRune(r)
				sawDigit = true
				continue
			case r == '.' && sawDigit:
				b.WriteString(`\.`)
				linePrefix = false
				continue
			}
			linePrefix = false
		}
		if strings.ContainsRune(discordSpecial, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// discordText escapes s (discordEscape) and truncates the result to at most max runes, never
// splitting an escape sequence, ending with an ellipsis when cut.
func discordText(s string, max int) string {
	esc := discordEscape(s)
	if utf8.RuneCountInString(esc) <= max {
		return esc
	}
	if max <= 1 {
		return truncate(esc, max)
	}
	rs := []rune(esc)
	n := 0
	for n < len(rs) {
		size := 1
		if rs[n] == '\\' && n+1 < len(rs) {
			size = 2 // an escape pair
		}
		if n+size > max-1 {
			break
		}
		n += size
	}
	return strings.TrimRightFunc(string(rs[:n]), unicode.IsSpace) + ellipsis
}

// buildDiscordPayload renders m as one embed within Discord's size limits. Title, description and
// fields are Markdown-escaped (discordText): they carry upstream-controlled text.
func buildDiscordPayload(set *settings, m Message, instance string, now time.Time) discordPayload {
	embed := discordEmbed{
		Title:       discordText(m.Title, discordMaxTitle),
		Description: discordText(m.Body, discordDescCap),
		URL:         m.absURL(),
		Color:       discordColor(m.Severity),
		Footer:      &discordFooter{Text: truncate(instance, 64)},
		Timestamp:   now.UTC().Format(time.RFC3339),
	}
	budget := discordMaxEmbedTotal - utf8.RuneCountInString(embed.Title) -
		utf8.RuneCountInString(embed.Description) - utf8.RuneCountInString(embed.Footer.Text)
	for i, f := range m.Fields {
		// Discord rejects empty names/values.
		name, value := discordText(f.Name, discordMaxFieldName), discordText(f.Value, discordMaxFieldValue)
		if name == "" {
			name = "\u200b" // zero-width space
		}
		if value == "" {
			value = "-"
		}
		df := discordField{Name: name, Value: value, Inline: f.Inline}
		cost := utf8.RuneCountInString(df.Name) + utf8.RuneCountInString(df.Value)
		remaining := len(m.Fields) - i
		if (len(embed.Fields) == discordMaxFields-1 && remaining > 1) || cost > budget-40 {
			embed.Fields = append(embed.Fields, discordField{
				Name: "…", Value: formatOmitted(remaining),
			})
			break
		}
		budget -= cost
		embed.Fields = append(embed.Fields, df)
	}
	return discordPayload{
		Username:        truncate(set.str("username"), discordMaxUsername),
		AvatarURL:       set.str("avatarUrl"),
		Embeds:          []discordEmbed{embed},
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
	}
}

// formatOmitted describes fields dropped to respect provider limits.
func formatOmitted(n int) string {
	if n == 1 {
		return "1 more field omitted"
	}
	return strconv.Itoa(n) + " more fields omitted"
}

// sendDiscord posts to a Discord webhook (204 No Content on success).
func (s *Service) sendDiscord(ctx context.Context, set *settings, m Message) error {
	req, err := jsonRequest(set.str("webhookUrl"), buildDiscordPayload(set, m, s.instance(), s.now()))
	if err != nil {
		return err
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	if !res.ok2xx() {
		return res.statusError(res.jsonField("message"))
	}
	return nil
}
