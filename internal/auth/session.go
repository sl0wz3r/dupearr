package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Session tokens are stateless HMAC tokens "<userID>|<expiry>|<remember>|<sessionID>|<signature>",
// but they can be revoked:
//
//   - the signature covers the user's password hash, so a password change (or an account
//     re-created by reset-auth) invalidates every session;
//   - it also covers the session generation (settings value "auth.sessionGeneration"), which
//     RevokeSessions increments: "log out all sessions", API-key regeneration and password changes;
//   - /logout adds the session id to a revocation list (settings value "auth.revokedSessions"), so
//     a copied cookie stops working when its owner signs out. The id is random per login and kept
//     when a token is renewed (sliding expiration), so this also covers copies taken before a
//     renewal, and renewals made from a copy. Entries are dropped once every token of the session
//     would have expired; when the list is full, every session is revoked instead.
//
// A renewal is only as valid as the token it renews (GAP-06): the signing key and generation live
// in one immutable signingState that RevokeSessions replaces as a unit, verifySession remembers
// the state and the password hash it verified against, and renewIfStale signs with exactly those.
// A request verified just before a revocation therefore gets a renewal that dies with the
// rotation. verifySession also re-checks, under the lock RevokeSessions swaps the state under,
// that the state it verified against is still the current one, so a request that raced the
// revocation is refused rather than authorised (and a logged-out session cannot slip through
// while the revocation list is cleared).
//
// Over HTTPS the cookie is named with the "__Host-" prefix (or "__Secure-" under a UrlBase), which
// browsers only accept from a secure origin: plain-HTTP services on the same host name can then
// neither read nor overwrite it.

const (
	sessionGenerationSetting = "auth.sessionGeneration"
	revokedSessionsSetting   = "auth.revokedSessions"

	// maxRevokedSessions bounds the revocation list; beyond it every session is revoked.
	maxRevokedSessions = 256

	// DeviceCookieName marks a browser that signed in successfully before (see deviceKey).
	DeviceCookieName = "DupearrDevice"
	// deviceLifetime is how long a browser stays "known" after its last successful login.
	deviceLifetime = 180 * 24 * time.Hour
)

// signingState is the session signing key and the session generation. It is never mutated:
// RevokeSessions replaces it as a whole, so a signature is always made with a consistent pair.
type signingState struct {
	key        []byte
	generation int64
}

// session is a verified session cookie.
type session struct {
	userID   int64
	expires  time.Time
	remember bool
	id       string // session id: random per login, kept across renewals

	// What the token was verified against; a renewal is signed with exactly these (GAP-06).
	signing      *signingState
	passwordHash string
}

// Session is a caller's verified session, captured before a change that revokes sessions (a
// password change, RevokeSessions) so that KeepSession can re-issue it for the caller afterwards.
type Session struct {
	userID   int64
	remember bool
	id       string
}

// sessionIDBytes is the size of a session id (hex encoded in the token).
const sessionIDBytes = 16

func newSessionID() string {
	b := make([]byte, sessionIDBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go ≥ 1.24)
	return hex.EncodeToString(b)
}

func lifetime(remember bool) time.Duration {
	if remember {
		return RememberLifetime
	}
	return SessionLifetime
}

// cookieName returns the name of a cookie for this request: base over plain HTTP; over HTTPS the
// "__Host-" prefixed name (cookie path "/") or "__Secure-" (under a UrlBase, where the path is not
// "/"), which browsers only accept with the Secure attribute from a secure origin.
func (s *Service) cookieName(base string, secure bool) string {
	switch {
	case !secure:
		return base
	case s.cookiePath() == "/":
		return "__Host-" + base
	default:
		return "__Secure-" + base
	}
}

// cookieNames returns every name a cookie may have (for clearing and detection).
func cookieNames(base string) []string {
	return []string{base, "__Host-" + base, "__Secure-" + base}
}

// HasSessionCookie reports whether the request carries a session cookie (valid or not), e.g. to
// tell the web UI (which sends its cookie along with the API key) from a script.
func HasSessionCookie(r *http.Request) bool {
	for _, n := range cookieNames(CookieName) {
		if _, err := r.Cookie(n); err == nil {
			return true
		}
	}
	return false
}

// setSessionCookie issues a session cookie for u, signed with the current signing state: a new
// session when id is "", else a re-issue of session id (KeepSession).
func (s *Service) setSessionCookie(w http.ResponseWriter, r *http.Request, u *models.User, remember bool, now time.Time, id string) {
	s.issueSessionCookie(w, r, s.signing.Load(), u.ID, u.PasswordHash, remember, now, id)
}

