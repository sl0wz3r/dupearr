package plex

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Official webhook example (trimmed), extended with library.new episode metadata.
const webhookJSON = `{ "event": "library.new", "user": true, "owner": true,
  "Account": { "id": 1, "thumb": "https://plex.tv/users/1022b120ffbaa/avatar?c=1465525047", "title": "elan" },
  "Server": { "title": "Office", "uuid": "54664a3d8acc39983675640ec9ce00b70af9cc36" },
  "Player": { "local": true, "publicAddress": "200.200.200.200", "title": "Plex Web (Safari)", "uuid": "r6yfkdnfggbh2bdnvkffwbms" },
  "Metadata": { "librarySectionType": "show", "ratingKey": "481485", "key": "/library/metadata/481485",
    "parentRatingKey": "481479", "grandparentRatingKey": "481398", "guid": "plex://episode/5d9c0cd2ffd9ef001e9bc730",
    "librarySectionID": 1224, "type": "episode", "title": "A Grave Mistake", "grandparentTitle": "Hi Hi Puffy AmiYumi",
    "index": 6, "parentIndex": 3, "addedAt": 1711557838 } }`

func checkWebhook(t *testing.T, p *WebhookPayload) {
	t.Helper()
	if p.Event != "library.new" || !p.User || !p.Owner {
		t.Errorf("event/user/owner = %q %v %v", p.Event, p.User, p.Owner)
	}
	if p.Server.UUID != "54664a3d8acc39983675640ec9ce00b70af9cc36" || p.Server.Title != "Office" {
		t.Errorf("server = %+v", p.Server)
	}
	m := p.Metadata
	if m.RatingKey != "481485" || m.Type != "episode" || m.Title != "A Grave Mistake" ||
		m.GrandparentTitle != "Hi Hi Puffy AmiYumi" || m.LibrarySectionID != "1224" {
		t.Errorf("metadata = %+v", m)
	}
	if p.LibrarySectionType != "show" || p.GrandparentRatingKey != "481398" || p.GUID != "plex://episode/5d9c0cd2ffd9ef001e9bc730" {
		t.Errorf("additive fields = %q %q %q", p.LibrarySectionType, p.GrandparentRatingKey, p.GUID)
	}
}

func multipartRequest(t *testing.T, fields map[string]string, withThumb bool) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if withThumb {
		fw, err := mw.CreateFormFile("thumb", "thumb.jpg")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(bytes.Repeat([]byte{0xff, 0xd8}, 50_000))
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/plex", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestParseWebhookMultipart(t *testing.T) {
	for _, thumb := range []bool{false, true} {
		p, err := ParseWebhook(multipartRequest(t, map[string]string{"payload": webhookJSON}, thumb))
		if err != nil {
			t.Fatalf("thumb=%v: %v", thumb, err)
		}
		checkWebhook(t, p)
	}
}

func TestParseWebhookJSONBody(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", ""} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(webhookJSON))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		p, err := ParseWebhook(r)
		if err != nil {
			t.Fatalf("%q: %v", ct, err)
		}
		checkWebhook(t, p)
	}
}

func TestParseWebhookURLEncoded(t *testing.T) {
	body := url.Values{"payload": {webhookJSON}}.Encode()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p, err := ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	checkWebhook(t, p)
}

func TestParseWebhookMinimalMusicEvent(t *testing.T) {
	body := `{"event":"media.play","user":"1","owner":0,"Server":{"title":"Office","uuid":"abc"},
	  "Metadata":{"librarySectionType":"artist","ratingKey":1936545,"librarySectionID":"1224","type":"track","title":"Love"}}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	p, err := ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Event != "media.play" || !p.User || p.Owner || p.Metadata.RatingKey != "1936545" || p.Metadata.Type != "track" {
		t.Errorf("payload = %+v", p)
	}
}

func TestParseWebhookErrors(t *testing.T) {
	tests := []struct {
		name string
		req  func() *http.Request
		want string
	}{
		{"nil request", func() *http.Request { return nil }, "empty request"},
		{"no payload field", func() *http.Request { return multipartRequest(t, map[string]string{"other": "x"}, true) }, `missing "payload"`},
		{"empty payload", func() *http.Request { return multipartRequest(t, map[string]string{"payload": "  "}, false) }, `missing "payload"`},
		{"invalid json", func() *http.Request { return multipartRequest(t, map[string]string{"payload": "{nope"}, false) }, "invalid payload JSON"},
		{"no event", func() *http.Request { return multipartRequest(t, map[string]string{"payload": `{"user":true}`}, false) }, "no event"},
		{"unsupported type", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
			r.Header.Set("Content-Type", "image/jpeg")
			return r
		}, "unsupported content type"},
		{"multipart without boundary", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
			r.Header.Set("Content-Type", "multipart/form-data")
			return r
		}, "boundary"},
		{"payload too large", func() *http.Request {
			big := `{"event":"x","pad":"` + strings.Repeat("a", maxWebhookPayload) + `"}`
			return multipartRequest(t, map[string]string{"payload": big}, false)
		}, "read body"},
		{"json body too large", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat(" ", maxWebhookPayload+10)))
			r.Header.Set("Content-Type", "application/json")
			return r
		}, "read body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWebhook(tt.req())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseWebhookPayloadAfterThumbAndDuplicateField(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("thumb", "t.jpg")
	_, _ = fw.Write([]byte("jpeg"))
	_ = mw.WriteField("payload", webhookJSON)
	_ = mw.WriteField("payload", `{"event":"second"}`) // first one wins
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	p, err := ParseWebhook(r)
	if err != nil {
		t.Fatal(err)
	}
	checkWebhook(t, p)
}
