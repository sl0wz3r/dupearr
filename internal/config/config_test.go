package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef"

var hexKey = regexp.MustCompile(`^[0-9a-f]{32}$`)

// canonical renders the file Dupearr writes for c (all elements, canonical order).
func canonical(c Config) string {
	return "<Config>\n" +
		"  <BindAddress>" + c.BindAddress + "</BindAddress>\n" +
		"  <Port>" + itoa(c.Port) + "</Port>\n" +
		"  <UrlBase>" + c.UrlBase + "</UrlBase>\n" +
		"  <EnableSsl>" + tf(c.EnableSsl) + "</EnableSsl>\n" +
		"  <SslPort>" + itoa(c.SslPort) + "</SslPort>\n" +
		"  <SslCertPath>" + c.SslCertPath + "</SslCertPath>\n" +
		"  <SslKeyPath>" + c.SslKeyPath + "</SslKeyPath>\n" +
		"  <ApiKey>" + c.ApiKey + "</ApiKey>\n" +
		"  <AuthenticationMethod>" + c.AuthenticationMethod + "</AuthenticationMethod>\n" +
		"  <AuthenticationRequired>" + c.AuthenticationRequired + "</AuthenticationRequired>\n" +
		"  <TrustedProxies>" + c.TrustedProxies + "</TrustedProxies>\n" +
		"  <AllowedHosts>" + c.AllowedHosts + "</AllowedHosts>\n" +
		"  <LogLevel>" + c.LogLevel + "</LogLevel>\n" +
		"  <LogSizeLimit>" + itoa(c.LogSizeLimit) + "</LogSizeLimit>\n" +
		"  <InstanceName>" + c.InstanceName + "</InstanceName>\n" +
		"  <LaunchBrowser>" + tf(c.LaunchBrowser) + "</LaunchBrowser>\n" +
		"  <Branch>" + c.Branch + "</Branch>\n" +
		"</Config>\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

func tf(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func defaultWithKey() Config {
	c := Default()
	c.ApiKey = testKey
	return c
}

// mustLoad loads dir ignoring the process environment.
func mustLoad(t *testing.T, dir string) *Manager {
	t.Helper()
	m, err := load(dir, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func writeFile(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // umask may have masked the mode
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestDefault(t *testing.T) {
	t.Parallel()
	want := Config{
		BindAddress: "*", Port: 3873, SslPort: 9873,
		AuthenticationMethod: "Forms", AuthenticationRequired: "Enabled",
		LogLevel: "info", LogSizeLimit: 1, InstanceName: "Dupearr", Branch: "main",
	}
	if got := Default(); got != want {
		t.Fatalf("Default() = %+v, want %+v", got, want)
	}
	c := Default()
	c.ApiKey = GenerateAPIKey()
	if errs := Validate(c); errs != nil {
		t.Fatalf("Default()+key invalid: %v", errs)
	}
}

func TestGenerateAPIKey(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 100 {
		k := GenerateAPIKey()
		if !hexKey.MatchString(k) {
			t.Fatalf("GenerateAPIKey() = %q, want 32 lower-case hex", k)
		}
		if seen[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seen[k] = true
	}
}

func TestLoadCreatesFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "data") // Load creates the directory too
	m := mustLoad(t, dir)

	c := m.Get()
	if !hexKey.MatchString(c.ApiKey) {
		t.Fatalf("ApiKey = %q, want generated 32 hex", c.ApiKey)
	}
	want := Default()
	want.ApiKey = c.ApiKey
	if c != want {
		t.Fatalf("Get() = %+v, want %+v", c, want)
	}
	if got := readFile(t, m.Path()); got != canonical(want) {
		t.Fatalf("new config.xml:\n%s\nwant:\n%s", got, canonical(want))
	}
	if runtime.GOOS != "windows" {
		if mode := fileMode(t, m.Path()); mode != 0o600 {
			t.Fatalf("mode = %o, want 600", mode)
		}
	}
	if !filepath.IsAbs(m.Path()) || !filepath.IsAbs(m.DataDir()) || filepath.Dir(m.Path()) != m.DataDir() {
		t.Fatalf("Path %q / DataDir %q not absolute or inconsistent", m.Path(), m.DataDir())
	}
	if got := m.EnvOverrides(); got == nil || len(got) != 0 {
		t.Fatalf("EnvOverrides() = %#v, want empty non-nil", got)
	}

	// Loading again keeps the key and does not rewrite anything.
	m2 := mustLoad(t, dir)
	if m2.Get() != c {
		t.Fatalf("reload = %+v, want %+v", m2.Get(), c)
	}
}

func TestLoadRelativeDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	m := mustLoad(t, "rel")
	if !filepath.IsAbs(m.DataDir()) || filepath.Base(m.DataDir()) != "rel" {
		t.Fatalf("DataDir() = %q", m.DataDir())
	}
	if _, err := os.Stat(filepath.Join("rel", FileName)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEmptyDataDir(t *testing.T) {
	t.Parallel()
	if _, err := load("  ", nil); err == nil {
		t.Fatal("want error for empty data dir")
	}
}

func TestLoadCanonicalFileIsNotRewritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := defaultWithKey()
	c.Port, c.UrlBase, c.LogLevel, c.LaunchBrowser = 8080, "/dupearr", "debug", true
	p := writeFile(t, dir, canonical(c), 0o644)

	m := mustLoad(t, dir)
	if m.Get() != c {
		t.Fatalf("Get() = %+v, want %+v", m.Get(), c)
	}
	if runtime.GOOS != "windows" && fileMode(t, p) != 0o644 {
		t.Fatal("canonical file was rewritten")
	}
}

func TestLoadFillsMissingAndPreservesUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := `<?xml version="1.0" encoding="utf-8"?>
<!-- prolog comment -->
<Config>
  <Port>8080</Port>
  <SslCertPassword>hunter2</SslCertPassword>
  <!-- keep me -->
  <Nested attr="1"><Child>v &amp; w</Child></Nested>
  <ApiKey>` + testKey + `</ApiKey>
  <UpdateMechanism>Docker</UpdateMechanism>
</Config>
`
	p := writeFile(t, dir, in, 0o644)
	m := mustLoad(t, dir)

	want := defaultWithKey()
	want.Port = 8080
	if m.Get() != want {
		t.Fatalf("Get() = %+v, want %+v", m.Get(), want)
	}
	wantFile := `<Config>
  <Port>8080</Port>
  <SslCertPassword>hunter2</SslCertPassword>
  <!-- keep me -->
  <Nested attr="1"><Child>v &amp; w</Child></Nested>
  <ApiKey>` + testKey + `</ApiKey>
  <UpdateMechanism>Docker</UpdateMechanism>
  <BindAddress>*</BindAddress>
  <UrlBase></UrlBase>
  <EnableSsl>False</EnableSsl>
  <SslPort>9873</SslPort>
  <SslCertPath></SslCertPath>
  <SslKeyPath></SslKeyPath>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <TrustedProxies></TrustedProxies>
  <AllowedHosts></AllowedHosts>
  <LogLevel>info</LogLevel>
  <LogSizeLimit>1</LogSizeLimit>
  <InstanceName>Dupearr</InstanceName>
  <LaunchBrowser>False</LaunchBrowser>
  <Branch>main</Branch>
</Config>
`
	if got := readFile(t, p); got != wantFile {
		t.Fatalf("config.xml:\n%s\nwant:\n%s", got, wantFile)
	}
	if runtime.GOOS != "windows" && fileMode(t, p) != 0o600 {
		t.Fatalf("rewritten file mode = %o, want 600", fileMode(t, p))
	}

	// Unknown content survives an Update round trip, and a reload changes nothing.
	if _, err := m.Update(func(c *Config) { c.InstanceName = "Dupearr 4K" }); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if want := strings.Replace(wantFile, "<InstanceName>Dupearr</InstanceName>", "<InstanceName>Dupearr 4K</InstanceName>", 1); got != want {
		t.Fatalf("after Update:\n%s\nwant:\n%s", got, want)
	}
	if m2 := mustLoad(t, dir); m2.Get().InstanceName != "Dupearr 4K" || readFile(t, p) != got {
		t.Fatal("reload after Update changed the file or lost the value")
	}
}

func TestLoadDuplicateElements(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := "<Config><Port>1111</Port><Port>2222</Port><Foo>1</Foo><foo>2</foo><ApiKey>" + testKey +
		"</ApiKey><apikey>ffffffffffffffffffffffffffffffff</apikey></Config>"
	p := writeFile(t, dir, in, 0o600)
	m := mustLoad(t, dir)
	if c := m.Get(); c.Port != 1111 || c.ApiKey != testKey {
		t.Fatalf("first element must win: %+v", c)
	}
	got := readFile(t, p)
	for _, elem := range []string{"<Port>", "<Foo>", "<ApiKey>", "<BindAddress>"} {
		if n := strings.Count(strings.ToLower(got), strings.ToLower(elem)); n != 1 {
			t.Errorf("%s occurs %d times in:\n%s", elem, n, got)
		}
	}
	if strings.Contains(got, "<foo>2</foo>") || strings.Contains(got, "2222") {
		t.Errorf("duplicate kept:\n%s", got)
	}
	// Stable: a second load does not add anything.
	mustLoad(t, dir)
	if again := readFile(t, p); again != got {
		t.Fatalf("second load changed file:\n%s", again)
	}
}

func TestLoadCanonicalisesValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, elems string
		check       func(Config) bool
		wantInFile  string
	}{
		{"Basic migrates to Forms", "<AuthenticationMethod>Basic</AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthForms }, "<AuthenticationMethod>Forms</AuthenticationMethod>"},
		{"basic lower-case migrates", "<AuthenticationMethod>basic</AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthForms }, "<AuthenticationMethod>Forms</AuthenticationMethod>"},
		{"BASIC upper-case migrates", "<AuthenticationMethod> BASIC </AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthForms }, "<AuthenticationMethod>Forms</AuthenticationMethod>"},
		{"forms (Radarr lower-case)", "<AuthenticationMethod>forms</AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthForms }, "<AuthenticationMethod>Forms</AuthenticationMethod>"},
		{"none", "<AuthenticationMethod>none</AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthNone }, "<AuthenticationMethod>None</AuthenticationMethod>"},
		{"external", "<AuthenticationMethod>EXTERNAL</AuthenticationMethod>",
			func(c Config) bool { return c.AuthenticationMethod == AuthExternal }, "<AuthenticationMethod>External</AuthenticationMethod>"},
		{"auth required", "<AuthenticationRequired>disabledforlocaladdresses</AuthenticationRequired>",
			func(c Config) bool { return c.AuthenticationRequired == AuthRequiredDisabledForLocal },
			"<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>"},
		{"bool case", "<EnableSsl>false</EnableSsl><LaunchBrowser>true</LaunchBrowser>",
			func(c Config) bool { return c.LaunchBrowser && !c.EnableSsl }, "<LaunchBrowser>True</LaunchBrowser>"},
		{"log level", "<LogLevel>Warning</LogLevel>",
			func(c Config) bool { return c.LogLevel == "warn" }, "<LogLevel>warn</LogLevel>"},
		{"log level fatal", "<LogLevel>Fatal</LogLevel>",
			func(c Config) bool { return c.LogLevel == "error" }, "<LogLevel>error</LogLevel>"},
		{"url base", "<UrlBase> dupearr/ </UrlBase>",
			func(c Config) bool { return c.UrlBase == "/dupearr" }, "<UrlBase>/dupearr</UrlBase>"},
		{"element name case", "<port>8080</port>",
			func(c Config) bool { return c.Port == 8080 }, "<Port>8080</Port>"},
		{"blank values get defaults", "<Port></Port><BindAddress/><AuthenticationMethod>  </AuthenticationMethod><InstanceName></InstanceName>",
			func(c Config) bool {
				return c.Port == DefaultPort && c.BindAddress == "*" && c.AuthenticationMethod == AuthForms && c.InstanceName == "Dupearr"
			}, "<Port>3873</Port>"},
		{"branch lower-cased", "<Branch>Develop</Branch>",
			func(c Config) bool { return c.Branch == "develop" }, "<Branch>develop</Branch>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := writeFile(t, dir, "<Config><ApiKey>"+testKey+"</ApiKey>"+tt.elems+"</Config>", 0o600)
			m := mustLoad(t, dir)
			if !tt.check(m.Get()) {
				t.Fatalf("unexpected config %+v", m.Get())
			}
			if got := readFile(t, p); !strings.Contains(got, tt.wantInFile) {
				t.Fatalf("file lacks %q:\n%s", tt.wantInFile, got)
			}
		})
	}
}

func TestLoadRegeneratesBlankAPIKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := writeFile(t, dir, "<Config><ApiKey></ApiKey></Config>", 0o600)
	m := mustLoad(t, dir)
	k := m.Get().ApiKey
	if !hexKey.MatchString(k) {
		t.Fatalf("ApiKey = %q", k)
	}
	if got := readFile(t, p); strings.Count(got, "<ApiKey>") != 1 || !strings.Contains(got, "<ApiKey>"+k+"</ApiKey>") {
		t.Fatalf("file:\n%s", got)
	}
}

func TestLoadEmptyFileIsRecreated(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "  \n\t"} {
		dir := t.TempDir()
		p := writeFile(t, dir, content, 0o600)
		m := mustLoad(t, dir)
		c := m.Get()
		if !hexKey.MatchString(c.ApiKey) {
			t.Fatalf("ApiKey = %q", c.ApiKey)
		}
		if got := readFile(t, p); got != canonical(c) {
			t.Fatalf("file:\n%s", got)
		}
	}
}

func TestLoadRejectsInvalidFiles(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, content, wantErr string }{
		{"not xml", "this is not xml", "corrupt"},
		{"wrong root", "<Settings><Port>1</Port></Settings>", "root element is <Settings>"},
		{"no root", "<?xml version=\"1.0\"?><!-- only a comment -->", "no <Config> root"},
		{"text before root", "junk <Config></Config>", "unexpected text before"},
		{"unclosed root", "<Config><Port>8080</Port>", "corrupt"},
		{"unclosed element", "<Config><Port>8080</Config>", "corrupt"},
		{"text inside root", "<Config>junk<Port>8080</Port></Config>", "unexpected text"},
		{"bad int", "<Config><Port>eighty</Port></Config>", "<Port>"},
		{"bad bool", "<Config><EnableSsl>maybe</EnableSsl></Config>", "<EnableSsl>"},
		{"port out of range", "<Config><Port>70000</Port></Config>", "port: Must be between 1 and 65535"},
		{"unknown auth method", "<Config><AuthenticationMethod>Kerberos</AuthenticationMethod></Config>", "authenticationMethod"},
		{"ssl without cert", "<Config><EnableSsl>True</EnableSsl></Config>", "sslCertPath"},
		{"bad url base", "<Config><UrlBase>/a/../b</UrlBase></Config>", "urlBase"},
		{"bad api key", "<Config><ApiKey>short</ApiKey></Config>", "apiKey"},
		{"second root", "<Config></Config><Config><Port>1</Port></Config>", "unexpected content after </Config>"},
		{"text after root", "<Config></Config>junk", "unexpected text \"junk\" after </Config>"},
		{"unclosed element after root", "<Config></Config><Port>", "corrupt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := writeFile(t, dir, tt.content, 0o644)
			_, err := load(dir, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if got := readFile(t, p); got != tt.content {
				t.Fatalf("invalid file was modified:\n%s", got)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Fatalf("unexpected files left behind: %v", entries)
			}
		})
	}
}

func TestLoadValidationErrorIsInspectable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "<Config><Port>0</Port><LogLevel>loud</LogLevel></Config>", 0o600)
	_, err := load(dir, nil)
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("err = %v, want ValidationErrors", err)
	}
	props := []string{}
	for _, v := range verrs {
		props = append(props, v.PropertyName)
	}
	if !slices.Equal(props, []string{"port", "logLevel"}) {
		t.Fatalf("properties = %v", props)
	}
}

func TestLoadRejectsHugeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "<Config>"+strings.Repeat("<!-- x -->", maxFileSize/10+1)+"</Config>", 0o600)
	if _, err := load(dir, nil); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeURLBase(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":              "",
		"/":             "",
		"//":            "",
		"  ":            "",
		"dupearr":       "/dupearr",
		"/dupearr":      "/dupearr",
		"/dupearr/":     "/dupearr",
		"dupearr/":      "/dupearr",
		" /a/b/ ":       "/a/b",
		"///x///":       "/x",
		"/Dupe-Arr_1.~": "/Dupe-Arr_1.~",
	}
	for in, want := range tests {
		if got := NormalizeURLBase(in); got != want {
			t.Errorf("NormalizeURLBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*Config)
		wantProp []string // nil = valid
	}{
		{"defaults", func(*Config) {}, nil},
		{"bind ipv4", func(c *Config) { c.BindAddress = "192.168.1.10" }, nil},
		{"bind ipv6", func(c *Config) { c.BindAddress = "::" }, nil},
		{"bind blank means *", func(c *Config) { c.BindAddress = " " }, nil},
		{"bind hostname", func(c *Config) { c.BindAddress = "myhost" }, []string{"bindAddress"}},
		{"bind cidr", func(c *Config) { c.BindAddress = "10.0.0.0/8" }, []string{"bindAddress"}},
		{"port 0", func(c *Config) { c.Port = 0 }, []string{"port"}},
		{"port 65536", func(c *Config) { c.Port = 65536 }, []string{"port"}},
		{"port 1", func(c *Config) { c.Port = 1 }, nil},
		{"port 65535", func(c *Config) { c.Port = 65535 }, nil},
		{"ssl port negative", func(c *Config) { c.SslPort = -1 }, []string{"sslPort"}},
		{"ssl complete", func(c *Config) { c.EnableSsl, c.SslCertPath, c.SslKeyPath = true, "/c/cert.pem", "/c/key.pem" }, nil},
		{"ssl missing cert+key", func(c *Config) { c.EnableSsl = true }, []string{"sslCertPath", "sslKeyPath"}},
		{"ssl missing key", func(c *Config) { c.EnableSsl, c.SslCertPath = true, "/c/cert.pem" }, []string{"sslKeyPath"}},
		{"ssl same port", func(c *Config) {
			c.EnableSsl, c.SslCertPath, c.SslKeyPath, c.SslPort = true, "/c", "/k", c.Port
		}, []string{"sslPort"}},
		{"ssl paths without ssl ok", func(c *Config) { c.SslCertPath = "/c" }, nil},
		{"same ports without ssl ok", func(c *Config) { c.SslPort = c.Port }, nil},
		{"api key empty", func(c *Config) { c.ApiKey = "" }, []string{"apiKey"}},
		{"api key short", func(c *Config) { c.ApiKey = "abc" }, []string{"apiKey"}},
		{"api key custom ok", func(c *Config) { c.ApiKey = "My-Custom_Key-0123456789" }, nil},
		{"api key bad chars", func(c *Config) { c.ApiKey = "0123456789abcdef0123456789abcde&" }, []string{"apiKey"}},
		{"api key too long", func(c *Config) { c.ApiKey = strings.Repeat("a", 129) }, []string{"apiKey"}},
		{"auth none", func(c *Config) { c.AuthenticationMethod = "None" }, nil},
		{"auth external lower", func(c *Config) { c.AuthenticationMethod = "external" }, nil},
		{"auth basic normalises", func(c *Config) { c.AuthenticationMethod = "Basic" }, nil},
		{"auth empty", func(c *Config) { c.AuthenticationMethod = "" }, []string{"authenticationMethod"}},
		{"auth bogus", func(c *Config) { c.AuthenticationMethod = "Oauth" }, []string{"authenticationMethod"}},
		{"auth required local", func(c *Config) { c.AuthenticationRequired = "DisabledForLocalAddresses" }, nil},
		{"auth required bogus", func(c *Config) { c.AuthenticationRequired = "Disabled" }, []string{"authenticationRequired"}},
		{"log trace", func(c *Config) { c.LogLevel = "trace" }, nil},
		{"log ERROR", func(c *Config) { c.LogLevel = "ERROR" }, nil},
		{"log bogus", func(c *Config) { c.LogLevel = "verbose" }, []string{"logLevel"}},
		{"log size 0", func(c *Config) { c.LogSizeLimit = 0 }, []string{"logSizeLimit"}},
		{"log size 10", func(c *Config) { c.LogSizeLimit = 10 }, nil},
		{"log size 11", func(c *Config) { c.LogSizeLimit = 11 }, []string{"logSizeLimit"}},
		{"instance empty", func(c *Config) { c.InstanceName = "  " }, []string{"instanceName"}},
		{"instance control", func(c *Config) { c.InstanceName = "Dupe\narr" }, []string{"instanceName"}},
		{"instance long", func(c *Config) { c.InstanceName = strings.Repeat("x", 101) }, []string{"instanceName"}},
		{"instance unicode", func(c *Config) { c.InstanceName = "Düpéarr 4K" }, nil},
		{"branch empty", func(c *Config) { c.Branch = "" }, []string{"branch"}},
		{"branch long", func(c *Config) { c.Branch = strings.Repeat("b", 101) }, []string{"branch"}},
		{"branch control", func(c *Config) { c.Branch = "ma\x00in" }, []string{"branch"}},
		{"ssl path long", func(c *Config) { c.SslCertPath = "/" + strings.Repeat("p", 4096) }, []string{"sslCertPath"}},
		{"ssl path control", func(c *Config) {
			c.EnableSsl, c.SslCertPath, c.SslKeyPath = true, "/c/cert.pem", "/c/key\x01.pem"
		}, []string{"sslKeyPath"}},
		{"everything wrong", func(c *Config) {
			*c = Config{BindAddress: "x", Port: -1, UrlBase: "a b", SslPort: 0, LogLevel: "x", AuthenticationMethod: "x", AuthenticationRequired: "x"}
		}, []string{"bindAddress", "port", "urlBase", "sslPort", "apiKey", "authenticationMethod", "authenticationRequired", "logLevel", "logSizeLimit", "instanceName", "branch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultWithKey()
			tt.mutate(&c)
			errs := Validate(c)
			var props []string
			for _, e := range errs {
				props = append(props, e.PropertyName)
				if e.ErrorMessage == "" || e.Error() == "" {
					t.Errorf("empty message for %s", e.PropertyName)
				}
			}
			if !slices.Equal(props, tt.wantProp) {
				t.Fatalf("Validate() properties = %v (%v), want %v", props, errs, tt.wantProp)
			}
		})
	}
}

func TestValidateURLBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in    string
		valid bool
	}{
		{"", true},
		{"/", true},
		{"dupearr", true},
		{"/dupearr/", true},
		{"/apps/dupearr", true},
		{"/dupe-arr_1.0~x", true},
		{"..", false},
		{"/..", false},
		{"/a/../b", false},
		{"/a/./b", false},
		{"/a b", false},
		{"/a?b=c", false},
		{"/a#frag", false},
		{"/a%2e%2e", false},
		{`/a\b`, false},
		{"/a//b", false},
		{"/a:b", false},
		{"http://x/y", false},
		{"/" + strings.Repeat("a", 201), false},
	}
	for _, tt := range tests {
		c := defaultWithKey()
		c.UrlBase = tt.in
		errs := Validate(c)
		if got := len(errs) == 0; got != tt.valid {
			t.Errorf("UrlBase %q: valid = %v (%v), want %v", tt.in, got, errs, tt.valid)
		}
		if !tt.valid && (len(errs) != 1 || errs[0].PropertyName != "urlBase") {
			t.Errorf("UrlBase %q: errors = %v", tt.in, errs)
		}
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := mustLoad(t, dir)
	before := m.Get()

	type change struct{ old, new Config }
	var changes []change
	m.OnChange(func(o, n Config) { changes = append(changes, change{o, n}) })
	m.OnChange(nil) // ignored

	got, err := m.Update(func(c *Config) {
		c.Port = 8080
		c.UrlBase = "dupearr/"
		c.AuthenticationMethod = "external"
		c.LogLevel = "DEBUG"
	})
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Port, want.UrlBase, want.AuthenticationMethod, want.LogLevel = 8080, "/dupearr", AuthExternal, "debug"
	if got != want || m.Get() != want {
		t.Fatalf("Update() = %+v, Get() = %+v, want %+v", got, m.Get(), want)
	}
	if len(changes) != 1 || changes[0].old != before || changes[0].new != want {
		t.Fatalf("OnChange calls = %+v", changes)
	}
	if file := readFile(t, m.Path()); file != canonical(want) {
		t.Fatalf("file:\n%s", file)
	}
	if reloaded := mustLoad(t, dir).Get(); reloaded != want {
		t.Fatalf("reloaded = %+v, want %+v", reloaded, want)
	}

	// A no-op update still succeeds, notifies and keeps the file stable.
	if _, err := m.Update(func(*Config) {}); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[1].old != want || changes[1].new != want {
		t.Fatalf("OnChange calls = %+v", changes)
	}
	if file := readFile(t, m.Path()); file != canonical(want) {
		t.Fatalf("file after no-op:\n%s", file)
	}
}

func TestUpdateValidationFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := mustLoad(t, dir)
	before, beforeFile := m.Get(), readFile(t, m.Path())
	called := false
	m.OnChange(func(_, _ Config) { called = true })

	_, err := m.Update(func(c *Config) {
		c.Port = 0
		c.EnableSsl = true
		c.UrlBase = "/../etc"
	})
	var verrs ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) != 4 {
		t.Fatalf("err = %v, want 4 ValidationErrors", err)
	}
	if !strings.Contains(err.Error(), "port") {
		t.Fatalf("error text %q", err.Error())
	}
	if called || m.Get() != before || readFile(t, m.Path()) != beforeFile {
		t.Fatal("failed Update changed state, file or notified subscribers")
	}
	if _, err := m.Update(nil); err == nil {
		t.Fatal("Update(nil) must fail")
	}
}

func TestUpdateSaveFailureKeepsState(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced")
	}
	dir := t.TempDir()
	m := mustLoad(t, dir)
	before := m.Get()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	called := false
	m.OnChange(func(_, _ Config) { called = true })
	if _, err := m.Update(func(c *Config) { c.Port = 9999 }); err == nil {
		t.Fatal("want save error")
	}
	if called || m.Get() != before {
		t.Fatal("failed save changed state or notified subscribers")
	}
}

func TestUpdateLeavesNoTempFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := mustLoad(t, dir)
	for i := range 5 {
		if _, err := m.Update(func(c *Config) { c.Port = 4000 + i }); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		t.Fatalf("dir contents = %v", entries)
	}
	if runtime.GOOS != "windows" && fileMode(t, m.Path()) != 0o600 {
		t.Fatalf("mode = %o", fileMode(t, m.Path()))
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	m := mustLoad(t, t.TempDir())
	var mu sync.Mutex
	notified := 0
	m.OnChange(func(_, n Config) {
		mu.Lock()
		notified++
		mu.Unlock()
		_ = n.Port
	})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				if _, err := m.Update(func(c *Config) { c.Port = 5000 + i*10 + j }); err != nil {
					t.Error(err)
				}
			}
		})
		wg.Go(func() {
			for range 50 {
				c := m.Get()
				if c.Port < 1 || c.ApiKey == "" {
					t.Error("torn read")
				}
				_ = m.EnvOverrides()
			}
		})
	}
	wg.Wait()
	if notified != 80 {
		t.Fatalf("notified %d times, want 80", notified)
	}
	if reloaded := mustLoad(t, m.DataDir()).Get(); reloaded != m.Get() {
		t.Fatalf("file and memory diverged: %+v vs %+v", reloaded, m.Get())
	}
}

