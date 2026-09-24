package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

var fixedNow = time.Date(2026, 9, 22, 10, 11, 12, 0, time.Local)

type fixture struct {
	dir string
	cfg *config.Manager
	db  *database.DB
	svc *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), filepath.Join(dir, dbFileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(dir, cfg, db, nil)
	svc.now = func() time.Time { return fixedNow }
	return &fixture{dir: dir, cfg: cfg, db: db, svc: svc}
}

// readZip returns the entries of a zip file by name.
func readZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func TestCreateWritesCompleteArchive(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := models.DefaultSettings()
	s.ScanIntervalMinutes = 1234
	if err := f.db.Settings().Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	b, err := f.svc.Create(ctx, TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	if !nameRe.MatchString(b.Name) || !strings.Contains(b.Name, "_2026.09.22_10.11.12") {
		t.Errorf("name = %q", b.Name)
	}
	if b.Type != TypeManual || b.Path != "/backup/manual/"+b.Name || b.ID != backupID(TypeManual, b.Name) || b.Size <= 0 {
		t.Errorf("backup = %+v", b)
	}
	p := filepath.Join(f.dir, BackupsDirName, TypeManual, b.Name)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("archive mode = %v, want 0600 (it holds secrets)", st.Mode().Perm())
	}

	entries := readZip(t, p)
	if len(entries) != 3 {
		t.Fatalf("entries = %v", len(entries))
	}
	wantCfg, _ := os.ReadFile(f.cfg.Path())
	if !bytes.Equal(entries[configFileName], wantCfg) {
		t.Error("config.xml differs from the live file")
	}
	if !strings.HasPrefix(string(entries[infoFileName]), "v") {
		t.Errorf("INFO = %q", entries[infoFileName])
	}
	// The snapshot is a healthy Dupearr database holding the saved settings.
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(snap, entries[dbFileName], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyDatabase(ctx, snap); err != nil {
		t.Fatalf("snapshot invalid: %v", err)
	}
	sdb, err := database.Open(ctx, snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	got, err := sdb.Settings().Get(ctx)
	if err != nil || got.ScanIntervalMinutes != 1234 {
		t.Errorf("snapshot settings = %+v, %v", got.ScanIntervalMinutes, err)
	}

	// No temporary files are left behind.
	left, _ := os.ReadDir(filepath.Join(f.dir, BackupsDirName))
	for _, e := range left {
		if strings.HasPrefix(e.Name(), tmpBackupPrefix) {
			t.Errorf("temporary %s left behind", e.Name())
		}
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Create(context.Background(), "weekly"); !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("unknown kind: err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.svc.Create(ctx, TypeManual); err == nil {
		t.Error("cancelled context: no error")
	}
	if list, _ := f.svc.List(); len(list) != 0 {
		t.Errorf("failed creates left backups: %+v", list)
	}
	noDB := New(f.dir, f.cfg, nil, nil)
	if _, err := noDB.Create(context.Background(), TypeManual); err == nil {
		t.Error("no store: no error")
	}
	missingCfg := New(t.TempDir(), nil, f.db, nil)
	if _, err := missingCfg.Create(context.Background(), TypeManual); err == nil {
		t.Error("missing config.xml: no error")
	}
}

func TestCreateSameSecondGetsUniqueNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, err := f.svc.Create(ctx, TypeScheduled)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Create(ctx, TypeScheduled)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == b.Name || a.ID == b.ID || !strings.HasSuffix(b.Name, "_2.zip") || !nameRe.MatchString(b.Name) {
		t.Fatalf("names %q / %q", a.Name, b.Name)
	}
	list, err := f.svc.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestArchiveNameSanitisesVersion(t *testing.T) {
	name := archiveName(fixedNow)
	if !nameRe.MatchString(name) {
		t.Errorf("%q does not match the listing pattern", name)
	}
	if v := fileVersion(); strings.ContainsAny(v, "/\\+ ") {
		t.Errorf("file version %q", v)
	}
}

func TestListFiltersAndOrders(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.dir, BackupsDirName)
	write := func(kind, name string, age time.Duration) {
		t.Helper()
		dir := filepath.Join(root, kind)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("zip"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := fixedNow.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write(TypeScheduled, "dupearr_backup_v1.0.0_2026.09.01_03.00.00.zip", 72*time.Hour)
	write(TypeManual, "dupearr_backup_v1.0.0_2026.09.03_03.00.00.zip", 24*time.Hour)
	write(TypeUpdate, "dupearr_backup_v0.9.0-beta_2026.09.02_03.00.00.zip", 48*time.Hour)
	write(TypeManual, "not-a-backup.zip", time.Hour)
	write(TypeManual, "sonarr_backup_v4.0.0_2026.09.03_03.00.00.zip", time.Hour)
	write(TypeManual, "xdupearr_backup_v1.0.0_2026.09.03_03.00.00.zip", time.Hour)
	if err := os.MkdirAll(filepath.Join(root, TypeManual, "dupearr_backup_v1.0.0_2026.09.04_03.00.00.zip"), 0o700); err != nil {
		t.Fatal(err)
	}
	write("weekly", "dupearr_backup_v1.0.0_2026.09.05_03.00.00.zip", 0) // unknown kind folder
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/hosts", filepath.Join(root, TypeManual, "dupearr_backup_v1.0.0_2026.09.06_03.00.00.zip")); err != nil {
			t.Fatal(err)
		}
	}

	list, err := f.svc.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range list {
		got = append(got, b.Type)
	}
	if strings.Join(got, ",") != "manual,update,scheduled" {
		t.Errorf("list order/types = %v (%+v)", got, list)
	}
	for _, b := range list {
		if b.ID != backupID(b.Type, b.Name) || b.ID <= 0 {
			t.Errorf("id of %+v", b)
		}
	}
}

func TestListEmpty(t *testing.T) {
	svc := New(t.TempDir(), nil, nil, nil)
	list, err := svc.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list = %#v, %v", list, err)
	}
}

func TestBackupIDStable(t *testing.T) {
	a := backupID(TypeManual, "dupearr_backup_v1_2026.01.01_00.00.00.zip")
	if a != backupID(TypeManual, "dupearr_backup_v1_2026.01.01_00.00.00.zip") {
		t.Error("id not deterministic")
	}
	if a == backupID(TypeScheduled, "dupearr_backup_v1_2026.01.01_00.00.00.zip") {
		t.Error("id ignores the type")
	}
	if a < 0 || a > 0x7fffffff {
		t.Errorf("id %d outside 31 bits", a)
	}
}

func TestFileValidation(t *testing.T) {
	f := newFixture(t)
	b, err := f.svc.Create(context.Background(), TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, kind, file string
		want             error
	}{
		{"valid", TypeManual, b.Name, nil},
		{"unknown kind", "other", b.Name, ErrInvalidBackup},
		{"kind traversal", "../manual", b.Name, ErrInvalidBackup},
		{"empty name", TypeManual, "", ErrInvalidBackup},
		{"parent traversal", TypeManual, "../x.zip", ErrInvalidBackup},
		{"nested", TypeManual, "a/b.zip", ErrInvalidBackup},
		{"backslash", TypeManual, `..\b.zip`, ErrInvalidBackup},
		{"absolute", TypeManual, "/etc/passwd.zip", ErrInvalidBackup},
		{"drive", TypeManual, "C:x.zip", ErrInvalidBackup},
		{"hidden", TypeManual, ".x.zip", ErrInvalidBackup},
		{"not zip", TypeManual, "config.xml", ErrInvalidBackup},
		{"nul byte", TypeManual, "a\x00.zip", ErrInvalidBackup},
		{"missing", TypeManual, "dupearr_backup_v1_2026.01.01_00.00.00.zip", ErrNotFound},
		{"missing kind dir", TypeUpdate, b.Name, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := f.svc.File(tt.kind, tt.file)
			if tt.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				want, _ := filepath.EvalSymlinks(filepath.Join(f.dir, BackupsDirName, TypeManual, b.Name))
				if p != want || !filepath.IsAbs(p) {
					t.Errorf("path = %q, want %q", p, want)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	_, err = f.svc.File(TypeManual, "dupearr_backup_v1_2026.01.01_00.00.00.zip")
	if !errors.Is(err, store.ErrNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("not-found error %v must match store.ErrNotFound and fs.ErrNotExist", err)
	}
}

func TestFileSymlinkConfinement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newFixture(t)
	dir := filepath.Join(f.dir, BackupsDirName, TypeManual)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.zip")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.zip")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.File(TypeManual, "escape.zip"); !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("symlink escaping the backup folder: err = %v", err)
	}

	// A kind folder that is itself a symlink (backups on another disk) is allowed.
	elsewhere := t.TempDir()
	name := "dupearr_backup_v1_2026.01.01_00.00.00.zip"
	if err := os.WriteFile(filepath.Join(elsewhere, name), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(f.dir, BackupsDirName, TypeScheduled)); err != nil {
		t.Fatal(err)
	}
	if p, err := f.svc.File(TypeScheduled, name); err != nil || !strings.HasSuffix(p, name) {
		t.Errorf("symlinked kind folder: %q, %v", p, err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	b, err := f.svc.Create(context.Background(), TypeManual)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.svc.List(); len(list) != 0 {
		t.Errorf("list after delete = %+v", list)
	}
	if err := f.svc.Delete(b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
}

func TestCleanup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	create := func(kind string, age time.Duration) Backup {
		t.Helper()
		f.svc.now = func() time.Time { return fixedNow.Add(-age) }
		b, err := f.svc.Create(ctx, kind)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(f.dir, BackupsDirName, kind, b.Name)
		mt := fixedNow.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return *b
	}
	oldScheduled := create(TypeScheduled, 40*24*time.Hour)
	create(TypeScheduled, 27*24*time.Hour)
	create(TypeScheduled, time.Hour)
	create(TypeManual, 400*24*time.Hour)
	create(TypeUpdate, 400*24*time.Hour)
	f.svc.now = func() time.Time { return fixedNow }

	if n, err := f.svc.Cleanup(0); n != 0 || err != nil {
		t.Errorf("retention 0: removed %d, %v", n, err)
	}
	n, err := f.svc.Cleanup(28)
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want 1", n, err)
	}
	list, _ := f.svc.List()
	if len(list) != 4 {
		t.Fatalf("remaining = %+v", list)
	}
	for _, b := range list {
		if b.ID == oldScheduled.ID {
			t.Error("old scheduled backup kept")
		}
	}
	if n, _ := f.svc.Cleanup(28); n != 0 {
		t.Errorf("second cleanup removed %d", n)
	}
}

func TestCleanupKeepsNewestScheduledBackup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var names []string
	for _, age := range []time.Duration{90 * 24 * time.Hour, 60 * 24 * time.Hour, 45 * 24 * time.Hour} {
		f.svc.now = func() time.Time { return fixedNow.Add(-age) }
		b, err := f.svc.Create(ctx, TypeScheduled)
		if err != nil {
			t.Fatal(err)
		}
		mt := fixedNow.Add(-age)
		if err := os.Chtimes(filepath.Join(f.dir, BackupsDirName, TypeScheduled, b.Name), mt, mt); err != nil {
			t.Fatal(err)
		}
		names = append(names, b.Name)
	}
	f.svc.now = func() time.Time { return fixedNow }
	n, err := f.svc.Cleanup(28)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2 (every expired backup but the newest)", n, err)
	}
	list, _ := f.svc.List()
	if len(list) != 1 || list[0].Name != names[2] {
		t.Errorf("remaining = %+v; want only the newest scheduled backup %s", list, names[2])
	}
	if n, _ := f.svc.Cleanup(28); n != 0 {
		t.Errorf("second cleanup removed %d; the last scheduled backup must be kept", n)
	}
}

