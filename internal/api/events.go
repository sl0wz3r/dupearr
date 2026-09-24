package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	// sseKeepAliveInterval is how often a ": ping" comment is sent (reverse proxies drop idle
	// connections after 60 s by default).
	sseKeepAliveInterval = 25 * time.Second
	// sseBuffer is the per-connection event buffer (the bus drops events for slow subscribers).
	sseBuffer = 256
	// sseRetry tells EventSource how long to wait before reconnecting (ms).
	sseRetry = 3000
)

// sseMessage is the JSON of one SSE message.
type sseMessage struct {
	Name     string `json:"name"`
	Action   string `json:"action"`
	Resource any    `json:"resource,omitempty"`
}

// handleEvents streams bus events as Server-Sent Events: "data: {name, action, resource}".
// DuplicateGroup resources are sent as DuplicateGroupSummary. A "version" message is sent on
// connect and a ": ping" comment every 25 s. The handler returns when the client disconnects, the
// request context is cancelled (shutdown), or the request's credentials stop being valid: they
// are re-checked whenever credentials change (logout, session revocation, password, API key or
// authentication method change) and at every keep-alive, so a stream never outlives its session.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.d.Bus == nil {
		s.writeErr(w, r, errUnavailable("Live updates"))
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived: no write timeout
	_ = rc.SetReadDeadline(time.Time{})  // and no body read deadline (limitBodyRead)

	var authChanged <-chan struct{}
	if s.d.Auth != nil {
		authChanged = s.d.Auth.CredentialsChanged()
	}
	stillValid := func() bool { return s.d.Auth == nil || s.d.Auth.StillAuthenticated(r) }

	ch, unsubscribe := s.d.Bus.Subscribe(sseBuffer)
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}

	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	hello := []byte("retry: " + strconv.Itoa(sseRetry) + "\n\n")
	if msg, err := json.Marshal(sseMessage{Name: "version", Action: events.ActionSync,
		Resource: map[string]string{"version": version.Version}}); err == nil {
		hello = append(hello, sseData(msg)...)
	}
	if !write(hello) {
		return
	}

	ticker := time.NewTicker(s.sseKeepAlive)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-authChanged:
			if !stillValid() {
				s.log.Debug("Ending a live-update stream: its credentials are no longer valid")
				return
			}
			authChanged = s.d.Auth.CredentialsChanged()
		case <-ticker.C:
			if !stillValid() {
				s.log.Debug("Ending a live-update stream: its credentials are no longer valid")
				return
			}
			if !write([]byte(": ping\n\n")) {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			msg, err := json.Marshal(nonNil(sseMessage{Name: ev.Name, Action: ev.Action, Resource: sseResource(ev)}))
			if err != nil {
				s.log.Warn("Dropping an event that cannot be encoded", "name", ev.Name, "error", err)
				continue
			}
			if !write(sseData(msg)) {
				return
			}
		}
	}
}

// sseData frames a JSON document as one SSE message (JSON never contains raw newlines).
func sseData(msg []byte) []byte {
	out := make([]byte, 0, len(msg)+8)
	out = append(out, "data: "...)
	out = append(out, msg...)
	return append(out, '\n', '\n')
}

// sseResource converts event resources for the UI: duplicate groups become summaries.
func sseResource(ev events.Event) any {
	if ev.Name != events.NameDuplicate {
		return ev.Resource
	}
	switch g := ev.Resource.(type) {
	case *models.DuplicateGroup:
		if g != nil {
			return summarize(g)
		}
	case models.DuplicateGroup:
		return summarize(&g)
	}
	return ev.Resource
}
