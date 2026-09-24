package main

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestHTTPServerLimits: header deadline, idle timeout and header cap are set; there is no write
// timeout (Server-Sent Events stream for hours) and no read timeout (backup uploads).
func TestHTTPServerLimits(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler(), context.Background(), nil)
	if srv.ReadHeaderTimeout != 10*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Errorf("ReadHeaderTimeout %s, IdleTimeout %s", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes %d, want a cap below the 1 MiB default", srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 0 || srv.ReadTimeout != 0 {
		t.Errorf("WriteTimeout %s / ReadTimeout %s would cut SSE streams or uploads", srv.WriteTimeout, srv.ReadTimeout)
	}
	if srv.BaseContext == nil {
		t.Error("no BaseContext: shutdown could not end SSE streams")
	}
}

// TestShutdownBudget: a graceful stop fits in the 10 s `docker stop` grace (Docker, Unraid).
func TestShutdownBudget(t *testing.T) {
	if shutdownTimeout > 8*time.Second {
		t.Fatalf("shutdownTimeout = %s, want ≤ 8s", shutdownTimeout)
	}
	for _, tc := range []struct{ left, want time.Duration }{
		{10 * time.Second, notifierShutdownTimeout},
		{2 * time.Second, 2 * time.Second},
		{0, minNotifierShutdown},
		{-time.Second, minNotifierShutdown},
	} {
		if got := notifierBudget(tc.left); got != tc.want {
			t.Errorf("notifierBudget(%s) = %s, want %s", tc.left, got, tc.want)
		}
	}
}

func TestWithoutEnv(t *testing.T) {
	got := withoutEnv([]string{"A=1", appLockFDEnv + "=7", "B=2", appLockFDEnv + "X=3"}, appLockFDEnv)
	if want := []string{"A=1", "B=2", appLockFDEnv + "X=3"}; !slices.Equal(got, want) {
		t.Fatalf("withoutEnv = %v, want %v", got, want)
	}
}

// TestRunningInDockerHonoursEnv: the official image's DUPEARR_DOCKER=1 counts (Podman, Kubernetes).
func TestRunningInDockerHonoursEnv(t *testing.T) {
	t.Setenv("DUPEARR_DOCKER", "1")
	if !runningInDocker() {
		t.Fatal("DUPEARR_DOCKER=1 not detected")
	}
}

// TestHealthCheckAfterBootGrace: issues found at boot are only notified by a check after the
// health checker's grace period, so one is queued right after it.
func TestHealthCheckAfterBootGrace(t *testing.T) {
	clearServerEnv(t)
	ctx := context.Background()
	a, err := newApp(ctx, options{dataDir: t.TempDir(), noBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.health = health.New(health.Deps{Store: a.db, Config: a.cfg, StartTime: time.Now(), BootGracePeriod: 50 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.checkHealthAfterGrace(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no health check queued after the grace period")
	}
	cmds, err := a.commands.Recent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cmds, func(c models.Command) bool { return c.Name == models.CmdCheckHealth }) {
		t.Fatalf("commands %+v lack CheckHealth", cmds)
	}

	// A shutdown before the end of the grace period queues nothing.
	a.health = health.New(health.Deps{Store: a.db, Config: a.cfg, StartTime: time.Now(), BootGracePeriod: time.Hour})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	a.checkHealthAfterGrace(cctx)
}
