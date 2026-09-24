package logging

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Secret-bearing shapes other packages are likely to log (models.MediaServer, ArrInstance, …).
type testServer struct {
	ID    int64
	Name  string
	URL   string
	Token string `json:"token"`
}

type testNotification struct {
	Name     string
	Settings map[string]any
	Inner    *testServer
	Hidden   string `json:"apiKey"` // secret by its JSON name only
	private  string
	Empty    string `json:"password"`
}

type cyclic struct {
	Name string
	Next *cyclic
}

type panicStringer struct{}

func (panicStringer) String() string { panic("boom") }

func TestHandlerMasksStructuredValues(t *testing.T) {
	t.Parallel()
	const secret = "STRUCTSECRET42"
	tl := newTestLog(t, "info", 1<<20)
	u, _ := url.Parse("http://radarr:7878/api/v3/movie?apikey=" + secret)
	tl.log.Info("values",
		"server", testServer{ID: 1, Name: "plex", URL: "http://plex:32400", Token: secret},
		"ptr", &testServer{Name: "ptr", Token: secret},
		"list", []testServer{{Name: "a", Token: secret}},
		"notification", testNotification{
			Name:     "discord",
			Settings: map[string]any{"webhookUrl": "https://example.com/hook/" + secret, "botToken": secret, "chatId": 42},
			Inner:    &testServer{Token: secret},
			Hidden:   secret,
			private:  secret,
		},
		"headers", http.Header{"X-Plex-Token": {secret}, "X-Gotify-Key": {secret}, "Accept": {"application/json"}},
		"urlValue", *u,
		"urlPtr", u,
		"userinfo", url.URL{Scheme: "smtp", User: url.UserPassword("bob", secret), Host: "mail:587"},
		"escapedBody", fmt.Sprintf("%q", `{"authToken":"`+secret+`"}`),
	)
	tl.log.Info(fmt.Sprintf("formatted %+v", testServer{Name: "fmt", Token: secret}))

	file := tl.file(t, FileName)
	for name, out := range map[string]string{"file": file, "stdout": tl.out.String(), "entry": tl.recent("")[0].Message + tl.recent("")[1].Message} {
		if strings.Contains(out, secret) {
			t.Errorf("%s leaks the secret:\n%s", name, out)
		}
	}
	for _, want := range []string{
		`server="{ID:1 Name:plex URL:http://plex:32400 Token:(removed)}"`,
		`ptr="&{ID:0 Name:ptr URL: Token:(removed)}"`,
		`list="[{ID:0 Name:a URL: Token:(removed)}]"`,
		`Hidden:(removed)`,
		`Empty:}`, // an empty secret stays visibly empty
		`Accept:[application/json]`,
		`urlValue="http://radarr:7878/api/v3/movie?apikey=(removed)"`,
		`urlPtr="http://radarr:7878/api/v3/movie?apikey=(removed)"`,
		`headers="map[Accept:[application/json] X-Gotify-Key:(removed) X-Plex-Token:(removed)]"`,
		`Settings:map[botToken:(removed) chatId:42 webhookUrl:(removed)]`,
		`userinfo=smtp://bob:(removed)@mail:587`,
		`|formatted {ID:0 Name:fmt URL: Token:(removed)}`,
	} {
		if !strings.Contains(file, want) {
			t.Errorf("file lacks %q:\n%s", want, file)
		}
	}
	if strings.Contains(file, "private") {
		t.Errorf("unexported field rendered:\n%s", file)
	}
}

