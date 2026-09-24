package version

import "testing"

func TestUserAgent(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	tests := []struct {
		version string
		want    string
	}{
		{"0.1.0-dev", "Dupearr/0.1.0-dev"},
		{"1.2.3", "Dupearr/1.2.3"},
		{"", "Dupearr/"},
	}
	for _, tt := range tests {
		Version = tt.version
		if got := UserAgent(); got != tt.want {
			t.Errorf("UserAgent() with Version=%q = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestDefaults(t *testing.T) {
	if Version == "" {
		t.Error("Version must have a non-empty default")
	}
	if Commit == "" {
		t.Error("Commit must have a non-empty default")
	}
}
