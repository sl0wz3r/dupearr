package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestParseArgs(t *testing.T) {
	data := t.TempDir()
	tests := []struct {
		name          string
		args          []string
		wantCmd       string
		wantData      string // "" = don't check
		wantNoBrowser bool
		wantErr       bool
		wantHelp      bool
	}{
		{name: "serve with data", args: []string{"--data", data}, wantCmd: cmdServe, wantData: data},
		{name: "serve nobrowser", args: []string{"--nobrowser", "-data=" + data}, wantCmd: cmdServe, wantData: data, wantNoBrowser: true},
		{name: "version", args: []string{"version"}, wantCmd: cmdVersion},
		{name: "subcommand first", args: []string{"healthcheck", "--data", data}, wantCmd: cmdHealthcheck, wantData: data},
		{name: "subcommand last", args: []string{"--data", data, "reset-auth"}, wantCmd: cmdResetAuth, wantData: data},
		{name: "flags around subcommand", args: []string{"--data", data, "healthcheck", "--nobrowser"}, wantCmd: cmdHealthcheck, wantData: data, wantNoBrowser: true},
		{name: "unknown subcommand", args: []string{"frobnicate"}, wantErr: true},
		{name: "two subcommands", args: []string{"version", "healthcheck"}, wantErr: true},
		{name: "unknown flag", args: []string{"--bogus"}, wantErr: true},
		{name: "help flag", args: []string{"--help"}, wantErr: true, wantHelp: true},
		{name: "help subcommand", args: []string{"help"}, wantErr: true, wantHelp: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseArgs(tt.args, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantHelp && !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("err = %v, want flag.ErrHelp", err)
			}
			if tt.wantErr {
				return
			}
			if opts.command != tt.wantCmd {
				t.Errorf("command = %q, want %q", opts.command, tt.wantCmd)
			}
			if tt.wantData != "" && opts.dataDir != tt.wantData {
				t.Errorf("dataDir = %q, want %q", opts.dataDir, tt.wantData)
			}
			if opts.noBrowser != tt.wantNoBrowser {
				t.Errorf("noBrowser = %v, want %v", opts.noBrowser, tt.wantNoBrowser)
			}
		})
	}
}

