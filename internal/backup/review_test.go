package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// stagedSetting returns an internal settings entry of the staged database ("" when missing).
func stagedSetting(t *testing.T, f *fixture, key string) string {
	t.Helper()
	var v string
	err := openTestDB(t, stagedPath(f, dbFileName)).QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return v
}

// TestRestoreKeepsWebhookTokenByDefault: the webhook token is a credential like the API key. A
// restore keeps the running instance's unless the security settings are restored, never shows
// either value, and keeps the current one when the backup has none; the recorded user id follows
// the users (SEC-008 review).
func TestRestoreKeepsWebhookTokenByDefault(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	backupTok, liveTok := strings.Repeat("bb", 16), strings.Repeat("aa", 16)
	set := func(key, value string) {
		t.Helper()
		if err := f.db.Settings().SetValue(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	set(webhookTokenSetting, backupTok)
	set(userIDSetting, "7")
	cfg, db := validParts(t, f)
	set(webhookTokenSetting, liveTok)
	set(userIDSetting, "1")

	stage := func(db []byte, opts RestoreOptions) *RestoreSummary {
		t.Helper()
		archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
		sum, err := f.svc.StageRestoreUpload(ctx, bytes.NewReader(archive), int64(len(archive)), opts)
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	for _, tc := range []struct {
		name    string
		opts    RestoreOptions
		want    string
		applied bool
		userID  string
	}{
		{"default", RestoreOptions{}, liveTok, false, "1"},
		{"opt-in", RestoreOptions{RestoreSecuritySettings: true}, backupTok, true, "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum := stage(db, tc.opts)
			if got := stagedSetting(t, f, webhookTokenSetting); got != tc.want {
				t.Errorf("staged webhook token = %q, want %q", got, tc.want)
			}
			c := changeOf(sum, "webhookToken")
			if c == nil || c.Applied != tc.applied || c.Current != "" || c.Backup != "" || c.Message == "" {
				t.Errorf("webhookToken change = %+v", c)
			}
			if got := stagedSetting(t, f, userIDSetting); got != tc.userID {
				t.Errorf("staged user id = %q, want %q", got, tc.userID)
			}
		})
	}

	// A backup without a token keeps the current one, even when security settings are restored.
	sum := stage(modifyDB(t, db, `DELETE FROM settings WHERE key = 'auth.webhookToken'`), RestoreOptions{RestoreSecuritySettings: true})
	if got := stagedSetting(t, f, webhookTokenSetting); got != liveTok {
		t.Errorf("staged webhook token = %q, want the current one", got)
	}
	if c := changeOf(sum, "webhookToken"); c == nil || c.Applied {
		t.Errorf("webhookToken change = %+v", c)
	}
}

// TestRestoreRejectsSecondUser: Dupearr keeps a single account, so a backup with two can only
// have been modified; restoring it would add a hidden login that password changes never touch
// (UpdateUser changes the first account only).
func TestRestoreRejectsSecondUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.db.Users().Upsert(ctx, "admin", "$2a$12$livehash"); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	two := modifyDB(t, db, `INSERT INTO users (username, password_hash, created_at)
		VALUES ('evil', '$2a$12$evilhash', '2026-01-01T00:00:00.000000000Z')`)
	for _, opts := range []RestoreOptions{{}, {RestoreSecuritySettings: true}} {
		archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: two})
		_, err := f.svc.StageRestoreUpload(ctx, bytes.NewReader(archive), int64(len(archive)), opts)
		if !errors.Is(err, ErrInvalidBackup) || !strings.Contains(err.Error(), "user accounts") {
			t.Fatalf("opts %+v: err = %v; want ErrInvalidBackup about the user accounts", opts, err)
		}
		assertNothingStaged(t, f.dir)
	}
}

// TestApplyPendingRestoreKeepsCurrentGeneration: the files a restore just replaced are the only
// way to undo it, so pruning never removes them, even when the clock went back and older .bak
// stamps sort after the new one.
func TestApplyPendingRestoreKeepsCurrentGeneration(t *testing.T) {
	dir := t.TempDir()
	live := map[string]string{"config.xml": "cfg", "dupearr.db": "db"}
	for _, st := range []string{"20300101T000000Z", "20310101T000000Z"} {
		live["config.xml.bak-"+st] = "old"
		live["dupearr.db.bak-"+st] = "old"
	}
	writeFiles(t, dir, live)
	stageRaw(t, dir, map[string]string{"config.xml": "new cfg", "dupearr.db": "new db"})
	if applied, err := applyPendingRestore(dir, fixedNow); !applied || err != nil {
		t.Fatalf("apply = %v, %v", applied, err)
	}
	var baks []string
	for name := range readFiles(t, dir) {
		if strings.Contains(name, ".bak-") {
			baks = append(baks, name)
		}
	}
	slices.Sort(baks)
	stamp := fixedNow.UTC().Format(bakTimeLayout)
	want := []string{"config.xml.bak-20310101T000000Z", "config.xml.bak-" + stamp, "dupearr.db.bak-20310101T000000Z",
		"dupearr.db.bak-" + stamp}
	slices.Sort(want)
	if !slices.Equal(baks, want) {
		t.Errorf("backup files = %v\nwant %v", baks, want)
	}
}

// nonSecurityConfigFields are the config.xml settings a restore always takes from the backup.
var nonSecurityConfigFields = map[string]bool{
	"logLevel": true, "logSizeLimit": true, "instanceName": true, "launchBrowser": true, "branch": true,
}

// TestSecurityFieldsCoverConfig forces a decision for every config.xml setting: either a restore
// keeps the running instance's value unless asked otherwise (securityFields), or it is restored
// from the backup (nonSecurityConfigFields). A new listener, authentication or trust setting
// (e.g. trusted proxies) must not be adopted from an archive silently.
func TestSecurityFieldsCoverConfig(t *testing.T) {
	security := map[string]bool{}
	for _, f := range securityFields {
		security[f.name] = true
	}
	typ := reflect.TypeFor[config.Config]()
	seen := map[string]bool{}
	for i := range typ.NumField() {
		r := []rune(typ.Field(i).Name)
		r[0] = unicode.ToLower(r[0])
		name := string(r)
		seen[name] = true
		if security[name] == nonSecurityConfigFields[name] {
			t.Errorf("config.Config.%s: list it in exactly one of securityFields and nonSecurityConfigFields", typ.Field(i).Name)
		}
	}
	for name := range security {
		if !seen[name] {
			t.Errorf("securityFields lists %q, which config.Config does not have", name)
		}
	}
}
