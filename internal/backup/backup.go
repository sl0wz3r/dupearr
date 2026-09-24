// Package backup creates, lists, restores and prunes zip backups of config.xml + dupearr.db in
// dataDir/Backups/{scheduled,manual,update} (System → Backup).
//
// # Archives
//
// A backup is dataDir/Backups/<type>/dupearr_backup_v<version>_<yyyy.MM.dd_HH.mm.ss>.zip (local
// time, like the *arr apps) holding exactly three flat entries: config.xml (the file as stored,
// without environment overrides), dupearr.db (a consistent VACUUM INTO snapshot) and INFO (the
// Dupearr version and creation time). Archives are written to a temporary file and renamed into
// place, so a crash never leaves a truncated backup behind. They contain API keys and tokens and
// are created with owner-only permissions.
//
// # Restore
//
// Restore and RestoreUpload accept only this zip format. The archive must contain config.xml and
// dupearr.db as flat entries (no directories, no path traversal, no links); the database must be
// a healthy Dupearr SQLite database (header, read-only open, PRAGMA integrity_check = ok, core
// tables present, no schema migration unknown to this build) and config.xml must be valid on its
// own, without DUPEARR__ environment overrides. The two files are then staged atomically in
// dataDir/.restore; ApplyPendingRestore swaps them in at the next start, before the database is
// opened, keeping the replaced files as <name>.bak-<timestamp>. Backups hold every secret except
// the session signing key; keep them private.
//
// The archive's database is untrusted: its rows are copied into a new database with this build's
// schema, so nothing of its schema is adopted, and an archive holding triggers or views (which
// Dupearr never creates) is refused. The running instance's security settings (authentication
// method and requirement, API key, webhook token, user, bind address, ports, SSL, URL base) are
// kept unless the restore is asked to restore them (RestoreOptions), a switch to authentication
// None or External is never restored, an archive with more than one user account is refused,
// and the session signing key is neither backed up nor restored, so a restore signs every
// session out. StageRestore/StageRestoreUpload return a summary of the differences
// and wait for ConfirmRestore; an unconfirmed restore is discarded at the next start. Only the
// newest generations of replaced files (.bak-<timestamp>) are kept.
//
// A restore never resumes deletions: in the staged copy of the database, removals that were
// pending when the backup was taken are cancelled, removals that were running are marked failed,
// and queued groups are re-opened for approval (the archive itself is left unchanged).
package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// Backup kinds.
const (
	TypeScheduled = "scheduled"
	TypeManual    = "manual"
	TypeUpdate    = "update"
)

// Layout and limits.
const (
	// BackupsDirName is the folder (under the data directory) holding the backups.
	BackupsDirName = "Backups"
	// RestoreDirName is the folder (under the data directory) holding a staged restore.
	RestoreDirName = ".restore"
	// MaxUploadSize is the largest backup archive RestoreUpload accepts.
	MaxUploadSize int64 = 512 << 20

	configFileName = config.FileName // "config.xml"
	dbFileName     = "dupearr.db"
	infoFileName   = "INFO"

	maxConfigSize int64 = 1 << 20 // config.Load refuses larger files too
	maxDBSize     int64 = 4 << 30 // decompressed; guards against zip bombs
	maxEntries          = 16

	tmpBackupPrefix = ".tmp-backup-"
	namePrefix      = "dupearr_backup_v"
	nameTimeLayout  = "2006.01.02_15.04.05"
)

// kinds lists the backup types in List order.
var kinds = []string{TypeScheduled, TypeManual, TypeUpdate}

// nameRe matches backup archive names this package creates (anchored: retention must never touch
// a stray file that merely contains the pattern).
var nameRe = regexp.MustCompile(`^dupearr_backup_v[0-9A-Za-z.\-]+_\d{4}\.\d{2}\.\d{2}_\d{2}\.\d{2}\.\d{2}(?:_\d+)?\.zip$`)

// ErrNotFound is returned when a backup does not exist. It also matches store.ErrNotFound and
// fs.ErrNotExist with errors.Is, so API handlers map it to 404 either way.
var ErrNotFound error = notFoundError{}

// ErrInvalidBackup is returned (wrapped, with the reason) for an unknown backup type, an invalid
// file name, an archive that is not a valid Dupearr backup, or a path outside the backup folder.
var ErrInvalidBackup = errors.New("invalid backup")

type notFoundError struct{}

func (notFoundError) Error() string { return "backup not found" }

func (notFoundError) Is(target error) bool {
	return target == store.ErrNotFound || target == fs.ErrNotExist
}

// Backup is one backup archive. Type: scheduled|manual|update; Path = "/backup/<type>/<name>".
type Backup struct {
	ID   int64     `json:"id"`
	Name string    `json:"name"`
	Path string    `json:"path"`
	Type string    `json:"type"`
	Size int64     `json:"size"`
	Time time.Time `json:"time"`
}

