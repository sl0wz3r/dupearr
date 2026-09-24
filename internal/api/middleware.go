package api

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sl0wz3r/dupearr/internal/logging"
)

// statusWriter records the status and size of a response. It forwards Flush and exposes the
// wrapped writer through Unwrap (http.ResponseController), which SSE relies on.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = code >= 200 // 1xx informational responses are not final
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = http.StatusOK, true
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (w *statusWriter) Flush() {
	if !w.wroteHeader {
		w.status, w.wroteHeader = http.StatusOK, true
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer (for http.ResponseController).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// wrap returns w as a *statusWriter (reusing an existing one).
func wrap(w http.ResponseWriter) *statusWriter {
	if sw, ok := w.(*statusWriter); ok {
		return sw
	}
	return &statusWriter{ResponseWriter: w}
}

// recoverPanics turns a handler panic into a logged error and a 500 JSON response (no stack
// trace in the response). http.ErrAbortHandler is re-raised so the server aborts the connection.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := wrap(w)
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value
				panic(v)
			}
			s.log.Error("Panic while handling a request",
				"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if !sw.wroteHeader {
				writeMessage(sw, http.StatusInternalServerError, "Internal server error")
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// logRequests logs every request at debug level: method, path, redacted query, status, size and
// duration. Query strings may carry the API key or a Plex token; logging.Redact masks them.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := wrap(w)
		next.ServeHTTP(sw, r)
		if !s.log.Enabled(r.Context(), slog.LevelDebug) {
			return
		}
		status := sw.status
		if !sw.wroteHeader {
			status = http.StatusOK
		}
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", sw.bytes,
			"durationMs", time.Since(start).Milliseconds(),
			"ip", remoteHost(r.RemoteAddr),
		}
		if r.URL.RawQuery != "" {
			attrs = append(attrs, "query", logging.Redact(r.URL.RawQuery))
		}
		s.log.Debug("HTTP request", attrs...)
	})
}

func remoteHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// defaultCSP is the Content-Security-Policy of every response that does not set its own (API
// JSON, downloads, static files): nothing may load or run if such a response is ever rendered as
// a document. HTML pages (spa.go) and the poster proxy replace it.
const defaultCSP = "default-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

// securityHeaders sets headers every response carries. API and bootstrap responses (which may
// contain the API key) are additionally marked uncacheable; the SPA handler sets its own caching
// and Content-Security-Policy.
//
// There is deliberately no Strict-Transport-Security: HSTS applies to a host name on every port,
// and Dupearr usually shares its host name (an Unraid server, a NAS) with plain-HTTP services
// (Sonarr, Radarr, the NAS UI) that it would break. Set HSTS on the reverse proxy that owns a
// dedicated host name instead.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", defaultCSP)
		next.ServeHTTP(w, r)
	})
}

// bodyReadTimeout bounds reading a request body. The HTTP server only has a ReadHeaderTimeout
// (long-lived responses such as SSE and slow authenticated uploads must not be cut), so without
// it a client could send the headers of a request that announces a body and then trickle or
// withhold it, holding a connection and a goroutine indefinitely — before authentication, since
// net/http drains an unread body before answering. A variable for tests.
var bodyReadTimeout = 30 * time.Second

// limitBodyRead sets a read deadline on requests that announce a body (it is the outermost
// middleware, so it applies before authentication). net/http clears the deadline itself once the
// body has been read to the end (it then starts watching the connection for a client that goes
// away), so a handler may run long after reading its body. Requests without a body are left
// alone: setting a deadline then would end their context when it passes. Handlers that accept
// large bodies from authenticated users extend it (see extendBodyReadDeadline).
//
// When the handler returns without having read the body to the end (a 401 from the auth
// middleware, a public page, an early validation error), the deadline is expired at once: net/http
// would otherwise wait for (and drain) the rest before closing or reusing the connection.
func limitBodyRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		// Not supported by every ResponseWriter (e.g. httptest.ResponseRecorder); best effort.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
		body := &eofTracker{ReadCloser: r.Body}
		r.Body = body
		next.ServeHTTP(w, r)
		if !body.eof.Load() {
			_ = rc.SetReadDeadline(time.Now())
		}
	})
}

// eofTracker records whether a request body was read to the end.
type eofTracker struct {
	io.ReadCloser
	eof atomic.Bool
}

func (t *eofTracker) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if err == io.EOF {
		t.eof.Store(true)
	}
	return n, err
}

// progressReader extends the connection's read deadline by idle after every successful read, so
// a large upload may take as long as it needs while it makes progress, but a stalled one ends.
type progressReader struct {
	r    io.ReadCloser
	rc   *http.ResponseController
	idle time.Duration
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		_ = p.rc.SetReadDeadline(time.Now().Add(p.idle))
	}
	return n, err
}

func (p *progressReader) Close() error { return p.r.Close() }

// extendBodyReadDeadline replaces the fixed body deadline of limitBodyRead with an idle deadline
// that every read extends (authenticated uploads only).
func extendBodyReadDeadline(w http.ResponseWriter, r *http.Request, idle time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(idle))
	r.Body = &progressReader{r: r.Body, rc: rc, idle: idle}
}

// urlBaseMiddleware serves the app under the configured UrlBase: a request whose path is outside
// the base is redirected (307, method and body preserved) to {UrlBase}{path}{?query}; inside it,
// the base is stripped before routing.
func (s *Server) urlBaseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := s.urlBase()
		if base == "" {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if p != base && !strings.HasPrefix(p, base+"/") {
			target := base + r.URL.EscapedPath()
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(p, base)
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		if r.URL.RawPath != "" {
			if raw := strings.TrimPrefix(r.URL.RawPath, base); raw != r.URL.RawPath {
				r2.URL.RawPath = raw
				if r2.URL.RawPath == "" {
					r2.URL.RawPath = "/"
				}
			} else {
				r2.URL.RawPath = ""
			}
		}
		next.ServeHTTP(w, r2)
	})
}

// apiFallback answers unknown /api paths with JSON: 405 (with Allow) when the path exists for
// another method, else 404.
func (s *Server) apiFallback(mux *http.ServeMux) http.Handler {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var allow []string
		for _, m := range methods {
			if m == r.Method {
				continue
			}
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/api/" && pattern != "/" {
				allow = append(allow, m)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			writeMessage(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		writeMessage(w, http.StatusNotFound, "Not found")
	})
}
