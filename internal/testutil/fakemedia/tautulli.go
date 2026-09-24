package fakemedia

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServerTautulli is the fake Tautulli's name in the request log and Options.Addrs.
const ServerTautulli = "tautulli"

// RuleTautulliKeyInURL: the Tautulli API key was sent as the ?apikey= query parameter. Dupearr
// sends it in the X-Api-Key header only (Tautulli 2.18.0+): a URL ends up in logs and proxies.
const RuleTautulliKeyInURL = "tautulli_apikey_query_param"

// TautulliMode switches the fake Tautulli into failure or legacy behaviour (Env.SetTautulliMode).
type TautulliMode struct {
	// OldVersion emulates Tautulli before 2.18.0: version v2.17.2, and the X-Api-Key header is
	// not read — without ?apikey= every command answers 401 "Parameter apikey is required".
	OldVersion bool
	// WrongIdentity reports another Plex server's machine identifier (get_server_info).
	WrongIdentity bool
	// ResultError answers get_history with HTTP 200 and result "error".
	ResultError bool
	// ShortPage returns one row less than requested on a full page that is not the last one.
	ShortPage bool
	// IgnoreKeyList matches a comma-separated rating_key literally (no list support).
	IgnoreKeyList bool
	// Down answers every request with 503.
	Down bool
}

// tautulliState is the fake Tautulli: its users and the recorded plays (session_history).
type tautulliState struct {
	apiKey, version string
	users           []tautulliUser
	rows            []*historyRow // started order
	mode            TautulliMode
	nextRowID       int64
	noHistory       map[string]bool // section key → keep_history 0
}

type tautulliUser struct {
	id       int64
	name     string
	active   bool
	keepHist bool
}

// historyRow is one session_history row.
type historyRow struct {
	id              int64
	ratingKey, guid string
	section         string
	title           string
	userID          int64
	user            string
	started         time.Time
	stopped         time.Time
}

// buildTautulli records the scenario's plays (after the Plex items exist).
func (w *world) buildTautulli(sc *Scenario, playsOf map[*item][]Play) {
	t := &tautulliState{apiKey: sc.Tautulli.APIKey, version: sc.Tautulli.Version, noHistory: map[string]bool{}, nextRowID: 1}
	if t.apiKey == "" {
		t.apiKey = DefaultTautulliAPIKey
	}
	if t.version == "" {
		t.version = DefaultTautulliVersion
	}
	users := sc.Tautulli.Users
	if users == nil {
		users = DefaultTautulliUsers()
	}
	byName := map[string]*tautulliUser{}
	for i, u := range users {
		tu := tautulliUser{id: int64(1000 + i), name: u.Name, active: !u.Inactive, keepHist: !u.NoHistory}
		if i == 0 {
			tu.id = 1 // the Plex owner
		}
		t.users = append(t.users, tu)
		byName[u.Name] = &t.users[len(t.users)-1]
	}
	for _, l := range sc.Libraries {
		t.noHistory[l.Key] = l.NoHistory
	}
	var rows []*historyRow
	for _, it := range w.plex.order {
		for _, p := range playsOf[it] {
			u := byName[p.User]
			if u == nil || !u.keepHist || t.noHistory[it.sec.key] {
				continue // Tautulli records nothing for them
			}
			minutes := p.Minutes
			if minutes == 0 {
				minutes = 90
			}
			rk := it.rk
			if p.RetiredKey != "" {
				rk = p.RetiredKey
			}
			started := w.start.Add(-p.Ago)
			rows = append(rows, &historyRow{ratingKey: rk, guid: it.guid, section: it.sec.key, title: it.title,
				userID: u.id, user: u.name, started: started, stopped: started.Add(time.Duration(minutes) * time.Minute)})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].started.Before(rows[j].started) })
	for _, r := range rows {
		r.id = t.nextRowID
		t.nextRowID++
	}
	t.rows = rows
	w.tautulli = t
}

// tautulliAPI serves the fake Tautulli's /api/v2.
type tautulliAPI struct {
	e *Env
	w *world
}

func (e *Env) tautulliHandler() http.Handler {
	h := &tautulliAPI{e: e, w: e.w}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2", h.api)
	mux.HandleFunc("POST /api/v2", h.api)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>404 Not Found</body></html>"))
	})
	return mux
}

// tautulliAnswer writes Tautulli's envelope.
func tautulliAnswer(w http.ResponseWriter, status int, result string, message any, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": result, "message": message, "data": data}})
}

