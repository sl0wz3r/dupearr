package tautulli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

const testKey = "0123456789abcdef0123456789abcdef"

// fakeServer answers /api/v2 with handle and records every request.
type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, cmd string)) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		f.mu.Unlock()
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"response":{"result":"error","message":"Invalid apikey","data":{}}}`))
			return
		}
		handle(w, r, r.URL.Query().Get("cmd"))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.reqs...)
}

func ok(w http.ResponseWriter, data any) {
	b, _ := json.Marshal(map[string]any{"response": map[string]any{"result": "success", "message": nil, "data": data}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func clientFor(f *fakeServer, url string) *Client {
	if url == "" {
		url = f.URL
	}
	return New(models.TautulliInstance{Name: "Tautulli", URL: url, APIKey: testKey}, Options{HTTPClient: f.Client(), Timeout: 5 * time.Second})
}

func TestKeyOnlyInHeader(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		switch cmd {
		case "get_tautulli_info":
			ok(w, map[string]any{"tautulli_version": "v2.18.1"})
		case "get_server_info":
			ok(w, map[string]any{"pms_identifier": "abc", "pms_name": "Plex &amp; Co"})
		}
	})
	// A stale ?apikey= in the stored URL must never be sent.
	c := clientFor(f, f.URL+"/?apikey=stale-key-in-url")
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "v2.18.1" || info.PMSIdentifier != "abc" || info.PMSName != "Plex & Co" {
		t.Fatalf("info %+v", info)
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for _, r := range reqs {
		if r.URL.Path != "/api/v2" {
			t.Fatalf("path %q", r.URL.Path)
		}
		raw := r.URL.String()
		if strings.Contains(strings.ToLower(raw), "apikey") || strings.Contains(raw, testKey) || strings.Contains(raw, "stale") {
			t.Fatalf("the key reached the URL: %s", raw)
		}
		if r.Header.Get("X-Api-Key") != testKey || !strings.HasPrefix(r.Header.Get("User-Agent"), "Dupearr/") ||
			r.Header.Get("Accept") != "application/json" {
			t.Fatalf("headers %v", r.Header)
		}
	}
}

func TestErrorsNeverCarryURLKeyOrBody(t *testing.T) {
	secretBody := "SECRET-UPSTREAM-BODY"
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secretBody))
	})
	c := clientFor(f, "")
	_, err := c.Info(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 500 || !Transient(err) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), secretBody) || strings.Contains(err.Error(), f.URL) {
		t.Fatalf("error text leaks: %v", err)
	}
	// A connection error: the URL (credentials, key) is never repeated.
	c = New(models.TautulliInstance{URL: "http://user:pa55word@127.0.0.1:1/root", APIKey: testKey}, Options{Timeout: 2 * time.Second})
	_, err = c.Info(context.Background())
	if err == nil || strings.Contains(err.Error(), "pa55word") || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "127.0.0.1:1/root") {
		t.Fatalf("err = %v", err)
	}
	if !Transient(err) {
		t.Fatalf("connection refused should be transient: %v", err)
	}
}

func TestClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   error
	}{
		{"401 invalid key", 401, "application/json", `{"response":{"result":"error","message":"Invalid apikey","data":{}}}`, ErrUnauthorized},
		{"401 key required (older Tautulli)", 401, "application/json", `{"response":{"result":"error","message":"Parameter apikey is required","data":{}}}`, ErrTooOld},
		{"401 header dropped (2.18+)", 401, "application/json", `{"response":{"result":"error","message":"Parameter apikey is required or X-Api-Key header is required","data":{}}}`, ErrKeyHeaderMissing},
		{"200 header dropped (2.18+)", 200, "application/json", `{"response":{"result":"error","message":"Parameter apikey is required or X-Api-Key header is required","data":{}}}`, ErrKeyHeaderMissing},
		{"200 key required", 200, "application/json", `{"response":{"result":"error","message":"Parameter apikey is required","data":{}}}`, ErrTooOld},
		{"200 invalid key", 200, "application/json", `{"response":{"result":"error","message":"Invalid apikey","data":{}}}`, ErrUnauthorized},
		{"api disabled", 200, "application/json", `{"response":{"result":"error","message":"API not enabled","data":{}}}`, ErrNotFound},
		{"404", 404, "text/plain", `not found`, ErrNotFound},
		{"command failed", 200, "application/json", `{"response":{"result":"error","message":"something broke: SECRET","data":{}}}`, ErrCommandFailed},
		{"html page", 200, "text/html", `<html>login</html>`, ErrWrongApp},
		{"not json", 200, "application/json", `hello`, ErrWrongApp},
		{"no envelope", 200, "application/json", `{"data":{}}`, ErrWrongApp},
		{"odd result", 200, "application/json", `{"response":{"result":"maybe","data":{}}}`, ErrWrongApp},
		{"redirect", 307, "text/plain", ``, ErrRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
				if tt.status == 307 {
					w.Header().Set("Location", "http://elsewhere.example/api/v2")
				}
				w.Header().Set("Content-Type", tt.ctype)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, err := clientFor(f, "").Users(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "login") {
				t.Fatalf("error echoes the body: %v", err)
			}
			if Transient(err) {
				t.Fatalf("%v must not be transient", err)
			}
		})
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	target := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) { ok(w, []any{}) })
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		http.Redirect(w, r, target.URL+"/api/v2?"+r.URL.RawQuery, http.StatusFound)
	})
	_, err := clientFor(f, "").Users(context.Background())
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("err = %v", err)
	}
	if n := len(target.requests()); n != 0 {
		t.Fatalf("the redirect was followed (%d requests)", n)
	}
}

func TestRefusesMetadataAddresses(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254", "http://[fd00:ec2::254]:8181", "http://100.100.100.200"} {
		c := New(models.TautulliInstance{URL: u, APIKey: testKey}, Options{Timeout: 5 * time.Second})
		_, err := c.Info(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: err = %v, want the address to be refused", u, err)
		}
	}
}

func TestBoundedBody(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		ok(w, []map[string]any{{"is_active": 1, "keep_history": 1, "username": strings.Repeat("x", 4096)}})
	})
	c := clientFor(f, "")
	c.limits.body = 1024
	if _, err := c.Users(context.Background()); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v", err)
	}
}

func TestVersionCheck(t *testing.T) {
	for v, want := range map[string]error{
		"v2.18.0": nil, "v2.18.1": nil, "2.19.0-beta": nil, "v3.0.0": nil,
		"v2.17.2": ErrTooOld, "v2.13.4": ErrTooOld, "v1.99.99": ErrTooOld,
		"": ErrWrongApp, "banana": ErrWrongApp,
	} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
			switch cmd {
			case "get_tautulli_info":
				ok(w, map[string]any{"tautulli_version": v})
			case "get_server_info":
				ok(w, map[string]any{"pms_identifier": "abc"})
			}
		})
		_, err := clientFor(f, "").Info(context.Background())
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Fatalf("%q: err = %v, want %v", v, err, want)
		}
	}
}

// TestServerIdentityIsCleaned: the monitored server's identifier is compared, but also shown in
// health messages and logs, so it is bounded and stripped of control characters like every text of
// an answer; a real identifier (40 hex characters) comes back unchanged.
func TestServerIdentityIsCleaned(t *testing.T) {
	for id, want := range map[string]string{
		"0123456789abcdef0123456789abcdef01234567": "0123456789abcdef0123456789abcdef01234567",
		"abc\ndef\u202e":           "abcdef",
		strings.Repeat("x", 1<<20): strings.Repeat("x", 100) + "…",
	} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
			switch cmd {
			case "get_tautulli_info":
				ok(w, map[string]any{"tautulli_version": "v2.18.1"})
			case "get_server_info":
				ok(w, map[string]any{"pms_identifier": id})
			}
		})
		info, err := clientFor(f, "").Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.PMSIdentifier != want {
			t.Fatalf("identifier of %d bytes: got %d bytes %q…", len(id), len(info.PMSIdentifier), info.PMSIdentifier[:min(20, len(info.PMSIdentifier))])
		}
	}
}

func TestUsersAndLibraryFlags(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		switch cmd {
		case "get_users":
			ok(w, []map[string]any{
				{"user_id": 1, "username": "owner", "email": "o@example.com", "is_active": 1, "keep_history": 1},
				{"user_id": "2", "is_active": "1", "keep_history": "0"},
				{"user_id": 3, "is_active": 0, "keep_history": ""},
				{"user_id": 4},
			})
		case "get_library":
			switch r.URL.Query().Get("section_id") {
			case "1":
				ok(w, map[string]any{"section_id": "1", "keep_history": 1})
			case "2":
				ok(w, map[string]any{"section_id": 2, "keep_history": "0"})
			case "3": // Tautulli's default answer for an unknown section
				ok(w, map[string]any{"section_id": 0, "section_name": "Local", "keep_history": 1})
			case "4":
				ok(w, map[string]any{"section_id": "4"})
			default:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"response":{"result":"error","message":"Unable to get library","data":null}}`))
			}
		}
	})
	c := clientFor(f, "")
	users, err := c.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	flag := func(b *bool) string {
		if b == nil {
			return "nil"
		}
		return strconv.FormatBool(*b)
	}
	got := []string{}
	for _, u := range users {
		got = append(got, fmt.Sprintf("%t/%s", u.Active, flag(u.KeepHistory)))
	}
	if want := "true/true true/false false/nil true/nil"; strings.Join(got, " ") != want {
		t.Fatalf("users %v, want %s", got, want)
	}
	for section, want := range map[string]string{"1": "true", "2": "false", "3": "nil", "4": "nil"} {
		l, err := c.Library(context.Background(), section)
		if err != nil {
			t.Fatalf("section %s: %v", section, err)
		}
		if flag(l.KeepHistory) != want {
			t.Fatalf("section %s: keep history %s, want %s", section, flag(l.KeepHistory), want)
		}
	}
	// An "error" result is a failed read (never an unknown flag that quietly drops the criteria).
	if _, err := c.Library(context.Background(), "9"); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("section 9: err = %v, want %v", err, ErrCommandFailed)
	}
	if _, err := c.Library(context.Background(), "1&cmd=x"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}

