package plex

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// plexTVOptions points both plex.tv hosts at the fake server.
func plexTVOptions(f *fakeServer) Options {
	o := testOptions(f.Client())
	o.PlexTVURL = f.URL + "/"
	o.ClientsPlexTVURL = f.URL
	return o
}

func TestCreatePin(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 201, `{"id":564964751,"code":"8lzjqnq8lye02n52jq3fqxf8e","product":"Dupearr","trusted":false,
		  "clientIdentifier":"test-client-id","expiresIn":1800,"createdAt":"2026-09-22T12:00:00Z",
		  "expiresAt":"2026-09-22T12:30:00Z","authToken":null,"newRegistration":null}`)
	})
	pin, err := CreatePin(context.Background(), plexTVOptions(f))
	if err != nil {
		t.Fatal(err)
	}
	want := &Pin{ID: 564964751, Code: "8lzjqnq8lye02n52jq3fqxf8e", ExpiresAt: time.Date(2026, 9, 22, 12, 30, 0, 0, time.UTC)}
	if !reflect.DeepEqual(pin, want) {
		t.Errorf("pin = %+v, want %+v", pin, want)
	}
	r := f.requests()[0]
	if r.Method != http.MethodPost || r.Path != "/api/v2/pins" || r.Query.Get("strong") != "true" {
		t.Errorf("request = %s %s?%s", r.Method, r.Path, r.RawQ)
	}
	if r.Header.Get("X-Plex-Client-Identifier") != testClientID || r.Header.Get("X-Plex-Product") != "Dupearr" ||
		r.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", r.Header)
	}
	if r.Header.Get("X-Plex-Token") != "" {
		t.Error("CreatePin must not send a token")
	}
}

func TestCreatePinExpiresInFallbackAndStringID(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 201, `{"id":"42","code":"abcd","expiresIn":"900"}`)
	})
	before := time.Now()
	pin, err := CreatePin(context.Background(), plexTVOptions(f))
	if err != nil {
		t.Fatal(err)
	}
	if pin.ID != 42 || pin.Code != "abcd" {
		t.Errorf("pin = %+v", pin)
	}
	if pin.ExpiresAt.Before(before.Add(899*time.Second)) || pin.ExpiresAt.After(time.Now().Add(901*time.Second)) {
		t.Errorf("ExpiresAt = %v, want ~now+900s", pin.ExpiresAt)
	}
}

func TestCreatePinErrors(t *testing.T) {
	if _, err := CreatePin(context.Background(), Options{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("missing client id: err = %v", err)
	}
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"no code", 201, `{"id":1}`},
		{"html", 200, `<html>maintenance</html>`},
		{"429", 429, `{"errors":[{"code":1,"message":"slow down"}]}`},
		{"empty", 201, ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tt.status, tt.body) })
			if _, err := CreatePin(context.Background(), plexTVOptions(f)); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCheckPin(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		token string
	}{
		{"pending", `{"id":564964751,"code":"8lzjq","authToken":null}`, ""},
		{"claimed", `{"id":564964751,"code":"8lzjq","authToken":"user-token-xyz"}`, "user-token-xyz"},
		{"xml attributes", `<?xml version="1.0" encoding="UTF-8"?><pin id="564964751" code="8lzjq" authToken="user-token-xyz"/>`, "user-token-xyz"},
		{"xml legacy elements", `<pin><id type="integer">564964751</id><code>8lzjq</code><auth-token>user-token-xyz</auth-token></pin>`, "user-token-xyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, tt.body) })
			pin, err := CheckPin(context.Background(), plexTVOptions(f), 564964751)
			if err != nil {
				t.Fatal(err)
			}
			if pin.ID != 564964751 || pin.Code != "8lzjq" || pin.AuthToken != tt.token {
				t.Errorf("pin = %+v", pin)
			}
			r := f.requests()[0]
			if r.Method != http.MethodGet || r.Path != "/api/v2/pins/564964751" {
				t.Errorf("request = %s %s", r.Method, r.Path)
			}
			if r.Header.Get("X-Plex-Client-Identifier") != testClientID {
				t.Error("CheckPin must send the same client identifier")
			}
		})
	}
}

func TestCheckPinErrors(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, `{"errors":[{"code":1020,"message":"Code not found or expired"}]}`)
	})
	o := plexTVOptions(f)
	if _, err := CheckPin(context.Background(), o, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired pin: err = %v, want ErrNotFound", err)
	}
	if _, err := CheckPin(context.Background(), o, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("id 0: err = %v", err)
	}
	o.ClientIdentifier = ""
	if _, err := CheckPin(context.Background(), o, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("no client id: err = %v", err)
	}
	if f.count() != 1 {
		t.Errorf("requests = %d, want 1", f.count())
	}
}

