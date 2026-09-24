package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
)

// Webhook token: the credential of the inbound webhooks (/api/v1/webhook/{radarr,sonarr,plex}).
//
// Webhook URLs end up stored in Radarr/Sonarr (returned unmasked by their API and kept in their
// backups), in the plex.tv account and in proxy access logs, so they must not carry the master
// API key. The webhook token is a separate random secret (settings value "auth.webhookToken")
// that is accepted only on the webhook routes — as the X-Api-Key header or the apikey query
// parameter — and can do nothing but queue the targeted scans those handlers queue. Every other
// route answers 401 to it. The master key is still accepted on the webhook routes for existing
// Connect entries, with a deprecation warning in the log.

const (
	webhookTokenSetting = "auth.webhookToken"
	webhookTokenBytes   = 16
)

// ViaWebhookToken is Info.Via for a webhook authenticated by the webhook token.
const ViaWebhookToken = "webhookToken"

func newWebhookToken() string {
	b := make([]byte, webhookTokenBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go ≥ 1.24)
	return hex.EncodeToString(b)
}

// loadWebhookToken reads the webhook token, generating and persisting one when missing or invalid.
func loadWebhookToken(ctx context.Context, s *Service) (string, error) {
	v, ok, err := s.st.Settings().GetValue(ctx, webhookTokenSetting)
	if err != nil {
		return "", fmt.Errorf("auth: read webhook token: %w", err)
	}
	if v = strings.TrimSpace(v); ok && len(v) == 2*webhookTokenBytes {
		if _, err := hex.DecodeString(v); err == nil {
			return v, nil
		}
	}
	tok := newWebhookToken()
	if err := s.st.Settings().SetValue(ctx, webhookTokenSetting, tok); err != nil {
		return "", fmt.Errorf("auth: save webhook token: %w", err)
	}
	return tok, nil
}

// WebhookToken returns the current webhook token.
func (s *Service) WebhookToken() string {
	s.webhookMu.RLock()
	defer s.webhookMu.RUnlock()
	return s.webhookToken
}

// RegenerateWebhookToken replaces the webhook token; webhook URLs holding the old one stop working.
func (s *Service) RegenerateWebhookToken(ctx context.Context) (string, error) {
	tok := newWebhookToken()
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	if err := s.st.Settings().SetValue(ctx, webhookTokenSetting, tok); err != nil {
		return "", fmt.Errorf("auth: save webhook token: %w", err)
	}
	s.webhookToken = tok
	s.log.Info("Webhook token regenerated")
	s.audit(ctx, audit.KindWebhookTokenChanged, "Webhook token regenerated")
	return tok, nil
}

// webhookCredential is the outcome of checking a webhook request's credential.
type webhookCredential int

const (
	webhookNone webhookCredential = iota
	webhookByToken
	webhookByMasterKey
)

// checkWebhook compares the request's X-Api-Key header (else apikey query parameter) with the
// webhook token and, for existing Connect entries, the master API key (constant time).
func (s *Service) checkWebhook(r *http.Request) webhookCredential {
	provided := r.Header.Get("X-Api-Key")
	if provided == "" {
		provided = r.URL.Query().Get("apikey")
	}
	if provided == "" {
		return webhookNone
	}
	tok := s.WebhookToken()
	byToken := tok != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(tok)) == 1
	master := s.cfg.Get().ApiKey
	byMaster := master != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(master)) == 1
	switch {
	case byToken:
		return webhookByToken
	case byMaster:
		return webhookByMasterKey
	}
	return webhookNone
}

// WebhookMasterKeyLastUsed returns when a webhook last authenticated with the master API key since
// start or the last API key change (zero: never). The WebhookApiKeyCheck health check reports it.
func (s *Service) WebhookMasterKeyLastUsed() time.Time {
	if ns := s.webhookMasterUsed.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// warnWebhookMasterKey logs (once as a warning, then at debug level) that a webhook still uses
// the master API key.
func (s *Service) warnWebhookMasterKey(r *http.Request) {
	s.webhookMasterUsed.Store(s.now().UnixNano())
	attrs := []any{"path", r.URL.Path}
	if s.webhookMasterWarned.CompareAndSwap(false, true) {
		s.log.Warn("A webhook authenticated with the master API key. Webhook URLs are stored in Radarr, Sonarr "+
			"and plex.tv: replace the key in them with the webhook token (Settings → Connections → Webhooks), "+
			"then regenerate the API key", attrs...)
		return
	}
	s.log.Debug("Webhook authenticated with the master API key", attrs...)
}
