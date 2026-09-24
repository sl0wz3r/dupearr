package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// modifyDB returns a copy of a database image with stmts applied.
func modifyDB(t *testing.T, db []byte, stmts ...string) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "modified.db")
	if err := os.WriteFile(p, db, 0o600); err != nil {
		t.Fatal(err)
	}
	sdb, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	for _, q := range append([]string{`PRAGMA journal_mode=DELETE`}, stmts...) {
		if _, err := sdb.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := sdb.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// dumpTables returns every row of every table except schema_migrations (whose applied_at
// differs) as text, keyed by table and position.
func dumpTables(t *testing.T, path string) map[string]string {
	t.Helper()
	db := openTestDB(t, path)
	var tables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	_ = rows.Close()
	out := map[string]string{}
	for _, tbl := range tables {
		rows, err := db.Query(`SELECT * FROM ` + quoteIdent(tbl) + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for i := 0; rows.Next(); i++ {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for j, v := range vals {
				if b, ok := v.([]byte); ok {
					vals[j] = string(b)
				}
			}
			out[fmt.Sprintf("%s#%d", tbl, i)] = fmt.Sprint(vals...)
		}
		_ = rows.Close()
	}
	return out
}

// schemaSQL returns the schema objects of a database (type/name → SQL), internal ones excluded.
func schemaSQL(t *testing.T, path string) map[string]string {
	t.Helper()
	db := openTestDB(t, path)
	rows, err := db.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var typ, name, q string
		if err := rows.Scan(&typ, &name, &q); err != nil {
			t.Fatal(err)
		}
		out[typ+"/"+name] = q
	}
	return out
}

func referenceSchema(t *testing.T) map[string]string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ref.db")
	if err := createReference(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return schemaSQL(t, p)
}

func uploadArchive(t *testing.T, f *fixture, cfg, db []byte) error {
	t.Helper()
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	return f.svc.RestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)))
}

func stagedPath(f *fixture, name string) string { return filepath.Join(f.dir, RestoreDirName, name) }

// TestRestoreRejectsTriggersAndViews: SQL triggers and views in a backup's database would be
// adopted into the live database and run hidden logic: keep cancelled removals pending (so the
// executor resumes them after the restart), re-create a deleted user (defeating reset-auth),
// undo password changes. Dupearr never creates any, so such an archive is refused (SEC-007).
func TestRestoreRejectsTriggersAndViews(t *testing.T) {
	f := newFixture(t)
	seedRemovals(t, f.db)
	cfg, db := validParts(t, f)
	tests := map[string]string{
		"trigger keeping removals pending": `CREATE TRIGGER keep_pending AFTER UPDATE OF status ON actions
			WHEN OLD.status = 'pending' BEGIN
			UPDATE actions SET status = 'pending', message = '', finished_at = NULL WHERE id = NEW.id; END`,
		"backdoor user trigger": `CREATE TRIGGER backdoor AFTER DELETE ON users BEGIN
			INSERT INTO users (username, password_hash, created_at) VALUES ('evil', 'x', '2026-01-01'); END`,
		"trigger on settings": `CREATE TRIGGER keep_dry_run_off AFTER UPDATE ON settings BEGIN SELECT 1; END`,
		"view":                `CREATE VIEW pending_removals AS SELECT * FROM actions WHERE status = 'pending'`,
		"table replaced by a view": `ALTER TABLE exclusions RENAME TO exclusions_old;
			CREATE VIEW exclusions AS SELECT * FROM exclusions_old`,
	}
	for name, stmt := range tests {
		t.Run(name, func(t *testing.T) {
			err := uploadArchive(t, f, cfg, modifyDB(t, db, stmt))
			if !errors.Is(err, ErrInvalidBackup) || !strings.Contains(err.Error(), "does not create") {
				t.Fatalf("err = %v; want ErrInvalidBackup naming the foreign object", err)
			}
			assertNothingStaged(t, f.dir)
		})
	}
}

