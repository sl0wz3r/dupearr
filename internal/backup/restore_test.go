package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// zipEntry describes one entry of a hand-made archive.
type zipEntry struct {
	name  string
	data  []byte
	mode  fs.FileMode
	flags uint16
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		h.Flags |= e.flags
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(e.name, "/") {
			if _, err := w.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// validParts returns a valid config.xml and database snapshot of the fixture.
func validParts(t *testing.T, f *fixture) (cfg, db []byte) {
	t.Helper()
	cfg, err := os.ReadFile(f.cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := f.db.BackupTo(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	db, err = os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, db
}

// foreignDB returns a healthy SQLite database that is not a Dupearr database.
func foreignDB(t *testing.T) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE series (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertNothingStaged checks that no restore is staged and no temporaries are left.
func assertNothingStaged(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore") {
			t.Errorf("unexpected %s in the data directory", e.Name())
		}
	}
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, RestoreDirName))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestRestoreUploadRejectsInvalidArchives(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	corrupt := append([]byte(sqliteHeader), bytes.Repeat([]byte{0xAB}, 4096)...)
	good := func() []zipEntry {
		return []zipEntry{{name: "config.xml", data: cfg}, {name: "dupearr.db", data: db}}
	}
	many := good()
	for i := range maxEntries {
		many = append(many, zipEntry{name: "extra" + string(rune('a'+i)), data: []byte("x")})
	}

	tests := []struct {
		name string
		data []byte
		size int64 // 0 = len(data)
	}{
		{"empty upload", nil, 0},
		{"not a zip", []byte("hello world, definitely not a zip file"), 0},
		{"raw sqlite database", db, 0},
		{"raw config.xml", cfg, 0},
		{"empty zip", makeZip(t), 0},
		{"missing config", makeZip(t, zipEntry{name: "dupearr.db", data: db}), 0},
		{"missing database", makeZip(t, zipEntry{name: "config.xml", data: cfg}), 0},
		{"traversal", makeZip(t, zipEntry{name: "../config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"absolute path", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "/tmp/dupearr.db", data: db}), 0},
		{"nested folder", makeZip(t, zipEntry{name: "backup/config.xml", data: cfg}, zipEntry{name: "backup/dupearr.db", data: db}), 0},
		{"backslash path", makeZip(t, zipEntry{name: `..\config.xml`, data: cfg}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"drive letter", makeZip(t, zipEntry{name: "C:config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"extra directory entry", makeZip(t, append(good(), zipEntry{name: "__MACOSX/"})...), 0},
		{"symlink entry", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}), 0},
		{"duplicate entry", makeZip(t, append(good(), zipEntry{name: "CONFIG.XML", data: cfg})...), 0},
		{"encrypted entry", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db, flags: 0x1}), 0},
		{"too many entries", makeZip(t, many...), 0},
		{"database not sqlite", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: []byte("SQLite? no")}), 0},
		{"database corrupt", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: corrupt}), 0},
		{"database truncated", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db[:len(db)/2]}), 0},
		{"not a dupearr database", makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: foreignDB(t)}), 0},
		{"config not xml", makeZip(t, zipEntry{name: "config.xml", data: []byte("{json: true}")}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"config wrong root", makeZip(t, zipEntry{name: "config.xml", data: []byte("<Other><Port>1</Port></Other>")}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"config invalid value", makeZip(t, zipEntry{name: "config.xml", data: []byte("<Config><Port>99999999</Port></Config>")}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"config empty", makeZip(t, zipEntry{name: "config.xml", data: nil}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"config too large", makeZip(t, zipEntry{name: "config.xml", data: bytes.Repeat([]byte(" "), int(maxConfigSize)+1)}, zipEntry{name: "dupearr.db", data: db}), 0},
		{"truncated upload", makeZip(t, good()...), -1},
		{"declared too large", makeZip(t, good()...), MaxUploadSize + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := tt.size
			switch size {
			case 0:
				size = int64(len(tt.data))
			case -1:
				size = int64(len(tt.data)) + 10
			}
			err := f.svc.RestoreUpload(context.Background(), bytes.NewReader(tt.data), size)
			if !errors.Is(err, ErrInvalidBackup) {
				t.Fatalf("err = %v, want ErrInvalidBackup", err)
			}
			assertNothingStaged(t, f.dir)
		})
	}
}

func TestRestoreUploadStagesWithoutTouchingLiveFiles(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	liveCfg, _ := os.ReadFile(f.cfg.Path())
	archive := makeZip(t,
		zipEntry{name: "Config.xml", data: cfg}, // case-insensitive like the *arr apps
		zipEntry{name: "dupearr.db", data: db},
		zipEntry{name: "INFO", data: []byte("v1.0.0\n")},
		zipEntry{name: "README.txt", data: []byte("ignored")},
	)
	if err := f.svc.RestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatal(err)
	}
	if got := stagedFiles(t, f.dir); strings.Join(got, ",") != "config.xml,dupearr.db" {
		t.Fatalf("staged = %v, want exactly config.xml and dupearr.db", got)
	}
	// The staged database is rebuilt from the archive's rows: same content, this build's schema.
	orig := filepath.Join(t.TempDir(), "orig.db")
	if err := os.WriteFile(orig, db, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := dumpTables(t, filepath.Join(f.dir, RestoreDirName, dbFileName)), dumpTables(t, orig); !equalMaps(got, want) {
		t.Errorf("staged database content differs from the archive:\n got %v\nwant %v", got, want)
	}
	if now, _ := os.ReadFile(f.cfg.Path()); !bytes.Equal(now, liveCfg) {
		t.Error("live config.xml changed by staging")
	}
	if err := f.db.Ping(context.Background()); err != nil {
		t.Errorf("live database unusable after staging: %v", err)
	}
	for _, e := range []string{".restore-tmp-", ".restore-upload-", ".restore-cfgcheck-", ".restore.old-"} {
		matches, _ := filepath.Glob(filepath.Join(f.dir, e+"*"))
		if len(matches) > 0 {
			t.Errorf("temporaries left: %v", matches)
		}
	}

	// Staging again replaces the previous staged restore.
	if err := f.svc.RestoreUpload(context.Background(), bytes.NewReader(archive), 0); err != nil {
		t.Fatal(err)
	}
	if got := stagedFiles(t, f.dir); len(got) != 2 {
		t.Errorf("staged after replace = %v", got)
	}
}

func TestRestoreUploadCancelled(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.svc.RestoreUpload(ctx, bytes.NewReader(archive), 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if err := f.svc.RestoreUpload(context.Background(), nil, 0); !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("nil reader err = %v", err)
	}
	assertNothingStaged(t, f.dir)
}

func TestExtractEnforcesDecompressedLimit(t *testing.T) {
	archive := makeZip(t, zipEntry{name: "dupearr.db", data: bytes.Repeat([]byte{0}, 1<<16)})
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := extract(context.Background(), zr.File[0], dst, 1024); !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("zip bomb: err = %v", err)
	}
}

