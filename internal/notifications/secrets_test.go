package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func settingsMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("settings %s: %v", raw, err)
	}
	return m
}

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		name     string
		cfg      models.NotificationConfig
		want     map[string]any
		wantJSON string // exact settings JSON when set
	}{
		{
			name: "telegram token masked, chat kept",
			cfg:  newConfig(KindTelegram, map[string]any{"botToken": "123:abc", "chatId": "-100", "topicId": 5}),
			want: map[string]any{"botToken": MaskedValue, "chatId": "-100", "topicId": float64(5)},
		},
		{
			name: "pushover both keys masked",
			cfg:  newConfig(KindPushover, map[string]any{"appToken": "aaa", "userKey": "uuu", "priority": "1"}),
			want: map[string]any{"appToken": MaskedValue, "userKey": MaskedValue, "priority": "1"},
		},
		{
			name: "empty secret stays empty",
			cfg:  newConfig(KindNtfy, map[string]any{"topic": "", "accessToken": "", "password": nil, "username": "u"}),
			want: map[string]any{"topic": "", "accessToken": "", "password": nil, "username": "u"},
		},
		{
			name: "apprise urls textarea masked",
			cfg:  newConfig(KindApprise, map[string]any{"serverUrl": "http://a", "urls": "mailto://u:p@x"}),
			want: map[string]any{"serverUrl": "http://a", "urls": MaskedValue},
		},
		{
			name: "webhook password and header values masked, benign headers kept",
			cfg: newConfig(KindWebhook, map[string]any{"url": "http://a", "username": "u", "password": "p",
				"headers": "Authorization: Bearer tok\nAccept: application/json\n# X-Old-Key: old\n# a note\n\nX-Empty:"}),
			want: map[string]any{"url": "http://a", "username": "u", "password": MaskedValue,
				"headers": "Authorization: ********\nAccept: application/json\n# X-Old-Key: ********\n# a note\n\nX-Empty:"},
		},
		{
			name: "non-string headers never echoed",
			cfg:  newConfig(KindWebhook, map[string]any{"url": "http://a", "headers": map[string]any{"Authorization": "x"}}),
			want: map[string]any{"url": "http://a", "headers": MaskedValue},
		},
		{
			name: "email password masked",
			cfg:  newConfig(KindEmail, map[string]any{"host": "h", "password": "pw", "port": 465}),
			want: map[string]any{"host": "h", "password": MaskedValue, "port": float64(465)},
		},
		{
			name: "unknown keys of known kind masked",
			cfg:  newConfig(KindDiscord, map[string]any{"webhookUrl": "https://d/x", "username": "bot", "botToken": "left-over"}),
			want: map[string]any{"webhookUrl": MaskedValue, "username": "bot", "botToken": MaskedValue},
		},
		{
			name: "unknown kind masks every string",
			cfg:  newConfig("mystery", map[string]any{"a": "x", "n": 3, "b": true}),
			want: map[string]any{"a": MaskedValue, "n": float64(3), "b": true},
		},
		{
			name:     "invalid settings become empty object",
			cfg:      models.NotificationConfig{Kind: KindWebhook, Settings: json.RawMessage(`"secret-string"`)},
			wantJSON: `{}`,
		},
		{
			name:     "nil settings become empty object",
			cfg:      models.NotificationConfig{Kind: KindWebhook},
			wantJSON: `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := string(tt.cfg.Settings)
			got := MaskSecrets(tt.cfg)
			if string(tt.cfg.Settings) != orig {
				t.Fatal("MaskSecrets mutated its input")
			}
			if got.Triggers == nil {
				t.Fatal("Triggers must not be nil")
			}
			if got.ID != tt.cfg.ID || got.Name != tt.cfg.Name || got.Kind != tt.cfg.Kind || got.Enabled != tt.cfg.Enabled {
				t.Fatal("non-settings fields changed")
			}
			if tt.wantJSON != "" {
				if string(got.Settings) != tt.wantJSON {
					t.Fatalf("settings = %s, want %s", got.Settings, tt.wantJSON)
				}
				return
			}
			m := settingsMap(t, got.Settings)
			if len(m) != len(tt.want) {
				t.Fatalf("settings = %v, want %v", m, tt.want)
			}
			for k, v := range tt.want {
				if m[k] != v {
					t.Errorf("%s = %#v, want %#v", k, m[k], v)
				}
			}
		})
	}
}

func TestMaskSecretsJSONHasEmptyTriggers(t *testing.T) {
	cfg := models.NotificationConfig{Kind: KindSlack, Settings: json.RawMessage(`{"webhookUrl":"https://h/x"}`)}
	b, err := json.Marshal(MaskSecrets(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"triggers":[]`) {
		t.Fatalf("JSON = %s", b)
	}
}

