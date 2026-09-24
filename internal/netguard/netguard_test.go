package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckDialAddress(t *testing.T) {
	for addr, blocked := range map[string]bool{
		"169.254.169.254:80": true, "169.254.1.1:443": true, "[fe80::1%en0]:80": true, "[fe80::1]:80": true,
		"[fd00:ec2::254]:80": true, "[::ffff:169.254.169.254]:80": true, "100.100.100.200:80": true,
		"127.0.0.1:80": false, "192.168.1.10:8080": false, "[::1]:25": false, "10.0.0.1:587": false,
		"100.100.100.100:53": false, "not-an-address": true,
	} {
		if err := CheckDialAddress("tcp", addr, nil); (err != nil) != blocked {
			t.Errorf("CheckDialAddress(%s) = %v, want blocked=%v", addr, err, blocked)
		}
	}
}

// r2-outbound-web#8: with an HTTP(S) proxy configured, the dial check only sees the proxy's
// address; Proxy must refuse a blocked destination before the request reaches the proxy.
func TestProxyRefusesBlockedDestinations(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	pu, _ := url.Parse(proxy.URL)
	tr := &http.Transport{
		Proxy:       Proxy(http.ProxyURL(pu)),
		DialContext: NewDialer(5 * time.Second).DialContext,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/",
		"http://100.100.100.200/",
	} {
		resp, err := client.Get(u)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("GET %s through the proxy succeeded", u)
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("GET %s: err = %v, want a refusal", u, err)
		}
	}
	if n := proxied.Load(); n != 0 {
		t.Fatalf("the proxy received %d request(s) for blocked destinations", n)
	}
	// An ordinary destination still goes through the proxy.
	resp, err := client.Get("http://192.0.2.10/")
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	_ = resp.Body.Close()
	if proxied.Load() != 1 {
		t.Fatalf("the proxy received %d requests, want 1", proxied.Load())
	}
}

func TestProxyWithoutProxyLeavesDialCheck(t *testing.T) {
	f := Proxy(func(*http.Request) (*url.URL, error) { return nil, nil })
	req, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/", nil)
	if u, err := f(req); u != nil || err != nil {
		t.Fatalf("Proxy() = %v, %v; want no proxy and no error (the dial check applies)", u, err)
	}
}
