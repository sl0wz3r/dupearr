package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	configFileName = "config.xml"
	probeTimeout   = 5 * time.Second
)

// probeConfig is the part of config.xml the healthcheck needs. It is parsed here rather than with
// config.Load so that the probe (run every few seconds by Docker) never creates or rewrites
// config.xml and keeps working even if the file cannot be fully loaded.
type probeConfig struct {
	BindAddress string
	Port        int
	UrlBase     string
}

// healthcheck GETs http://127.0.0.1:<Port><UrlBase>/ping (or the bound address when BindAddress is
// a specific IP) and returns the process exit code: 0 healthy, 1 unhealthy.
func healthcheck(ctx context.Context, dataDir string, stderr io.Writer) int {
	pc, err := readProbeConfig(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	url := pingURL(pc)
	if err := probe(ctx, url); err != nil {
		fmt.Fprintf(stderr, "healthcheck: %s: %v\n", url, err)
		return 1
	}
	return 0
}

// readProbeConfig reads BindAddress/Port/UrlBase from dataDir/config.xml (defaults when the file
// does not exist yet) and applies the DUPEARR__SERVER__* env overrides.
func readProbeConfig(dataDir string) (probeConfig, error) {
	pc := probeConfig{Port: config.DefaultPort}

	raw, err := os.ReadFile(filepath.Join(dataDir, configFileName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// not written yet: defaults
	case err != nil:
		return pc, err
	default:
		var x struct {
			XMLName     xml.Name `xml:"Config"`
			BindAddress string   `xml:"BindAddress"`
			Port        string   `xml:"Port"`
			UrlBase     string   `xml:"UrlBase"`
		}
		if err := xml.Unmarshal(raw, &x); err != nil {
			return pc, fmt.Errorf("parse %s: %w", configFileName, err)
		}
		pc.BindAddress = strings.TrimSpace(x.BindAddress)
		if p, err := strconv.Atoi(strings.TrimSpace(x.Port)); err == nil && p > 0 {
			pc.Port = p
		}
		pc.UrlBase = x.UrlBase
	}

	// Variable names match case-insensitively, exactly as config.Load resolves them (e.g.
	// "Dupearr__Server__Port" in a compose file).
	if v, ok := config.LookupEnv("DUPEARR__SERVER__BINDADDRESS"); ok {
		pc.BindAddress = strings.TrimSpace(v)
	}
	if v, ok := config.LookupEnv("DUPEARR__SERVER__PORT"); ok {
		if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 {
			pc.Port = p
		}
	}
	if v, ok := config.LookupEnv("DUPEARR__SERVER__URLBASE"); ok {
		pc.UrlBase = v
	}
	pc.UrlBase = normalizeURLBase(pc.UrlBase)
	return pc, nil
}

// normalizeURLBase returns "" or "/something" (leading slash, no trailing slash).
func normalizeURLBase(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "/")
	if s == "" {
		return ""
	}
	return "/" + s
}

func pingURL(pc probeConfig) string {
	host := clientHost(pc.BindAddress, "127.0.0.1")
	return "http://" + net.JoinHostPort(host, strconv.Itoa(pc.Port)) + pc.UrlBase + "/ping"
}

// probe succeeds when url answers 200 with {"status":"OK"}.
func probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{Proxy: nil}, // never send the local probe through HTTP_PROXY
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !strings.EqualFold(body.Status, "OK") {
		return fmt.Errorf("status %q", body.Status)
	}
	return nil
}