func TestReadEnvPreference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		environ []string
		want    int
	}{
		{"upper wins over mixed", []string{"Dupearr__Server__Port=1", "DUPEARR__SERVER__PORT=2", "dupearr__server__port=3"}, 2},
		{"smallest spelling wins", []string{"dupearr__server__port=3", "Dupearr__Server__Port=1"}, 1},
		{"empty ignored", []string{"DUPEARR__SERVER__PORT=", "dupearr__server__port=7"}, 7},
		{"unrelated ignored", []string{"PORT=9", "DUPEARR__SERVER__PORTX=9", "DUPEARR__=1", "garbage"}, 0},
	}
	for _, tt := range tests {
		e, err := readEnv(tt.environ)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if e.values.Port != tt.want || e.set[1] != (tt.want != 0) {
			t.Errorf("%s: port = %d set=%v, want %d", tt.name, e.values.Port, e.set[1], tt.want)
		}
	}
}

func TestLoadAcceptsBOMAndEpilog(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		// Windows editors (Notepad) prepend a UTF-8 byte order mark; .NET/Servarr accept it.
		"bom":              "\xef\xbb\xbf" + canonical(defaultWithKey()),
		"bom + decl":       "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" + canonical(defaultWithKey()),
		"trailing comment": canonical(defaultWithKey()) + "<!-- edited by hand -->\n\n",
		"trailing pi":      canonical(defaultWithKey()) + "<?note x?>",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, dir, content, 0o600)
			m, err := load(dir, nil)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := m.Get(); got != defaultWithKey() {
				t.Fatalf("Get() = %+v", got)
			}
			if raw := readFile(t, m.Path()); strings.HasPrefix(raw, "\xef\xbb\xbf") {
				t.Fatal("byte order mark kept: encoding/xml readers (the healthcheck) reject it")
			} else if strings.HasPrefix(name, "bom") && raw != canonical(defaultWithKey()) {
				t.Fatalf("config.xml not rewritten canonically:\n%s", raw)
			}
		})
	}
}

