package notifications

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestMaskHeaders(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"credential headers", "Authorization: Bearer abc\nX-Api-Key: k", "Authorization: ********\nX-Api-Key: ********"},
		{"benign headers kept (any case)", "accept: application/json\nContent-Type: text/plain", "accept: application/json\nContent-Type: text/plain"},
		{"custom header masked by default", "X-Source: dupearr", "X-Source: ********"},
		{"commented-out header masked", "#Authorization: Bearer old", "# Authorization: ********"},
		{"prose comment kept", "# headers for n8n", "# headers for n8n"},
		{"blank lines and empty values kept", "\nX-Empty:\n\n", "\nX-Empty:\n\n"},
		{"malformed line hidden whole", "Bearer abc123", MaskedValue},
		{"already masked is idempotent", "X-Key: ********", "X-Key: ********"},
		{"crlf", "X-Key: v\r\nAccept: */*\r\n", "X-Key: ********\nAccept: */*\r\n"},
		{"value with colons", "X-Sig: a:b:c", "X-Sig: ********"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskHeaders(tt.in); got != tt.want {
				t.Fatalf("maskHeaders(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMergeHeaders(t *testing.T) {
	stored := "Authorization: Bearer tok\nX-Key: first\nX-Key: second\nAccept: */*\n# X-Old: old-secret"
	tests := []struct {
		name, in, want string
	}{
		{"round trip", maskHeaders(stored), "Authorization: Bearer tok\nX-Key: first\nX-Key: second\nAccept: */*\n# X-Old: old-secret"},
		{"name case-insensitive, spelling kept", "authorization: ********", "authorization: Bearer tok"},
		{"occurrence kept when an earlier one is edited", "X-Key: changed\nX-Key: ********", "X-Key: changed\nX-Key: second"},
		{"new header name stays masked", "X-New: ********", "X-New: ********"},
		{"more occurrences than stored stay masked", "X-Key: ********\nX-Key: ********\nX-Key: ********", "X-Key: first\nX-Key: second\nX-Key: ********"},
		{"comment never restores an active header", "# Authorization: ********", "# Authorization: ********"},
		{"active header never takes a commented value", "X-Old: ********", "X-Old: ********"},
		{"edits and additions kept", "Authorization: ********\nX-Extra: 1", "Authorization: Bearer tok\nX-Extra: 1"},
		{"removed lines stay removed", "Accept: */*", "Accept: */*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeHeaders(tt.in, stored); got != tt.want {
				t.Fatalf("mergeHeaders(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseHeadersRejectsMasks(t *testing.T) {
	for _, in := range []string{"X-Key: ********", MaskedValue, "Accept: */*\n  ********  "} {
		_, err := parseHeaders(in)
		if err == nil || !strings.Contains(err.Error(), "hidden") {
			t.Errorf("parseHeaders(%q) = %v, want a 'hidden' error", in, err)
		}
	}
	if _, err := parseHeaders("# X-Key: ********\nX-Ok: 1"); err != nil {
		t.Errorf("masked comment rejected: %v", err)
	}
}

func TestHeaderSecrets(t *testing.T) {
	got := strings.Join(headerSecrets("Authorization: Bearer tok-1\n# X-Old: old-2\nnot a header line\n# prose\nAccept: */*"), "|")
	for _, want := range []string{"Bearer tok-1", "tok-1", "old-2", "not a header line"} {
		if !strings.Contains(got, want) {
			t.Errorf("headerSecrets = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "prose") || strings.Contains(got, "*/*") {
		t.Errorf("prose comment or benign header collected: %q", got)
	}
}

// TestWebhookMaskedHeadersEndToEnd follows an edit through the API flow: GET (MaskSecrets), PUT
// with the masks unchanged (MergeSecrets + ValidateConfig), then delivery with the real values.
func TestWebhookMaskedHeadersEndToEnd(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, _, _ := newTestService(t)
	stored := newConfig(KindWebhook, map[string]any{
		"url": srv.URL + "/hook", "headers": "X-Api-Key: key-SECRET\nAccept: application/json",
	}, models.OnFileDeleted)

	masked := MaskSecrets(stored)
	if strings.Contains(string(masked.Settings), "key-SECRET") {
		t.Fatalf("API response leaks a header value: %s", masked.Settings)
	}
	merged := MergeSecrets(masked, stored)
	if errs := ValidateConfig(merged); errs != nil {
		t.Fatalf("merged config invalid: %+v", errs)
	}
	if err := s.Test(context.Background(), merged); err != nil {
		t.Fatal(err)
	}
	r := rec.only(t)
	if r.Header.Get("X-Api-Key") != "key-SECRET" || r.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", r.Header)
	}

	// A mask that cannot be restored is never sent.
	orphan := newConfig(KindWebhook, map[string]any{"url": srv.URL + "/hook", "headers": "X-New: ********"})
	err := s.Test(context.Background(), MergeSecrets(orphan, stored))
	var vErr *ValidationFailedError
	if !errors.As(err, &vErr) || vErr.Errors[0].PropertyName != "headers" {
		t.Fatalf("Test = %v", err)
	}
	if len(rec.requests()) != 1 {
		t.Fatal("masked header sent")
	}
}
