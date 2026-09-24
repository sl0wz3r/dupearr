package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/version"
)

// ---------------------------------------------------------------------------
// GET /ping
// ---------------------------------------------------------------------------

const (
	pingCacheTTL = 5 * time.Second
	pingTimeout  = 3 * time.Second
)

// pingCache remembers the last database check for pingCacheTTL (Servarr caches its /ping DB read
// for 5 s), so a tight healthcheck loop does not hammer SQLite.
type pingCache struct {
	mu      sync.Mutex
	checked time.Time
	ok      bool
}

// handlePing answers {"status":"OK"}, or 500 {"status":"Error"} when the database is unreachable.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	body := map[string]string{"status": "OK"}
	if !s.pingOK(r.Context()) {
		status, body["status"] = http.StatusInternalServerError, "Error"
	}
	s.writeJSON(w, status, body)
}

func (s *Server) pingOK(ctx context.Context) bool {
	if s.d.Store == nil {
		return true
	}
	s.ping.mu.Lock()
	defer s.ping.mu.Unlock()
	now := s.now()
	if !s.ping.checked.IsZero() && now.Sub(s.ping.checked) < pingCacheTTL && now.After(s.ping.checked) {
		return s.ping.ok
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	err := s.d.Store.Ping(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("Ping: database check failed", "error", err)
	}
	s.ping.checked, s.ping.ok = now, err == nil
	return s.ping.ok
}

// ---------------------------------------------------------------------------
// GET /initialize.json
// ---------------------------------------------------------------------------

type initializeResponse struct {
	APIRoot              string `json:"apiRoot"`
	URLBase              string `json:"urlBase"`
	Version              string `json:"version"`
	InstanceName         string `json:"instanceName"`
	AuthenticationMethod string `json:"authenticationMethod"`
	Commit               string `json:"commit"`
}

// handleInitialize returns the SPA bootstrap document. The auth middleware only lets
// authenticated callers (cookie, API key, bypass) through. It never carries the master API key:
// the web UI authenticates with its session cookie (or the local-address bypass, External or
// None), so a stolen session, a device on the LAN under DisabledForLocalAddresses, or a script in
// another app sharing the host name (a path-based reverse proxy) cannot turn a brief foothold into
// the master credential, which outlives logouts and "log out all sessions". The key is shown in
// Settings → General after re-entering the password (POST /config/host/apikey/reveal).
func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	c := s.d.Config.Get()
	s.writeJSON(w, http.StatusOK, initializeResponse{
		APIRoot:              c.UrlBase + "/api/v1",
		URLBase:              c.UrlBase,
		Version:              version.Version,
		InstanceName:         c.InstanceName,
		AuthenticationMethod: s.authMethod(),
		Commit:               version.Commit,
	})
}

func (s *Server) authMethod() string {
	if s.d.Auth != nil {
		return s.d.Auth.Method()
	}
	return s.d.Config.Get().AuthenticationMethod
}

// ---------------------------------------------------------------------------
// /api/v1/auth/status, /api/v1/auth/setup
// ---------------------------------------------------------------------------

type authStatusResponse struct {
	SetupRequired        bool   `json:"setupRequired"`
	AuthenticationMethod string `json:"authenticationMethod"`
	Authenticated        bool   `json:"authenticated"`
	// EnvForced lists, while setup is required, the authentication settings environment
	// variables force (e.g. {"authenticationRequired":"DisabledForLocalAddresses"}): the setup page
	// shows them read-only instead of offering a choice the save would discard (GAP-07). It is
	// omitted once an account exists, so this public route does not advertise the requirement.
	EnvForced map[string]string `json:"envForced,omitempty"`
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	res := authStatusResponse{
		SetupRequired:        s.d.Auth.SetupRequired(r.Context()),
		AuthenticationMethod: s.d.Auth.Method(),
		Authenticated:        s.d.Auth.Authenticated(r),
	}
	if res.SetupRequired {
		if forced := s.d.Auth.EnvForcedAuth(); len(forced) > 0 {
			res.EnvForced = forced
		}
	}
	s.writeJSON(w, http.StatusOK, res)
}

