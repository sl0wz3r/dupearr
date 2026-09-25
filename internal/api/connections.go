package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	// upstreamTimeout bounds connection tests and other calls to Plex/plex.tv/*arrs.
	upstreamTimeout = 30 * time.Second
	// syncTimeout bounds a synchronous library sync.
	syncTimeout = 2 * time.Minute

	maxNameLength       = 100
	maxPathLength       = 4096
	maxScopeGroupLength = 64
	mediaCoverCache     = "private, max-age=86400"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// forceSave reports whether ?forceSave=true skips the connection test.
func forceSave(r *http.Request) bool { return truthy(r.URL.Query().Get("forceSave")) }

// validateName checks a display name.
func validateName(prop, name string) []config.ValidationError {
	switch {
	case name == "":
		return []config.ValidationError{invalid(prop, "Name is required")}
	case utf8.RuneCountInString(name) > maxNameLength:
		return []config.ValidationError{invalid(prop, "Name must be at most %d characters", maxNameLength)}
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return []config.ValidationError{invalid(prop, "Name must not contain control characters")}
	}
	return nil
}

// normalizeBaseURL trims a connection URL (adding http:// when no scheme is given) and checks it
// is an http(s) URL with a host and without credentials, query or fragment.
func normalizeBaseURL(prop, raw string) (string, []config.ValidationError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", []config.ValidationError{invalid(prop, "URL is required")}
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return raw, []config.ValidationError{invalid(prop, "Invalid URL")}
	case u.Scheme != "http" && u.Scheme != "https":
		return raw, []config.ValidationError{invalid(prop, "URL must start with http:// or https://")}
	case u.Host == "" || u.Hostname() == "":
		return raw, []config.ValidationError{invalid(prop, "URL must include a host")}
	case u.User != nil:
		return raw, []config.ValidationError{invalid(prop, "URL must not contain credentials")}
	case u.RawQuery != "" || u.Fragment != "":
		return raw, []config.ValidationError{invalid(prop, "URL must not contain a query or fragment")}
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return raw, []config.ValidationError{invalid(prop, "Invalid port")}
		}
	}
	return strings.TrimRight(raw, "/"), nil
}

// sameEndpoint reports whether two connection URLs address the same endpoint: scheme, host,
// port (default ports made explicit) and path (without a trailing slash). The path counts: behind
// a path-routing reverse proxy, https://proxy/tenant-a and https://proxy/tenant-b are different
// servers.
func sameEndpoint(a, b string) bool {
	ua, err1 := url.Parse(strings.TrimSpace(a))
	ub, err2 := url.Parse(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return normalizedEndpoint(a) == normalizedEndpoint(b)
}

// secretEndpoint is where a stored secret is sent: the connection URL and whether the TLS
// certificate is verified.
type secretEndpoint struct {
	url       string
	verifyTLS bool
}

// restoreSecret replaces a masked secret with the stored one. The stored secret is only reused
// for the same endpoint (sameEndpoint) with TLS verification no weaker than before: otherwise
// anyone able to edit or test a connection without knowing its secret (a leaked API key, a
// stolen session) could point it at their own server — a different host or path, or the same
// one through an interception proxy once verification is off — and have a test deliver the
// secret there.
func restoreSecret(prop string, value *string, next secretEndpoint, stored *string, prev secretEndpoint) *config.ValidationError {
	if *value != maskedSecret {
		return nil
	}
	if stored == nil || *stored == "" {
		e := invalid(prop, "Enter the %s", secretLabel(prop))
		return &e
	}
	if !sameEndpoint(next.url, prev.url) {
		e := invalid(prop, "Re-enter the %s: the URL changed", secretLabel(prop))
		return &e
	}
	if prev.verifyTLS && !next.verifyTLS && strings.HasPrefix(strings.ToLower(strings.TrimSpace(next.url)), "https:") {
		e := invalid(prop, "Re-enter the %s: certificate verification was turned off", secretLabel(prop))
		return &e
	}
	*value = *stored
	return nil
}

// serverSecret names a media server's credential in a restoreSecret error: a Jellyfin server's is
// an API key (the property stays "token").
func serverSecret(kind models.MediaServerKind, e *config.ValidationError) *config.ValidationError {
	if e != nil && kind == models.MediaServerJellyfin {
		e.ErrorMessage = strings.Replace(e.ErrorMessage, "the token", "the API key", 1)
	}
	return e
}

func secretLabel(prop string) string {
	if prop == "apiKey" {
		return "API key"
	}
	return prop
}

// upstreamError maps an error of a Plex/plex.tv/*arr call to a client-safe response: credentials,
// wrong application, redirects and bad input are 400, everything else (unreachable, 5xx) is 502.
//
// The message never carries the upstream's response body (see upstreamerr.Message): the URL of a
// connection test is free-form (any host, any path prefix), so echoing what the server answered
// would let an API client read internal services' responses through Dupearr (SEC-031). The body
// is not logged either, for the same reason (the log is readable through the API).
func upstreamError(app string, err error) error {
	if err == nil {
		return nil
	}
	msg := logging.Redact(upstreamerr.Message(err))
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return errStatus(http.StatusBadGateway, "%s did not respond in time", app)
	case errors.Is(err, plex.ErrUnauthorized), errors.Is(err, arr.ErrUnauthorized), errors.Is(err, jellyfin.ErrUnauthorized):
		return errBadRequest("%s rejected the credentials: %s", app, msg)
	case errors.Is(err, plex.ErrForbidden),
		errors.Is(err, plex.ErrInvalidArgument), errors.Is(err, arr.ErrInvalidArgument),
		errors.Is(err, arr.ErrWrongApp), errors.Is(err, arr.ErrRedirect), errors.Is(err, plex.ErrRedirect),
		errors.Is(err, jellyfin.ErrForbidden), errors.Is(err, jellyfin.ErrInvalidArgument), errors.Is(err, jellyfin.ErrWrongApp),
		errors.Is(err, jellyfin.ErrTooOld), errors.Is(err, jellyfin.ErrRedirect), errors.Is(err, jellyfin.ErrRefused):
		return errBadRequest("%s", msg)
	case errors.Is(err, plex.ErrNotFound), errors.Is(err, arr.ErrNotFound), errors.Is(err, jellyfin.ErrNotFound):
		return errBadRequest("%s: not found at this URL (%s)", app, msg)
	}
	return errStatus(http.StatusBadGateway, "Unable to connect to %s: %s", app, msg)
}

// ---------------------------------------------------------------------------
// Media servers
// ---------------------------------------------------------------------------

func maskServer(ms models.MediaServer) models.MediaServer {
	if ms.Token != "" {
		ms.Token = maskedSecret
	}
	return ms
}

