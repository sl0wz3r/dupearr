package notifications

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// SEC-017: titles, bodies and field values carry upstream-controlled text (Plex titles, release and
// file names, paths, upstream error text). Discord renders Markdown in embeds, so such text must not
// become a masked link, a mention, a heading or other formatting.

const phishing = "[Plex sign-in expired – re-authorize](https://evil.example/login)"

// unescapeDiscord reverses discordEscape: a backslash makes the next character literal.
func unescapeDiscord(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if !escaped && r == '\\' {
			escaped = true
			continue
		}
		escaped = false
		b.WriteRune(r)
	}
	return b.String()
}

func TestDiscordEscapesUpstreamText(t *testing.T) {
	set, _ := parseSettings(mustSchema(t, KindDiscord), nil)
	m := normalizeMessage(Message{
		Title: phishing,
		Body:  "Moved to the recycle bin via filesystem: /data/movies/" + phishing + ".mkv\n# Heading\n> quote\n- item\n1. first\n<@123> <#456> <t:1700000000> @everyone **bold** __u__ ~~s~~ ||spoiler|| `code` C:\\x",
		Fields: []Field{
			{Name: "Files", Value: "/data/a/" + phishing + ".mkv\n/data/b/https://evil.example/x.mkv"},
			{Name: phishing, Value: "v"},
		},
		URL: "https://dupearr.example.com/activity",
	})
	p := buildDiscordPayload(set, m, "D", fixedNow)
	e := p.Embeds[0]

	checks := map[string][2]string{
		"title":       {e.Title, m.Title},
		"description": {e.Description, m.Body},
		"field value": {e.Fields[0].Value, m.Fields[0].Value},
		"field name":  {e.Fields[1].Name, m.Fields[1].Name},
	}
	for what, c := range checks {
		got, orig := c[0], c[1]
		for _, bad := range []string{"](", "://", "<@", "<#", "<t:", "**", "__", "~~", "||", "`"} {
			if containsUnescaped(got, bad) {
				t.Errorf("%s %q renders %q", what, got, bad)
			}
		}
		for _, line := range strings.Split(got, "\n") {
			l := strings.TrimLeft(line, " \t")
			for _, marker := range []string{"#", ">", "- ", "* ", "1. "} {
				if strings.HasPrefix(l, marker) {
					t.Errorf("%s line %q starts with Markdown marker %q", what, line, marker)
				}
			}
		}
		if unescapeDiscord(got) != orig {
			t.Errorf("%s: escaping changed the text: %q → %q", what, orig, unescapeDiscord(got))
		}
	}
	if e.URL != "https://dupearr.example.com/activity" {
		t.Errorf("embed url = %q; the deep link is the only intentional link", e.URL)
	}
}

// containsUnescaped reports whether s contains sub at a position not preceded by an escaping
// backslash.
func containsUnescaped(s, sub string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return false
		}
		pos := i + j
		backslashes := 0
		for k := pos - 1; k >= 0 && s[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return true
		}
		i = pos + 1
	}
}

func TestDiscordTextTruncation(t *testing.T) {
	for _, s := range []string{strings.Repeat("[", 300), strings.Repeat("a[", 200), strings.Repeat(`\`, 301)} {
		got := discordText(s, discordMaxTitle)
		if n := utf8.RuneCountInString(got); n > discordMaxTitle {
			t.Fatalf("len = %d > %d", n, discordMaxTitle)
		}
		body := strings.TrimSuffix(got, ellipsis)
		if containsUnescaped(body+"x", `\x`) {
			t.Errorf("truncation split an escape: %q", got)
		}
		if !strings.HasPrefix(s, unescapeDiscord(body)) {
			t.Errorf("truncated text %q is not a prefix of the input", unescapeDiscord(body))
		}
	}
	if got := discordText("plain title", 256); got != "plain title" {
		t.Errorf("plain text changed: %q", got)
	}
}