// historyServer serves get_history from rows like Tautulli (grouping and paging honoured).
type historyServer struct {
	rows    []map[string]any
	mutate  func(q url.Values, total int, rows []map[string]any) (int, []map[string]any)
	pages   int
	lastQry url.Values
}

func (h *historyServer) handle(w http.ResponseWriter, r *http.Request, cmd string) {
	q := r.URL.Query()
	h.lastQry = q
	h.pages++
	var match []map[string]any
	keys := map[string]bool{}
	for _, k := range strings.Split(q.Get("rating_key"), ",") {
		if k != "" {
			keys[k] = true
		}
	}
	for _, row := range h.rows {
		if len(keys) > 0 && !keys[fmt.Sprint(row["rating_key"])] {
			continue
		}
		if s := q.Get("section_id"); s != "" && fmt.Sprint(row["section_id"]) != s {
			continue
		}
		if g := q.Get("guid"); g != "" && !strings.HasPrefix(fmt.Sprint(row["guid"]), g) {
			continue
		}
		match = append(match, row)
	}
	start, _ := strconv.Atoi(q.Get("start"))
	length, _ := strconv.Atoi(q.Get("length"))
	total := len(match)
	page := []map[string]any{}
	if start < len(match) {
		page = match[start:min(len(match), start+length)]
	}
	if h.mutate != nil {
		total, page = h.mutate(q, total, page)
	}
	ok(w, map[string]any{"draw": 1, "recordsTotal": len(h.rows), "recordsFiltered": total, "data": page})
}