func TestLoadDuplicatePrefersFirstNonBlank(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A blank ApiKey ahead of the real one must not regenerate the key (that would silently
	// break every client using it).
	p := writeFile(t, dir, "<Config><ApiKey></ApiKey><Port>4000</Port><ApiKey>"+testKey+"</ApiKey><Port>5000</Port><ApiKey>"+
		"ffffffffffffffffffffffffffffffff</ApiKey></Config>", 0o600)
	m := mustLoad(t, dir)
	if c := m.Get(); c.ApiKey != testKey || c.Port != 4000 {
		t.Fatalf("Get() = %+v, want the first non-blank values", c)
	}
	got := readFile(t, p)
	if strings.Count(got, "<ApiKey>") != 1 || !strings.Contains(got, "<ApiKey>"+testKey+"</ApiKey>") || strings.Contains(got, "5000") {
		t.Fatalf("config.xml:\n%s", got)
	}
}

func TestUpdateThroughSymlinkKeepsLink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir, realDir := t.TempDir(), t.TempDir()
	real := filepath.Join(realDir, "dupearr-config.xml")
	if err := os.WriteFile(real, []byte(canonical(defaultWithKey())), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, FileName)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	m := mustLoad(t, dir)
	if _, err := m.Update(func(c *Config) { c.InstanceName = "Linked" }); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.xml is no longer a symlink: %v %v", fi, err)
	}
	if got := readFile(t, real); !strings.Contains(got, "<InstanceName>Linked</InstanceName>") {
		t.Fatalf("link target not updated:\n%s", got)
	}
	if entries, _ := os.ReadDir(realDir); len(entries) != 1 {
		t.Fatalf("temp files left next to the target: %v", entries)
	}
}

func TestLoadRefusesToWriteAnUnloadableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Just under the size limit, with elements missing: the rewrite (indentation per node plus the
	// missing elements) would exceed maxFileSize and never load again.
	comments := strings.Repeat("<!--x-->", (maxFileSize-100)/8)
	content := "<Config><ApiKey>" + testKey + "</ApiKey>" + comments + "</Config>"
	if len(content) > maxFileSize {
		t.Fatalf("test file too large: %d", len(content))
	}
	p := writeFile(t, dir, content, 0o600)
	if _, err := load(dir, nil); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("err = %v, want a size error", err)
	}
	if readFile(t, p) != content {
		t.Fatal("file modified")
	}
}

// The reverse-proxy trust lists (issue #1) are ordinary config.xml settings: stored as "a, b" whatever
// separators were typed, saved by Update and read back unchanged; an empty list is valid.
func TestTrustListsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := defaultWithKey()
	c.TrustedProxies = "172.18.0.5 ,10.0.0.0/8;fd00::/8"
	c.AllowedHosts = "Dupearr.Example.com\n*.lan.example.org"
	p := writeFile(t, dir, canonical(c), 0o600)

	m := mustLoad(t, dir)
	got := m.Get()
	if got.TrustedProxies != "172.18.0.5, 10.0.0.0/8, fd00::/8" || got.AllowedHosts != "Dupearr.Example.com, *.lan.example.org" {
		t.Fatalf("loaded lists = %q / %q", got.TrustedProxies, got.AllowedHosts)
	}
	want := defaultWithKey()
	want.TrustedProxies, want.AllowedHosts = got.TrustedProxies, got.AllowedHosts
	if file := readFile(t, p); file != canonical(want) {
		t.Fatalf("config.xml was not canonicalised:\n%s", file)
	}

	if _, err := m.Update(func(c *Config) { c.TrustedProxies = "10.0.0.2;;  10.0.0.3" }); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, dir).Get(); got.TrustedProxies != "10.0.0.2, 10.0.0.3" || got.AllowedHosts != want.AllowedHosts {
		t.Fatalf("reloaded lists = %q / %q", got.TrustedProxies, got.AllowedHosts)
	}
	saved := readFile(t, p)
	if _, err := m.Update(func(c *Config) { c.TrustedProxies, c.AllowedHosts = "", " " }); err != nil {
		t.Fatalf("clearing the lists: %v", err)
	}
	if got := mustLoad(t, dir).Get(); got.TrustedProxies != "" || got.AllowedHosts != "" {
		t.Fatalf("cleared lists reloaded as %q / %q", got.TrustedProxies, got.AllowedHosts)
	}
	if !strings.Contains(saved, "<TrustedProxies>10.0.0.2, 10.0.0.3</TrustedProxies>") {
		t.Fatalf("saved file:\n%s", saved)
	}
}

