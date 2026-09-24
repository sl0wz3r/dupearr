//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mustLock(t *testing.T, dir string) *appLock {
	t.Helper()
	l, warning, err := acquireAppLock(dir)
	if err != nil || l == nil || warning != "" {
		t.Fatalf("acquireAppLock = %v, %q, %v", l, warning, err)
	}
	return l
}

// TestAppLockRefusesASecondServer: one lock per data directory; the refusal says who holds it and
// what to do; releasing it lets the next server start.
func TestAppLockRefusesASecondServer(t *testing.T) {
	dir := t.TempDir()
	first := mustLock(t, dir)
	l, _, err := acquireAppLock(dir)
	if !errors.Is(err, errAppLocked) || l != nil {
		t.Fatalf("second lock = %v, %v; want errAppLocked", l, err)
	}
	if msg := err.Error(); !strings.Contains(msg, strconv.Itoa(os.Getpid())) || !strings.Contains(msg, dir) || !strings.Contains(msg, "--data") {
		t.Fatalf("message %q lacks the holder, the directory or the remedy", msg)
	}
	first.release()
	mustLock(t, dir).release()
	if _, err := os.Stat(filepath.Join(dir, appLockName)); err != nil {
		t.Fatalf("the lock file was removed: %v", err)
	}
}

// TestAppLockSurvivesRestart: the restart hands a descriptor over (no close-on-exec); while only
// that descriptor is open the directory stays locked, and the re-executed process adopts it.
func TestAppLockSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	l := mustLock(t, dir)
	env, _ := l.handOver() // undo is for a failed exec; the descriptor is adopted below
	name, v, ok := strings.Cut(env, "=")
	if !ok || name != appLockFDEnv {
		t.Fatalf("handOver env = %q", env)
	}
	fd, err := strconv.Atoi(v)
	if err != nil {
		t.Fatal(err)
	}
	if flags, err := fcntlGetFD(fd); err != nil || flags&syscall.FD_CLOEXEC != 0 {
		t.Fatalf("handed-over descriptor flags %d, %v: must survive exec", flags, err)
	}
	l.release() // exec closes every close-on-exec descriptor; the duplicate keeps the lock
	if _, _, err := acquireAppLock(dir); !errors.Is(err, errAppLocked) {
		t.Fatalf("lock during the restart: %v, want still held", err)
	}
	t.Setenv(appLockFDEnv, v)
	adopted := mustLock(t, dir)
	if _, set := os.LookupEnv(appLockFDEnv); set {
		t.Error("the hand-over variable is still in the environment")
	}
	if _, _, err := acquireAppLock(dir); !errors.Is(err, errAppLocked) {
		t.Fatalf("after adopting: %v, want held", err)
	}
	adopted.release()
	mustLock(t, dir).release()
}

// TestAppLockIgnoresForeignDescriptor: a hand-over variable naming another file is not adopted
// (and that descriptor is left alone).
func TestAppLockIgnoresForeignDescriptor(t *testing.T) {
	dir := t.TempDir()
	other, err := os.CreateTemp(t.TempDir(), "other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	t.Setenv(appLockFDEnv, strconv.Itoa(int(other.Fd())))
	l := mustLock(t, dir)
	defer l.release()
	if l.f.Fd() == other.Fd() {
		t.Fatal("adopted a descriptor of another file")
	}
	if _, err := other.Write([]byte("x")); err != nil {
		t.Fatalf("the foreign descriptor was closed: %v", err)
	}
}

// TestAppLockNeverFollowsSymlinks: a symbolic link in place of the lock file stops the start.
func TestAppLockNeverFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(target, filepath.Join(dir, appLockName)); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if l, _, err := acquireAppLock(dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("acquireAppLock = %v, %v; want refused", l, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the link target was created: %v", err)
	}
}

// TestServeRefusesLockedDataDir: the server command exits with a clear message while another
// server holds the data directory; version and healthcheck never take the lock, and reset-auth
// hands the reset to the running server (GAP-13).
func TestServeRefusesLockedDataDir(t *testing.T) {
	dir := t.TempDir()
	l := mustLock(t, dir)
	defer l.release()
	restore := resetAuthWait
	resetAuthWait = 100 * time.Millisecond
	defer func() { resetAuthWait = restore }()
	var stderr bytes.Buffer
	if code := run([]string{"--data", dir, "--nobrowser"}, &bytes.Buffer{}, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "already running") {
		t.Fatalf("serve = %d (%q), want 1 with the lock message", code, stderr.String())
	}
	var out bytes.Buffer
	stderr.Reset()
	if code := run([]string{"reset-auth", "--data", dir}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "running") {
		t.Fatalf("reset-auth while the directory is locked = %d (%s): %s", code, stderr.String(), out.String())
	}
	if code := run([]string{"version"}, &out, &stderr); code != 0 {
		t.Fatalf("version = %d", code)
	}
	stderr.Reset()
	run([]string{"healthcheck", "--data", dir}, &out, &stderr) // no server: fails, but not on the lock
	if strings.Contains(stderr.String(), "already running") {
		t.Fatalf("healthcheck took the lock: %s", stderr.String())
	}
}

// fcntlGetFD returns a descriptor's flags (FD_CLOEXEC).
func fcntlGetFD(fd int) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}
