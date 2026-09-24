package notifications

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// SEC-015: credentials that live in a URL or name (Discord/Slack webhook URLs, the ntfy topic, an
// Apprise config key, the token in a generic webhook URL) are bearer credentials too; API
// responses must not return them in clear.

func TestMaskSecretsBearerURLsAndNames(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		set    map[string]any
		key    string
		want   string
		secret string // must not appear anywhere in the masked settings
	}{
		{"discord webhook URL", KindDiscord,
			map[string]any{"webhookUrl": "https://discord.com/api/webhooks/123/tok-SECRET", "username": "Dupearr"},
			"webhookUrl", MaskedValue, "tok-SECRET"},
		{"slack webhook URL", KindSlack,
			map[string]any{"webhookUrl": "https://hooks.slack.com/services/T0/B0/xyz-SECRET"},
			"webhookUrl", MaskedValue, "xyz-SECRET"},
		{"ntfy topic", KindNtfy,
			map[string]any{"topic": "alerts_SECRET"},
			"topic", MaskedValue, "alerts_SECRET"},
		{"apprise config key", KindApprise,
			map[string]any{"serverUrl": "http://apprise:8000", "configKey": "key_SECRET"},
			"configKey", MaskedValue, "key_SECRET"},
		{"webhook URL path, query and fragment", KindWebhook,
			map[string]any{"url": "https://ha.local:8123/api/webhook/hook-SECRET?token=tok-SECRET&v=1#frag-SECRET"},
			"url", "https://ha.local:8123/api/webhook/********?token=********&v=********#********", "SECRET"},
		{"webhook URL with trailing slash", KindWebhook,
			map[string]any{"url": "https://n8n.local/webhook/uuid-SECRET/"},
			"url", "https://n8n.local/webhook/********/", "SECRET"},
		{"webhook URL valueless query", KindWebhook,
			map[string]any{"url": "https://h.example/x?SECRET"},
			"url", "https://h.example/********?********", "SECRET"},
		{"webhook URL with credentials", KindWebhook,
			map[string]any{"url": "https://user:pw-SECRET@h.example/"},
			"url", "https://********@h.example/", "SECRET"},
		{"webhook URL with a token before the last segment (Discord /slack)", KindWebhook,
			map[string]any{"url": "https://discord.com/api/webhooks/123456789012345678/tokSECRETabcdefghijklmnop/slack"},
			"url", "https://discord.com/api/webhooks/********/********/********", "SECRET"},
		{"webhook URL with a bot token segment (Telegram)", KindWebhook,
			map[string]any{"url": "https://api.telegram.org/bot123456:ABC-SECRET/sendMessage?chat_id=1"},
			"url", "https://api.telegram.org/********/********?chat_id=********", "SECRET"},
		{"webhook URL with matrix parameters", KindWebhook,
			map[string]any{"url": "https://h.example/api;token=SECRET/hook"},
			"url", "https://h.example/********/********", "SECRET"},
		{"webhook URL route names stay readable", KindWebhook,
			map[string]any{"url": "https://h.example/api/v1/services/hook-x"},
			"url", "https://h.example/api/v1/services/********", ""},
		{"webhook URL without path", KindWebhook,
			map[string]any{"url": "https://h.example"},
			"url", "https://h.example", ""},
		{"unparsable webhook URL never echoed", KindWebhook,
			map[string]any{"url": "http://[::1%zz/SECRET"},
			"url", MaskedValue, "SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := MaskSecrets(newConfig(tt.kind, tt.set))
			if got := settingsMap(t, masked.Settings)[tt.key]; got != tt.want {
				t.Errorf("%s = %#v, want %#v", tt.key, got, tt.want)
			}
			if tt.secret != "" && strings.Contains(string(masked.Settings), tt.secret) {
				t.Errorf("masked settings leak %q: %s", tt.secret, masked.Settings)
			}
		})
	}
}

func TestSchemaMarksBearerCredentialsSecret(t *testing.T) {
	for _, c := range []struct{ kind, field string }{
		{KindDiscord, "webhookUrl"}, {KindSlack, "webhookUrl"}, {KindNtfy, "topic"}, {KindApprise, "configKey"},
	} {
		p := mustSchema(t, c.kind)
		i := slices.IndexFunc(p.Fields, func(f FieldSchema) bool { return f.Name == c.field })
		if i < 0 || !p.Fields[i].Secret {
			t.Errorf("%s.%s must be a secret field", c.kind, c.field)
		}
	}
}

