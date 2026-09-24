// Package logging sets up Dupearr's structured logger: stdout plus *arr-style rotating log files in
// dataDir/logs, and an in-memory ring buffer that backs System → Events.
//
// Component loggers are derived by cmd/dupearr with slog.Logger.With("component", "<Name>");
// Entry.Logger is taken from that attribute. Errors logged under an "error" (or "err") attribute
// become Entry.Exception. Every message and attribute is passed through Redact before it is
// written anywhere.
//
// Levels are trace (LevelTrace, -8), debug, info, warn and error. The file and stdout receive
// every record at or above the configured level; the ring buffer keeps info and above (like the
// *arr Events page, which is fed from Info logs), so debug/trace noise never evicts warnings.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/store"
)

// LevelTrace is the most verbose level (below slog.LevelDebug), as in the *arr apps.
const LevelTrace = slog.Level(-8)

const (
	// FileName is the active log file in the log directory.
	FileName = fileBase + fileExt
	// MaxArchives is the number of rotated files kept (dupearr.0.txt newest … dupearr.4.txt).
	MaxArchives = 5
	// RingSize is the number of entries kept in memory for Recent.
	RingSize = 1000

	fileBase = "dupearr"
	fileExt  = ".txt"

	defaultSizeLimitMB = 1
	maxSizeLimitMB     = 1 << 16
	defaultPageSize    = 20
	maxPageSize        = 1000
)

// ErrFileNotFound is returned (wrapped) by FilePath for names that are not a current log file,
// including anything that looks like path traversal.
var ErrFileNotFound = errors.New("log file not found")

// safeFileName is the file-name shape Servarr accepts for log downloads.
var safeFileName = regexp.MustCompile(`^[-.a-zA-Z0-9]+\.txt$`)

// LogFile is one log file on disk (System → Log Files).
type LogFile struct {
	Filename      string    `json:"filename"`
	LastWriteTime time.Time `json:"lastWriteTime"`
	Size          int64     `json:"size"`
}

// Entry is one in-memory log entry (System → Events).
type Entry struct {
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Logger    string    `json:"logger"`
	Message   string    `json:"message"`
	Exception string    `json:"exception,omitempty"`
}

// Manager controls the logger created by Setup (level changes, file listing, ring buffer).
// It is safe for concurrent use.
type Manager struct {
	logDir string
	core   *core
}

// Setup configures a slog logger writing to stdout and to dataDir/logs/dupearr.txt with *arr-style
// rotation (dupearr.txt → dupearr.0.txt … dupearr.N.txt, N = 5) at sizeLimitMB. Also keeps an
// in-memory ring buffer (1000 entries) for System → Events.
//
// logDir (normally dataDir/logs) is created when missing. level is trace|debug|info|warn|error
// ("" = info); sizeLimitMB ≤ 0 means the default of 1 MB. MaxArchives (5) rotated files are kept:
// dupearr.0.txt (newest) … dupearr.4.txt (oldest), plus the active dupearr.txt.
func Setup(logDir, level string, sizeLimitMB int) (*slog.Logger, *Manager, error) {
	return setup(logDir, level, sizeLimitBytes(sizeLimitMB), os.Stdout)
}

// sizeLimitBytes converts a size limit in MB to bytes: ≤ 0 means the default (1 MB), and huge
// values are clamped far from overflow.
func sizeLimitBytes(sizeLimitMB int) int64 {
	if sizeLimitMB <= 0 {
		sizeLimitMB = defaultSizeLimitMB
	}
	return int64(min(sizeLimitMB, maxSizeLimitMB)) << 20
}

// setup is Setup with the rotation size in bytes and the console writer injectable (tests).
func setup(logDir, level string, maxBytes int64, stdout io.Writer) (*slog.Logger, *Manager, error) {
	lvl := slog.LevelInfo
	if strings.TrimSpace(level) != "" {
		var err error
		if lvl, err = ParseLevel(level); err != nil {
			return nil, nil, fmt.Errorf("logging: %w", err)
		}
	}
	if strings.TrimSpace(logDir) == "" {
		return nil, nil, errors.New("logging: log directory is empty")
	}
	abs, err := filepath.Abs(logDir)
	if err != nil {
		return nil, nil, fmt.Errorf("logging: resolve log directory: %w", err)
	}
	if err := os.MkdirAll(abs, logDirMode); err != nil {
		return nil, nil, fmt.Errorf("logging: create log directory: %w", err)
	}
	restrictPermissions(abs)
	rot, err := openRotator(abs, maxBytes, MaxArchives)
	if err != nil {
		return nil, nil, fmt.Errorf("logging: %w", err)
	}

	c := &core{level: new(slog.LevelVar), file: rot, stdout: stdout, ring: newRing(RingSize)}
	c.level.Set(lvl)
	return slog.New(&handler{core: c}), &Manager{logDir: abs, core: c}, nil
}

// Log files can hold client addresses, host names, library titles, file paths and mistyped
// credentials, so they are private to the account Dupearr runs as.
const (
	logDirMode  os.FileMode = 0o700
	logFileMode os.FileMode = 0o600
)