func playRow(id int, rk, guid string, user int, started int64) map[string]any {
	return map[string]any{"row_id": id, "id": id, "rating_key": rk, "guid": guid, "user_id": user, "user": "name" + strconv.Itoa(user),
		"section_id": 1, "date": started, "started": started, "stopped": started + 3600}
}

func TestHistoryPagingAndChecks(t *testing.T) {
	base := int64(1_750_000_000)
	var rows []map[string]any
	for i := 1; i <= 25; i++ {
		rk := []string{"101", "102", "103"}[i%3]
		rows = append(rows, playRow(i, rk, "plex://movie/"+rk, i%2, base+int64(i)*86400))
	}
	rows = append(rows, map[string]any{"row_id": nil, "rating_key": "101", "state": "playing"}) // live session
	rows[3]["guid"] = "plex://movie/abc&lt;x&gt;"
	h := &historyServer{rows: rows}
	f := newFake(t, h.handle)
	c := clientFor(f, "")
	got, err := c.History(context.Background(), HistoryFilter{RatingKeys: []string{"101", "102"}, PageSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, r := range rows[:25] {
		if r["rating_key"] != "103" {
			want++
		}
	}
	if len(got) != want {
		t.Fatalf("%d rows, want %d", len(got), want)
	}
	for _, p := range []string{"grouping", "include_activity"} {
		if h.lastQry.Get(p) != "0" {
			t.Fatalf("%s = %q", p, h.lastQry.Get(p))
		}
	}
	if h.lastQry.Get("order_dir") != "asc" || h.lastQry.Get("rating_key") != "101,102" {
		t.Fatalf("query %v", h.lastQry)
	}
	all, err := c.History(context.Background(), HistoryFilter{SectionID: "1"})
	if err != nil || len(all) != 25 {
		t.Fatalf("section read: %d rows, %v", len(all), err)
	}
	if all[3].GUID != "plex://movie/abc<x>" || all[0].PlayedAt().Unix() != base+86400+3600 {
		t.Fatalf("row %+v", all[3])
	}

	fail := func(name string, mutate func(q url.Values, total int, rows []map[string]any) (int, []map[string]any), filter HistoryFilter) {
		t.Helper()
		h.mutate = mutate
		defer func() { h.mutate = nil }()
		if filter.PageSize == 0 {
			filter.PageSize = 4
		}
		_, err := c.History(context.Background(), filter)
		if !errors.Is(err, ErrIncomplete) {
			t.Fatalf("%s: err = %v, want ErrIncomplete", name, err)
		}
		if Transient(err) {
			t.Fatalf("%s: incomplete reads are not retried", name)
		}
	}
	keys := HistoryFilter{RatingKeys: []string{"101", "102"}}
	fail("short page", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		if q.Get("start") == "4" {
			return total, rows[:2]
		}
		return total, rows
	}, keys)
	fail("empty page before the end", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		if q.Get("start") == "8" {
			return total, nil
		}
		return total, rows
	}, keys)
	fail("unrequested key", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		out := append([]map[string]any(nil), rows...)
		out = append(out[:len(out)-1], playRow(999, "555", "plex://x", 1, base))
		return total, out
	}, keys)
	var last map[string]any // the last row of the first page
	fail("a page repeats a row of the previous one", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		switch q.Get("start") {
		case "0":
			last = rows[len(rows)-1]
		case "4": // the row at position 4 is skipped, the previous page's last row comes again
			out := append([]map[string]any{last}, rows[1:]...)
			return total, out
		}
		return total, rows
	}, keys)
	fail("shrinking history", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		if q.Get("start") != "0" {
			return total - 1, rows
		}
		return total, rows
	}, keys)
	fail("more rows than asked", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		return total, append(append([]map[string]any(nil), rows...), rows...)
	}, keys)
	fail("row cap", nil, HistoryFilter{SectionID: "1", MaxRows: 10})
	fail("no rating key", func(q url.Values, total int, rows []map[string]any) (int, []map[string]any) {
		out := append([]map[string]any(nil), rows...)
		out[0] = map[string]any{"row_id": 77, "rating_key": ""}
		return total, out
	}, HistoryFilter{SectionID: "1"})

	if _, err := c.History(context.Background(), HistoryFilter{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unfiltered read: %v", err)
	}
	if _, err := c.History(context.Background(), HistoryFilter{RatingKeys: []string{"1,2"}}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad key: %v", err)
	}
}