func TestVerifyDatabaseAcceptsWALModeFileWithoutSideFiles(t *testing.T) {
	// A user-made zip may carry a raw copy of a WAL-mode database; it must verify without
	// creating -wal/-shm files next to it.
	dir := t.TempDir()
	p := filepath.Join(dir, "dupearr.db")
	db, err := database.Open(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	copyDir := t.TempDir()
	raw, _ := os.ReadFile(p)
	cp := filepath.Join(copyDir, "dupearr.db")
	if err := os.WriteFile(cp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyDatabase(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(copyDir)
	if len(entries) != 1 {
		t.Errorf("verification created files: %v", entries)
	}
}

func TestRestoreByIDAndApply(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := models.DefaultSettings()
	s.ScanIntervalMinutes = 111
	if err := f.db.Settings().Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Create(ctx, TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	backupCfg := readZip(t, filepath.Join(f.dir, BackupsDirName, TypeManual, b.Name))[configFileName]

	// Change the live state after the backup.
	s.ScanIntervalMinutes = 999
	if err := f.db.Settings().Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cfg.Update(func(c *config.Config) { c.InstanceName = "Changed" }); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.Restore(ctx, 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
	if err := f.svc.Restore(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil { // the process restarts before applying
		t.Fatal(err)
	}

	applied, err := ApplyPendingRestore(f.dir)
	if err != nil || !applied {
		t.Fatalf("apply = %v, %v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, RestoreDirName)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("staged restore not removed")
	}
	if got, _ := os.ReadFile(filepath.Join(f.dir, configFileName)); !bytes.Equal(got, backupCfg) {
		t.Error("config.xml not restored")
	}
	baks, _ := filepath.Glob(filepath.Join(f.dir, "*.bak-*"))
	if len(baks) < 2 {
		t.Errorf("replaced files not kept: %v", baks)
	}

	db, err := database.Open(ctx, filepath.Join(f.dir, dbFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.Settings().Get(ctx)
	if err != nil || got.ScanIntervalMinutes != 111 {
		t.Errorf("restored settings = %d, %v; want 111", got.ScanIntervalMinutes, err)
	}

	if applied, err := ApplyPendingRestore(f.dir); applied || err != nil {
		t.Errorf("second apply = %v, %v", applied, err)
	}
}

// stageRaw writes a staged restore directly (as Restore would have).
func stageRaw(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	sd := filepath.Join(dir, RestoreDirName)
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sd, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sd
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()+"/"] = ""
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func TestApplyPendingRestore(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	const stamp = "20260922T080000Z"
	const oldStamp = "20260101T000000Z"
	tests := []struct {
		name        string
		live        map[string]string
		staged      map[string]string // nil = no .restore
		wantApplied bool
		wantErr     bool
		want        map[string]string // resulting data dir (files only; "x/" = dir)
	}{
		{
			name:        "nothing staged",
			live:        map[string]string{"config.xml": "old cfg", "dupearr.db": "old db"},
			wantApplied: false,
			want:        map[string]string{"config.xml": "old cfg", "dupearr.db": "old db"},
		},
		{
			name: "full swap keeps old files and side files",
			live: map[string]string{
				"config.xml": "old cfg", "dupearr.db": "old db", "dupearr.db-wal": "old wal",
				"dupearr.db-shm": "old shm", "dupearr.db-journal": "old journal", "other.txt": "keep",
			},
			staged:      map[string]string{"config.xml": "new cfg", "dupearr.db": "new db"},
			wantApplied: true,
			want: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db", "other.txt": "keep",
				"config.xml.bak-" + stamp: "old cfg", "dupearr.db.bak-" + stamp: "old db",
				"dupearr.db.bak-" + stamp + "-wal": "old wal", "dupearr.db.bak-" + stamp + "-journal": "old journal",
			},
		},
		{
			name:        "fresh data dir",
			live:        map[string]string{},
			staged:      map[string]string{"config.xml": "new cfg", "dupearr.db": "new db"},
			wantApplied: true,
			want:        map[string]string{"config.xml": "new cfg", "dupearr.db": "new db"},
		},
		{
			name:        "incomplete staging is discarded",
			live:        map[string]string{"config.xml": "old cfg", "dupearr.db": "old db"},
			staged:      map[string]string{"config.xml": "new cfg"},
			wantApplied: false,
			want:        map[string]string{"config.xml": "old cfg", "dupearr.db": "old db"},
		},
		{
			name: "resume after the database was swapped",
			live: map[string]string{
				"config.xml": "old cfg", "dupearr.db": "new db", "dupearr.db.bak-" + oldStamp: "old db",
			},
			staged:      map[string]string{applyingMarker: oldStamp + "\n", "config.xml": "new cfg"},
			wantApplied: true,
			want: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db",
				"config.xml.bak-" + oldStamp: "old cfg", "dupearr.db.bak-" + oldStamp: "old db",
			},
		},
		{
			name: "resume after the wal was moved",
			live: map[string]string{
				"config.xml": "old cfg", "dupearr.db": "old db", "dupearr.db.bak-" + oldStamp + "-wal": "old wal",
			},
			staged:      map[string]string{applyingMarker: oldStamp, "config.xml": "new cfg", "dupearr.db": "new db"},
			wantApplied: true,
			want: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db",
				"config.xml.bak-" + oldStamp: "old cfg", "dupearr.db.bak-" + oldStamp: "old db",
				"dupearr.db.bak-" + oldStamp + "-wal": "old wal",
			},
		},
		{
			name: "resume after everything was moved",
			live: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db",
				"config.xml.bak-" + oldStamp: "old cfg", "dupearr.db.bak-" + oldStamp: "old db",
			},
			staged:      map[string]string{applyingMarker: oldStamp},
			wantApplied: true,
			want: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db",
				"config.xml.bak-" + oldStamp: "old cfg", "dupearr.db.bak-" + oldStamp: "old db",
			},
		},
		{
			name: "backup name collision",
			live: map[string]string{
				"config.xml": "old cfg", "dupearr.db": "old db", "dupearr.db.bak-" + stamp: "older db",
			},
			staged:      map[string]string{"config.xml": "new cfg", "dupearr.db": "new db"},
			wantApplied: true,
			want: map[string]string{
				"config.xml": "new cfg", "dupearr.db": "new db", "config.xml.bak-" + stamp: "old cfg",
				"dupearr.db.bak-" + stamp: "older db", "dupearr.db.bak-" + stamp + "-1": "old db",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.live)
			if tt.staged != nil {
				stageRaw(t, dir, tt.staged)
			}
			applied, err := applyPendingRestore(dir, now)
			if (err != nil) != tt.wantErr || applied != tt.wantApplied {
				t.Fatalf("apply = %v, %v; want applied=%v err=%v", applied, err, tt.wantApplied, tt.wantErr)
			}
			if got := readFiles(t, dir); !equalMaps(got, tt.want) {
				t.Errorf("data dir = %v\nwant       %v", got, tt.want)
			}
		})
	}
}

func TestApplyPendingRestoreRefusesNonFolder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{RestoreDirName: "not a folder", "config.xml": "cfg"})
	if applied, err := ApplyPendingRestore(dir); applied || err == nil {
		t.Fatalf("apply = %v, %v; want an error", applied, err)
	}
	if got := readFiles(t, dir)["config.xml"]; got != "cfg" {
		t.Error("live config touched")
	}
}

func TestApplyPendingRestoreCleansTemporaries(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{".restore-tmp-abc", ".restore-cfgcheck-1", ".restore.old-x", "Backups/.tmp-backup-9", "Backups/manual", ".restorex", "keep"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, dir, map[string]string{".restore-upload-1.zip": "x"})
	if applied, err := ApplyPendingRestore(dir); applied || err != nil {
		t.Fatalf("apply = %v, %v", applied, err)
	}
	got := readFiles(t, dir)
	want := map[string]string{"Backups/": "", ".restorex/": "", "keep/": ""}
	if !equalMaps(got, want) {
		t.Errorf("data dir = %v, want %v", got, want)
	}
	if b := readFiles(t, filepath.Join(dir, "Backups")); !equalMaps(b, map[string]string{"manual/": ""}) {
		t.Errorf("Backups = %v", b)
	}
}

// seedRemovals adds a queued group with a pending, a running and a finished removal, and an
// untouched pending group, to the fixture database.
func seedRemovals(t *testing.T, db *database.DB) (queued, other int64, actions map[models.ActionStatus]int64) {
	t.Helper()
	ctx := context.Background()
	group := func(key string) *models.DuplicateGroup {
		file := func(vk string, d models.Decision) models.GroupFile {
			return models.GroupFile{
				Version: models.MediaVersion{Key: vk, ServerID: 1, RatingKey: "rk-" + key, Resolution: models.Res1080,
					Parts: []models.MediaPart{{ID: 1, Path: "/media/" + vk + ".mkv", Size: 1 << 30}}},
				Decision: d, EngineDecision: d,
			}
		}
		g := &models.DuplicateGroup{Key: key, MediaType: models.MediaTypeMovie, Title: "Movie " + key, ServerID: 1,
			LibraryIDs: []int64{1}, Status: models.GroupPending, ProfileID: 1,
			Files: []models.GroupFile{file(key+"/keep", models.DecisionKeep), file(key+"/remove", models.DecisionRemove)}}
		if _, err := db.Groups().Upsert(ctx, g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := group("movie:tmdb:1")
	if err := db.Groups().UpdateStatus(ctx, g.ID, models.GroupQueued, ""); err != nil {
		t.Fatal(err)
	}
	actions = map[models.ActionStatus]int64{}
	for _, st := range []models.ActionStatus{models.ActionPending, models.ActionRunning, models.ActionSucceeded} {
		a := &models.Action{GroupID: g.ID, VersionKey: g.Key + "/remove", Title: g.Title, Paths: []string{"/media/x.mkv"},
			Status: st, Message: "original"}
		if err := db.Actions().Create(ctx, a); err != nil {
			t.Fatalf("create %s action: %v", st, err)
		}
		actions[st] = a.ID
	}
	return g.ID, group("movie:tmdb:2").ID, actions
}

// TestRestoreNeverResumesRemovals checks that removals queued or running when the backup was
// taken are cancelled in the restored database, so a restore can never resume deletions.
func TestRestoreNeverResumesRemovals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	queued, other, actions := seedRemovals(t, f.db)
	b, err := f.svc.Create(ctx, TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	// The backup itself is unchanged: it still holds the queued removal.
	if err := f.svc.Restore(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := stagedFiles(t, f.dir); len(got) != 2 {
		t.Fatalf("staged files = %v; want only config.xml and dupearr.db (no journal/wal side files)", got)
	}
	// The live database is untouched until the restart.
	if a, err := f.db.Actions().Get(ctx, actions[models.ActionPending]); err != nil || a.Status != models.ActionPending {
		t.Fatalf("live pending action = %+v, %v", a, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(f.dir); err != nil || !applied {
		t.Fatalf("apply = %v, %v", applied, err)
	}

	db, err := database.Open(ctx, filepath.Join(f.dir, dbFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[models.ActionStatus]models.ActionStatus{
		models.ActionPending:   models.ActionCancelled,
		models.ActionRunning:   models.ActionFailed,
		models.ActionSucceeded: models.ActionSucceeded,
	}
	for from, to := range want {
		a, err := db.Actions().Get(ctx, actions[from])
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != to {
			t.Errorf("%s action restored as %s; want %s", from, a.Status, to)
		}
		if from != models.ActionSucceeded && (a.FinishedAt == nil || !strings.Contains(a.Message, "restored")) {
			t.Errorf("%s action = %+v; want a finish time and an explanation", from, a)
		}
		if from == models.ActionSucceeded && a.Message != "original" {
			t.Errorf("finished action changed: %+v", a)
		}
	}
	if pending, err := db.Actions().ListPending(ctx, 100); err != nil || len(pending) != 0 {
		t.Errorf("pending actions after restore = %+v, %v", pending, err)
	}
	g, err := db.Groups().Get(ctx, queued)
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != models.GroupPending || !strings.Contains(g.StatusReason, "restored") {
		t.Errorf("queued group restored as %s (%q); want pending for re-approval", g.Status, g.StatusReason)
	}
	if o, err := db.Groups().Get(ctx, other); err != nil || o.Status != models.GroupPending || o.StatusReason != "" {
		t.Errorf("unrelated group = %+v, %v", o, err)
	}
}

// withMigration returns a copy of a database snapshot with an extra schema migration recorded.
func withMigration(t *testing.T, db []byte, version int) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "newer.db")
	if err := os.WriteFile(p, db, 0o600); err != nil {
		t.Fatal(err)
	}
	sdb, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', '2030-01-01T00:00:00Z')`, version); err != nil {
		t.Fatal(err)
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

// TestRestoreRejectsNewerSchema checks that a backup written by a newer Dupearr (which the
// database layer would refuse to open, leaving Dupearr unable to start) is rejected up front.
func TestRestoreRejectsNewerSchema(t *testing.T) {
	f := newFixture(t)
	cfg, db := validParts(t, f)
	newer := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: withMigration(t, db, 9999)})
	err := f.svc.RestoreUpload(context.Background(), bytes.NewReader(newer), int64(len(newer)))
	if !errors.Is(err, ErrInvalidBackup) || !strings.Contains(err.Error(), "newer version") || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("err = %v; want a newer-version rejection", err)
	}
	assertNothingStaged(t, f.dir)

	// The same database without the unknown migration is accepted.
	same := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	if err := f.svc.RestoreUpload(context.Background(), bytes.NewReader(same), int64(len(same))); err != nil {
		t.Fatalf("current schema rejected: %v", err)
	}
}
