package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// openAt opens a database at path and closes it when the test ends.
func openAt(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// newTestDB opens a fresh database in a temp dir.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "dupearr.db"))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// Open / migrations
// ---------------------------------------------------------------------------

func TestOpen_ConnectionPragmas(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	pools := []struct {
		name      string
		db        *sql.DB
		queryOnly int
	}{
		{"writer", d.w, 0},
		{"reader", d.r, 1},
	}
	for _, p := range pools {
		t.Run(p.name, func(t *testing.T) {
			var mode string
			must(t, p.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode))
			if mode != "wal" {
				t.Errorf("journal_mode = %q, want wal", mode)
			}
			checks := []struct {
				pragma string
				want   int
			}{
				{"foreign_keys", 1},
				{"busy_timeout", 10000},
				{"synchronous", 1}, // NORMAL
				{"query_only", p.queryOnly},
			}
			for _, c := range checks {
				var got int
				must(t, p.db.QueryRowContext(ctx, `PRAGMA `+c.pragma).Scan(&got))
				if got != c.want {
					t.Errorf("%s = %d, want %d", c.pragma, got, c.want)
				}
			}
		})
	}
	if _, err := d.r.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('x', 'y')`); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestOpen_MigrationsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	migrations, err := loadMigrations(migrationsFS)
	must(t, err)
	if len(migrations) == 0 {
		t.Fatal("no embedded migrations")
	}

	d, err := Open(ctx, path, testLogger())
	must(t, err)
	must(t, d.Settings().SetValue(ctx, "k", "v"))
	must(t, d.Close())

	for i := range 3 {
		d, err := Open(ctx, path, testLogger())
		if err != nil {
			t.Fatalf("re-open %d: %v", i, err)
		}
		var n, latest int
		must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*), MAX(version) FROM schema_migrations`).Scan(&n, &latest))
		if n != len(migrations) || latest != migrations[len(migrations)-1].version {
			t.Fatalf("schema_migrations: %d rows (latest %d), want %d", n, latest, len(migrations))
		}
		v, ok, err := d.Settings().GetValue(ctx, "k")
		must(t, err)
		if !ok || v != "v" {
			t.Fatalf("data lost after re-open: %q %v", v, ok)
		}
		must(t, d.Close())
	}
}

// TestOpen_UpgradesExistingDatabase: a database created by an older build (only the first
// migration applied, with data) is migrated in place and its rows stay usable.
func TestOpen_UpgradesExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	migrations, err := loadMigrations(migrationsFS)
	must(t, err)
	if len(migrations) < 2 {
		t.Skip("needs at least two migrations")
	}

	// Build a version-1 database by hand, the way the first release left it.
	raw, err := sql.Open(driverName, dsn(path, false))
	must(t, err)
	_, err = raw.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY NOT NULL,
		name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	must(t, err)
	_, err = raw.ExecContext(ctx, migrations[0].sql)
	must(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, ?, ?)`,
		migrations[0].version, migrations[0].name, fmtTime(nowUTC()))
	must(t, err)
	now := fmtTime(nowUTC())
	_, err = raw.ExecContext(ctx, `INSERT INTO duplicate_groups (key, status, first_seen_at, last_seen_at, updated_at)
		VALUES ('movie:tmdb:1', 'pending', ?, ?, ?)`, now, now, now)
	must(t, err)
	must(t, raw.Close())

	d := openAt(t, path)
	var n int
	must(t, d.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n))
	if n != len(migrations) {
		t.Fatalf("schema_migrations has %d rows after upgrade, want %d", n, len(migrations))
	}
	old, err := d.Groups().GetByKey(ctx, "movie:tmdb:1")
	must(t, err)
	g := testGroup("movie:tmdb:1", 1, 2)
	if upsert(t, d, g) || g.ID != old.ID {
		t.Fatalf("upgraded group not updated in place (id %d, old %d)", g.ID, old.ID)
	}
}

func TestOpen_RefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	d, err := Open(ctx, path, testLogger())
	must(t, err)
	_, err = d.w.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'future', '')`)
	must(t, err)
	must(t, d.Close())

	if _, err := Open(ctx, path, testLogger()); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open with a future migration: err = %v, want a 'newer' error", err)
	}
}