// validateServer normalises and checks a media server (after the token was restored).
func validateServer(ms *models.MediaServer) []config.ValidationError {
	ms.Name = strings.TrimSpace(ms.Name)
	ms.Token = strings.TrimSpace(ms.Token)
	if ms.Kind == "" {
		ms.Kind = models.MediaServerPlex
	}
	errs := validateName("name", ms.Name)
	switch {
	case ms.Kind.Supported():
	case strings.EqualFold(string(ms.Kind), "emby"):
		errs = append(errs, invalid("kind", "Emby is not supported yet"))
	default:
		errs = append(errs, invalid("kind", "Only Plex and Jellyfin media servers are supported"))
	}
	u, uerrs := normalizeBaseURL("url", ms.URL)
	ms.URL = u
	errs = append(errs, uerrs...)
	// The credential is a Plex token or a Jellyfin API key (the "token" property either way).
	label, what := "Token", "the token"
	if ms.Kind == models.MediaServerJellyfin {
		label, what = "API key", "the API key"
	}
	switch {
	case ms.Token == "":
		errs = append(errs, invalid("token", "%s is required", label))
	case ms.Token == maskedSecret:
		errs = append(errs, invalid("token", "Enter %s", what))
	case strings.ContainsAny(ms.Token, " \t\r\n"):
		errs = append(errs, invalid("token", "%s must not contain whitespace", label))
	}
	// docs/DECISIONS.md D11: "" = may share storage with the other servers (paths are compared),
	// "separate" = another host or a friend's server (only its mapped folders are compared).
	ms.Storage = strings.ToLower(strings.TrimSpace(ms.Storage))
	if ms.Storage != "" && ms.Storage != models.StorageSeparate {
		errs = append(errs, invalid("storage", "Must be empty (same storage as the other servers) or %q", models.StorageSeparate))
	}
	return errs
}

type mediaServerTestResult struct {
	Version              string `json:"version"`
	MachineIdentifier    string `json:"machineIdentifier"`
	FriendlyName         string `json:"friendlyName"`
	MediaDeletionAllowed bool   `json:"mediaDeletionAllowed"`
	// Owned tells whether the token belongs to the server's owner (Plex only accepts deletions
	// made with the owner's token), per plex.tv; null when unknown (plex.tv unreachable, or the
	// token is not listed there for this server). Always null for Jellyfin.
	Owned *bool `json:"owned"`
	// Jellyfin (docs/DECISIONS.md D12): the product the server reports, whether the credential is
	// an API key or an administrator (a test only succeeds when it is), and why removals of files
	// it lists are disabled ("" when they are not).
	Product          string `json:"product,omitempty"`
	Administrator    bool   `json:"administrator,omitempty"`
	RemovalsDisabled string `json:"removalsDisabled,omitempty"`
}

// plexTVOwnerTimeout bounds the plex.tv ownership lookup of a connection test: plex.tv is often
// unreachable in LAN-only setups, and the answer is optional.
const plexTVOwnerTimeout = 5 * time.Second

// testServer runs the connection test of ms's kind (validateServer refuses every other kind before
// a test).
func (s *Server) testServer(ctx context.Context, ms models.MediaServer) (*mediaServerTestResult, error) {
	switch {
	case ms.Kind.IsPlex():
		return s.testPlex(ctx, ms)
	case ms.Kind == models.MediaServerJellyfin:
		return s.testJellyfin(ctx, ms)
	}
	return nil, errValidation(invalid("kind", "Only Plex and Jellyfin media servers are supported"))
}

