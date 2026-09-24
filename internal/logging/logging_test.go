package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/store"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing stdout.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testLog struct {
	log *slog.Logger
	m   *Manager
	dir string
	out *syncBuffer
}

func newTestLog(t *testing.T, level string, maxBytes int64) testLog {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	out := &syncBuffer{}
	log, m, err := setup(dir, level, maxBytes, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return testLog{log: log, m: m, dir: dir, out: out}
}

func (tl testLog) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tl.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (tl testLog) recent(level string) []Entry {
	return tl.m.Recent(store.Paging{Page: 1, PageSize: 1000}, level).Records
}

var lineRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\|(Trace|Debug|Info|Warn|Error)\|[^|]+\|.*$`)

func TestLineFormatAndEntry(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	ts := time.Date(2026, 9, 22, 17, 30, 0, 123_456_789, time.Local)
	r := slog.NewRecord(ts, slog.LevelInfo, "Scan finished", 0)
	r.AddAttrs(slog.Int("groups", 3), slog.String("path", "/data/a b"), slog.Bool("dryRun", true))
	h := tl.log.Handler().WithAttrs([]slog.Attr{slog.String("component", "Scanner")})
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	wantFile := "2026-09-22 17:30:00.123|Info|Scanner|Scan finished groups=3 path=\"/data/a b\" dryRun=true\n"
	if got := tl.file(t, FileName); got != wantFile {
		t.Fatalf("file:\n%q\nwant\n%q", got, wantFile)
	}
	wantOut := "2026-09-22 17:30:00.123 [Info] Scanner: Scan finished groups=3 path=\"/data/a b\" dryRun=true\n"
	if got := tl.out.String(); got != wantOut {
		t.Fatalf("stdout:\n%q\nwant\n%q", got, wantOut)
	}
	entries := tl.recent("")
	want := Entry{Time: ts.UTC(), Level: "info", Logger: "Scanner", Message: `Scan finished groups=3 path="/data/a b" dryRun=true`}
	if len(entries) != 1 || entries[0] != want {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}
	if entries[0].Time.Location() != time.UTC {
		t.Fatal("entry time not UTC")
	}
}

func TestDefaultComponentAndPerRecordComponent(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("no component")
	tl.log.Info("record component", "component", "Api")
	tl.log.With("component", "A").With("component", "B").Info("last wins")
	tl.log.With("component", "  ").Info("blank ignored")
	tl.log.With("component", "Bad|Name\n").Info("sanitised")
	tl.log.WithGroup("g").Info("grouped", "component", "NotMe")

	got := []string{}
	for _, e := range tl.recent("") {
		got = append(got, e.Logger+":"+e.Message)
	}
	slices.Reverse(got)
	want := []string{"Dupearr:no component", "Api:record component", "B:last wins", "Dupearr:blank ignored",
		"Bad_Name:sanitised", "Dupearr:grouped g.component=NotMe"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	for _, line := range strings.Split(strings.TrimSpace(tl.file(t, FileName)), "\n") {
		if !lineRE.MatchString(line) {
			t.Errorf("malformed line %q", line)
		}
	}
}

func TestLevels(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "trace", 1<<20)
	ctx := context.Background()
	tl.log.Log(ctx, LevelTrace, "t")
	tl.log.Debug("d")
	tl.log.Info("i")
	tl.log.Warn("w")
	tl.log.Error("e")
	tl.log.Log(ctx, slog.LevelError+4, "beyond error")

	lines := strings.Split(strings.TrimSpace(tl.file(t, FileName)), "\n")
	wantLevels := []string{"Trace", "Debug", "Info", "Warn", "Error", "Error"}
	if len(lines) != len(wantLevels) {
		t.Fatalf("lines = %q", lines)
	}
	for i, l := range lines {
		if parts := strings.Split(l, "|"); parts[1] != wantLevels[i] {
			t.Errorf("line %d level %q, want %q", i, parts[1], wantLevels[i])
		}
	}
	if !strings.Contains(tl.out.String(), "[Trace] Dupearr: t") {
		t.Fatalf("stdout lacks trace line:\n%s", tl.out.String())
	}

	// The ring buffer keeps info and above only.
	var lv []string
	for _, e := range tl.recent("") {
		lv = append(lv, e.Level)
	}
	if !slices.Equal(lv, []string{"error", "error", "warn", "info"}) {
		t.Fatalf("ring levels = %v", lv)
	}
}

func TestSetLevel(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "warn", 1<<20)
	if tl.m.Level() != "warn" {
		t.Fatalf("Level() = %q", tl.m.Level())
	}
	tl.log.Info("hidden")
	if err := tl.m.SetLevel("DEBUG"); err != nil {
		t.Fatal(err)
	}
	tl.log.Debug("shown")
	if err := tl.m.SetLevel("bogus"); err == nil {
		t.Fatal("SetLevel(bogus) must fail")
	}
	if tl.m.Level() != "debug" {
		t.Fatalf("Level() after bad SetLevel = %q", tl.m.Level())
	}
	if err := tl.m.SetLevel("trace"); err != nil || tl.m.Level() != "trace" {
		t.Fatalf("SetLevel(trace): %v, Level() = %q", err, tl.m.Level())
	}
	if !tl.log.Enabled(context.Background(), LevelTrace) {
		t.Fatal("trace not enabled")
	}
	file := tl.file(t, FileName)
	if strings.Contains(file, "hidden") || !strings.Contains(file, "|Debug|Dupearr|shown") {
		t.Fatalf("file:\n%s", file)
	}
}

func TestParseLevelAndLevelName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]slog.Level{
		"trace": LevelTrace, "Debug": slog.LevelDebug, " info ": slog.LevelInfo, "WARN": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError, "fatal": slog.LevelError,
	} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "verbose", "5"} {
		if _, err := ParseLevel(bad); err == nil {
			t.Errorf("ParseLevel(%q) must fail", bad)
		}
	}
	for l, want := range map[slog.Level]string{
		LevelTrace - 4: "trace", LevelTrace: "trace", -5: "trace", slog.LevelDebug: "debug", -1: "debug",
		slog.LevelInfo: "info", 2: "info", slog.LevelWarn: "warn", slog.LevelError: "error", 12: "error",
	} {
		if got := LevelName(l); got != want {
			t.Errorf("LevelName(%d) = %q, want %q", l, got, want)
		}
	}
}

type panicErr struct{}

func (*panicErr) Error() string { panic("boom") }

func TestErrorAttributesBecomeException(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	log := tl.log.With("component", "Executor")
	log.Error("Delete failed", "groupId", 7, "error", errors.New("radarr: 409 conflict"))
	log.With("err", "stale data").Warn("Skipped", "err", fmt.Errorf("wrapped: %w", os.ErrNotExist))
	log.WithGroup("req").Error("Nested", "error", "not an exception")
	var nilPtr *panicErr
	log.Error("Typed nil", "error", nilPtr)

	entries := tl.recent("")
	slices.Reverse(entries)
	want := []Entry{
		{Level: "error", Logger: "Executor", Message: "Delete failed groupId=7", Exception: "radarr: 409 conflict"},
		{Level: "warn", Logger: "Executor", Message: "Skipped", Exception: "stale data; wrapped: file does not exist"},
		{Level: "error", Logger: "Executor", Message: `Nested req.error="not an exception"`},
		{Level: "error", Logger: "Executor", Message: "Typed nil", Exception: "<nil>"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i := range want {
		entries[i].Time = time.Time{}
		if entries[i] != want[i] {
			t.Errorf("entry %d = %+v\nwant %+v", i, entries[i], want[i])
		}
	}
	file := tl.file(t, FileName)
	for _, s := range []string{
		`|Error|Executor|Delete failed groupId=7 error="radarr: 409 conflict"`,
		`|Warn|Executor|Skipped err="stale data" err="wrapped: file does not exist"`,
	} {
		if !strings.Contains(file, s) {
			t.Errorf("file lacks %q:\n%s", s, file)
		}
	}
}

func TestHandlerRedactsSecrets(t *testing.T) {
	t.Parallel()
	const secret = "S3CRETVALUE123"
	tl := newTestLog(t, "trace", 1<<20)
	log := tl.log.With("component", "Plex", "token", secret)
	log.Info("GET http://plex:32400/library?X-Plex-Token="+secret,
		"url", "http://radarr/api/v3/movie?apikey="+secret,
		"apiKey", secret,
		"headers", map[string][]string{"X-Api-Key": {secret}},
		slog.Group("plex", "accessToken", secret, "body", `{"authToken":"`+secret+`"}`),
		"error", errors.New(`Get "http://sonarr/api/v3/series?apikey=`+secret+`": timeout`),
		"webhook", "https://discord.com/api/webhooks/1/"+secret,
		"empty", "",
	)
	log.Debug("debug " + secret + " password=" + secret)

	outputs := map[string]string{"file": tl.file(t, FileName), "stdout": tl.out.String()}
	for _, e := range tl.recent("") {
		outputs["entry"] += e.Message + e.Exception
	}
	for name, out := range outputs {
		if strings.Count(out, secret) > 1 || (name != "file" && name != "stdout" && strings.Contains(out, secret)) {
			t.Errorf("%s leaks the secret:\n%s", name, out)
		}
		if !strings.Contains(out, Removed) {
			t.Errorf("%s has no %s marker:\n%s", name, Removed, out)
		}
	}
	// The one remaining occurrence is the plain word in the debug message, which is not a
	// recognisable secret; everything recognisable is masked.
	if !strings.Contains(outputs["file"], "debug "+secret+" password=(removed)") {
		t.Errorf("debug line not redacted as expected:\n%s", outputs["file"])
	}
	if !strings.Contains(outputs["file"], `empty=""`) || !strings.Contains(outputs["file"], "apiKey=(removed)") {
		t.Errorf("attribute rendering:\n%s", outputs["file"])
	}
}

type lazy struct{ v string }

func (l lazy) LogValue() slog.Value { return slog.GroupValue(slog.String("v", l.v)) }

func TestAttributeRendering(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tl.log.WithGroup("").With("a", 1).WithGroup("req").With("b", 2).Info("m",
		"c", 3,
		slog.Group("", "inline", "x"),
		slog.Group("empty"),
		slog.Attr{},
		slog.Any("lazy", lazy{"y"}),
		slog.Time("at", ts),
		slog.Duration("took", 1500*time.Millisecond),
		slog.Any("raw", []byte("bytes")),
		slog.Float64("f", 1.5),
		slog.Any("nil", nil),
		"new\nline", "v\"q",
	)
	want := `m a=1 req.b=2 req.c=3 req.inline=x req.lazy.v=y req.at=2026-01-02T03:04:05Z req.took=1.5s req.raw=bytes req.f=1.5 req.nil=<nil> "req.new\nline"="v\"q"`
	entries := tl.recent("")
	if len(entries) != 1 || entries[0].Message != want {
		t.Fatalf("message = %q\nwant      %q", entries[0].Message, want)
	}
}

func TestDerivedLoggersDoNotShareAttributes(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	base := tl.log.With("a", 1)
	l2 := base.With("b", 2)
	l3 := base.With("c", 3)
	l2.Info("two")
	l3.Info("three")
	base.Info("base", "d", 4)
	var got []string
	for _, e := range tl.recent("") {
		got = append(got, e.Message)
	}
	want := []string{"base a=1 d=4", "three a=1 c=3", "two a=1 b=2"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestControlCharactersAreEscaped(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("line1\nline2\r\x1b[31mred\ttab", "v", "a\nb")
	file := tl.file(t, FileName)
	if strings.Count(file, "\n") != 1 {
		t.Fatalf("record spans several lines:\n%q", file)
	}
	if !strings.Contains(file, `line1\nline2\r\x1b[31mred`+"\ttab"+` v="a\nb"`) {
		t.Fatalf("file = %q", file)
	}
}

func TestRecentPagingAndFilter(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	for i := range 25 {
		switch i % 5 {
		case 0:
			tl.log.Error("msg " + strconv.Itoa(i))
		case 1:
			tl.log.Warn("msg " + strconv.Itoa(i))
		default:
			tl.log.Info("msg " + strconv.Itoa(i))
		}
	}
	msgs := func(p store.Page[Entry]) []string {
		out := []string{}
		for _, e := range p.Records {
			out = append(out, strings.TrimPrefix(e.Message, "msg "))
		}
		return out
	}

	p := tl.m.Recent(store.Paging{Page: 1, PageSize: 10}, "")
	if p.TotalRecords != 25 || p.Page != 1 || p.PageSize != 10 || p.SortKey != "time" || p.SortDirection != "descending" {
		t.Fatalf("page meta = %+v", p)
	}
	if got := msgs(p); !slices.Equal(got, []string{"24", "23", "22", "21", "20", "19", "18", "17", "16", "15"}) {
		t.Fatalf("page 1 = %v", got)
	}
	if got := msgs(tl.m.Recent(store.Paging{Page: 3, PageSize: 10}, "")); !slices.Equal(got, []string{"4", "3", "2", "1", "0"}) {
		t.Fatalf("page 3 = %v", got)
	}
	if p := tl.m.Recent(store.Paging{Page: 4, PageSize: 10}, ""); p.Records == nil || len(p.Records) != 0 || p.TotalRecords != 25 {
		t.Fatalf("page 4 = %+v", p)
	}
	warn := tl.m.Recent(store.Paging{Page: 1, PageSize: 100}, "warn")
	if warn.TotalRecords != 10 || !slices.Equal(msgs(warn), []string{"21", "20", "16", "15", "11", "10", "6", "5", "1", "0"}) {
		t.Fatalf("warn filter = %v (%d)", msgs(warn), warn.TotalRecords)
	}
	if e := tl.m.Recent(store.Paging{Page: 1, PageSize: 100}, "ERROR"); e.TotalRecords != 5 {
		t.Fatalf("error filter total = %d", e.TotalRecords)
	}
	if all := tl.m.Recent(store.Paging{Page: 1, PageSize: 100}, "nonsense"); all.TotalRecords != 25 {
		t.Fatalf("unknown level must not filter: %d", all.TotalRecords)
	}
	asc := tl.m.Recent(store.Paging{Page: 1, PageSize: 3, SortDirection: "ascending"}, "")
	if asc.SortDirection != "ascending" || !slices.Equal(msgs(asc), []string{"0", "1", "2"}) {
		t.Fatalf("ascending = %+v", asc)
	}

	// Normalisation of bad paging input never panics.
	for _, pg := range []store.Paging{{}, {Page: -5, PageSize: -1}, {Page: 1, PageSize: 5000}, {Page: int(^uint(0) >> 1), PageSize: 1000}} {
		p := tl.m.Recent(pg, "")
		if p.Page < 1 || p.PageSize < 1 || p.PageSize > 1000 || p.Records == nil {
			t.Fatalf("Recent(%+v) = %+v", pg, p)
		}
	}
	if p := tl.m.Recent(store.Paging{}, ""); p.PageSize != 20 || len(p.Records) != 20 {
		t.Fatalf("default page size: %+v", p)
	}
}

func TestRingCapacity(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<30)
	for i := range RingSize + 150 {
		tl.log.Info(strconv.Itoa(i))
	}
	p := tl.m.Recent(store.Paging{Page: 1, PageSize: 1000}, "")
	if p.TotalRecords != RingSize || len(p.Records) != RingSize {
		t.Fatalf("total = %d, records = %d", p.TotalRecords, len(p.Records))
	}
	if p.Records[0].Message != strconv.Itoa(RingSize+149) || p.Records[RingSize-1].Message != "150" {
		t.Fatalf("newest %q oldest %q", p.Records[0].Message, p.Records[RingSize-1].Message)
	}
}

// numbers returns the message numbers ("n=<i>") found in a log file, in order.
func numbers(t *testing.T, content string) []int {
	t.Helper()
	var out []int
	for _, m := range regexp.MustCompile(`\|m (\d+)\n`).FindAllStringSubmatch(content, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

func TestRotation(t *testing.T) {
	t.Parallel()
	// Each line is 44 bytes ("2026-09-22 17:30:00.123|Info|Dupearr|m 00\n"): two per 100-byte file.
	tl := newTestLog(t, "info", 100)
	const total = 40
	for i := range total {
		tl.log.Info(fmt.Sprintf("m %02d", i))
	}

	entries, err := os.ReadDir(tl.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	want := []string{"dupearr.0.txt", "dupearr.1.txt", "dupearr.2.txt", "dupearr.3.txt", "dupearr.4.txt", "dupearr.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("files = %v, want %v", names, want)
	}

	// Newest content is in dupearr.txt, then .0 (newest archive) … .4 (oldest).
	order := []string{"dupearr.txt", "dupearr.0.txt", "dupearr.1.txt", "dupearr.2.txt", "dupearr.3.txt", "dupearr.4.txt"}
	next := total
	for _, name := range order {
		content := tl.file(t, name)
		if int64(len(content)) > 100 {
			t.Errorf("%s is %d bytes, over the limit", name, len(content))
		}
		nums := numbers(t, content)
		if len(nums) != 2 || nums[1] != next-1 || nums[0] != next-2 {
			t.Fatalf("%s holds %v, want [%d %d]", name, nums, next-2, next-1)
		}
		next -= 2
	}

	files, err := tl.m.Files()
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, f := range files {
		listed = append(listed, f.Filename)
		if f.Size <= 0 || f.LastWriteTime.IsZero() || f.LastWriteTime.Location() != time.UTC {
			t.Errorf("bad LogFile %+v", f)
		}
	}
	if !slices.Equal(listed, order) {
		t.Fatalf("Files() = %v, want %v", listed, order)
	}
}

func TestRotationOfExistingFileAndOversizedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := strings.Repeat("x", 150) + "\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	log, m, err := setup(dir, "info", 100, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	log.Info("first") // the pre-existing oversized file is archived before this write
	big := strings.Repeat("y", 300)
	log.Info(big) // larger than the limit: goes to a fresh file on its own

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("dupearr.1.txt"); got != old {
		t.Fatalf("dupearr.1.txt = %q", got)
	}
	if got := read("dupearr.0.txt"); !strings.HasSuffix(got, "|first\n") || strings.Count(got, "\n") != 1 {
		t.Fatalf("dupearr.0.txt = %q", got)
	}
	if got := read(FileName); !strings.HasSuffix(got, "|"+big+"\n") || strings.Count(got, "\n") != 1 {
		t.Fatalf("dupearr.txt = %q", got)
	}
}

func TestFilesAndFilePath(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("hello")
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(tl.dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("dupearr.0.txt")
	mk("dupearr.debug.txt")
	mk("other.txt")
	mk("dupearr.log")
	mk("dupearr bad.txt")
	if err := os.Mkdir(filepath.Join(tl.dir, "dupearr.9.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(tl.dir, "dupearr.link.txt")); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(tl.dir, "dupearr.debug.txt"), past, past); err != nil {
		t.Fatal(err)
	}

	files, err := tl.m.Files()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Filename)
	}
	if len(names) != 3 || names[2] != "dupearr.debug.txt" || !slices.Contains(names, FileName) || !slices.Contains(names, "dupearr.0.txt") {
		t.Fatalf("Files() = %v", names)
	}

	p, err := tl.m.FilePath(FileName)
	if err != nil || p != filepath.Join(tl.dir, FileName) || !filepath.IsAbs(p) {
		t.Fatalf("FilePath(%q) = %q, %v", FileName, p, err)
	}
	for _, bad := range []string{
		"", ".", "..", "../config.xml", "../logs/dupearr.txt", "logs/dupearr.txt", "/etc/passwd",
		`..\dupearr.txt`, "dupearr.txt/..", "dupearr.99.txt", "other.txt", "dupearr.log", "dupearr.9.txt",
		"dupearr.link.txt", "dupearr bad.txt", "DUPEARR.TXT", "dupearr.txt\x00",
	} {
		if p, err := tl.m.FilePath(bad); !errors.Is(err, ErrFileNotFound) {
			t.Errorf("FilePath(%q) = %q, %v; want ErrFileNotFound", bad, p, err)
		}
	}
}

func TestFilesMissingDirectory(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	_ = tl.m.Close()
	if err := os.RemoveAll(tl.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.m.Files(); err == nil {
		t.Fatal("Files() on a missing directory must fail")
	}
	if _, err := tl.m.FilePath(FileName); err == nil {
		t.Fatal("FilePath() on a missing directory must fail")
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("before")
	if err := tl.m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tl.m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	tl.log.Info("after") // must not panic; still reaches stdout and the ring buffer
	if file := tl.file(t, FileName); strings.Contains(file, "after") || !strings.Contains(file, "before") {
		t.Fatalf("file = %q", file)
	}
	if !strings.Contains(tl.out.String(), "Dupearr: after") {
		t.Fatalf("stdout = %q", tl.out.String())
	}
	if e := tl.recent(""); len(e) != 2 || e[0].Message != "after" {
		t.Fatalf("ring = %+v", e)
	}
}

func TestSetupErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notDir := filepath.Join(dir, "file")
	if err := os.WriteFile(notDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, dir, level string }{
		{"bad level", dir, "loud"},
		{"empty dir", " ", "info"},
		{"dir is a file", notDir, "info"},
		{"parent is a file", filepath.Join(notDir, "logs"), "info"},
	}
	for _, tt := range tests {
		if _, _, err := setup(tt.dir, tt.level, 100, &syncBuffer{}); err == nil {
			t.Errorf("%s: want error", tt.name)
		}
	}
	if _, _, err := Setup(dir, "loud", 1); err == nil {
		t.Error("Setup with bad level must fail")
	}
}

func TestSetupPublic(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "a", "logs")
	log, m, err := Setup(dir, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Level() != "info" || log == nil {
		t.Fatalf("Level() = %q", m.Level())
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if m.core.file.maxBytes != 1<<20 {
		t.Fatalf("default size limit = %d", m.core.file.maxBytes)
	}
	_, m2, err := Setup(t.TempDir(), "error", 5)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if m2.core.file.maxBytes != 5<<20 || m2.Level() != "error" {
		t.Fatalf("maxBytes = %d, level %q", m2.core.file.maxBytes, m2.Level())
	}
}

func TestFileWriteFailureIsReportedOnce(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "info", 1<<20)
	// Swap in a closed file handle to force write errors.
	tl.m.core.mu.Lock()
	_ = tl.m.core.file.f.Close()
	tl.m.core.mu.Unlock()

	tl.log.Info("one")
	tl.log.Info("two")
	out := tl.out.String()
	if n := strings.Count(out, "[Error] Logging:"); n != 1 {
		t.Fatalf("write failure reported %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "Dupearr: one") || !strings.Contains(out, "Dupearr: two") {
		t.Fatalf("stdout lost records:\n%s", out)
	}
	if len(tl.recent("")) != 2 {
		t.Fatal("ring lost records")
	}
}

func TestConcurrentLogging(t *testing.T) {
	t.Parallel()
	tl := newTestLog(t, "debug", 4096)
	var wg sync.WaitGroup
	for g := range 8 {
		log := tl.log.With("component", "G"+strconv.Itoa(g))
		wg.Go(func() {
			for i := range 200 {
				log.Info("message", "i", i, "token", "secret")
				log.Debug("debug", "i", i)
			}
		})
	}
	wg.Go(func() {
		for range 50 {
			_ = tl.m.Recent(store.Paging{Page: 1, PageSize: 50}, "info")
			if _, err := tl.m.Files(); err != nil {
				t.Error(err)
			}
			_ = tl.m.SetLevel("debug")
			_ = tl.m.Level()
		}
	})
	wg.Wait()

	if got := tl.m.Recent(store.Paging{Page: 1, PageSize: 1}, "").TotalRecords; got != RingSize {
		t.Fatalf("ring holds %d, want %d", got, RingSize)
	}
	files, err := tl.m.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != MaxArchives+1 {
		t.Fatalf("files = %+v", files)
	}
	for _, f := range files {
		content := tl.file(t, f.Filename)
		if strings.Contains(content, "secret") {
			t.Fatalf("%s leaks a secret", f.Filename)
		}
		for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			if !lineRE.MatchString(line) {
				t.Fatalf("%s: torn line %q", f.Filename, line)
			}
		}
	}
}

func TestArchiveIndexAndFileOrderTies(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]int{"dupearr.txt": -1, "dupearr.0.txt": 0, "dupearr.4.txt": 4, "dupearr.12.txt": 12} {
		if got, ok := archiveIndex(name); !ok || got != want {
			t.Errorf("archiveIndex(%q) = %d, %v", name, got, ok)
		}
	}
	for _, name := range []string{"dupearr.debug.txt", "dupearr..txt", "dupearr.-1.txt", "dupearr.01.txt", "dupearr.1234567.txt", "other.0.txt", "dupearr.0.log"} {
		if _, ok := archiveIndex(name); ok {
			t.Errorf("archiveIndex(%q) accepted", name)
		}
	}

	tl := newTestLog(t, "info", 1<<20)
	tl.log.Info("x")
	same := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"dupearr.10.txt", "dupearr.2.txt", "dupearr.debug.txt", "dupearr.0.txt", FileName} {
		p := filepath.Join(tl.dir, name)
		if name != FileName {
			if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(p, same, same); err != nil {
			t.Fatal(err)
		}
	}
	files, err := tl.m.Files()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Filename)
	}
	want := []string{FileName, "dupearr.0.txt", "dupearr.2.txt", "dupearr.10.txt", "dupearr.debug.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("Files() = %v, want %v", names, want)
	}
}

func TestRotationFailureKeepsLogging(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced")
	}
	t.Parallel()
	tl := newTestLog(t, "info", 100)
	tl.log.Info("m 00")
	if err := os.Chmod(tl.dir, 0o500); err != nil { // renames now fail
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tl.dir, 0o755) })
	for i := 1; i < 6; i++ {
		tl.log.Info(fmt.Sprintf("m %02d", i))
	}
	// 44-byte lines, 100-byte limit: rotation is attempted before m 02 and, because a failed
	// rotation waits for another limit's worth of data, next before m 04 — not on every write.
	if n := strings.Count(tl.out.String(), "[Error] Logging:"); n != 2 {
		t.Fatalf("rotation failure reported %d times, want once per attempt (2):\n%s", n, tl.out.String())
	}
	if got := numbers(t, tl.file(t, FileName)); !slices.Equal(got, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("active file holds %v, want every record", got)
	}
	if _, err := os.Stat(filepath.Join(tl.dir, "dupearr.0.txt")); err == nil {
		t.Fatal("archive created in a read-only directory")
	}
}

func TestOpenRotatorRejectsBadSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		max  int64
		keep int
	}{{0, 5}, {-1, 5}, {100, 0}} {
		if _, err := openRotator(t.TempDir(), tc.max, tc.keep); err == nil {
			t.Errorf("openRotator(%d, %d) must fail", tc.max, tc.keep)
		}
	}
	r, err := openRotator(t.TempDir(), 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("x")); !errors.Is(err, errClosed) {
		t.Fatalf("Write after Close = %v", err)
	}
}