func TestAuthURL(t *testing.T) {
	got := AuthURL(Options{ClientIdentifier: "abc-123", Product: "Dupearr"}, "8lzjqnq8lye02n52jq3fqxf8e")
	want := "https://app.plex.tv/auth#?clientID=abc-123&code=8lzjqnq8lye02n52jq3fqxf8e&context%5Bdevice%5D%5Bproduct%5D=Dupearr"
	if got != want {
		t.Errorf("AuthURL =\n%s\nwant\n%s", got, want)
	}
	// Values are escaped inside the fragment; spaces as %20.
	got = AuthURL(Options{ClientIdentifier: "a&b=c", Product: "My App"}, " co de ")
	if !strings.Contains(got, "clientID=a%26b%3Dc") || !strings.Contains(got, "code=co%20de") ||
		!strings.HasSuffix(got, "context%5Bdevice%5D%5Bproduct%5D=My%20App") {
		t.Errorf("AuthURL escaping = %s", got)
	}
	u, err := url.Parse(got)
	if err != nil || u.Host != "app.plex.tv" || u.Path != "/auth" {
		t.Errorf("AuthURL does not parse as app.plex.tv/auth: %v", err)
	}
}

const resourcesJSON = `[
  { "name": "Tower", "product": "Plex Media Server", "productVersion": "1.43.4.10903-abc", "platform": "Linux",
    "provides": "server", "clientIdentifier": "0123456789abcdef", "owned": true, "accessToken": "srv-token-1",
    "httpsRequired": false, "presence": true,
    "connections": [
      { "protocol": "https", "address": "192.168.1.10", "port": 32400,
        "uri": "https://192-168-1-10.0123456789abcdef.plex.direct:32400", "local": true, "relay": false, "IPv6": false },
      { "protocol": "https", "address": "203.0.113.5", "port": "32400",
        "uri": "https://203-0-113-5.0123456789abcdef.plex.direct:32400", "local": "0", "relay": "1" } ] },
  { "name": "Friend's Server", "provides": "server,client", "clientIdentifier": "fedcba", "owned": "0",
    "accessToken": "srv-token-2", "productVersion": "1.40.0", "platform": "Windows", "connections": [] },
  { "name": "iPhone", "product": "Plex for iOS", "provides": "client,player", "clientIdentifier": "phone",
    "accessToken": "ignored" },
  { "name": "Plexamp", "provides": "player,pubsub-player", "clientIdentifier": "amp" }
]`

func TestServers(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, resourcesJSON) })
	res, err := Servers(context.Background(), plexTVOptions(f), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	want := []Resource{
		{Name: "Tower", ClientIdentifier: "0123456789abcdef", ProductVersion: "1.43.4.10903-abc", Platform: "Linux",
			Owned: true, AccessToken: "srv-token-1", Connections: []Connection{
				{URI: "https://192-168-1-10.0123456789abcdef.plex.direct:32400", Address: "192.168.1.10", Port: 32400, Protocol: "https", Local: true},
				{URI: "https://203-0-113-5.0123456789abcdef.plex.direct:32400", Address: "203.0.113.5", Port: 32400, Protocol: "https", Relay: true},
			}},
		{Name: "Friend's Server", ClientIdentifier: "fedcba", ProductVersion: "1.40.0", Platform: "Windows",
			AccessToken: "srv-token-2", Connections: []Connection{}},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("Servers =\n%+v\nwant\n%+v", res, want)
	}
	r := f.requests()[0]
	if r.Method != http.MethodGet || r.Path != "/api/v2/resources" {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
	for _, k := range []string{"includeHttps", "includeRelay", "includeIPv6"} {
		if r.Query.Get(k) != "1" {
			t.Errorf("query %s = %q, want 1", k, r.Query.Get(k))
		}
	}
	if r.Header.Get("X-Plex-Token") != "user-token" || strings.Contains(r.RawQ, "user-token") {
		t.Error("the plex.tv token must be sent as a header only")
	}
}

func TestServersAlternativeShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"wrapped MediaContainer", `{"MediaContainer":{"size":1,"Device":[{"name":"Tower","provides":"server","clientIdentifier":"abc","owned":1,"accessToken":"t",
			"Connection":[{"protocol":"http","address":"10.0.0.2","port":"32400","uri":"http://10.0.0.2:32400","local":"1"}]}]}}`},
		{"resources key", `{"resources":[{"name":"Tower","provides":"server","clientIdentifier":"abc","owned":true,"accessToken":"t",
			"connections":{"protocol":"http","address":"10.0.0.2","port":32400,"uri":"http://10.0.0.2:32400","local":true}}]}`},
		{"single object", `{"name":"Tower","provides":"server","clientIdentifier":"abc","owned":true,"accessToken":"t","device":"PC",
			"connections":[{"protocol":"http","address":"10.0.0.2","port":32400,"uri":"http://10.0.0.2:32400","local":true}]}`},
		{"xml v1", `<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer size="2">
  <Device name="Tower" product="Plex Media Server" provides="server" clientIdentifier="abc" owned="1" accessToken="t">
    <Connection protocol="http" address="10.0.0.2" port="32400" uri="http://10.0.0.2:32400" local="1"/>
  </Device>
  <Device name="Phone" provides="client,player" clientIdentifier="p"/>
</MediaContainer>`},
		{"xml v2", `<resources><resource name="Tower" provides="server" clientIdentifier="abc" owned="true" accessToken="t">
<connections><connection protocol="http" address="10.0.0.2" port="32400" uri="http://10.0.0.2:32400" local="true" relay="false"/></connections>
</resource></resources>`},
	}
	want := []Resource{{Name: "Tower", ClientIdentifier: "abc", Owned: true, AccessToken: "t", Connections: []Connection{
		{URI: "http://10.0.0.2:32400", Address: "10.0.0.2", Port: 32400, Protocol: "http", Local: true}}}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, tt.body) })
			res, err := Servers(context.Background(), plexTVOptions(f), "user-token")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(res, want) {
				t.Errorf("Servers =\n%+v\nwant\n%+v", res, want)
			}
		})
	}
}

