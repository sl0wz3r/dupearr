package arr

import (
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestWebBase(t *testing.T) {
	cases := []struct {
		name     string
		url, ext string
		want     string
		ok       bool
	}{
		{"connection URL", "http://radarr:7878", "", "http://radarr:7878", true},
		{"connection URL with URL base", "http://10.0.0.5:7878/radarr", "", "http://10.0.0.5:7878/radarr", true},
		{"trailing slashes", "http://radarr:7878/radarr//", "", "http://radarr:7878/radarr", true},
		{"query and fragment dropped", "http://radarr:7878/radarr?x=1#top", "", "http://radarr:7878/radarr", true},
		{"external URL wins", "http://radarr:7878", "https://radarr.example.com/", "https://radarr.example.com", true},
		{"external URL with its own base", "http://radarr:7878/radarr", "https://media.example.com/movies", "https://media.example.com/movies", true},
		{"external URL surrounded by spaces", "http://radarr:7878", "  https://radarr.example.com  ", "https://radarr.example.com", true},
		{"no URL", "", "", "", false},
		{"javascript URL", "javascript:alert(1)", "", "", false},
		{"javascript external URL", "http://radarr:7878", "javascript:alert(1)", "", false},
		{"ftp", "ftp://radarr:7878", "", "", false},
		{"no scheme", "radarr:7878", "", "", false},
		{"no host", "http://", "", "", false},
		{"credentials in URL", "http://user:secret@radarr:7878", "", "", false},
		{"credentials in external URL", "http://radarr:7878", "https://user:secret@radarr.example.com", "", false},
		{"bad port", "http://radarr:99999", "", "", false},
		// A restored External URL that is unusable never falls back to the connection URL.
		{"invalid external URL gives no links", "http://radarr:7878", "data:text/html,hi", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WebBase(models.ArrInstance{Kind: models.ArrRadarr, URL: tc.url, ExternalURL: tc.ext, APIKey: testAPIKey})
			if got != tc.want || ok != tc.ok {
				t.Fatalf("WebBase(%q, %q) = %q, %v; want %q, %v", tc.url, tc.ext, got, ok, tc.want, tc.ok)
			}
			if strings.Contains(got, testAPIKey) || strings.Contains(got, "secret") {
				t.Fatalf("a link carries a secret: %q", got)
			}
		})
	}
}

func TestItemAndQueueWebURL(t *testing.T) {
	base := "http://radarr:7878/radarr"
	if got := ItemWebURL(base, models.ArrRadarr, "603"); got != "http://radarr:7878/radarr/movie/603" {
		t.Errorf("Radarr item = %q", got)
	}
	if got := ItemWebURL("https://sonarr.example.com/tv", models.ArrSonarr, "the-expanse"); got != "https://sonarr.example.com/tv/series/the-expanse" {
		t.Errorf("Sonarr item = %q", got)
	}
	if got := QueueWebURL(base); got != "http://radarr:7878/radarr/activity/queue" {
		t.Errorf("queue = %q", got)
	}
	if got := QueueWebURL(""); got != "" {
		t.Errorf("queue without a base = %q", got)
	}
	for _, slug := range []string{"", "..", "a/b", "a.b", "a b", "a?b", "a#b", "%2e", "é", strings.Repeat("a", 201)} {
		if got := ItemWebURL(base, models.ArrRadarr, slug); got != "" {
			t.Errorf("slug %q gave %q, want no link", slug, got)
		}
	}
	if got := ItemWebURL(base, models.ArrRadarr, strings.Repeat("a", 200)); got == "" {
		t.Error("a 200-character slug gave no link")
	}
	if got := ItemWebURL(base, "lidarr", "603"); got != "" {
		t.Errorf("unknown kind gave %q", got)
	}
	if got := ItemWebURL("", models.ArrRadarr, "603"); got != "" {
		t.Errorf("no base gave %q", got)
	}
}

func TestIsWebPagePath(t *testing.T) {
	for _, p := range []string{"/movie/603", "/radarr/movie/603/", "/series/the-expanse", "/activity/queue", "/activity",
		"/settings/general", "/system/status", "/wanted/missing", "/add/new", "/calendar", "/api", "/api/v3", "/radarr/API/V3/"} {
		if !IsWebPagePath(p) {
			t.Errorf("IsWebPagePath(%q) = false, want true", p)
		}
	}
	// Start pages, with or without a URL base.
	for _, p := range []string{"", "/", "/radarr", "/radarr/", "/movies", "/media/movie", "/activity-log", "/sonarr4k", "/apis"} {
		if IsWebPagePath(p) {
			t.Errorf("IsWebPagePath(%q) = true, want false", p)
		}
	}
}
