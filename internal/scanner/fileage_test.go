package scanner

import (
	"go/build"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileChangeTimeCoversEveryPlatform: exactly one fileage_*.go file provides fileChangeTime on
// every platform. A file name ending in _<GOOS>.go is an implicit build constraint that overrides
// its //go:build line (fileage_linux.go listing openbsd, dragonfly, solaris and illumos left them
// without fileChangeTime, so the scanner did not compile there).
func TestFileChangeTimeCoversEveryPlatform(t *testing.T) {
	files, err := filepath.Glob("fileage_*.go")
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			sources = append(sources, f)
		}
	}
	if len(sources) == 0 {
		t.Fatal("no fileage_*.go files")
	}
	platforms := []struct{ goos, goarch string }{
		{"aix", "ppc64"}, {"android", "arm64"}, {"darwin", "arm64"}, {"dragonfly", "amd64"},
		{"freebsd", "amd64"}, {"illumos", "amd64"}, {"ios", "arm64"}, {"js", "wasm"},
		{"linux", "amd64"}, {"linux", "arm"}, {"netbsd", "amd64"}, {"openbsd", "amd64"},
		{"plan9", "amd64"}, {"solaris", "amd64"}, {"wasip1", "wasm"}, {"windows", "amd64"},
	}
	for _, p := range platforms {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH = p.goos, p.goarch
		var matched []string
		for _, f := range sources {
			ok, err := ctx.MatchFile(".", f)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if ok {
				matched = append(matched, f)
			}
		}
		if len(matched) != 1 {
			t.Errorf("%s/%s: fileChangeTime is provided by %v, want exactly one file", p.goos, p.goarch, matched)
		}
	}
}