func TestOpen_InvalidPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	// SQLite treats files shorter than one page as empty databases, so write a few pages.
	must(t, os.WriteFile(file, []byte(strings.Repeat("definitely not a database\n", 400)), 0o600))
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"missing directory", filepath.Join(dir, "missing", "dupearr.db")},
		{"parent is a file", filepath.Join(file, "dupearr.db")},
		{"not a database", file},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := Open(ctx, tt.path, nil)
			if err == nil {
				_ = d.Close()
				t.Fatalf("Open(%q) succeeded", tt.path)
			}
		})
	}
}

func TestOpen_PathWithURICharacters(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "my data #1 ?x=1&y %20")
	must(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "dupe arr.db")
	d := openAt(t, path)
	if d.Path() != path {
		t.Fatalf("Path() = %q, want %q", d.Path(), path)
	}
	must(t, d.Settings().SetValue(ctx, "k", "v"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created at the literal path: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		must(t, err)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("database file mode = %v, want 0600", st.Mode().Perm())
		}
	}
}

func TestOpen_RelativePathBecomesAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d := openAt(t, "rel.db")
	if !filepath.IsAbs(d.Path()) || filepath.Base(d.Path()) != "rel.db" {
		t.Fatalf("Path() = %q, want an absolute path", d.Path())
	}
}