// testJellyfin connects to a Jellyfin server (docs/DECISIONS.md D12, research §5.3.6). The key is
// only sent once the URL is known to be Jellyfin 12.1 or later: GET /System/Info/Public (no
// credential) must identify "Jellyfin Server" ≥ 12.1; GET /System/Info with the key must answer
// with the same server id; GET /Library/VirtualFolders must answer (only an API key or an
// administrator may, research S14: anything less cannot see every playback session); the removal
// gate says whether removals of its files are disabled (path substitutions).
func (s *Server) testJellyfin(ctx context.Context, ms models.MediaServer) (*mediaServerTestResult, error) {
	if s.d.JellyfinFactory == nil {
		return nil, errUnavailable("Jellyfin")
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	c := s.d.JellyfinFactory(ms)
	pub, err := c.PublicInfo(ctx)
	if err != nil {
		return nil, upstreamError("Jellyfin", err)
	}
	id, err := c.Identity(ctx)
	switch {
	case errors.Is(err, jellyfin.ErrUnauthorized):
		return nil, errBadRequest("Jellyfin rejected the API key")
	case err != nil:
		return nil, upstreamError("Jellyfin", err)
	case id.MachineIdentifier != pub.MachineIdentifier:
		return nil, errBadRequest("The server answers with another server id with the API key than without it (%s, %s); check the URL and any reverse proxy in front of Jellyfin",
			pub.MachineIdentifier, id.MachineIdentifier)
	}
	if _, err := c.Sections(ctx); err != nil {
		if errors.Is(err, jellyfin.ErrForbidden) {
			return nil, errBadRequest("This credential is not an API key or an administrator's, so Dupearr could not see every playback session. " +
				"Create an API key in Jellyfin → Dashboard → API Keys (or use an administrator's token)")
		}
		return nil, upstreamError("Jellyfin", err)
	}
	res := &mediaServerTestResult{
		Version:           id.Version,
		MachineIdentifier: id.MachineIdentifier,
		FriendlyName:      id.FriendlyName,
		Product:           jellyfin.ProductName,
		Administrator:     true,
	}
	switch why, err := c.RemovalProblem(ctx); {
	case err != nil:
		s.log.Debug("Could not read whether removals from a Jellyfin server are safe", "server", ms.Name, "error", err)
		res.RemovalsDisabled = "Dupearr could not read Jellyfin's configuration; removals stay disabled until it can"
	case why != "":
		res.RemovalsDisabled = why
	}
	return res, nil
}

// warnRemovalsDisabled sets X-Dupearr-Warning when a Jellyfin test found removals disabled.
func (s *Server) warnRemovalsDisabled(w http.ResponseWriter, ms models.MediaServer, res *mediaServerTestResult) {
	if res == nil || res.RemovalsDisabled == "" {
		return
	}
	s.log.Warn("Removals from this media server are disabled", "server", ms.Name, "reason", res.RemovalsDisabled)
	w.Header().Set("X-Dupearr-Warning", sanitizeHeader("Removals from this server are disabled: "+res.RemovalsDisabled))
}

// serverLabel names a media server's kind in API messages ("Plex", "Jellyfin").
func serverLabel(ms models.MediaServer) string { return ms.Kind.Label() }

// testPlex connects to a Plex server: identity plus the allowMediaDeletion setting, and — in
// parallel, best effort — whether plex.tv lists the token as the server owner's.
func (s *Server) testPlex(ctx context.Context, ms models.MediaServer) (*mediaServerTestResult, error) {
	if s.d.PlexFactory == nil {
		return nil, errUnavailable("Plex")
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()

	// Started before the server calls so a slow plex.tv adds no latency on top of them.
	type ownerAnswer struct {
		list []plex.Resource
		err  error
	}
	ownerCh := make(chan ownerAnswer, 1) // buffered: never blocks when abandoned
	octx, ocancel := context.WithTimeout(ctx, plexTVOwnerTimeout)
	defer ocancel()
	go func() {
		list, err := plex.Servers(octx, s.d.PlexOpts, ms.Token)
		ownerCh <- ownerAnswer{list, err}
	}()

	c := s.d.PlexFactory(ms)
	id, err := c.Identity(ctx)
	if err != nil {
		return nil, upstreamError("Plex", err)
	}
	if id.MachineIdentifier == "" {
		return nil, errBadRequest("The server did not report a machine identifier; is this a Plex Media Server?")
	}
	allowed, err := c.MediaDeletionAllowed(ctx)
	if err != nil {
		s.log.Debug("Could not read allowMediaDeletion", "server", ms.Name, "error", err)
		allowed = false
	}
	res := &mediaServerTestResult{
		Version:              id.Version,
		MachineIdentifier:    id.MachineIdentifier,
		FriendlyName:         id.FriendlyName,
		MediaDeletionAllowed: allowed,
	}
	select {
	case a := <-ownerCh:
		if a.err != nil {
			s.log.Debug("Could not ask plex.tv who owns the server", "server", ms.Name, "error", logging.Redact(a.err.Error()))
			break
		}
		for _, r := range a.list {
			if strings.EqualFold(strings.TrimSpace(r.ClientIdentifier), id.MachineIdentifier) {
				owned := r.Owned
				res.Owned = &owned
				break
			}
		}
	case <-octx.Done():
		s.log.Debug("plex.tv did not answer the ownership lookup in time", "server", ms.Name)
	}
	return res, nil
}

// warnNotOwner sets X-Dupearr-Warning when the connection test found that the token is not the
// server owner's (Plex refuses deletions made with it).
func (s *Server) warnNotOwner(w http.ResponseWriter, ms models.MediaServer, res *mediaServerTestResult) {
	if res == nil || res.Owned == nil || *res.Owned {
		return
	}
	s.log.Warn("The Plex token is not the server owner's: Plex refuses deletions made with it", "server", ms.Name)
	w.Header().Set("X-Dupearr-Warning", "This token belongs to a user the server is shared with, not to its owner: "+
		"Plex refuses deletions made with it, so deleting through Plex will not work. Sign in with the owner's account.")
}

func (s *Server) handleMediaServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.MediaServers().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]models.MediaServer, 0, len(list))
	for _, ms := range list {
		out = append(out, maskServer(ms))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMediaServer(w http.ResponseWriter, r *http.Request) {
	ms, err := s.loadServer(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, maskServer(*ms))
}

// loadServer loads the media server named by the {id} path value.
func (s *Server) loadServer(r *http.Request) (*models.MediaServer, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	ms, err := s.d.Store.MediaServers().Get(r.Context(), id)
	return ms, notFoundAs(err, "Media server")
}

// ensureUniqueServer refuses a second connection to the same server (it would be scanned and
// acted on twice). kind names it in the message ("This Plex server …", "This Jellyfin server …").
func (s *Server) ensureUniqueServer(ctx context.Context, kind models.MediaServerKind, machineID string, selfID int64) error {
	if machineID == "" {
		return nil
	}
	list, err := s.d.Store.MediaServers().List(ctx)
	if err != nil {
		return err
	}
	for _, other := range list {
		if other.ID != selfID && strings.EqualFold(other.MachineIdentifier, machineID) {
			return errConflict("This %s server is already configured as %q", kind.Label(), other.Name)
		}
	}
	return nil
}

// identityProbeTimeout bounds the identity read of a forced save (?forceSave=true is typically used
// while the server is unreachable, so the save must not wait for the full upstream timeout).
const identityProbeTimeout = 10 * time.Second

// probeIdentity reads the machine identifier of the server a forced save points at, best effort:
// "" when it cannot be reached (the save goes ahead; scans, syncs and tests fill it in later). A
// Plex server is asked with a Plex request carrying the credential as X-Plex-Token; a Jellyfin
// server only through its public identity (/System/Info/Public, no credential: the URL is not
// known to be Jellyfin yet). Any other kind is not asked.
func (s *Server) probeIdentity(ctx context.Context, ms models.MediaServer) (id, name string) {
	ctx, cancel := context.WithTimeout(ctx, identityProbeTimeout)
	defer cancel()
	var (
		ident *mediaserver.Identity
		err   error
	)
	switch {
	case ms.Kind.IsPlex() && s.d.PlexFactory != nil:
		ident, err = s.d.PlexFactory(ms).Identity(ctx)
	case ms.Kind == models.MediaServerJellyfin && s.d.JellyfinFactory != nil:
		ident, err = s.d.JellyfinFactory(ms).PublicInfo(ctx)
	default:
		return "", ""
	}
	if err != nil || ident == nil {
		s.log.Debug("Forced save: the server's identity could not be read", "server", ms.Name, "error", err)
		return "", ""
	}
	return strings.TrimSpace(ident.MachineIdentifier), ident.FriendlyName
}

// errOtherPlexServer is the 409 of a forced save whose URL answers as another server than the
// stored one: every stored rating key and media id belongs to the stored server. kind names it.
func errOtherPlexServer(kind models.MediaServerKind, name string) error {
	if name == "" {
		name = "unnamed"
	}
	return errConflict("This URL points to a different %s server (%s) than the one this media server was set up with; "+
		"add it as a new media server instead (saving without a test does not change that)", kind.Label(), name)
}

// adoptServerIdentity stores the identity a successful connection test of a saved media server
// found while it has none (it was force-saved while unreachable). The row is read again under the
// connection lock and only written while it still has no identity and the URL and token that were
// tested, and no other configured server has this identity. A stored identity is never replaced.
func (s *Server) adoptServerIdentity(ctx context.Context, tested models.MediaServer, mid string) {
	mid = strings.TrimSpace(mid)
	if tested.ID <= 0 || mid == "" {
		return
	}
	s.connMu.Lock()
	defer s.connMu.Unlock()
	cur, err := s.d.Store.MediaServers().Get(ctx, tested.ID)
	if err != nil || strings.TrimSpace(cur.MachineIdentifier) != "" || cur.URL != tested.URL || cur.Token != tested.Token {
		return
	}
	if err := s.ensureUniqueServer(ctx, cur.Kind, mid, cur.ID); err != nil {
		s.log.Warn("Not storing the media server's identity: another media server has it", "server", cur.Name, "error", err)
		return
	}
	cur.MachineIdentifier = mid
	if err := s.d.Store.MediaServers().Update(ctx, cur); err != nil {
		s.log.Warn("Could not store the media server's identity", "server", cur.Name, "error", err)
		return
	}
	s.log.Info("Stored the media server's identity", "server", cur.Name, "machineIdentifier", mid)
}

// handleMediaServerCreate adds a media server after a connection test. With ?forceSave=true the
// test is skipped, but a server that answers still has its identity read: it is stored, and a
// server already configured under another name is refused (409).
func (s *Server) handleMediaServerCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ms := models.MediaServer{Kind: models.MediaServerPlex, Enabled: true, VerifyTLS: true}
	if err := decodeJSON(r, &ms); err != nil {
		s.writeErr(w, r, err)
		return
	}
	ms.ID, ms.MachineIdentifier = 0, ""
	if errs := validateServer(&ms); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	var tested *mediaServerTestResult
	if !forceSave(r) {
		res, err := s.testServer(ctx, ms)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		tested = res
		ms.MachineIdentifier = res.MachineIdentifier
	} else {
		ms.MachineIdentifier, _ = s.probeIdentity(ctx, ms)
	}
	if err := s.ensureUniqueServer(ctx, ms.Kind, ms.MachineIdentifier, 0); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.MediaServers().Create(ctx, &ms); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Media server added", "id", ms.ID, "name", ms.Name)
	s.syncLibrariesLater(ctx, ms.ID)
	s.checkHealthLater(ctx, "media server added")
	s.warnNotOwner(w, ms, tested)
	s.warnRemovalsDisabled(w, ms, tested)
	s.writeJSON(w, http.StatusCreated, maskServer(ms))
}