func TestMergeSecrets(t *testing.T) {
	stored := newConfig(KindWebhook, map[string]any{"url": "https://old/hook", "username": "u", "password": "stored-pass"})
	tests := []struct {
		name     string
		incoming models.NotificationConfig
		want     map[string]any
	}{
		{
			name:     "mask restored",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://old/hook", "username": "u2", "password": MaskedValue}),
			want:     map[string]any{"url": "https://old/hook", "username": "u2", "password": "stored-pass"},
		},
		{
			name:     "url changed: stored secret never sent to the new destination",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://attacker.example/hook", "username": "u", "password": MaskedValue}),
			want:     map[string]any{"url": "https://attacker.example/hook", "username": "u", "password": MaskedValue},
		},
		{
			name:     "url changed with a re-entered secret",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://new/hook", "username": "u", "password": "typed-again"}),
			want:     map[string]any{"url": "https://new/hook", "username": "u", "password": "typed-again"},
		},
		{
			name:     "new secret kept",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://old/hook", "password": "new-pass"}),
			want:     map[string]any{"url": "https://old/hook", "password": "new-pass"},
		},
		{
			name:     "cleared secret stays cleared",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://old/hook", "password": ""}),
			want:     map[string]any{"url": "https://old/hook", "password": ""},
		},
		{
			name:     "mask in non-secret field untouched",
			incoming: newConfig(KindWebhook, map[string]any{"url": "https://old/hook", "username": MaskedValue}),
			want:     map[string]any{"url": "https://old/hook", "username": MaskedValue},
		},
		{
			name:     "kind change never carries secrets",
			incoming: newConfig(KindEmail, map[string]any{"host": "h", "password": MaskedValue}),
			want:     map[string]any{"host": "h", "password": MaskedValue},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeSecrets(tt.incoming, stored)
			m := settingsMap(t, got.Settings)
			if len(m) != len(tt.want) {
				t.Fatalf("settings = %v, want %v", m, tt.want)
			}
			for k, v := range tt.want {
				if m[k] != v {
					t.Errorf("%s = %#v, want %#v", k, m[k], v)
				}
			}
		})
	}
}

func TestMaskMergeRoundTrip(t *testing.T) {
	for _, kind := range knownKinds() {
		t.Run(kind, func(t *testing.T) {
			set := validSettings(kind)
			switch kind {
			case KindNtfy:
				set = with(set, "accessToken", "tk_secret")
			case KindWebhook:
				set = with(set, "username", "u", "password", "hunter22",
					"headers", "X-Api-Key: key-1\nAccept: text/plain\nX-Api-Key: key-2\n# X-Old: old-key")
			case KindApprise:
				set = with(set, "username", "u", "password", "hunter22")
			case KindEmail:
				set = with(set, "username", "u", "password", "hunter22")
			}
			stored := newConfig(kind, set, models.OnFileDeleted)
			masked := MaskSecrets(stored)
			if !strings.Contains(string(masked.Settings), MaskedValue) {
				t.Fatalf("nothing masked: %s", masked.Settings)
			}
			merged := MergeSecrets(masked, stored)
			if errs := ValidateConfig(merged); errs != nil {
				t.Fatalf("merged config invalid: %+v", errs)
			}
			a, b := settingsMap(t, merged.Settings), settingsMap(t, stored.Settings)
			if len(a) != len(b) {
				t.Fatalf("merged %v != stored %v", a, b)
			}
			for k := range b {
				if a[k] != b[k] {
					t.Errorf("%s: merged %#v != stored %#v", k, a[k], b[k])
				}
			}
		})
	}
}

func TestMergeSecretsUnresolvedMaskFailsValidation(t *testing.T) {
	incoming := newConfig(KindGotify, map[string]any{"serverUrl": "http://g", "appToken": MaskedValue})
	merged := MergeSecrets(incoming, models.NotificationConfig{}) // nothing stored
	errs := ValidateConfig(merged)
	if len(errs) != 1 || errs[0].PropertyName != "appToken" {
		t.Fatalf("errs = %+v", errs)
	}
}