// config.xml stays lenient like the environment: entries the reverse-proxy code refuses (a range
// that spans the internet, a typo) never stop Dupearr from starting. They are skipped and logged by
// internal/auth; only Settings → General refuses them.
func TestInvalidTrustEntriesDoNotStopStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := defaultWithKey()
	c.TrustedProxies = "0.0.0.0/0, bogus"
	c.AllowedHosts = "bad/host"
	writeFile(t, dir, canonical(c), 0o600)
	m, err := load(dir, nil)
	if err != nil {
		t.Fatalf("Load refused invalid trust entries: %v", err)
	}
	if got := m.Get(); got.TrustedProxies != "0.0.0.0/0, bogus" || got.AllowedHosts != "bad/host" {
		t.Fatalf("lists = %q / %q", got.TrustedProxies, got.AllowedHosts)
	}
	if errs := Validate(m.Get()); len(errs) != 0 {
		t.Fatalf("Validate = %v, want no problems", errs)
	}
	environ := []string{"DUPEARR__AUTH__TRUSTEDPROXIES=0.0.0.0/0 bogus", "DUPEARR__AUTH__ALLOWEDHOSTS=bad/host"}
	m, err = load(t.TempDir(), environ)
	if err != nil {
		t.Fatalf("Load refused invalid trust entries from the environment: %v", err)
	}
	if got := m.Get(); got.TrustedProxies != "0.0.0.0/0, bogus" || !slices.Equal(m.EnvOverrides(), []string{"trustedProxies", "allowedHosts"}) {
		t.Fatalf("lists = %q, overrides %v", got.TrustedProxies, m.EnvOverrides())
	}
}