// Service manages backups. It is safe for concurrent use; operations that write are serialised.
type Service struct {
	dataDir string
	cfg     *config.Manager
	st      store.Store
	log     *slog.Logger

	now func() time.Time // test hook
	mu  sync.Mutex       // serialises Create, Delete, Cleanup and restores
}

// New returns a Service for dataDir. cfg may be nil (config.xml is then read from dataDir).
func New(dataDir string, cfg *config.Manager, st store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	return &Service{dataDir: dataDir, cfg: cfg, st: st, log: log, now: time.Now}
}

func (s *Service) backupsDir() string { return filepath.Join(s.dataDir, BackupsDirName) }

func (s *Service) configPath() string {
	if s.cfg != nil {
		return s.cfg.Path()
	}
	return filepath.Join(s.dataDir, configFileName)
}

func validKind(kind string) bool {
	return kind == TypeScheduled || kind == TypeManual || kind == TypeUpdate
}

// Create writes a zip: config.xml + dupearr.db (VACUUM INTO snapshot).
//
// The database snapshot is taken with store.BackupTo into a temporary folder next to the
// backups, the archive is written there and renamed into Backups/<kind>/ once complete.
func (s *Service) Create(ctx context.Context, kind string) (*Backup, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("create backup: %w: unknown backup type %q", ErrInvalidBackup, kind)
	}
	if s.st == nil {
		return nil, errors.New("create backup: no database")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	root := s.backupsDir()
	dir := filepath.Join(root, kind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}
	cfgData, err := readLimited(s.configPath(), maxConfigSize)
	if err != nil {
		return nil, fmt.Errorf("create backup: read config: %w", err)
	}

	tmpDir, err := os.MkdirTemp(root, tmpBackupPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshot := filepath.Join(tmpDir, dbFileName)
	if err := s.st.BackupTo(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("create backup: snapshot database: %w", err)
	}
	if err := scrubSnapshot(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("create backup: snapshot database: %w", err)
	}

	now := s.now()
	info := fmt.Sprintf("v%s\n%s\n", version.Version, now.Format("2006-01-02 15:04:05"))
	tmpZip := filepath.Join(tmpDir, "backup.zip")
	if err := writeArchive(ctx, tmpZip, now, cfgData, snapshot, []byte(info)); err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}

	name, err := uniqueName(dir, archiveName(now))
	if err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmpZip, final); err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}
	syncDir(dir)
	st, err := os.Stat(final)
	if err != nil {
		return nil, fmt.Errorf("create backup: %w", err)
	}
	b := newBackup(kind, name, st)
	s.log.Info("Backup created", "type", kind, "name", name, "size", b.Size)
	return &b, nil
}

// scrubSnapshot removes the session signing key from a database snapshot: with it and the
// password hash next to it, anyone holding the archive could forge session cookies for this
// instance. It is not needed to restore (a new key is generated at startup). secure_delete
// overwrites the deleted row, and the rollback journal keeps the change in the file itself.
func scrubSnapshot(ctx context.Context, path string) (err error) {
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=rw&_pragma=secure_delete(1)"))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); err == nil {
			err = cerr
		}
	}()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("journal mode is %q", mode)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, sessionKeySetting)
	return err
}

// archiveName returns the file name of a backup created at t (local time).
func archiveName(t time.Time) string {
	return namePrefix + fileVersion() + "_" + t.Local().Format(nameTimeLayout) + ".zip"
}

// fileVersion is version.Version restricted to file-name-safe characters.
func fileVersion() string {
	v := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, version.Version)
	if v == "" {
		v = "0"
	}
	return v
}

// uniqueName returns name, or name with a "_N" suffix when a backup of that name already exists
// (two backups within one second). Callers hold s.mu, so the check cannot race with another
// Create.
func uniqueName(dir, name string) (string, error) {
	base := strings.TrimSuffix(name, ".zip")
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = base + "_" + strconv.Itoa(i) + ".zip"
		}
		if _, err := os.Lstat(filepath.Join(dir, candidate)); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many backups named %s", name)
}

// writeArchive writes the backup zip to path (created exclusively, mode 0600, fsynced).
func writeArchive(ctx context.Context, path string, now time.Time, cfgData []byte, dbPath string, info []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	add := func(name string, r io.Reader) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now}
		h.SetMode(0o600)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fmt.Errorf("add %s: %w", name, err)
		}
		if _, err := io.Copy(w, ctxReader{ctx: ctx, r: r}); err != nil {
			return fmt.Errorf("add %s: %w", name, err)
		}
		return nil
	}
	if err := add(configFileName, strings.NewReader(string(cfgData))); err != nil {
		return err
	}
	db, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	err = add(dbFileName, db)
	_ = db.Close()
	if err != nil {
		return err
	}
	if err := add(infoFileName, strings.NewReader(string(info))); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	return f.Sync()
}

