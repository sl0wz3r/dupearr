package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe io.Writer for capturing run's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// randomPorts makes every server listen on a free port.
var randomPorts = []string{"-plex-port", "0", "-radarr-port", "0", "-radarr4k-port", "0", "-sonarr-port", "0", "-tautulli-port", "0"}

// startRun runs the command in the background and waits until it serves.
func startRun(t *testing.T, args ...string) (stdout, stderr *syncBuffer, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr = &syncBuffer{}, &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, append(append([]string{}, randomPorts...), args...), stdout, stderr) }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(stdout.String(), "Serving until interrupted") {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("run exited early: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("run did not start serving:\n%s", stdout)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var once sync.Once
	var runErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case runErr = <-done:
			case <-time.After(15 * time.Second):
				runErr = errors.New("run did not return after cancel")
			}
		})
		return runErr
	}
	t.Cleanup(func() { _ = stop() })
	return stdout, stderr, stop
}

var rePlexLine = regexp.MustCompile(`(?m)^Plex\s+(http://\S+)\s+token=(\S+)`)

func get(t *testing.T, url string, hdr http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestRunServesUntilCancelled(t *testing.T) {
	stdout, stderr, stop := startRun(t, "-scenario", "minimal", "-playing", " 101, ,nope ", "-v", "-media-deletion=false")
	out := stdout.String()
	for _, want := range []string{`scenario "minimal"`, "Path mappings to configure in Dupearr", "/data/media → ", "radarr4k", "apiKey=", "allowMediaDeletion=false", "temporary and removed on exit"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	m := rePlexLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no Plex line in:\n%s", out)
	}
	plexURL, token := m[1], m[2]
	hdr := http.Header{"Accept": {"application/json"}, "X-Plex-Token": {token}}
	if code, body := get(t, plexURL+"/status/sessions", hdr); code != http.StatusOK || !strings.Contains(body, `"ratingKey":"101"`) {
		t.Fatalf("sessions: %d %s", code, body)
	}
	if code, body := get(t, plexURL+"/", hdr); code != http.StatusOK || strings.Contains(body, "allowMediaDeletion") {
		t.Fatalf("root with -media-deletion=false: %d %s", code, body)
	}
	// A forbidden call is summarized on shutdown.
	req, _ := http.NewRequest(http.MethodPut, plexURL+"/library/sections/1/emptyTrash", nil)
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}
	out = stdout.String()
	if !strings.Contains(out, "1 forbidden request(s) received") || !strings.Contains(out, "plex_empty_trash") {
		t.Fatalf("shutdown summary missing:\n%s", out)
	}
	logs := stderr.String()
	if !strings.Contains(logs, "/status/sessions -> 200") || strings.Contains(logs, token) {
		t.Fatalf("verbose log:\n%s", logs)
	}
	if _, err := http.Get(plexURL + "/identity"); err == nil {
		t.Fatal("server still up after shutdown")
	}
}

func TestRunKeepsDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fake")
	stdout, _, stop := startRun(t, "-scenario", "default", "-data", dir)
	if strings.Contains(stdout.String(), "temporary") || !strings.Contains(stdout.String(), dir) {
		t.Errorf("output for a kept data dir:\n%s", stdout)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media", "movies", "Blade Runner 2049 (2017)")); err != nil {
		t.Fatalf("data dir not kept: %v", err)
	}
	// Restarting on its own directory works (the media tree is reset).
	_, _, stop2 := startRun(t, "-scenario", "empty", "-data", dir)
	if err := stop2(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "media", "movies", "Blade Runner 2049 (2017)")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media tree not reset: %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)

	tests := []struct {
		name    string
		args    []string
		isUsage bool
		want    string
	}{
		{"unknown scenario", []string{"-scenario", "nope"}, true, "unknown scenario"},
		{"port out of range", []string{"-plex-port", "70000"}, true, "invalid plex port"},
		{"negative port", []string{"-sonarr-port", "-1"}, true, "invalid sonarr port"},
		{"unknown flag", []string{"-bogus"}, true, "flag provided but not defined"},
		{"positional argument", []string{"extra"}, true, "unexpected arguments"},
		{"foreign data dir", []string{"-data", foreign}, false, "refusing to use non-empty directory"},
		{"port in use", []string{"-radarr-port", busyPort}, false, "listen radarr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, randomPorts...), tt.args...)
			err := run(context.Background(), args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run(%v) = %v, want error containing %q", tt.args, err, tt.want)
			}
			if errors.Is(err, errUsage) != tt.isUsage {
				t.Fatalf("usage error = %v, want %v (%v)", errors.Is(err, errUsage), tt.isUsage, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(foreign, "keep.txt")); err != nil {
		t.Fatalf("foreign data dir touched: %v", err)
	}
}

func TestRunHelpAndList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, &stdout, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h = %v", err)
	}
	if !strings.Contains(stderr.String(), "-plex-port") || !strings.Contains(stderr.String(), "Usage: fakemedia") {
		t.Fatalf("usage:\n%s", stderr.String())
	}
	stdout.Reset()
	if err := run(context.Background(), []string{"-list"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"default ", "minimal ", "empty ", "discs ", "looseclips ", "-disc-scanner"} {
		if !strings.Contains(stdout.String(), name) {
			t.Errorf("-list lacks %q:\n%s", name, stdout.String())
		}
	}
}

// TestRunDiscScanner serves the discs scenario with the disc-image scanner: the movie libraries
// report it, the UHD disc is one version with 300 parts, and a per-file delete of a disc member is
// summarised on shutdown with the disc it left incomplete.
func TestRunDiscScanner(t *testing.T) {
	stdout, _, stop := startRun(t, "-scenario", "discs", "-disc-scanner")
	out := stdout.String()
	for _, want := range []string{`scenario "discs"`, `scanner="Plex Movie Scanner with Disc Image Support"`, "Full-disc backups (11):",
		"Plex version with 300 part(s)", "radarr tracks BDMV/STREAM/00800.m2ts"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	m := rePlexLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no Plex line in:\n%s", out)
	}
	plexURL, token := m[1], m[2]
	hdr := http.Header{"Accept": {"application/json"}, "X-Plex-Token": {token}}
	code, body := get(t, plexURL+"/library/sections/1/all?type=1", hdr)
	if code != http.StatusOK || strings.Count(body, "/BDMV/STREAM/") < 300 || !strings.Contains(body, `"title":"Alien"`) {
		t.Fatalf("section listing: %d (%d disc parts)", code, strings.Count(body, "/BDMV/STREAM/"))
	}
	var listing struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
				Media     []struct {
					ID   int64 `json:"id"`
					Part []struct {
						File string `json:"file"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatal(err)
	}
	var rk, mediaID string
	for _, it := range listing.MediaContainer.Metadata {
		for _, md := range it.Media {
			if it.Title == "Gladiator" && strings.Contains(md.Part[0].File, "/BDMV/STREAM/") {
				rk, mediaID = it.RatingKey, strconv.FormatInt(md.ID, 10)
			}
		}
	}
	if rk == "" {
		t.Fatalf("Gladiator disc version not found")
	}
	req, _ := http.NewRequest(http.MethodDelete, plexURL+"/library/metadata/"+rk+"/media/"+mediaID, nil)
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}
	out = stdout.String()
	if !strings.Contains(out, "delete_disc_member") || !strings.Contains(out, "1 full-disc backup(s) left incomplete") {
		t.Fatalf("shutdown summary:\n%s", out)
	}
}

func TestAllInterfaces(t *testing.T) {
	tests := map[string]bool{"": true, "0.0.0.0": true, "::": true, "127.0.0.1": false, "::1": false, "localhost": false, "192.168.1.2": false}
	for in, want := range tests {
		if got := allInterfaces(in); got != want {
			t.Errorf("allInterfaces(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	tests := map[string][]string{
		"":            nil,
		" , ":         nil,
		"101":         {"101"},
		" 101, 102 ,": {"101", "102"},
	}
	for in, want := range tests {
		got := splitList(in)
		if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("splitList(%q) = %q, want %q", in, got, want)
		}
	}
}