// escape HTML-escapes a string like Tautulli's API output does.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// api dispatches a command. Authentication follows Tautulli: ?apikey= wins; since 2.18.0 the
// X-Api-Key header is read when there is no parameter.
func (h *tautulliAPI) api(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	t := h.w.tautulli
	if t.mode.Down {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html><body>503 Service Unavailable</body></html>"))
		return
	}
	q := r.URL.Query()
	key, viaQuery := q.Get("apikey"), q.Has("apikey")
	if viaQuery {
		h.w.violate(ServerTautulli, r, RuleTautulliKeyInURL, "the API key must only travel in the X-Api-Key header")
	} else if !t.mode.OldVersion {
		key = r.Header.Get("X-Api-Key")
	}
	switch {
	case key == "" && t.mode.OldVersion:
		tautulliAnswer(w, http.StatusUnauthorized, "error", "Parameter apikey is required", map[string]any{})
		return
	case key == "": // 2.18.0 and later name the header too
		tautulliAnswer(w, http.StatusUnauthorized, "error", "Parameter apikey is required or X-Api-Key header is required", map[string]any{})
		return
	case subtle.ConstantTimeCompare([]byte(key), []byte(t.apiKey)) != 1:
		tautulliAnswer(w, http.StatusUnauthorized, "error", "Invalid apikey", map[string]any{})
		return
	}
	switch cmd := q.Get("cmd"); cmd {
	case "get_tautulli_info":
		v := t.version
		if t.mode.OldVersion {
			v = "v2.17.2"
		}
		tautulliAnswer(w, http.StatusOK, "success", nil, map[string]any{
			"tautulli_version": v, "tautulli_branch": "master", "tautulli_install_type": "docker",
			"tautulli_platform": "Linux", "tautulli_python_version": "3.12.4",
		})
	case "get_server_info":
		id := h.w.plex.machineID
		if t.mode.WrongIdentity {
			id = "0000000000000000000000000000000000000000"
		}
		tautulliAnswer(w, http.StatusOK, "success", nil, map[string]any{
			"pms_identifier": id, "pms_name": escape(h.w.plex.friendlyName), "pms_version": h.w.plex.version,
			"pms_platform": "Linux", "pms_ip": "127.0.0.1", "pms_port": 32400, "pms_is_remote": 0, "pms_ssl": 0,
			"pms_url": "http://127.0.0.1:32400", "pms_url_manual": 0,
		})
	case "get_users":
		out := []map[string]any{}
		for _, u := range t.users {
			out = append(out, map[string]any{
				"row_id": u.id, "user_id": u.id, "username": escape(u.name), "friendly_name": escape(u.name),
				"email": u.name + "@example.invalid", "is_active": b2i(u.active), "is_admin": b2i(u.id == 1),
				"keep_history": b2i(u.keepHist), "is_home_user": 0, "do_notify": 1, "allow_guest": 0,
			})
		}
		tautulliAnswer(w, http.StatusOK, "success", nil, out)
	case "get_library":
		h.library(w, q.Get("section_id"))
	case "get_history":
		h.history(w, r)
	default:
		tautulliAnswer(w, http.StatusOK, "error", "Unknown command: "+escape(cmd), map[string]any{})
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// library answers get_library; like Tautulli, an unknown section gets the "Local" defaults.
func (h *tautulliAPI) library(w http.ResponseWriter, sectionID string) {
	t := h.w.tautulli
	for _, s := range h.w.plex.sections {
		if s.key == sectionID {
			n := 0
			for _, it := range h.w.plex.order {
				if it.sec == s && (it.typ == "movie" || it.typ == "show") {
					n++
				}
			}
			tautulliAnswer(w, http.StatusOK, "success", nil, map[string]any{
				"row_id": s.key, "server_id": h.w.plex.machineID, "section_id": s.key, "section_name": escape(s.title),
				"section_type": s.typ, "count": n, "is_active": 1, "do_notify": 1, "do_notify_created": 1,
				"keep_history": b2i(!t.noHistory[s.key]), "deleted_section": 0,
			})
			return
		}
	}
	tautulliAnswer(w, http.StatusOK, "success", nil, map[string]any{
		"row_id": 0, "server_id": "", "section_id": 0, "section_name": "Local", "section_type": "",
		"count": 0, "is_active": 1, "do_notify": 0, "do_notify_created": 0, "keep_history": 1, "deleted_section": 0,
	})
}

// truthy reads a Tautulli boolean parameter (absent = def).
func truthy(q string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "":
		return def
	case "0", "false", "no":
		return false
	}
	return true
}

// history answers get_history like Tautulli's datatables query: filters (rating_key list, guid
// prefix, section_id, user_id), grouping and live activity on unless turned off, ordering by date,
// start/length paging (default 25) and HTML-escaped strings.
func (h *tautulliAPI) history(w http.ResponseWriter, r *http.Request) {
	t := h.w.tautulli
	if t.mode.ResultError {
		tautulliAnswer(w, http.StatusOK, "error", "Unable to execute database query.", nil)
		return
	}
	q := r.URL.Query()
	keys := map[string]bool{}
	if rk := q.Get("rating_key"); rk != "" {
		if t.mode.IgnoreKeyList {
			keys[rk] = true
		} else {
			for _, k := range strings.Split(rk, ",") {
				if k = strings.TrimSpace(k); k != "" {
					keys[k] = true
				}
			}
		}
	}
	guid, _, _ := strings.Cut(q.Get("guid"), "?")
	section, user := q.Get("section_id"), q.Get("user_id")
	match := func(rk, g, sec string, uid int64) bool {
		return (len(keys) == 0 || keys[rk]) && (guid == "" || strings.HasPrefix(g, guid)) &&
			(section == "" || sec == section) && (user == "" || user == strconv.FormatInt(uid, 10))
	}
	type out struct {
		row   *historyRow
		live  bool
		count int
	}
	var rows []out
	for _, hr := range t.rows {
		if !match(hr.ratingKey, hr.guid, hr.section, hr.userID) {
			continue
		}
		if truthy(q.Get("grouping"), true) && len(rows) > 0 {
			// Grouping links consecutive sessions of one user and item into one row.
			if last := &rows[len(rows)-1]; !last.live && last.row.userID == hr.userID && last.row.ratingKey == hr.ratingKey {
				last.count++
				continue
			}
		}
		rows = append(rows, out{row: hr, count: 1})
	}
	if truthy(q.Get("include_activity"), true) {
		for _, rk := range h.w.plex.playing {
			it := h.w.plex.items[rk]
			if it == nil || !match(rk, it.guid, it.sec.key, 1) {
				continue
			}
			rows = append(rows, out{live: true, row: &historyRow{ratingKey: rk, guid: it.guid, section: it.sec.key, title: it.title,
				userID: 1, user: t.users[0].name, started: h.w.now().UTC()}})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].row.started.Before(rows[j].row.started) })
	if !strings.EqualFold(q.Get("order_dir"), "asc") {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	start, _ := strconv.Atoi(q.Get("start"))
	length := 25
	if l, err := strconv.Atoi(q.Get("length")); err == nil && l > 0 {
		length = l
	}
	start = max(start, 0)
	page := rows[min(start, len(rows)):min(len(rows), start+length)]
	if t.mode.ShortPage && len(page) == length && start+length < len(rows) {
		page = page[:len(page)-1]
	}
	data := make([]map[string]any, 0, len(page))
	for _, o := range page {
		hr := o.row
		row := map[string]any{
			"reference_id": hr.id, "row_id": hr.id, "id": hr.id, "date": hr.started.Unix(), "started": hr.started.Unix(),
			"stopped": hr.stopped.Unix(), "duration": int(hr.stopped.Sub(hr.started).Seconds()), "user_id": hr.userID,
			"user": escape(hr.user), "friendly_name": escape(hr.user), "platform": "Chrome", "player": "Plex Web",
			"media_type": "movie", "rating_key": hr.ratingKey, "parent_rating_key": "", "grandparent_rating_key": "",
			"full_title": escape(hr.title), "title": escape(hr.title), "guid": escape(hr.guid), "section_id": hr.section,
			"percent_complete": 100, "watched_status": 1, "group_count": o.count, "state": nil,
		}
		if o.live {
			row["row_id"], row["reference_id"], row["id"], row["state"], row["stopped"] = nil, nil, nil, "playing", 0
		}
		data = append(data, row)
	}
	tautulliAnswer(w, http.StatusOK, "success", nil, map[string]any{
		"draw": 1, "recordsTotal": len(t.rows), "recordsFiltered": len(rows), "data": data,
		"total_duration": "0 mins", "filter_duration": "0 mins",
	})
}

// ---------------------------------------------------------------------------
// Controls
// ---------------------------------------------------------------------------

// SetTautulliMode switches the fake Tautulli's failure / legacy behaviour.
func (e *Env) SetTautulliMode(m TautulliMode) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.tautulli.mode = m
}

