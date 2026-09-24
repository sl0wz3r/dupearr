package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestTransportErrorOfAPeerThatSpeaksFirst: a non-HTTP service (SMTP, FTP, SSH …) sends its banner
// as soon as the connection opens. When those bytes arrive before net/http marks the request as
// sent, the transport fails with "readLoopPeekFailLocked" instead of "malformed HTTP …" (a timing
// race, e.g. under load). An HTTP server never speaks first: this is a non-HTTP peer too.
func TestTransportErrorOfAPeerThatSpeaksFirst(t *testing.T) {
	err := transportError(context.Background(), "POST", "127.0.0.1:1", fmt.Errorf("readLoopPeekFailLocked: %w", error(nil)))
	if !strings.Contains(err.Error(), "not HTTP") || strings.Contains(err.Error(), "readLoopPeekFailLocked") {
		t.Fatalf("err = %v, want the not-HTTP hint", err)
	}
	if err := transportError(context.Background(), "POST", "h", errors.New("connection refused")); strings.Contains(err.Error(), "not HTTP") {
		t.Fatalf("an ordinary error was classified as not HTTP: %v", err)
	}
}
