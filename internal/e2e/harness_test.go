//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// dupearrBin is the binary under test (built by TestMain or DUPEARR_E2E_BINARY).
var dupearrBin string

const (
	startTimeout   = 60 * time.Second // process start until /ping answers
	commandTimeout = 90 * time.Second // one command (scan, queue run) to finish
	stopTimeout    = 20 * time.Second // SIGTERM until the process has exited
	requestTimeout = 60 * time.Second
	pollInterval   = 50 * time.Millisecond
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if p := os.Getenv("DUPEARR_E2E_BINARY"); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: DUPEARR_E2E_BINARY: %v\n", err)
			return 1
		}
		dupearrBin = abs
		return m.Run()
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "dupearr-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "dupearr")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args := []string{"build", "-o", bin}
	if os.Getenv("DUPEARR_E2E_RACE") != "" {
		args = append(args, "-race")
	}
	args = append(args, "./cmd/dupearr")
	build := exec.Command("go", args...)
	build.Dir = root
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	started := time.Now()
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go %s: %v\n", strings.Join(args, " "), err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "e2e: built %s in %s\n", bin, time.Since(started).Round(time.Millisecond))
	dupearrBin = bin
	return m.Run()
}

// repoRoot is the module root (tests run in their package directory, internal/e2e).
func repoRoot() (string, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("module root not found from the test directory: %w", err)
	}
	return root, nil
}

// ---------------------------------------------------------------------------
// The dupearr process
// ---------------------------------------------------------------------------

// syncBuffer collects the process output (written by the exec goroutines, read by the test).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("… (%d earlier lines)", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

// dupearr is one running dupearr subprocess.
type dupearr struct {
	t       testing.TB
	base    string // http://127.0.0.1:port
	apiKey  string
	dataDir string
	client  *http.Client

	cmd     *exec.Cmd
	out     *syncBuffer
	exited  chan struct{}
	waitErr error
}

type startOptions struct {
	// forms keeps the default authentication (Forms, first-run setup pending) instead of None.
	forms bool
	// env adds environment variables (KEY=value) for the process.
	env []string
}

var errPortInUse = errors.New("port in use")

// startDupearr starts the binary with a fresh data directory and a free loopback port, waits
// until /ping answers and reads the API key from config.xml. The process is stopped (SIGTERM,
// then killed) when the test ends.
func startDupearr(t testing.TB, o startOptions) *dupearr {
	t.Helper()
	dataDir := t.TempDir()
	for attempt := 1; ; attempt++ {
		d, err := launch(t, dataDir, o)
		if err == nil {
			return d
		}
		if errors.Is(err, errPortInUse) && attempt < 5 {
			t.Logf("port taken before dupearr bound it; retrying (%v)", err)
			continue
		}
		t.Fatalf("starting dupearr: %v", err)
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// childEnv is the test process environment without any DUPEARR__ override (a developer's shell
// must not change the tests) and without proxy settings, plus extra.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		uk := strings.ToUpper(k)
		if strings.HasPrefix(uk, "DUPEARR__") || strings.HasPrefix(uk, "DUPEARR_E2E") ||
			uk == "HTTP_PROXY" || uk == "HTTPS_PROXY" || uk == "NO_PROXY" || uk == "ALL_PROXY" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func launch(t testing.TB, dataDir string, o startOptions) (*dupearr, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	env := []string{
		"DUPEARR__SERVER__PORT=" + strconv.Itoa(port),
		"DUPEARR__SERVER__BINDADDRESS=127.0.0.1",
		"DUPEARR__LOG__LEVEL=debug",
		// plex.tv (owner lookups) must never be reached with the fake's token: route every
		// non-loopback request to a closed port (loopback requests are never proxied).
		"HTTPS_PROXY=http://127.0.0.1:1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if !o.forms {
		env = append(env, "DUPEARR__AUTH__METHOD=None")
	}
	env = append(env, o.env...)
	cmd := exec.Command(dupearrBin, "--data", dataDir, "--nobrowser")
	cmd.Env = childEnv(env...)
	cmd.Dir = dataDir
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	d := &dupearr{
		t:       t,
		base:    "http://127.0.0.1:" + strconv.Itoa(port),
		dataDir: dataDir,
		client: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		cmd:    cmd,
		out:    out,
		exited: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		d.waitErr = cmd.Wait()
		close(d.exited)
	}()
	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case <-d.exited:
			o := out.String()
			if strings.Contains(o, "address already in use") || strings.Contains(o, "bind:") {
				return nil, fmt.Errorf("%w: %s", errPortInUse, tail(o, 5))
			}
			return nil, fmt.Errorf("dupearr exited before answering /ping (%v):\n%s", d.waitErr, tail(o, 80))
		default:
		}
		if time.Now().After(deadline) {
			d.stop()
			return nil, fmt.Errorf("/ping did not answer within %s:\n%s", startTimeout, tail(out.String(), 80))
		}
		resp, err := d.client.Get(d.base + "/ping")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(pollInterval)
	}
	t.Cleanup(func() {
		// A clean shutdown is part of every test (a race-built binary also exits non-zero when
		// the race detector fired). Windows has no SIGTERM: the process is killed there.
		graceful := d.stop()
		switch {
		case runtime.GOOS == "windows":
		case !graceful:
			t.Errorf("dupearr did not stop within %s of SIGTERM (killed)", stopTimeout)
		case d.waitErr != nil:
			t.Errorf("dupearr exited with %v after SIGTERM", d.waitErr)
		}
		if t.Failed() {
			t.Logf("dupearr output (%s):\n%s", d.base, tail(out.String(), 150))
		}
	})
	key, err := readAPIKey(filepath.Join(dataDir, "config.xml"))
	if err != nil {
		return nil, err
	}
	d.apiKey = key
	return d, nil
}