// TestMaskedURLRoundTrip: sending the masked form back unchanged keeps the stored value, for
// fully masked (Discord/Slack) and partially masked (generic webhook) URLs.
func TestMaskedURLRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		kind string
		set  map[string]any
	}{
		{KindDiscord, map[string]any{"webhookUrl": "https://discord.com/api/webhooks/123/tok"}},
		{KindSlack, map[string]any{"webhookUrl": "https://hooks.slack.com/services/T0/B0/xyz"}},
		{KindNtfy, map[string]any{"topic": "alerts_x1", "accessToken": "tk_1"}},
		{KindApprise, map[string]any{"serverUrl": "http://apprise:8000", "configKey": "dupearr"}},
		{KindWebhook, map[string]any{"url": "https://ha.local/api/webhook/abc?token=t1&x=%2F", "username": "u", "password": "pw",
			"headers": "X-Api-Key: k1"}},
		{KindWebhook, map[string]any{"url": "https://discord.com/api/webhooks/123456789012345678/tok-abcdefghijklmnop/slack"}},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			stored := newConfig(tt.kind, tt.set, models.OnFileDeleted)
			merged := MergeSecrets(MaskSecrets(stored), stored)
			if errs := ValidateConfig(merged); errs != nil {
				t.Fatalf("merged config invalid: %+v", errs)
			}
			if !jsonEqual(t, merged.Settings, stored.Settings) {
				t.Fatalf("merged %s != stored %s", merged.Settings, stored.Settings)
			}
		})
	}
}

// TestMaskedWebhookURLEdited: a masked URL that was edited (host or path changed, mask kept) is
// never completed with the stored secret parts, and validation asks for the full URL again.
func TestMaskedWebhookURLEdited(t *testing.T) {
	stored := newConfig(KindWebhook, map[string]any{
		"url": "https://ha.local/api/webhook/hook-SECRET?token=tok-SECRET", "username": "u", "password": "pw-SECRET",
	}, models.OnFileDeleted)
	for name, url := range map[string]string{
		"host changed": "https://attacker.example/api/webhook/********?token=********",
		"path changed": "https://ha.local/api/other/********?token=********",
		"query added":  "https://ha.local/api/webhook/********?token=********&y=1",
	} {
		t.Run(name, func(t *testing.T) {
			edited := MaskSecrets(stored)
			obj := settingsMap(t, edited.Settings)
			obj["url"] = url
			edited.Settings = mustJSON(t, obj)
			merged := MergeSecrets(edited, stored)
			if strings.Contains(string(merged.Settings), "SECRET") {
				t.Fatalf("stored secret restored for an edited URL: %s", merged.Settings)
			}
			errs := ValidateConfig(merged)
			if !slices.Contains(props(errs), "url") || !slices.Contains(props(errs), "password") {
				t.Fatalf("errs = %+v, want url and password errors", errs)
			}
		})
	}
}

// TestDiscordURLChangeNeedsNoOtherSecret: re-entering the Discord webhook URL (a new channel) works;
// the mask for it alone never carries over to another kind.
func TestDiscordURLChangeAndKindChange(t *testing.T) {
	stored := newConfig(KindDiscord, map[string]any{"webhookUrl": "https://discord.com/api/webhooks/1/old"}, models.OnFileDeleted)
	edited := newConfig(KindDiscord, map[string]any{"webhookUrl": "https://discord.com/api/webhooks/2/new"}, models.OnFileDeleted)
	if merged := MergeSecrets(edited, stored); !jsonEqual(t, merged.Settings, edited.Settings) || ValidateConfig(merged) != nil {
		t.Fatalf("merged = %s", merged.Settings)
	}
	toSlack := newConfig(KindSlack, map[string]any{"webhookUrl": MaskedValue}, models.OnFileDeleted)
	merged := MergeSecrets(toSlack, stored)
	if strings.Contains(string(merged.Settings), "old") || !slices.Contains(props(ValidateConfig(merged)), "webhookUrl") {
		t.Fatalf("kind change carried the Discord URL: %s", merged.Settings)
	}
}

func TestSecretURLFieldsAreTrimmed(t *testing.T) {
	cfg := newConfig(KindDiscord, map[string]any{"webhookUrl": "  https://discord.com/api/webhooks/1/abc \n"})
	if errs := ValidateConfig(cfg); errs != nil {
		t.Fatalf("a pasted URL with surrounding whitespace must stay valid: %+v", errs)
	}
}

func TestSensitiveValuesOfSecretURL(t *testing.T) {
	sch := mustSchema(t, KindDiscord)
	set, err := parseSettings(sch, newConfig(KindDiscord, map[string]any{"webhookUrl": "https://discord.com/api/webhooks/1/tok-x"}).Settings)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(set.sensitiveValues(), "|")
	if !strings.Contains(got, "/api/webhooks/1/tok-x") {
		t.Errorf("sensitive values %q miss the webhook path", got)
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	am, bm := settingsMap(t, a), settingsMap(t, b)
	if len(am) != len(bm) {
		return false
	}
	for k, v := range bm {
		if am[k] != v {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
