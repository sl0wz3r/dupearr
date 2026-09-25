// Package health runs *arr-style health checks (System → Health).
//
// Checks (HealthCheck.Source):
//
//   - NoMediaServerCheck (warning): no enabled media server.
//   - MediaServerConnectivityCheck (error, per server): the server cannot be reached or reports a
//     different machine identifier than the one configured.
//   - PlexMediaDeletionCheck (warning, per server; only when "plex" is in deletionMethods): Plex's
//     "Allow media deletion" is off (Plex also requires the owner's token).
//   - PlexOwnerCheck (warning, per server; only when "plex" is in deletionMethods): plex.tv says
//     the server's token is not the owner's, so Plex refuses deletions made with it. Only checked
//     when the client supports it (PlexOwnership); an unknown answer (plex.tv unreachable, token
//     not listed) is not an issue.
//   - ArrConnectivityCheck (error, per instance): a Radarr/Sonarr instance cannot be reached.
//   - ArrRecycleBinCheck (notice, per instance; only when "arr" is in deletionMethods): the *arr
//     has no recycling bin, so deletes through it are permanent.
//   - PathMappingCheck (warning; only when "filesystem" is in deletionMethods): an enabled
//     library folder has no server path mapping, or its mapped local folder does not exist.
//   - RecycleBinCheck (error when the configured recycle bin is not usable: not absolute, not a
//     folder, not writable, missing with a missing or unwritable parent folder, or it is/contains
//     a mapped media folder, which the executor refuses; a bin that does not exist yet but whose
//     parent is a writable folder is fine — it is created on first use; warning when it lies
//     inside a mapped Plex library folder without a .plexignore that excludes everything, i.e.
//     has a "*" line).
//   - Probes of individual servers/instances that panic are reported as warnings for them.
//   - DiscDetectionUnavailable (notice): full-disc detection (settings.DetectDiscs) is on, but no
//     folder of an enabled movie library on an enabled server maps to a local path, so the scan
//     cannot look for BDMV/VIDEO_TS/ISO backups next to the movies.
//   - DryRunCheck (notice): dry run is on.
//   - AuthenticationCheck (warning): authentication method None.
//   - ExternalAuthCheck (warning: authentication method External without trusted proxies, i.e.
//     any client that reaches the port directly and names Dupearr by an IP address or a private
//     host name is trusted; notice: External with trusted proxies — the port must still only be
//     reachable through the authenticating reverse proxy).
//   - ReverseProxyCheck (warning): a request with a forwarding header (X-Forwarded-For,
//     Forwarded, X-Real-IP) came from a local peer that is not one of the trusted proxies — a
//     reverse proxy missing from the trusted proxies (Settings → General or
//     DUPEARR__AUTH__TRUSTEDPROXIES; its clients then share one login throttling key and
//     local-address checks cannot see them). It clears once the proxy is trusted.
//   - LastScanCheck (warning): the most recent scan failed.
//   - DatabaseCheck (error): the database does not answer.
//   - With two or more enabled Plex servers only (docs/DECISIONS.md D11): MultiServerFoldersCheck
//     (notice: two servers index the same folders, or a disabled server overlaps an enabled one and
//     its files are not protected), ArrServerLinksCheck (warning: an *arr instance's media server
//     links are not confirmed), MultiServerMappingCheck (warning: a server not declared separate
//     has an unmapped enabled library folder), SeparateServerCheck (warning: a separate server
//     listed files with the same name and size as another server's in the last full scan) and
//     MediaServerIdentityCheck (warning: an enabled server is stored without its identity).
//
// Like the *arr apps only failing checks are reported. Checks run concurrently, each bounded by
// a 10 second timeout. Every Run caches its results, publishes them on the event bus (name
// "health", action "sync", resource = the full list) and sends OnHealthIssue / OnHealthRestored
// notifications for warnings and errors that appeared / disappeared. Which issues were already
// notified is remembered in the settings store, so a restart does not repeat notifications.
//
// Like the *arr apps, OnHealthIssue notifications are held back during a boot grace period
// (BootGracePeriod after Deps.StartTime): servers that start after Dupearr (a Plex container
// booting alongside it) do not cause an issue + restored pair. Checks still run and are displayed;
// an issue that is still present when the grace period is over is notified by the first Run
// after it (GracePeriodEnd tells the caller when to schedule that run).
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/tautulli"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Check sources (HealthCheck.Source).
const (
	SourceNoMediaServer           = "NoMediaServerCheck"
	SourceMediaServerConnectivity = "MediaServerConnectivityCheck"
	SourcePlexMediaDeletion       = "PlexMediaDeletionCheck"
	SourcePlexOwner               = "PlexOwnerCheck"
	SourceArrConnectivity         = "ArrConnectivityCheck"
	SourceArrRecycleBin           = "ArrRecycleBinCheck"
	SourcePathMapping             = "PathMappingCheck"
	SourceRecycleBin              = "RecycleBinCheck"
	SourceDryRun                  = "DryRunCheck"
	SourceAuthentication          = "AuthenticationCheck"
	SourceExternalAuth            = "ExternalAuthCheck"
	SourceWebhookAPIKey           = "WebhookApiKeyCheck"
	SourceReverseProxy            = "ReverseProxyCheck"
	SourceLastScan                = "LastScanCheck"
	SourceDatabase                = "DatabaseCheck"
	// SourceDiscDetection (notice): full-disc detection is on but no enabled movie library folder
	// is mapped to a local path, so Dupearr cannot look for discs (docs/DECISIONS.md D9).
	SourceDiscDetection = "DiscDetectionUnavailable"
	// SourceTautulliConnectivity (error): a Tautulli connection cannot be read, or monitors another
	// Plex server (docs/DECISIONS.md D10).
	SourceTautulliConnectivity = "TautulliConnectivityCheck"
	// SourceWatchHistory: a profile ranks by play history but a server it applies to has no enabled
	// Tautulli (warning), or Tautulli keeps no history for some libraries or users (notice).
	SourceWatchHistory = "WatchHistoryCheck"
)

