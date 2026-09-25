package logging

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		// Query strings.
		{"radarr apikey", "GET http://radarr:7878/api/v3/movie?apikey=0123456789abcdef0123456789abcdef",
			"GET http://radarr:7878/api/v3/movie?apikey=(removed)"},
		{"api_key mid query", "http://x/api?page=1&api_key=abc123&pageSize=10", "http://x/api?page=1&api_key=(removed)&pageSize=10"},
		{"apiKey camel", "http://x/api?apiKey=abc", "http://x/api?apiKey=(removed)"},
		{"plex token query", "http://plex:32400/library/sections/1/all?X-Plex-Token=tok-EN_123&includeGuids=1",
			"http://plex:32400/library/sections/1/all?X-Plex-Token=(removed)&includeGuids=1"},
		{"plex token lower", "http://plex/x?x-plex-token=abc", "http://plex/x?x-plex-token=(removed)"},
		{"token", "https://gotify.example/message?token=AbC.dEf", "https://gotify.example/message?token=(removed)"},
		{"access_token fragment", "https://app/cb#access_token=abc&state=1", "https://app/cb#access_token=(removed)&state=1"},
		{"several params", "?apikey=a&token=b&password=c&keep=d", "?apikey=(removed)&token=(removed)&password=(removed)&keep=d"},
		{"key=value text", "login password=hunter2 user=bob", "login password=(removed) user=bob"},
		{"quoted kv", `retry token="abc"`, `retry token="(removed)"`},
		{"pass and auth", "x?pass=p1&auth=a1&pwd=p2", "x?pass=(removed)&auth=(removed)&pwd=(removed)"},

		// Headers.
		{"x-api-key header", "X-Api-Key: 0123456789abcdef", "X-Api-Key: (removed)"},
		{"x-plex-token header", "X-Plex-Token: abcDEF123", "X-Plex-Token: (removed)"},
		{"http.Header dump", "map[Accept:[application/json] X-Api-Key:[k1] X-Plex-Token:[t1]]",
			"map[Accept:[application/json] X-Api-Key:[(removed)] X-Plex-Token:[(removed)]]"},
		{"bearer", "Authorization: Bearer eyJhbGciOi.payload.sig", "Authorization: Bearer (removed)"},
		{"basic", "authorization: Basic dXNlcjpwYXNz==", "authorization: Basic (removed)"},
		{"bare authorization", "Authorization=abc123", "Authorization=(removed)"},
		{"json header", `{"X-Api-Key":"abc","Accept":"json"}`, `{"X-Api-Key":"(removed)","Accept":"json"}`},
		{"cookie", "Cookie: DupearrAuth=abc; theme=dark", "Cookie: (removed)"},
		{"set-cookie", "Set-Cookie: DupearrAuth=abc; Path=/; HttpOnly", "Set-Cookie: (removed)"},

		// JSON and XML.
		{"json password", `{"username":"bob","password":"hunter2"}`, `{"username":"bob","password":"(removed)"}`},
		{"json apiKey spaced", `{"apiKey": "abc", "name": "Radarr"}`, `{"apiKey": "(removed)", "name": "Radarr"}`},
		{"json escaped quote", `{"token":"a\"b","x":1}`, `{"token":"(removed)","x":1}`},
		{"json tokens", `{"accessToken":"a","authToken":"b","plexToken":"c"}`, `{"accessToken":"(removed)","authToken":"(removed)","plexToken":"(removed)"}`},
		{"json password confirmation", `{"passwordConfirmation":"x"}`, `{"passwordConfirmation":"(removed)"}`},
		{"json empty password kept", `{"password":""}`, `{"password":""}`},
		{"json secret word as value", `{"msg":"token","ok":true}`, `{"msg":"token","ok":true}`},
		{"xml api key", "<Config><ApiKey>abc</ApiKey><Port>1</Port></Config>", "<Config><ApiKey>(removed)</ApiKey><Port>1</Port></Config>"},
		{"xml password", "<SslCertPassword>pw</SslCertPassword>", "<SslCertPassword>(removed)</SslCertPassword>"},

		// Notification providers.
		{"discord", "POST https://discord.com/api/webhooks/123456789012345678/AbC-dEf_123xyz failed",
			"POST https://discord.com/api/webhooks/123456789012345678/(removed) failed"},
		{"discordapp v10", "https://discordapp.com/api/v10/webhooks/1/tok?wait=true", "https://discordapp.com/api/v10/webhooks/1/(removed)?wait=true"},
		{"discord ptb", "https://ptb.discord.com/api/webhooks/42/secret", "https://ptb.discord.com/api/webhooks/42/(removed)"},
		{"slack", "https://hooks.slack.com/services/T000/B000/XXXXXXXX", "https://hooks.slack.com/services/(removed)"},
		{"slack workflow", "url=https://hooks.slack.com/workflows/T1/A2/3/abc end", "url=https://hooks.slack.com/workflows/(removed) end"},
		{"telegram", "https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/sendMessage",
			"https://api.telegram.org/bot123456789:(removed)/sendMessage"},
		{"apprise telegram", "tgram://123456789:AAHdqTcvCH1v/-100200", "tgram://123456789:(removed)/-100200"},
		{"userinfo", "smtp://user:s3cr3t@mail.example.com:587", "smtp://user:(removed)@mail.example.com:587"},
		{"userinfo empty user", "https://:tok@host/x", "https://:(removed)@host/x"},

		// Nothing to redact.
		{"plain", "Scan finished: 3 groups in /data/movies (keyed by tmdb)", "Scan finished: 3 groups in /data/movies (keyed by tmdb)"},
		{"rating key", "ratingKey=123 key=/library/metadata/123", "ratingKey=123 key=/library/metadata/123"},
		{"port not userinfo", "http://host:8080/path@x", "http://host:8080/path@x"},
		{"monkey", "monkey=1 author=me", "monkey=1 author=me"},
		{"email", "notify admin@example.com", "notify admin@example.com"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			if got != tt.want {
				t.Fatalf("Redact(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if again := Redact(got); again != got {
				t.Fatalf("not idempotent: %q → %q", got, again)
			}
		})
	}
}

