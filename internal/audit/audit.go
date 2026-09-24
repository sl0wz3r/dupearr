// Package audit records security-relevant events (docs/SECURITY.md GAP-12) in the durable
// history (Activity → History, event type models.EventSecurity): sign-ins and failed sign-ins,
// first-run setup and authentication resets, credential, API-key, webhook-token and session
// changes, changes of the authentication, deletion, disc-removal, logging and connection settings,
// backup downloads and restores.
//
// The records do not depend on the log level (a session that turns logging down to "error"
// cannot hide what it does next), they survive restarts, and they are kept for at least
// models.SecurityHistoryRetentionDays whatever the history retention is set to. A record never
// holds a secret (passwords, keys, tokens, URLs with credentials): callers pass names and flags.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Kinds (HistoryEvent data "kind").
const (
	KindSetup               = "setup"
	KindAuthReset           = "authReset"
	KindLogin               = "login"
	KindLoginFailed         = "loginFailed"
	KindCredentialsChanged  = "credentialsChanged"
	KindAPIKeyChanged       = "apiKeyChanged"
	KindAPIKeyRevealed      = "apiKeyRevealed"
	KindWebhookTokenChanged = "webhookTokenChanged"
	KindSessionsRevoked     = "sessionsRevoked"
	KindHostSettings        = "hostSettingsChanged"
	KindLogLevelLowered     = "logLevelLowered"
	KindRemovalSettings     = "removalSettingsChanged"
	KindConnectionChanged   = "connectionChanged"
	KindBackupDownloaded    = "backupDownloaded"
	KindRestoreConfirmed    = "restoreConfirmed"
)

// writeTimeout bounds one record (it is written even when the request that caused it has ended).
const writeTimeout = 5 * time.Second

// maxValue bounds one attribute value in a record.
const maxValue = 200

// Record writes a security event: title is the one-line summary shown in Activity → History;
// attrs are key/value pairs (like slog) stored in the event's data and listed in its message.
// A failure is logged at Error (never hidden by the log level) and otherwise ignored: the action
// that caused the event has happened already. st may be nil (nothing is recorded).
func Record(ctx context.Context, st store.Store, log *slog.Logger, kind, title string, attrs ...any) {
	if st == nil {
		return
	}
	data := map[string]any{"kind": kind}
	var parts []string
	for i := 0; i+1 < len(attrs); i += 2 {
		k, ok := attrs[i].(string)
		if !ok || k == "" || k == "kind" {
			continue
		}
		v := bound(fmt.Sprint(attrs[i+1]))
		data[k] = v
		parts = append(parts, k+"="+v)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		raw = nil
	}
	e := &models.HistoryEvent{EventType: models.EventSecurity, Title: bound(title), Message: strings.Join(parts, ", "), Data: raw}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := st.History().Add(wctx, e); err != nil && log != nil {
		log.Error("Could not record a security event in the history", "kind", kind, "error", err)
	}
}

// bound shortens s to maxValue characters and replaces control characters.
func bound(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxValue {
		s = string(r[:maxValue]) + "…"
	}
	return s
}
