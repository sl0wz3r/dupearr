// Command dupearr runs Dupearr, a self-hosted *arr-style service with a web UI that finds
// duplicate movies and TV episodes in Plex, decides which copy to keep with user-defined profiles
// (e.g. "keep the highest resolution") and removes the others safely through Radarr/Sonarr, Plex
// or the filesystem.
//
// Usage:
//
//	dupearr [--data DIR] [--nobrowser]   run the server (web UI + /api/v1)
//	dupearr healthcheck [--data DIR]     exit 0 when GET {UrlBase}/ping answers OK (Docker HEALTHCHECK)
//	dupearr version                      print build information
//	dupearr reset-auth [--data DIR]      reset authentication to Forms with no user (first-run setup)
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sl0wz3r/dupearr/internal/api"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// Subcommands ("" = run the server).
const (
	cmdServe       = ""
	cmdVersion     = "version"
	cmdHealthcheck = "healthcheck"
	cmdResetAuth   = "reset-auth"
)

const (
	dockerDataDir = "/config"
	// shutdownTimeout bounds a graceful shutdown (HTTP drain, then the notifier; the running command
	// is cancelled and awaited): below the 10 s `docker stop` grace of Docker and Unraid.
	shutdownTimeout = 8 * time.Second
	// requestGrace is how long in-flight requests may finish after shutdown starts before every
	// request context is cancelled (which ends long-lived SSE streams).
	requestGrace = 2 * time.Second
	// notifierShutdownTimeout bounds how long queued notifications may still be sent at shutdown
	// (within what is left of shutdownTimeout, but at least minNotifierShutdown).
	notifierShutdownTimeout = 3 * time.Second
	minNotifierShutdown     = time.Second

	// HTTP server limits. There is no WriteTimeout (SSE streams stay open) and no ReadTimeout
	// (backup uploads); the API bounds request bodies itself (internal/api limitBodyRead: a 30 s
	// read deadline for every request that announces a body, expired at once when a handler does
	// not read it; a backup upload gets an idle deadline instead).
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10
)

const usageText = `Dupearr finds and removes duplicate movies and episodes in Plex.

Usage:
  dupearr [--data DIR] [--nobrowser]   run the server
  dupearr healthcheck [--data DIR]     exit 0 when the local server answers /ping
  dupearr version                      print build information
  dupearr reset-auth [--data DIR]      reset authentication (forces first-run setup)

Flags:
`

type options struct {
	command   string
	dataDir   string
	noBrowser bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches a command line and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: %v\n", err)
		return 2
	}

	switch opts.command {
	case cmdVersion:
		printVersion(stdout)
		return 0
	case cmdHealthcheck:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return healthcheck(ctx, opts.dataDir, stderr)
	case cmdResetAuth:
		if err := resetAuth(context.Background(), opts.dataDir, stdout); err != nil {
			fmt.Fprintf(stderr, "dupearr: reset-auth: %v\n", err)
			return 1
		}
		return 0
	default:
		return serve(opts, stderr)
	}
}

// parseArgs accepts the subcommand before or after the flags
// ("dupearr healthcheck --data /config" and "dupearr --data /config healthcheck").
func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("dupearr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.dataDir, "data", "",
		"data `directory` for config.xml, the database, logs and backups\n(default: "+dockerDataDir+" when writable, else <user config dir>/Dupearr)")
	fs.BoolVar(&opts.noBrowser, "nobrowser", false, "do not open the web UI in a browser on startup")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageText)
		fs.PrintDefaults()
	}

	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return opts, err
		}
		if fs.NArg() == 0 {
			break
		}
		if opts.command != "" {
			return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		opts.command = fs.Arg(0)
		rest = fs.Args()[1:]
	}

	switch opts.command {
	case cmdServe, cmdHealthcheck, cmdResetAuth:
	case cmdVersion:
		return opts, nil // needs no data directory
	case "help":
		fs.Usage()
		return opts, flag.ErrHelp
	default:
		return opts, fmt.Errorf("unknown command %q (want %s, %s or %s)", opts.command, cmdVersion, cmdHealthcheck, cmdResetAuth)
	}

	if opts.dataDir == "" {
		opts.dataDir = defaultDataDir()
	}
	abs, err := filepath.Abs(opts.dataDir)
	if err != nil {
		return opts, fmt.Errorf("data directory: %w", err)
	}
	opts.dataDir = abs
	return opts, nil
}

