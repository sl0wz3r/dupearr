package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The app-level data-directory lock: one server per data directory, whatever started it (a
// native or systemd install, `docker exec … /app/dupearr`, a second container on the same /config).
// Two servers on one database would both run the removal queue — bypassing the per-run limits —
// and both write the same logs. The server holds the lock for its whole lifetime, across in-place
// restarts (re-exec). Subcommands (healthcheck, version, reset-auth) never take it.
//
// It is a separate file from the Docker entrypoint's ".dupearr.lock", which the entrypoint holds on
// descriptor 9 and the server inherits: a second lock on that file through a new descriptor would
// conflict with it.
const (
	appLockName = ".dupearr-app.lock"
	// appLockFDEnv hands the lock's descriptor to the re-executed process (internal).
	appLockFDEnv = "DUPEARR_APP_LOCK_FD"
)

// errAppLocked means another server holds the data directory's lock.
var errAppLocked = errors.New("another Dupearr server is already running with this data directory")

// lockedError is the message of a refused start (errAppLocked with the details).
func lockedError(dataDir, path string, pid int) error {
	who := ""
	if pid > 0 {
		who = fmt.Sprintf(" (process %d)", pid)
	}
	return fmt.Errorf("%w%s: %s is locked by it (%s). Stop the other server first, or start this one with another --data directory",
		errAppLocked, who, dataDir, path)
}

// readLockPID returns the process id recorded in a lock file (0 when unknown).
func readLockPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// writeLockPID records this process id in the lock file (informational; best effort).
func writeLockPID(f *os.File) {
	if f == nil {
		return
	}
	if err := f.Truncate(0); err != nil {
		return
	}
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	_ = f.Sync()
}
