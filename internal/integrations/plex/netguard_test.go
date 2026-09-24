package plex

import (
	"context"
	"strings"
	"testing"
	"time"
)

// r2-outbound-web#8: the Plex client never connects to link-local / cloud-metadata addresses.
func TestClientRefusesMetadataAddresses(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254:32400", "http://[fd00:ec2::254]:32400", "http://100.100.100.200"} {
		c := New(u, "tok", Options{Timeout: 5 * time.Second})
		start := time.Now()
		_, err := c.Identity(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: err = %v, want the address to be refused", u, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: refusal took %s", u, time.Since(start))
		}
	}
}