func TestRenderAny(t *testing.T) {
	t.Parallel()
	loop := &cyclic{Name: "a"}
	loop.Next = loop
	many := make([]int, 1000)
	var nilServer *testServer
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "<nil>"},
		{"typed nil pointer", nilServer, "<nil>"},
		{"typed nil error", (*os.PathError)(nil), "<nil>"},
		{"error", errors.New("boom"), "boom"},
		{"bytes", []byte("raw"), "raw"},
		{"strings", []string{"a", "b"}, "[a b]"},
		{"nil slice", []string(nil), "[]"},
		{"map sorted", map[string]int{"b": 2, "a": 1}, "map[a:1 b:2]"},
		{"secret map key", map[string]string{"apiKey": "k", "name": "n"}, "map[apiKey:(removed) name:n]"},
		{"func", func() {}, "func()"},
		{"chan", make(chan int), "chan int"},
		{"panicking stringer", panicStringer{}, "%!v(PANIC=String method: boom)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderAny(tt.in); got != tt.want {
				t.Fatalf("renderAny() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := renderAny(loop); !strings.HasPrefix(got, "&{Name:a Next:{Name:a") || !strings.Contains(got, "…") || len(got) > 200 {
		t.Errorf("cyclic value = %q", got)
	}
	if got := renderAny(many); !strings.HasSuffix(got, " 0 …]") || strings.Count(got, " ") != maxAnyItems {
		t.Errorf("long slice = %q", got)
	}
	if got := renderAny(strings.Repeat("x", 3*maxAnyBytes)); len(got) != 3*maxAnyBytes {
		t.Errorf("a plain string is not truncated by renderAny (len %d)", len(got))
	}
	big := make([]string, 50)
	for i := range big {
		big[i] = strings.Repeat("y", 1024)
	}
	if got := renderAny(big); len(got) > maxAnyBytes+4*1024 || !strings.HasSuffix(got, "…]") {
		t.Errorf("large value not truncated: len %d", len(got))
	}
}

func TestRedactHardening(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in, want string }{
		{"escaped json", `body "{\"authToken\":\"abc\",\"user\":\"bob\"}"`, `body "{\"authToken\":\"(removed)\",\"user\":\"bob\"}"`},
		{"struct dump", "{ID:1 Name:plex Token:abc123 URL:http://x}", "{ID:1 Name:plex Token:(removed) URL:http://x}"},
		{"map dump", "map[apiKey:abc name:x]", "map[apiKey:(removed) name:x]"},
		{"gotify header", "X-Gotify-Key: abc", "X-Gotify-Key: (removed)"},
		{"gotify header dump", "map[X-Gotify-Key:[abc]]", "map[X-Gotify-Key:[(removed)]]"},
		{"custom x token header", "X-Auth-Token=abc", "X-Auth-Token=(removed)"},
		{"quoted pair", `"password=hunter2"`, `"password=(removed)"`},
		{"raw query field", "RawQuery:apikey=abc", "RawQuery:apikey=(removed)"},
		{"prose after colon kept", "token: expired, retrying", "token: expired, retrying"},
		{"plex client identifier kept", "X-Plex-Client-Identifier: abc", "X-Plex-Client-Identifier: abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			if got != tt.want {
				t.Fatalf("Redact(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
			if again := Redact(got); again != got {
				t.Fatalf("not idempotent: %q → %q", got, again)
			}
		})
	}
}

func TestSensitiveKeyHeadersAndProviderFields(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]bool{
		"X-Gotify-Key": true, "x-custom-key": true, "userKey": true, "privateKey": true,
		"webhookUrl": true, "discordWebhookURL": true, "botToken": true,
		"keyboard": false, "x-forwarded-for": false, "monkey": false, "sslKeyPath": false, "chatId": false,
	} {
		if got := sensitiveKey(key); got != want {
			t.Errorf("sensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestComponentAndSeparatorsAreSanitised(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	tl.log.With("component", "Plex|x\ty?X-Plex-Token=abc").Info("a\u2028b\u2029c")
	file := tl.file(t, FileName)
	if strings.Contains(file, "abc") {
		t.Fatalf("component leaks a secret:\n%s", file)
	}
	if !strings.Contains(file, "|Plex_x_y?X-Plex-Token=(removed)|a\\u2028b\\u2029c\n") {
		t.Fatalf("file = %q", file)
	}
}

func TestSetupRefusesNonRegularLogFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			logs := filepath.Join(root, "logs")
			if err := os.MkdirAll(logs, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "movie.mkv")
			if err := os.WriteFile(target, []byte("MEDIA"), 0o644); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "symlink" {
				err = os.Symlink(target, filepath.Join(logs, FileName))
			} else {
				err = os.Mkdir(filepath.Join(logs, FileName), 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, m, err := setup(logs, "info", 1<<20, &syncBuffer{}); err == nil {
				_ = m.Close()
				t.Fatal("setup accepted a non-regular dupearr.txt")
			}
			if b, _ := os.ReadFile(target); string(b) != "MEDIA" {
				t.Fatalf("symlink target modified: %q", b)
			}
		})
	}
}

func TestRotatorReopensButNeverThroughSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	r, err := openRotator(dir, 1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Simulate a rotation whose reopen failed, then a symlink planted at dupearr.txt.
	_ = r.f.Close()
	r.f = nil
	active := filepath.Join(dir, FileName)
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, active); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("line\n")); err == nil {
		t.Fatal("Write went through a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "KEEP" {
		t.Fatalf("symlink target modified: %q", b)
	}

	// Once the name is sane again, file logging recovers without a restart.
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("recovered\n")); err != nil {
		t.Fatalf("Write after recovery: %v", err)
	}
	if b, _ := os.ReadFile(active); string(b) != "recovered\n" {
		t.Fatalf("dupearr.txt = %q", b)
	}
}

func TestSetSizeLimit(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	for _, tc := range []struct {
		mb   int
		want int64
	}{{5, 5 << 20}, {0, 1 << 20}, {-3, 1 << 20}, {1 << 30, int64(maxSizeLimitMB) << 20}} {
		tl.m.SetSizeLimit(tc.mb)
		tl.m.core.mu.Lock()
		got := tl.m.core.file.maxBytes
		tl.m.core.mu.Unlock()
		if got != tc.want {
			t.Errorf("SetSizeLimit(%d): maxBytes = %d, want %d", tc.mb, got, tc.want)
		}
	}

	// Applies to the next write: shrink to the (test-only) byte limit and see a rotation.
	tl.log.Info(strings.Repeat("a", 60))
	tl.m.core.mu.Lock()
	tl.m.core.file.setMaxBytes(100)
	tl.m.core.mu.Unlock()
	tl.log.Info(strings.Repeat("b", 60))
	if _, err := os.Stat(filepath.Join(tl.dir, "dupearr.0.txt")); err != nil {
		t.Fatalf("no rotation after lowering the limit: %v", err)
	}

	_ = tl.m.Close()
	tl.m.SetSizeLimit(3) // no-op after Close, must not panic
}
