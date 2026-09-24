// Package api is Dupearr's HTTP layer: UrlBase handling, auth middleware, the /api/v1 handlers
// (docs/API.md is authoritative), Server-Sent Events, webhooks, and serving the embedded SPA.
//
// Middleware chain (outermost first): body read deadline (requests that announce a body must
// send it within 30 s) → panic recovery (500 JSON, stack only in the log) → request
// logging (debug level, secrets redacted) → security headers → UrlBase handling (requests outside
// the base get 307 to {UrlBase}{path}; the base is stripped internally) → authentication
// (internal/auth) → routes (stdlib ServeMux with method patterns).
//
// Conventions (docs/API.md): JSON bodies use camelCase; slices and maps are never encoded as null;
// validation errors are 400 [{propertyName, errorMessage}]; other errors are {"message"} with
// 404/409/5xx and never carry internal error text (500s are logged instead); secrets are masked
// as "********" and a mask sent back on update keeps the stored value. POST creates answer 201,
// PUT updates 202 (like Servarr).
package api

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/commands"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Deps are the API server's dependencies.
type Deps struct {
	Config      *config.Manager
	Store       store.Store
	Bus         *events.Bus
	Log         *slog.Logger
	Logs        *logging.Manager
	Auth        *auth.Service
	Commands    *commands.Manager
	Scanner     *scanner.Service
	Executor    *executor.Service
	Health      *health.Checker
	Backups     *backup.Service
	Notifier    *notifications.Service
	PlexOpts    plex.Options
	PlexFactory func(s models.MediaServer) *plex.Client
	ArrFactory  func(a models.ArrInstance) *arr.Client
	// TautulliFactory returns the client of a Tautulli connection (connection tests;
	// docs/DECISIONS.md D10). nil: tests answer 503.
	TautulliFactory func(t models.TautulliInstance) *tautulli.Client
	WebFS           fs.FS // embedded SPA (web/dist); may lack index.html in dev
	StartTime       time.Time
	Restart         func() // graceful restart (re-exec); call after the response is written
}

// The subsets of the services the handlers use. The concrete services satisfy them (checked
// below); tests substitute fakes.
type (
	scannerService interface {
		SyncLibraries(ctx context.Context, serverID int64) error
		Reevaluate(ctx context.Context, groupID int64) (*models.DuplicateGroup, error)
		ReevaluateAll(ctx context.Context) error
		ReevaluateMatching(ctx context.Context, match func(*models.DuplicateGroup) bool) (int, error)
	}
	executorService interface {
		ApproveReviewed(ctx context.Context, groupID int64, trigger, signature string) ([]models.Action, error)
		Restore(ctx context.Context, actionID int64) error
	}
	backupService interface {
		Create(ctx context.Context, kind string) (*backup.Backup, error)
		List() ([]backup.Backup, error)
		File(kind, name string) (string, error)
		Delete(id int64) error
		StageRestore(ctx context.Context, id int64, opts backup.RestoreOptions) (*backup.RestoreSummary, error)
		StageRestoreUpload(ctx context.Context, r io.Reader, size int64, opts backup.RestoreOptions) (*backup.RestoreSummary, error)
		ConfirmRestore(ctx context.Context) error
		DiscardRestore() error
	}
)

var (
	_ scannerService  = (*scanner.Service)(nil)
	_ executorService = (*executor.Service)(nil)
	_ backupService   = (*backup.Service)(nil)
)

// Server is the HTTP API.
type Server struct {
	d   Deps
	log *slog.Logger

	scanner  scannerService  // nil when Deps.Scanner is nil
	executor executorService // nil when Deps.Executor is nil
	backups  backupService   // nil when Deps.Backups is nil

	now      func() time.Time
	isDocker func() bool

	ping pingCache

	// connMu orders URL changes of media servers / *arr instances (write lock: quarantine + save)
	// against approvals (read lock: endpoint check + queueing), so an approval can never queue
	// removals with ids read from an endpoint that was just replaced (see endpoints.go).
	connMu sync.RWMutex

	// Background re-evaluation of open groups (settings/profile changes), coalesced. bgCtx is
	// cancelled by Close.
	bgMu      sync.Mutex
	bgRunning bool
	bgPending bool
	bgClosed  bool
	bgWG      sync.WaitGroup
	bgCtx     context.Context
	bgCancel  context.CancelFunc

	sseKeepAlive time.Duration

	// webhookScans rate-limits the targeted scans webhooks queue (webhooks.go).
	webhookScans *webhookLimiter
}

