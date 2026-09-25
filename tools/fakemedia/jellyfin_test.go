package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var reJellyfinLine = regexp.MustCompile(`(?m)^Jellyfin\s+(http://\S+)\s+apiKey=(\S+)\s+serverId=(\S+)`)

// TestRunServesJellyfin: -jellyfin-port serves the fake Jellyfin over the scenario's tree, describes
// it with its libraries and path mapping, and it answers only a header credential.
func TestRunServesJellyfin(t *testing.T) {
	stdout, _, stop := startRun(t, "-scenario", "minimal", "-jellyfin-port", "0")
	out := stdout.String()
	m := reJellyfinLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no Jellyfin line in:\n%s", out)
	}
	for _, want := range []string{`library "Movies" (movies)`, `library "Shows" (tvshows)`, "jellyfin  /data/media → "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	jfURL, key, serverID := m[1], m[2], m[3]
	if code, body := get(t, jfURL+"/System/Info/Public", nil); code != http.StatusOK || !strings.Contains(body, serverID) {
		t.Fatalf("public info: %d %s", code, body)
	}
	if code, _ := get(t, jfURL+"/System/Info", nil); code != http.StatusUnauthorized {
		t.Fatalf("system info without a credential: %d", code)
	}
	hdr := http.Header{"Authorization": {`MediaBrowser Token="` + key + `", Client="test", Device="test", DeviceId="test", Version="1"`}}
	if code, body := get(t, jfURL+"/Library/VirtualFolders", hdr); code != http.StatusOK || !strings.Contains(body, `"Movies"`) {
		t.Fatalf("virtual folders: %d %s", code, body)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "forbidden request") {
		t.Fatalf("violations:\n%s", stdout)
	}
}

func TestRunRejectsABadJellyfinPort(t *testing.T) {
	if err := run(t.Context(), []string{"-jellyfin-port", "70000"}, &syncBuffer{}, &syncBuffer{}); err == nil || !strings.Contains(err.Error(), "invalid jellyfin port") {
		t.Fatalf("run = %v", err)
	}
}
