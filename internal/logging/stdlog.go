package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Standard library logger bridge (docs/DECISIONS.md "Security review decisions": connection tests
// never echo non-HTTP banners of free-form URLs, at any log level).
//
// A few standard-library packages report through the global "log" logger, which writes to stderr
// (the container log) unless redirected. net/http is the one that matters: when a peer sends bytes
// before a request was sent — the banner of an SMTP, FTP or SSH service behind a free-form URL — its
// transport prints "Unsolicited response received on idle HTTP channel starting with %q", quoting
// those bytes. StdLogWriter routes the global logger into the application log and drops the quoted
// bytes of such lines.

// unsolicitedPrefix starts net/http's report of bytes a peer sent on an idle connection.
const unsolicitedPrefix = "Unsolicited response received on idle HTTP channel"

// StdLogWriter returns a writer for log.SetOutput that forwards each line of the standard library
// logger to l (warning level), except net/http's unsolicited-response reports, which are logged at
// debug level without the peer's bytes.
func StdLogWriter(l *slog.Logger) io.Writer { return stdLogWriter{l: l} }

type stdLogWriter struct{ l *slog.Logger }

func (w stdLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.Contains(line, unsolicitedPrefix):
			w.l.Debug("An HTTP server sent data before the request (not HTTP?); its bytes are not logged", "component", "net/http")
		default:
			w.l.Warn(capText(line, maxMessageBytes), "component", "stdlib")
		}
	}
	return len(p), nil
}
