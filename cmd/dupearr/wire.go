package main

import (
	"context"
	"crypto/rand"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/api"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/commands"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/version"
	"github.com/sl0wz3r/dupearr/web"
)

const (
	dbFileName        = "dupearr.db"
	logDirName        = "logs"
	plexClientIDKey   = "plex.clientIdentifier" // store.SettingsRepo value
	httpClientTimeout = 30 * time.Second
	scanConcurrency   = 4
)

// Compile-time checks that the concrete clients satisfy the interfaces their consumers declare.
var (
	_ scanner.PlexClient  = (*plex.Client)(nil)
	_ scanner.ArrClient   = (*arr.Client)(nil)
	_ executor.PlexClient = (*plex.Client)(nil)
	_ executor.ArrClient  = (*arr.Client)(nil)
)

// app holds every long-lived service of a running server.
type app struct {
	opts      options
	startTime time.Time

	cfg  *config.Manager
	log  *slog.Logger
	logs *logging.Manager
	db   *database.DB
	bus  *events.Bus

	notifier    *notifications.Service
	plexOpts    plex.Options
	plexFactory func(models.MediaServer) *plex.Client
	arrFactory  func(models.ArrInstance) *arr.Client

	scanner  *scanner.Service
	executor *executor.Service
	health   *health.Checker
	backups  *backup.Service
	commands *commands.Manager
	auth     *auth.Service
	api      *api.Server

	restartOnce sync.Once
	restartCh   chan struct{}
}

// newApp builds every service in dependency order (docs/CONTRACTS.md "cmd/dupearr"). On error,
// whatever was already opened is closed.
func newApp(ctx context.Context, opts options) (*app, error) {
	a := &app{opts: opts, startTime: time.Now(), restartCh: make(chan struct{})}
	if err := a.wire(ctx); err != nil {
		if a.log != nil {
			a.log.Error("Startup failed", "error", err)
		}
		a.close()
		return nil, err
	}
	return a, nil
}