// TestRestoreRebuildsSchema: whatever the archive's schema holds besides Dupearr's (extra tables
// and indexes, altered table definitions), the staged database has exactly this build's schema,
// with the rows of the columns Dupearr knows.
func TestRestoreRebuildsSchema(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.db.Exclusions().Create(ctx, &models.Exclusion{Kind: models.ExcludeGroupKey, Value: "42", Title: "Kept"}); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	tampered := modifyDB(t, db,
		`CREATE TABLE evil (x TEXT)`,
		`INSERT INTO evil VALUES ('payload')`,
		`CREATE INDEX idx_evil ON actions(title)`,
		`ALTER TABLE exclusions RENAME TO exclusions_old`,
		`CREATE TABLE exclusions (id INTEGER PRIMARY KEY, kind TEXT, value TEXT, title TEXT, reason TEXT,
			created_at TEXT, extra TEXT DEFAULT 'x', CHECK (length(value) < 10))`,
		`INSERT INTO exclusions (id, kind, value, title, reason, created_at) SELECT id, kind, value, title, reason, created_at FROM exclusions_old`,
		`DROP TABLE exclusions_old`,
	)
	if err := uploadArchive(t, f, cfg, tampered); err != nil {
		t.Fatal(err)
	}
	staged := stagedPath(f, dbFileName)
	if got, want := schemaSQL(t, staged), referenceSchema(t); !equalMaps(got, want) {
		t.Errorf("staged schema differs from this build's:\n got %v\nwant %v", got, want)
	}
	var title string
	if err := openTestDB(t, staged).QueryRow(`SELECT title FROM exclusions WHERE value = '42'`).Scan(&title); err != nil || title != "Kept" {
		t.Errorf("exclusion row = %q, %v", title, err)
	}
	if got := stagedFiles(t, f.dir); strings.Join(got, ",") != "config.xml,dupearr.db" {
		t.Errorf("staged files = %v", got)
	}
}