// CheckTimeout bounds each check of a Run.
const CheckTimeout = 10 * time.Second

// BootGracePeriod is how long after start OnHealthIssue notifications are held back (like the
// *arr apps' startup grace period). Deps.BootGracePeriod overrides it.
const BootGracePeriod = 15 * time.Minute

// PlexTVTimeout bounds the plex.tv lookup of PlexOwnerCheck: plex.tv is often unreachable in
// LAN-only setups, and an unknown answer is not an issue.
const PlexTVTimeout = 5 * time.Second

const (
	// notifiedKey is the settings value holding the issues already notified (JSON).
	notifiedKey = "health.notifiedIssues"
	// loadTimeout bounds reading the shared inputs (settings, connections) of a Run.
	loadTimeout = 10 * time.Second
	// timeoutGrace is how long a check may still deliver its own result after its timeout.
	timeoutGrace = time.Second
)

// ArrMediaManagement is an optional capability of the values returned by Deps.ArrFactory: when
// such a value also implements it (the real *arr.Client does), ArrRecycleBinCheck reports
// instances without a recycling bin. It is detected with a type assertion so Deps.ArrFactory
// keeps its documented signature.
type ArrMediaManagement interface {
	MediaManagement(context.Context) (*arr.MediaManagement, error)
}

// MediaServerClient is what the checks read from a media server of any kind: its identity
// (MediaServerConnectivityCheck). The values of Deps.MediaServerFactory and Deps.PlexFactory
// satisfy it.
type MediaServerClient interface {
	Identity(context.Context) (*mediaserver.Identity, error)
}

// PlexOwnership is an optional capability of the media server clients (the real *plex.Client
// implements it): whether the client's token belongs to the owner of the server with the given
// machine identifier, according to plex.tv. known is false when plex.tv does not list the server
// for the token. PlexOwnerCheck uses it; other clients are skipped.
type PlexOwnership interface {
	Ownership(ctx context.Context, machineID string) (owned, known bool, err error)
}

// PlexDeletionSetting is an optional capability of the media server clients (the real
// *plex.Client and every value of Deps.PlexFactory implement it): the server's "Allow media
// deletion" setting. PlexMediaDeletionCheck uses it; other clients are skipped.
type PlexDeletionSetting interface {
	MediaDeletionAllowed(context.Context) (bool, error)
}

var (
	_ PlexOwnership       = (*plex.Client)(nil)
	_ PlexDeletionSetting = (*plex.Client)(nil)
)

// TautulliClient is what the Tautulli checks read (the real *tautulli.Client implements it).
type TautulliClient interface {
	Info(context.Context) (*tautulli.Info, error)
	Users(context.Context) ([]tautulli.User, error)
	Library(ctx context.Context, sectionID string) (*tautulli.Library, error)
}

var _ TautulliClient = (*tautulli.Client)(nil)