func (a *app) wire(ctx context.Context) error {
	dataDir := a.opts.dataDir
	if err := os.MkdirAll(dataDir, dataDirMode); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	// config.xml → logging
	cfg, err := config.Load(dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	a.cfg = cfg
	c := cfg.Get()
	a.log, a.logs, err = logging.Setup(filepath.Join(dataDir, logDirName), c.LogLevel, c.LogSizeLimit)
	if err != nil {
		return fmt.Errorf("set up logging: %w", err)
	}
	// The standard library logger (net/http reports through it) goes to the application log, never
	// with a free-form URL's peer bytes (logging.StdLogWriter).
	stdlog.SetFlags(0)
	stdlog.SetOutput(logging.StdLogWriter(a.log))
	log := a.log
	log.Info("Starting Dupearr", "version", version.Version, "commit", version.Commit,
		"os", runtime.GOOS, "arch", runtime.GOARCH, "dataDir", dataDir)

	// A restore staged by the previous process is swapped in before the DB is opened; it may
	// have replaced config.xml, so reload it.
	restored, err := backup.ApplyPendingRestore(dataDir)
	if err != nil {
		return fmt.Errorf("apply pending backup restore: %w", err)
	}
	if restored {
		log.Warn("Applied a pending backup restore")
		if a.cfg, err = config.Load(dataDir); err != nil {
			return fmt.Errorf("reload config after restore: %w", err)
		}
		if err := a.logs.SetLevel(a.cfg.Get().LogLevel); err != nil {
			log.Warn("Invalid log level in restored config", "error", err)
		}
	}
	a.cfg.OnChange(a.onConfigChange)

	// Triggers and views are never part of Dupearr's schema: drop any (e.g. adopted from a
	// tampered backup by an earlier version) before they can run.
	dbPath := filepath.Join(dataDir, dbFileName)
	removed, err := backup.RemoveForeignSchemaObjects(ctx, dataDir, dbPath)
	if err != nil {
		return fmt.Errorf("check the database schema: %w", err)
	}
	for _, obj := range removed {
		log.Error("Removed a database object Dupearr does not create; the database was modified by someone else "+
			"(for example through a tampered backup). Review the user, API key, connections, notifications and settings",
			"object", obj)
	}

	// database (+ migrations, default profiles and settings)
	a.db, err = database.Open(ctx, dbPath, component(log, "Database"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := a.db.Seed(ctx, engine.ProfileTemplates()); err != nil {
		return fmt.Errorf("seed database: %w", err)
	}
	// `dupearr reset-auth` ran while the previous process was serving: reset now, before
	// authentication is set up and before any request is accepted.
	if err := a.applyRequestedAuthReset(ctx); err != nil {
		return err
	}

	a.bus = events.New()
	a.notifier = notifications.New(a.db, component(log, "Notifications"))

	// integration client factories
	clientID, err := plexClientIdentifier(ctx, a.db)
	if err != nil {
		return err
	}
	a.plexOpts = plex.Options{
		ClientIdentifier: clientID,
		Product:          "Dupearr",
		Version:          version.Version,
		VerifyTLS:        true, // plex.tv; per-server clients use MediaServer.VerifyTLS
		Timeout:          httpClientTimeout,
	}
	a.plexFactory = func(s models.MediaServer) *plex.Client {
		o := a.plexOpts
		o.VerifyTLS = s.VerifyTLS
		return plex.New(s.URL, s.Token, o)
	}
	a.arrFactory = func(inst models.ArrInstance) *arr.Client {
		return arr.New(inst, arr.Options{VerifyTLS: inst.VerifyTLS, Timeout: httpClientTimeout})
	}

	// scanner → executor. The closures below reference services built later in this function;
	// they are only invoked once commands run, after wiring has finished.
	a.scanner = scanner.New(scanner.Deps{
		Store:       a.db,
		Bus:         a.bus,
		Log:         component(log, "Scanner"),
		Notifier:    a.notifier,
		PlexFactory: func(s models.MediaServer) scanner.PlexClient { return a.plexFactory(s) },
		ArrFactory:  func(i models.ArrInstance) scanner.ArrClient { return a.arrFactory(i) },
		Now:         time.Now,
		Concurrency: scanConcurrency,
		// Auto mode approves exactly what the scan checked: the executor refuses the group when
		// its decisions changed since (another scan, a re-evaluation or the user).
		AutoApprove: func(ctx context.Context, groupID int64, trigger, signature string) error {
			_, err := a.executor.ApproveReviewed(ctx, groupID, trigger, signature)
			return err
		},
	})
	a.executor = executor.New(executor.Deps{
		Store:       a.db,
		Bus:         a.bus,
		Log:         component(log, "Executor"),
		Notifier:    a.notifier,
		PlexFactory: func(s models.MediaServer) executor.PlexClient { return a.plexFactory(s) },
		ArrFactory:  func(i models.ArrInstance) executor.ArrClient { return a.arrFactory(i) },
		Now:         time.Now,
		Enqueue: func(ctx context.Context, name string, body any, trigger string) error {
			_, err := a.commands.Enqueue(ctx, name, body, trigger)
			return err
		},
		DataDir: a.cfg.DataDir(),
		// Approvals and the scanner's read → evaluate → upsert of a group exclude each other, so
		// a scan or re-evaluation never stores a status it read before an approval over it.
		GroupLock: a.scanner.GroupLock(),
	})

	// health → backup → commands
	a.health = health.New(health.Deps{
		Store:     a.db,
		Config:    a.cfg,
		Bus:       a.bus,
		Notifier:  a.notifier,
		Log:       component(log, "Health"),
		StartTime: a.startTime, // OnHealthIssue waits for the boot grace period (see checkHealthAfterGrace)
		WebhookMasterKeyUsed: func() time.Time {
			if a.auth == nil {
				return time.Time{}
			}
			return a.auth.WebhookMasterKeyLastUsed()
		},
		ProxyTrust: func() (int, int) {
			if a.auth == nil {
				return 0, 0
			}
			t := a.auth.NetworkTrust()
			return len(t.TrustedProxies), len(t.AllowedHosts)
		},
		UntrustedProxySeen: func() (time.Time, string) {
			if a.auth == nil {
				return time.Time{}, ""
			}
			return a.auth.UntrustedProxySeen()
		},
		PlexFactory: func(s models.MediaServer) interface {
			Identity(context.Context) (*plex.Identity, error)
			MediaDeletionAllowed(context.Context) (bool, error)
		} {
			return a.plexFactory(s)
		},
		ArrFactory: func(i models.ArrInstance) interface {
			Status(context.Context) (*arr.SystemStatus, error)
		} {
			return a.arrFactory(i)
		},
	})
	a.backups = backup.New(dataDir, a.cfg, a.db, component(log, "Backup"))
	a.commands = commands.New(a.db, a.bus, component(log, "Commands"))
	a.registerCommands()

	// auth → api
	a.auth, err = auth.New(a.cfg, a.db, component(log, "Auth"))
	if err != nil {
		return fmt.Errorf("initialize authentication: %w", err)
	}
	a.api = api.New(api.Deps{
		Config:      a.cfg,
		Store:       a.db,
		Bus:         a.bus,
		Log:         component(log, "Api"),
		Logs:        a.logs,
		Auth:        a.auth,
		Commands:    a.commands,
		Scanner:     a.scanner,
		Executor:    a.executor,
		Health:      a.health,
		Backups:     a.backups,
		Notifier:    a.notifier,
		PlexOpts:    a.plexOpts,
		PlexFactory: a.plexFactory,
		ArrFactory:  a.arrFactory,
		WebFS:       web.FS(),
		StartTime:   a.startTime,
		Restart:     a.requestRestart,
	})
	return nil
}

// close releases the database and log files. Safe on a partially wired app.
func (a *app) close() {
	if a.db != nil {
		if err := a.db.Close(); err != nil && a.log != nil {
			a.log.Error("Failed to close database", "error", err)
		}
	}
	if a.logs != nil {
		_ = a.logs.Close()
	}
}

// requestRestart asks run to shut down gracefully and re-exec (api.Deps.Restart).
func (a *app) requestRestart() {
	a.restartOnce.Do(func() { close(a.restartCh) })
}

// onConfigChange applies config.xml changes that take effect without a restart: the log level
// and the log file size limit.
func (a *app) onConfigChange(prev, next config.Config) {
	if prev.LogLevel != next.LogLevel {
		if err := a.logs.SetLevel(next.LogLevel); err != nil {
			a.log.Warn("Failed to change log level", "level", next.LogLevel, "error", err)
		} else {
			// Logged at a level the new setting still shows: a quieter log must never hide that it
			// was made quieter (the change is also recorded in the history; GAP-12).
			level := slog.LevelInfo
			for _, l := range []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				if level = l; a.log.Enabled(context.Background(), l) {
					break
				}
			}
			a.log.Log(context.Background(), level, "Log level changed", "from", prev.LogLevel, "level", next.LogLevel)
		}
	}
	if prev.LogSizeLimit != next.LogSizeLimit {
		a.logs.SetSizeLimit(next.LogSizeLimit)
		a.log.Info("Log file size limit changed", "sizeLimitMB", next.LogSizeLimit)
	}
}

// component derives a service logger; logging maps the "component" attribute to Entry.Logger.
func component(l *slog.Logger, name string) *slog.Logger {
	return l.With("component", name)
}

// plexClientIdentifier returns the install's stable X-Plex-Client-Identifier, generating and
// persisting one on first start.
func plexClientIdentifier(ctx context.Context, st store.Store) (string, error) {
	id, ok, err := st.Settings().GetValue(ctx, plexClientIDKey)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", plexClientIDKey, err)
	}
	if ok && id != "" {
		return id, nil
	}
	id = newUUID()
	if err := st.Settings().SetValue(ctx, plexClientIDKey, id); err != nil {
		return "", fmt.Errorf("save %s: %w", plexClientIDKey, err)
	}
	return id, nil
}

// newUUID returns a random (version 4) UUID string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (Go ≥ 1.24)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
