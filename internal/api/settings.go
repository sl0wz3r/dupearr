package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// reevaluateTimeout bounds a background re-evaluation of every open group.
const reevaluateTimeout = 30 * time.Minute

// ---------------------------------------------------------------------------
// /api/v1/config/host
// ---------------------------------------------------------------------------

// hostConfig is the HostConfig resource (config.xml + the local account).
type hostConfig struct {
	BindAddress            string   `json:"bindAddress"`
	Port                   int      `json:"port"`
	URLBase                string   `json:"urlBase"`
	EnableSsl              bool     `json:"enableSsl"`
	SslPort                int      `json:"sslPort"`
	SslCertPath            string   `json:"sslCertPath"`
	SslKeyPath             string   `json:"sslKeyPath"`
	APIKey                 string   `json:"apiKey"`
	AuthenticationMethod   string   `json:"authenticationMethod"`
	AuthenticationRequired string   `json:"authenticationRequired"`
	Username               string   `json:"username"`
	Password               string   `json:"password"`                       // write-only; masked
	PasswordConfirmation   string   `json:"passwordConfirmation,omitempty"` // write-only
	LogLevel               string   `json:"logLevel"`
	LogSizeLimit           int      `json:"logSizeLimit"`
	InstanceName           string   `json:"instanceName"`
	LaunchBrowser          bool     `json:"launchBrowser"`
	Branch                 string   `json:"branch"`
	EnvOverrides           []string `json:"envOverrides"`              // read-only
	RestartRequired        *bool    `json:"restartRequired,omitempty"` // PUT response only
	// WebhookToken is the credential of the webhook URLs (read-only; see
	// POST /config/host/webhooktoken).
	WebhookToken string `json:"webhookToken"`
	// CurrentPassword (write-only) confirms a change of the username, password, authentication
	// method or requirement, or API key made from a signed-in browser (see needsCurrentPassword).
	CurrentPassword string `json:"currentPassword,omitempty"`
	// KeepAPIKey (write-only) keeps the API key when the password changes; by default a password
	// change also replaces the API key, so a key read by whoever knew the old password stops working.
	KeepAPIKey bool `json:"keepApiKey,omitempty"`
}

// hostConfigOf returns the HostConfig resource. The API key is masked unless revealKey (see
// canSeeAPIKey): only a caller that already holds it or has just proven the password sees it.
func (s *Server) hostConfigOf(c config.Config, u *models.User, revealKey bool) hostConfig {
	key := c.ApiKey
	if !revealKey && key != "" {
		key = maskedSecret
	}
	h := hostConfig{
		BindAddress:            c.BindAddress,
		Port:                   c.Port,
		URLBase:                c.UrlBase,
		EnableSsl:              c.EnableSsl,
		SslPort:                c.SslPort,
		SslCertPath:            c.SslCertPath,
		SslKeyPath:             c.SslKeyPath,
		APIKey:                 key,
		AuthenticationMethod:   c.AuthenticationMethod,
		AuthenticationRequired: c.AuthenticationRequired,
		LogLevel:               c.LogLevel,
		LogSizeLimit:           c.LogSizeLimit,
		InstanceName:           c.InstanceName,
		LaunchBrowser:          c.LaunchBrowser,
		Branch:                 c.Branch,
		EnvOverrides:           s.d.Config.EnvOverrides(),
	}
	if u != nil {
		h.Username, h.Password = u.Username, maskedSecret
	}
	if s.d.Auth != nil {
		h.WebhookToken = s.d.Auth.WebhookToken()
	}
	return h
}

