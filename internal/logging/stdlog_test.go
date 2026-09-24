package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestStdLogWriterNeverLogsPeerBytes: net/http reports "Unsolicited response received on idle HTTP
// channel starting with %q" through the standard library logger, quoting what the peer sent — the
// banner of whatever service answers a free-form URL (connection tests). Such lines are logged
// without the peer's bytes; other standard-library lines reach the application log.
func TestStdLogWriterNeverLogsPeerBytes(t *testing.T) {
	var buf bytes.Buffer
	w := StdLogWriter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	line := "Unsolicited response received on idle HTTP channel starting with \"220-internal-service-banner v1.2.3 secret-ish=abc\\r\\n\"; err=<nil>\n"
	if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("write: %d %v", n, err)
	}
	if strings.Contains(buf.String(), "internal-service-banner") || strings.Contains(buf.String(), "secret-ish") {
		t.Fatalf("peer bytes logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "before the request") {
		t.Fatalf("the event was not logged: %q", buf.String())
	}
	buf.Reset()
	if _, err := w.Write([]byte("http2: something unusual happened\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "http2: something unusual happened") {
		t.Fatalf("an ordinary line was lost: %q", buf.String())
	}
}
