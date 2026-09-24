package plex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestClientCannotEmptyTrash: docs/DECISIONS.md D6 forbids section emptyTrash (if a share is
// offline or a scan is running, it wipes those items and their watch state from Plex). The client
// therefore has no method for it and no code of the package builds the endpoint, so no caller can
// reach it by accident.
func TestClientCannotEmptyTrash(t *testing.T) {
	typ := reflect.TypeOf(&Client{})
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; strings.Contains(strings.ToLower(name), "trash") {
			t.Errorf("Client.%s exists; Dupearr must never empty Plex's trash (DECISIONS D6)", name)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("list package sources: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(b)), "emptytrash") {
			t.Errorf("%s mentions emptyTrash; Dupearr must never call it (DECISIONS D6)", f)
		}
	}
}
