package plex

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// TestNonHTTPPeerBannerNotEchoed: the server URL is free-form (connection tests reach any
// host:port); net/http quotes a non-HTTP peer's first line or header in its error, which must not
// come back to the caller (SSH, FTP, POP3 banners, …).
func TestNonHTTPPeerBannerNotEchoed(t *testing.T) {
	const secret = "internal-banner-SECRET"
	for _, banner := range []string{"SSH-2.0-OpenSSH_9.6p1 " + secret, "+OK " + secret, "HTTP/1.1 200 OK\r\nX-" + secret} {
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
					time.Sleep(100 * time.Millisecond)
					_ = c.Close()
				}
			}()
			c := New("http://"+ln.Addr().String(), testToken, Options{ClientIdentifier: testClientID, Timeout: 5 * time.Second})
			c.retryDelay = 0
			_, err = c.Identity(context.Background())
			if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "OpenSSH") {
				t.Fatalf("err = %v, want an error without the peer's banner", err)
			}
			if !errors.Is(err, errNotHTTP) {
				t.Errorf("err = %v, want errNotHTTP", err)
			}
		})
	}
}