// restrictPermissions makes the log directory and the log files already in it (created by
// earlier versions with 0755/0644, or with a lax umask) private: MkdirAll and OpenFile never
// change the mode of what exists. Best effort: what cannot be changed (another owner) is left as
// it is. Symlinks and anything that is not a log file are never touched.
func restrictPermissions(dir string) {
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() && fi.Mode().Perm() != logDirMode {
		_ = os.Chmod(dir, logDirMode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isLogFileName(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Mode().Perm()&^logFileMode != 0 {
			_ = os.Chmod(filepath.Join(dir, e.Name()), logFileMode)
		}
	}
}

// ParseLevel parses a level name (case-insensitive): trace, debug, info, warn (or warning) and
// error (or fatal).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "fatal":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want trace, debug, info, warn or error)", s)
}

// LevelName returns the lower-case name of the level band l falls in: trace, debug, info, warn
// or error.
func LevelName(l slog.Level) string {
	switch {
	case l < slog.LevelDebug:
		return "trace"
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

// SetLevel changes the minimum level at runtime (trace|debug|info|warn|error).
func (m *Manager) SetLevel(level string) error {
	lvl, err := ParseLevel(level)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	m.core.level.Set(lvl)
	return nil
}

// SetSizeLimit changes the log file rotation size at runtime (config LogSizeLimit, in MB; ≤ 0
// means the default of 1 MB). It applies from the next write; it is a no-op after Close.
func (m *Manager) SetSizeLimit(sizeLimitMB int) {
	m.core.mu.Lock()
	defer m.core.mu.Unlock()
	if m.core.file != nil {
		m.core.file.setMaxBytes(sizeLimitBytes(sizeLimitMB))
	}
}

// Level returns the current minimum level name.
func (m *Manager) Level() string { return LevelName(m.core.level.Level()) }

// Files lists the log files, newest first.
//
// Only regular files named dupearr*.txt in the log directory are listed (no symlinks or
// directories). Ties in modification time are broken by rotation order (dupearr.txt, then
// dupearr.0.txt, …). Never nil.
func (m *Manager) Files() ([]LogFile, error) {
	entries, err := os.ReadDir(m.logDir)
	if err != nil {
		return nil, fmt.Errorf("logging: list log files: %w", err)
	}
	files := []LogFile{}
	for _, e := range entries {
		name := e.Name()
		if !isLogFileName(name) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed by a concurrent rotation
		}
		files = append(files, LogFile{Filename: name, LastWriteTime: fi.ModTime().UTC(), Size: fi.Size()})
	}
	slices.SortFunc(files, func(a, b LogFile) int {
		if c := b.LastWriteTime.Compare(a.LastWriteTime); c != 0 {
			return c
		}
		if c := rotationRank(a.Filename) - rotationRank(b.Filename); c != 0 {
			return c
		}
		return strings.Compare(a.Filename, b.Filename)
	})
	return files, nil
}

// isLogFileName reports whether name is a safe dupearr*.txt file name.
func isLogFileName(name string) bool {
	return strings.HasPrefix(name, fileBase) && safeFileName.MatchString(name)
}

// rotationRank orders dupearr.txt before dupearr.0.txt … and other names last.
func rotationRank(name string) int {
	if i, ok := archiveIndex(name); ok {
		return i
	}
	return 1 << 20
}

// FilePath returns the absolute path of a log file; validates name (no traversal, must be one of
// Files()).
func (m *Manager) FilePath(name string) (string, error) {
	if !isLogFileName(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("%w: %q", ErrFileNotFound, name)
	}
	files, err := m.Files()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.Filename == name {
			return filepath.Join(m.logDir, name), nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrFileNotFound, name)
}

// Recent returns a page of the in-memory ring buffer (newest first), filtered to entries at or
// above the given minimum level ("" = all).
//
// Paging is normalised: page < 1 → 1, pageSize < 1 → 20, pageSize > 1000 → 1000. The sort key
// is always "time"; SortDirection "ascending" returns oldest first, anything else newest first.
// An unknown level is treated as "". Records is never nil.
func (m *Manager) Recent(p store.Paging, level string) store.Page[Entry] {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 {
		p.PageSize = defaultPageSize
	}
	p.PageSize = min(p.PageSize, maxPageSize)
	ascending := strings.EqualFold(p.SortDirection, "ascending")
	minLevel, err := ParseLevel(level)
	if err != nil {
		minLevel = LevelTrace - 1000 // no filter
	}

	m.core.mu.Lock()
	matched := make([]Entry, 0, 64)
	m.core.ring.newestFirst(func(e ringEntry) bool {
		if e.level >= minLevel {
			matched = append(matched, e.entry)
		}
		return true
	})
	m.core.mu.Unlock()

	if ascending {
		slices.Reverse(matched)
	}
	page := store.Page[Entry]{
		Page:          p.Page,
		PageSize:      p.PageSize,
		SortKey:       "time",
		SortDirection: "descending",
		TotalRecords:  len(matched),
		Records:       []Entry{},
	}
	if ascending {
		page.SortDirection = "ascending"
	}
	pages := (len(matched) + p.PageSize - 1) / p.PageSize
	if p.Page <= pages { // compare pages first: (Page-1)*PageSize could overflow for huge pages
		start := (p.Page - 1) * p.PageSize
		end := min(start+p.PageSize, len(matched))
		page.Records = append(page.Records, matched[start:end]...)
	}
	return page
}

// Close flushes and closes the log file. Logging continues to stdout and the ring buffer; closing
// twice is a no-op.
func (m *Manager) Close() error {
	m.core.mu.Lock()
	defer m.core.mu.Unlock()
	if m.core.file == nil {
		return nil
	}
	err := m.core.file.Close()
	m.core.file = nil
	if err != nil {
		return fmt.Errorf("logging: close log file: %w", err)
	}
	return nil
}
