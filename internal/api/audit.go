package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Security events (internal/audit, docs/SECURITY.md GAP-12): every credential, authentication,
// removal-setting and connection change made through the API is recorded in the durable history
// with how the request was authenticated and the client address, whatever the log level.

// audit records a security event for the request r.
func (s *Server) audit(r *http.Request, kind, title string, attrs ...any) {
	attrs = append([]any{"via", auth.FromContext(r.Context()).Via}, attrs...)
	if s.d.Auth != nil {
		s.d.Auth.Audit(r.Context(), r, kind, title, attrs...)
		return
	}
	audit.Record(r.Context(), s.d.Store, s.log, kind, title, attrs...)
}

// audited wraps a state-changing handler: when it answers 2xx, a security event of kind is
// recorded naming the request (method and path; never the body, which may hold secrets).
func (s *Server) audited(kind, title string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := wrap(w)
		h(sw, r)
		if sw.status >= 200 && sw.status < 300 {
			s.audit(r, kind, title, "request", r.Method+" "+truncate(r.URL.Path, 120))
		}
	}
}

// logLevelRank orders the log levels from the most to the least verbose.
var logLevelRank = map[string]int{"trace": 0, "debug": 1, "info": 2, "warn": 3, "error": 4}

// auditHostChanges records the security-relevant differences of a config.xml save (values of
// secrets are never recorded, only that they changed).
func (s *Server) auditHostChanges(r *http.Request, old, saved config.Config, usernameChanged, passwordChanged bool) {
	var changed []string
	add := func(name string, a, b any) {
		if a != b {
			changed = append(changed, fmt.Sprintf("%s: %v → %v", name, a, b))
		}
	}
	add("authenticationMethod", old.AuthenticationMethod, saved.AuthenticationMethod)
	add("authenticationRequired", old.AuthenticationRequired, saved.AuthenticationRequired)
	add("bindAddress", old.BindAddress, saved.BindAddress)
	add("port", old.Port, saved.Port)
	add("urlBase", old.UrlBase, saved.UrlBase)
	add("enableSsl", old.EnableSsl, saved.EnableSsl)
	add("sslPort", old.SslPort, saved.SslPort)
	add("logLevel", old.LogLevel, saved.LogLevel)
	if usernameChanged {
		changed = append(changed, "username")
	}
	if passwordChanged {
		changed = append(changed, "password")
	}
	if old.ApiKey != saved.ApiKey {
		changed = append(changed, "apiKey")
		s.audit(r, audit.KindAPIKeyChanged, "API key replaced", "passwordChanged", passwordChanged)
	}
	if lo, ln := logLevelRank[old.LogLevel], logLevelRank[saved.LogLevel]; ln > lo {
		// Less is logged from now on: the history keeps what the log may no longer show.
		s.audit(r, audit.KindLogLevelLowered, "Log level lowered to "+saved.LogLevel, "from", old.LogLevel, "to", saved.LogLevel)
	}
	if len(changed) > 0 {
		s.audit(r, audit.KindHostSettings, "Host settings changed", "changes", strings.Join(changed, "; "))
	}
}

// auditRemovalSettings records the differences of the settings that decide which files are
// removed, how, and what the history keeps.
func (s *Server) auditRemovalSettings(r *http.Request, old, saved models.Settings) {
	var changed []string
	add := func(name string, a, b any) {
		if fmt.Sprint(a) != fmt.Sprint(b) {
			changed = append(changed, fmt.Sprintf("%s: %v → %v", name, a, b))
		}
	}
	add("dryRun", old.DryRun, saved.DryRun)
	add("mode", old.Mode, saved.Mode)
	if !slices.Equal(old.DeletionMethods, saved.DeletionMethods) {
		add("deletionMethods", strings.Join(old.DeletionMethods, ","), strings.Join(saved.DeletionMethods, ","))
	}
	add("recycleBinPath", old.RecycleBinPath, saved.RecycleBinPath)
	add("recycleBinCleanupDays", old.RecycleBinCleanupDays, saved.RecycleBinCleanupDays)
	add("minAgeHours", old.MinAgeHours, saved.MinAgeHours)
	add("maxDeletionsPerRun", old.MaxDeletionsPerRun, saved.MaxDeletionsPerRun)
	add("maxBytesPerRunGb", old.MaxBytesPerRunGB, saved.MaxBytesPerRunGB)
	add("stableScansRequired", old.StableScansRequired, saved.StableScansRequired)
	add("detectDiscs", old.DetectDiscs, saved.DetectDiscs)
	add("allowDiscRemoval", old.AllowDiscRemoval, saved.AllowDiscRemoval)
	add("keepPlayableCopy", old.KeepPlayableCopy, saved.KeepPlayableCopy)
	add("historyRetentionDays", old.HistoryRetentionDays, saved.HistoryRetentionDays)
	add("unmonitorWhenKeeperElsewhere", old.UnmonitorWhenKeeperElsewhere, saved.UnmonitorWhenKeeperElsewhere)
	add("addExclusionWhenKeeperElsewhere", old.AddExclusionWhenKeeperElsewhere, saved.AddExclusionWhenKeeperElsewhere)
	if len(changed) > 0 {
		s.audit(r, audit.KindRemovalSettings, "Removal settings changed", "changes", strings.Join(changed, "; "))
	}
}