func TestLoadMigrations(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		want    []int
		wantErr string
	}{
		{
			name: "sorted by version, non-sql ignored",
			fsys: fstest.MapFS{
				"migrations/0010_b.sql":   {Data: []byte("SELECT 1;")},
				"migrations/0002_a.sql":   {Data: []byte("SELECT 1;")},
				"migrations/README.md":    {Data: []byte("x")},
				"migrations/sub/0003.sql": {Data: []byte("x")},
			},
			want: []int{2, 10},
		},
		{name: "missing underscore", fsys: fstest.MapFS{"migrations/0001.sql": {}}, wantErr: "NNNN_description"},
		{name: "non-numeric", fsys: fstest.MapFS{"migrations/abc_x.sql": {}}, wantErr: "invalid version"},
		{name: "zero version", fsys: fstest.MapFS{"migrations/0000_x.sql": {}}, wantErr: "invalid version"},
		{
			name:    "duplicate version",
			fsys:    fstest.MapFS{"migrations/0001_a.sql": {}, "migrations/1_b.sql": {}},
			wantErr: "share version",
		},
		{name: "no directory", fsys: fstest.MapFS{}, wantErr: "read embedded migrations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadMigrations(tt.fsys)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			must(t, err)
			var versions []int
			for _, m := range got {
				versions = append(versions, m.version)
			}
			if fmt.Sprint(versions) != fmt.Sprint(tt.want) {
				t.Fatalf("versions = %v, want %v", versions, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Seed
// ---------------------------------------------------------------------------

func templates(names ...string) []models.Profile {
	out := make([]models.Profile, len(names))
	for i, n := range names {
		isDefault := strings.HasSuffix(n, "*")
		out[i] = models.Profile{
			Name:      strings.TrimSuffix(n, "*"),
			IsDefault: isDefault,
			KeepCount: 1,
			Criteria:  []models.Criterion{{Type: models.CritResolution, Enabled: true, Order: []string{"2160", "1080"}}},
		}
	}
	return out
}

func TestSeed(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string // profiles created before Seed
		templates   []models.Profile
		wantNames   []string
		wantDefault string
	}{
		{"flagged default kept", nil, templates("A", "B*", "C"), []string{"A", "B", "C"}, "B"},
		{"first becomes default when none flagged", nil, templates("A", "B"), []string{"A", "B"}, "A"},
		{"only the first flagged default wins", nil, templates("A*", "B*"), []string{"A", "B"}, "A"},
		{"no templates", nil, nil, []string{}, ""},
		{"existing profiles are left alone", []string{"Mine"}, templates("A*"), []string{"Mine"}, "Mine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d := newTestDB(t)
			for _, n := range tt.existing {
				must(t, d.Profiles().Create(ctx, &models.Profile{Name: n}))
			}
			tpl := append([]models.Profile(nil), tt.templates...)
			for range 2 { // idempotent
				must(t, d.Seed(ctx, tt.templates))
			}
			for i := range tpl {
				if tpl[i].ID != tt.templates[i].ID || tpl[i].IsDefault != tt.templates[i].IsDefault {
					t.Fatal("Seed mutated the caller's templates")
				}
			}
			list, err := d.Profiles().List(ctx)
			must(t, err)
			var names []string
			defaults := 0
			for _, p := range list {
				names = append(names, p.Name)
				if p.IsDefault {
					defaults++
				}
			}
			if fmt.Sprint(names) != fmt.Sprint(tt.wantNames) && !(len(names) == 0 && len(tt.wantNames) == 0) {
				t.Fatalf("profiles = %v, want %v", names, tt.wantNames)
			}
			if tt.wantDefault == "" {
				_, err := d.Profiles().GetDefault(ctx)
				wantNotFound(t, err)
				return
			}
			if defaults != 1 {
				t.Fatalf("%d default profiles, want 1", defaults)
			}
			def, err := d.Profiles().GetDefault(ctx)
			must(t, err)
			if def.Name != tt.wantDefault {
				t.Fatalf("default = %q, want %q", def.Name, tt.wantDefault)
			}
			if len(def.Criteria) == 0 && len(tt.existing) == 0 {
				t.Fatal("seeded profile lost its criteria")
			}
		})
	}
}

func TestSeed_SettingsDocument(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	if _, ok, err := d.Settings().GetValue(ctx, settingsKey); err != nil || ok {
		t.Fatalf("settings document before Seed: ok=%v err=%v", ok, err)
	}
	must(t, d.Seed(ctx, nil))
	if _, ok, err := d.Settings().GetValue(ctx, settingsKey); err != nil || !ok {
		t.Fatalf("settings document after Seed: ok=%v err=%v", ok, err)
	}

	// Seed never overwrites saved settings.
	s := models.DefaultSettings()
	s.DryRun = false
	s.MaxDeletionsPerRun = 3
	must(t, d.Settings().Save(ctx, s))
	must(t, d.Seed(ctx, nil))
	got, err := d.Settings().Get(ctx)
	must(t, err)
	if got.DryRun || got.MaxDeletionsPerRun != 3 {
		t.Fatalf("Seed overwrote settings: %+v", got)
	}
}

func TestSeed_RepairsMissingDefault(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	must(t, d.Profiles().Create(ctx, &models.Profile{Name: "A"}))
	must(t, d.Profiles().Create(ctx, &models.Profile{Name: "B"}))
	_, err := d.w.ExecContext(ctx, `UPDATE profiles SET is_default = 0`)
	must(t, err)

	must(t, d.Seed(ctx, templates("T*")))
	def, err := d.Profiles().GetDefault(ctx)
	must(t, err)
	if def.Name != "A" {
		t.Fatalf("default = %q, want the oldest profile A", def.Name)
	}
	list, err := d.Profiles().List(ctx)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("templates inserted although profiles exist: %d profiles", len(list))
	}
}

// ---------------------------------------------------------------------------
// Ping / Vacuum / BackupTo / Close
// ---------------------------------------------------------------------------

func TestPingAndVacuum(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	must(t, d.Ping(ctx))
	for i := range 50 {
		must(t, d.History().Add(ctx, &models.HistoryEvent{EventType: models.EventScanCompleted, Title: fmt.Sprint(i)}))
	}
	_, err := d.History().DeleteOlderThan(ctx, time.Now().Add(time.Hour))
	must(t, err)
	must(t, d.Vacuum(ctx))
	must(t, d.Ping(ctx))
}

func TestBackupTo(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	must(t, d.Seed(ctx, templates("Default*")))
	must(t, d.Settings().SetValue(ctx, "plex.clientIdentifier", "abc"))
	g := testGroup("movie:tmdb:1", 1, 10)
	_, err := d.Groups().Upsert(ctx, g)
	must(t, err)

	dir := t.TempDir()
	dst := filepath.Join(dir, "backup.db")
	must(t, d.BackupTo(ctx, dst))

	t.Run("destination must not exist", func(t *testing.T) {
		err := d.BackupTo(ctx, dst)
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("second BackupTo: err = %v, want fs.ErrExist", err)
		}
	})
	t.Run("empty destination", func(t *testing.T) {
		if err := d.BackupTo(ctx, " "); err == nil {
			t.Fatal("BackupTo(\"\") succeeded")
		}
	})
	t.Run("failed snapshot leaves no file", func(t *testing.T) {
		missing := filepath.Join(dir, "missing-dir", "b.db")
		if err := d.BackupTo(ctx, missing); err == nil {
			t.Fatal("BackupTo into a missing directory succeeded")
		}
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		p := filepath.Join(dir, "cancelled.db")
		if err := d.BackupTo(cctx, p); err == nil {
			t.Fatal("BackupTo with a cancelled context succeeded")
		}
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("partial backup left behind: %v", err)
		}
	})
	t.Run("backup is an openable database with the data", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			st, err := os.Stat(dst)
			must(t, err)
			if st.Mode().Perm() != 0o600 {
				t.Errorf("backup mode = %v, want 0600", st.Mode().Perm())
			}
		}
		b := openAt(t, dst)
		v, ok, err := b.Settings().GetValue(ctx, "plex.clientIdentifier")
		must(t, err)
		if !ok || v != "abc" {
			t.Fatalf("backup value = %q/%v", v, ok)
		}
		got, err := b.Groups().GetByKey(ctx, "movie:tmdb:1")
		must(t, err)
		if len(got.Files) != 2 {
			t.Fatalf("backup group has %d files", len(got.Files))
		}
		var integrity string
		must(t, b.r.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity))
		if integrity != "ok" {
			t.Fatalf("integrity_check = %q", integrity)
		}
	})
}