// AddPlay records a play of the movie or episode with rating key rk by user, started ago before
// now, in the item's library.
func (e *Env) AddPlay(rk, user string, ago time.Duration) error {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	t := e.w.tautulli
	it := e.w.plex.items[rk]
	if it == nil {
		return fmt.Errorf("fakemedia: no item with rating key %q", rk)
	}
	var u *tautulliUser
	for i := range t.users {
		if t.users[i].name == user {
			u = &t.users[i]
		}
	}
	if u == nil {
		return fmt.Errorf("fakemedia: no Tautulli user %q", user)
	}
	started := e.w.now().UTC().Add(-ago)
	t.rows = append(t.rows, &historyRow{id: t.nextRowID, ratingKey: rk, guid: it.guid, section: it.sec.key, title: it.title,
		userID: u.id, user: u.name, started: started, stopped: started.Add(90 * time.Minute)})
	t.nextRowID++
	sort.SliceStable(t.rows, func(i, j int) bool { return t.rows[i].started.Before(t.rows[j].started) })
	return nil
}

// TautulliPlays returns the number of plays recorded under a rating key.
func (e *Env) TautulliPlays(rk string) int {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	n := 0
	for _, r := range e.w.tautulli.rows {
		if r.ratingKey == rk {
			n++
		}
	}
	return n
}
