package notifications

import (
	"context"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// sendDirect delivers a message synchronously through the same path Notify uses.
// asProviderHost makes every address count as a provider's public endpoint for the rest of the
// test, so the provider's error message (withheld for other addresses) can be checked.
func asProviderHost(t *testing.T) {
	t.Helper()
	prev := providerHost
	providerHost = func(string) bool { return true }
	t.Cleanup(func() { providerHost = prev })
}

func sendDirect(t *testing.T, s *Service, cfg models.NotificationConfig, m Message) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.deliver(ctx, cfg, m)
}

func assertJSONRequest(t *testing.T, r recordedRequest, method, path string) {
	t.Helper()
	if r.Method != method || r.Path != path {
		t.Fatalf("request = %s %s, want %s %s", r.Method, r.Path, method, path)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ua := r.Header.Get("User-Agent"); ua != version.UserAgent() {
		t.Errorf("User-Agent = %q", ua)
	}
}

func assertNoLeak(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

func TestDiscord(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusNoContent, "")
	s, _, _ := newTestService(t)
	s.SetInstanceName("Dupearr 4K")
	cfg := newConfig(KindDiscord, map[string]any{
		"webhookUrl": srv.URL + "/api/webhooks/123/tok-secret",
		"username":   "Dupearr Bot",
		"avatarUrl":  "https://example.com/a.png",
	})
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPost, "/api/webhooks/123/tok-secret")
	body := decodeBody(t, r.Body)
	if body["username"] != "Dupearr Bot" || body["avatar_url"] != "https://example.com/a.png" {
		t.Errorf("username/avatar = %v / %v", body["username"], body["avatar_url"])
	}
	if am := body["allowed_mentions"].(map[string]any); len(am["parse"].([]any)) != 0 {
		t.Errorf("allowed_mentions = %v", am)
	}
	embed := body["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != `3 duplicates found \<Movie & Co\>` { // Markdown-escaped (SEC-017)
		t.Errorf("title = %v", embed["title"])
	}
	if embed["color"] != float64(discordColorWarning) {
		t.Errorf("color = %v", embed["color"])
	}
	if embed["url"] != "https://dupearr.example.com/duplicates?status=pending" {
		t.Errorf("url = %v", embed["url"])
	}
	if embed["timestamp"] != "2026-09-22T12:30:00Z" {
		t.Errorf("timestamp = %v", embed["timestamp"])
	}
	if embed["footer"].(map[string]any)["text"] != "Dupearr 4K" {
		t.Errorf("footer = %v", embed["footer"])
	}
	fields := embed["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("fields = %v", fields)
	}
	f0 := fields[0].(map[string]any)
	if f0["name"] != "Library" || f0["value"] != "Movies" || f0["inline"] != true {
		t.Errorf("field = %v", f0)
	}
}

func TestDiscordColorsAndLimits(t *testing.T) {
	set, _ := parseSettings(mustSchema(t, KindDiscord), nil)
	for sev, want := range map[string]int{SeverityInfo: discordColorInfo, SeverityWarning: discordColorWarning, SeverityError: discordColorError} {
		p := buildDiscordPayload(set, normalizeMessage(Message{Title: "t", Severity: sev}), "D", fixedNow)
		if p.Embeds[0].Color != want {
			t.Errorf("%s colour = %x, want %x", sev, p.Embeds[0].Color, want)
		}
	}
	m := Message{Title: strings.Repeat("T", 300), Body: strings.Repeat("b", 5000)}
	for i := 0; i < 40; i++ {
		m.Fields = append(m.Fields, Field{Name: "Name", Value: strings.Repeat("v", 1500)})
	}
	m.Fields = append(m.Fields, Field{Name: "empty"})
	p := buildDiscordPayload(set, normalizeMessage(m), "D", fixedNow)
	e := p.Embeds[0]
	total := len([]rune(e.Title)) + len([]rune(e.Description)) + len([]rune(e.Footer.Text))
	for _, f := range e.Fields {
		total += len([]rune(f.Name)) + len([]rune(f.Value))
		if f.Name == "" || f.Value == "" || len([]rune(f.Value)) > discordMaxFieldValue {
			t.Errorf("invalid field %q", f.Name)
		}
	}
	if len(e.Fields) > discordMaxFields || total > discordMaxEmbedTotal || len([]rune(e.Title)) > discordMaxTitle {
		t.Fatalf("limits exceeded: %d fields, %d chars", len(e.Fields), total)
	}
	if last := e.Fields[len(e.Fields)-1]; !strings.Contains(last.Value, "omitted") {
		t.Errorf("missing omitted marker, last field %+v", last)
	}
}