func TestClose_Idempotent(t *testing.T) {
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "x.db"), nil)
	must(t, err)
	must(t, d.Close())
	must(t, d.Close())
	if err := d.Ping(context.Background()); err == nil {
		t.Fatal("Ping after Close succeeded")
	}
}

func TestCancelledContext(t *testing.T) {
	d := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Settings().SetValue(ctx, "k", "v"); err == nil {
		t.Error("write with a cancelled context succeeded")
	}
	if _, err := d.Groups().List(ctx, store.GroupFilter{}, store.Paging{}); err == nil {
		t.Error("read with a cancelled context succeeded")
	}
	if _, err := d.Groups().Upsert(ctx, testGroup("k", 1, 1)); err == nil {
		t.Error("transaction with a cancelled context succeeded")
	}
}

// TestConcurrentWriters hammers the store from many goroutines (API + scanner + executor style)
// and requires that no operation fails, in particular never with SQLITE_BUSY.
func TestConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	must(t, d.Seed(ctx, templates("Default*")))

	const (
		workers = 16
		ops     = 25
	)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	record := func(err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ops {
				switch (w + i) % 6 {
				case 0:
					g := testGroup(fmt.Sprintf("movie:tmdb:%d", i%7), int64(w), int64(i))
					_, err := d.Groups().Upsert(ctx, g)
					record(err)
				case 1:
					record(d.History().Add(ctx, &models.HistoryEvent{EventType: models.EventGroupDetected, Title: "x"}))
				case 2:
					// Approve-style: queue the removal of the group's loser. A concurrent
					// MarkUnseenResolved may have resolved the group meanwhile: then the store
					// must refuse the removal (ErrNotRemovable), which is not a failure here.
					g := testGroup(fmt.Sprintf("movie:tmdb:%d", i%7), int64(w), int64(i))
					if _, err := d.Groups().Upsert(ctx, g); err != nil {
						record(err)
						continue
					}
					err := d.Actions().Create(ctx, &models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID, Title: "a"})
					if errors.Is(err, ErrNotRemovable) {
						err = nil
					}
					record(err)
				case 3:
					record(d.Settings().SetValue(ctx, fmt.Sprintf("k%d", w), fmt.Sprint(i)))
				case 4:
					_, err := d.Groups().List(ctx, store.GroupFilter{Search: "movie"}, store.Paging{PageSize: 50})
					record(err)
					_, err = d.Groups().Stats(ctx)
					record(err)
				case 5:
					_, err := d.Actions().CancelPendingForGroup(ctx, int64(i))
					record(err)
					_, err = d.Groups().MarkUnseenResolved(ctx, int64(i), []int64{1, 2})
					record(err)
					// Executor-style: start whatever is queued; losing the race against a
					// cancellation is expected (ErrActionNotPending), anything else is not.
					queued, err := d.Actions().ListPending(ctx, 5)
					record(err)
					for _, a := range queued {
						a.Status = models.ActionRunning
						if err := d.Actions().Update(ctx, &a); err != nil && !errors.Is(err, ErrActionNotPending) {
							record(err)
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		t.Error(err)
	}
	if n := len(errs); n > 0 {
		t.Fatalf("%d concurrent operations failed", n)
	}
}

// TestWrite_PanicReleasesWriter: a panic inside a write transaction must not leak the transaction
// (it would pin the only writer connection and hang every later write of the process).
func TestWrite_PanicReleasesWriter(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate")
			}
		}()
		_ = d.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('half', 'written')`); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	must(t, d.Settings().SetValue(wctx, "after", "panic"))
	if _, ok, err := d.Settings().GetValue(ctx, "half"); err != nil || ok {
		t.Fatalf("the panicking transaction was committed (ok=%v, err=%v)", ok, err)
	}
}

// TestOpen_ConcurrentFirstOpen: several processes opening a brand-new database at once (the
// server and `dupearr reset-auth`, say) all succeed; the migration is applied exactly once.
func TestOpen_ConcurrentFirstOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dupearr.db")
	const n = 6
	var (
		wg   sync.WaitGroup
		errs = make([]error, n)
		dbs  = make([]*DB, n)
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dbs[i], errs[i] = Open(ctx, path, testLogger())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open %d: %v", i, err)
			continue
		}
		t.Cleanup(func() { _ = dbs[i].Close() })
	}
	if t.Failed() {
		return
	}
	migrations, err := loadMigrations(migrationsFS)
	must(t, err)
	var rows int
	must(t, dbs[0].r.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows))
	if rows != len(migrations) {
		t.Fatalf("schema_migrations has %d rows, want %d", rows, len(migrations))
	}

	// Deterministic form of the race: migrate read the applied list before another process
	// applied the migration. Re-applying must be skipped, not fail on "table already exists".
	for _, m := range migrations {
		applied, err := dbs[0].applyMigration(ctx, m)
		if err != nil || applied {
			t.Fatalf("re-applying %s: applied=%v err=%v", m.name, applied, err)
		}
	}
	// A failing migration leaves nothing behind (its transaction is rolled back).
	bad := migration{version: 9999, name: "9999_bad", sql: `CREATE TABLE half_done (x); SELECT * FROM missing_table;`}
	if applied, err := dbs[0].applyMigration(ctx, bad); err == nil || applied {
		t.Fatalf("bad migration: applied=%v err=%v", applied, err)
	}
	var tables int
	must(t, dbs[0].r.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&tables))
	if tables != 0 {
		t.Fatal("a failed migration left a table behind")
	}
}

// TestWriteLockDoesNotBlockReaders shows the pool design: while a write transaction holds the
// lock, readers still get the last committed snapshot, and a second writer queues in the pool
// (honouring its context) instead of failing with SQLITE_BUSY.
func TestWriteLockDoesNotBlockReaders(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	g := testGroup("a", 1, 1)
	upsert(t, d, g)

	tx, err := d.w.BeginTx(ctx, nil) // BEGIN IMMEDIATE on the only writer connection
	must(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE duplicate_groups SET title = 'uncommitted'`)
	must(t, err)

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	page, err := d.Groups().List(rctx, store.GroupFilter{}, store.Paging{})
	must(t, err)
	if len(page.Records) != 1 || page.Records[0].Title != g.Title {
		t.Fatalf("reader saw %+v, want the committed title %q", page.Records, g.Title)
	}

	wctx, wcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer wcancel()
	if err := d.Settings().SetValue(wctx, "k", "v"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued writer: err = %v, want context.DeadlineExceeded", err)
	}

	must(t, tx.Rollback())
	must(t, d.Settings().SetValue(ctx, "k", "v"))
}