// TestHistoryShapesAreNeverEmpty: data that does not look like get_history's is an error, never an
// empty history ("no plays").
func TestHistoryShapesAreNeverEmpty(t *testing.T) {
	for name, data := range map[string]string{
		"object without rows": `{}`,
		"list":                `[]`,
		"null":                `null`,
		"rows without total":  `{"data":[]}`,
		"total without rows":  `{"recordsFiltered":0}`,
		"rows not a list":     `{"recordsFiltered":0,"data":{}}`,
		"total not a number":  `{"recordsFiltered":"many","data":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"response":{"result":"success","message":null,"data":` + data + `}}`))
			})
			c := clientFor(f, "")
			if _, err := c.History(context.Background(), HistoryFilter{RatingKeys: []string{"1"}}); err == nil {
				t.Fatal("no error")
			}
			if _, err := c.FirstPlay(context.Background(), "1"); err == nil {
				t.Fatal("FirstPlay: no error")
			}
		})
	}
	// A genuinely empty history is fine.
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, cmd string) {
		ok(w, map[string]any{"recordsFiltered": "0", "recordsTotal": 0, "data": []any{}})
	})
	rows, err := clientFor(f, "").History(context.Background(), HistoryFilter{RatingKeys: []string{"1"}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows %v, err %v", rows, err)
	}
	first, err := clientFor(f, "").FirstPlay(context.Background(), "1")
	if err != nil || first != nil {
		t.Fatalf("first %v, err %v", first, err)
	}
}

func TestFirstPlay(t *testing.T) {
	h := &historyServer{rows: []map[string]any{playRow(5, "101", "plex://a", 1, 1_700_000_000), playRow(6, "102", "plex://b", 1, 1_700_100_000)}}
	f := newFake(t, h.handle)
	row, err := clientFor(f, "").FirstPlay(context.Background(), "1")
	if err != nil || row == nil || row.RatingKey != "101" || row.Started.Unix() != 1_700_000_000 {
		t.Fatalf("row %+v, err %v", row, err)
	}
	if h.lastQry.Get("length") != "1" || h.lastQry.Get("order_column") != "date" || h.lastQry.Get("section_id") != "1" {
		t.Fatalf("query %v", h.lastQry)
	}
}

func TestTransient(t *testing.T) {
	for err, want := range map[error]bool{
		&HTTPError{StatusCode: 503}:                   true,
		&HTTPError{StatusCode: 502}:                   true,
		&HTTPError{StatusCode: 400}:                   false,
		fmt.Errorf("x: %w", context.DeadlineExceeded): true,
		fmt.Errorf("x: %w", context.Canceled):         false,
		ErrUnauthorized:                               false,
		ErrTooOld:                                     false,
		ErrIncomplete:                                 false,
		errors.New("connection refused"):              true,
	} {
		if got := Transient(err); got != want {
			t.Errorf("Transient(%v) = %v, want %v", err, got, want)
		}
	}
}