func mustSchema(t *testing.T, kind string) ProviderSchema {
	t.Helper()
	p, ok := providerSchema(kind)
	if !ok {
		t.Fatalf("no schema for %s", kind)
	}
	return p
}

func TestDiscordErrorIsRedacted(t *testing.T) {
	srv, _ := newRecorder(t, http.StatusNotFound, `{"message": "Unknown Webhook", "code": 10015}`)
	s, _, _ := newTestService(t)
	cfg := newConfig(KindDiscord, map[string]any{"webhookUrl": srv.URL + "/api/webhooks/123/tok-secret"})
	// The test server is not discord.com, so its message is withheld (see
	// TestDeliveryWithholdsResponseBodiesOfOtherAddresses); the status and provider remain.
	err := sendDirect(t, s, cfg, sampleMessage())
	assertNoLeak(t, err, "tok-secret", "/api/webhooks")
	if !strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "Unknown Webhook") || !strings.HasPrefix(err.Error(), "Discord: ") {
		t.Fatalf("err = %v", err)
	}
}

func TestSlack(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "ok")
	s, _, _ := newTestService(t)
	cfg := newConfig(KindSlack, map[string]any{"webhookUrl": srv.URL + "/services/T0/B0/xyz"})
	m := sampleMessage()
	m.Severity = SeverityError
	if err := sendDirect(t, s, cfg, m); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPost, "/services/T0/B0/xyz")
	body := decodeBody(t, r.Body)
	if !strings.Contains(body["text"].(string), "&lt;Movie &amp; Co&gt;") {
		t.Errorf("fallback text not escaped: %v", body["text"])
	}
	blocks := body["blocks"].([]any)
	types := []string{}
	for _, b := range blocks {
		types = append(types, b.(map[string]any)["type"].(string))
	}
	if strings.Join(types, ",") != "header,section,section,section,context" {
		t.Fatalf("block types = %v", types)
	}
	header := blocks[0].(map[string]any)["text"].(map[string]any)
	if header["type"] != "plain_text" || header["text"] != "3 duplicates found <Movie & Co>" {
		t.Errorf("header = %v", header)
	}
	fields := blocks[2].(map[string]any)["fields"].([]any)
	if fields[0].(map[string]any)["text"] != "*Library*\nMovies" {
		t.Errorf("field = %v", fields[0])
	}
	link := blocks[3].(map[string]any)["text"].(map[string]any)["text"]
	if link != "<https://dupearr.example.com/duplicates?status=pending|Open in Dupearr>" {
		t.Errorf("link = %v", link)
	}
	ctx := blocks[4].(map[string]any)["elements"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(ctx, ":rotating_light: Error") {
		t.Errorf("context = %v", ctx)
	}
}

func TestSlackManyFields(t *testing.T) {
	m := Message{Title: "t"}
	for i := 0; i < 55; i++ {
		m.Fields = append(m.Fields, Field{Name: "n", Value: "v"})
	}
	p := buildSlackPayload(normalizeMessage(m), "D")
	sections := 0
	for _, b := range p.Blocks {
		if len(b.Fields) > slackFieldsPerBlock {
			t.Fatalf("section with %d fields", len(b.Fields))
		}
		if len(b.Fields) > 0 {
			sections++
		}
	}
	if sections != slackMaxFieldBlocks || len(p.Blocks) > 50 {
		t.Fatalf("sections = %d, blocks = %d", sections, len(p.Blocks))
	}
}

