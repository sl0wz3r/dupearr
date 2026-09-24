package notifications

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// validSettings returns a minimal valid settings object per kind.
func validSettings(kind string) map[string]any {
	switch kind {
	case KindDiscord:
		return map[string]any{"webhookUrl": "https://discord.com/api/webhooks/1/abc"}
	case KindSlack:
		return map[string]any{"webhookUrl": "https://hooks.slack.com/services/T/B/x"}
	case KindTelegram:
		return map[string]any{"botToken": "123456:ABC-def_ghi", "chatId": "-1001234567890"}
	case KindPushover:
		return map[string]any{"appToken": "azGDORePK8gMaC0QOYAMyEEuzJnyUi", "userKey": "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"}
	case KindGotify:
		return map[string]any{"serverUrl": "http://gotify:80", "appToken": "AbCdEf123"}
	case KindNtfy:
		return map[string]any{"topic": "dupearr_alerts"}
	case KindApprise:
		return map[string]any{"serverUrl": "http://apprise:8000", "configKey": "dupearr"}
	case KindWebhook:
		return map[string]any{"url": "https://example.com/hook"}
	case KindEmail:
		return map[string]any{"host": "smtp.example.com", "port": 587, "from": "Dupearr <dupearr@example.com>", "to": "me@example.com"}
	}
	return map[string]any{}
}

