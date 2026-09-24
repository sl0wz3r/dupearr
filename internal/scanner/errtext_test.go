package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
)

// TestScanFailureTextIsSanitized: the error of a failed scan is stored (scan run), published
// (history, SSE) and sent to third-party notification connections. Upstream-controlled text in it
// (a hostile or broken media server's answer) must not carry secrets, control characters (log or
// terminal injection, bidi spoofing) or unbounded length to any of them.
func TestScanFailureTextIsSanitized(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []notifications.WebhookPayload
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p notifications.WebhookPayload
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}))
	defer hook.Close()
	settings, _ := json.Marshal(map[string]string{"url": hook.URL})
	cfg := models.NotificationConfig{Name: "hook", Kind: notifications.KindWebhook, Settings: settings,
		Triggers: []string{models.OnScanCompleted}, Enabled: true}
	if err := h.db.Notifications().Create(h.ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	n := notifications.New(h.db, slog.New(slog.DiscardHandler))
	deps := h.deps
	deps.Notifier = n
	svc := New(deps)

	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	const secret = "sEcReTpLeXtOkEn0123456789"
	px.setListErr("1", fmt.Errorf("plex: GET /library/sections/1/all?X-Plex-Token=%s: HTTP 500:\n\x1b[2J\u202e<b>%s</b>",
		secret, strings.Repeat("upstream text ", 500)))

	run, err := svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err == nil || run == nil || run.Status != runFailed {
		t.Fatalf("run=%+v err=%v, want a failed scan", run, err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	if err := n.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	stored, err := h.db.ScanRuns().Latest(h.ctx)
	if err != nil || stored.ID != run.ID {
		t.Fatalf("latest scan run = %+v, %v", stored, err)
	}
	hist := h.history(models.EventScanFailed)
	if len(hist) != 1 {
		t.Fatalf("scanFailed history = %+v", hist)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("notifications = %+v, want one scan-failed message", got)
	}
	for name, text := range map[string]string{
		"returned run error": run.Error,
		"stored run error":   stored.Error,
		"history message":    hist[0].Message,
		"history data":       string(hist[0].Data),
		"notification body":  got[0].Message,
	} {
		if strings.Contains(text, secret) {
			t.Errorf("%s leaks the token: %q", name, text)
		}
		if name == "history data" {
			continue // JSON: escapes control characters itself; only the secret matters here
		}
		if text == "" || !strings.Contains(text, "could not list") {
			t.Errorf("%s = %q, want the (sanitized) reason", name, text)
		}
		if n := utf8.RuneCountInString(text); n > 320 {
			t.Errorf("%s is %d runes long, want it bounded", name, n)
		}
		for _, r := range text {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Errorf("%s contains the control/format character %U: %q", name, r, text)
				break
			}
		}
	}
}

func TestErrorText(t *testing.T) {
	long := strings.Repeat("é", maxErrorRunes+50)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", fmt.Errorf("plex: GET /library/sections/1/all: HTTP 500"), "plex: GET /library/sections/1/all: HTTP 500"},
		{"white space and controls collapse", fmt.Errorf("  a\r\n\tb\x00\x1b[31m c  "), "a b [31m c"},
		{"format characters dropped", fmt.Errorf("evil\u202egnp.exe\u200b"), "evilgnp.exe"},
		{"api key redacted", fmt.Errorf("GET /api/v3/movie?apikey=0123456789abcdef&x=1 refused"), "GET /api/v3/movie?apikey=(removed)&x=1 refused"},
		{"bounded", fmt.Errorf("%s", long), strings.Repeat("é", maxErrorRunes) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorText(tc.err); got != tc.want {
				t.Fatalf("errorText = %q, want %q", got, tc.want)
			}
		})
	}
}
