package plex

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// A JSON body without a MediaContainer (legacy "_children" PMS, a proxy's JSON error page, a
// different API behind the URL) must be an error, never an empty listing, "deletion disabled" or
// — most importantly — "nothing is playing".
func TestResponsesWithoutMediaContainerAreErrors(t *testing.T) {
	bodies := map[string]string{
		"empty object":   `{}`,
		"null container": `{"MediaContainer":null}`,
		"legacy":         `{"_children":[{"_elementType":"Video","ratingKey":"1"}]}`,
		"proxy error":    `{"error":"upstream unavailable","status":200}`,
	}
	calls := map[string]func(c *Client) error{
		"ActiveSessions": func(c *Client) error {
			m, err := c.ActiveSessions(context.Background())
			if m != nil {
				t.Errorf("ActiveSessions returned a map with the error: %v", m)
			}
			return err
		},
		"MediaDeletionAllowed": func(c *Client) error { _, err := c.MediaDeletionAllowed(context.Background()); return err },
		"Sections":             func(c *Client) error { _, err := c.Sections(context.Background()); return err },
		"AllItems": func(c *Client) error {
			_, err := c.AllItems(context.Background(), "1", models.MediaTypeMovie)
			return err
		},
		"Item":     func(c *Client) error { _, err := c.Item(context.Background(), "1049"); return err },
		"Identity": func(c *Client) error { _, err := c.Identity(context.Background()); return err },
	}
	for bname, body := range bodies {
		for cname, call := range calls {
			t.Run(bname+"/"+cname, func(t *testing.T) {
				f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) })
				err := call(newTestClient(f, ""))
				if err == nil {
					t.Fatal("expected an error for a response without MediaContainer")
				}
				if cname != "Identity" && !errors.Is(err, errNoContainer) {
					t.Errorf("err = %v, want errNoContainer", err)
				}
			})
		}
	}
}

// An episode whose season Plex omits (hidden seasons) must never look like Season 0 (specials):
// the engine's show-level key "s<season>e<episode>" would merge it with an unrelated special.
func TestEpisodeUnknownSeasonIsMinusOne(t *testing.T) {
	ep := func(extra string) string {
		return `{"MediaContainer":{"Metadata":[{"ratingKey":"900","type":"episode","title":"Pilot","grandparentTitle":"Show",
		  "guid":"plex://episode/abc"` + extra + `,"Media":[{"id":1,"Part":[{"id":1,"file":"/tv/Show/Show.S01E05.mkv"}]}]}]}}`
	}
	tests := []struct {
		name        string
		extra       string
		season, epi int
	}{
		{"both absent", ``, -1, 0},
		{"season absent", `,"index":5`, -1, 5},
		{"specials season 0 kept", `,"index":5,"parentIndex":0`, 0, 5},
		{"string numbers", `,"index":"5","parentIndex":"2"`, 2, 5},
		{"null season", `,"index":5,"parentIndex":null`, -1, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, routes(map[string]string{
				"/library/metadata/900": ep(tt.extra),
				"/library/sections/2/all": `{"MediaContainer":{"size":1,"totalSize":1,"Metadata":[` +
					strings.TrimSuffix(strings.TrimPrefix(ep(tt.extra), `{"MediaContainer":{"Metadata":[`), `]}}`) + `]}}`,
			}))
			c := newTestClient(f, "")
			item, err := c.Item(context.Background(), "900")
			if err != nil {
				t.Fatal(err)
			}
			if item.Season != tt.season || item.Episode != tt.epi {
				t.Errorf("Item season/episode = %d/%d, want %d/%d", item.Season, item.Episode, tt.season, tt.epi)
			}
			refs, err := c.AllItems(context.Background(), "2", models.MediaTypeEpisode)
			if err != nil || len(refs) != 1 {
				t.Fatalf("AllItems = %+v, %v", refs, err)
			}
			if refs[0].Season != tt.season || refs[0].Episode != tt.epi {
				t.Errorf("ItemRef season/episode = %d/%d, want %d/%d", refs[0].Season, refs[0].Episode, tt.season, tt.epi)
			}
		})
	}
}