// setupCodeRE finds the first-run setup code in the process output (logged at startup).
var setupCodeRE = regexp.MustCompile(`setupCode=([A-Za-z0-9-]+)`)

// setupCode returns the latest first-run setup code the process printed ("" when none).
func (d *dupearr) setupCode() string {
	m := setupCodeRE.FindAllStringSubmatch(d.out.String(), -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// stop sends SIGTERM and waits for the exit (killing the process after stopTimeout). It reports
// whether the process exited on its own.
func (d *dupearr) stop() bool {
	select {
	case <-d.exited:
		return true
	default:
	}
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil { // Windows: not supported
		_ = d.cmd.Process.Kill()
		<-d.exited
		return false
	}
	select {
	case <-d.exited:
		return true
	case <-time.After(stopTimeout):
		_ = d.cmd.Process.Kill()
		<-d.exited
		return false
	}
}

func readAPIKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var c struct {
		ApiKey string `xml:"ApiKey"`
	}
	if err := xml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(c.ApiKey) == "" {
		return "", fmt.Errorf("%s has no ApiKey", path)
	}
	return strings.TrimSpace(c.ApiKey), nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

type apiResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r apiResponse) String() string {
	b := string(r.Body)
	if !utf8.ValidString(b) || strings.ContainsRune(b, 0) {
		return fmt.Sprintf("HTTP %d (%d bytes of binary data, %s)", r.Status, len(r.Body), r.Header.Get("Content-Type"))
	}
	if len(b) > 600 {
		b = b[:600] + "…"
	}
	return fmt.Sprintf("HTTP %d %s", r.Status, b)
}

// message returns the "message" of an error body.
func (r apiResponse) message() string {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(r.Body, &m)
	return m.Message
}

// request performs an API request with the API key. body may be nil, []byte or a JSON value.
func (d *dupearr) request(method, path string, body any) apiResponse {
	d.t.Helper()
	return d.requestWith(d.client, method, path, body, http.Header{"X-Api-Key": {d.apiKey}})
}

// requestWith performs a request with the given client and headers (no API key unless given).
func (d *dupearr) requestWith(c *http.Client, method, path string, body any, hdr http.Header) apiResponse {
	d.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			d.t.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, d.base+path, rd)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if rd != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return apiResponse{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// expect performs a request, fails the test unless the status is want and decodes the body into
// out (when non-nil).
func (d *dupearr) expect(method, path string, body any, want int, out any) apiResponse {
	d.t.Helper()
	r := d.request(method, path, body)
	if r.Status != want {
		d.t.Fatalf("%s %s: %s, want %d", method, path, r, want)
	}
	if out != nil {
		if err := json.Unmarshal(r.Body, out); err != nil {
			d.t.Fatalf("%s %s: decode %T: %v (%s)", method, path, out, err, r)
		}
	}
	return r
}

// eventually polls f until it reports true or timeout passes (then fails with f's last detail).
func eventually(t testing.TB, timeout time.Duration, what string, f func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, detail := f()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s: %s", timeout, what, detail)
		}
		time.Sleep(pollInterval)
	}
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func finished(c models.Command) bool {
	switch c.Status {
	case models.CommandCompleted, models.CommandFailed, models.CommandAborted:
		return true
	}
	return false
}

// startCommand queues a command (POST /api/v1/command).
func (d *dupearr) startCommand(name string, body map[string]any) models.Command {
	d.t.Helper()
	payload := map[string]any{"name": name}
	for k, v := range body {
		payload[k] = v
	}
	var c models.Command
	d.expect(http.MethodPost, "/api/v1/command", payload, http.StatusCreated, &c)
	return c
}

// waitCommand polls a command until it finished.
func (d *dupearr) waitCommand(id int64) models.Command {
	d.t.Helper()
	var c models.Command
	eventually(d.t, commandTimeout, fmt.Sprintf("command %d", id), func() (bool, string) {
		c = models.Command{}
		d.expect(http.MethodGet, "/api/v1/command/"+strconv.FormatInt(id, 10), nil, http.StatusOK, &c)
		return finished(c), fmt.Sprintf("%s is %s (%s)", c.Name, c.Status, c.Message)
	})
	return c
}

// runCommand queues a command, waits for it and for everything it queued (waitIdle), and fails
// the test unless it completed.
func (d *dupearr) runCommand(name string, body map[string]any) models.Command {
	d.t.Helper()
	c := d.waitCommand(d.startCommand(name, body).ID)
	if c.Status != models.CommandCompleted {
		d.t.Fatalf("command %s %s: %s", name, c.Status, c.Message)
	}
	d.waitIdle()
	return c
}

// commands returns the recent commands (newest first).
func (d *dupearr) commands() []models.Command {
	d.t.Helper()
	var cs []models.Command
	d.expect(http.MethodGet, "/api/v1/command", nil, http.StatusOK, &cs)
	return cs
}

// waitIdle waits until no command is queued or running (observed twice in a row, so a command
// queued by one that just finished — a re-scan after a removal — is waited for as well).
func (d *dupearr) waitIdle() {
	d.t.Helper()
	idle := 0
	eventually(d.t, commandTimeout, "the command queue to drain", func() (bool, string) {
		var busy []string
		for _, c := range d.commands() {
			if !finished(c) {
				busy = append(busy, fmt.Sprintf("%s#%d(%s)", c.Name, c.ID, c.Status))
			}
		}
		if len(busy) > 0 {
			idle = 0
			return false, strings.Join(busy, ", ")
		}
		idle++
		return idle >= 2, ""
	})
}

// commandsNamed returns the finished commands called name, oldest first.
func (d *dupearr) commandsNamed(name string) []models.Command {
	d.t.Helper()
	var out []models.Command
	for _, c := range d.commands() {
		if c.Name == name {
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out
}

// ---------------------------------------------------------------------------
// Duplicates, actions, settings
// ---------------------------------------------------------------------------

type fileSummary struct {
	ID              int64  `json:"id"`
	Decision        string `json:"decision"`
	Resolution      string `json:"resolution"`
	DynamicRange    string `json:"dynamicRange"`
	VideoCodec      string `json:"videoCodec"`
	Size            int64  `json:"size"`
	LibraryTitle    string `json:"libraryTitle"`
	ArrInstanceName string `json:"arrInstanceName"`
}

type groupSummary struct {
	ID               int64         `json:"id"`
	Key              string        `json:"key"`
	MediaType        string        `json:"mediaType"`
	Title            string        `json:"title"`
	Year             int           `json:"year"`
	ShowTitle        string        `json:"showTitle"`
	Season           int           `json:"season"`
	Episode          int           `json:"episode"`
	Status           string        `json:"status"`
	StatusReason     string        `json:"statusReason"`
	Flags            []string      `json:"flags"`
	FileCount        int           `json:"fileCount"`
	KeepCount        int           `json:"keepCount"`
	RemoveCount      int           `json:"removeCount"`
	ReclaimableBytes int64         `json:"reclaimableBytes"`
	Signature        string        `json:"signature"`
	Files            []fileSummary `json:"files"`
}

// label names a group in assertions: the movie title, or "Show S01E02" for an episode.
func (g groupSummary) label() string {
	if g.ShowTitle != "" {
		return fmt.Sprintf("%s S%02dE%02d", g.ShowTitle, g.Season, g.Episode)
	}
	return g.Title
}

type groupDetail struct {
	models.DuplicateGroup
	Actions []models.Action `json:"actions"`
}

func groupLabel(g models.DuplicateGroup) string {
	return groupSummary{Title: g.Title, ShowTitle: g.ShowTitle, Season: g.Season, Episode: g.Episode}.label()
}

type paged[T any] struct {
	TotalRecords int `json:"totalRecords"`
	Records      []T `json:"records"`
}

// groups lists every duplicate group (any status).
func (d *dupearr) groups() []groupSummary {
	d.t.Helper()
	var p paged[groupSummary]
	d.expect(http.MethodGet, "/api/v1/duplicate?pageSize=1000&sortKey=title&sortDirection=ascending", nil, http.StatusOK, &p)
	if p.TotalRecords != len(p.Records) {
		d.t.Fatalf("duplicate list: %d of %d records", len(p.Records), p.TotalRecords)
	}
	return p.Records
}

// groupsBy returns the groups whose label is label (several when a group was re-keyed).
func (d *dupearr) groupsBy(label string) []groupSummary {
	d.t.Helper()
	var out []groupSummary
	for _, g := range d.groups() {
		if g.label() == label {
			out = append(out, g)
		}
	}
	return out
}

// groupID returns the id of the one group labelled label.
func (d *dupearr) groupID(label string) int64 {
	d.t.Helper()
	gs := d.groupsBy(label)
	if len(gs) != 1 {
		d.t.Fatalf("groups labelled %q: %d, want 1 (%s)", label, len(gs), d.describeGroups())
	}
	return gs[0].ID
}

// group fetches the full group (versions, decisions, actions).
func (d *dupearr) group(id int64) groupDetail {
	d.t.Helper()
	var g groupDetail
	d.expect(http.MethodGet, "/api/v1/duplicate/"+strconv.FormatInt(id, 10), nil, http.StatusOK, &g)
	return g
}

// groupNamed fetches the one group labelled label.
func (d *dupearr) groupNamed(label string) groupDetail {
	d.t.Helper()
	return d.group(d.groupID(label))
}

// describeGroups renders the group list for failure messages.
func (d *dupearr) describeGroups() string {
	var b strings.Builder
	for _, g := range d.groups() {
		fmt.Fprintf(&b, "\n  #%d %-28s %-9s flags=%v keep=%d remove=%d %s", g.ID, g.label(), g.Status, g.Flags, g.KeepCount, g.RemoveCount, g.StatusReason)
	}
	return b.String()
}

// actions lists every action (any status), oldest first.
func (d *dupearr) actions() []models.Action {
	d.t.Helper()
	var p paged[models.Action]
	d.expect(http.MethodGet, "/api/v1/action?pageSize=1000&sortKey=id&sortDirection=ascending", nil, http.StatusOK, &p)
	sort.Slice(p.Records, func(i, j int) bool { return p.Records[i].ID < p.Records[j].ID })
	return p.Records
}

// actionsOf returns the actions of one group, oldest first.
func (d *dupearr) actionsOf(groupID int64) []models.Action {
	d.t.Helper()
	var out []models.Action
	for _, a := range d.actions() {
		if a.GroupID == groupID {
			out = append(out, a)
		}
	}
	return out
}

// approve approves one group (optionally with the reviewed signature).
func (d *dupearr) approve(id int64, signature string) apiResponse {
	d.t.Helper()
	var body any
	if signature != "" {
		body = map[string]string{"signature": signature}
	}
	return d.request(http.MethodPost, fmt.Sprintf("/api/v1/duplicate/%d/approve", id), body)
}

type bulkResult struct {
	Succeeded []int64 `json:"succeeded"`
	Failed    []struct {
		ID      int64  `json:"id"`
		Message string `json:"message"`
	} `json:"failed"`
}

func (d *dupearr) bulk(action string, ids ...int64) bulkResult {
	d.t.Helper()
	var res bulkResult
	d.expect(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": ids, "action": action}, http.StatusOK, &res)
	return res
}

// bulkApprove approves groups the way the UI does: with the signature of each reviewed group
// (sent with string keys, like JSON objects from the browser). It fails unless all succeeded.
func (d *dupearr) bulkApprove(groups ...groupSummary) {
	d.t.Helper()
	ids := make([]int64, 0, len(groups))
	sigs := map[string]string{}
	for _, g := range groups {
		ids = append(ids, g.ID)
		sigs[strconv.FormatInt(g.ID, 10)] = g.Signature
	}
	var res bulkResult
	d.expect(http.MethodPost, "/api/v1/duplicate/bulk", map[string]any{"ids": ids, "action": "approve", "signatures": sigs}, http.StatusOK, &res)
	if len(res.Failed) > 0 || len(res.Succeeded) != len(ids) {
		d.t.Fatalf("bulk approve %v: %+v", ids, res)
	}
}

// withStatus returns the groups with the given status.
func withStatus(groups []groupSummary, status string) []groupSummary {
	var out []groupSummary
	for _, g := range groups {
		if g.Status == status {
			out = append(out, g)
		}
	}
	return out
}

// putSettings merges patch onto the stored settings.
func (d *dupearr) putSettings(patch map[string]any) models.Settings {
	d.t.Helper()
	var st models.Settings
	d.expect(http.MethodPut, "/api/v1/config/settings", patch, http.StatusAccepted, &st)
	return st
}

func (d *dupearr) health() []models.HealthCheck {
	d.t.Helper()
	var hc []models.HealthCheck
	d.expect(http.MethodGet, "/api/v1/health", nil, http.StatusOK, &hc)
	return hc
}

// history returns the audit events of a group, oldest first.
func (d *dupearr) history(groupID int64) []models.HistoryEvent {
	d.t.Helper()
	var p paged[models.HistoryEvent]
	d.expect(http.MethodGet, fmt.Sprintf("/api/v1/history?groupId=%d&pageSize=1000", groupID), nil, http.StatusOK, &p)
	sort.Slice(p.Records, func(i, j int) bool { return p.Records[i].ID < p.Records[j].ID })
	return p.Records
}

// ---------------------------------------------------------------------------
// The whole stack: fake media servers + configured dupearr
// ---------------------------------------------------------------------------

// scopeGroup links the "Movies" and "Movies 4K" libraries (cross-library duplicates).
const scopeGroup = "movies"

type stack struct {
	t          *testing.T
	env        *fakemedia.Env
	d          *dupearr
	serverID   int64
	arrIDs     map[string]int64 // fakemedia instance name → Dupearr id
	recycleBin string           // Dupearr's recycle bin (not created until first use)
}

type stackOptions struct {
	scenario *fakemedia.Scenario // nil = fakemedia.Default()
	settings map[string]any      // applied before the first scan
	// recycleBin sets settings.recycleBinPath to stack.recycleBin (<fake data dir>/dupearr-recycle:
	// outside every library, on the media's filesystem, not created yet).
	recycleBin bool
	noScan     bool
}

// newStack starts the fake media servers and dupearr, configures dupearr through the API (Plex,
// the three *arr instances, path mappings, the "movies" scope group, settings) and runs a full
// scan. Every fake request that breaks a safety rule fails the test at the end.
func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	sc := o.scenario
	if sc == nil {
		sc = fakemedia.Default()
	}
	env := fakemedia.StartWithOptions(t, fakemedia.Options{Scenario: sc, Dir: t.TempDir()})
	// Registered before dupearr's cleanup, so it runs after dupearr stopped.
	t.Cleanup(func() { env.AssertNoViolations(t) })
	s := &stack{
		t:          t,
		env:        env,
		d:          startDupearr(t, startOptions{}),
		arrIDs:     map[string]int64{},
		recycleBin: filepath.Join(env.Dir, "dupearr-recycle"),
	}
	s.configure()
	// fakemedia creates every file when the test starts, so by their change time every untracked
	// copy is brand new (Plex dates the items weeks back): the minimum age would hold them all.
	// Tests of the minimum age set it themselves.
	settings := map[string]any{"minAgeHours": 0}
	for k, v := range o.settings {
		settings[k] = v
	}
	if o.recycleBin {
		settings["recycleBinPath"] = s.recycleBin
	}
	if len(settings) > 0 {
		s.d.putSettings(settings)
	}
	s.d.waitIdle()
	if !o.noScan {
		s.scan()
	}
	return s
}

// configure adds the media server, the *arr instances, the path mappings and the scope group.
func (s *stack) configure() {
	t, d, env := s.t, s.d, s.env
	t.Helper()
	var ms models.MediaServer
	d.expect(http.MethodPost, "/api/v1/mediaserver", map[string]any{
		"name": "Fake Plex", "kind": "plex", "url": env.Plex.URL, "token": env.PlexToken, "enabled": true,
	}, http.StatusCreated, &ms)
	if ms.Token != "********" || ms.MachineIdentifier != env.MachineIdentifier {
		t.Fatalf("created media server = %+v", ms)
	}
	s.serverID = ms.ID
	for _, name := range []string{fakemedia.InstanceRadarr, fakemedia.InstanceRadarr4K, fakemedia.InstanceSonarr} {
		srv := env.Instances[name]
		var a models.ArrInstance
		d.expect(http.MethodPost, "/api/v1/arr", map[string]any{
			"name": srv.InstanceName, "kind": srv.Kind, "url": srv.URL, "apiKey": srv.APIKey, "enabled": true,
		}, http.StatusCreated, &a)
		s.arrIDs[name] = a.ID
	}
	for _, m := range env.PathMappings() {
		pm := map[string]any{"sourceType": models.PathSourceServer, "sourceId": s.serverID, "remotePath": m.Remote, "localPath": m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm["sourceType"], pm["sourceId"] = models.PathSourceArr, s.arrIDs[m.Server]
		}
		d.expect(http.MethodPost, "/api/v1/pathmapping", pm, http.StatusCreated, nil)
	}
	var libs []models.Library
	d.expect(http.MethodGet, fmt.Sprintf("/api/v1/mediaserver/%d/library", s.serverID), nil, http.StatusOK, &libs)
	if len(libs) != 3 {
		t.Fatalf("libraries = %+v, want Movies, Movies 4K and TV Shows", libs)
	}
	for _, l := range libs {
		patch := map[string]any{"enabled": true}
		if l.Type == fakemedia.LibraryMovie {
			patch["scopeGroup"] = scopeGroup
		}
		d.expect(http.MethodPut, fmt.Sprintf("/api/v1/library/%d", l.ID), patch, http.StatusAccepted, nil)
	}
}

// scan runs a full DuplicateScan and waits for everything it queued.
func (s *stack) scan() models.Command {
	s.t.Helper()
	return s.d.runCommand(models.CmdDuplicateScan, nil)
}

// approveAndProcess approves one group and waits for the queue run it starts.
func (s *stack) approveAndProcess(label string) groupDetail {
	s.t.Helper()
	g := s.d.groupNamed(label)
	if r := s.d.approve(g.ID, g.Signature); r.Status != http.StatusOK {
		s.t.Fatalf("approve %s: %s", label, r)
	}
	s.d.waitIdle()
	return s.d.group(g.ID)
}

// ---------------------------------------------------------------------------
// Fake-side assertions
// ---------------------------------------------------------------------------

// isMutation reports a request that changes the fake world: any DELETE, any *arr write (command,
// movie/editor, episode/monitor, exclusions) and any Plex refresh/scan.
func isMutation(r fakemedia.Request) bool {
	switch {
	case r.Method == http.MethodDelete:
		return true
	case r.Server != fakemedia.ServerPlex:
		return r.Method == http.MethodPost || r.Method == http.MethodPut
	default:
		return strings.HasSuffix(strings.TrimRight(r.Path, "/"), "/refresh") || r.Method == http.MethodPut || r.Method == http.MethodPost
	}
}

func describeRequests(rs []fakemedia.Request) string {
	var b strings.Builder
	for _, r := range rs {
		q := ""
		if len(r.Query) > 0 {
			v := url.Values{}
			for k, vs := range r.Query {
				if strings.EqualFold(k, "X-Plex-Token") || strings.EqualFold(k, "apikey") {
					continue
				}
				v[k] = vs
			}
			if len(v) > 0 {
				q = "?" + v.Encode()
			}
		}
		fmt.Fprintf(&b, "\n  %s %s %s%s -> %d", r.Server, r.Method, r.Path, q, r.Status)
	}
	return b.String()
}

// mutations returns the mutating requests received since the last ResetRequests.
func mutations(env *fakemedia.Env) []fakemedia.Request {
	var out []fakemedia.Request
	for _, r := range env.Requests() {
		if isMutation(r) {
			out = append(out, r)
		}
	}
	return out
}

// requestsMatching returns the logged requests of server with method whose path contains substr.
func requestsMatching(env *fakemedia.Env, server, method, substr string) []fakemedia.Request {
	var out []fakemedia.Request
	for _, r := range env.RequestsTo(server) {
		if r.Method == method && strings.Contains(r.Path, substr) {
			out = append(out, r)
		}
	}
	return out
}

// requireFile fails unless the file behind a Plex path exists (or not, with want=false).
func (s *stack) requireFile(remotePath string, want bool) {
	s.t.Helper()
	if got := s.env.FileExists(remotePath); got != want {
		s.t.Fatalf("file %s exists = %t, want %t", remotePath, got, want)
	}
}

// filesOf returns the group's versions split by decision.
func filesOf(g groupDetail) (keep, remove []models.GroupFile) {
	for _, f := range g.Files {
		if f.Decision == models.DecisionKeep {
			keep = append(keep, f)
		} else {
			remove = append(remove, f)
		}
	}
	return keep, remove
}

// partPaths returns the Plex paths of a version.
func partPaths(v models.MediaVersion) []string {
	out := make([]string, 0, len(v.Parts))
	for _, p := range v.Parts {
		out = append(out, p.Path)
	}
	return out
}

// fileContaining returns the group's version whose first part path contains substr.
func fileContaining(t testing.TB, g groupDetail, substr string) models.GroupFile {
	t.Helper()
	for _, f := range g.Files {
		if len(f.Version.Parts) > 0 && strings.Contains(f.Version.Parts[0].Path, substr) {
			return f
		}
	}
	t.Fatalf("%s has no version with a file containing %q", groupLabel(g.DuplicateGroup), substr)
	return models.GroupFile{}
}

// readLines returns the non-empty lines of a file.
func readLines(t testing.TB, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}