// currentUser returns the account, or nil when none exists.
func (s *Server) currentUser(ctx context.Context) (*models.User, error) {
	if s.d.Auth == nil {
		return nil, nil
	}
	u, err := s.d.Auth.User(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

func (s *Server) handleHostConfig(w http.ResponseWriter, r *http.Request) {
	u, err := s.currentUser(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.hostConfigOf(s.d.Config.Get(), u, s.canSeeAPIKey(r, u)))
}

// passwordProtected reports whether the account's password guards the master credentials: a
// Forms account exists. (With None or External, or before setup, there is no password to ask for.)
func (s *Server) passwordProtected(u *models.User) bool {
	return u != nil && s.d.Auth != nil && s.d.Auth.Method() == config.AuthForms
}

// canSeeAPIKey reports whether a response may carry the API key without a password check: the
// caller authenticated with it, or no password protects it — except while first-run setup is
// pending, when a caller without the key (the local-address bypass) could create the account with
// it (setupPendingForCaller).
func (s *Server) canSeeAPIKey(r *http.Request, u *models.User) bool {
	if auth.FromContext(r.Context()).Via == auth.ViaAPIKey {
		return true
	}
	return !s.passwordProtected(u) && !s.setupPendingForCaller(r)
}

// setupPendingForCaller reports whether first-run setup is pending (Forms, no account) and the
// caller did not authenticate with the API key. Such a caller is the local-address bypass of
// DisabledForLocalAddresses (GAP-10): it may use the API like any local client, but it must not
// create the account, see or replace the API key, or download a backup — only POST
// /api/v1/auth/setup with the setup code from the log creates the account, so a remote client
// that appears local (Docker's userland proxy, NAT loopback) cannot claim the instance.
func (s *Server) setupPendingForCaller(r *http.Request) bool {
	return s.d.Auth != nil && auth.FromContext(r.Context()).Via != auth.ViaAPIKey && s.d.Auth.SetupRequired(r.Context())
}

// errSetupPending answers a credential request refused by setupPendingForCaller.
func errSetupPending() error {
	return errStatus(http.StatusForbidden, "First-run setup is pending: create the account on the setup page, with the setup code Dupearr printed in its log")
}

type revealAPIKeyRequest struct {
	CurrentPassword string `json:"currentPassword"`
}

type revealAPIKeyResponse struct {
	APIKey string `json:"apiKey"`
}

// handleRevealAPIKey returns the API key after checking the current password (when a Forms account
// exists; throttled like the login). Browser sessions never receive the key otherwise
// (initialize.json and GET /config/host mask it), so a stolen cookie or an unattended tab is not
// enough to obtain the master credential.
func (s *Server) handleRevealAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCurrentPasswordBody(w, r); !ok {
		return
	}
	s.log.Info("API key revealed", "via", auth.FromContext(r.Context()).Via)
	s.audit(r, audit.KindAPIKeyRevealed, "API key revealed")
	s.writeJSON(w, http.StatusOK, revealAPIKeyResponse{APIKey: s.d.Config.Get().ApiKey})
}

// checkCurrentPasswordBody reads an optional {"currentPassword"} body and, when a password
// protects the master credentials (passwordProtected), checks it. It writes the error response
// itself and reports whether to continue.
func (s *Server) checkCurrentPasswordBody(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return nil, false
	}
	if s.setupPendingForCaller(r) {
		s.writeErr(w, r, errSetupPending())
		return nil, false
	}
	u, err := s.currentUser(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return nil, false
	}
	var req revealAPIKeyRequest
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		if err := decodeJSON(r, &req); err != nil {
			s.writeErr(w, r, err)
			return nil, false
		}
	}
	if !s.passwordProtected(u) {
		return u, true
	}
	if req.CurrentPassword == "" {
		s.writeErr(w, r, errValidation(invalid("currentPassword", "Enter your current password")))
		return nil, false
	}
	if err := s.d.Auth.VerifyPassword(r, req.CurrentPassword); err != nil {
		s.writeCredentialError(w, r, err)
		return nil, false
	}
	return u, true
}