// defaultDataDir is /config when it exists and is writable (Docker), else the per-user config
// directory (~/.config/Dupearr, ~/Library/Application Support/Dupearr, %AppData%\Dupearr).
func defaultDataDir() string {
	if isWritableDir(dockerDataDir) {
		return dockerDataDir
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "Dupearr")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "Dupearr")
	}
	return "Dupearr"
}

// dataDirMode is the mode of a new data directory: it holds config.xml, the database, backups and
// logs, all of them secrets or personal data.
const dataDirMode os.FileMode = 0o700

// dataDirStrip are the permission bits prepareDataDir removes from an existing data directory:
// every permission of "other", and write for the group. Whoever can write the directory can
// replace config.xml (authentication None), the database, or plant a staged restore (.restore)
// that is applied at the next start, even though they cannot read the files themselves.
const dataDirStrip os.FileMode = 0o027

// prepareDataDir creates the data directory (owner-only) when it is missing. An existing one that
// other users can access (created by an earlier version with 0755 or 0775, by hand, or by a NAS
// "new permissions" tool with 0777) loses its permissions for "other" and write permission for
// the group; the group's read and search permissions are kept, since a NAS may share the folder
// with a group on purpose, and everything Dupearr keeps inside is private anyway. The change is
// best effort: when it fails (a folder owned by someone else) the returned warning says so.
func prepareDataDir(dir string) (warning string, err error) {
	if err := os.MkdirAll(dir, dataDirMode); err != nil {
		return "", err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		return "", nil
	}
	restrictDataFiles(dir)
	if fi.Mode().Perm()&dataDirStrip == 0 {
		return "", nil
	}
	mode := fi.Mode().Perm() &^ dataDirStrip
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Sprintf("The data directory %s is accessible to other users (%v) and its permissions could not be restricted (%v); "+
			"restrict them yourself (e.g. chmod g-w,o-rwx), since it holds API keys, tokens and logs", dir, fi.Mode().Perm(), err), nil
	}
	return "", nil
}

// restrictDataFiles makes the files Dupearr keeps secrets in owner-only again when something
// loosened them, e.g. a NAS "new permissions" tool (0666 files, 0777 folders), since the data
// directory itself may stay readable by a group: config.xml, the database and its side files,
// the files replaced by restores (<name>.bak-…) and the Backups folder with its archives. Only
// names Dupearr creates are touched, never through a symlink; failures are ignored (best effort).
func restrictDataFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, p := e.Name(), filepath.Join(dir, e.Name())
		switch {
		case e.Type().IsRegular() && (name == config.FileName || strings.HasPrefix(name, config.FileName+".bak-") ||
			name == dbFileName || strings.HasPrefix(name, dbFileName+"-") || strings.HasPrefix(name, dbFileName+".bak-")):
			restrictMode(p)
		case e.IsDir() && name == backup.BackupsDirName:
			_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err == nil && (d.IsDir() || d.Type().IsRegular()) { // WalkDir never follows symlinks
					restrictMode(path)
				}
				return nil
			})
		}
	}
}

// restrictMode removes the group and other permissions of the regular file or folder p (a
// symlink is left alone).
func restrictMode(p string) {
	fi, err := os.Lstat(p)
	if err != nil || !(fi.Mode().IsRegular() || fi.IsDir()) || fi.Mode().Perm()&0o077 == 0 {
		return
	}
	_ = os.Chmod(p, fi.Mode().Perm()&^0o077)
}

func isWritableDir(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".dupearr-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func printVersion(w io.Writer) {
	built := version.BuildDate
	if built == "" {
		built = "unknown"
	}
	fmt.Fprintf(w, "Dupearr %s (commit %s, built %s, %s %s/%s)\n",
		version.Version, version.Commit, built, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// serve runs the server until SIGINT/SIGTERM or a restart request, then shuts down gracefully
// and, for a restart, re-executes the binary. The data directory's lock (lock.go) is taken first
// and held for the whole lifetime, restarts included: a second server on the same data directory
// exits with a message.
func serve(opts options, stderr io.Writer) int {
	dirWarning, err := prepareDataDir(opts.dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: create data directory: %v\n", err)
		return 1
	}
	lock, lockWarning, err := acquireAppLock(opts.dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: %v\n", err)
		return 1
	}
	defer lock.release()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default handling: a second Ctrl+C kills immediately
	}()

	a, err := newApp(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: %v\n", err)
		return 1
	}
	if lockWarning != "" {
		a.log.Warn(lockWarning)
	}
	if dirWarning != "" {
		a.log.Warn(dirWarning)
	}
	restart, err := a.run(ctx)
	a.close()
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: %v\n", err)
		return 1
	}
	if restart {
		stop()
		return reexec(stderr, lock)
	}
	return 0
}