// Deps are the checker's dependencies.
//
// Store is required for every check but AuthenticationCheck and ExternalAuthCheck; Config, Bus, Notifier, Log and the
// factories may be nil (the checks needing them are skipped). A factory may also return nil to
// skip one connection. The value returned by ArrFactory may implement ArrMediaManagement, the
// media server clients PlexOwnership and PlexDeletionSetting.
type Deps struct {
	Store       store.Store
	Config      *config.Manager
	Bus         *events.Bus
	Notifier    *notifications.Service
	Log         *slog.Logger
	PlexFactory func(s models.MediaServer) interface {
		Identity(context.Context) (*plex.Identity, error)
		MediaDeletionAllowed(context.Context) (bool, error)
	}
	ArrFactory func(a models.ArrInstance) interface {
		Status(context.Context) (*arr.SystemStatus, error)
	}
	// MediaServerFactory returns the client of a media server of any supported kind (nil when
	// there is none for its kind). When set it is used instead of PlexFactory, which stays for
	// tests and callers wired before the kind-neutral contract; cmd/dupearr wires only this one.
	MediaServerFactory mediaserver.Factory
	// TautulliFactory returns the client of a Tautulli connection (TautulliConnectivityCheck,
	// WatchHistoryCheck's notices); nil skips those probes.
	TautulliFactory func(t models.TautulliInstance) TautulliClient

	// WebhookMasterKeyUsed reports when a webhook last authenticated with the master API key
	// (zero: never); WebhookApiKeyCheck warns about it. May be nil.
	WebhookMasterKeyUsed func() time.Time
	// ProxyTrust reports how many trusted proxies and allowed hosts are in effect (Settings →
	// General / config.xml, or DUPEARR__AUTH__TRUSTEDPROXIES / DUPEARR__AUTH__ALLOWEDHOSTS);
	// ExternalAuthCheck uses it.
	// May be nil (ExternalAuthCheck then only gives its notice).
	ProxyTrust func() (trustedProxies, allowedHosts int)
	// UntrustedProxySeen reports when a request with a forwarding header last came from a local
	// peer that is not a trusted proxy, and that peer (zero time: never since start);
	// ReverseProxyCheck warns about it. May be nil.
	UntrustedProxySeen func() (time.Time, string)

	// StartTime is when the process started (zero: when New is called); OnHealthIssue
	// notifications are held back until StartTime + BootGracePeriod.
	StartTime time.Time
	// BootGracePeriod overrides the package default BootGracePeriod when > 0; a negative value
	// disables the grace period.
	BootGracePeriod time.Duration
}

// Checker runs and caches health checks. It is safe for concurrent use; runs are serialised.
type Checker struct {
	d   Deps
	log *slog.Logger

	// graceEnd is when the boot grace period ends (zero: no grace period).
	graceEnd time.Time

	// Test hooks.
	timeout       time.Duration
	grace         time.Duration
	plexTVTimeout time.Duration
	now           func() time.Time
	notify        func(ctx context.Context, msg notifications.Message)

	runMu          sync.Mutex // serialises Run (and guards the fields below)
	notified       map[string]notifiedIssue
	notifiedLoaded bool

	mu      sync.RWMutex
	results []models.HealthCheck
}

// notifiedIssue is an issue a notification was sent for (persisted as JSON).
type notifiedIssue struct {
	Source  string            `json:"source"`
	Type    models.HealthType `json:"type"`
	Message string            `json:"message"`
}

// result is one check outcome plus the stable identity used to detect appear/disappear.
type result struct {
	key   string // e.g. "MediaServerConnectivityCheck:server:3"
	check models.HealthCheck
}

// New returns a Checker.
func New(d Deps) *Checker {
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Checker{d: d, log: log, timeout: CheckTimeout, grace: timeoutGrace, plexTVTimeout: PlexTVTimeout, now: time.Now,
		notified: map[string]notifiedIssue{}}
	start := d.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	switch {
	case d.BootGracePeriod > 0:
		c.graceEnd = start.Add(d.BootGracePeriod)
	case d.BootGracePeriod == 0:
		c.graceEnd = start.Add(BootGracePeriod)
	}
	c.notify = func(ctx context.Context, msg notifications.Message) {
		if d.Notifier != nil {
			d.Notifier.Notify(ctx, msg)
		}
	}
	return c
}

