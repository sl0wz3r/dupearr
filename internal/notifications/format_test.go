package notifications

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestEventType(t *testing.T) {
	tests := map[string]string{
		models.OnDuplicatesFound: "DuplicatesFound",
		models.OnFileDeleted:     "FileDeleted",
		models.OnDeleteFailed:    "DeleteFailed",
		models.OnScanCompleted:   "ScanCompleted",
		models.OnHealthIssue:     "HealthIssue",
		models.OnHealthRestored:  "HealthRestored",
		EventTest:                "Test",
		"":                       "Unknown",
		"online":                 "Online",
		"custom":                 "Custom",
	}
	for in, want := range tests {
		if got := eventType(in); got != want {
			t.Errorf("eventType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSeverity(t *testing.T) {
	tests := map[string]string{"": "info", "INFO": "info", "Warn": "warning", "warning": "warning",
		"error": "error", "failure": "error", "bogus": "info"}
	for in, want := range tests {
		if got := normalizeSeverity(in); got != want {
			t.Errorf("normalizeSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 6, "hello…"},
		{"héllo wörld", 8, "héllo w…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.max); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
	long := strings.Repeat("é", 5000)
	if got := truncateBytes(long, 100); len(got) > 100 || !utf8.ValidString(got) {
		t.Errorf("truncateBytes produced %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got := truncateBytes("short", 100); got != "short" {
		t.Errorf("truncateBytes changed short input: %q", got)
	}
}

func TestNormalizeMessage(t *testing.T) {
	in := Message{
		Event:    " onFileDeleted ",
		Title:    "  multi\nline\x00 title ",
		Body:     "line1\r\nline2\x07",
		Fields:   []Field{{Name: "", Value: ""}, {Name: "A\tB", Value: " v "}},
		URL:      " /relative ",
		Severity: "WARN",
	}
	orig := in.Fields[1]
	got := normalizeMessage(in)
	if got.Event != "onFileDeleted" || got.Title != "multi line title" || got.Body != "line1\nline2" {
		t.Fatalf("got %+v", got)
	}
	if got.Severity != SeverityWarning || len(got.Fields) != 1 || got.Fields[0].Name != "A B" || got.Fields[0].Value != "v" {
		t.Fatalf("got %+v", got)
	}
	if in.Fields[1] != orig {
		t.Fatal("caller's fields mutated")
	}
	if got.absURL() != "" {
		t.Fatal("relative URL must not be used as a link")
	}
	if normalizeMessage(Message{}).Title != defaultTitle {
		t.Fatal("empty title not defaulted")
	}
	abs := Message{URL: "https://x.example/d/1"}
	if abs.absURL() != abs.URL {
		t.Fatal("absolute URL dropped")
	}
}

func TestPlainText(t *testing.T) {
	m := normalizeMessage(sampleMessage())
	got := plainText(m)
	want := "Scan of \"Movies\" found 3 new duplicate groups.\n\nLibrary: Movies\nReclaimable: 42.1 GB\n\nhttps://dupearr.example.com/duplicates?status=pending"
	if got != want {
		t.Fatalf("plainText =\n%s\nwant\n%s", got, want)
	}
	if plainText(Message{Title: "only title"}) != "only title" {
		t.Fatal("empty message should fall back to the title")
	}
}

func TestRedact(t *testing.T) {
	got := redact("token abc/def and abc%2Fdef and x", []string{"abc/def", "x", ""})
	if strings.Contains(got, "abc") || !strings.Contains(got, "and x") {
		t.Fatalf("redact = %q", got)
	}
}