// with returns a copy of m with overrides applied (nil value deletes the key).
func with(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			delete(out, kv[i].(string))
			continue
		}
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func props(errs []config.ValidationError) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.PropertyName)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestValidateConfigValidPerKind(t *testing.T) {
	for _, kind := range knownKinds() {
		t.Run(kind, func(t *testing.T) {
			cfg := newConfig(kind, validSettings(kind), models.OnDuplicatesFound, models.OnHealthIssue)
			if errs := ValidateConfig(cfg); errs != nil {
				t.Fatalf("unexpected errors: %+v", errs)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(c *models.NotificationConfig)
		kind      string
		settings  map[string]any
		wantProps []string
		wantMsg   string
	}{
		{name: "empty name", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Name = "  " }, wantProps: []string{"name"}},
		{name: "long name", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Name = strings.Repeat("x", 129) }, wantProps: []string{"name"}},
		{name: "control char in name", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Name = "a\nb" }, wantProps: []string{"name"}},
		{name: "missing kind", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Kind = "" }, wantProps: []string{"kind"}},
		{name: "unknown kind", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Kind = "carrier-pigeon" }, wantProps: []string{"kind"}, wantMsg: "expected one of discord"},
		{name: "unknown trigger", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Triggers = []string{models.OnFileDeleted, "onGrab"} }, wantProps: []string{"triggers"}, wantMsg: `"onGrab"`},
		{name: "settings not object", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Settings = json.RawMessage(`["x"]`) }, wantProps: []string{"settings"}},
		{name: "settings malformed", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Settings = json.RawMessage(`{"url":`) }, wantProps: []string{"settings"}},
		{name: "null settings means empty", kind: KindWebhook, mutate: func(c *models.NotificationConfig) { c.Settings = nil }, wantProps: []string{"url"}, wantMsg: "URL is required"},

		// Generic field checks.
		{name: "required missing", kind: KindDiscord, settings: map[string]any{}, wantProps: []string{"webhookUrl"}},
		{name: "url scheme", kind: KindDiscord, settings: map[string]any{"webhookUrl": "ftp://x/y"}, wantProps: []string{"webhookUrl"}, wantMsg: "http:// or https://"},
		{name: "url without host", kind: KindDiscord, settings: map[string]any{"webhookUrl": "https:///path"}, wantProps: []string{"webhookUrl"}},
		{name: "url with spaces", kind: KindDiscord, settings: map[string]any{"webhookUrl": "https://a b/"}, wantProps: []string{"webhookUrl"}},
		{name: "url with credentials", kind: KindWebhook, settings: map[string]any{"url": "https://u:p@example.com/"}, wantProps: []string{"url"}, wantMsg: "credentials"},
		{name: "relative url", kind: KindWebhook, settings: map[string]any{"url": "/hook"}, wantProps: []string{"url"}},
		{name: "string field given object", kind: KindDiscord, settings: with(validSettings(KindDiscord), "username", map[string]any{"a": 1}), wantProps: []string{"username"}},
		{name: "number field given text", kind: KindGotify, settings: with(validSettings(KindGotify), "priority", "high"), wantProps: []string{"priority"}},
		{name: "number field given float", kind: KindGotify, settings: with(validSettings(KindGotify), "priority", 1.5), wantProps: []string{"priority"}},
		{name: "number as numeric string ok", kind: KindGotify, settings: with(validSettings(KindGotify), "priority", "7")},
		{name: "checkbox given number", kind: KindTelegram, settings: with(validSettings(KindTelegram), "sendSilently", 1), wantProps: []string{"sendSilently"}},
		{name: "checkbox as string ok", kind: KindTelegram, settings: with(validSettings(KindTelegram), "sendSilently", "true")},
		{name: "select invalid", kind: KindWebhook, settings: with(validSettings(KindWebhook), "method", "DELETE"), wantProps: []string{"method"}},
		{name: "select numeric value ok", kind: KindPushover, settings: with(validSettings(KindPushover), "priority", -1)},
		{name: "masked secret rejected", kind: KindGotify, settings: with(validSettings(KindGotify), "appToken", MaskedValue), wantProps: []string{"appToken"}, wantMsg: "re-enter"},
		{name: "control chars in text", kind: KindDiscord, settings: with(validSettings(KindDiscord), "username", "bot\x00"), wantProps: []string{"username"}},
		{name: "control chars in secret", kind: KindGotify, settings: with(validSettings(KindGotify), "appToken", "tok\r\nX-Evil: 1"), wantProps: []string{"appToken"}},

		// Telegram.
		{name: "telegram bad token", kind: KindTelegram, settings: with(validSettings(KindTelegram), "botToken", "abc/../x"), wantProps: []string{"botToken"}},
		{name: "telegram bad chat", kind: KindTelegram, settings: with(validSettings(KindTelegram), "chatId", "my chat"), wantProps: []string{"chatId"}},
		{name: "telegram channel username ok", kind: KindTelegram, settings: with(validSettings(KindTelegram), "chatId", "@dupearr_news")},
		{name: "telegram negative topic", kind: KindTelegram, settings: with(validSettings(KindTelegram), "topicId", -3), wantProps: []string{"topicId"}},

		// Pushover.
		{name: "pushover token chars", kind: KindPushover, settings: with(validSettings(KindPushover), "appToken", "abc-123"), wantProps: []string{"appToken"}},
		{name: "pushover bad device", kind: KindPushover, settings: with(validSettings(KindPushover), "devices", "phone, my tablet"), wantProps: []string{"devices"}},
		{name: "pushover devices ok", kind: KindPushover, settings: with(validSettings(KindPushover), "devices", "phone, tablet_2")},
		{name: "pushover retry too low", kind: KindPushover, settings: with(validSettings(KindPushover), "retry", 10), wantProps: []string{"retry"}},
		{name: "pushover expire too high", kind: KindPushover, settings: with(validSettings(KindPushover), "expire", 20000), wantProps: []string{"expire"}},
		{name: "pushover priority out of options", kind: KindPushover, settings: with(validSettings(KindPushover), "priority", "3"), wantProps: []string{"priority"}},

		// Gotify.
		{name: "gotify priority range", kind: KindGotify, settings: with(validSettings(KindGotify), "priority", 11), wantProps: []string{"priority"}},

		// ntfy.
		{name: "ntfy bad topic", kind: KindNtfy, settings: with(validSettings(KindNtfy), "topic", "my topic"), wantProps: []string{"topic"}},
		{name: "ntfy token and user", kind: KindNtfy, settings: with(validSettings(KindNtfy), "accessToken", "tk_x", "username", "u", "password", "p"), wantProps: []string{"accessToken"}},
		{name: "ntfy user without password", kind: KindNtfy, settings: with(validSettings(KindNtfy), "username", "u"), wantProps: []string{"password"}},
		{name: "ntfy password without user", kind: KindNtfy, settings: with(validSettings(KindNtfy), "password", "p"), wantProps: []string{"username"}},
		{name: "ntfy priority", kind: KindNtfy, settings: with(validSettings(KindNtfy), "priority", "9"), wantProps: []string{"priority"}},
		{name: "ntfy empty server uses default", kind: KindNtfy, settings: with(validSettings(KindNtfy), "serverUrl", "")},
		{name: "ntfy bad tag", kind: KindNtfy, settings: with(validSettings(KindNtfy), "tags", "ok, not ok"), wantProps: []string{"tags"}},

		// Apprise.
		{name: "apprise neither key nor urls", kind: KindApprise, settings: with(validSettings(KindApprise), "configKey", nil), wantProps: []string{"configKey"}},
		{name: "apprise both", kind: KindApprise, settings: with(validSettings(KindApprise), "urls", "json://x"), wantProps: []string{"configKey"}, wantMsg: "not both"},
		{name: "apprise bad key", kind: KindApprise, settings: with(validSettings(KindApprise), "configKey", "../etc"), wantProps: []string{"configKey"}},
		{name: "apprise urls ok", kind: KindApprise, settings: with(validSettings(KindApprise), "configKey", nil, "urls", "mailto://u:p@example.com\njson://host")},
		{name: "apprise password without user", kind: KindApprise, settings: with(validSettings(KindApprise), "password", "p"), wantProps: []string{"username"}},

		// Webhook.
		{name: "webhook headers ok", kind: KindWebhook, settings: with(validSettings(KindWebhook), "headers", "X-Token: abc\n# comment\n\nX-Other: 1:2")},
		{name: "webhook header without colon", kind: KindWebhook, settings: with(validSettings(KindWebhook), "headers", "X-Token abc"), wantProps: []string{"headers"}, wantMsg: "line 1"},
		{name: "webhook header bad name", kind: KindWebhook, settings: with(validSettings(KindWebhook), "headers", "Bad Name: x"), wantProps: []string{"headers"}},
		{name: "webhook forbidden header", kind: KindWebhook, settings: with(validSettings(KindWebhook), "headers", "Host: evil"), wantProps: []string{"headers"}},
		{name: "webhook authorization conflict", kind: KindWebhook, settings: with(validSettings(KindWebhook), "headers", "Authorization: Bearer x", "username", "u"), wantProps: []string{"headers"}},
		{name: "webhook password without user", kind: KindWebhook, settings: with(validSettings(KindWebhook), "password", "p"), wantProps: []string{"username"}},

		// Email.
		{name: "email host with scheme", kind: KindEmail, settings: with(validSettings(KindEmail), "host", "smtp://mail"), wantProps: []string{"host"}},
		{name: "email ip host ok", kind: KindEmail, settings: with(validSettings(KindEmail), "host", "192.168.1.5")},
		{name: "email port range", kind: KindEmail, settings: with(validSettings(KindEmail), "port", 70000), wantProps: []string{"port"}},
		{name: "email bad from", kind: KindEmail, settings: with(validSettings(KindEmail), "from", "not an address"), wantProps: []string{"from"}},
		{name: "email bad to", kind: KindEmail, settings: with(validSettings(KindEmail), "to", "a@example.com, nope"), wantProps: []string{"to"}},
		{name: "email bad bcc", kind: KindEmail, settings: with(validSettings(KindEmail), "bcc", "@@"), wantProps: []string{"bcc"}},
		{name: "email missing to", kind: KindEmail, settings: with(validSettings(KindEmail), "to", nil), wantProps: []string{"to"}},
		{name: "email bad encryption", kind: KindEmail, settings: with(validSettings(KindEmail), "encryption", "ssl"), wantProps: []string{"encryption"}},
		{name: "email creds without tls", kind: KindEmail, settings: with(validSettings(KindEmail), "encryption", "none", "username", "u", "password", "p"), wantProps: []string{"encryption"}},
		{name: "email creds without tls on localhost ok", kind: KindEmail, settings: with(validSettings(KindEmail), "host", "127.0.0.1", "encryption", "none", "username", "u", "password", "p")},
		{name: "email password without user", kind: KindEmail, settings: with(validSettings(KindEmail), "password", "p"), wantProps: []string{"username"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := tt.settings
			if set == nil {
				set = validSettings(tt.kind)
			}
			cfg := newConfig(tt.kind, set, models.OnDuplicatesFound)
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			errs := ValidateConfig(cfg)
			if got := props(errs); !slices.Equal(got, tt.wantProps) {
				t.Fatalf("props = %v, want %v (errors %+v)", got, tt.wantProps, errs)
			}
			if tt.wantMsg != "" {
				found := false
				for _, e := range errs {
					if strings.Contains(e.ErrorMessage, tt.wantMsg) {
						found = true
					}
				}
				if !found {
					t.Fatalf("no error message contains %q: %+v", tt.wantMsg, errs)
				}
			}
			for _, e := range errs {
				if e.ErrorMessage == "" {
					t.Errorf("empty message for %s", e.PropertyName)
				}
			}
		})
	}
}

