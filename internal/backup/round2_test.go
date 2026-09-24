package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// writeTree writes files (relative paths, folders created) below dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// stageArchive stages cfg+db for review and returns the summary.
func stageArchive(t *testing.T, f *fixture, cfg, db []byte) *RestoreSummary {
	t.Helper()
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	sum, err := f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// r2-data-files#5: config.xml elements this build does not know are never adopted from an
// archive (a later build could read them — a trust list — without any review); the live file's
// own unknown elements are kept.
func TestRestoreNeverAdoptsUnknownConfigElements(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	live, err := os.ReadFile(f.cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	live = bytes.Replace(live, []byte("</Config>"), []byte("  <LiveOnly>keep-me</LiveOnly>\n</Config>"), 1)
	if err := os.WriteFile(f.cfg.Path(), live, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = bytes.Replace(cfg, []byte("</Config>"), []byte(`  <TrustedProxies>0.0.0.0/0</TrustedProxies>
  <AllowedHosts>*.attacker.example</AllowedHosts>
  <!-- planted -->
  <InstanceName>ignored duplicate</InstanceName>
</Config>`), 1)
	cfg = bytes.Replace(cfg, []byte("<InstanceName>Dupearr</InstanceName>"), []byte("<InstanceName>From backup</InstanceName>"), 1)
	stageArchive(t, f, cfg, db)
	staged, err := os.ReadFile(stagedPath(f, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"TrustedProxies", "AllowedHosts", "attacker", "planted"} {
		if bytes.Contains(staged, []byte(bad)) {
			t.Fatalf("the staged config.xml adopted %q from the archive:\n%s", bad, staged)
		}
	}
	if !bytes.Contains(staged, []byte("<LiveOnly>keep-me</LiveOnly>")) {
		t.Errorf("the live config.xml's own unknown element was dropped:\n%s", staged)
	}
	if got := stagedConfig(t, f).InstanceName; got != "From backup" {
		t.Errorf("InstanceName = %q, want the backup's value", got)
	}
	if err := f.svc.ConfirmRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); !applied || err != nil {
		t.Fatalf("apply: %v, %v", applied, err)
	}
	after, err := os.ReadFile(filepath.Join(f.dir, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(after, []byte("TrustedProxies")) {
		t.Fatalf("the live config.xml now carries <TrustedProxies> from the archive:\n%s", after)
	}
}

// r2-data-files#6: a restore never turns dry run off (the backup's deletion methods, mappings and
// connections must be reviewed first), and the summary lists what decides removals.
func TestRestoreKeepsDryRunOnAndSummarisesRemovalSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	st := models.DefaultSettings()
	st.DryRun = false
	st.Mode = "auto"
	st.DeletionMethods = []string{"filesystem"}
	st.RecycleBinPath = "/data/.bin"
	if err := f.db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	ms := &models.MediaServer{Name: "Evil PMS", Kind: models.MediaServerPlex, URL: "http://10.9.9.9:32400", Token: "t", Enabled: true}
	if err := f.db.MediaServers().Create(ctx, ms); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	// The live instance differs from the backup.
	if err := f.db.MediaServers().Delete(ctx, ms.ID); err != nil {
		t.Fatal(err)
	}
	live := models.DefaultSettings()
	live.DryRun = false
	if err := f.db.Settings().Save(ctx, live); err != nil {
		t.Fatal(err)
	}
	sum := stageArchive(t, f, cfg, db)
	var stagedDoc string
	if err := openTestDB(t, stagedPath(f, dbFileName)).QueryRow(`SELECT value FROM settings WHERE key = 'settings'`).Scan(&stagedDoc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stagedDoc, `"dryRun":true`) {
		t.Fatalf("the staged settings keep dry run off: %s", stagedDoc)
	}
	c := changeOf(sum, "dryRun")
	if c == nil || c.Applied || c.Backup != "false" {
		t.Fatalf("dryRun change = %+v, want the backup's false not applied", c)
	}
	for _, name := range []string{"mode", "deletionMethods", "recycleBinPath", "mediaServers"} {
		if changeOf(sum, name) == nil {
			t.Errorf("the summary does not list %s: %+v", name, sum.Changes)
		}
	}
	if c := changeOf(sum, "mediaServers"); c != nil && !strings.Contains(c.Backup, "10.9.9.9") {
		t.Errorf("mediaServers change = %+v", c)
	}
}

// r2-data-files#6: the "keep current" security values are those of the moment of staging, so a
// credential change made before the confirmation makes the staged restore stale.
func TestConfirmRestoreRefusesChangedSecurityState(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	stageArchive(t, f, cfg, db)
	if _, err := f.cfg.Update(func(c *config.Config) { c.ApiKey = "0123456789abcdef0123456789abcdef" }); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConfirmRestore(context.Background()); !errors.Is(err, ErrStaleRestore) {
		t.Fatalf("confirm after an API key change: %v, want ErrStaleRestore", err)
	}
	assertNothingStaged(t, f.dir)

	// A password change too.
	stageArchive(t, f, cfg, db)
	if _, err := f.db.Users().Upsert(context.Background(), "admin", "$2a$12$newhash"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConfirmRestore(context.Background()); !errors.Is(err, ErrStaleRestore) {
		t.Fatalf("confirm after a user change: %v, want ErrStaleRestore", err)
	}
	// Unchanged: confirmed.
	stageArchive(t, f, cfg, db)
	if err := f.svc.ConfirmRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// r2-data-files#2: a recycle bin misplaced at <data>/.restore is never emptied as an incomplete
// staged restore, and a staged folder other users could write to is never adopted.
func TestApplyPendingRestoreRefusesForeignRestoreFolders(t *testing.T) {
	t.Run("recycle bin", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, RestoreDirName)
		file := filepath.Join(bin, "2026-09-20", "Movies", "Film (2020)", "Film.mkv")
		writeTree(t, dir, map[string]string{
			filepath.Join(RestoreDirName, recycleBinMarkerName):      "marker",
			strings.TrimPrefix(file, dir+string(filepath.Separator)): "video",
		})
		if err := os.Chmod(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		if applied, err := ApplyPendingRestore(dir); applied || err == nil || !strings.Contains(err.Error(), "recycle bin") {
			t.Fatalf("apply = %v, %v; want a refusal", applied, err)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("the recycled file was deleted: %v", err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{
			filepath.Join(RestoreDirName, configFileName): "<Config><AuthenticationMethod>None</AuthenticationMethod></Config>",
			filepath.Join(RestoreDirName, dbFileName):     "db",
		})
		if err := os.Chmod(filepath.Join(dir, RestoreDirName), 0o775); err != nil {
			t.Fatal(err)
		}
		if applied, err := ApplyPendingRestore(dir); applied || err == nil {
			t.Fatalf("apply = %v, %v; want a refusal", applied, err)
		}
		if _, err := os.Stat(filepath.Join(dir, configFileName)); err == nil {
			t.Fatal("a planted config.xml was adopted")
		}
	})
}
