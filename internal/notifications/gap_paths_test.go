package notifications

// GAP-14 (docs/SECURITY.md): removal notifications carry the files' full server paths only to
// connections that opt in ("Include File Paths"); every other connection gets the title, size and
// method, never the library layout.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func removalMessage() Message {
	return Message{
		Event:            models.OnFileDeleted,
		Title:            "Duplicate removed: Heat (1995)",
		Body:             "Moved to the recycle bin: /data/movies/Heat (1995)/Heat 720p.mkv",
		BodyWithoutPaths: "Moved 1 file to the recycle bin.",
		Fields: []Field{
			{Name: "Method", Value: "Filesystem", Inline: true},
			{Name: "Files", Value: "/data/movies/Heat (1995)/Heat 720p.mkv", Paths: true},
		},
		Severity: SeverityInfo,
	}
}

func TestRemovalPathsOnlyReachConnectionsThatIncludeThem(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, repo, _ := newTestService(t)
	private := webhookCfg(1, srv.URL, "/private", true, models.OnFileDeleted)
	withPaths := newConfig(KindWebhook, map[string]any{"url": srv.URL + "/with-paths", SettingIncludePaths: true}, models.OnFileDeleted)
	withPaths.ID, withPaths.Name = 2, "with paths"
	repo.cfgs = []models.NotificationConfig{private, withPaths}
	s.Notify(context.Background(), removalMessage())
	s.wg.Wait()

	reqs := rec.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d deliveries, want 2", len(reqs))
	}
	for _, r := range reqs {
		body := string(r.Body)
		hasPath := strings.Contains(body, "/data/movies")
		switch r.Path {
		case "/private":
			if hasPath || !strings.Contains(body, "Heat (1995)") || !strings.Contains(body, "Moved 1 file") {
				t.Errorf("default connection got %s", body)
			}
		case "/with-paths":
			if !hasPath {
				t.Errorf("opted-in connection got no path: %s", body)
			}
		}
	}
}

func TestEveryProviderOffersIncludePaths(t *testing.T) {
	for _, p := range Schema() {
		i := slices.IndexFunc(p.Fields, func(f FieldSchema) bool { return f.Name == SettingIncludePaths })
		if i < 0 {
			t.Errorf("%s has no %s setting", p.Kind, SettingIncludePaths)
			continue
		}
		if f := p.Fields[i]; f.Type != fieldCheckbox || f.Default != false || f.Secret {
			t.Errorf("%s: %s = %+v", p.Kind, SettingIncludePaths, f)
		}
	}
}