// Run runs all checks, caches, publishes, notifies on change.
//
// The result lists failing checks only (never nil), most severe first. If ctx is cancelled
// before every check finished, the previous results are kept and returned unchanged.
func (c *Checker) Run(ctx context.Context) []models.HealthCheck {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	snap := c.load(ctx)
	checks := c.checks()
	outs := make([][]result, len(checks))
	var wg sync.WaitGroup
	for i, ch := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i] = c.runOne(ctx, ch, snap)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		c.log.Debug("Health check run cancelled; keeping the previous results")
		return c.Results()
	}

	var all []result
	for _, o := range outs {
		all = append(all, o...)
	}
	sortResults(all)
	list := make([]models.HealthCheck, 0, len(all))
	for _, r := range all {
		list = append(list, r.check)
	}

	c.mu.Lock()
	c.results = list
	c.mu.Unlock()
	c.d.Bus.Publish(events.Event{Name: events.NameHealth, Action: events.ActionSync, Resource: cloneChecks(list)})
	c.processChanges(ctx, all)
	return cloneChecks(list)
}

// GracePeriodEnd returns when the boot grace period ends (zero when there is none). Issues found
// before then are displayed but only notified by a Run after it, so the caller should schedule a
// CheckHealth shortly after this time.
func (c *Checker) GracePeriodEnd() time.Time { return c.graceEnd }

// inGracePeriod reports whether OnHealthIssue notifications are currently held back.
func (c *Checker) inGracePeriod() bool {
	return !c.graceEnd.IsZero() && c.now().Before(c.graceEnd)
}

// Results returns the cached results of the last Run (never nil).
func (c *Checker) Results() []models.HealthCheck {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneChecks(c.results)
}

// check is one named health check.
type check struct {
	source string
	fn     func(ctx context.Context, s *snapshot) []result
}

func (c *Checker) checks() []check {
	return []check{
		{SourceNoMediaServer, c.checkNoMediaServer},
		{SourceMediaServerConnectivity, c.checkMediaServerConnectivity},
		{SourcePlexMediaDeletion, c.checkPlexMediaDeletion},
		{SourcePlexOwner, c.checkPlexOwner},
		{SourceArrConnectivity, c.checkArrConnectivity},
		{SourceArrRecycleBin, c.checkArrRecycleBin},
		{SourceTautulliConnectivity, c.checkTautulliConnectivity},
		{SourceWatchHistory, c.checkWatchHistory},
		{SourcePathMapping, c.checkPathMapping},
		{SourceDiscDetection, c.checkDiscDetection},
		{SourceRecycleBin, c.checkRecycleBin},
		{SourceDryRun, c.checkDryRun},
		{SourceAuthentication, c.checkAuthentication},
		{SourceExternalAuth, c.checkExternalAuth},
		{SourceWebhookAPIKey, c.checkWebhookAPIKey},
		{SourceReverseProxy, c.checkReverseProxy},
		{SourceLastScan, c.checkLastScan},
		{SourceDatabase, c.checkDatabase},
		// Several media servers (multiserver.go; only with two or more enabled servers).
		{SourceMultiServerFolders, c.checkMultiServerFolders},
		{SourceArrServerLinks, c.checkArrServerLinks},
		{SourceMultiServerMapping, c.checkMultiServerMapping},
		{SourceSeparateServer, c.checkSeparateServer},
		{SourceMediaServerIdentity, c.checkMediaServerIdentity},
	}
}

// runOne runs a check with its timeout, converting panics and hangs into warnings.
func (c *Checker) runOne(ctx context.Context, ch check, snap *snapshot) []result {
	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	done := make(chan []result, 1) // buffered: a hung check's goroutine never blocks forever
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.log.Error("Health check panicked", "check", ch.source, "panic", fmt.Sprint(r))
				done <- []result{{key: ch.source + ":internal", check: models.HealthCheck{
					Source: ch.source, Type: models.HealthWarning,
					Message: "The " + ch.source + " health check failed unexpectedly; see the logs",
				}}}
			}
		}()
		done <- ch.fn(cctx, snap)
	}()
	select {
	case r := <-done:
		return r
	case <-cctx.Done():
	}
	// Prefer the check's own (timeout) result when it arrives shortly after the deadline.
	grace := time.NewTimer(c.grace)
	defer grace.Stop()
	select {
	case r := <-done:
		return r
	case <-grace.C:
	}
	if ctx.Err() != nil {
		return nil
	}
	c.log.Warn("Health check timed out", "check", ch.source, "timeout", c.timeout)
	return []result{{key: ch.source + ":timeout", check: models.HealthCheck{
		Source: ch.source, Type: models.HealthWarning,
		Message: fmt.Sprintf("The %s health check did not complete within %s", ch.source, c.timeout),
	}}}
}

