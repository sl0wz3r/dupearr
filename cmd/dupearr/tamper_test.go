package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// plantTrigger adds a backdoor trigger that re-creates a user whenever the users are deleted.
func plantTrigger(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER backdoor AFTER DELETE ON users BEGIN
		INSERT INTO users (username, password_hash, created_at) VALUES ('evil', 'x', '2026-01-01T00:00:00Z'); END`); err != nil {
		t.Fatal(err)
	}
}

func triggerCount(t *testing.T, dir string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type IN ('trigger', 'view')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestResetAuthRemovesBackdoorTrigger: a trigger planted in the database (e.g. through a tampered
// backup restored by an earlier version) must not survive `dupearr reset-auth` nor re-create a
// user after it (SEC-007).
func TestResetAuthRemovesBackdoorTrigger(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod></Config>`)
	seedUser(t, dir)
	plantTrigger(t, dir)
	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	if n := userCount(t, dir); n != 0 {
		t.Fatalf("users after reset-auth = %d; the backdoor re-created one", n)
	}
	if n := triggerCount(t, dir); n != 0 {
		t.Fatalf("triggers left = %d", n)
	}
	if !strings.Contains(out.String(), `Removed trigger "backdoor"`) {
		t.Errorf("output does not report the removal: %q", out.String())
	}
}

// TestStartupRemovesForeignTriggers: the server drops triggers and views Dupearr does not create
// before it opens the database.
func TestStartupRemovesForeignTriggers(t *testing.T) {
	clearServerEnv(t)
	dir := t.TempDir()
	seedUser(t, dir)
	plantTrigger(t, dir)
	a, err := newApp(context.Background(), options{dataDir: dir, noBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if n := triggerCount(t, dir); n != 0 {
		t.Fatalf("triggers left after startup = %d", n)
	}
	if err := a.db.Users().DeleteAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, err := a.db.Users().Count(context.Background()); err != nil || n != 0 {
		t.Fatalf("users = %d, %v", n, err)
	}
}

// TestPrepareDataDir: a new data directory is private; an existing one loses access for other
// users and write access for its group (which could replace config.xml or plant a staged
// restore), but keeps its group's read access (SEC-016).
func TestPrepareDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	root := t.TempDir()
	fresh := filepath.Join(root, "a", "Dupearr")
	if w, err := prepareDataDir(fresh); err != nil || w != "" {
		t.Fatalf("prepareDataDir = %q, %v", w, err)
	}
	for dir, tc := range map[string]struct{ before, after os.FileMode }{
		filepath.Join(root, "open"):        {0o777, 0o750},
		filepath.Join(root, "umask002"):    {0o775, 0o750},
		filepath.Join(root, "groupwrite"):  {0o770, 0o750},
		filepath.Join(root, "default"):     {0o755, 0o750},
		filepath.Join(root, "group"):       {0o750, 0o750},
		filepath.Join(root, "private"):     {0o700, 0o700},
		filepath.Join(root, "groupsearch"): {0o710, 0o710},
	} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, tc.before); err != nil {
			t.Fatal(err)
		}
		if w, err := prepareDataDir(dir); err != nil || w != "" {
			t.Fatalf("prepareDataDir(%s) = %q, %v", filepath.Base(dir), w, err)
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != tc.after {
			t.Errorf("%s: mode = %v, want %v", filepath.Base(dir), fi.Mode().Perm(), tc.after)
		}
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("new data directory mode = %v, want 0700", fi.Mode().Perm())
	}
}

// TestPrepareDataDirRestrictsSecretFiles: files holding secrets that something loosened (a NAS
// "new permissions" tool: 0666 files, 0777 folders) are owner-only again at startup, since the
// data directory may stay readable by its group. Other files and symlinks are left alone.
func TestPrepareDataDirRestrictsSecretFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	outside := filepath.Join(root, "outside.txt")
	mk := func(p string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]os.FileMode{
		"config.xml":                      0o600,
		"config.xml.bak-20260101T000000Z": 0o600,
		"dupearr.db":                      0o600,
		"dupearr.db-wal":                  0o600,
		"dupearr.db.bak-20260101T000000Z": 0o600,
		"Backups/manual/b.zip":            0o600,
		"notes.txt":                       0o644, // not Dupearr's
	}
	for name := range want {
		mk(filepath.Join(dir, name), 0o666)
	}
	if err := os.Chmod(filepath.Join(dir, "notes.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"Backups", "Backups/manual"} {
		if err := os.Chmod(filepath.Join(dir, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	mk(outside, 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "dupearr.db-shm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if w, err := prepareDataDir(dir); err != nil || w != "" {
		t.Fatalf("prepareDataDir = %q, %v", w, err)
	}
	want["Backups"], want["Backups/manual"], want["."] = 0o700, 0o700, 0o750
	for name, mode := range want {
		if fi, err := os.Lstat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: mode = %v, %v; want %v", name, fi.Mode().Perm(), err, mode)
		}
	}
	if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("symlink target changed: %v, %v", fi.Mode().Perm(), err)
	}
}
