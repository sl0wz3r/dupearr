package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Login throttling (docs/DECISIONS.md D8). Every attempt is counted against two keys:
//
//   - the client address (ClientIP: the TCP peer, or the client a trusted proxy names; IPv6 per
//     /56): freeAttempts attempts, then each further attempt must wait
//     baseDelay·2^(failures-freeAttempts), capped at maxDelay;
//   - the typed username (userFreeAttempts, capped at userMaxDelay), which rotating source
//     addresses cannot bypass.
//
// A browser that has signed in successfully before carries a device cookie (see deviceKey in session.go):
// its attempts are counted against that cookie only, so an attacker who keeps the username (or a
// shared proxy address) in backoff cannot lock the owner out. A key is forgotten after
// forgetAfter without attempts; a successful login resets it.
const (
	freeAttempts   = 5
	baseDelay      = time.Second
	maxDelay       = 15 * time.Minute
	forgetAfter    = time.Hour
	maxLimiterKeys = 10000

	userFreeAttempts = 10
	userMaxDelay     = 5 * time.Minute

	// ipv6LimiterPrefix is the IPv6 prefix length one client is throttled by: end users usually
	// get a /56 (often a /48), so a single host can rotate through 256 /64s.
	ipv6LimiterPrefix = 56
)

// limiterPolicy is the backoff schedule of a limiter.
type limiterPolicy struct {
	free int
	base time.Duration
	max  time.Duration
}

var (
	addressPolicy = limiterPolicy{free: freeAttempts, base: baseDelay, max: maxDelay}
	userPolicy    = limiterPolicy{free: userFreeAttempts, base: baseDelay, max: userMaxDelay}
)

type attempts struct {
	failures int
	last     time.Time
}

// limiter tracks failed login attempts per key. It is safe for concurrent use.
type limiter struct {
	policy  limiterPolicy
	mu      sync.Mutex
	entries map[string]*attempts
}

func newLimiter() *limiter { return newLimiterWith(addressPolicy) }

func newLimiterWith(p limiterPolicy) *limiter {
	return &limiter{policy: p, entries: make(map[string]*attempts)}
}

// delay returns the wait required after n failures under the address policy.
func delay(n int) time.Duration { return addressPolicy.delay(n) }

func (p limiterPolicy) delay(n int) time.Duration {
	if n < p.free {
		return 0
	}
	shift := n - p.free
	if shift >= 20 {
		return p.max
	}
	return min(p.base<<shift, p.max)
}

// begin starts an attempt. It returns the remaining wait when the key is backing off; otherwise
// it counts the attempt as a failure up front (so parallel guesses are throttled too) and returns
// 0 — call reset on success or abort when the attempt could not be evaluated.
func (l *limiter) begin(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e != nil && now.Sub(e.last) > forgetAfter {
		delete(l.entries, key)
		e = nil
	}
	if e != nil {
		if until := e.last.Add(l.policy.delay(e.failures)); now.Before(until) {
			return until.Sub(now)
		}
	}
	if e == nil {
		if len(l.entries) >= maxLimiterKeys {
			l.evictLocked(now)
		}
		e = &attempts{}
		l.entries[key] = e
	}
	e.failures++
	e.last = now
	return 0
}

// abort undoes the failure counted by begin (the attempt was not evaluated, e.g. a DB error).
func (l *limiter) abort(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil {
		e.failures--
		if e.failures <= 0 {
			delete(l.entries, key)
		}
	}
}