// handleHostConfigUpdate saves config.xml settings and the account. Fields absent from the body
// keep their values; the password changes only when a new value (not the mask) is sent, and must
// then match passwordConfirmation. Env-overridden fields cannot be changed. restartRequired is
// true when the listener (bind address, ports, SSL) or the URL base changed.
//
// Credential changes made from a signed-in browser need the current password (currentPassword,
// see needsCurrentPassword). A password change revokes every session and — unless keepApiKey is
// true or the key is set by the environment — replaces the API key; so does an API key change.
// The caller's own session is re-issued.
func (s *Server) handleHostConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	ctx := r.Context()
	keep := s.d.Auth.CurrentSession(r) // before a change revokes it
	old := s.d.Config.Get()
	u, err := s.currentUser(ctx)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := s.hostConfigOf(old, u, true)
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}

	env := make(map[string]bool)
	for _, k := range s.d.Config.EnvOverrides() {
		env[k] = true
	}
	if in.APIKey == maskedSecret {
		in.APIKey = "" // the mask keeps the key (applyHostConfig: an empty key is not a change)
	}
	next := applyHostConfig(old, in, env)
	username := strings.TrimSpace(in.Username)
	passwordChanged := in.Password != "" && in.Password != maskedSecret
	usernameChanged := u != nil && username != u.Username
	credentialChange := passwordChanged || usernameChanged || next.ApiKey != old.ApiKey ||
		next.AuthenticationMethod != old.AuthenticationMethod || next.AuthenticationRequired != old.AuthenticationRequired
	needPassword := credentialChange && s.needsCurrentPassword(r, u)
	// A password change replaces the API key too (unless asked not to, or the key is env-set).
	rotateKey := passwordChanged && u != nil && !in.KeepAPIKey && next.ApiKey == old.ApiKey && !env["apiKey"]

	if credentialChange && s.setupPendingForCaller(r) {
		s.writeErr(w, r, errSetupPending())
		return
	}
	var errs []config.ValidationError
	if needPassword && in.CurrentPassword == "" {
		errs = append(errs, invalid("currentPassword", "Enter your current password to change the username, password, API key or authentication settings"))
	}
	if next.AuthenticationMethod == config.AuthNone && old.AuthenticationMethod != config.AuthNone {
		errs = append(errs, invalid("authenticationMethod", "None can only be set in config.xml or with DUPEARR__AUTH__METHOD"))
	}
	if next.AuthenticationMethod == config.AuthForms && u == nil && !passwordChanged {
		errs = append(errs, invalid("password", "Password is required"))
	}
	if next.AuthenticationMethod == config.AuthForms || passwordChanged || usernameChanged {
		// Credentials are stored: the username must be valid, and so must a new password.
		pw := in.Password
		if !passwordChanged {
			pw = "unchanged" // only the username is checked
		}
		for _, e := range auth.ValidateCredentials(username, pw) {
			errs = appendUnique(errs, e)
		}
	}
	if passwordChanged && in.Password != in.PasswordConfirmation {
		errs = append(errs, invalid("passwordConfirmation", "Must match Password"))
	}
	errs = append(errs, config.Validate(next)...)
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if needPassword {
		if err := s.d.Auth.VerifyPassword(r, in.CurrentPassword); err != nil {
			s.writeCredentialError(w, r, err)
			return
		}
	}

	if passwordChanged || usernameChanged {
		pw := ""
		if passwordChanged {
			pw = in.Password
		}
		if u, err = s.d.Auth.UpdateUser(ctx, username, pw); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	saved, err := s.d.Config.Update(func(c *config.Config) {
		*c = applyHostConfig(*c, in, env)
		if rotateKey {
			c.ApiKey = config.GenerateAPIKey()
		}
	})
	if err != nil {
		s.writeErr(w, r, validationFromConfig(err))
		return
	}
	if saved.ApiKey != old.ApiKey {
		s.log.Info("API key replaced", "reason", map[bool]string{true: "password changed", false: "API key changed"}[rotateKey])
		if !passwordChanged { // a password change has revoked every session already
			if err := s.d.Auth.RevokeSessions(ctx); err != nil {
				s.log.Warn("Could not revoke sessions after the API key change", "error", err)
			}
		}
	}
	if passwordChanged || saved.ApiKey != old.ApiKey {
		if err := s.d.Auth.KeepSession(w, r, keep); err != nil {
			s.log.Warn("Could not renew the session after a credential change", "error", err)
		}
	}
	s.auditHostChanges(r, old, saved, usernameChanged, passwordChanged)
	restart := old.BindAddress != saved.BindAddress || old.Port != saved.Port || old.UrlBase != saved.UrlBase ||
		old.EnableSsl != saved.EnableSsl || old.SslPort != saved.SslPort ||
		old.SslCertPath != saved.SslCertPath || old.SslKeyPath != saved.SslKeyPath
	s.log.Info("Host settings saved", "restartRequired", restart)
	out := s.hostConfigOf(saved, u, needPassword || s.canSeeAPIKey(r, u))
	out.RestartRequired = &restart
	s.publish(events.NameSettings, events.ActionUpdated, empty)
	s.writeJSON(w, http.StatusAccepted, out)
}