// reexec replaces the process with a fresh copy of the binary (same args and environment); the
// data directory's lock is handed over, so no other server can start in between.
func reexec(stderr io.Writer, lock *appLock) int {
	if runtime.GOOS == "windows" {
		// Windows cannot replace a running process; exit cleanly and let the service manager
		// (or the user) start Dupearr again.
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "dupearr: restart: %v\n", err)
		return 1
	}
	env := os.Environ()
	lockEnv, undo := lock.handOver()
	if lockEnv != "" {
		env = append(withoutEnv(env, appLockFDEnv), lockEnv)
	}
	err = syscall.Exec(exe, os.Args, env) // only returns on failure
	undo()
	fmt.Fprintf(stderr, "dupearr: restart: exec %s: %v\n", exe, err)
	return 1
}

// withoutEnv returns env without the entries of variable name.
func withoutEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != name {
			out = append(out, kv)
		}
	}
	return out
}

// run starts the command workers and HTTP server(s) and blocks until ctx is done (signal), a
// restart is requested, or a server fails. It then shuts everything down in reverse order.
func (a *app) run(ctx context.Context) (restart bool, err error) {
	c := a.cfg.Get()

	// Commands get their own context so they stop only after HTTP has shut down.
	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	a.recoverInterrupted(ctx)
	go a.commands.Start(workCtx)
	if _, err := a.commands.Enqueue(ctx, models.CmdCheckHealth, struct{}{}, models.TriggerScheduled); err != nil {
		a.log.Warn("Failed to queue startup health check", "error", err)
	}
	go a.checkHealthAfterGrace(workCtx)
	go a.watchResetRequest(workCtx, resetWatchInterval)

	// Every request context derives from serveCtx; cancelling it ends SSE streams on shutdown.
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	servers, serveErr, err := a.listen(c, serveCtx)
	if err != nil {
		a.stopBackground(cancelWork, time.Now().Add(shutdownTimeout))
		return false, err
	}
	a.maybeLaunchBrowser(c)

	select {
	case <-ctx.Done():
		a.log.Info("Shutdown requested")
	case <-a.restartCh:
		restart = true
		a.log.Info("Restart requested")
	case err = <-serveErr:
		a.log.Error("HTTP server failed", "error", err)
	}

	deadline := time.Now().Add(shutdownTimeout)
	shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	grace := time.AfterFunc(requestGrace, cancelServe)
	defer grace.Stop()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			a.log.Warn("HTTP server did not shut down cleanly", "address", srv.Addr, "error", err)
			_ = srv.Close()
		}
	}

	a.stopBackground(cancelWork, deadline)
	a.log.Info("Dupearr stopped", "restart", restart)
	return restart, err
}

// checkHealthAfterGrace queues a CheckHealth right after the health checker's boot grace period,
// during which issues are shown but not notified (health.Checker.GracePeriodEnd): an issue found
// at boot is then notified without waiting for the next scheduled check (up to 6 h).
func (a *app) checkHealthAfterGrace(ctx context.Context) {
	if a.health == nil || a.commands == nil {
		return
	}
	end := a.health.GracePeriodEnd()
	if end.IsZero() {
		return
	}
	t := time.NewTimer(time.Until(end) + time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
	}
	if _, err := a.commands.Enqueue(ctx, models.CmdCheckHealth, struct{}{}, models.TriggerScheduled); err != nil && ctx.Err() == nil {
		a.log.Warn("Failed to queue the health check after the startup grace period", "error", err)
	}
}

// recoverInterrupted runs before any command starts: commands a previous process left running are
// aborted, and removals it left running (a crash or restart in the middle of a delete) are failed
// with their groups, never executed again (executor.Service.RecoverInterrupted).
func (a *app) recoverInterrupted(ctx context.Context) {
	if err := a.db.Commands().FailRunning(ctx); err != nil {
		a.log.Warn("Failed to abort commands interrupted by the last shutdown", "error", err)
	}
	n, err := a.executor.RecoverInterrupted(ctx)
	if err != nil {
		a.log.Error("Failed to recover removals interrupted by the last shutdown", "error", err)
	}
	if n > 0 {
		a.log.Warn("Removals were interrupted by the last shutdown; verify their files, then approve the groups again", "count", n)
	}
}

