//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// appLock is an exclusive flock(2) on <data dir>/.dupearr-app.lock, held until the process exits.
// A flock belongs to the open file description: a re-exec keeps it when the descriptor survives
// the exec (handOver), and the new process adopts that descriptor (inheritedAppLock).
type appLock struct {
	f    *os.File
	path string
}

// acquireAppLock takes the data directory's lock. err wraps errAppLocked when another server holds
// it, and is set when the lock file is a symbolic link (never followed). A filesystem without lock
// support (or a lock file that cannot be opened) only yields a warning: the server starts without
// this protection.
func acquireAppLock(dataDir string) (lock *appLock, warning string, err error) {
	path := filepath.Join(dataDir, appLockName)
	if l := inheritedAppLock(path); l != nil {
		return l, "", nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if errors.Is(err, os.ErrPermission) {
		// Created by another user (e.g. an earlier run as root): a read-only descriptor locks too.
		f, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	}
	switch {
	case errors.Is(err, syscall.ELOOP):
		return nil, "", fmt.Errorf("%s is a symbolic link; remove it (the lock file is never followed)", path)
	case err != nil:
		return nil, fmt.Sprintf("Could not open the data directory lock %s (%v); starting without protection against a second server on this data directory", path, err), nil
	}
	if fi, serr := f.Stat(); serr != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, "", fmt.Errorf("%s is not a regular file; remove it", path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, "", lockedError(dataDir, path, readLockPID(path))
		}
		return nil, fmt.Sprintf("Could not lock %s (%v); starting without protection against a second server on this data directory", path, err), nil
	}
	writeLockPID(f)
	return &appLock{f: f, path: path}, "", nil
}

// inheritedAppLock adopts the lock descriptor a restart handed over (appLockFDEnv): it must be
// open on this very lock file (same device and inode), and its lock is confirmed (a no-op on the
// same open file description). The variable is removed from the environment either way.
func inheritedAppLock(path string) *appLock {
	v, ok := os.LookupEnv(appLockFDEnv)
	if !ok {
		return nil
	}
	_ = os.Unsetenv(appLockFDEnv)
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 {
		return nil
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	cur, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || cur.Dev != st.Dev || cur.Ino != st.Ino {
		return nil // not our lock file: leave the descriptor alone
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil
	}
	writeLockPID(f)
	return &appLock{f: f, path: path}
}

// handOver prepares the lock for an in-place restart: a duplicate of its descriptor without
// close-on-exec, named in the returned environment entry, keeps the lock through the exec. undo
// closes the duplicate when the exec failed.
func (l *appLock) handOver() (env string, undo func()) {
	if l == nil || l.f == nil {
		return "", func() {}
	}
	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(l.f.Fd())) // dup(2) clears close-on-exec on the new descriptor
	syscall.ForkLock.RUnlock()
	if err != nil {
		return "", func() {}
	}
	return appLockFDEnv + "=" + strconv.Itoa(fd), func() { _ = syscall.Close(fd) }
}

// release unlocks (closing the descriptor). The file stays: removing a lock file others may have
// open lets two processes lock two different files.
func (l *appLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
	l.f = nil
}