// needsCurrentPassword reports whether a credential change must be confirmed with the current
// password: whenever a Forms account exists, however the request was authenticated (a session
// cookie, the local-address bypass, or the API key — scripts can send currentPassword too). A
// brief foothold (a stolen session, a device on the LAN under DisabledForLocalAddresses, a leaked
// API key) must not be enough to lock the owner out, switch the authentication method or take
// over the master credential. (None can never be set over the API.)
func (s *Server) needsCurrentPassword(_ *http.Request, u *models.User) bool {
	return s.passwordProtected(u)
}

// writeCredentialError answers a failed current-password check.
func (s *Server) writeCredentialError(w http.ResponseWriter, r *http.Request, err error) {
	var te *auth.ThrottledError
	switch {
	case errors.As(err, &te):
		secs := int((te.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeMessage(w, http.StatusTooManyRequests, te.Error())
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeMessage(w, http.StatusServiceUnavailable, "Too many password checks at once; try again in a moment")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.writeErr(w, r, errValidation(invalid("currentPassword", "The current password is incorrect")))
	default:
		s.writeErr(w, r, err)
	}
}

func appendUnique(errs []config.ValidationError, e config.ValidationError) []config.ValidationError {
	if slices.Contains(errs, e) {
		return errs
	}
	return append(errs, e)
}

// applyHostConfig returns c with the resource's config.xml fields applied, except env-overridden
// ones (keyed by HostConfig JSON name). An empty apiKey keeps the current key.
func applyHostConfig(c config.Config, h hostConfig, env map[string]bool) config.Config {
	set := func(name string, apply func()) {
		if !env[name] {
			apply()
		}
	}
	set("bindAddress", func() { c.BindAddress = h.BindAddress })
	set("port", func() { c.Port = h.Port })
	set("urlBase", func() { c.UrlBase = h.URLBase })
	set("enableSsl", func() { c.EnableSsl = h.EnableSsl })
	set("sslPort", func() { c.SslPort = h.SslPort })
	set("sslCertPath", func() { c.SslCertPath = h.SslCertPath })
	set("sslKeyPath", func() { c.SslKeyPath = h.SslKeyPath })
	if strings.TrimSpace(h.APIKey) != "" {
		set("apiKey", func() { c.ApiKey = h.APIKey })
	}
	set("authenticationMethod", func() { c.AuthenticationMethod = h.AuthenticationMethod })
	set("authenticationRequired", func() { c.AuthenticationRequired = h.AuthenticationRequired })
	set("logLevel", func() { c.LogLevel = h.LogLevel })
	set("logSizeLimit", func() { c.LogSizeLimit = h.LogSizeLimit })
	set("instanceName", func() { c.InstanceName = h.InstanceName })
	set("launchBrowser", func() { c.LaunchBrowser = h.LaunchBrowser })
	set("branch", func() { c.Branch = h.Branch })
	return config.Normalize(c)
}

// handleRegenerateAPIKey replaces the API key with a new random one and revokes every session;
// the caller's own session is re-issued. When a Forms account exists the current password is
// required ({"currentPassword"}): the response carries the new key.
func (s *Server) handleRegenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	if slices.Contains(s.d.Config.EnvOverrides(), "apiKey") {
		s.writeErr(w, r, errConflict("The API key is set by DUPEARR__AUTH__APIKEY and cannot be regenerated here"))
		return
	}
	if _, ok := s.checkCurrentPasswordBody(w, r); !ok {
		return
	}
	var keep *auth.Session
	if s.d.Auth != nil {
		keep = s.d.Auth.CurrentSession(r)
	}
	saved, err := s.d.Config.Update(func(c *config.Config) { c.ApiKey = config.GenerateAPIKey() })
	if err != nil {
		s.writeErr(w, r, validationFromConfig(err))
		return
	}
	s.log.Info("API key regenerated")
	s.audit(r, audit.KindAPIKeyChanged, "API key regenerated")
	if s.d.Auth != nil {
		if err := s.d.Auth.RevokeSessions(r.Context()); err != nil {
			s.log.Warn("Could not revoke sessions after regenerating the API key", "error", err)
		}
		if err := s.d.Auth.KeepSession(w, r, keep); err != nil {
			s.log.Warn("Could not renew the session after regenerating the API key", "error", err)
		}
	}
	u, err := s.currentUser(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.publish(events.NameSettings, events.ActionUpdated, empty)
	s.writeJSON(w, http.StatusOK, s.hostConfigOf(saved, u, true))
}

// handleRegenerateWebhookToken replaces the webhook token (webhook URLs holding the old one stop
// working) and answers the HostConfig.
func (s *Server) handleRegenerateWebhookToken(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	if _, err := s.d.Auth.RegenerateWebhookToken(r.Context()); err != nil {
		s.writeErr(w, r, err)
		return
	}
	u, err := s.currentUser(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.publish(events.NameSettings, events.ActionUpdated, empty)
	s.writeJSON(w, http.StatusOK, s.hostConfigOf(s.d.Config.Get(), u, s.canSeeAPIKey(r, u)))
}

// ---------------------------------------------------------------------------
// /api/v1/config/settings
// ---------------------------------------------------------------------------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Store.Settings().Get(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

// handleSettingsUpdate validates and saves the operational settings, then re-evaluates open
// groups in the background (decisions depend on them), queues a health check and publishes a
// "settings" event.
//
// The body is decoded on top of the stored settings: fields absent from the body (or null for a
// number, boolean or string) keep their stored values and are never zeroed, so an older or partial
// client cannot silently turn dry run off or reset a limit. The merged result is then validated as
// a whole (validateSettings, recycleBinPlacementProblems).
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// encoding/json decodes an array into the existing backing array: never write into a slice
	// the store might share.
	st.DeletionMethods = slices.Clone(st.DeletionMethods)
	before := st
	before.DeletionMethods = slices.Clone(st.DeletionMethods)
	if err := decodeJSON(r, &st); err != nil {
		s.writeErr(w, r, err)
		return
	}
	st = normalizeSettings(st)
	errs := validateSettings(st)
	if !slices.ContainsFunc(errs, func(e config.ValidationError) bool { return e.PropertyName == "recycleBinPath" }) {
		more, err := s.recycleBinPlacementProblems(ctx, st.RecycleBinPath)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		errs = append(errs, more...)
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Settings().Save(ctx, st); err != nil {
		s.writeErr(w, r, err)
		return
	}
	saved, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Settings saved", "dryRun", saved.DryRun, "mode", saved.Mode)
	s.auditRemovalSettings(r, before, saved)
	s.publish(events.NameSettings, events.ActionUpdated, empty)
	s.reevaluateAllAsync("settings changed")
	s.checkHealthLater(ctx, "settings changed")
	if warnings := s.settingsWarnings(ctx, saved); len(warnings) > 0 {
		w.Header().Set("X-Dupearr-Warning", sanitizeHeader(strings.Join(warnings, ". ")))
	}
	s.writeJSON(w, http.StatusAccepted, saved)
}

// checkHealthLater queues a CheckHealth command so System → Health never shows results that a
// configuration change made stale. A check that is already running may have read the configuration
// before this change, so it does not count (EnqueueFresh); an identical queued check still absorbs
// the request.
func (s *Server) checkHealthLater(ctx context.Context, reason string) {
	if s.d.Commands == nil {
		return
	}
	if _, err := s.d.Commands.EnqueueFresh(context.WithoutCancel(ctx), models.CmdCheckHealth, struct{}{}, models.TriggerManual); err != nil {
		s.log.Debug("Could not queue a health check", "reason", reason, "error", err)
	}
}

func normalizeSettings(st models.Settings) models.Settings {
	st.Mode = strings.ToLower(strings.TrimSpace(st.Mode))
	methods := make([]string, 0, len(st.DeletionMethods))
	for _, m := range st.DeletionMethods {
		methods = append(methods, strings.ToLower(strings.TrimSpace(m)))
	}
	st.DeletionMethods = methods
	if p := strings.TrimSpace(st.RecycleBinPath); p != "" {
		st.RecycleBinPath = filepath.Clean(p)
	} else {
		st.RecycleBinPath = ""
	}
	return st
}

// minScanIntervalMinutes is the shortest scheduled full-scan interval: a full scan walks every
// enabled library (and every *arr), so shorter intervals only load the servers.
const minScanIntervalMinutes = 15

// validateSettings checks models.Settings (property names are its JSON names): mode manual|auto;
// deletionMethods a non-empty subset of arr/plex/filesystem without repeats; the circuit breakers
// maxDeletionsPerRun ≥ 1 and maxBytesPerRunGb ≥ 1 (they can never be switched off, D6);
// stableScansRequired ≥ 1; minAgeHours ≥ 0; scanIntervalMinutes 0 (off) or ≥ 15;
// recycleBinCleanupDays ≥ 0 (0 keeps recycled files); duration tolerances ≥ 0; maxGroupSize ≥ 2;
// recycleBinPath empty or an absolute path that is not a filesystem root (see also
// recycleBinPlacementProblems).
func validateSettings(st models.Settings) []config.ValidationError {
	var errs []config.ValidationError
	add := func(prop, format string, args ...any) { errs = append(errs, invalid(prop, format, args...)) }
	intRange := func(prop string, v, lo, hi int) {
		if v < lo || v > hi {
			add(prop, "Must be between %d and %d", lo, hi)
		}
	}

	if st.Mode != models.ModeManual && st.Mode != models.ModeAuto {
		add("mode", "Must be manual or auto")
	}
	switch {
	case len(st.DeletionMethods) == 0:
		add("deletionMethods", "At least one deletion method is required")
	default:
		seen := map[string]bool{}
		for _, m := range st.DeletionMethods {
			switch m {
			case models.MethodArr, models.MethodPlex, models.MethodFilesystem:
			default:
				add("deletionMethods", "Unknown deletion method '%s' (use arr, plex, filesystem)", truncate(m, 32))
				continue
			}
			if seen[m] {
				add("deletionMethods", "Deletion method '%s' is listed twice", m)
			}
			seen[m] = true
		}
	}
	if st.ScanIntervalMinutes != 0 && (st.ScanIntervalMinutes < minScanIntervalMinutes || st.ScanIntervalMinutes > 525600) {
		add("scanIntervalMinutes", "Must be 0 (disabled) or between %d and 525600", minScanIntervalMinutes)
	}
	intRange("minAgeHours", st.MinAgeHours, 0, 87600)
	intRange("maxDeletionsPerRun", st.MaxDeletionsPerRun, 1, 10000)
	if p := st.RecycleBinPath; p != "" {
		switch {
		case !filepath.IsAbs(p):
			add("recycleBinPath", "Must be an absolute path")
		case strings.IndexFunc(p, unicode.IsControl) >= 0:
			add("recycleBinPath", "Must not contain control characters")
		case filepath.Dir(p) == p:
			add("recycleBinPath", "Must not be a filesystem root")
		}
	}
	intRange("recycleBinCleanupDays", st.RecycleBinCleanupDays, 0, 3650)
	intRange("maxGroupSize", st.MaxGroupSize, 2, 100)
	intRange("stableScansRequired", st.StableScansRequired, 1, 100)
	intRange("maxBytesPerRunGb", st.MaxBytesPerRunGB, 1, 1000000)
	if math.IsNaN(st.DurationTolerancePercent) || st.DurationTolerancePercent < 0 || st.DurationTolerancePercent > 100 {
		add("durationTolerancePercent", "Must be between 0 and 100")
	}
	intRange("durationToleranceMinutes", st.DurationToleranceMinutes, 0, 600)
	intRange("historyRetentionDays", st.HistoryRetentionDays, 0, 36500)
	intRange("backupIntervalDays", st.BackupIntervalDays, 0, 365)
	intRange("backupRetentionDays", st.BackupRetentionDays, 0, 3650)
	if st.AllowDiscRemoval && st.RecycleBinPath == "" {
		// docs/DECISIONS.md D9: a full disc is only ever moved to the recycle bin, never deleted.
		add("allowDiscRemoval", "Removing full discs needs a recycle bin: a disc is only ever moved there, never deleted. Set the recycle bin first")
	}
	return errs
}

// settingsWarnings returns the problems of saved settings that do not block saving (sent in
// X-Dupearr-Warning): full-disc detection without a local path to any enabled movie library, and
// disc removal without the filesystem method (the only one that removes a disc).
func (s *Server) settingsWarnings(ctx context.Context, st models.Settings) []string {
	var out []string
	if st.DetectDiscs {
		if mapped, known := s.movieFoldersMapped(ctx); known && !mapped {
			out = append(out, "Full-disc detection looks into the local copies of your movie folders, but no path mapping covers an enabled movie library: add one under Path Mappings")
		}
	}
	if st.AllowDiscRemoval && !slices.Contains(st.DeletionMethods, models.MethodFilesystem) {
		out = append(out, "Full discs are only removed by the filesystem method, which is not enabled: disc removals will be refused")
	}
	return out
}

// movieFoldersMapped reports whether a path mapping of its server covers a folder of an enabled
// movie library; known is false when there is no such library (or the store cannot be read).
func (s *Server) movieFoldersMapped(ctx context.Context) (mapped, known bool) {
	libs, err := s.d.Store.Libraries().List(ctx)
	if err != nil {
		return false, false
	}
	mappings, err := s.d.Store.PathMappings().List(ctx)
	if err != nil {
		return false, false
	}
	mapper := pathmap.New(mappings)
	for _, l := range libs {
		if !l.Enabled || !strings.EqualFold(strings.TrimSpace(l.Type), "movie") {
			continue
		}
		for _, loc := range l.Locations {
			if strings.TrimSpace(loc) == "" {
				continue
			}
			known = true
			if _, ok := mapper.ToLocal(models.PathSourceServer, l.ServerID, loc); ok {
				return true, true
			}
		}
	}
	return false, known
}

// ---------------------------------------------------------------------------
// Background work and events
// ---------------------------------------------------------------------------

// publish sends an event on the bus (no-op without a bus).
func (s *Server) publish(name, action string, resource any) {
	if s.d.Bus != nil {
		s.d.Bus.Publish(events.Event{Name: name, Action: action, Resource: resource})
	}
}

// reevaluateAllAsync re-evaluates every open group in the background. Requests arriving while a
// run is in progress are coalesced into one follow-up run.
func (s *Server) reevaluateAllAsync(reason string) {
	if s.scanner == nil {
		return
	}
	s.bgMu.Lock()
	if s.bgClosed {
		s.bgMu.Unlock()
		s.log.Debug("Not re-evaluating open duplicate groups: shutting down", "reason", reason)
		return
	}
	if s.bgRunning {
		s.bgPending = true
		s.bgMu.Unlock()
		return
	}
	s.bgRunning = true
	s.bgWG.Add(1)
	s.bgMu.Unlock()

	go func() {
		defer s.bgWG.Done()
		for {
			ctx, cancel := context.WithTimeout(s.bgCtx, reevaluateTimeout)
			err := s.scanner.ReevaluateAll(ctx)
			cancel()
			switch {
			case err != nil && s.bgCtx.Err() != nil:
				s.log.Debug("Re-evaluation of open duplicate groups stopped by shutdown", "reason", reason)
			case err != nil:
				s.log.Warn("Re-evaluating open duplicate groups failed", "reason", reason, "error", err)
			default:
				s.log.Debug("Re-evaluated open duplicate groups", "reason", reason)
			}
			s.bgMu.Lock()
			if !s.bgPending || s.bgClosed {
				s.bgRunning, s.bgPending = false, false
				s.bgMu.Unlock()
				return
			}
			s.bgPending = false
			s.bgMu.Unlock()
		}
	}()
}

// waitBackground blocks until background work has finished (tests).
func (s *Server) waitBackground() { s.bgWG.Wait() }