func TestSlackError(t *testing.T) {
	asProviderHost(t)
	srv, _ := newRecorder(t, http.StatusForbidden, "invalid_token")
	s, _, _ := newTestService(t)
	err := sendDirect(t, s, newConfig(KindSlack, map[string]any{"webhookUrl": srv.URL + "/services/T/B/sekrit"}), sampleMessage())
	assertNoLeak(t, err, "sekrit")
	if !strings.Contains(err.Error(), "invalid_token") {
		t.Fatalf("err = %v", err)
	}
}

func TestTelegram(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, `{"ok":true,"result":{}}`)
	s, _, _ := newTestService(t)
	s.telegramAPI = srv.URL
	cfg := newConfig(KindTelegram, map[string]any{
		"botToken": "123456:ABC-def", "chatId": "-1001234", "topicId": 42, "sendSilently": true,
	})
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPost, "/bot123456:ABC-def/sendMessage")
	body := decodeBody(t, r.Body)
	if body["chat_id"] != "-1001234" || body["parse_mode"] != "HTML" || body["message_thread_id"] != float64(42) || body["disable_notification"] != true {
		t.Errorf("body = %v", body)
	}
	if lp := body["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Errorf("link_preview_options = %v", lp)
	}
	text := body["text"].(string)
	for _, want := range []string{
		"<b>⚠️ 3 duplicates found &lt;Movie &amp; Co&gt;</b>",
		"Scan of &#34;Movies&#34; found 3 new duplicate groups.",
		"<b>Library:</b> Movies",
		`<a href="https://dupearr.example.com/duplicates?status=pending">Open in Dupearr</a>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
}

func TestTelegramErrors(t *testing.T) {
	token := "123456:SECRETtoken"
	t.Run("api error", func(t *testing.T) {
		srv, _ := newRecorder(t, http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
		s, _, _ := newTestService(t)
		s.telegramAPI = srv.URL
		err := s.Test(context.Background(), newConfig(KindTelegram, map[string]any{"botToken": token, "chatId": "1"}))
		assertNoLeak(t, err, token, "SECRETtoken")
		if !strings.Contains(err.Error(), "chat not found") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ok false with 200", func(t *testing.T) {
		srv, _ := newRecorder(t, http.StatusOK, `{"ok":false,"description":"nope"}`)
		s, _, _ := newTestService(t)
		s.telegramAPI = srv.URL
		err := s.Test(context.Background(), newConfig(KindTelegram, map[string]any{"botToken": token, "chatId": "1"}))
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		srv, _ := newRecorder(t, http.StatusOK, "")
		srv.Close()
		s, _, _ := newTestService(t)
		s.telegramAPI = srv.URL
		err := s.Test(context.Background(), newConfig(KindTelegram, map[string]any{"botToken": token, "chatId": "1"}))
		assertNoLeak(t, err, token, "SECRETtoken", "/bot")
	})
	t.Run("unchecked token never used", func(t *testing.T) {
		srv, rec := newRecorder(t, http.StatusOK, `{"ok":true}`)
		s, _, _ := newTestService(t)
		s.telegramAPI = srv.URL
		cfg := newConfig(KindTelegram, map[string]any{"botToken": "1:a/../../evil?x=", "chatId": "1"})
		if err := sendDirect(t, s, cfg, sampleMessage()); err == nil {
			t.Fatal("expected validation error")
		}
		if len(rec.requests()) != 0 {
			t.Fatal("request sent with an invalid token")
		}
	})
}

func TestPushover(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, `{"status":1,"request":"abc"}`)
	s, _, _ := newTestService(t)
	s.pushoverAPI = srv.URL + "/1/messages.json"
	cfg := newConfig(KindPushover, map[string]any{
		"appToken": "apptoken123", "userKey": "userkey456", "devices": "phone, tablet",
		"priority": "2", "sound": "magic", "retry": 120, "expire": 600,
	})
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	if r.Method != http.MethodPost || r.Path != "/1/messages.json" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("request = %s %s %s", r.Method, r.Path, r.Header.Get("Content-Type"))
	}
	form, err := url.ParseQuery(string(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"token": "apptoken123", "user": "userkey456", "device": "phone,tablet", "priority": "2",
		"sound": "magic", "retry": "120", "expire": "600", "html": "1",
		"title": "3 duplicates found <Movie & Co>", "url": "https://dupearr.example.com/duplicates?status=pending",
		"url_title": "Open in Dupearr", "timestamp": "1790080200",
	}
	for k, v := range want {
		if form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, form.Get(k), v)
		}
	}
	msg := form.Get("message")
	if !strings.Contains(msg, "<b>Library:</b> Movies") || !strings.Contains(msg, "&#34;Movies&#34;") {
		t.Errorf("message = %q", msg)
	}
	if len([]rune(msg)) > pushoverMaxMessage {
		t.Errorf("message too long: %d", len([]rune(msg)))
	}
}

func TestPushoverNormalPriorityOmitsRetry(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, `{"status":1}`)
	s, _, _ := newTestService(t)
	s.pushoverAPI = srv.URL
	cfg := newConfig(KindPushover, map[string]any{"appToken": "a1b2c3", "userKey": "u1v2w3"})
	if err := sendDirect(t, s, cfg, Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(string(rec.only(t).Body))
	if form.Get("priority") != "0" || form.Has("retry") || form.Has("expire") || form.Has("device") || form.Has("url") {
		t.Fatalf("form = %v", form)
	}
}

func TestPushoverError(t *testing.T) {
	srv, _ := newRecorder(t, http.StatusBadRequest, `{"token":"invalid","errors":["application token is invalid"],"status":0}`)
	s, _, _ := newTestService(t)
	s.pushoverAPI = srv.URL
	err := s.Test(context.Background(), newConfig(KindPushover, map[string]any{"appToken": "apptokenSECRET", "userKey": "userkeySECRET"}))
	assertNoLeak(t, err, "apptokenSECRET", "userkeySECRET")
	if !strings.Contains(err.Error(), "application token is invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestGotify(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, `{"id":1}`)
	s, _, _ := newTestService(t)
	cfg := newConfig(KindGotify, map[string]any{"serverUrl": srv.URL + "/gotify/", "appToken": "gotifySECRET", "priority": 8})
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPost, "/gotify/message")
	if r.Header.Get("X-Gotify-Key") != "gotifySECRET" || r.RawQuery != "" {
		t.Errorf("auth header %q, query %q", r.Header.Get("X-Gotify-Key"), r.RawQuery)
	}
	body := decodeBody(t, r.Body)
	if body["title"] != "3 duplicates found <Movie & Co>" || body["priority"] != float64(8) {
		t.Errorf("body = %v", body)
	}
	if !strings.Contains(body["message"].(string), "Library: Movies") {
		t.Errorf("message = %v", body["message"])
	}
	extras := body["extras"].(map[string]any)
	click := extras["client::notification"].(map[string]any)["click"].(map[string]any)["url"]
	if click != "https://dupearr.example.com/duplicates?status=pending" {
		t.Errorf("click = %v", click)
	}
	if extras["client::display"].(map[string]any)["contentType"] != "text/plain" {
		t.Errorf("display = %v", extras["client::display"])
	}
}

func TestGotifyDefaultPriorityAndError(t *testing.T) {
	asProviderHost(t)
	srv, rec := newRecorder(t, http.StatusUnauthorized, `{"error":"Unauthorized","errorCode":401,"errorDescription":"you need to provide a valid access token"}`)
	s, _, _ := newTestService(t)
	err := sendDirect(t, s, newConfig(KindGotify, map[string]any{"serverUrl": srv.URL, "appToken": "tokenSECRET"}), Message{Title: "t"})
	assertNoLeak(t, err, "tokenSECRET")
	if !strings.Contains(err.Error(), "valid access token") {
		t.Fatalf("err = %v", err)
	}
	if body := decodeBody(t, rec.only(t).Body); body["priority"] != float64(5) {
		t.Errorf("default priority = %v", body["priority"])
	}
}

func TestNtfy(t *testing.T) {
	tests := []struct {
		name     string
		set      map[string]any
		sev      string
		wantAuth string
		wantTags []string
		wantPrio any
	}{
		{
			name:     "token",
			set:      map[string]any{"topic": "dupearr", "accessToken": "tk_SECRET", "priority": "4", "tags": "movie_camera, dupearr"},
			sev:      SeverityError,
			wantAuth: "Bearer tk_SECRET",
			wantTags: []string{"rotating_light", "movie_camera", "dupearr"},
			wantPrio: float64(4),
		},
		{
			name:     "basic auth",
			set:      map[string]any{"topic": "dupearr", "username": "phil", "password": "pw"},
			sev:      SeverityInfo,
			wantAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("phil:pw")),
			wantPrio: float64(3),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := newRecorder(t, http.StatusOK, `{"id":"x"}`)
			s, _, _ := newTestService(t)
			tt.set["serverUrl"] = srv.URL
			m := sampleMessage()
			m.Severity = tt.sev
			if err := sendDirect(t, s, newConfig(KindNtfy, tt.set), m); err != nil {
				t.Fatal(err)
			}
			r := rec.only(t)
			assertJSONRequest(t, r, http.MethodPost, "/")
			if got := r.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			body := decodeBody(t, r.Body)
			if body["topic"] != "dupearr" || body["priority"] != tt.wantPrio || body["click"] != "https://dupearr.example.com/duplicates?status=pending" {
				t.Errorf("body = %v", body)
			}
			var tags []string
			if raw, ok := body["tags"].([]any); ok {
				for _, x := range raw {
					tags = append(tags, x.(string))
				}
			}
			if strings.Join(tags, ",") != strings.Join(tt.wantTags, ",") {
				t.Errorf("tags = %v, want %v", tags, tt.wantTags)
			}
		})
	}
}

func TestNtfyError(t *testing.T) {
	asProviderHost(t)
	srv, _ := newRecorder(t, http.StatusForbidden, `{"code":40301,"http":403,"error":"forbidden","link":"https://ntfy.sh/docs/publish/#authentication"}`)
	s, _, _ := newTestService(t)
	err := sendDirect(t, s, newConfig(KindNtfy, map[string]any{"serverUrl": srv.URL, "topic": "t", "accessToken": "tk_SECRET"}), sampleMessage())
	assertNoLeak(t, err, "tk_SECRET")
	if !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err = %v", err)
	}
}

func TestApprise(t *testing.T) {
	t.Run("stateful", func(t *testing.T) {
		srv, rec := newRecorder(t, http.StatusOK, "Notification(s) sent.")
		s, _, _ := newTestService(t)
		cfg := newConfig(KindApprise, map[string]any{"serverUrl": srv.URL + "/apprise", "configKey": "dupearr", "tag": "admins", "username": "u", "password": "p"})
		m := sampleMessage()
		m.Severity = SeverityError
		if err := sendDirect(t, s, cfg, m); err != nil {
			t.Fatal(err)
		}
		r := rec.only(t)
		assertJSONRequest(t, r, http.MethodPost, "/apprise/notify/dupearr")
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p")) {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		body := decodeBody(t, r.Body)
		if body["type"] != "failure" || body["format"] != "text" || body["tag"] != "admins" || body["title"] != "3 duplicates found <Movie & Co>" {
			t.Errorf("body = %v", body)
		}
		if _, ok := body["urls"]; ok {
			t.Error("stateful request must not send urls")
		}
	})
	t.Run("stateless", func(t *testing.T) {
		srv, rec := newRecorder(t, http.StatusOK, "")
		s, _, _ := newTestService(t)
		cfg := newConfig(KindApprise, map[string]any{"serverUrl": srv.URL, "urls": "discord://a/b\n# comment\n json://host , mailto://x"})
		if err := sendDirect(t, s, cfg, Message{Title: "t", Severity: SeverityWarning}); err != nil {
			t.Fatal(err)
		}
		r := rec.only(t)
		assertJSONRequest(t, r, http.MethodPost, "/notify/")
		body := decodeBody(t, r.Body)
		if body["urls"] != "discord://a/b,json://host,mailto://x" || body["type"] != "warning" {
			t.Errorf("body = %v", body)
		}
	})
	for _, tt := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusNoContent, "", "no configuration"},
		{http.StatusFailedDependency, "", "one or more Apprise notifications failed"},
		{http.StatusBadRequest, "Bad FORMAT", "Bad FORMAT"},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			asProviderHost(t)
			srv, _ := newRecorder(t, tt.status, tt.body)
			s, _, _ := newTestService(t)
			err := sendDirect(t, s, newConfig(KindApprise, map[string]any{"serverUrl": srv.URL, "urls": "pover://userSECRET@tokenSECRET"}), Message{Title: "t"})
			assertNoLeak(t, err, "userSECRET", "tokenSECRET")
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestWebhook(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusAccepted, "")
	s, _, _ := newTestService(t)
	s.SetInstanceName("Dupearr Home")
	cfg := newConfig(KindWebhook, map[string]any{
		"url": srv.URL + "/hook?key=abc", "method": "PUT", "username": "user", "password": "pass",
		"headers": "X-Custom: one\nX-Other: two",
	})
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPut, "/hook")
	if r.RawQuery != "key=abc" || r.Header.Get("X-Custom") != "one" || r.Header.Get("X-Other") != "two" {
		t.Errorf("query %q headers %v", r.RawQuery, r.Header)
	}
	if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pass")) {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	want := `{"eventType":"DuplicatesFound","title":"3 duplicates found \u003cMovie \u0026 Co\u003e","message":"Scan of \"Movies\" found 3 new duplicate groups.","severity":"warning","fields":[{"name":"Library","value":"Movies"},{"name":"Reclaimable","value":"42.1 GB"}],"url":"https://dupearr.example.com/duplicates?status=pending","instanceName":"Dupearr Home","timestamp":"2026-09-22T12:30:00Z"}`
	if string(r.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", r.Body, want)
	}
}

func TestWebhookTestEventAndEmptyFields(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, _, _ := newTestService(t)
	if err := s.Test(context.Background(), newConfig(KindWebhook, map[string]any{"url": srv.URL})); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	assertJSONRequest(t, r, http.MethodPost, "/")
	body := decodeBody(t, r.Body)
	if body["eventType"] != "Test" || body["title"] != TestTitle || body["severity"] != "info" || body["instanceName"] != "Dupearr" {
		t.Errorf("body = %v", body)
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("unexpected Authorization header")
	}

	rec.mu.Lock()
	rec.reqs = nil
	rec.mu.Unlock()
	if err := sendDirect(t, s, newConfig(KindWebhook, map[string]any{"url": srv.URL}), Message{Event: models.OnScanCompleted, Title: "done"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rec.only(t).Body), `"fields":[]`) {
		t.Errorf("fields must be [] not null: %s", rec.only(t).Body)
	}
}

func TestHTTPBehaviour(t *testing.T) {
	t.Run("redirects are not followed", func(t *testing.T) {
		srv, rec := newRecorder(t, http.StatusFound, "")
		rec.headers = map[string]string{"Location": "https://elsewhere.example.com/steal?token=x"}
		s, _, _ := newTestService(t)
		err := sendDirect(t, s, newConfig(KindWebhook, map[string]any{"url": srv.URL + "/hook"}), Message{Title: "t"})
		if err == nil || !strings.Contains(err.Error(), "redirect") || !strings.Contains(err.Error(), "elsewhere.example.com") {
			t.Fatalf("err = %v", err)
		}
		assertNoLeak(t, err, "token=x")
		if len(rec.requests()) != 1 {
			t.Fatalf("requests = %d", len(rec.requests()))
		}
	})
	t.Run("429 retried once", func(t *testing.T) {
		srv, rec := newRecorder(t, 0, "")
		rec.respond = func(w http.ResponseWriter, _ *http.Request, n int) {
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
		s, _, _ := newTestService(t)
		if err := sendDirect(t, s, newConfig(KindDiscord, map[string]any{"webhookUrl": srv.URL + "/x"}), Message{Title: "t"}); err != nil {
			t.Fatal(err)
		}
		if len(rec.requests()) != 2 {
			t.Fatalf("requests = %d", len(rec.requests()))
		}
	})
	t.Run("long 429 not retried", func(t *testing.T) {
		srv, rec := newRecorder(t, http.StatusTooManyRequests, `{"message":"You are being rate limited."}`)
		rec.headers = map[string]string{"Retry-After": "60"}
		s, _, _ := newTestService(t)
		err := sendDirect(t, s, newConfig(KindDiscord, map[string]any{"webhookUrl": srv.URL + "/x"}), Message{Title: "t"})
		if err == nil || !strings.Contains(err.Error(), "429") || len(rec.requests()) != 1 {
			t.Fatalf("err = %v, requests = %d", err, len(rec.requests()))
		}
	})
	t.Run("html error bodies are not echoed", func(t *testing.T) {
		srv, _ := newRecorder(t, http.StatusBadGateway, "<!DOCTYPE html><html><body>proxy error</body></html>")
		s, _, _ := newTestService(t)
		err := sendDirect(t, s, newConfig(KindWebhook, map[string]any{"url": srv.URL}), Message{Title: "t"})
		if err == nil || err.Error() != "Webhook: HTTP 502 Bad Gateway" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("server echoing a secret is redacted", func(t *testing.T) {
		asProviderHost(t)
		srv, _ := newRecorder(t, http.StatusUnauthorized, "bad credentials for user:hunter2-secret")
		s, _, _ := newTestService(t)
		err := sendDirect(t, s, newConfig(KindWebhook, map[string]any{"url": srv.URL, "username": "user", "password": "hunter2-secret"}), Message{Title: "t"})
		assertNoLeak(t, err, "hunter2-secret")
		if !strings.Contains(err.Error(), MaskedValue) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("timeout respected", func(t *testing.T) {
		release := make(chan struct{})
		srv, rec := newRecorder(t, 0, "")
		rec.respond = func(w http.ResponseWriter, r *http.Request, _ int) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		defer close(release)
		s, _, _ := newTestService(t)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := s.Test(ctx, newConfig(KindWebhook, map[string]any{"url": srv.URL + "/slow-SECRET"}))
		if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v", err)
		}
		assertNoLeak(t, err, "slow-SECRET")
		if time.Since(start) > 3*time.Second {
			t.Fatalf("Test took %v", time.Since(start))
		}
	})
}

func TestTestValidatesFirst(t *testing.T) {
	s, _, _ := newTestService(t)
	err := s.Test(context.Background(), newConfig(KindGotify, map[string]any{"serverUrl": "not a url", "appToken": MaskedValue}))
	var vErr *ValidationFailedError
	if !errors.As(err, &vErr) || len(vErr.Errors) != 2 {
		t.Fatalf("err = %v", err)
	}
	if err := (*Service)(nil).Test(context.Background(), newConfig(KindWebhook, validSettings(KindWebhook))); err == nil {
		t.Fatal("nil service Test must fail")
	}
}

func TestDeliverRejectsInvalidStoredConfig(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, _, _ := newTestService(t)
	for _, cfg := range []models.NotificationConfig{
		newConfig(KindWebhook, map[string]any{"url": srv.URL, "password": MaskedValue, "username": "u"}),
		newConfig(KindWebhook, map[string]any{"url": "file:///etc/passwd"}),
		newConfig("unknown", map[string]any{"url": srv.URL}),
		{Kind: KindWebhook, Settings: []byte(`[1]`)},
	} {
		if err := sendDirect(t, s, cfg, Message{Title: "t"}); err == nil {
			t.Errorf("deliver(%s %s) = nil", cfg.Kind, cfg.Settings)
		}
	}
	if n := len(rec.requests()); n != 0 {
		t.Fatalf("%d requests sent for invalid configs", n)
	}
}

// roundTripFunc lets a test intercept outbound requests without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultEndpoints(t *testing.T) {
	tests := []struct {
		name string
		cfg  models.NotificationConfig
		body string
		want string
	}{
		{"telegram", newConfig(KindTelegram, map[string]any{"botToken": "1:abc", "chatId": "5"}), `{"ok":true}`, "https://api.telegram.org/bot1:abc/sendMessage"},
		{"pushover", newConfig(KindPushover, map[string]any{"appToken": "abc", "userKey": "def"}), `{"status":1}`, "https://api.pushover.net/1/messages.json"},
		{"ntfy default server", newConfig(KindNtfy, map[string]any{"topic": "t"}), `{}`, "https://ntfy.sh/"},
		{"ntfy empty server", newConfig(KindNtfy, map[string]any{"topic": "t", "serverUrl": ""}), `{}`, "https://ntfy.sh/"},
		{"ntfy subpath", newConfig(KindNtfy, map[string]any{"topic": "t", "serverUrl": "https://example.com/ntfy"}), `{}`, "https://example.com/ntfy/"},
		{"gotify subpath", newConfig(KindGotify, map[string]any{"serverUrl": "https://example.com/gotify/", "appToken": "x"}), `{}`, "https://example.com/gotify/message"},
		{"apprise stateless", newConfig(KindApprise, map[string]any{"serverUrl": "http://apprise:8000", "urls": "json://x"}), ``, "http://apprise:8000/notify/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := newTestService(t)
			var got string
			s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				got = r.URL.String()
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.body)), Header: http.Header{}, Request: r}, nil
			})
			if err := sendDirect(t, s, tt.cfg, Message{Title: "t"}); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("URL = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTelegramHTMLLimits(t *testing.T) {
	m := Message{Title: strings.Repeat("<t>", 500), Body: strings.Repeat("& body ", 2000), URL: "https://x.example/d"}
	for i := 0; i < 100; i++ {
		m.Fields = append(m.Fields, Field{Name: "n<" + strconv.Itoa(i), Value: strings.Repeat("v", 300)})
	}
	text := telegramHTML(normalizeMessage(m))
	// Telegram counts characters after entity parsing; the raw text is an upper bound.
	if n := len([]rune(text)); n > 4096*2 {
		t.Fatalf("text has %d runes", n)
	}
	plain := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, ""))
	if n := len([]rune(plain)); n > 4096 {
		t.Fatalf("visible text has %d characters", n)
	}
	if !strings.Contains(text, "omitted") || strings.Contains(text, "<t>") {
		t.Fatalf("unexpected text: %.300s", text)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		in     string
		want   time.Duration
		wantOK bool
	}{
		{"", 0, false},
		{"2", 2 * time.Second, true},
		{"0.5", 500 * time.Millisecond, true},
		{"-1", 0, false},
		{"NaN", 0, false},
		{"soon", 0, false},
		// Huge values must clamp, never overflow into a negative (immediate) retry.
		{"1e30", maxRetryAfterHeader, true},
		{"+Inf", maxRetryAfterHeader, true},
		{"99999999999999999999", maxRetryAfterHeader, true},
		{"Mon, 02 Jan 2006 15:04:05 GMT", 0, true}, // in the past
	}
	for _, tt := range tests {
		h := http.Header{}
		if tt.in != "" {
			h.Set("Retry-After", tt.in)
		}
		got, ok := retryAfter(h)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestHugeRetryAfterNotRetried(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusTooManyRequests, "")
	rec.headers = map[string]string{"Retry-After": "1e30"}
	s, _, _ := newTestService(t)
	err := sendDirect(t, s, newConfig(KindWebhook, map[string]any{"url": srv.URL}), Message{Title: "t"})
	if err == nil || len(rec.requests()) != 1 {
		t.Fatalf("err = %v, requests = %d (want an error and no retry)", err, len(rec.requests()))
	}
}

func TestTelegramNonJSONSuccessIsFailure(t *testing.T) {
	srv, _ := newRecorder(t, http.StatusOK, "<html>captive portal</html>")
	s, _, _ := newTestService(t)
	s.telegramAPI = srv.URL
	err := s.Test(context.Background(), newConfig(KindTelegram, map[string]any{"botToken": "123456:ABC", "chatId": "1"}))
	if err == nil || !strings.Contains(err.Error(), "non-JSON") {
		t.Fatalf("err = %v", err)
	}
}
