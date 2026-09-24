package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestReadProbeConfigCaseInsensitiveEnv: the probe resolves DUPEARR__ variables exactly like
// config.Load (names match case-insensitively), so it probes the port the server listens on.
func TestReadProbeConfigCaseInsensitiveEnv(t *testing.T) {
	clearServerEnv(t)
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><Port>8123</Port></Config>`)
	t.Setenv("dupearr__server__port", "9100")
	t.Setenv("Dupearr__Server__UrlBase", "/c")
	pc, err := readProbeConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pingURL(pc), "http://127.0.0.1:9100/c/ping"; got != want {
		t.Fatalf("pingURL = %q, want %q", got, want)
	}
}

func seedUser(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(dir, dbFileName), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Users().Upsert(ctx, "admin", "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012"); err != nil {
		t.Fatal(err)
	}
}

func userCount(t *testing.T, dir string) int {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(dir, dbFileName), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := db.Users().Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestResetAuth(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	t.Run("config.xml", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigXML(t, dir, `<Config><AuthenticationMethod>External</AuthenticationMethod></Config>`)
		seedUser(t, dir)
		var out bytes.Buffer
		if err := resetAuth(context.Background(), dir, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "reset to Forms") {
			t.Fatalf("output = %q", out.String())
		}
		cfg, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if m := cfg.Get().AuthenticationMethod; m != config.AuthForms {
			t.Fatalf("method = %q", m)
		}
		if n := userCount(t, dir); n != 0 {
			t.Fatalf("users = %d", n)
		}
	})
	for _, tc := range []struct {
		env, want string
	}{
		{"None", "stays None while that variable is set"},
		{"forms", "Start Dupearr, then open the web UI"},
	} {
		t.Run("environment "+tc.env, func(t *testing.T) {
			dir := t.TempDir()
			writeConfigXML(t, dir, `<Config><AuthenticationMethod>External</AuthenticationMethod></Config>`)
			if _, err := config.Load(dir); err != nil { // normalizes and writes the file once
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, configFileName))
			if err != nil {
				t.Fatal(err)
			}
			seedUser(t, dir)
			t.Setenv("dupearr__auth__method", tc.env)
			var out bytes.Buffer
			if err := resetAuth(context.Background(), dir, &out); err != nil {
				t.Fatal(err)
			}
			msg := out.String()
			if !strings.Contains(msg, "by the "+authMethodEnv+" environment variable") || !strings.Contains(msg, tc.want) ||
				strings.Contains(msg, "reset to Forms") {
				t.Fatalf("output = %q", msg)
			}
			after, err := os.ReadFile(filepath.Join(dir, configFileName))
			if err != nil {
				t.Fatal(err)
			}
			// The env-set method stays as it is in config.xml (the credentials are still rotated).
			if !bytes.Contains(before, []byte("<AuthenticationMethod>External</AuthenticationMethod>")) ||
				!bytes.Contains(after, []byte("<AuthenticationMethod>External</AuthenticationMethod>")) {
				t.Fatalf("config.xml's authentication method changed:\n%s\n→\n%s", before, after)
			}
			if n := userCount(t, dir); n != 0 {
				t.Fatalf("users = %d", n)
			}
		})
	}
}

// TestStartupRecoversInterruptedRemovals: a removal a previous process left "running" is failed
// (with its group) before any command can start.
func TestStartupRecoversInterruptedRemovals(t *testing.T) {
	clearServerEnv(t)
	ctx := context.Background()
	dir := t.TempDir()
	a, err := newApp(ctx, options{dataDir: dir, noBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()

	g := &models.DuplicateGroup{
		Key: "movie:tmdb:1", MediaType: models.MediaTypeMovie, Title: "Film", ServerID: 1,
		LibraryIDs: []int64{1}, Status: models.GroupQueued, ProfileID: 1,
		Files: []models.GroupFile{
			{Version: models.MediaVersion{Key: "plex:1:1", ServerID: 1, RatingKey: "10", MediaID: 1,
				Parts: []models.MediaPart{{ID: 1, Path: "/m/a.mkv", Size: 2}}}, Decision: models.DecisionKeep, EngineDecision: models.DecisionKeep},
			{Version: models.MediaVersion{Key: "plex:1:2", ServerID: 1, RatingKey: "10", MediaID: 2,
				Parts: []models.MediaPart{{ID: 2, Path: "/m/b.mkv", Size: 1}}}, Decision: models.DecisionRemove, EngineDecision: models.DecisionRemove},
		},
	}
	if _, err := a.db.Groups().Upsert(ctx, g); err != nil {
		t.Fatal(err)
	}
	act := models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID, VersionKey: "plex:1:2", Title: "Film", Paths: []string{"/m/b.mkv"}, Size: 1}
	if err := a.db.Actions().Create(ctx, &act); err != nil {
		t.Fatal(err)
	}
	act.Status = models.ActionRunning
	if err := a.db.Actions().Update(ctx, &act); err != nil {
		t.Fatal(err)
	}

	a.recoverInterrupted(ctx)

	got, err := a.db.Actions().Get(ctx, act.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.ActionFailed || !strings.Contains(got.Message, "Interrupted by a restart") {
		t.Fatalf("action = %+v", got)
	}
	grp, err := a.db.Groups().Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grp.Status != models.GroupFailed {
		t.Fatalf("group = %q (%s)", grp.Status, grp.StatusReason)
	}

	// Live config changes reach logging (level and size limit) without a restart.
	if _, err := a.cfg.Update(func(c *config.Config) { c.LogLevel = "debug"; c.LogSizeLimit = 2 }); err != nil {
		t.Fatal(err)
	}
	if lvl := a.logs.Level(); lvl != "debug" {
		t.Fatalf("log level = %q", lvl)
	}

	// Shutdown order: the API's background work and the notifier stop before the database closes.
	_, cancel := context.WithCancel(ctx)
	a.stopBackground(cancel, time.Now().Add(shutdownTimeout))
}
