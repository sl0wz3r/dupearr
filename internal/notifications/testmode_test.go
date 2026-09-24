package notifications

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// SEC-031: POST /notification/test reaches any URL (or SMTP host) an admin enters. Its result must
// not become a read-back channel for internal services: the body of an error response from an
// address that is not a provider's public endpoint is withheld from the returned error — and from
// the log at every level, since the log level is an ordinary setting and the log is served by the
// API (r2-data-files#1) — and link-local / cloud-metadata addresses are never connected to.
// Background deliveries withhold it the same way (r2-outbound-web#6).

const internalBanner = "internal-service-banner v1.2.3 secret-ish=abc"

func TestTestWithholdsResponseBodiesOfOtherAddresses(t *testing.T) {
	cases := map[string]func(url string) map[string]any{
		KindWebhook: func(u string) map[string]any { return map[string]any{"url": u + "/admin"} },
		KindDiscord: func(u string) map[string]any { return map[string]any{"webhookUrl": u + "/api/webhooks/1/x"} },
		KindSlack:   func(u string) map[string]any { return map[string]any{"webhookUrl": u + "/services/x"} },
		KindGotify:  func(u string) map[string]any { return map[string]any{"serverUrl": u, "appToken": "tok"} },
		KindNtfy:    func(u string) map[string]any { return map[string]any{"serverUrl": u, "topic": "t"} },
		KindApprise: func(u string) map[string]any { return map[string]any{"serverUrl": u, "configKey": "k"} },
	}
	for kind, settings := range cases {
		t.Run(kind, func(t *testing.T) {
			// Plain text and the JSON fields providers extract ({"message"}, {"error"}, …).
			for _, body := range []string{internalBanner,
				`{"message":"` + internalBanner + `","error":"` + internalBanner + `","errorDescription":"` + internalBanner + `"}`} {
				srv, _ := newRecorder(t, http.StatusForbidden, body)
				s, _, logs := newTestService(t)
				err := s.Test(context.Background(), newConfig(kind, settings(srv.URL)))
				if err == nil {
					t.Fatal("expected an error")
				}
				if strings.Contains(err.Error(), "internal-service-banner") {
					t.Fatalf("Test reflects the response body: %v", err)
				}
				if !strings.Contains(err.Error(), "HTTP 403") {
					t.Errorf("err = %v, want the status", err)
				}
				if strings.Contains(logs.String(), "internal-service-banner") {
					t.Errorf("the withheld response must not be logged (not even at debug level); logs:\n%s", logs.String())
				}
			}
		})
	}
}

// TestDeliveryWithholdsResponseBodiesOfOtherAddresses (r2-outbound-web#6): a saved connection's
// failed delivery is logged ("notification failed", GET /api/v1/log), so saving a connection to an
// internal URL and triggering any event must not read that service's answer back either.
func TestDeliveryWithholdsResponseBodiesOfOtherAddresses(t *testing.T) {
	for _, body := range []string{internalBanner, `{"message":"` + internalBanner + `","error":"` + internalBanner + `"}`} {
		srv, _ := newRecorder(t, http.StatusInternalServerError, body)
		for kind, settings := range map[string]map[string]any{
			KindWebhook: {"url": srv.URL + "/some/path"},
			KindNtfy:    {"serverUrl": srv.URL, "topic": "t"},
			KindGotify:  {"serverUrl": srv.URL, "appToken": "tok"},
		} {
			s, _, logs := newTestService(t)
			cfg := newConfig(kind, settings)
			err := sendDirect(t, s, cfg, sampleMessage())
			if err == nil || strings.Contains(err.Error(), "internal-service-banner") || !strings.Contains(err.Error(), "HTTP 500") {
				t.Fatalf("%s: err = %v, want the status without the body", kind, err)
			}
			s.send(cfg, sampleMessage()) // the background path: logs "notification failed" at warn
			if !strings.Contains(logs.String(), "notification failed") {
				t.Fatalf("%s: expected the failure to be logged; logs:\n%s", kind, logs.String())
			}
			if strings.Contains(logs.String(), "internal-service-banner") {
				t.Errorf("%s: a failed delivery logged the response body; logs:\n%s", kind, logs.String())
			}
		}
	}
}

// TestStatusErrorKeepsProviderDetailForProviderEndpoints: the provider's own explanation is kept
// where the address is a provider's (fixed or public) endpoint.
func TestStatusErrorKeepsProviderDetailForProviderEndpoints(t *testing.T) {
	r := &httpResult{status: http.StatusForbidden, body: []byte(`{"error":"forbidden"}`)}
	if err := r.statusError(r.jsonField("error")); !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
	r.withhold = true
	if err := r.statusError(r.jsonField("error")); strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("withheld err = %v", err)
	}
}