// processChanges sends OnHealthIssue for warnings/errors that appeared (or got more severe) and
// OnHealthRestored for notified issues that disappeared (or dropped to a notice). During the boot
// grace period new or escalated issues are not notified and not recorded as notified, so the
// first Run after the grace period notifies those still present; restored notifications for
// issues notified before the restart are still sent.
func (c *Checker) processChanges(ctx context.Context, all []result) {
	c.loadNotified(ctx)
	grace := c.inGracePeriod()
	current := make(map[string]result, len(all))
	for _, r := range all {
		if _, dup := current[r.key]; !dup {
			current[r.key] = r
		}
	}
	nctx := context.WithoutCancel(ctx)
	changed := false

	restoredKeys := make([]string, 0)
	for key := range c.notified {
		if r, ok := current[key]; ok && serious(r.check.Type) {
			continue
		}
		restoredKeys = append(restoredKeys, key)
	}
	sort.Strings(restoredKeys)
	for _, key := range restoredKeys {
		prev := c.notified[key]
		delete(c.notified, key)
		changed = true
		c.log.Info("Health issue resolved", "source", prev.Source, "message", prev.Message)
		c.notify(nctx, notifications.Message{
			Event:    models.OnHealthRestored,
			Title:    "Health check restored",
			Body:     "Resolved: " + prev.Message,
			Fields:   []notifications.Field{{Name: "Source", Value: prev.Source, Inline: true}},
			Severity: "info",
		})
	}

	for _, r := range all {
		if !serious(r.check.Type) {
			continue
		}
		prev, ok := c.notified[r.key]
		if ok && severity(r.check.Type) <= severity(prev.Type) {
			if prev.Message != r.check.Message || prev.Type != r.check.Type {
				c.notified[r.key] = notifiedIssue{Source: r.check.Source, Type: r.check.Type, Message: r.check.Message}
				changed = true
			}
			continue
		}
		if grace {
			c.log.Info("Health issue (not notified during the startup grace period)", "source", r.check.Source,
				"type", r.check.Type, "message", r.check.Message, "graceEnds", c.graceEnd.Format(time.RFC3339))
			continue
		}
		c.notified[r.key] = notifiedIssue{Source: r.check.Source, Type: r.check.Type, Message: r.check.Message}
		changed = true
		c.log.Warn("Health issue", "source", r.check.Source, "type", r.check.Type, "message", r.check.Message)
		c.notify(nctx, notifications.Message{
			Event:    models.OnHealthIssue,
			Title:    "Health check failure",
			Body:     r.check.Message,
			Fields:   []notifications.Field{{Name: "Source", Value: r.check.Source, Inline: true}},
			Severity: string(r.check.Type),
		})
	}
	if changed {
		c.saveNotified(ctx)
	}
}

// loadNotified reads the notified issues persisted by a previous process (once).
func (c *Checker) loadNotified(ctx context.Context) {
	if c.notifiedLoaded || c.d.Store == nil {
		return
	}
	c.notifiedLoaded = true
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
	defer cancel()
	raw, ok, err := c.d.Store.Settings().GetValue(lctx, notifiedKey)
	if err != nil || !ok || raw == "" {
		if err != nil {
			c.log.Debug("Cannot read notified health issues", "error", err)
		}
		return
	}
	var stored map[string]notifiedIssue
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		c.log.Debug("Ignoring unreadable notified health issues", "error", err)
		return
	}
	for k, v := range stored {
		if _, exists := c.notified[k]; !exists && serious(v.Type) {
			c.notified[k] = v
		}
	}
}

func (c *Checker) saveNotified(ctx context.Context) {
	if c.d.Store == nil {
		return
	}
	b, err := json.Marshal(c.notified)
	if err != nil {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
	defer cancel()
	if err := c.d.Store.Settings().SetValue(sctx, notifiedKey, string(b)); err != nil {
		c.log.Debug("Cannot save notified health issues", "error", err)
	}
}

func serious(t models.HealthType) bool {
	return t == models.HealthWarning || t == models.HealthError
}

func severity(t models.HealthType) int {
	switch t {
	case models.HealthError:
		return 3
	case models.HealthWarning:
		return 2
	case models.HealthNotice:
		return 1
	default:
		return 0
	}
}

// sortResults orders by severity (most severe first), then source, then message.
func sortResults(rs []result) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i].check, rs[j].check
		if sa, sb := severity(a.Type), severity(b.Type); sa != sb {
			return sa > sb
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Message < b.Message
	})
}

func cloneChecks(in []models.HealthCheck) []models.HealthCheck {
	out := make([]models.HealthCheck, len(in))
	copy(out, in)
	return out
}