// issueSessionCookie issues a session cookie signed with st for the user userID whose password
// hash is passwordHash.
func (s *Service) issueSessionCookie(w http.ResponseWriter, r *http.Request, st *signingState, userID int64, passwordHash string,
	remember bool, now time.Time, id string) {
	if id == "" {
		id = newSessionID()
	}
	exp := now.Add(lifetime(remember)).Truncate(time.Second)
	secure := s.secureRequest(r)
	c := &http.Cookie{
		Name:     s.cookieName(CookieName, secure),
		Value:    signToken(st, userID, exp, remember, id, passwordHash),
		Path:     s.cookiePath(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if remember {
		c.Expires = exp
		c.MaxAge = int(RememberLifetime / time.Second)
	}
	http.SetCookie(w, c)
}

// clearSessionCookies expires the session cookie under every name.
func (s *Service) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := s.secureRequest(r)
	for _, name := range cookieNames(CookieName) {
		if strings.HasPrefix(name, "__") && !secure {
			continue // prefixed cookies can only be set (and cleared) over HTTPS
		}
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     s.cookiePath(),
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			Secure:   secure || strings.HasPrefix(name, "__"),
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// encodeToken returns a token signed with the current signing state (see signToken).
func (s *Service) encodeToken(userID int64, exp time.Time, remember bool, id, passwordHash string) string {
	return signToken(s.signing.Load(), userID, exp, remember, id, passwordHash)
}

// signToken returns base64url("<userID>|<expiryUnix>|<remember 0/1>|<session id>|<hex HMAC>"),
// signed with st.
func signToken(st *signingState, userID int64, exp time.Time, remember bool, id, passwordHash string) string {
	payload := tokenPayload(userID, exp.Unix(), remember, id)
	sig := st.sign(payload, passwordHash)
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + hex.EncodeToString(sig)))
}

func tokenPayload(userID, expUnix int64, remember bool, id string) string {
	r := "0"
	if remember {
		r = "1"
	}
	return strconv.FormatInt(userID, 10) + "|" + strconv.FormatInt(expUnix, 10) + "|" + r + "|" + id
}

// sign computes the token MAC over the payload, st's session generation and the password hash,
// keyed with st's key.
func (st *signingState) sign(payload, passwordHash string) []byte {
	m := hmac.New(sha256.New, st.key)
	m.Write([]byte(tokenVersion + "|" + strconv.FormatInt(st.generation, 10) + "|" + payload + "|"))
	m.Write([]byte(passwordHash))
	return m.Sum(nil)
}

// token is a decoded (not yet verified) session cookie.
type token struct {
	payload  string // what the signature covers
	userID   int64
	exp      time.Time
	remember bool
	id       string
	sig      []byte
}

// parseToken decodes a cookie value without verifying its signature.
func parseToken(value string) (token, bool) {
	if value == "" || len(value) > maxCookieLength {
		return token{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return token{}, false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 5 {
		return token{}, false
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	expUnix, err2 := strconv.ParseInt(parts[1], 10, 64)
	id, err3 := hex.DecodeString(parts[3])
	sig, err4 := hex.DecodeString(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || uid <= 0 || (parts[2] != "0" && parts[2] != "1") ||
		len(id) != sessionIDBytes || len(sig) != sha256.Size {
		return token{}, false
	}
	t := token{userID: uid, exp: time.Unix(expUnix, 0), remember: parts[2] == "1", id: hex.EncodeToString(id), sig: sig}
	t.payload = tokenPayload(uid, expUnix, t.remember, t.id)
	return t, true
}

// verifySession checks the request's session cookie: well-formed, unexpired, not issued for
// longer than its lifetime, signed for the user's current password hash with the current signing
// state (key and generation), and its session not revoked by a logout. The revocation check and
// the check that the signing state is still current are made together under revokedMu, which
// RevokeSessions holds while it replaces the state and clears the revocation list: a request that
// raced a revocation is refused (GAP-06).
func (s *Service) verifySession(r *http.Request) (*session, bool) {
	c, err := r.Cookie(s.cookieName(CookieName, s.secureRequest(r)))
	if err != nil {
		return nil, false
	}
	t, ok := parseToken(c.Value)
	if !ok {
		return nil, false
	}
	now := s.now()
	if !now.Before(t.exp) || t.exp.Sub(now) > lifetime(t.remember)+time.Minute {
		return nil, false
	}
	st := s.signing.Load()
	if s.isRevokedOrRotated(t.id, st) {
		return nil, false // checked before the lookup too: a revoked cookie costs no query
	}
	u, err := s.st.Users().GetByID(r.Context(), t.userID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && r.Context().Err() == nil {
			s.log.Warn("Cannot verify session: user lookup failed", "error", err)
		}
		return nil, false
	}
	if !hmac.Equal(t.sig, st.sign(t.payload, u.PasswordHash)) || s.isRevokedOrRotated(t.id, st) {
		return nil, false
	}
	return &session{userID: t.userID, expires: t.exp, remember: t.remember, id: t.id, signing: st, passwordHash: u.PasswordHash}, true
}

// isRevokedOrRotated reports whether session id was revoked by a logout or st is no longer the
// current signing state (a revocation replaced it).
func (s *Service) isRevokedOrRotated(id string, st *signingState) bool {
	s.revokedMu.Lock()
	defer s.revokedMu.Unlock()
	_, revoked := s.revoked[id]
	return revoked || s.signing.Load() != st
}

// renewIfStale re-issues the cookie once more than half of its lifetime has passed (sliding
// expiration, like ASP.NET cookie authentication in the *arrs). The renewal is signed with the
// signing state and password hash the token was verified against, never with state read later: a
// renewal made while a revocation or password change runs dies with it (GAP-06).
func (s *Service) renewIfStale(w http.ResponseWriter, r *http.Request, sess *session) {
	now := s.now()
	if sess.expires.Sub(now) >= lifetime(sess.remember)/2 || sess.signing == nil {
		return
	}
	s.issueSessionCookie(w, r, sess.signing, sess.userID, sess.passwordHash, sess.remember, now, sess.id)
}

// CurrentSession returns the request's valid session cookie, or nil. Capture it before a change
// that revokes sessions and hand it to KeepSession afterwards, so the user who made the change
// stays signed in in this browser.
func (s *Service) CurrentSession(r *http.Request) *Session {
	if sess, ok := s.verifySession(r); ok {
		return &Session{userID: sess.userID, remember: sess.remember, id: sess.id}
	}
	return nil
}

// KeepSession issues a fresh session cookie (current password and generation) for a session
// captured with CurrentSession, and a fresh device cookie (the change revoked the old one). A nil
// sess is a no-op.
func (s *Service) KeepSession(w http.ResponseWriter, r *http.Request, sess *Session) error {
	if sess == nil {
		return nil
	}
	u, err := s.st.Users().GetByID(r.Context(), sess.userID)
	if err != nil {
		return fmt.Errorf("auth: keep session: %w", err)
	}
	now := s.now()
	s.setSessionCookie(w, r, u, sess.remember, now, sess.id)
	s.setDeviceCookie(w, r, u.Username, now)
	return nil
}

// ---------------------------------------------------------------------------
// Session generation and revocation
// ---------------------------------------------------------------------------

func loadGeneration(ctx context.Context, st store.Store) (int64, error) {
	v, ok, err := st.Settings().GetValue(ctx, sessionGenerationSetting)
	if err != nil {
		return 0, fmt.Errorf("auth: read session generation: %w", err)
	}
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

func loadRevoked(ctx context.Context, st store.Store) map[string]int64 {
	out := make(map[string]int64)
	v, ok, err := st.Settings().GetValue(ctx, revokedSessionsSetting)
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return out
	}
	if json.Unmarshal([]byte(v), &out) != nil {
		return make(map[string]int64)
	}
	return out
}

// RevokeSessions signs out every session and device cookie and ends live-update streams
// authenticated by them. It replaces the session signing key with a new random one (and increments
// the session generation), so the revocation also covers a leaked key — from an old backup, say:
// the generation alone is a guessable counter. Use KeepSession to keep the caller signed in.
//
// The new key and generation are persisted first (outside revokedMu, so request verification
// never waits for database I/O); then the signing state is replaced and the revocation list
// cleared in one short critical section under revokedMu (see verifySession).
func (s *Service) RevokeSessions(ctx context.Context) error {
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	key, err := newSessionKey(ctx, s.st)
	if err != nil {
		return fmt.Errorf("auth: revoke sessions: %w", err)
	}
	next := &signingState{key: key, generation: s.signing.Load().generation + 1}
	if err := s.st.Settings().SetValue(ctx, sessionGenerationSetting, strconv.FormatInt(next.generation, 10)); err != nil {
		s.log.Warn("Could not save the session generation", "error", err)
	}
	s.revokedMu.Lock()
	s.signing.Store(next)
	s.revoked = make(map[string]int64)
	s.revokedMu.Unlock()
	if err := s.st.Settings().SetValue(ctx, revokedSessionsSetting, "{}"); err != nil {
		s.log.Warn("Could not clear the session revocation list", "error", err)
	}
	s.log.Info("Auth-Sessions revoked: every session must sign in again")
	s.audit(ctx, audit.KindSessionsRevoked, "Every session was signed out")
	s.notifyCredentialsChanged()
	return nil
}

// revokeSession adds one session (every token of it: renewals keep the id) to the revocation
// list (logout).
func (s *Service) revokeSession(ctx context.Context, sess *session) {
	// Writes of the list are ordered, so an older snapshot never overwrites a newer one.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.revokedMu.Lock()
	now := s.now()
	for id, exp := range s.revoked {
		if exp < now.Unix() {
			delete(s.revoked, id)
		}
	}
	if len(s.revoked) >= maxRevokedSessions {
		s.revokedMu.Unlock()
		if err := s.RevokeSessions(ctx); err != nil {
			s.log.Warn("Could not revoke the session on logout", "error", err)
		}
		return
	}
	// A token of this session may have been renewed just now (by its owner or from a copy): the
	// entry must outlive the longest token the session can have.
	s.revoked[sess.id] = now.Add(lifetime(sess.remember) + time.Minute).Unix()
	data, err := json.Marshal(s.revoked)
	s.revokedMu.Unlock()
	if err == nil {
		err = s.st.Settings().SetValue(ctx, revokedSessionsSetting, string(data))
	}
	if err != nil {
		s.log.Warn("Could not persist the session revocation (it holds until restart)", "error", err)
	}
	s.notifyCredentialsChanged()
}

// ---------------------------------------------------------------------------
// Device cookies (login throttling)
// ---------------------------------------------------------------------------

// deviceMAC signs a device cookie for username (case-insensitive), its expiry and the session
// generation (so "log out all sessions" also forgets every device).
func (s *Service) deviceMAC(username string, expUnix int64) []byte {
	st := s.signing.Load()
	m := hmac.New(sha256.New, st.key)
	m.Write([]byte("device|v1|" + strconv.FormatInt(st.generation, 10) + "|" + strconv.FormatInt(expUnix, 10) + "|"))
	m.Write([]byte(strings.ToLower(strings.TrimSpace(username))))
	return m.Sum(nil)
}

// setDeviceCookie marks this browser as one that signed in as username.
func (s *Service) setDeviceCookie(w http.ResponseWriter, r *http.Request, username string, now time.Time) {
	exp := now.Add(deviceLifetime).Unix()
	secure := s.secureRequest(r)
	value := strconv.FormatInt(exp, 10) + "|" + hex.EncodeToString(s.deviceMAC(username, exp))
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(DeviceCookieName, secure),
		Value:    base64.RawURLEncoding.EncodeToString([]byte(value)),
		Path:     s.cookiePath(),
		MaxAge:   int(deviceLifetime / time.Second),
		Expires:  time.Unix(exp, 0),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// deviceKey returns the limiter key of a valid device cookie for username, or "".
func (s *Service) deviceKey(r *http.Request, username string) string {
	c, err := r.Cookie(s.cookieName(DeviceCookieName, s.secureRequest(r)))
	if err != nil || len(c.Value) > maxCookieLength {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return ""
	}
	expStr, sigHex, ok := strings.Cut(string(raw), "|")
	if !ok {
		return ""
	}
	exp, err1 := strconv.ParseInt(expStr, 10, 64)
	sig, err2 := hex.DecodeString(sigHex)
	if err1 != nil || err2 != nil || len(sig) != sha256.Size || s.now().Unix() >= exp {
		return ""
	}
	if !hmac.Equal(sig, s.deviceMAC(username, exp)) {
		return ""
	}
	return "d:" + hex.EncodeToString(sig[:12])
}

// ---------------------------------------------------------------------------
// Credential change notifications (live-update streams)
// ---------------------------------------------------------------------------

// CredentialsChanged returns a channel that is closed at the next change that may invalidate
// the credentials of an open request (logout, session revocation, password or API-key change,
// authentication method change). Long-lived handlers (Server-Sent Events) re-check the request
// with StillAuthenticated when it fires, then take the new channel.
func (s *Service) CredentialsChanged() <-chan struct{} {
	s.changedMu.Lock()
	defer s.changedMu.Unlock()
	return s.changed
}

func (s *Service) notifyCredentialsChanged() {
	s.changedMu.Lock()
	defer s.changedMu.Unlock()
	close(s.changed)
	s.changed = make(chan struct{})
}

// StillAuthenticated re-checks the credentials of a request that was authenticated earlier
// (ignoring the authentication info in its context): a long-lived response must end once its
// session is revoked or its API key replaced.
func (s *Service) StillAuthenticated(r *http.Request) bool {
	_, ok := s.authenticate(nil, r)
	return ok
}