func TestValidateConfigNeverEchoesSecrets(t *testing.T) {
	secret := "super-secret-password-value"
	cfg := newConfig(KindEmail, with(validSettings(KindEmail), "encryption", "none", "username", "u", "password", secret, "port", "x"))
	for _, e := range ValidateConfig(cfg) {
		if strings.Contains(e.ErrorMessage, secret) {
			t.Fatalf("validation message leaks the secret: %q", e.ErrorMessage)
		}
	}
}

func TestParseHeaders(t *testing.T) {
	h, err := parseHeaders("x-token: abc \r\n\n# skip: me\nX-Multi: 1\nx-multi: 2\nX-Colon: a:b")
	if err != nil {
		t.Fatal(err)
	}
	if h.Get("X-Token") != "abc" || h.Get("X-Colon") != "a:b" || len(h.Values("X-Multi")) != 2 {
		t.Fatalf("headers = %v", h)
	}
	if _, ok := h["Skip"]; ok {
		t.Fatal("comment parsed as header")
	}
	for _, bad := range []string{"NoColon", ": value", "Content-Length: 5", "Transfer-Encoding: chunked", "X\x7fBad: v", "X-Ok: bad\x00value"} {
		if _, err := parseHeaders(bad); err == nil {
			t.Errorf("parseHeaders(%q) = nil error", bad)
		}
	}
}
