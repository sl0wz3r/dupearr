package logging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r2-data-files#8: one upstream-controlled value (a Plex title) of several MiB must not be kept
// whole in the ring buffer, nor let a handful of lines rotate every earlier record out of the
// log files.
func TestLogEntriesAreBounded(t *testing.T) {
	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("First line: evidence")
	huge := strings.Repeat("A", 2<<20)
	for range 7 {
		tl.log.Info(huge, "title", huge, "error", errors.New(huge))
	}
	entries := tl.recent("")
	newest := entries[0]
	if n := len(newest.Message) + len(newest.Exception); n > 64<<10 {
		t.Fatalf("newest ring entry holds %d bytes", n)
	}
	if !strings.Contains(newest.Message, "truncated") {
		t.Errorf("a truncated value should say so: %.200s", newest.Message)
	}
	var all strings.Builder
	files, _ := filepath.Glob(filepath.Join(tl.dir, "dupearr*.txt"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	if !strings.Contains(all.String(), "First line: evidence") {
		t.Fatal("7 upstream-controlled lines rotated the earlier history out of every log file")
	}
}

func TestCapTextKeepsUTF8(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := capText(s, 5)
	if !strings.HasPrefix(got, "éé…(truncated 16 bytes)") {
		t.Fatalf("capText = %q", got)
	}
	if capText("short", 10) != "short" {
		t.Fatal("short strings must be kept")
	}
}
