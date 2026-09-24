package fakemedia

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// tautulliGet calls the fake Tautulli's /api/v2 with the key in the X-Api-Key header.
func tautulliGet(t *testing.T, env *Env, params url.Values, header bool) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.Tautulli.URL+"/api/v2?"+params.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if header {
		req.Header.Set("X-Api-Key", env.TautulliAPIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var env2 struct {
		Response map[string]any `json:"response"`
	}
	_ = json.Unmarshal(b, &env2)
	return resp.StatusCode, env2.Response
}

func historyOf(t *testing.T, env *Env, params url.Values) (total int, rows []map[string]any) {
	t.Helper()
	params.Set("cmd", "get_history")
	code, resp := tautulliGet(t, env, params, true)
	if code != http.StatusOK || resp["result"] != "success" {
		t.Fatalf("get_history %v: %d %v", params, code, resp)
	}
	data := resp["data"].(map[string]any)
	for _, r := range data["data"].([]any) {
		rows = append(rows, r.(map[string]any))
	}
	return int(data["recordsFiltered"].(float64)), rows
}

func TestTautulliFakeIdentityUsersLibraries(t *testing.T) {
	sc := Watch()
	sc.Libraries[1].NoHistory = true
	sc.Tautulli.Users = []TautulliUser{{Name: "fakeowner"}, {Name: "alice"}, {Name: "bob", NoHistory: true}, {Name: "carol", Inactive: true}}
	env := Start(t, sc)
	code, resp := tautulliGet(t, env, url.Values{"cmd": {"get_server_info"}}, true)
	if code != 200 || resp["data"].(map[string]any)["pms_identifier"] != env.MachineIdentifier {
		t.Fatalf("server info %d %v", code, resp)
	}
	_, resp = tautulliGet(t, env, url.Values{"cmd": {"get_tautulli_info"}}, true)
	if resp["data"].(map[string]any)["tautulli_version"] != DefaultTautulliVersion {
		t.Fatalf("info %v", resp)
	}
	_, resp = tautulliGet(t, env, url.Values{"cmd": {"get_users"}}, true)
	users := resp["data"].([]any)
	if len(users) != 4 || users[2].(map[string]any)["keep_history"].(float64) != 0 || users[3].(map[string]any)["is_active"].(float64) != 0 {
		t.Fatalf("users %v", users)
	}
	_, resp = tautulliGet(t, env, url.Values{"cmd": {"get_library"}, "section_id": {SectionMovies4K}}, true)
	if l := resp["data"].(map[string]any); l["keep_history"].(float64) != 0 || l["section_id"] != SectionMovies4K {
		t.Fatalf("library %v", l)
	}
	// An unknown section gets Tautulli's "Local" defaults (section 0, history kept).
	_, resp = tautulliGet(t, env, url.Values{"cmd": {"get_library"}, "section_id": {"77"}}, true)
	if l := resp["data"].(map[string]any); l["section_id"].(float64) != 0 || l["keep_history"].(float64) != 1 {
		t.Fatalf("unknown library %v", l)
	}
	// No plays are recorded for a user or library without history: Mad Max (4K) and bob.
	if total, _ := historyOf(t, env, url.Values{"section_id": {SectionMovies4K}, "grouping": {"0"}}); total != 0 {
		t.Fatalf("plays in a library without history: %d", total)
	}
	_, rows := historyOf(t, env, url.Values{"grouping": {"0"}, "length": {"100"}, "section_id": {SectionMovies}})
	for _, r := range rows {
		if r["user"] == "bob" {
			t.Fatalf("a play of a user without history: %v", r)
		}
	}
}

func TestTautulliFakeHistorySemantics(t *testing.T) {
	env := Start(t, Watch())
	sicario := env.RatingKey(SectionMovies, "Sicario")
	arrival := env.RatingKey(SectionMovies, "Arrival")
	// Grouping is on by default: alice's two Sicario plays become one row.
	total, rows := historyOf(t, env, url.Values{"rating_key": {sicario}, "include_activity": {"0"}})
	if total != 1 || rows[0]["group_count"].(float64) != 2 {
		t.Fatalf("grouped: %d %v", total, rows)
	}
	total, _ = historyOf(t, env, url.Values{"rating_key": {sicario}, "grouping": {"0"}, "include_activity": {"0"}})
	if total != 2 {
		t.Fatalf("ungrouped: %d", total)
	}
	// Live activity is on by default: a session in progress has no row id.
	env.SetPlaying(sicario)
	total, rows = historyOf(t, env, url.Values{"rating_key": {sicario}, "grouping": {"0"}})
	live := 0
	for _, r := range rows {
		if r["row_id"] == nil {
			live++
		}
	}
	if total != 3 || live != 1 {
		t.Fatalf("with activity: %d rows, %d live", total, live)
	}
	env.SetPlaying()
	// A comma-separated rating key list; paging with start/length and recordsFiltered; asc order.
	keys := url.Values{"rating_key": {sicario + "," + arrival}, "grouping": {"0"}, "include_activity": {"0"},
		"order_column": {"date"}, "order_dir": {"asc"}, "length": {"2"}}
	total, page1 := historyOf(t, env, keys)
	keys.Set("start", "2")
	_, page2 := historyOf(t, env, keys)
	keys.Set("start", "4")
	_, page3 := historyOf(t, env, keys)
	if total != 5 || len(page1) != 2 || len(page2) != 2 || len(page3) != 1 {
		t.Fatalf("paging: total %d pages %d/%d/%d", total, len(page1), len(page2), len(page3))
	}
	if page1[0]["started"].(float64) > page1[1]["started"].(float64) {
		t.Fatal("not in ascending order")
	}
	// The default page is 25 rows.
	_, rows = historyOf(t, env, url.Values{"grouping": {"0"}, "section_id": {SectionMovies}})
	if len(rows) > 25 {
		t.Fatalf("default page: %d rows", len(rows))
	}
	// guid matches as a prefix (SQL LIKE 'guid%').
	guid := rows[0]["guid"].(string)
	total, _ = historyOf(t, env, url.Values{"guid": {guid[:len(guid)-3]}, "grouping": {"0"}})
	if total == 0 {
		t.Fatal("guid prefix did not match")
	}
	// Plays under an earlier Plex item of Blade Runner 4K.
	if n := env.TautulliPlays("9001"); n != 1 {
		t.Fatalf("retired key plays = %d", n)
	}
}

func TestTautulliFakeModesAndViolations(t *testing.T) {
	env := Start(t, Watch())
	// The key in the URL works, like the real Tautulli, but is a violation.
	code, resp := tautulliGet(t, env, url.Values{"cmd": {"get_users"}, "apikey": {env.TautulliAPIKey}}, false)
	if code != 200 || resp["result"] != "success" {
		t.Fatalf("query key: %d %v", code, resp)
	}
	vs := env.Violations()
	if len(vs) != 1 || vs[0].Rule != RuleTautulliKeyInURL {
		t.Fatalf("violations %v", vs)
	}
	// No key at all: Tautulli 2.18+ names the header too.
	code, resp = tautulliGet(t, env, url.Values{"cmd": {"get_users"}}, false)
	if code != http.StatusUnauthorized || !strings.Contains(resp["message"].(string), "X-Api-Key header is required") {
		t.Fatalf("no key: %d %v", code, resp)
	}
	// A wrong key.
	if code, _ := tautulliGet(t, &Env{Tautulli: env.Tautulli, TautulliAPIKey: "wrong"}, url.Values{"cmd": {"get_users"}}, true); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", code)
	}
	// Tautulli before 2.18.0 ignores the header.
	env.SetTautulliMode(TautulliMode{OldVersion: true})
	code, resp = tautulliGet(t, env, url.Values{"cmd": {"get_tautulli_info"}}, true)
	if code != http.StatusUnauthorized || !strings.Contains(resp["message"].(string), "apikey is required") {
		t.Fatalf("old version: %d %v", code, resp)
	}
	env.SetTautulliMode(TautulliMode{WrongIdentity: true})
	_, resp = tautulliGet(t, env, url.Values{"cmd": {"get_server_info"}}, true)
	if resp["data"].(map[string]any)["pms_identifier"] == env.MachineIdentifier {
		t.Fatal("identity knob ignored")
	}
	env.SetTautulliMode(TautulliMode{ResultError: true})
	code, resp = tautulliGet(t, env, url.Values{"cmd": {"get_history"}, "section_id": {SectionMovies}}, true)
	if code != 200 || resp["result"] != "error" {
		t.Fatalf("result error: %d %v", code, resp)
	}
	env.SetTautulliMode(TautulliMode{ShortPage: true})
	_, rows := historyOf(t, env, url.Values{"section_id": {SectionMovies}, "grouping": {"0"}, "length": {"2"}})
	if len(rows) != 1 {
		t.Fatalf("short page: %d rows", len(rows))
	}
	env.SetTautulliMode(TautulliMode{Down: true})
	if code, _ := tautulliGet(t, env, url.Values{"cmd": {"get_users"}}, true); code != http.StatusServiceUnavailable {
		t.Fatalf("down: %d", code)
	}
	env.SetTautulliMode(TautulliMode{})
	if err := env.AddPlay(env.RatingKey(SectionMovies4K, "Arrival"), "alice", 1); err != nil {
		t.Fatal(err)
	}
	if env.TautulliPlays(env.RatingKey(SectionMovies4K, "Arrival")) != 1 {
		t.Fatal("AddPlay did not record the play")
	}
}