// TestCorruptRowsReturnErrors: hand-edited or damaged JSON/time columns surface as errors, never
// as panics or silently empty data.
func TestCorruptRowsReturnErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		corrupt string
		read    func(d *DB) error
	}{
		{"group flags", `UPDATE duplicate_groups SET flags = '{'`, func(d *DB) error {
			_, err := d.Groups().GetByKey(ctx, "a")
			return err
		}},
		{"group time", `UPDATE duplicate_groups SET last_seen_at = 'yesterday'`, func(d *DB) error {
			_, err := d.Groups().List(ctx, store.GroupFilter{}, store.Paging{})
			return err
		}},
		{"retained overrides", `UPDATE duplicate_groups SET retained_overrides = '['`, func(d *DB) error {
			_, err := d.Groups().Upsert(ctx, testGroup("a", 1, 2))
			return err
		}},
		{"file version", `UPDATE group_files SET version = '[1'`, func(d *DB) error {
			_, err := d.Groups().ListByRatingKeys(ctx, 1, []string{"rk-a"})
			return err
		}},
		{"action paths", `UPDATE actions SET paths = 'x'`, func(d *DB) error {
			_, err := d.Actions().ListPending(ctx, 0)
			return err
		}},
		{"profile criteria", `UPDATE profiles SET criteria = '{"a":'`, func(d *DB) error {
			_, err := d.Profiles().GetDefault(ctx)
			return err
		}},
		{"scan stats", `UPDATE scan_runs SET stats = '['`, func(d *DB) error {
			_, err := d.ScanRuns().Latest(ctx)
			return err
		}},
		{"task time", `UPDATE tasks SET next_execution = 'soon'`, func(d *DB) error {
			_, err := d.Tasks().List(ctx)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDB(t)
			g := testGroup("a", 1, 1)
			upsert(t, d, g)
			must(t, d.Actions().Create(ctx, &models.Action{GroupID: g.ID, GroupFileID: g.Files[1].ID}))
			must(t, d.Profiles().Create(ctx, &models.Profile{Name: "p"}))
			must(t, d.ScanRuns().Create(ctx, &models.ScanRun{}))
			must(t, d.Tasks().Upsert(ctx, &models.ScheduledTask{TaskName: "x", NextExecution: ptr(time.Now())}))
			must(t, tt.read(d)) // readable before the corruption
			_, err := d.w.ExecContext(ctx, tt.corrupt)
			must(t, err)
			if err := tt.read(d); err == nil {
				t.Fatal("corrupt row read without error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// codec helpers
// ---------------------------------------------------------------------------

func TestTimeLayout_SortsChronologically(t *testing.T) {
	base := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(100 * time.Millisecond),
		base.Add(123456789),
		base.Add(time.Second),
		base.Add(time.Second + time.Nanosecond),
	}
	for i := 1; i < len(times); i++ {
		a, b := fmtTime(times[i-1]), fmtTime(times[i])
		if !(a < b) {
			t.Errorf("%q !< %q", a, b)
		}
	}
	// Non-UTC input is stored as UTC and round-trips exactly.
	loc := time.FixedZone("x", 5*3600)
	in := time.Date(2025, 6, 1, 12, 0, 0, 42, loc)
	s := fmtTime(in)
	if !strings.HasSuffix(s, "Z") {
		t.Fatalf("%q is not UTC", s)
	}
	out, err := parseTime(s)
	must(t, err)
	if !out.Equal(in) {
		t.Fatalf("round trip %v != %v", out, in)
	}
	if _, err := parseTime("garbage"); err == nil {
		t.Fatal("parseTime(garbage) succeeded")
	}
}

func TestSortSpecResolve(t *testing.T) {
	spec := sortSpec{
		columns: map[string]sortColumn{
			"title":     {exprs: []string{"title"}, defaultDir: sortAscending},
			"createdAt": {exprs: []string{"created_at"}, defaultDir: sortDescending},
		},
		defaultKey: "createdAt",
		tiebreak:   "id",
	}
	tests := []struct {
		name              string
		in                store.Paging
		page, size        int
		key, dir, orderBy string
	}{
		{"defaults", store.Paging{}, 1, 20, "createdAt", sortDescending, "created_at DESC, id DESC"},
		{"clamped", store.Paging{Page: -3, PageSize: 5000}, 1, 1000, "createdAt", sortDescending, "created_at DESC, id DESC"},
		{"unknown key falls back", store.Paging{SortKey: "id; DROP TABLE x", SortDirection: "ascending"}, 1, 20, "createdAt", sortAscending, "created_at ASC, id ASC"},
		{"case-insensitive key, key default dir", store.Paging{Page: 3, PageSize: 10, SortKey: "TITLE"}, 3, 10, "title", sortAscending, "title ASC, id ASC"},
		{"explicit direction", store.Paging{SortKey: "title", SortDirection: "descending"}, 1, 20, "title", sortDescending, "title DESC, id DESC"},
		{"short direction", store.Paging{SortKey: "title", SortDirection: "DESC"}, 1, 20, "title", sortDescending, "title DESC, id DESC"},
		{"bogus direction", store.Paging{SortKey: "title", SortDirection: "sideways"}, 1, 20, "title", sortAscending, "title ASC, id ASC"},
		{"page number cannot overflow the offset", store.Paging{Page: math.MaxInt, PageSize: 1000}, math.MaxInt / 1000, 1000,
			"createdAt", sortDescending, "created_at DESC, id DESC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := spec.resolve(tt.in)
			if r.page != tt.page || r.size != tt.size || r.key != tt.key || r.dir != tt.dir || r.orderBy != tt.orderBy {
				t.Fatalf("resolve(%+v) = %+v", tt.in, r)
			}
			if r.offset() != (tt.page-1)*tt.size {
				t.Fatalf("offset = %d", r.offset())
			}
		})
	}
}

func TestWrap(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	if wrap(nil, "x") != nil {
		t.Fatal("wrap(nil) != nil")
	}
	wantNotFound(t, wrap(sql.ErrNoRows, "get %d", 1))

	// A UNIQUE violation is reported as ErrConstraint.
	_, err := d.w.ExecContext(ctx, `INSERT INTO exclusions (kind, value, created_at) VALUES ('a', 'b', '')`)
	must(t, err)
	_, err = d.w.ExecContext(ctx, `INSERT INTO exclusions (kind, value, created_at) VALUES ('a', 'b', '')`)
	if werr := wrap(err, "insert"); !errors.Is(werr, ErrConstraint) {
		t.Fatalf("wrap(unique violation) = %v, want ErrConstraint", werr)
	}
	// A foreign-key violation too (foreign_keys is on).
	_, err = d.w.ExecContext(ctx, `INSERT INTO libraries (server_id, section_key, updated_at) VALUES (999, '1', '')`)
	if werr := wrap(err, "insert"); !errors.Is(werr, ErrConstraint) {
		t.Fatalf("wrap(fk violation) = %v, want ErrConstraint", werr)
	}
}