// collidingNames returns two backup file names whose IDs (31-bit hashes) are equal for kind.
func collidingNames(t *testing.T, kind string) (string, string) {
	t.Helper()
	seen := map[int64]string{}
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5_000_000 {
		name := namePrefix + "1.0.0_" + base.Add(time.Duration(i)*time.Second).Format(nameTimeLayout) + ".zip"
		id := backupID(kind, name)
		if prev, ok := seen[id]; ok {
			return prev, name
		}
		seen[id] = name
	}
	t.Skip("no hash collision found")
	return "", ""
}

// TestAmbiguousIDIsRefused checks that Delete and Restore never act on one of two backups that
// share an id.
func TestAmbiguousIDIsRefused(t *testing.T) {
	f := newFixture(t)
	a, b := collidingNames(t, TypeManual)
	dir := filepath.Join(f.dir, BackupsDirName, TypeManual)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("zip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id := backupID(TypeManual, a)
	if err := f.svc.Delete(id); !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("delete err = %v; want ErrInvalidBackup (ambiguous)", err)
	}
	if err := f.svc.Restore(context.Background(), id); !errors.Is(err, ErrInvalidBackup) {
		t.Errorf("restore err = %v; want ErrInvalidBackup (ambiguous)", err)
	}
	for _, n := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}
