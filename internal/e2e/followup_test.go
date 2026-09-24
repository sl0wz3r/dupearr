//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestNewCopyWaitsForMinimumAge: Plex dates the item, not the version, so a copy put next to an old
// title used to be as "old" as the title and removable at once. fakemedia creates every file when
// the test starts while Plex dates the titles weeks back — every untracked copy is such a new copy:
// with the default minimum age (7 days) it is held back (deferred) and cannot be approved. A copy an *arr tracks keeps the *arr's date (Severance's loser, imported weeks ago).
func TestNewCopyWaitsForMinimumAge(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{recycleBin: true, settings: map[string]any{
		"dryRun": false, "deletionMethods": []string{"arr", "filesystem", "plex"}, "minAgeHours": 168,
	}})
	d := s.d
	br := d.groupNamed("Blade Runner 2049")
	_, remove := filesOf(br)
	if len(remove) != 1 || remove[0].Version.Arr != nil {
		t.Fatalf("Blade Runner losers %+v, want one untracked copy", remove)
	}
	if br.Status != models.GroupDeferred || !slices.Contains(br.Flags, models.FlagMinAge) {
		t.Fatalf("Blade Runner: %s %v (%s), want deferred by the minimum age", br.Status, br.Flags, br.StatusReason)
	}
	if age := time.Since(remove[0].Version.AddedAt); age < 0 || age > time.Hour {
		t.Errorf("the new copy's date added is %s (%s ago), want its file's", remove[0].Version.AddedAt, age)
	}
	if sev := d.groupNamed("Severance S01E01"); sev.Status != models.GroupPending {
		t.Errorf("Severance (tracked loser, dated by Sonarr): %s (%s), want pending", sev.Status, sev.StatusReason)
	}

	// Not even an approval by hand queues it (the decisions' safety check), and a queue run
	// touches nothing.
	s.env.ResetRequests()
	if r := d.approve(br.ID, br.Signature); r.Status != http.StatusBadRequest || !strings.Contains(r.message(), "minimum age") {
		t.Fatalf("approve: %s, want 400 (minimum age)", r)
	}
	d.runCommand(models.CmdProcessQueue, nil)
	if acts := d.actionsOf(br.ID); len(acts) != 0 {
		t.Fatalf("actions = %+v, want none", acts)
	}
	s.requireFile(remove[0].Version.Parts[0].Path, true)
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("a copy younger than the minimum age was touched:%s", describeRequests(m))
	}
}

// TestSecondServerOnSameDataDir: one server per data directory. A second process on the same
// directory exits with a clear message — also right after an in-place restart (the lock is handed
// over through the exec) — while subcommands such as healthcheck still work.
func TestSecondServerOnSameDataDir(t *testing.T) {
	t.Parallel()
	d := startDupearr(t, startOptions{})
	refused := func(when string) {
		t.Helper()
		_, err := launch(t, d.dataDir, startOptions{})
		if err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("second server %s: %v, want it refused", when, err)
		}
	}
	refused("while the first runs")

	port := strings.TrimPrefix(d.base, "http://127.0.0.1:")
	hc := exec.Command(dupearrBin, "healthcheck", "--data", d.dataDir)
	hc.Env = childEnv("DUPEARR__SERVER__PORT="+port, "DUPEARR__SERVER__BINDADDRESS=127.0.0.1")
	if out, err := hc.CombinedOutput(); err != nil {
		t.Fatalf("healthcheck while the server holds the lock: %v\n%s", err, out)
	}

	var before struct {
		StartTime time.Time `json:"startTime"`
	}
	d.expect(http.MethodGet, "/api/v1/system/status", nil, http.StatusOK, &before)
	d.expect(http.MethodPost, "/api/v1/system/restart", nil, http.StatusAccepted, nil)
	eventually(t, startTimeout, "the restarted server", func() (bool, string) {
		req, err := http.NewRequest(http.MethodGet, d.base+"/api/v1/system/status", nil)
		if err != nil {
			return false, err.Error()
		}
		req.Header.Set("X-Api-Key", d.apiKey)
		resp, err := d.client.Do(req)
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		var after struct {
			StartTime time.Time `json:"startTime"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&after) != nil {
			return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return after.StartTime.After(before.StartTime), "start time " + after.StartTime.String()
	})
	select {
	case <-d.exited:
		t.Fatalf("the process exited instead of restarting in place: %v", d.waitErr)
	default:
	}
	refused("after an in-place restart")
	if pid := strconv.Itoa(d.cmd.Process.Pid); !strings.Contains(d.out.String(), "Restart requested") {
		t.Errorf("no restart in the log of process %s", pid)
	}
}