// stopBackground stops, in order, everything that may still use the database once HTTP is down:
// the API's background jobs (a settings re-evaluation), the command workers (the running command
// is cancelled and awaited) and the notifier's queued sends (bounded by notifierShutdownTimeout and
// what is left until deadline, but given at least minNotifierShutdown). The database is closed
// afterwards by close.
func (a *app) stopBackground(cancelWork context.CancelFunc, deadline time.Time) {
	if a.api != nil {
		a.api.Close()
	}
	cancelWork()
	a.commands.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), notifierBudget(time.Until(deadline)))
	defer cancel()
	if err := a.notifier.Shutdown(ctx); err != nil {
		a.log.Warn("Notifications still being sent were abandoned at shutdown", "error", err)
	}
}

// listen binds the HTTP server (fatal on failure) and, when enabled, the HTTPS server (logged and
// skipped on failure, like *arr: a bad certificate must not take the UI down).
func (a *app) listen(c config.Config, base context.Context) ([]*http.Server, <-chan error, error) {
	handler := a.api.Handler()
	errCh := make(chan error, 2)
	newServer := func(addr string) *http.Server {
		return newHTTPServer(addr, handler, base, slog.NewLogLogger(a.log.Handler(), slog.LevelDebug))
	}
	start := func(srv *http.Server, ln net.Listener, useTLS bool) {
		go func() {
			var err error
			if useTLS {
				err = srv.ServeTLS(ln, "", "")
			} else {
				err = srv.Serve(ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s: %w", srv.Addr, err)
			}
		}()
	}

	addr := listenAddr(c.BindAddress, c.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	srv := newServer(addr)
	start(srv, ln, false)
	servers := []*http.Server{srv}
	a.log.Info("Listening for HTTP", "address", addr, "urlBase", c.UrlBase)

	if c.EnableSsl {
		tlsAddr := listenAddr(c.BindAddress, c.SslPort)
		if tlsSrv, tlsLn, err := listenTLS(c, tlsAddr, newServer); err != nil {
			a.log.Error("HTTPS disabled", "address", tlsAddr, "error", err)
		} else {
			start(tlsSrv, tlsLn, true)
			servers = append(servers, tlsSrv)
			a.log.Info("Listening for HTTPS", "address", tlsAddr, "urlBase", c.UrlBase)
		}
	}
	return servers, errCh, nil
}

// newHTTPServer returns the HTTP(S) server with Dupearr's limits: a header read deadline (slow
// clients cannot hold connections before a request is routed), an idle timeout for keep-alive
// connections and a header size cap. No WriteTimeout: SSE responses stream for hours.
func newHTTPServer(addr string, handler http.Handler, base context.Context, errLog *log.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          errLog,
	}
}

// notifierBudget is how long queued notifications may still be sent at shutdown when left is what
// remains of the shutdown budget.
func notifierBudget(left time.Duration) time.Duration {
	return max(min(left, notifierShutdownTimeout), minNotifierShutdown)
}

func listenTLS(c config.Config, addr string, newServer func(string) *http.Server) (*http.Server, net.Listener, error) {
	tlsCfg, err := loadTLS(c)
	if err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen: %w", err)
	}
	srv := newServer(addr)
	srv.TLSConfig = tlsCfg
	return srv, ln, nil
}

func loadTLS(c config.Config) (*tls.Config, error) {
	if c.SslCertPath == "" || c.SslKeyPath == "" {
		return nil, errors.New("EnableSsl is set but SslCertPath or SslKeyPath is empty")
	}
	cert, err := tls.LoadX509KeyPair(c.SslCertPath, c.SslKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

// listenAddr turns config.xml's BindAddress ("*" = all interfaces) and a port into host:port.
func listenAddr(bind string, port int) string {
	host := strings.Trim(strings.TrimSpace(bind), "[]")
	if host == "*" {
		host = ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// clientHost is the host a local client should use to reach a server bound to bind: fallback for
// wildcard binds, otherwise the bound address itself.
func clientHost(bind, fallback string) string {
	host := strings.Trim(strings.TrimSpace(bind), "[]")
	switch host {
	case "", "*", "0.0.0.0", "::":
		return fallback
	}
	return host
}

func (a *app) maybeLaunchBrowser(c config.Config) {
	if !c.LaunchBrowser || a.opts.noBrowser || runningInDocker() {
		return
	}
	url := "http://" + net.JoinHostPort(clientHost(c.BindAddress, "localhost"), strconv.Itoa(c.Port)) + c.UrlBase + "/"
	if err := openBrowser(url); err != nil {
		a.log.Debug("Could not open a browser", "url", url, "error", err)
	}
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap
	return nil
}

// runningInDocker reports a container (no browser launch): DUPEARR_DOCKER=1 (set by the official
// image, so Podman and Kubernetes count too), /.dockerenv (Docker) or /run/.containerenv (Podman).
func runningInDocker() bool { return api.RunningInDocker() }
