package arr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// r2-outbound-web#8: the *arr client never connects to link-local / cloud-metadata addresses (the
// connection test URL is free-form and its answer would come back through the API).
func TestClientRefusesMetadataAddresses(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254", "http://[fd00:ec2::254]:7878", "http://100.100.100.200"} {
		c := New(models.ArrInstance{Kind: models.ArrRadarr, URL: u, APIKey: "0123456789abcdef0123456789abcdef"}, Options{Timeout: 5 * time.Second})
		start := time.Now()
		_, err := c.Status(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: err = %v, want the address to be refused", u, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: refusal took %s", u, time.Since(start))
		}
	}
}