type setupRequest struct {
	AuthenticationMethod   string `json:"authenticationMethod"`
	AuthenticationRequired string `json:"authenticationRequired"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	PasswordConfirmation   string `json:"passwordConfirmation"`
	// SetupCode is the one-time code Dupearr prints in its log at startup while setup is pending.
	SetupCode string `json:"setupCode"`
}

// maxAuthBody caps the body of the unauthenticated login and setup requests (a username, a
// password and a few flags), so that neither can be used to send megabytes before authenticating.
const maxAuthBody = 8 << 10

// handleAuthSetup creates the first account. It is unauthenticated, so it only works from local
// addresses that name this server by a local IP or a private host name (auth.LocalAccessAllowed:
// a DNS-rebinding page must not be able to claim the account), only while setup is required, and
// only with the setup code from the log (auth.CheckSetupCode): a remote client can appear with a
// local address (Docker's userland proxy, NAT loopback, a reverse proxy without forwarding
// headers) and choose any Host header, so neither check alone proves the admin is at the keyboard.
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	// Setup state first: once an account exists nothing is logged (GET /api/v1/auth/status tells
	// anyone whether setup is pending anyway), so this public route cannot be used to flood the log.
	if !s.d.Auth.SetupRequired(r.Context()) {
		s.writeErr(w, r, errConflict("Authentication is already set up"))
		return
	}
	if !s.d.Auth.LocalAccessAllowed(r) {
		s.d.Auth.LogRefusedSetup(r, "the client or the host name it used is not local")
		s.writeErr(w, r, errStatus(http.StatusForbidden, "First-run setup is only available from the local network, opening Dupearr by its local IP address, localhost or its local host name; use `dupearr reset-auth` or config.xml otherwise"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	var req setupRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !s.d.Auth.CheckSetupCode(req.SetupCode) {
		s.d.Auth.LogRefusedSetup(r, "missing or wrong setup code")
		s.writeErr(w, r, errStatus(http.StatusForbidden, "Enter the setup code that Dupearr printed in its log at startup (for Docker: docker logs <container>)"))
		return
	}
	if req.Password != req.PasswordConfirmation {
		s.writeErr(w, r, errValidation(invalid("passwordConfirmation", "Must match Password")))
		return
	}
	err := s.d.Auth.Setup(r.Context(), req.AuthenticationMethod, req.AuthenticationRequired, req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrSetupNotRequired):
		s.writeErr(w, r, errConflict("Authentication is already set up"))
	case errors.Is(err, auth.ErrLockedDown):
		s.writeErr(w, r, errStatus(http.StatusServiceUnavailable, "%s", auth.ErrLockedDown.Error()))
	case err != nil:
		s.writeErr(w, r, err)
	default:
		s.writeJSON(w, http.StatusOK, empty)
	}
}

// ---------------------------------------------------------------------------
// POST /login, POST /logout
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username   string          `json:"username"`
	Password   string          `json:"password"`
	RememberMe json.RawMessage `json:"rememberMe"`
}

// truthy interprets a checkbox value: true, "on", "true", "1", "yes".
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "on", "1", "yes":
		return true
	}
	return false
}

// handleLogin accepts JSON {"username","password","rememberMe"} (answering 200 {} / 401 / 429) or
// an HTML form post (username, password, rememberMe=on, optional returnUrl), which is answered with
// a 303 redirect like Servarr: to returnUrl (local paths only) or the UI on success, to
// /login?loginFailed=true otherwise.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	form := mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data"

	var username, password, returnURL string
	var remember bool
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	if form {
		if err := r.ParseForm(); err != nil {
			s.writeErr(w, r, errBadRequest("Invalid form body"))
			return
		}
		username, password = r.PostForm.Get("username"), r.PostForm.Get("password")
		remember = truthy(r.PostForm.Get("rememberMe"))
		returnURL = r.PostForm.Get("returnUrl")
		if returnURL == "" {
			returnURL = r.URL.Query().Get("returnUrl")
		}
	} else {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			s.writeErr(w, r, err)
			return
		}
		username, password = req.Username, req.Password
		if raw := strings.TrimSpace(string(req.RememberMe)); raw != "" && raw != "null" {
			var b bool
			var str string
			if json.Unmarshal(req.RememberMe, &b) == nil {
				remember = b
			} else if json.Unmarshal(req.RememberMe, &str) == nil {
				remember = truthy(str)
			}
		}
	}

	err := s.d.Auth.Login(w, r, username, password, remember)
	var te *auth.ThrottledError
	base := s.urlBase()
	switch {
	case err == nil:
		if form {
			http.Redirect(w, r, s.safeReturnURL(returnURL), http.StatusSeeOther)
			return
		}
		s.writeJSON(w, http.StatusOK, empty)
	case errors.As(err, &te):
		secs := int((te.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		if form {
			http.Redirect(w, r, base+"/login?loginFailed=true&throttled=true", http.StatusSeeOther)
			return
		}
		writeMessage(w, http.StatusTooManyRequests, te.Error())
	case errors.Is(err, auth.ErrBusy), errors.Is(err, auth.ErrLockedDown):
		w.Header().Set("Retry-After", "1")
		if form {
			http.Redirect(w, r, base+"/login?loginFailed=true&throttled=true", http.StatusSeeOther)
			return
		}
		msg := "Too many sign-in attempts at once; try again in a moment"
		if errors.Is(err, auth.ErrLockedDown) {
			msg = auth.ErrLockedDown.Error()
		}
		writeMessage(w, http.StatusServiceUnavailable, msg)
	case errors.Is(err, auth.ErrInvalidCredentials):
		if form {
			target := base + "/login?loginFailed=true"
			if returnURL != "" {
				target += "&returnUrl=" + url.QueryEscape(returnURL)
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		writeMessage(w, http.StatusUnauthorized, "Invalid username or password")
	default:
		s.writeErr(w, r, err)
	}
}

// safeReturnURL returns a same-site path for a post-login redirect: returnUrl when it is a local
// absolute path (not "//host" or "/\host"), prefixed with UrlBase when needed; else the UI root.
func (s *Server) safeReturnURL(raw string) string {
	base := s.urlBase()
	home := base + "/"
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/\\") {
		return home
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return home
	}
	if strings.Contains(raw, "\\") || strings.ContainsAny(raw, "\r\n\t") {
		return home
	}
	if base != "" && raw != base && !strings.HasPrefix(raw, base+"/") {
		raw = base + raw
	}
	return raw
}

// handleLogout signs the caller out: the session cookie is cleared and its token revoked. It
// is POST-only and must be same-origin (a browser always sends Origin on a POST), so another site
// cannot sign the user out.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOriginPost(r) {
		writeMessage(w, http.StatusForbidden, "Cross-site request rejected")
		return
	}
	if s.d.Auth != nil {
		s.d.Auth.Logout(w, r)
	}
	s.writeJSON(w, http.StatusOK, empty)
}

// handleLogoutGet refuses GET /logout: a link or an <img> on any site could otherwise sign the
// user out. The web UI signs out with POST.
func (s *Server) handleLogoutGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	writeMessage(w, http.StatusMethodNotAllowed, "Sign out with POST /logout")
}

// sameOriginPost reports whether a request's Origin (else Referer) names the host it was sent to
// (Host or the first X-Forwarded-Host); a request without either header (not a browser) passes.
func sameOriginPost(r *http.Request) bool {
	src := strings.TrimSpace(r.Header.Get("Origin"))
	if src == "" {
		src = strings.TrimSpace(r.Header.Get("Referer"))
		if src == "" {
			return true
		}
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	hosts := []string{r.Host}
	if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" {
		hosts = append(hosts, fh)
	}
	for _, h := range hosts {
		if strings.EqualFold(hostWithDefaultPort(u.Host, u.Scheme), hostWithDefaultPort(h, u.Scheme)) {
			return true
		}
	}
	return false
}

// hostWithDefaultPort returns host:port, adding the scheme's default port when none is given.
func hostWithDefaultPort(host, scheme string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), port)
}

// handleRevokeSessions signs out every session and device cookie ("log out all sessions"); the
// caller's own session, if the request carries one, is re-issued so they stay signed in here.
func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	if s.d.Auth == nil {
		s.writeErr(w, r, errUnavailable("Authentication"))
		return
	}
	keep := s.d.Auth.CurrentSession(r)
	if err := s.d.Auth.RevokeSessions(r.Context()); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.d.Auth.KeepSession(w, r, keep); err != nil {
		s.log.Warn("Could not renew the caller's session after revoking sessions", "error", err)
	}
	s.writeJSON(w, http.StatusOK, empty)
}

// validationFromConfig converts an error from config.Manager.Update into a response error.
func validationFromConfig(err error) error {
	var ve config.ValidationErrors
	if errors.As(err, &ve) {
		return errValidation(ve...)
	}
	return err
}