// New returns a Server.
func New(d Deps) *Server {
	s := &Server{
		d:            d,
		log:          d.Log,
		now:          time.Now,
		isDocker:     runningInDocker,
		sseKeepAlive: sseKeepAliveInterval,
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	s.webhookScans = newWebhookLimiter(func() time.Time { return s.now() })
	if d.Scanner != nil {
		s.scanner = d.Scanner
	}
	if d.Executor != nil {
		s.executor = d.Executor
	}
	if d.Backups != nil {
		s.backups = d.Backups
	}
	if s.d.StartTime.IsZero() {
		s.d.StartTime = time.Now()
	}
	return s
}

// Close stops background work started by handlers (the coalesced re-evaluation of open groups
// after a settings or profile change) and waits for it to return. Call it after the HTTP servers
// have shut down and before the store is closed; later changes no longer start background work.
// It is safe to call more than once.
func (s *Server) Close() {
	s.bgMu.Lock()
	s.bgClosed = true
	s.bgMu.Unlock()
	s.bgCancel()
	s.bgWG.Wait()
}

// Handler returns the full mux incl. UrlBase handling, middleware, SPA fallback.
// Long-lived handlers (SSE) must return when the request context is cancelled: cmd/dupearr
// cancels every request context shortly after shutdown begins.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	var h http.Handler = mux
	if s.d.Auth != nil {
		h = s.d.Auth.Middleware(h)
	} else {
		h = failClosed(h)
	}
	h = s.urlBaseMiddleware(h)
	h = securityHeaders(h)
	h = s.logRequests(h)
	h = s.recoverPanics(h)
	h = limitBodyRead(h)
	return h
}