func TestRedactLeavesNoSecret(t *testing.T) {
	t.Parallel()
	const secret = "SUPERSECRET0123456789"
	inputs := []string{
		"http://radarr/api/v3/system/status?apikey=" + secret,
		"X-Plex-Token=" + secret,
		`{"authToken":"` + secret + `"}`,
		"Authorization: Bearer " + secret,
		"https://discord.com/api/webhooks/1/" + secret,
		"https://hooks.slack.com/services/" + secret,
		"https://api.telegram.org/bot123456:" + secret + "/getMe",
		"https://user:" + secret + "@host/",
		`Get "http://sonarr:8989/api/v3/series?apikey=` + secret + `": dial tcp: connection refused`,
	}
	for _, in := range inputs {
		if got := Redact(in); strings.Contains(got, secret) || !strings.Contains(got, Removed) {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
}

func TestSensitiveKey(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]bool{
		"apiKey": true, "api_key": true, "X-Api-Key": true, "token": true, "plexToken": true,
		"authToken": true, "password": true, "PasswordConfirmation": true, "secret": true,
		"authorization": true, "cookie": true, "auth": true, "pwd": true, "clientSecret": true,
		"key": false, "ratingKey": false, "path": false, "author": false, "component": false, "": false,
	} {
		if got := sensitiveKey(key); got != want {
			t.Errorf("sensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func BenchmarkRedactNoSecret(b *testing.B) {
	s := "Scan finished for library Movies: 1234 items, 12 duplicate groups, 3.4 GB reclaimable"
	for b.Loop() {
		_ = Redact(s)
	}
}

func BenchmarkRedactURL(b *testing.B) {
	s := "GET http://plex:32400/library/sections/1/all?type=1&X-Plex-Token=abcdef&includeGuids=1"
	for b.Loop() {
		_ = Redact(s)
	}
}

// TestRedactMediaBrowserAuthorization: Jellyfin's credential header ("MediaBrowser Token=\"…\"")
// is masked in every form a log line can hold it: plain, as a Go http.Header dump, as JSON, and
// the legacy X-Emby-Authorization; no parameter survives a quote, and redacting twice changes
// nothing.
func TestRedactMediaBrowserAuthorization(t *testing.T) {
	t.Parallel()
	const key = "0123456789abcdef0123456789abcdef"
	for _, in := range []string{
		`Authorization: MediaBrowser Token="` + key + `", Client="Dupearr", Device="Dupearr", DeviceId="d", Version="0.3.0"`,
		`map[Accept:[application/json] Authorization:[MediaBrowser Token="` + key + `", Client="Dupearr"]]`,
		`{"Authorization":"MediaBrowser Token=\"` + key + `\", Client=\"Dupearr\""}`,
		`X-Emby-Authorization: Emby Token="` + key + `"`,
		`authorization: mediabrowser token=` + key,
	} {
		got := Redact(in)
		if strings.Contains(got, key) || !strings.Contains(got, Removed) {
			t.Errorf("Redact(%q) = %q", in, got)
		}
		if again := Redact(got); again != got {
			t.Errorf("Redact is not idempotent: %q → %q", got, again)
		}
	}
}