func TestParseArgsDataDirIsAbsolute(t *testing.T) {
	opts, err := parseArgs([]string{"--data", "relative/dir"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(opts.dataDir) {
		t.Fatalf("dataDir %q is not absolute", opts.dataDir)
	}
}

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, io.Discard); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.HasPrefix(out.String(), "Dupearr ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunBadArgsExitCode(t *testing.T) {
	if code := run([]string{"frobnicate"}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestListenAddr(t *testing.T) {
	tests := []struct {
		bind string
		port int
		want string
	}{
		{"*", 3873, ":3873"},
		{"", 3873, ":3873"},
		{" * ", 1, ":1"},
		{"0.0.0.0", 3873, "0.0.0.0:3873"},
		{"127.0.0.1", 8080, "127.0.0.1:8080"},
		{"::", 3873, "[::]:3873"},
		{"[::1]", 9873, "[::1]:9873"},
		{"::1", 9873, "[::1]:9873"},
	}
	for _, tt := range tests {
		if got := listenAddr(tt.bind, tt.port); got != tt.want {
			t.Errorf("listenAddr(%q, %d) = %q, want %q", tt.bind, tt.port, got, tt.want)
		}
	}
}

func TestClientHost(t *testing.T) {
	tests := []struct{ bind, want string }{
		{"*", "127.0.0.1"},
		{"", "127.0.0.1"},
		{"0.0.0.0", "127.0.0.1"},
		{"::", "127.0.0.1"},
		{"[::]", "127.0.0.1"},
		{"192.168.1.5", "192.168.1.5"},
		{"[::1]", "::1"},
	}
	for _, tt := range tests {
		if got := clientHost(tt.bind, "127.0.0.1"); got != tt.want {
			t.Errorf("clientHost(%q) = %q, want %q", tt.bind, got, tt.want)
		}
	}
}

func TestNormalizeURLBase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"  ", ""},
		{"dupearr", "/dupearr"},
		{"/dupearr/", "/dupearr"},
		{"//dupearr//", "/dupearr"},
		{"/a/b", "/a/b"},
	}
	for _, tt := range tests {
		if got := normalizeURLBase(tt.in); got != tt.want {
			t.Errorf("normalizeURLBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// clearServerEnv neutralizes DUPEARR__SERVER__* overrides from the developer's environment.
func clearServerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DUPEARR__SERVER__BINDADDRESS", "DUPEARR__SERVER__PORT", "DUPEARR__SERVER__URLBASE"} {
		t.Setenv(k, "")
	}
}

func writeConfigXML(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadProbeConfig(t *testing.T) {
	clearServerEnv(t)

	t.Run("missing file uses defaults", func(t *testing.T) {
		pc, err := readProbeConfig(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if pc.Port != config.DefaultPort || pc.UrlBase != "" {
			t.Fatalf("got %+v", pc)
		}
	})

	t.Run("values from file", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigXML(t, dir, `<Config>
  <BindAddress>*</BindAddress>
  <Port> 8123 </Port>
  <UrlBase>dupearr/</UrlBase>
  <ApiKey>0123456789abcdef0123456789abcdef</ApiKey>
  <SomethingUnknown>kept</SomethingUnknown>
</Config>`)
		pc, err := readProbeConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if pc.Port != 8123 || pc.UrlBase != "/dupearr" || pc.BindAddress != "*" {
			t.Fatalf("got %+v", pc)
		}
		if got, want := pingURL(pc), "http://127.0.0.1:8123/dupearr/ping"; got != want {
			t.Fatalf("pingURL = %q, want %q", got, want)
		}
	})

	t.Run("env overrides win", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigXML(t, dir, `<Config><Port>8123</Port><UrlBase>/a</UrlBase></Config>`)
		t.Setenv("DUPEARR__SERVER__PORT", "9000")
		t.Setenv("DUPEARR__SERVER__URLBASE", "/b/")
		t.Setenv("DUPEARR__SERVER__BINDADDRESS", "10.0.0.2")
		pc, err := readProbeConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := pingURL(pc), "http://10.0.0.2:9000/b/ping"; got != want {
			t.Fatalf("pingURL = %q, want %q", got, want)
		}
	})

	t.Run("invalid xml", func(t *testing.T) {
		dir := t.TempDir()
		writeConfigXML(t, dir, `<NotConfig><Port>1</Port></NotConfig>`)
		if _, err := readProbeConfig(dir); err == nil {
			t.Fatal("expected an error for a non-<Config> root")
		}
	})
}

func TestHealthcheck(t *testing.T) {
	clearServerEnv(t)

	newServer := func(t *testing.T, h http.HandlerFunc) (dir string) {
		t.Helper()
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		dir = t.TempDir()
		writeConfigXML(t, dir, "<Config><Port>"+u.Port()+"</Port><UrlBase>/dupe</UrlBase></Config>")
		return dir
	}

	t.Run("healthy", func(t *testing.T) {
		dir := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/dupe/ping" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "OK"})
		})
		if code := healthcheck(context.Background(), dir, io.Discard); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})

	t.Run("server error", func(t *testing.T) {
		dir := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		if code := healthcheck(context.Background(), dir, io.Discard); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("wrong body", func(t *testing.T) {
		dir := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"Starting"}`))
		})
		if code := healthcheck(context.Background(), dir, io.Discard); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		dir := t.TempDir()
		writeConfigXML(t, dir, "<Config><Port>"+strconv.Itoa(port)+"</Port></Config>")
		if code := healthcheck(context.Background(), dir, io.Discard); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})
}

func TestDecodeBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int64
		wantErr bool
	}{
		{"empty", "", 0, false},
		{"null", "null", 0, false},
		{"empty object", "{}", 0, false},
		{"value", `{"serverId":7}`, 7, false},
		{"invalid", `{"serverId":"x"}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &models.Command{Name: models.CmdSyncLibraries, Body: json.RawMessage(tt.body)}
			got, err := decodeBody[syncLibrariesBody](cmd)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got.ServerID != tt.want {
				t.Fatalf("ServerID = %d, want %d", got.ServerID, tt.want)
			}
		})
	}
}

func TestScanSummary(t *testing.T) {
	if got := scanSummary(nil); got != "Scan completed" {
		t.Errorf("nil run: %q", got)
	}
	run := &models.ScanRun{Stats: models.ScanStats{GroupsFound: 3, NewGroups: 1, ResolvedGroups: 2, AutoApproved: 1, Errors: 4}}
	want := "3 duplicate groups found (1 new, 2 resolved), 1 auto-approved, 4 errors"
	if got := scanSummary(run); got != want {
		t.Errorf("scanSummary = %q, want %q", got, want)
	}
}

func TestNewUUID(t *testing.T) {
	a, b := newUUID(), newUUID()
	if a == b {
		t.Fatal("UUIDs must differ")
	}
	if len(a) != 36 || a[14] != '4' || !strings.ContainsRune("89ab", rune(a[19])) {
		t.Fatalf("not a v4 UUID: %q", a)
	}
}
