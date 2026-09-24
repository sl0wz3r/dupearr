package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// errClosed is returned when writing after Close.
var errClosed = errors.New("log file is closed")

// rotator is an io.Writer appending to dir/dupearr.txt that rotates *arr style (NLog rolling
// archives) once the file would exceed maxBytes: dupearr.txt → dupearr.0.txt (newest archive),
// dupearr.0.txt → dupearr.1.txt, …, keeping `keep` archives. It is not safe for concurrent use;
// the handler core serialises access.
type rotator struct {
	dir      string
	maxBytes int64
	keep     int

	f      *os.File
	size   int64
	closed bool // Close was called: never reopen
}

// openRotator opens (appending to) dir/dupearr.txt.
func openRotator(dir string, maxBytes int64, keep int) (*rotator, error) {
	if maxBytes <= 0 || keep < 1 {
		return nil, fmt.Errorf("invalid rotation settings (maxBytes %d, keep %d)", maxBytes, keep)
	}
	r := &rotator{dir: dir, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

// archiveName returns the file name of archive i (i < 0: the active file).
func archiveName(i int) string {
	if i < 0 {
		return FileName
	}
	return fileBase + "." + strconv.Itoa(i) + fileExt
}

// archiveIndex parses a log file name: -1 for dupearr.txt, N for dupearr.N.txt, ok=false otherwise.
func archiveIndex(name string) (int, bool) {
	if name == FileName {
		return -1, true
	}
	mid, ok := strings.CutPrefix(name, fileBase+".")
	if !ok {
		return 0, false
	}
	mid, ok = strings.CutSuffix(mid, fileExt)
	if !ok || mid == "" || len(mid) > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(mid)
	if err != nil || n < 0 || strconv.Itoa(n) != mid {
		return 0, false
	}
	return n, true
}

func (r *rotator) path(i int) string { return filepath.Join(r.dir, archiveName(i)) }

// open opens (creating it when missing) the active log file for appending. It refuses anything
// but a regular file: a symlink planted at dupearr.txt would otherwise make every log line append
// to its target (a media file, say), and opening a FIFO for writing would block logging forever.
func (r *rotator) open() error {
	p := r.path(-1)
	if li, err := os.Lstat(p); err == nil && !li.Mode().IsRegular() {
		return fmt.Errorf("open log file: %s is not a regular file (%v)", p, li.Mode().Type())
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	if fi.Mode().Perm()&^logFileMode != 0 {
		_ = f.Chmod(logFileMode) // an existing file from an earlier version; best effort
	}
	// Re-check the name after opening, in case it was swapped for a symlink in between.
	if li, err := os.Lstat(p); err != nil || !li.Mode().IsRegular() || !os.SameFile(li, fi) {
		_ = f.Close()
		return fmt.Errorf("open log file: %s changed while it was being opened", p)
	}
	r.f, r.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first when p would push a non-empty file past maxBytes (a single
// oversized write still goes to a fresh file). When rotation fails the write still goes to the
// active file, and the error is returned alongside. When the active file could not be (re)opened
// earlier, every write retries opening it, so file logging recovers on its own.
func (r *rotator) Write(p []byte) (int, error) {
	if r.closed {
		return 0, errClosed
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	var rotErr error
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		rotErr = r.rotate()
		if r.f == nil {
			return 0, rotErr
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		err = fmt.Errorf("write log file: %w", err)
	}
	return n, errors.Join(rotErr, err)
}

// rotate closes the active file, shifts the archives up by one (dropping the oldest) and opens a
// new active file. Only files named dupearr.txt / dupearr.N.txt inside dir are ever touched.
func (r *rotator) rotate() error {
	var errs []error
	if err := r.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close log file: %w", err))
	}
	r.f = nil

	if err := os.Remove(r.path(r.keep - 1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove oldest log archive: %w", err))
	}
	for i := r.keep - 2; i >= -1; i-- {
		if err := os.Rename(r.path(i), r.path(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("rotate %s: %w", archiveName(i), err))
		}
	}
	if err := r.open(); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if len(errs) > 0 && r.size > 0 {
		// The active file could not be archived: keep appending to it, but do not retry
		// on every write — wait for another maxBytes.
		r.size = 0
	}
	return errors.Join(errs...)
}

// setMaxBytes changes the rotation size; it applies from the next write.
func (r *rotator) setMaxBytes(n int64) {
	if n > 0 {
		r.maxBytes = n
	}
}

// Close closes the active file. Further writes fail with errClosed.
func (r *rotator) Close() error {
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