// syncLibrariesLater queues a SyncLibraries command for one server.
func (s *Server) syncLibrariesLater(ctx context.Context, serverID int64) {
	body := struct {
		ServerID int64 `json:"serverId"`
	}{serverID}
	if _, err := s.enqueue(context.WithoutCancel(ctx), models.CmdSyncLibraries, body, models.TriggerManual); err != nil {
		s.log.Warn("Could not queue a library sync", "serverId", serverID, "error", err)
	}
}

func (s *Server) handleMediaServerUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stored, err := s.loadServer(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := *stored
	in.Token = maskedSecret
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in.ID, in.MachineIdentifier, in.CreatedAt = stored.ID, stored.MachineIdentifier, stored.CreatedAt
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	// Stored rating keys, version keys and groups belong to the stored kind: another kind is another
	// server (docs/DECISIONS.md D12).
	if in.Kind != stored.Kind && !(in.Kind.IsPlex() && stored.Kind.IsPlex()) {
		s.writeErr(w, r, errValidation(invalid("kind", "The kind of a media server cannot be changed; add a new media server instead")))
		return
	}
	if e := serverSecret(in.Kind, restoreSecret("token", &in.Token, secretEndpoint{in.URL, in.VerifyTLS}, &stored.Token, secretEndpoint{stored.URL, stored.VerifyTLS})); e != nil {
		s.writeErr(w, r, errValidation(*e))
		return
	}
	if errs := validateServer(&in); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	var tested *mediaServerTestResult
	if !forceSave(r) {
		res, err := s.testServer(ctx, in)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		tested = res
		if stored.MachineIdentifier != "" && !strings.EqualFold(res.MachineIdentifier, stored.MachineIdentifier) {
			s.writeErr(w, r, errValidation(invalid("url",
				"This URL points to a different %s server (%s); add it as a new media server instead", in.Kind.Label(), res.FriendlyName)))
			return
		}
		in.MachineIdentifier = res.MachineIdentifier
		if err := s.ensureUniqueServer(ctx, in.Kind, in.MachineIdentifier, in.ID); err != nil {
			s.writeErr(w, r, err)
			return
		}
	} else if mid, name := s.probeIdentity(ctx, in); mid != "" {
		// A forced save skips the test, not the identity: a reachable server that is not the stored
		// one is refused; a missing identity (force-saved while unreachable) is filled in.
		if stored.MachineIdentifier != "" && !strings.EqualFold(mid, stored.MachineIdentifier) {
			s.writeErr(w, r, errOtherPlexServer(in.Kind, name))
			return
		}
		if stored.MachineIdentifier == "" {
			if err := s.ensureUniqueServer(ctx, in.Kind, mid, in.ID); err != nil {
				s.writeErr(w, r, err)
				return
			}
			in.MachineIdentifier = mid
		}
	}
	s.connMu.Lock()
	if endpointChanged(stored.URL, in.URL) {
		// Stored rating keys / media ids came from the old URL (see endpoints.go).
		if err := s.endpointRepointed(ctx, endpointKindServer, stored.ID, in.Name); err != nil {
			s.connMu.Unlock()
			s.writeErr(w, r, err)
			return
		}
	}
	err = s.d.Store.MediaServers().Update(ctx, &in)
	s.connMu.Unlock()
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Media server"))
		return
	}
	saved, err := s.d.Store.MediaServers().Get(ctx, in.ID)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Media server"))
		return
	}
	s.log.Info("Media server updated", "id", saved.ID, "name", saved.Name)
	if saved.Enabled && (saved.URL != stored.URL || saved.Token != stored.Token || !stored.Enabled) {
		s.syncLibrariesLater(ctx, saved.ID)
	}
	s.checkHealthLater(ctx, "media server updated")
	s.warnNotOwner(w, *saved, tested)
	s.warnRemovalsDisabled(w, *saved, tested)
	s.writeJSON(w, http.StatusAccepted, maskServer(*saved))
}