// reset forgets a key (successful login).
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// evictLocked drops expired keys, or the stalest one when none has expired.
func (l *limiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.entries {
		if now.Sub(e.last) > forgetAfter {
			delete(l.entries, k)
			continue
		}
		if oldestKey == "" || e.last.Before(oldest) {
			oldestKey, oldest = k, e.last
		}
	}
	if len(l.entries) >= maxLimiterKeys && oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

// limiterKey is the throttling key of a client: the IPv4 address, or the /56 prefix of an IPv6
// address (see ipv6LimiterPrefix).
func limiterKey(ip net.IP) string {
	addr, ok := netipFromIP(ip)
	if !ok {
		return "unknown"
	}
	if addr.Is6() {
		if p, err := addr.Prefix(ipv6LimiterPrefix); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// userLimiterKey is the throttling key of a typed username: a fixed-size digest of its lower-case
// form (so a long username cannot bloat the limiter).
func userLimiterKey(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return "u:" + hex.EncodeToString(sum[:16])
}

// netipFromIP converts ip to an unmapped netip.Addr.
func netipFromIP(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// ---------------------------------------------------------------------------
// Log sampling
// ---------------------------------------------------------------------------

// Warnings that any client can trigger at will (a throttled or failed login, a refused setup, a
// rejected cross-site request) are sampled: at most logSampleBurst lines of one kind per
// logSampleWindow are logged at Warn, the rest at Debug, and the next Warn line of that kind
// reports how many were demoted ("suppressed"). Without this, a few thousand cheap requests (or a
// malicious page looping no-cors requests through a LAN visitor's browser) rotate the whole log
// history away, including the evidence of the attack.
const (
	logSampleWindow = time.Minute
	logSampleBurst  = 10
)

// logSampler counts Warn lines per kind (a fixed set of strings, so its maps stay small).
type logSampler struct {
	mu         sync.Mutex
	window     time.Time
	counts     map[string]int
	suppressed map[string]int
}

// allow reports whether one more line of kind may be logged at Warn now and, if so, how many
// lines of that kind were demoted to Debug since the last one that was not.
func (l *logSampler) allow(kind string, now time.Time) (ok bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil || now.Sub(l.window) >= logSampleWindow || now.Before(l.window) {
		l.window = now
		l.counts = make(map[string]int)
	}
	if l.suppressed == nil {
		l.suppressed = make(map[string]int)
	}
	if l.counts[kind] >= logSampleBurst {
		l.suppressed[kind]++
		return false, 0
	}
	l.counts[kind]++
	suppressed = l.suppressed[kind]
	delete(l.suppressed, kind)
	return true, suppressed
}

// maxLoggedPath bounds a request path written to the log at Warn (a URL may be ~64 KiB).
const maxLoggedPath = 200

// truncatePath bounds a client-supplied path for logging.
func truncatePath(p string) string {
	if len(p) > maxLoggedPath {
		return p[:maxLoggedPath] + "…"
	}
	return p
}

// warnSampled logs msg at Warn, or at Debug once more than logSampleBurst lines of kind were
// logged in the current window (see logSampler).
func (s *Service) warnSampled(kind, msg string, attrs ...any) {
	ok, suppressed := s.logSample.allow(kind, s.now())
	if !ok {
		s.log.Debug(msg, attrs...)
		return
	}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	s.log.Warn(msg, attrs...)
}

// LogRefusedSetup logs a refused first-run setup request (sampled: an unauthenticated client can
// send any number of them).
func (s *Service) LogRefusedSetup(r *http.Request, reason string) {
	s.warnSampled("setup", "Refused first-run setup: "+reason,
		append(s.clientAttrs(r), "host", truncateHost(r.Host))...)
}

// ---------------------------------------------------------------------------
// bcrypt concurrency
// ---------------------------------------------------------------------------

// ErrBusy is returned by Login (and VerifyPassword) when every password-hashing slot stays busy
// for bcryptWait: the attempt was not evaluated and is not counted. Answer 503 + Retry-After.
var ErrBusy = errors.New("authentication is busy; try again in a moment")

// bcryptWait is how long an unauthenticated password check waits for a free hashing slot (a
// variable for tests).
var bcryptWait = 2 * time.Second

// bcryptSlots bounds concurrent bcrypt operations (each costs ~0.2 s of one CPU at cost 12), so
// a flood of unauthenticated logins cannot starve the rest of the server (or Plex transcodes on
// the same host). A variable so tests can resize it.
var bcryptSlots = make(chan struct{}, max(2, runtime.NumCPU()/4))

// acquireBcrypt takes a hashing slot, waiting at most wait (forever when wait <= 0) or until ctx
// ends. The returned function releases it.
func acquireBcrypt(ctx context.Context, wait time.Duration) (release func(), ok bool) {
	slots := bcryptSlots
	release = func() { <-slots }
	select {
	case slots <- struct{}{}:
		return release, true
	default:
	}
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case slots <- struct{}{}:
		return release, true
	case <-timeout:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}
