package auth

// Regression tests for GAP-06 (docs/SECURITY.md): a request authenticated just before
// RevokeSessions must never be renewed with the key, generation or password hash that the
// revocation installed, and a session revoked by a logout must never come back.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sessionRequest is a GET of a protected route carrying c.
func sessionRequest(c *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	req.RemoteAddr = remoteAddr
	req.AddCookie(c)
	return req
}

// cookieValid reports whether c authenticates a request now.
func (e *testEnv) cookieValid(c *http.Cookie) bool {
	rr := httptest.NewRecorder()
	e.svc.Middleware(echo).ServeHTTP(rr, sessionRequest(c))
	return rr.Code == http.StatusOK
}

// A renewal is only as valid as the token it renews: verified before a revocation, renewed after
// it, the renewal must die with the rotation (deterministic interleaving).
func TestRenewalAfterRevocationIsSignedWithTheVerifiedState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(e *testEnv)
	}{
		{"log out all sessions", func(e *testEnv) {
			if err := e.svc.RevokeSessions(context.Background()); err != nil {
				t.Fatal(err)
			}
		}},
		{"password change", func(e *testEnv) {
			if _, err := e.svc.UpdateUser(context.Background(), testUser, "a brand new password 123"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, nil)
			e.createUser()
			stolen := e.login(false)
			e.advance(4 * 24 * time.Hour) // past half of the 7-day lifetime: every request renews it
			req := sessionRequest(stolen)
			sess, ok := e.svc.verifySession(req)
			if !ok {
				t.Fatal("the stolen cookie does not verify (test setup)")
			}
			tc.revoke(e)
			rr := httptest.NewRecorder()
			e.svc.renewIfStale(rr, req, sess)
			renewed := findCookie(rr.Result().Cookies())
			if renewed != nil && e.cookieValid(renewed) {
				t.Fatal("a cookie renewed from a session verified before the revocation is valid after it")
			}
			if e.cookieValid(stolen) {
				t.Fatal("the stolen cookie itself still works")
			}
		})
	}
}

// hammerDuring polls a protected route with c from 32 goroutines while revoke runs, and returns
// every cookie the responses renewed.
func (e *testEnv) hammerDuring(c *http.Cookie, revoke func()) []*http.Cookie {
	h := e.svc.Middleware(echo)
	var (
		mu      sync.Mutex
		renewed []*http.Cookie
		stop    atomic.Bool
		wg      sync.WaitGroup
	)
	for range 32 {
		wg.Go(func() {
			for !stop.Load() {
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, sessionRequest(c))
				if nc := findCookie(rr.Result().Cookies()); nc != nil {
					mu.Lock()
					renewed = append(renewed, nc)
					mu.Unlock()
				}
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	revoke()
	time.Sleep(20 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	return renewed
}

// The scratch proof of concept of the finding, looped: an attacker polling with a stolen stale
// cookie while the owner uses "log out all sessions" or changes the password keeps no session.
func TestNoRenewalSurvivesAConcurrentRevocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(t *testing.T, e *testEnv)
	}{
		{"log out all sessions", func(t *testing.T, e *testEnv) {
			if err := e.svc.RevokeSessions(context.Background()); err != nil {
				t.Error(err)
			}
		}},
		{"password change", func(t *testing.T, e *testEnv) {
			if _, err := e.svc.UpdateUser(context.Background(), testUser, "a brand new password 123"); err != nil {
				t.Error(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for attempt := range 8 {
				e := newTestEnv(t, nil)
				e.createUser()
				stolen := e.login(false)
				e.advance(4 * 24 * time.Hour)
				renewed := e.hammerDuring(stolen, func() { tc.revoke(t, e) })
				for _, c := range renewed {
					if e.cookieValid(c) {
						t.Fatalf("attempt %d: a cookie renewed during the revocation is valid after it (%d renewals)", attempt, len(renewed))
					}
				}
				if e.cookieValid(stolen) {
					t.Fatalf("attempt %d: the stolen cookie survived", attempt)
				}
			}
		})
	}
}

// A session the owner signed out (revoked by id) must not be resurrected when "log out all
// sessions" clears the revocation list while the attacker polls with it.
func TestSignedOutSessionIsNotResurrectedByRevokeSessions(t *testing.T) {
	for attempt := range 4 {
		e := newTestEnv(t, nil)
		e.createUser()
		stolen := e.login(false)
		e.advance(4 * 24 * time.Hour)
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		req.RemoteAddr = remoteAddr
		req.AddCookie(stolen)
		e.svc.Logout(httptest.NewRecorder(), req)
		if e.cookieValid(stolen) {
			t.Fatal("logout did not revoke the session (test setup)")
		}
		renewed := e.hammerDuring(stolen, func() {
			if err := e.svc.RevokeSessions(context.Background()); err != nil {
				t.Error(err)
			}
		})
		for _, c := range renewed {
			if e.cookieValid(c) {
				t.Fatalf("attempt %d: a signed-out session came back through log out all sessions", attempt)
			}
		}
	}
}

// A request whose token was verified with the state a revocation replaced must not be accepted
// once the revocation happened (the check is linearised with RevokeSessions).
func TestVerifySessionRefusesATokenOfAReplacedSigningState(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	c := e.login(false)
	before := e.svc.signing.Load()
	if err := e.svc.RevokeSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.svc.signing.Load() == before {
		t.Fatal("RevokeSessions kept the signing state")
	}
	// A token signed with the replaced state (what the in-flight request verified against).
	u, err := e.svc.User(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := &Service{}
	old.signing.Store(before)
	stale := &http.Cookie{Name: CookieName, Value: old.encodeToken(u.ID, e.clock().Add(time.Hour), false, newSessionID(), u.PasswordHash)}
	if e.cookieValid(stale) {
		t.Fatal("a token signed with the replaced signing state is accepted")
	}
	if e.cookieValid(c) {
		t.Fatal("a session from before RevokeSessions is accepted")
	}
}