func TestPublicProviderHost(t *testing.T) {
	for host, want := range map[string]bool{
		"discord.com": true, "DISCORD.com": true, "canary.discord.com": true, "discordapp.com": true,
		"hooks.slack.com": true, "ntfy.sh": true, "ntfy.sh:443": true,
		"evil-discord.com": false, "discord.com.evil.example": false, "127.0.0.1": false,
		"gotify.local": false, "slack.com": false, "": false,
	} {
		if got := publicProviderHost(host); got != want {
			t.Errorf("publicProviderHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestMetadataAndLinkLocalAddressesRefused(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest/api/token",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
		"http://100.100.100.200/latest/meta-data/",
	} {
		t.Run(u, func(t *testing.T) {
			s, _, _ := newTestService(t)
			start := time.Now()
			err := s.Test(context.Background(), newConfig(KindWebhook, map[string]any{"url": u, "method": "PUT",
				"headers": "X-aws-ec2-metadata-token-ttl-seconds: 21600"}))
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("err = %v, want the address to be refused", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Errorf("refusal took %s; it must happen before connecting", time.Since(start))
			}
		})
	}
	t.Run("smtp", func(t *testing.T) {
		s, _, _ := newTestService(t)
		cfg := newConfig(KindEmail, with(validSettings(KindEmail), "host", "169.254.169.254", "port", 25, "encryption", "none"))
		if err := s.Test(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("err = %v, want the address to be refused", err)
		}
	})
}

func TestCheckDialAddress(t *testing.T) {
	for addr, blocked := range map[string]bool{
		"169.254.169.254:80": true, "169.254.1.1:443": true, "[fe80::1%en0]:80": true, "[fe80::1]:80": true,
		"[fd00:ec2::254]:80": true, "[::ffff:169.254.169.254]:80": true, "100.100.100.200:80": true,
		"127.0.0.1:80": false, "192.168.1.10:8080": false, "[::1]:25": false, "10.0.0.1:587": false,
		"100.100.100.100:53": false, "not-an-address": true,
	} {
		if err := checkDialAddress("tcp", addr, nil); (err != nil) != blocked {
			t.Errorf("checkDialAddress(%s) = %v, want blocked=%v", addr, err, blocked)
		}
	}
}

// TestSMTPTestDoesNotEchoForeignBanners: the email provider connects to any host:port; a service
// that is not an SMTP server (POP3, a custom TCP service, …) must not have its banner echoed back.
func TestSMTPTestDoesNotEchoForeignBanners(t *testing.T) {
	for _, banner := range []string{"+OK " + internalBanner, "hello " + internalBanner} {
		t.Run(banner[:5], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					_, _ = c.Write([]byte(banner + "\r\n"))
					time.Sleep(200 * time.Millisecond)
					_ = c.Close()
				}
			}()
			s, _, _ := newTestService(t)
			err = s.Test(context.Background(), newConfig(KindEmail, emailConfig(ln.Addr().(*net.TCPAddr).Port)))
			if err == nil || strings.Contains(err.Error(), "internal-service-banner") {
				t.Fatalf("err = %v, want an error without the banner", err)
			}
			if !strings.Contains(err.Error(), "not an SMTP server") {
				t.Errorf("err = %v, want an SMTP hint", err)
			}
		})
	}
}

// TestHTTPTestDoesNotEchoForeignBanners: the HTTP providers connect to any URL; net/http quotes a
// non-HTTP peer's first line or header in its error, which must not come back as the test result
// (SSH, FTP, POP3 banners, …).
func TestHTTPTestDoesNotEchoForeignBanners(t *testing.T) {
	for _, banner := range []string{
		"SSH-2.0-OpenSSH_9.6p1 " + internalBanner,
		"220-" + internalBanner,
		"+OK " + internalBanner,
		"HTTP/1.1 200 OK\r\nX-" + internalBanner,
	} {
		t.Run(banner[:5], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					_, _ = c.Write([]byte(banner + "\r\n\r\n"))
					time.Sleep(200 * time.Millisecond)
					_ = c.Close()
				}
			}()
			s, _, _ := newTestService(t)
			err = s.Test(context.Background(), newConfig(KindWebhook, map[string]any{"url": "http://" + ln.Addr().String() + "/hook"}))
			if err == nil || strings.Contains(err.Error(), "internal-service-banner") || strings.Contains(err.Error(), "OpenSSH") {
				t.Fatalf("err = %v, want an error without the banner", err)
			}
			if !strings.Contains(err.Error(), "not HTTP") {
				t.Errorf("err = %v, want a not-HTTP hint", err)
			}
		})
	}
}
