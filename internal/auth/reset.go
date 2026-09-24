package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/store"
)

// ResetStoredCredentials replaces the credentials Dupearr keeps in its database, for
// `dupearr reset-auth` (run while Dupearr is stopped): a new session signing key and a
// new webhook token, a new session generation and an empty revocation list. Every session and
// device cookie stops working, and so do webhook URLs holding the old token. The account itself
// and config.xml (the API key) are the caller's business.
func ResetStoredCredentials(ctx context.Context, st store.Store) error {
	if _, err := newSessionKey(ctx, st); err != nil {
		return err
	}
	if err := st.Settings().SetValue(ctx, webhookTokenSetting, newWebhookToken()); err != nil {
		return fmt.Errorf("auth: save webhook token: %w", err)
	}
	gen, err := loadGeneration(ctx, st)
	if err != nil {
		return err
	}
	if err := st.Settings().SetValue(ctx, sessionGenerationSetting, strconv.FormatInt(gen+1, 10)); err != nil {
		return fmt.Errorf("auth: save session generation: %w", err)
	}
	if err := st.Settings().SetValue(ctx, revokedSessionsSetting, "{}"); err != nil {
		return fmt.Errorf("auth: clear session revocations: %w", err)
	}
	v, _, err := st.Settings().GetValue(ctx, webhookTokenSetting)
	if err != nil || strings.TrimSpace(v) == "" {
		return fmt.Errorf("auth: the webhook token was not saved: %v", err)
	}
	return nil
}