// routes registers every endpoint of docs/API.md.
func (s *Server) routes(mux *http.ServeMux) {
	// Unauthenticated / bootstrap
	mux.HandleFunc("GET /ping", s.handlePing)
	mux.HandleFunc("GET /login", s.serveSPA)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /logout", s.handleLogoutGet)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /initialize.json", s.handleInitialize)
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /api/v1/auth/sessions/revoke", s.handleRevokeSessions)

	// System
	mux.HandleFunc("GET /api/v1/system/status", s.handleSystemStatus)
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/health/check", s.handleHealthCheck)
	mux.HandleFunc("GET /api/v1/system/task", s.handleTasks)
	mux.HandleFunc("POST /api/v1/system/restart", s.handleRestart)
	mux.HandleFunc("GET /api/v1/log", s.handleLogs)
	mux.HandleFunc("GET /api/v1/log/file", s.handleLogFiles)
	mux.HandleFunc("GET /api/v1/log/file/{filename}", s.handleLogFile)
	mux.HandleFunc("GET /api/v1/system/backup", s.handleBackups)
	mux.HandleFunc("POST /api/v1/system/backup", s.handleBackupCreate)
	mux.HandleFunc("DELETE /api/v1/system/backup/{id}", s.handleBackupDelete)
	mux.HandleFunc("POST /api/v1/system/backup/restore/upload", s.handleBackupRestoreUpload)
	mux.HandleFunc("POST /api/v1/system/backup/restore/confirm", s.handleBackupRestoreConfirm)
	mux.HandleFunc("DELETE /api/v1/system/backup/restore", s.handleBackupRestoreDiscard)
	mux.HandleFunc("POST /api/v1/system/backup/restore/{id}", s.handleBackupRestore)
	mux.HandleFunc("POST /api/v1/system/backup/download/{id}", s.handleBackupDownloadPassword)
	mux.HandleFunc("GET /backup/{type}/{name}", s.handleBackupDownload)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)

	// Commands
	mux.HandleFunc("POST /api/v1/command", s.handleCommandCreate)
	mux.HandleFunc("GET /api/v1/command", s.handleCommands)
	mux.HandleFunc("GET /api/v1/command/{id}", s.handleCommand)

	// Settings
	mux.HandleFunc("GET /api/v1/config/host", s.handleHostConfig)
	mux.HandleFunc("PUT /api/v1/config/host", s.handleHostConfigUpdate)
	mux.HandleFunc("POST /api/v1/config/host/apikey", s.handleRegenerateAPIKey)
	mux.HandleFunc("POST /api/v1/config/host/apikey/reveal", s.handleRevealAPIKey)
	mux.HandleFunc("POST /api/v1/config/host/webhooktoken", s.handleRegenerateWebhookToken)
	mux.HandleFunc("GET /api/v1/config/settings", s.handleSettings)
	mux.HandleFunc("PUT /api/v1/config/settings", s.handleSettingsUpdate)

	// Media servers & libraries
	mux.HandleFunc("GET /api/v1/mediaserver", s.handleMediaServers)
	mux.HandleFunc("POST /api/v1/mediaserver", s.audited(audit.KindConnectionChanged, "Media server added", s.handleMediaServerCreate))
	mux.HandleFunc("POST /api/v1/mediaserver/test", s.handleMediaServerTest)
	mux.HandleFunc("GET /api/v1/mediaserver/{id}", s.handleMediaServer)
	mux.HandleFunc("PUT /api/v1/mediaserver/{id}", s.audited(audit.KindConnectionChanged, "Media server changed", s.handleMediaServerUpdate))
	mux.HandleFunc("DELETE /api/v1/mediaserver/{id}", s.audited(audit.KindConnectionChanged, "Media server removed", s.handleMediaServerDelete))
	mux.HandleFunc("GET /api/v1/mediaserver/{id}/library", s.handleServerLibraries)
	mux.HandleFunc("POST /api/v1/mediaserver/{id}/library/sync", s.handleServerLibrariesSync)
	mux.HandleFunc("GET /api/v1/library", s.handleLibraries)
	mux.HandleFunc("GET /api/v1/library/{id}", s.handleLibrary)
	mux.HandleFunc("PUT /api/v1/library/{id}", s.audited(audit.KindConnectionChanged, "Library settings changed", s.handleLibraryUpdate))
	mux.HandleFunc("GET /api/v1/mediacover/{serverId}", s.handleMediaCover)
	mux.HandleFunc("POST /api/v1/plex/pin", s.handlePlexPinCreate)
	mux.HandleFunc("GET /api/v1/plex/pin/{id}", s.handlePlexPinCheck)
	mux.HandleFunc("GET /api/v1/plex/servers", s.handlePlexServers)

	// Applications (*arr instances)
	mux.HandleFunc("GET /api/v1/arr", s.handleArrs)
	mux.HandleFunc("POST /api/v1/arr", s.audited(audit.KindConnectionChanged, "Application added", s.handleArrCreate))
	mux.HandleFunc("POST /api/v1/arr/test", s.handleArrTest)
	mux.HandleFunc("GET /api/v1/arr/{id}", s.handleArr)
	mux.HandleFunc("PUT /api/v1/arr/{id}", s.audited(audit.KindConnectionChanged, "Application changed", s.handleArrUpdate))
	mux.HandleFunc("DELETE /api/v1/arr/{id}", s.audited(audit.KindConnectionChanged, "Application removed", s.handleArrDelete))

	// Watch history (Tautulli connections, docs/DECISIONS.md D10)
	mux.HandleFunc("GET /api/v1/tautulli", s.handleTautullis)
	mux.HandleFunc("POST /api/v1/tautulli", s.audited(audit.KindConnectionChanged, "Tautulli connection added", s.handleTautulliCreate))
	mux.HandleFunc("POST /api/v1/tautulli/test", s.handleTautulliTest)
	mux.HandleFunc("GET /api/v1/tautulli/{id}", s.handleTautulli)
	mux.HandleFunc("PUT /api/v1/tautulli/{id}", s.audited(audit.KindConnectionChanged, "Tautulli connection changed", s.handleTautulliUpdate))
	mux.HandleFunc("DELETE /api/v1/tautulli/{id}", s.audited(audit.KindConnectionChanged, "Tautulli connection removed", s.handleTautulliDelete))

	// Path mappings
	mux.HandleFunc("GET /api/v1/pathmapping", s.handlePathMappings)
	mux.HandleFunc("POST /api/v1/pathmapping", s.audited(audit.KindConnectionChanged, "Path mapping added", s.handlePathMappingCreate))
	mux.HandleFunc("GET /api/v1/pathmapping/{id}", s.handlePathMapping)
	mux.HandleFunc("PUT /api/v1/pathmapping/{id}", s.audited(audit.KindConnectionChanged, "Path mapping changed", s.handlePathMappingUpdate))
	mux.HandleFunc("DELETE /api/v1/pathmapping/{id}", s.audited(audit.KindConnectionChanged, "Path mapping removed", s.handlePathMappingDelete))

	// Profiles
	mux.HandleFunc("GET /api/v1/profile", s.handleProfiles)
	mux.HandleFunc("POST /api/v1/profile", s.handleProfileCreate)
	mux.HandleFunc("GET /api/v1/profile/schema", s.handleProfileSchema)
	mux.HandleFunc("GET /api/v1/profile/{id}", s.handleProfile)
	mux.HandleFunc("PUT /api/v1/profile/{id}", s.handleProfileUpdate)
	mux.HandleFunc("DELETE /api/v1/profile/{id}", s.handleProfileDelete)

	// Duplicates
	mux.HandleFunc("GET /api/v1/duplicate", s.handleDuplicates)
	mux.HandleFunc("GET /api/v1/duplicate/stats", s.handleDuplicateStats)
	mux.HandleFunc("POST /api/v1/duplicate/bulk", s.handleDuplicateBulk)
	mux.HandleFunc("GET /api/v1/duplicate/{id}", s.handleDuplicate)
	mux.HandleFunc("POST /api/v1/duplicate/{id}/approve", s.handleDuplicateApprove)
	mux.HandleFunc("POST /api/v1/duplicate/{id}/ignore", s.handleDuplicateIgnore)
	mux.HandleFunc("POST /api/v1/duplicate/{id}/unignore", s.handleDuplicateUnignore)
	mux.HandleFunc("PUT /api/v1/duplicate/{id}/file/{fileId}/override", s.handleDuplicateOverride)
	mux.HandleFunc("POST /api/v1/duplicate/{id}/rescan", s.handleDuplicateRescan)

	// Activity
	mux.HandleFunc("GET /api/v1/queue", s.handleQueue)
	mux.HandleFunc("DELETE /api/v1/queue/{id}", s.handleQueueCancel)
	mux.HandleFunc("GET /api/v1/action", s.handleActions)
	mux.HandleFunc("POST /api/v1/action/{id}/restore", s.handleActionRestore)
	mux.HandleFunc("GET /api/v1/history", s.handleHistory)
	mux.HandleFunc("GET /api/v1/scan", s.handleScans)

	// Exclusions
	mux.HandleFunc("GET /api/v1/exclusion", s.handleExclusions)
	mux.HandleFunc("POST /api/v1/exclusion", s.handleExclusionCreate)
	mux.HandleFunc("DELETE /api/v1/exclusion/{id}", s.handleExclusionDelete)

	// Notifications
	mux.HandleFunc("GET /api/v1/notification", s.handleNotifications)
	mux.HandleFunc("POST /api/v1/notification", s.audited(audit.KindConnectionChanged, "Notification connection added", s.handleNotificationCreate))
	mux.HandleFunc("POST /api/v1/notification/test", s.handleNotificationTest)
	mux.HandleFunc("GET /api/v1/notification/schema", s.handleNotificationSchema)
	mux.HandleFunc("GET /api/v1/notification/triggers", s.handleNotificationTriggers)
	mux.HandleFunc("GET /api/v1/notification/{id}", s.handleNotification)
	mux.HandleFunc("PUT /api/v1/notification/{id}", s.audited(audit.KindConnectionChanged, "Notification connection changed", s.handleNotificationUpdate))
	mux.HandleFunc("DELETE /api/v1/notification/{id}", s.audited(audit.KindConnectionChanged, "Notification connection removed", s.handleNotificationDelete))

	// Webhooks (webhook token — or, deprecated, the API key — required by the auth middleware)
	mux.HandleFunc("POST /api/v1/webhook/radarr", s.arrWebhookHandler(models.ArrRadarr))
	mux.HandleFunc("POST /api/v1/webhook/sonarr", s.arrWebhookHandler(models.ArrSonarr))
	mux.HandleFunc("POST /api/v1/webhook/plex", s.handlePlexWebhook)

	// Fallbacks: JSON 404/405 under /api, the SPA everywhere else.
	mux.Handle("/api/", s.apiFallback(mux))
	mux.HandleFunc("/", s.serveSPA)
}

// failClosed refuses everything but /ping when no auth service is configured.
func failClosed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			next.ServeHTTP(w, r)
			return
		}
		writeMessage(w, http.StatusServiceUnavailable, "Authentication is not configured")
	})
}

// urlBase returns the configured UrlBase ("" or "/something").
func (s *Server) urlBase() string {
	if s.d.Config == nil {
		return ""
	}
	return s.d.Config.Get().UrlBase
}