// Edition tokens are told apart from title words using the item's titles: a Redux copy named
// "Apocalypse Now Redux (1979)" is an edition of "Apocalypse Now", while an episode titled "The
// Director's Cut" is not an edition.
func TestItemEditionUsesTitles(t *testing.T) {
	movie := `{"MediaContainer":{"Metadata":[{"ratingKey":"7","type":"movie","title":"Apocalypse Now","year":1979,
	  "Media":[
	    {"id":1,"Part":[{"id":1,"file":"/m/Apocalypse Now (1979)/Apocalypse Now (1979) Bluray-1080p.mkv"}]},
	    {"id":2,"Part":[{"id":2,"file":"/m/Apocalypse Now (1979)/Apocalypse Now Redux (1979) Bluray-1080p.mkv"}]}]}]}}`
	finalCut := `{"MediaContainer":{"Metadata":[{"ratingKey":"8","type":"movie","title":"The Final Cut","year":2004,
	  "Media":[{"id":3,"Part":[{"id":3,"file":"/movies/The Final Cut (2004).mkv"}]}]}]}}`
	episode := `{"MediaContainer":{"Metadata":[{"ratingKey":"9","type":"episode","title":"The Director's Cut",
	  "grandparentTitle":"Extended Family","index":5,"parentIndex":1,
	  "Media":[{"id":4,"Part":[{"id":4,"file":"/tv/Extended Family (2023)/Season 01/Extended Family (2023) - S01E05 - The Director's Cut.mkv"}]}]}]}}`
	f := newFakeServer(t, routes(map[string]string{
		"/library/metadata/7": movie, "/library/metadata/8": finalCut, "/library/metadata/9": episode,
	}))
	c := newTestClient(f, "")
	get := func(rk string) *models.MediaItem {
		t.Helper()
		it, err := c.Item(context.Background(), rk)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	m := get("7")
	if m.Versions[0].Edition != "" || m.Versions[1].Edition != "Redux" {
		t.Errorf("editions = %q / %q, want \"\" / \"Redux\" (a different cut must never share the base group)",
			m.Versions[0].Edition, m.Versions[1].Edition)
	}
	if e := get("8").Versions[0].Edition; e != "" {
		t.Errorf("The Final Cut (2004) edition = %q, want none (title word)", e)
	}
	if e := get("9").Versions[0].Edition; e != "" {
		t.Errorf("episode edition = %q, want none (episode/show title words)", e)
	}
}

// Plex webhook Metadata carries both "guid" and a "Guid" array; encoding/json folds keys
// case-insensitively, so the array must not blank the guid.
func TestParseWebhookGuidArrayDoesNotClobberGuid(t *testing.T) {
	for _, body := range []string{
		`{"event":"library.new","Metadata":{"ratingKey":"1","type":"movie","guid":"plex://movie/abc",
		  "Guid":[{"id":"imdb://tt0196229"},{"id":"tmdb://9398"}]}}`,
		`{"event":"library.new","Metadata":{"ratingKey":"1","type":"movie",
		  "Guid":[{"id":"imdb://tt0196229"},{"id":"tmdb://9398"}],"guid":"plex://movie/abc"}}`,
	} {
		r, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		p, err := ParseWebhook(r)
		if err != nil {
			t.Fatal(err)
		}
		if p.GUID != "plex://movie/abc" {
			t.Errorf("GUID = %q, want plex://movie/abc", p.GUID)
		}
		want := map[string]string{"imdb": "tt0196229", "tmdb": "9398", "plex": "plex://movie/abc"}
		if !reflect.DeepEqual(p.ExternalIDs, want) {
			t.Errorf("ExternalIDs = %v, want %v", p.ExternalIDs, want)
		}
	}
	r, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"event":"media.play"}`))
	p, err := ParseWebhook(r)
	if err != nil || p.ExternalIDs == nil {
		t.Errorf("ExternalIDs must be non-nil: %#v, %v", p, err)
	}
}

// Clients built per call (the factories do that) share one transport per TLS mode, so idle
// keep-alive connections are reused instead of piling up; a caller-supplied client is kept.
func TestClientsShareTransports(t *testing.T) {
	a := New("http://a.invalid", "t", Options{VerifyTLS: true})
	b := New("http://b.invalid", "t", Options{VerifyTLS: true, Timeout: time.Second})
	insecure := New("http://c.invalid", "t", Options{VerifyTLS: false})
	if a.hc.Transport == nil || a.hc.Transport != b.hc.Transport {
		t.Error("verifying clients must share one transport")
	}
	if insecure.hc.Transport == a.hc.Transport {
		t.Error("an insecure client must not share the verifying transport")
	}
	tr, ok := insecure.hc.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("VerifyTLS=false transport must skip verification")
	}
	if vt := a.hc.Transport.(*http.Transport); vt.TLSClientConfig.InsecureSkipVerify {
		t.Error("VerifyTLS=true transport must verify certificates")
	}
	if b.hc.Timeout != time.Second || a.hc.Timeout != defaultTimeout {
		t.Errorf("timeouts = %v / %v", a.hc.Timeout, b.hc.Timeout)
	}
	custom := &http.Client{Transport: &http.Transport{}}
	c := New("http://d.invalid", "t", Options{HTTPClient: custom})
	if c.hc.Transport != custom.Transport || c.hc == custom {
		t.Error("a caller-supplied client's transport is used (the client itself is copied)")
	}
	if c.hc.CheckRedirect == nil || custom.CheckRedirect != nil {
		t.Error("the copy gets the token-safe redirect policy; the caller's client is not modified")
	}
}