func TestServersErrors(t *testing.T) {
	if _, err := Servers(context.Background(), Options{}, "  "); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty token: err = %v", err)
	}
	t.Run("401", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 401, `{"errors":[{"code":1001,"message":"User could not be authenticated"}]}`)
		})
		if _, err := Servers(context.Background(), plexTVOptions(f), "bad"); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("err = %v, want ErrUnauthorized", err)
		}
	})
	t.Run("garbage never echoes the body", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `[{"name":"x","accessToken":"secret-server-token","connections":[{"port":{]`)
		})
		_, err := Servers(context.Background(), plexTVOptions(f), "user-token")
		if err == nil || strings.Contains(err.Error(), "secret-server-token") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unrecognized", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `"hello"`) })
		if _, err := Servers(context.Background(), plexTVOptions(f), "user-token"); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("empty list", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `[]`) })
		res, err := Servers(context.Background(), plexTVOptions(f), "user-token")
		if err != nil || res == nil || len(res) != 0 {
			t.Errorf("Servers = %#v, %v; want empty non-nil", res, err)
		}
	})
}

func TestPlexTVBaseURLValidation(t *testing.T) {
	o := Options{ClientIdentifier: "x", PlexTVURL: "ftp://plex.tv"}
	if _, err := CreatePin(context.Background(), o); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestPlexTVDefaults(t *testing.T) {
	o := Options{}.withDefaults()
	if o.PlexTVURL != "https://plex.tv" || o.ClientsPlexTVURL != "https://clients.plex.tv" {
		t.Errorf("defaults = %q, %q", o.PlexTVURL, o.ClientsPlexTVURL)
	}
}

func TestProvidesServer(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{{"server", true}, {"client,server", true}, {" Server , player", true}, {"client,player", false}, {"", false}, {"servers", false}} {
		if got := providesServer(tt.in); got != tt.want {
			t.Errorf("providesServer(%q) = %v", tt.in, got)
		}
	}
}

func TestParsePlexTime(t *testing.T) {
	if got := parsePlexTime("2026-09-22T12:30:00.123Z"); !got.Equal(time.Date(2026, 9, 22, 12, 30, 0, 123e6, time.UTC)) {
		t.Errorf("rfc3339 = %v", got)
	}
	if got := parsePlexTime("1790000000"); !got.Equal(time.Unix(1790000000, 0)) {
		t.Errorf("epoch = %v", got)
	}
	if !parsePlexTime("").IsZero() || !parsePlexTime("tomorrow").IsZero() {
		t.Error("unparseable must be zero")
	}
}

func TestOwnerOf(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "user-token" {
			writeJSON(w, 401, `{"errors":[{"code":1001}]}`)
			return
		}
		writeJSON(w, 200, resourcesJSON)
	})
	opts := plexTVOptions(f)
	ctx := context.Background()
	tests := []struct {
		machineID          string
		wantOwned, wantKnw bool
	}{
		{"0123456789abcdef", true, true},
		{"0123456789ABCDEF", true, true}, // machine identifiers compare case-insensitively
		{"fedcba", false, true},          // a shared server
		{"not-listed", false, false},
	}
	for _, tt := range tests {
		owned, known, err := OwnerOf(ctx, opts, "user-token", tt.machineID)
		if err != nil || owned != tt.wantOwned || known != tt.wantKnw {
			t.Errorf("OwnerOf(%s) = %v, %v, %v; want %v, %v", tt.machineID, owned, known, err, tt.wantOwned, tt.wantKnw)
		}
	}
	if _, known, err := OwnerOf(ctx, opts, "other-token", "0123456789abcdef"); !errors.Is(err, ErrUnauthorized) || known {
		t.Errorf("unknown token: known=%v err=%v", known, err)
	}
	if _, _, err := OwnerOf(ctx, opts, "user-token", " "); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("empty machine id: err = %v", err)
	}

	// The client method uses the client's own token and options.
	c := New("http://127.0.0.1:1", "user-token", opts)
	if owned, known, err := c.Ownership(ctx, "fedcba"); err != nil || owned || !known {
		t.Errorf("Ownership = %v, %v, %v", owned, known, err)
	}
}
