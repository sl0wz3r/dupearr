package arr

import (
	"errors"
	"fmt"
	"testing"
)

// TestTransportCauseOfAPeerThatSpeaksFirst: see notifications.TestTransportErrorOfAPeerThatSpeaksFirst
// — a peer that sends bytes before the request is not an HTTP server.
func TestTransportCauseOfAPeerThatSpeaksFirst(t *testing.T) {
	if err := transportCause(fmt.Errorf("readLoopPeekFailLocked: %w", error(nil))); !errors.Is(err, errNotHTTP) {
		t.Fatalf("err = %v, want errNotHTTP", err)
	}
	if err := transportCause(errors.New("connection refused")); errors.Is(err, errNotHTTP) {
		t.Fatalf("an ordinary error was classified as not HTTP: %v", err)
	}
}