func TestSensitiveValues(t *testing.T) {
	sch, _ := providerSchema(KindWebhook)
	set, err := parseSettings(sch, newConfig(KindWebhook, map[string]any{
		"url": "https://h.example/hooks/tok?key=qv", "username": "user", "password": "pw-1",
		"headers": "Authorization: Bearer hdr-token",
	}).Settings)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(set.sensitiveValues(), "|")
	for _, want := range []string{"pw-1", "/hooks/tok", "key=qv", "Bearer hdr-token"} {
		if !strings.Contains(got, want) {
			t.Errorf("sensitive values %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "user") || strings.Contains(got, "h.example") {
		t.Errorf("non-secret values included: %q", got)
	}
}

func TestMergeSecretsDestinationBinding(t *testing.T) {
	tests := []struct {
		name         string
		kind         string
		stored       map[string]any
		incoming     map[string]any
		wantRestored bool
		secretKey    string // field whose restoration is checked
	}{
		{"gotify same server", KindGotify,
			map[string]any{"serverUrl": "http://gotify:80", "appToken": "tok"},
			map[string]any{"serverUrl": " http://gotify:80 ", "appToken": MaskedValue, "priority": 8}, true, "appToken"},
		{"gotify server changed", KindGotify,
			map[string]any{"serverUrl": "http://gotify:80", "appToken": "tok"},
			map[string]any{"serverUrl": "http://evil:80", "appToken": MaskedValue}, false, "appToken"},
		{"ntfy default server made explicit", KindNtfy,
			map[string]any{"topic": "t", "accessToken": "tk_1"},
			map[string]any{"serverUrl": DefaultNtfyServer, "topic": "t2", "accessToken": MaskedValue}, true, "accessToken"},
		{"ntfy server changed from default", KindNtfy,
			map[string]any{"topic": "t", "username": "u", "password": "pw"},
			map[string]any{"serverUrl": "https://ntfy.evil", "topic": "t", "username": "u", "password": MaskedValue}, false, "password"},
		{"apprise urls follow the server", KindApprise,
			map[string]any{"serverUrl": "http://apprise:8000", "urls": "tgram://secret/1"},
			map[string]any{"serverUrl": "http://evil:8000", "urls": MaskedValue}, false, "urls"},
		{"webhook method change keeps secret", KindWebhook,
			map[string]any{"url": "https://h/hook", "username": "u", "password": "pw"},
			map[string]any{"url": "https://h/hook", "method": "PUT", "username": "u", "password": MaskedValue}, true, "password"},
		{"webhook path change (multi-tenant receivers)", KindWebhook,
			map[string]any{"url": "https://webhook.site/aaa", "username": "u", "password": "pw"},
			map[string]any{"url": "https://webhook.site/bbb", "username": "u", "password": MaskedValue}, false, "password"},
		{"email host case-insensitive", KindEmail,
			with(validSettings(KindEmail), "username", "u", "password", "pw"),
			with(validSettings(KindEmail), "host", "SMTP.Example.com", "username", "u", "password", MaskedValue), true, "password"},
		{"email port default vs explicit", KindEmail,
			with(validSettings(KindEmail), "port", nil, "username", "u", "password", "pw"),
			with(validSettings(KindEmail), "port", 587, "username", "u", "password", MaskedValue), true, "password"},
		{"email host changed", KindEmail,
			with(validSettings(KindEmail), "username", "u", "password", "pw"),
			with(validSettings(KindEmail), "host", "smtp.evil.example", "username", "u", "password", MaskedValue), false, "password"},
		{"email port changed", KindEmail,
			with(validSettings(KindEmail), "username", "u", "password", "pw"),
			with(validSettings(KindEmail), "port", 2525, "username", "u", "password", MaskedValue), false, "password"},
		{"telegram fixed endpoint: chat change keeps token", KindTelegram,
			validSettings(KindTelegram),
			with(validSettings(KindTelegram), "chatId", "@otherchannel", "botToken", MaskedValue), true, "botToken"},
		{"pushover fixed endpoint", KindPushover,
			validSettings(KindPushover),
			with(validSettings(KindPushover), "devices", "phone", "userKey", MaskedValue), true, "userKey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored := newConfig(tt.kind, tt.stored, models.OnFileDeleted)
			merged := MergeSecrets(newConfig(tt.kind, tt.incoming, models.OnFileDeleted), stored)
			got := settingsMap(t, merged.Settings)[tt.secretKey]
			want := any(MaskedValue)
			if tt.wantRestored {
				want = tt.stored[tt.secretKey]
			}
			if got != want {
				t.Fatalf("%s = %#v, want %#v", tt.secretKey, got, want)
			}
			errs := ValidateConfig(merged)
			if tt.wantRestored && errs != nil {
				t.Fatalf("restored config invalid: %+v", errs)
			}
			if !tt.wantRestored && !slices.Contains(props(errs), tt.secretKey) {
				t.Fatalf("unresolved mask not reported: %+v", errs)
			}
		})
	}
}

// TestSecretExfiltrationViaTest is the end-to-end attack: an API client that can only see masked
// connections re-points one at its own server and presses Test.
func TestSecretExfiltrationViaTest(t *testing.T) {
	attacker, rec := newRecorder(t, http.StatusOK, "")
	s, _, _ := newTestService(t)
	stored := newConfig(KindWebhook, map[string]any{
		"url": "https://ha.internal/api/webhook/x", "username": "u", "password": "stored-SECRET",
		"headers": "X-Api-Key: header-SECRET",
	}, models.OnFileDeleted)

	edited := MaskSecrets(stored)
	obj := settingsMap(t, edited.Settings)
	obj["url"] = attacker.URL + "/collect"
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	edited.Settings = raw

	err = s.Test(context.Background(), MergeSecrets(edited, stored))
	var vErr *ValidationFailedError
	if !errors.As(err, &vErr) || !slices.Contains(props(vErr.Errors), "password") || !slices.Contains(props(vErr.Errors), "headers") {
		t.Fatalf("Test = %v, want validation errors for password and headers", err)
	}
	assertNoLeak(t, err, "SECRET")
	for _, r := range rec.requests() {
		if strings.Contains(string(r.Body)+fmt.Sprint(r.Header), "SECRET") {
			t.Fatal("stored secret sent to the new destination")
		}
	}
	if n := len(rec.requests()); n != 0 {
		t.Fatalf("%d requests reached the new destination", n)
	}
}
