//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// appLock is a best-effort lock where flock(2) is not available (Windows, …): the lock file is
// created exclusively and holds the owner's process id; a file whose process is gone is stale and
// taken over. It is removed when the server stops.
type appLock struct {
	f    *os.File
	path string
}

// acquireAppLock takes the data directory's lock (see lock_flock.go for the contract).
func acquireAppLock(dataDir string) (lock *appLock, warning string, err error) {
	path := filepath.Join(dataDir, appLockName)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			writeLockPID(f)
			return &appLock{f: f, path: path}, "", nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Sprintf("Could not create the data directory lock %s (%v); starting without protection against a second server on this data directory", path, err), nil
		}
		pid := readLockPID(path)
		if pid > 0 && pid != os.Getpid() && processAlive(pid) {
			return nil, "", lockedError(dataDir, path, pid)
		}
		// Stale (its process is gone) or left by this very process before an in-place restart.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Sprintf("Could not replace the stale lock %s (%v); starting without protection against a second server on this data directory", path, err), nil
		}
	}
	return nil, fmt.Sprintf("Could not take the data directory lock %s; starting without protection against a second server on this data directory", path), nil
}

// processAlive reports whether a process with this id exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false // Windows: no such process
	}
	defer func() { _ = p.Release() }()
	if runtime.GOOS == "windows" {
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// handOver: the re-executed process keeps the process id and takes the file over (see above).
func (l *appLock) handOver() (env string, undo func()) { return "", func() {} }

// release removes the lock file.
func (l *appLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
	_ = os.Remove(l.path)
}
