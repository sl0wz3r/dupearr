package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// maxWebhookBody caps inbound webhook bodies (Plex multipart payloads may carry a thumbnail).
const maxWebhookBody = 16 << 20

// webhookResponse answers every webhook: whether a TargetedScan was queued (and, when it was not
// although the event asked for one, why).
type webhookResponse struct {
	Queued    bool   `json:"queued"`
	CommandID int64  `json:"commandId,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Webhook-triggered scans are bounded (GAP-11): every targeted scan lists the libraries involved
// in full from Plex and runs in the exclusive command lane, and distinct ids defeat the queue's
// de-duplication, so whoever holds the webhook token — which travels in *arr and Plex webhook
// URLs — could otherwise queue scans without limit, starve scans, backups and removals, and turn
// every request into full library listings against Plex. Webhooks are a shortcut: the scheduled
// scan finds whatever a refused webhook would have.
const (
	// maxQueuedWebhookScans is how many webhook-triggered targeted scans may wait in the queue.
	maxQueuedWebhookScans = 8
	// webhookScanBurst webhook scans may be queued at once; one more every webhookScanRefill.
	webhookScanBurst  = 30
	webhookScanRefill = 20 * time.Second
	// webhookRefusedWarnEvery samples the warning about refused webhook scans.
	webhookRefusedWarnEvery = time.Minute
)

// webhookLimiter is the token bucket of webhook-triggered scans.
type webhookLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	tokens   float64
	last     time.Time
	lastWarn time.Time
}

func newWebhookLimiter(now func() time.Time) *webhookLimiter {
	return &webhookLimiter{now: now, tokens: webhookScanBurst, last: now()}
}

// allow takes a token when one is available.
func (l *webhookLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens = min(webhookScanBurst, l.tokens+float64(elapsed)/float64(webhookScanRefill))
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// warnDue reports whether a refused webhook scan should be logged as a warning (sampled).
func (l *webhookLimiter) warnDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := l.now(); now.Sub(l.lastWarn) >= webhookRefusedWarnEvery {
		l.lastWarn = now
		return true
	}
	return false
}

// arrWebhookHandler returns the handler of /webhook/radarr (kind radarr) or /webhook/sonarr.
func (s *Server) arrWebhookHandler(kind models.ArrKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.handleArrWebhook(w, r, kind) }
}

// handleArrWebhook handles Radarr/Sonarr "Connect → Webhook" calls (API key required by the auth
// middleware): relevant events (Download/Upgrade, Rename, file deletes) queue a TargetedScan by
// external id; Test and other events answer {"queued":false}. A payload of the other application
// (a Sonarr payload — it has a "series" — sent to /webhook/radarr, or a Radarr payload — a
// "movie" — sent to /webhook/sonarr) is rejected with 400, Test events included, so a
// misconfigured Connect entry is noticed when it is tested. Payloads without either (e.g. Health
// or ApplicationUpdate events) are accepted and ignored.
func (s *Server) handleArrWebhook(w http.ResponseWriter, r *http.Request, route models.ArrKind) {
	body := http.MaxBytesReader(w, r.Body, maxWebhookBody)
	defer body.Close()
	p, err := arr.ParseWebhook(body)
	if err != nil {
		s.log.Debug("Rejected an *arr webhook", "path", r.URL.Path, "error", err)
		s.writeErr(w, r, errBadRequest("Invalid webhook payload"))
		return
	}
	if kind := p.Kind(); kind != "" && kind != route {
		s.log.Warn("Rejected a webhook sent to the wrong URL", "path", r.URL.Path, "payload", kind, "instance", p.InstanceName)
		s.writeErr(w, r, errBadRequest("This is a %s webhook; use the /api/v1/webhook/%s URL for it", appLabel(kind), kind))
		return
	}
	if strings.EqualFold(p.EventType, "Test") {
		s.log.Info("Received a test webhook", "path", r.URL.Path, "instance", p.InstanceName)
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	scan, ok := p.ToTargetedScan()
	if !ok {
		s.log.Debug("Ignoring webhook event", "path", r.URL.Path, "eventType", p.EventType)
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	s.queueWebhookScan(w, r, scan, "eventType", p.EventType, "instance", p.InstanceName)
}

// handlePlexWebhook handles Plex webhooks (multipart "payload"): library.new on a configured
// server queues a TargetedScan of that item.
func (s *Server) handlePlexWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	p, err := plex.ParseWebhook(r)
	if err != nil {
		s.log.Debug("Rejected a Plex webhook", "error", err)
		s.writeErr(w, r, errBadRequest("Invalid webhook payload"))
		return
	}
	if p.Event != "library.new" {
		s.log.Debug("Ignoring Plex webhook event", "event", p.Event)
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	rk := strings.TrimSpace(p.Metadata.RatingKey)
	if !ratingKeyRe.MatchString(rk) {
		s.log.Debug("Ignoring Plex webhook without a usable rating key", "event", p.Event)
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	servers, err := s.d.Store.MediaServers().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var server *models.MediaServer
	for i := range servers {
		if servers[i].Enabled && servers[i].MachineIdentifier != "" && strings.EqualFold(servers[i].MachineIdentifier, p.Server.UUID) {
			server = &servers[i]
			break
		}
	}
	if server == nil {
		s.log.Info("Ignoring a Plex webhook from a server that is not configured (or disabled)", "server", p.Server.Title)
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	scan := models.TargetedScanBody{ServerID: server.ID, RatingKeys: []string{rk}}
	s.queueWebhookScan(w, r, scan, "event", p.Event, "server", server.Name)
}

// appLabel is the display name of an *arr kind.
func appLabel(k models.ArrKind) string {
	if k == models.ArrSonarr {
		return "Sonarr"
	}
	return "Radarr"
}

// queueWebhookScan enqueues a TargetedScan with trigger "webhook" and answers, unless webhook
// scans are over their bounds (see maxQueuedWebhookScans).
func (s *Server) queueWebhookScan(w http.ResponseWriter, r *http.Request, scan models.TargetedScanBody, logAttrs ...any) {
	if errs := validateTargetedScan(scan); len(errs) > 0 {
		s.writeJSON(w, http.StatusOK, webhookResponse{})
		return
	}
	if s.d.Commands != nil {
		reason := ""
		switch {
		case s.d.Commands.CountQueued(models.CmdTargetedScan, models.TriggerWebhook) >= maxQueuedWebhookScans:
			reason = "too many webhook scans are waiting"
		case !s.webhookScans.allow():
			reason = "webhooks are arriving faster than scans are allowed"
		}
		if reason != "" {
			attrs := append([]any{"reason", reason}, logAttrs...)
			if s.webhookScans.warnDue() {
				s.log.Warn("A webhook scan was not queued; the next scheduled scan covers the change", attrs...)
			} else {
				s.log.Debug("A webhook scan was not queued", attrs...)
			}
			// 200: an *arr must not report the connection as failing for a skipped shortcut.
			s.writeJSON(w, http.StatusOK, webhookResponse{Reason: reason + "; the next scheduled scan covers the change"})
			return
		}
	}
	cmd, err := s.enqueue(r.Context(), models.CmdTargetedScan, scan, models.TriggerWebhook)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Webhook queued a targeted scan", append([]any{"commandId", cmd.ID}, logAttrs...)...)
	s.writeJSON(w, http.StatusOK, webhookResponse{Queued: true, CommandID: cmd.ID})
}