func (s *Server) handleMediaServerDelete(w http.ResponseWriter, r *http.Request) {
	ms, err := s.loadServer(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.MediaServers().Delete(r.Context(), ms.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Media server"))
		return
	}
	s.log.Info("Media server deleted", "id", ms.ID, "name", ms.Name)
	s.checkHealthLater(r.Context(), "media server deleted")
	s.writeJSON(w, http.StatusOK, empty)
}

// handleMediaServerTest tests an (unsaved) media server; with an id, a masked token is taken from
// the stored server.
func (s *Server) handleMediaServerTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in := models.MediaServer{VerifyTLS: true}
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	var stored *models.MediaServer
	if in.ID > 0 {
		ms, err := s.d.Store.MediaServers().Get(ctx, in.ID)
		switch {
		case err == nil:
			stored = ms
		case in.Token == maskedSecret || !errors.Is(err, store.ErrNotFound):
			s.writeErr(w, r, notFoundAs(err, "Media server"))
			return
		}
	}
	// A body without a kind tests a saved server as its own kind (else Plex, as always).
	if in.Kind == "" && stored != nil {
		in.Kind = stored.Kind
	}
	if in.Kind == "" {
		in.Kind = models.MediaServerPlex
	}
	// A stored credential is only ever sent the way its own kind sends it.
	if in.Token == maskedSecret && stored != nil && in.Kind != stored.Kind && !(in.Kind.IsPlex() && stored.Kind.IsPlex()) {
		s.writeErr(w, r, errValidation(invalid("token", "Re-enter the credential: the kind of media server changed")))
		return
	}
	if in.Token == maskedSecret {
		var token *string
		var prev secretEndpoint
		if stored != nil {
			token, prev = &stored.Token, secretEndpoint{stored.URL, stored.VerifyTLS}
		}
		if e := serverSecret(in.Kind, restoreSecret("token", &in.Token, secretEndpoint{in.URL, in.VerifyTLS}, token, prev)); e != nil {
			s.writeErr(w, r, errValidation(*e))
			return
		}
	}
	if errs := validateServer(&in); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	res, err := s.testServer(ctx, in)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Testing a saved server that was force-saved while unreachable fills in its identity.
	if stored != nil && stored.MachineIdentifier == "" && in.URL == stored.URL && in.Token == stored.Token {
		s.adoptServerIdentity(ctx, *stored, res.MachineIdentifier)
	}
	s.writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// Libraries
// ---------------------------------------------------------------------------

func (s *Server) handleServerLibraries(w http.ResponseWriter, r *http.Request) {
	ms, err := s.loadServer(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	list, err := s.d.Store.Libraries().ListByServer(r.Context(), ms.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

// handleServerLibrariesSync re-reads the server's library sections now and returns them.
func (s *Server) handleServerLibrariesSync(w http.ResponseWriter, r *http.Request) {
	ms, err := s.loadServer(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if s.scanner == nil {
		s.writeErr(w, r, errUnavailable("The scanner"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()
	if err := s.scanner.SyncLibraries(ctx, ms.ID); err != nil {
		s.writeErr(w, r, upstreamError(serverLabel(*ms), err))
		return
	}
	list, err := s.d.Store.Libraries().ListByServer(r.Context(), ms.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.Libraries().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) loadLibrary(r *http.Request) (*models.Library, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	l, err := s.d.Store.Libraries().Get(r.Context(), id)
	return l, notFoundAs(err, "Library")
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	l, err := s.loadLibrary(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, l)
}

// handleLibraryUpdate edits a library: only enabled, profileId (null = default profile) and
// scopeGroup are taken from the body. When the library is enabled or disabled, or gets another
// profile or scope group, its open groups are re-evaluated before the response (a disabled
// library's groups go to review, which cancels their queued removals).
func (s *Server) handleLibraryUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stored, err := s.loadLibrary(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := *stored
	// Only enabled/profileId/scopeGroup are editable. Clone the slice: encoding/json decodes a
	// JSON array into the existing backing array, which in shares with stored (and next), so a
	// "locations" value in the body would otherwise silently rewrite the library's locations.
	in.Locations = slices.Clone(stored.Locations)
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	next := *stored
	next.Enabled = in.Enabled
	next.ProfileID = in.ProfileID
	next.ScopeGroup = strings.TrimSpace(in.ScopeGroup)

	var errs []config.ValidationError
	if next.ProfileID != nil {
		if *next.ProfileID <= 0 {
			errs = append(errs, invalid("profileId", "Invalid profile id"))
		} else if _, err := s.d.Store.Profiles().Get(ctx, *next.ProfileID); fsNotFound(err) {
			errs = append(errs, invalid("profileId", "Profile %d does not exist", *next.ProfileID))
		} else if err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	switch {
	case utf8.RuneCountInString(next.ScopeGroup) > maxScopeGroupLength:
		errs = append(errs, invalid("scopeGroup", "Must be at most %d characters", maxScopeGroupLength))
	case strings.IndexFunc(next.ScopeGroup, unicode.IsControl) >= 0:
		errs = append(errs, invalid("scopeGroup", "Must not contain control characters"))
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Libraries().Update(ctx, &next); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Library"))
		return
	}
	saved, err := s.d.Store.Libraries().Get(ctx, next.ID)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Library"))
		return
	}
	scopeChanged := !strings.EqualFold(strings.TrimSpace(stored.ScopeGroup), strings.TrimSpace(saved.ScopeGroup))
	if stored.Enabled != saved.Enabled || scopeChanged || !sameProfile(stored.ProfileID, saved.ProfileID) {
		s.log.Info("Library updated", "id", saved.ID, "title", saved.Title, "enabled", saved.Enabled, "scopeGroup", saved.ScopeGroup)
		s.reevaluateAffected(ctx, "library changed", usesLibrary(saved.ID))
	}
	switch {
	case stored.Enabled != saved.Enabled:
		s.checkHealthLater(ctx, "library enabled or disabled") // PathMappingCheck, RecycleBinCheck, WatchHistoryCheck
	case !sameProfile(stored.ProfileID, saved.ProfileID):
		s.checkHealthLater(ctx, "library profile changed") // WatchHistoryCheck
	}
	s.writeJSON(w, http.StatusAccepted, saved)
}

func sameProfile(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// handleMediaCover proxies a Plex poster (query path=/library/…, w, h) for the UI.
func (s *Server) handleMediaCover(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "serverId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	thumb := q.Get("path")
	if !strings.HasPrefix(thumb, "/library/") || strings.Contains(thumb, "..") {
		s.writeErr(w, r, errValidation(invalid("path", "Must be a Plex /library/ artwork path")))
		return
	}
	width, _ := strconv.Atoi(q.Get("w"))
	height, _ := strconv.Atoi(q.Get("h"))
	ms, err := s.d.Store.MediaServers().Get(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Media server"))
		return
	}
	if !ms.Kind.IsPlex() {
		// Plex artwork only: another kind's server is never sent a Plex request with its credential.
		s.writeErr(w, r, errNotFound("Image"))
		return
	}
	if s.d.PlexFactory == nil {
		s.writeErr(w, r, errUnavailable("Plex"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	body, contentType, err := s.d.PlexFactory(*ms).Photo(ctx, thumb, width, height)
	if err != nil {
		switch {
		case errors.Is(err, plex.ErrInvalidArgument):
			s.writeErr(w, r, errValidation(invalid("path", "Must be a Plex /library/ artwork path")))
		case errors.Is(err, plex.ErrNotFound):
			s.writeErr(w, r, errNotFound("Image"))
		default:
			s.log.Debug("Poster proxy failed", "serverId", id, "error", upstreamerr.Message(err))
			s.writeErr(w, r, upstreamError("Plex", err))
		}
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", mediaCoverCache)
	// The bytes come from Plex: if they are ever opened as a document (e.g. an SVG), they must not
	// run script in Dupearr's origin. (CSP of an image does not affect <img> rendering.)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, body); err != nil && r.Context().Err() == nil {
		s.log.Debug("Poster proxy: copy failed", "serverId", id, "error", err)
	}
}

// ---------------------------------------------------------------------------
// plex.tv sign-in
// ---------------------------------------------------------------------------

type plexPinResponse struct {
	ID      int64  `json:"id"`
	Code    string `json:"code"`
	AuthURL string `json:"authUrl"`
}

type plexPinStatus struct {
	Authenticated bool   `json:"authenticated"`
	AuthToken     string `json:"authToken"`
}

func (s *Server) handlePlexPinCreate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	pin, err := plex.CreatePin(ctx, s.d.PlexOpts)
	if err != nil {
		s.writeErr(w, r, upstreamError("plex.tv", err))
		return
	}
	s.writeJSON(w, http.StatusOK, plexPinResponse{ID: pin.ID, Code: pin.Code, AuthURL: plex.AuthURL(s.d.PlexOpts, pin.Code)})
}

func (s *Server) handlePlexPinCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	pin, err := plex.CheckPin(ctx, s.d.PlexOpts, id)
	if err != nil {
		if errors.Is(err, plex.ErrNotFound) {
			s.writeErr(w, r, errStatus(http.StatusNotFound, "The PIN has expired; start the sign-in again"))
			return
		}
		s.writeErr(w, r, upstreamError("plex.tv", err))
		return
	}
	s.writeJSON(w, http.StatusOK, plexPinStatus{Authenticated: pin.AuthToken != "", AuthToken: pin.AuthToken})
}

// handlePlexServers lists the servers a plex.tv account can reach (token in the X-Plex-Token
// header). The account token controls the whole Plex account, so it is refused in the query
// string, where reverse-proxy access logs and browser history would keep it.
func (s *Server) handlePlexServers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("token") {
		s.writeErr(w, r, errValidation(invalid("token", "Send the plex.tv token in the X-Plex-Token header, not in the URL")))
		return
	}
	token := strings.TrimSpace(r.Header.Get("X-Plex-Token"))
	if token == "" {
		s.writeErr(w, r, errValidation(invalid("token", "A plex.tv token is required")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	list, err := plex.Servers(ctx, s.d.PlexOpts, token)
	if err != nil {
		s.writeErr(w, r, upstreamError("plex.tv", err))
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

// ---------------------------------------------------------------------------
// *arr instances
// ---------------------------------------------------------------------------

func maskArr(a models.ArrInstance) models.ArrInstance {
	if a.APIKey != "" {
		a.APIKey = maskedSecret
	}
	return a
}

func validateArr(a *models.ArrInstance) []config.ValidationError {
	a.Name = strings.TrimSpace(a.Name)
	a.APIKey = strings.TrimSpace(a.APIKey)
	a.Kind = models.ArrKind(strings.ToLower(strings.TrimSpace(string(a.Kind))))
	errs := validateName("name", a.Name)
	if a.Kind != models.ArrRadarr && a.Kind != models.ArrSonarr {
		errs = append(errs, invalid("kind", "Must be radarr or sonarr"))
	}
	u, uerrs := normalizeBaseURL("url", a.URL)
	a.URL = u
	errs = append(errs, uerrs...)
	switch {
	case a.APIKey == "":
		errs = append(errs, invalid("apiKey", "API key is required"))
	case a.APIKey == maskedSecret:
		errs = append(errs, invalid("apiKey", "Enter the API key"))
	case strings.ContainsAny(a.APIKey, " \t\r\n"):
		errs = append(errs, invalid("apiKey", "API key must not contain whitespace"))
	}
	tags := make([]string, 0, len(a.Tags))
	for _, t := range a.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	a.Tags = tags
	return errs
}

type arrTestResult struct {
	AppName               string `json:"appName"`
	Version               string `json:"version"`
	InstanceName          string `json:"instanceName"`
	RecycleBin            string `json:"recycleBin"`
	RecycleBinCleanupDays int    `json:"recycleBinCleanupDays"`
}

// testArr checks the instance answers as the configured application and reads its recycle bin.
func (s *Server) testArr(ctx context.Context, a models.ArrInstance) (*arrTestResult, error) {
	if s.d.ArrFactory == nil {
		return nil, errUnavailable("The *arr client")
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	app := "Radarr"
	if a.Kind == models.ArrSonarr {
		app = "Sonarr"
	}
	c := s.d.ArrFactory(a)
	st, err := c.Status(ctx)
	if err != nil {
		return nil, upstreamError(app, err)
	}
	mm, err := c.MediaManagement(ctx)
	if err != nil {
		return nil, upstreamError(app, err)
	}
	return &arrTestResult{
		AppName:               st.AppName,
		Version:               st.Version,
		InstanceName:          st.InstanceName,
		RecycleBin:            mm.RecycleBin,
		RecycleBinCleanupDays: mm.RecycleBinCleanupDays,
	}, nil
}

func (s *Server) handleArrs(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.ArrInstances().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]models.ArrInstance, 0, len(list))
	for _, a := range list {
		out = append(out, maskArr(a))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadArr(r *http.Request) (*models.ArrInstance, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	a, err := s.d.Store.ArrInstances().Get(r.Context(), id)
	return a, notFoundAs(err, "Application")
}

func (s *Server) handleArr(w http.ResponseWriter, r *http.Request) {
	a, err := s.loadArr(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, maskArr(*a))
}

// ensureUniqueArr refuses a second connection to the same *arr endpoint: both entries would track
// (and act on) the same files.
func (s *Server) ensureUniqueArr(ctx context.Context, a models.ArrInstance) error {
	list, err := s.d.Store.ArrInstances().List(ctx)
	if err != nil {
		return err
	}
	for _, other := range list {
		if other.ID != a.ID && !endpointChanged(other.URL, a.URL) {
			return errConflict("This application is already configured as %q", other.Name)
		}
	}
	return nil
}

// arrLinks validates and completes the media server links of an instance (docs/DECISIONS.md D11):
// every id must name a media server; with exactly one enabled Plex server and no links, the
// instance is linked to it and the links count as confirmed (it can only feed that server; adding
// or enabling a second server makes them unconfirmed again, see the media server repository).
// Otherwise an instance without links is never stored as confirmed: "feeds none of the servers"
// would make the versions it tracks count as untracked wherever mapped paths cannot decide.
func (s *Server) arrLinks(ctx context.Context, a *models.ArrInstance) ([]config.ValidationError, error) {
	servers, err := s.d.Store.MediaServers().List(ctx)
	if err != nil {
		return nil, err
	}
	known := map[int64]bool{}
	var enabled []int64
	for _, ms := range servers {
		known[ms.ID] = true
		if ms.Enabled && ms.Kind.Supported() {
			enabled = append(enabled, ms.ID)
		}
	}
	var errs []config.ValidationError
	ids := make([]int64, 0, len(a.ServerIDs))
	for _, id := range a.ServerIDs {
		switch {
		case !known[id]:
			errs = append(errs, invalid("serverIds", "Media server %d does not exist", id))
		case !slices.Contains(ids, id):
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	switch {
	case len(ids) == 0 && len(enabled) == 1:
		ids, a.LinksConfirmed = []int64{enabled[0]}, true
	case len(ids) == 0:
		a.LinksConfirmed = false
	}
	a.ServerIDs = ids
	return errs, nil
}

func (s *Server) handleArrCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := models.ArrInstance{Enabled: true, VerifyTLS: true}
	if err := decodeJSON(r, &a); err != nil {
		s.writeErr(w, r, err)
		return
	}
	a.ID = 0
	errs := validateArr(&a)
	linkErrs, err := s.arrLinks(ctx, &a)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if errs = append(errs, linkErrs...); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.ensureUniqueArr(ctx, a); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !forceSave(r) {
		if _, err := s.testArr(ctx, a); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.d.Store.ArrInstances().Create(ctx, &a); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Application added", "id", a.ID, "name", a.Name, "kind", a.Kind)
	s.checkHealthLater(ctx, "application added")
	s.writeJSON(w, http.StatusCreated, maskArr(a))
}

func (s *Server) handleArrUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stored, err := s.loadArr(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := *stored
	in.APIKey = maskedSecret
	// Fields absent from the body keep their stored values; the slices are cloned so decoding
	// never writes into the stored instance's backing arrays.
	in.Tags = slices.Clone(stored.Tags)
	in.ServerIDs = slices.Clone(stored.ServerIDs)
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in.ID, in.CreatedAt = stored.ID, stored.CreatedAt
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	if e := restoreSecret("apiKey", &in.APIKey, secretEndpoint{in.URL, in.VerifyTLS}, &stored.APIKey, secretEndpoint{stored.URL, stored.VerifyTLS}); e != nil {
		s.writeErr(w, r, errValidation(*e))
		return
	}
	errs := validateArr(&in)
	if in.Kind != stored.Kind {
		errs = append(errs, invalid("kind", "The application type cannot be changed; add a new application instead"))
	}
	linkErrs, err := s.arrLinks(ctx, &in)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if errs = append(errs, linkErrs...); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.ensureUniqueArr(ctx, in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !forceSave(r) {
		if _, err := s.testArr(ctx, in); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	s.connMu.Lock()
	if endpointChanged(stored.URL, in.URL) {
		// The test cannot tell two instances of the same app apart, and stored moviefile /
		// episodefile ids are only meaningful on the instance they came from (see endpoints.go).
		if err := s.endpointRepointed(ctx, endpointKindArr, stored.ID, in.Name); err != nil {
			s.connMu.Unlock()
			s.writeErr(w, r, err)
			return
		}
	}
	err = s.d.Store.ArrInstances().Update(ctx, &in)
	s.connMu.Unlock()
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Application"))
		return
	}
	saved, err := s.d.Store.ArrInstances().Get(ctx, in.ID)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Application"))
		return
	}
	s.log.Info("Application updated", "id", saved.ID, "name", saved.Name)
	s.checkHealthLater(ctx, "application updated")
	s.writeJSON(w, http.StatusAccepted, maskArr(*saved))
}

func (s *Server) handleArrDelete(w http.ResponseWriter, r *http.Request) {
	a, err := s.loadArr(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.ArrInstances().Delete(r.Context(), a.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Application"))
		return
	}
	s.log.Info("Application deleted", "id", a.ID, "name", a.Name)
	s.checkHealthLater(r.Context(), "application deleted")
	s.writeJSON(w, http.StatusOK, empty)
}

// handleArrTest tests an (unsaved) *arr instance; with an id, a masked API key is taken from the
// stored instance. Certificates are verified unless verifyTls is false (like create).
func (s *Server) handleArrTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	in := models.ArrInstance{VerifyTLS: true}
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if u, errs := normalizeBaseURL("url", in.URL); len(errs) == 0 {
		in.URL = u
	}
	if in.APIKey == maskedSecret {
		var key *string
		var prev secretEndpoint
		if in.ID > 0 {
			stored, err := s.d.Store.ArrInstances().Get(ctx, in.ID)
			if err != nil {
				s.writeErr(w, r, notFoundAs(err, "Application"))
				return
			}
			key, prev = &stored.APIKey, secretEndpoint{stored.URL, stored.VerifyTLS}
		}
		if e := restoreSecret("apiKey", &in.APIKey, secretEndpoint{in.URL, in.VerifyTLS}, key, prev); e != nil {
			s.writeErr(w, r, errValidation(*e))
			return
		}
	}
	if errs := validateArr(&in); len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	res, err := s.testArr(ctx, in)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// Path mappings
// ---------------------------------------------------------------------------

// validatePathMapping normalises and checks a mapping; the source must exist.
func (s *Server) validatePathMapping(ctx context.Context, m *models.PathMapping) ([]config.ValidationError, error) {
	m.SourceType = strings.ToLower(strings.TrimSpace(m.SourceType))
	m.RemotePath = strings.TrimSpace(m.RemotePath)
	m.LocalPath = strings.TrimSpace(m.LocalPath)
	var errs []config.ValidationError
	switch m.SourceType {
	case models.PathSourceServer, models.PathSourceArr:
		if m.SourceID <= 0 {
			errs = append(errs, invalid("sourceId", "Select a media server or application"))
			break
		}
		var err error
		if m.SourceType == models.PathSourceServer {
			_, err = s.d.Store.MediaServers().Get(ctx, m.SourceID)
		} else {
			_, err = s.d.Store.ArrInstances().Get(ctx, m.SourceID)
		}
		switch {
		case fsNotFound(err):
			errs = append(errs, invalid("sourceId", "The selected %s does not exist", map[string]string{
				models.PathSourceServer: "media server", models.PathSourceArr: "application"}[m.SourceType]))
		case err != nil:
			return nil, err
		}
	default:
		errs = append(errs, invalid("sourceType", "Must be server or arr"))
	}
	errs = append(errs, pathProblems("remotePath", m.RemotePath, false)...)
	local := pathProblems("localPath", m.LocalPath, true)
	if len(local) == 0 {
		placement, err := s.localPathPlacementProblems(ctx, m.LocalPath)
		if err != nil {
			return nil, err
		}
		local = placement
	}
	errs = append(errs, local...)
	if len(errs) == 0 {
		m.LocalPath = filepath.Clean(m.LocalPath)
	}
	return errs, nil
}

// localPathPlacementProblems checks a mapping's local folder (absolute, not a root) against
// Dupearr's own folders (r2-data-files#2): Dupearr acts on files inside mapped folders (removal,
// restore, the recycle bin), so a mapped folder must neither be, contain nor lie inside the data
// folder (config.xml, the database, backups, logs), nor be or lie inside the recycle bin (whose
// cleanup permanently deletes what is inside). Both the configured and the symlink-resolved forms
// are compared.
func (s *Server) localPathPlacementProblems(ctx context.Context, local string) ([]config.ValidationError, error) {
	var errs []config.ValidationError
	forms := pathForms(local)
	if s.d.Config != nil {
		if dataDir := s.d.Config.DataDir(); dataDir != "" {
			data := pathForms(dataDir)
			if anyWithin(forms, data) || anyWithin(data, forms) {
				errs = append(errs, invalid("localPath", "Must not be, contain or lie inside Dupearr's data folder %s", filepath.Clean(dataDir)))
			}
		}
	}
	st, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		return nil, err
	}
	if bin := strings.TrimSpace(st.RecycleBinPath); bin != "" && filepath.IsAbs(bin) && anyWithin(forms, pathForms(bin)) {
		errs = append(errs, invalid("localPath", "Must not be or lie inside the recycle bin %s: its cleanup permanently deletes what is inside", filepath.Clean(bin)))
	}
	return errs, nil
}

// systemFolders are folders of the operating system that never hold media: a mapping to one of
// them (or below) is almost certainly a mistake (warned, not refused).
var systemFolders = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc",
	"/root", "/run", "/sbin", "/sys", "/usr", "/var/lib", "/var/log", "/var/run", "/System", "/Library",
	"/private/etc", "/private/var/db", `C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`}

// warnSystemLocalPath sets X-Dupearr-Warning when the local folder (or the folder it resolves to)
// is or lies inside an operating-system folder.
func warnSystemLocalPath(w http.ResponseWriter, local string) {
	for _, f := range pathForms(local) {
		for _, sys := range systemFolders {
			if !filepath.IsAbs(sys) {
				continue
			}
			if _, ok := relBelow(f, sys); ok {
				w.Header().Set("X-Dupearr-Warning", fmt.Sprintf("Local path %s is a system folder (%s); map your media folders instead", sanitizeHeader(local), sys))
				return
			}
		}
	}
}

// pathProblems checks a mapping path. Local paths must be absolute (on Dupearr's host) and not a
// filesystem root: Dupearr only ever deletes inside mapped local roots (docs/DECISIONS.md D6), so a
// root would allow deletions anywhere. Remote paths are as the media server / *arr sees them
// (POSIX, Windows drive or UNC): they must be absolute (the mapper ignores other mappings) and not
// "/" or a bare UNC host, which would map every path of that source; a Windows drive root such as
// "D:\" (a dedicated media drive) is allowed.
func pathProblems(prop, p string, local bool) []config.ValidationError {
	switch {
	case p == "":
		return []config.ValidationError{invalid(prop, "Path is required")}
	case len(p) > maxPathLength:
		return []config.ValidationError{invalid(prop, "Path must be at most %d characters", maxPathLength)}
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return []config.ValidationError{invalid(prop, "Path must not contain control characters")}
	}
	if local {
		clean := filepath.Clean(p)
		switch {
		case !filepath.IsAbs(clean):
			return []config.ValidationError{invalid(prop, "Must be an absolute path")}
		case filepath.Dir(clean) == clean:
			return []config.ValidationError{invalid(prop, "Must not be a filesystem root")}
		}
		return nil
	}
	n := pathmap.Normalize(p)
	switch {
	case n == "/":
		return []config.ValidationError{invalid(prop, "Must not be the filesystem root: map the media folders (e.g. /data/media)")}
	case strings.HasPrefix(n, "//") && !strings.Contains(n[2:], "/"):
		return []config.ValidationError{invalid(prop, "Must include the share name (\\\\server\\share)")}
	case !isRemoteAbs(n):
		return []config.ValidationError{invalid(prop, "Must be an absolute path as the media server or application sees it (e.g. /data/movies or D:\\Movies)")}
	}
	return nil
}

// isRemoteAbs reports whether a normalised remote path is absolute: POSIX ("/…"), UNC ("//…")
// or a Windows drive path ("c:/…").
func isRemoteAbs(n string) bool {
	if strings.HasPrefix(n, "/") {
		return true
	}
	return len(n) >= 3 && n[1] == ':' && n[2] == '/' && ((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= 'A' && n[0] <= 'Z'))
}

// warnMissingLocalPath sets X-Dupearr-Warning when the local path is not accessible (saving is
// still allowed: the share may be mounted later).
func warnMissingLocalPath(w http.ResponseWriter, local string) {
	fi, err := os.Stat(local)
	switch {
	case err != nil:
		w.Header().Set("X-Dupearr-Warning", fmt.Sprintf("Local path %s does not exist or is not accessible by Dupearr", sanitizeHeader(local)))
	case !fi.IsDir():
		w.Header().Set("X-Dupearr-Warning", fmt.Sprintf("Local path %s is not a folder", sanitizeHeader(local)))
	}
}

// sanitizeHeader drops characters that are not allowed in a header value.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func (s *Server) handlePathMappings(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.PathMappings().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) loadPathMapping(r *http.Request) (*models.PathMapping, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.d.Store.PathMappings().Get(r.Context(), id)
	return m, notFoundAs(err, "Path mapping")
}

func (s *Server) handlePathMapping(w http.ResponseWriter, r *http.Request) {
	m, err := s.loadPathMapping(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, m)
}

func (s *Server) handlePathMappingCreate(w http.ResponseWriter, r *http.Request) {
	var m models.PathMapping
	if err := decodeJSON(r, &m); err != nil {
		s.writeErr(w, r, err)
		return
	}
	m.ID = 0
	errs, err := s.validatePathMapping(r.Context(), &m)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.PathMappings().Create(r.Context(), &m); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.checkHealthLater(r.Context(), "path mapping added")
	warnMissingLocalPath(w, m.LocalPath)
	warnSystemLocalPath(w, m.LocalPath)
	s.writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handlePathMappingUpdate(w http.ResponseWriter, r *http.Request) {
	stored, err := s.loadPathMapping(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	in := *stored
	if err := decodeJSON(r, &in); err != nil {
		s.writeErr(w, r, err)
		return
	}
	in.ID = stored.ID
	errs, err := s.validatePathMapping(r.Context(), &in)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.PathMappings().Update(r.Context(), &in); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Path mapping"))
		return
	}
	s.checkHealthLater(r.Context(), "path mapping updated")
	warnMissingLocalPath(w, in.LocalPath)
	warnSystemLocalPath(w, in.LocalPath)
	s.writeJSON(w, http.StatusAccepted, in)
}

func (s *Server) handlePathMappingDelete(w http.ResponseWriter, r *http.Request) {
	m, err := s.loadPathMapping(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Store.PathMappings().Delete(r.Context(), m.ID); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Path mapping"))
		return
	}
	s.checkHealthLater(r.Context(), "path mapping deleted")
	s.writeJSON(w, http.StatusOK, empty)
}