// TestRestoreUpgradesOlderSchema: a backup from before a column-adding migration is restored into
// this build's schema, with the column's default.
func TestRestoreUpgradesOlderSchema(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	queued, _, _ := seedRemovals(t, f.db)
	cfg, db := validParts(t, f)
	old := modifyDB(t, db,
		`ALTER TABLE duplicate_groups DROP COLUMN retained_overrides`,
		`DELETE FROM schema_migrations WHERE version = 2`)
	if err := uploadArchive(t, f, cfg, old); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); err != nil || !applied {
		t.Fatalf("apply = %v, %v", applied, err)
	}
	rdb, err := database.Open(ctx, filepath.Join(f.dir, dbFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	g, err := rdb.Groups().Get(ctx, queued)
	if err != nil || g.Title == "" || g.Status != models.GroupPending {
		t.Fatalf("restored group = %+v, %v", g, err)
	}
}

// TestUpgradableMigrationsCoverSchema makes every new migration an explicit decision: list it in
// upgradableMigrations when copying an older database's rows into the new schema is all it takes
// (it only adds tables, indexes or columns with defaults); otherwise teach rebuild to transform
// the rows first.
func TestUpgradableMigrationsCoverSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ref.db")
	if err := createReference(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	versions, err := migrationVersions(context.Background(), sqliteURI(p, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	for v := range versions {
		if v != 1 && !upgradableMigrations[v] {
			t.Errorf("migration %d is not in upgradableMigrations: decide whether restoring an older backup needs more than copying its rows", v)
		}
	}
	for v := range upgradableMigrations {
		if !versions[v] {
			t.Errorf("upgradableMigrations lists unknown migration %d", v)
		}
	}
}

// securityBackup returns an archive whose config.xml weakens authentication and changes the
// listener and API key, and whose database has another user and a session signing key.
func securityBackup(t *testing.T, f *fixture) (cfg, db []byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.Users().Upsert(ctx, "olduser", "$2a$12$backuphash"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Settings().SetValue(ctx, sessionKeySetting, strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
	_, db = validParts(t, f)
	// The live instance moves on: new password, new user name.
	if _, err := f.db.Users().Upsert(ctx, "admin", "$2a$12$livehash"); err != nil {
		t.Fatal(err)
	}
	cfg = []byte(`<Config>
  <BindAddress>127.0.0.1</BindAddress>
  <Port>8080</Port>
  <UrlBase>/old</UrlBase>
  <ApiKey>backupkey0123456789abcdef</ApiKey>
  <AuthenticationMethod>None</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <InstanceName>From backup</InstanceName>
</Config>
`)
	return cfg, db
}

func stagedConfig(t *testing.T, f *fixture) config.Config {
	t.Helper()
	data, err := os.ReadFile(stagedPath(f, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func stagedUsers(t *testing.T, f *fixture) []string {
	t.Helper()
	rows, err := openTestDB(t, stagedPath(f, dbFileName)).Query(`SELECT username || ':' || password_hash FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func stagedSessionKey(t *testing.T, f *fixture) bool {
	t.Helper()
	var n int
	if err := openTestDB(t, stagedPath(f, dbFileName)).QueryRow(`SELECT count(*) FROM settings WHERE key = ?`, sessionKeySetting).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func changeOf(sum *RestoreSummary, setting string) *RestoreChange {
	for i := range sum.Changes {
		if sum.Changes[i].Setting == setting {
			return &sum.Changes[i]
		}
	}
	return nil
}

// TestRestoreKeepsSecuritySettingsByDefault: a restore keeps the running instance's
// authentication, API key, user and listener, never restores the session signing key, and
// reports the differences (SEC-008).
func TestRestoreKeepsSecuritySettingsByDefault(t *testing.T) {
	f := newFixture(t)
	live := f.cfg.File()
	cfg, db := securityBackup(t, f)
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	sum, err := f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := stagedConfig(t, f)
	if got.AuthenticationMethod != config.AuthForms || got.AuthenticationRequired != config.AuthRequiredEnabled ||
		got.ApiKey != live.ApiKey || got.Port != live.Port || got.BindAddress != live.BindAddress || got.UrlBase != live.UrlBase {
		t.Errorf("staged security settings = %+v; want the current ones %+v", got, live)
	}
	if got.InstanceName != "From backup" {
		t.Errorf("instance name = %q; other settings must be restored", got.InstanceName)
	}
	if users := stagedUsers(t, f); !slices.Equal(users, []string{"admin:$2a$12$livehash"}) {
		t.Errorf("staged users = %v; want the current user", users)
	}
	if stagedSessionKey(t, f) {
		t.Error("the backup's session signing key was restored")
	}
	for _, s := range []string{"authenticationMethod", "authenticationRequired", "apiKey", "bindAddress", "port", "urlBase", "users"} {
		c := changeOf(sum, s)
		if c == nil || c.Applied {
			t.Errorf("change %s = %+v; want it reported and not applied", s, c)
		}
	}
	if c := changeOf(sum, "apiKey"); c != nil && (c.Current != "" || c.Backup != "") {
		t.Errorf("API key values exposed in the summary: %+v", c)
	}
	if c := changeOf(sum, "users"); c != nil && (strings.Contains(c.Current, "$2a$") || strings.Contains(c.Backup, "$2a$")) {
		t.Errorf("password hashes exposed in the summary: %+v", c)
	}
	// Restore (immediate) behaves the same.
	if err := uploadArchive(t, f, cfg, db); err != nil {
		t.Fatal(err)
	}
	if got := stagedConfig(t, f); got.AuthenticationMethod != config.AuthForms || got.ApiKey != live.ApiKey {
		t.Errorf("Restore staged %s / key changed", got.AuthenticationMethod)
	}
}

// TestRestoreSecuritySettingsOptIn: when asked, the backup's security settings are restored,
// except a switch to authentication None or External.
func TestRestoreSecuritySettingsOptIn(t *testing.T) {
	f := newFixture(t)
	cfg, db := securityBackup(t, f)
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	opts := RestoreOptions{RestoreSecuritySettings: true}
	sum, err := f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), opts)
	if err != nil {
		t.Fatal(err)
	}
	got := stagedConfig(t, f)
	if got.ApiKey != "backupkey0123456789abcdef" || got.Port != 8080 || got.BindAddress != "127.0.0.1" ||
		got.UrlBase != "/old" || got.AuthenticationRequired != config.AuthRequiredDisabledForLocal {
		t.Errorf("staged config = %+v; want the backup's security settings", got)
	}
	if got.AuthenticationMethod != config.AuthForms {
		t.Errorf("authentication method = %s; a switch to None must never be restored", got.AuthenticationMethod)
	}
	if c := changeOf(sum, "authenticationMethod"); c == nil || c.Applied || c.Message == "" {
		t.Errorf("authenticationMethod change = %+v", c)
	}
	if c := changeOf(sum, "port"); c == nil || !c.Applied {
		t.Errorf("port change = %+v", c)
	}
	if users := stagedUsers(t, f); !slices.Equal(users, []string{"olduser:$2a$12$backuphash"}) {
		t.Errorf("staged users = %v; want the backup's", users)
	}
	if stagedSessionKey(t, f) {
		t.Error("the backup's session signing key was restored")
	}

	// A backup without a user keeps the current one, and cannot switch to Forms without one.
	noUser := modifyDB(t, db, `DELETE FROM users`)
	archive = makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: noUser})
	sum, err = f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), opts)
	if err != nil {
		t.Fatal(err)
	}
	if users := stagedUsers(t, f); !slices.Equal(users, []string{"admin:$2a$12$livehash"}) {
		t.Errorf("staged users = %v; want the current user kept", users)
	}
	if c := changeOf(sum, "users"); c == nil || c.Applied {
		t.Errorf("users change = %+v", c)
	}
}

// TestStagedRestoreNeedsConfirmation: a restore staged for review is discarded at the next start
// unless it was confirmed.
func TestStagedRestoreNeedsConfirmation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg, db := validParts(t, f)
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	if err := f.svc.ConfirmRestore(ctx); !errors.Is(err, ErrNoStagedRestore) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirm without a staged restore: %v", err)
	}
	if _, err := f.svc.StageRestoreUpload(ctx, bytes.NewReader(archive), int64(len(archive)), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); applied || err != nil {
		t.Fatalf("unconfirmed restore applied: %v, %v", applied, err)
	}
	assertNothingStaged(t, f.dir)

	if _, err := f.svc.StageRestoreUpload(ctx, bytes.NewReader(archive), int64(len(archive)), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DiscardRestore(); err != nil {
		t.Fatal(err)
	}
	assertNothingStaged(t, f.dir)

	if _, err := f.svc.StageRestoreUpload(ctx, bytes.NewReader(archive), int64(len(archive)), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConfirmRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); !applied || err != nil {
		t.Fatalf("confirmed restore: applied = %v, %v", applied, err)
	}
}

// TestRestoreRejectsEnvMaskedInvalidConfig: a config.xml that is only valid because a DUPEARR__
// variable overrides one of its values is refused: Dupearr could not start once the variable is
// removed (SEC-008). Not parallel: t.Setenv.
func TestRestoreRejectsEnvMaskedInvalidConfig(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "8080")
	f := newFixture(t)
	_, db := validParts(t, f)
	bad := []byte("<Config><Port>99999</Port><ApiKey>backupkey0123456789abcdef</ApiKey></Config>")
	archive := makeZip(t, zipEntry{name: "config.xml", data: bad}, zipEntry{name: "dupearr.db", data: db})
	for _, opts := range []RestoreOptions{{}, {RestoreSecuritySettings: true}} {
		_, err := f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), opts)
		if !errors.Is(err, ErrInvalidBackup) || !strings.Contains(err.Error(), "port") {
			t.Fatalf("opts %+v: err = %v; want the invalid port refused", opts, err)
		}
		assertNothingStaged(t, f.dir)
	}
}

// TestCreateOmitsSessionKey: backups never carry the session signing key, which would let whoever
// holds an archive forge session cookies for this instance.
func TestCreateOmitsSessionKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := strings.Repeat("5e", 32)
	if err := f.db.Settings().SetValue(ctx, sessionKeySetting, key); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Settings().SetValue(ctx, "plex.clientIdentifier", "keep-me"); err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Create(ctx, TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	raw := readZip(t, filepath.Join(f.dir, BackupsDirName, TypeManual, b.Name))[dbFileName]
	if bytes.Contains(raw, []byte(key)) {
		t.Fatal("the backup's database holds the session signing key")
	}
	if !bytes.Contains(raw, []byte("keep-me")) {
		t.Error("other settings were removed from the backup")
	}
	if v, ok, err := f.db.Settings().GetValue(ctx, sessionKeySetting); err != nil || !ok || v != key {
		t.Errorf("live session key = %q, %v, %v; must be untouched", v, ok, err)
	}
}

// TestApplyPendingRestorePrunesOldGenerations: files replaced by restores hold old secrets; only
// the newest generations are kept.
func TestApplyPendingRestorePrunesOldGenerations(t *testing.T) {
	dir := t.TempDir()
	live := map[string]string{"config.xml": "cfg", "dupearr.db": "db", "notes.bak-20200101T000000Z": "keep",
		"dupearr.db.bak-latest": "keep"}
	for _, st := range []string{"20240101T000000Z", "20250101T000000Z", "20260101T000000Z"} {
		live["config.xml.bak-"+st] = "old"
		live["dupearr.db.bak-"+st] = "old"
		live["dupearr.db.bak-"+st+"-wal"] = "old"
	}
	live["dupearr.db.bak-20240101T000000Z-1"] = "old"
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
	want := []string{"config.xml.bak-20260101T000000Z", "config.xml.bak-" + stamp, "dupearr.db.bak-20260101T000000Z",
		"dupearr.db.bak-20260101T000000Z-wal", "dupearr.db.bak-" + stamp, "dupearr.db.bak-latest", "notes.bak-20200101T000000Z"}
	slices.Sort(want)
	if !slices.Equal(baks, want) {
		t.Errorf("backup files = %v\nwant %v", baks, want)
	}
}

// TestRemoveForeignSchemaObjects: triggers and views planted in the live database (e.g. by a
// backup restored with an earlier version) are removed before the database is used.
func TestRemoveForeignSchemaObjects(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	p := filepath.Join(dir, dbFileName)
	if removed, err := RemoveForeignSchemaObjects(ctx, dir, p); err != nil || len(removed) != 0 {
		t.Fatalf("missing database: %v, %v", removed, err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("the database was created")
	}
	db, err := database.Open(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveForeignSchemaObjects(ctx, dir, p); err != nil || len(removed) != 0 {
		t.Fatalf("clean database: removed %v, %v", removed, err)
	}
	raw, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TRIGGER backdoor AFTER DELETE ON users BEGIN INSERT INTO users (username, password_hash, created_at) VALUES ('evil', 'x', 'now'); END`,
		`CREATE VIEW "odd ""name""" AS SELECT 1`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	removed, err := RemoveForeignSchemaObjects(ctx, dir, p)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed = %v, %v", removed, err)
	}
	if got := schemaSQL(t, p); !equalMaps(got, referenceSchema(t)) {
		t.Errorf("schema after cleanup = %v", got)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, restoreTmpPrefix+"*")); len(entries) > 0 {
		t.Errorf("temporaries left: %v", entries)
	}
}