// List returns backups newest first; ID = stable hash of type/name.
//
// Only regular files whose names match the backup naming scheme are listed. The result is never
// nil.
func (s *Service) List() ([]Backup, error) {
	out := []Backup{}
	for _, kind := range kinds {
		list, err := s.listKind(kind)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		if out[i].Name != out[j].Name {
			return out[i].Name > out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

func (s *Service) listKind(kind string) ([]Backup, error) {
	entries, err := os.ReadDir(filepath.Join(s.backupsDir(), kind))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s backups: %w", kind, err)
	}
	var out []Backup
	for _, de := range entries {
		if !de.Type().IsRegular() || !nameRe.MatchString(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil { // removed meanwhile
			continue
		}
		out = append(out, newBackup(kind, de.Name(), info))
	}
	return out, nil
}

func newBackup(kind, name string, info fs.FileInfo) Backup {
	return Backup{
		ID:   backupID(kind, name),
		Name: name,
		Path: "/backup/" + kind + "/" + name,
		Type: kind,
		Size: info.Size(),
		Time: info.ModTime().UTC(),
	}
}

// backupID is a stable, positive 31-bit FNV-1a hash of "backup-<type>-<name>" (like the *arr
// HashConverter.GetHashInt31); nothing is stored.
func backupID(kind, name string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte("backup-" + kind + "-" + name))
	return int64(h.Sum32() & 0x7fffffff)
}

// find returns the backup with the given id. IDs are 31-bit hashes, so two archives can share
// one; Delete and Restore then refuse to guess which one was meant.
func (s *Service) find(id int64) (Backup, error) {
	list, err := s.List()
	if err != nil {
		return Backup{}, err
	}
	var found []Backup
	for _, b := range list {
		if b.ID == id {
			found = append(found, b)
		}
	}
	switch len(found) {
	case 0:
		return Backup{}, fmt.Errorf("backup %d: %w", id, ErrNotFound)
	case 1:
		return found[0], nil
	default:
		return Backup{}, fmt.Errorf("%w: id %d matches %d backups (%s and %s); rename or remove one of them",
			ErrInvalidBackup, id, len(found), found[0].Path, found[1].Path)
	}
}

// File returns the validated absolute path of a backup for download.
//
// kind must be scheduled, manual or update; name must be a plain file name (no separators, not
// hidden) ending in .zip. The path is resolved with filepath.EvalSymlinks and must stay inside
// the (resolved) Backups/<kind> folder and be a regular file.
func (s *Service) File(kind, name string) (string, error) {
	if !validKind(kind) {
		return "", fmt.Errorf("%w: unknown backup type %q", ErrInvalidBackup, kind)
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(s.backupsDir(), kind))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, err)
	}
	p, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, err)
	}
	if !inside(p, dir) {
		return "", fmt.Errorf("%w: %s/%s resolves outside the backup folder", ErrInvalidBackup, kind, name)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("backup %s/%s: %w", kind, name, ErrNotFound)
	}
	return p, nil
}

// validateName accepts plain, non-hidden *.zip file names.
func validateName(name string) error {
	switch {
	case name == "", len(name) > 255,
		name != filepath.Base(name),
		strings.ContainsAny(name, "/\\:\x00"),
		strings.HasPrefix(name, "."),
		!strings.HasSuffix(strings.ToLower(name), ".zip"):
		return fmt.Errorf("%w: invalid backup file name %q", ErrInvalidBackup, name)
	}
	return nil
}

// inside reports whether p equals dir or lies below it (both already resolved).
func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Delete removes a backup.
func (s *Service) Delete(id int64) error {
	b, err := s.find(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.File(b.Type, b.Name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("backup %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("delete backup %s: %w", b.Name, err)
	}
	s.log.Info("Backup deleted", "type", b.Type, "name", b.Name)
	return nil
}

// Cleanup deletes scheduled backups older than retentionDays (manual/update backups are kept).
//
// Age is the archive's modification time. retentionDays ≤ 0 disables cleanup. The newest
// scheduled backup is always kept, however old, so a clock jump or scheduled backups that stopped
// being created can never leave no scheduled backup at all. It returns how many archives were
// deleted; failures to delete individual files are joined into the error.
func (s *Service) Cleanup(retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listKind(TypeScheduled)
	if err != nil {
		return 0, err
	}
	newest := -1
	for i, b := range list {
		if newest < 0 || b.Time.After(list[newest].Time) ||
			(b.Time.Equal(list[newest].Time) && b.Name > list[newest].Name) {
			newest = i
		}
	}
	cutoff := s.now().AddDate(0, 0, -retentionDays)
	removed := 0
	var errs []error
	for i, b := range list {
		if i == newest || !b.Time.Before(cutoff) {
			continue
		}
		p, err := s.File(b.Type, b.Name)
		if err == nil {
			err = os.Remove(p)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove old backup %s: %w", b.Name, err))
			continue
		}
		removed++
		s.log.Info("Removed old backup", "name", b.Name, "retentionDays", retentionDays)
	}
	return removed, errors.Join(errs...)
}

// readLimited reads a whole file, refusing files larger than limit.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// ctxReader stops reading once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// syncDir fsyncs a directory so renames in it are durable (best effort; not supported everywhere).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
