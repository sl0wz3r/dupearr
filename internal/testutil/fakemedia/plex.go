package fakemedia

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
)

// plexAPI serves the fake Plex Media Server.
type plexAPI struct {
	e *Env
	w *world
}

func (e *Env) plexHandler() http.Handler {
	h := &plexAPI{e: e, w: e.w}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /identity", h.identity)
	mux.HandleFunc("GET /{$}", h.root)
	mux.HandleFunc("GET /:/prefs", h.prefs)
	mux.HandleFunc("GET /:/prefs/get", h.prefs)
	mux.HandleFunc("GET /library/sections", h.sections)
	mux.HandleFunc("GET /library/sections/all", h.sections)
	mux.HandleFunc("GET /library/sections/{id}/all", h.sectionAll)
	mux.HandleFunc("PUT /library/sections/{id}/all", h.bulkEdit)
	mux.HandleFunc("GET /library/sections/{id}/refresh", h.sectionRefresh)
	mux.HandleFunc("POST /library/sections/{id}/refresh", h.sectionRefresh)
	mux.HandleFunc("DELETE /library/sections/{id}/refresh", h.cancelRefresh)
	mux.HandleFunc("PUT /library/sections/{id}/emptyTrash", h.emptyTrash)
	mux.HandleFunc("DELETE /library/sections/{id}", h.deleteSection)
	mux.HandleFunc("GET /library/metadata/{ids}", h.metadata)
	mux.HandleFunc("GET /library/metadata/{rk}/children", h.children)
	mux.HandleFunc("GET /library/metadata/{rk}/allLeaves", h.allLeaves)
	mux.HandleFunc("GET /library/metadata/{rk}/thumb/{ts}", h.thumb)
	mux.HandleFunc("GET /library/metadata/{rk}/art/{ts}", h.thumb)
	mux.HandleFunc("DELETE /library/metadata/{ids}", h.deleteItem)
	mux.HandleFunc("DELETE /library/metadata/{ids}/{element}", h.deleteElement)
	mux.HandleFunc("DELETE /library/metadata/{ids}/media/{mediaId}", h.deleteMedia)
	mux.HandleFunc("DELETE /library/metadata/", h.malformedDelete)
	mux.HandleFunc("PUT /library/metadata/{ids}/refresh", h.refreshItem)
	mux.HandleFunc("PUT /library/metadata/{ids}/merge", h.mergeSplit)
	mux.HandleFunc("PUT /library/metadata/{ids}/split", h.mergeSplit)
	mux.HandleFunc("GET /status/sessions", h.sessions)
	mux.HandleFunc("GET /photo/:/transcode", h.photo)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { plexError(w, http.StatusNotFound) })
	return h.auth(h.canonicalDeletes(mux))
}

// canonicalDeletes rejects DELETEs under /library/ whose path is not canonical (empty segments as
// in /library/metadata//media/5, dot segments, a trailing slash as in /library/metadata/1/media/):
// which PMS handler such a URL reaches is UNVERIFIED (PMS treats every request as though it had a
// trailing slash), so Dupearr must never emit one (docs/research/plex-api.md §9.2). Without this
// check Go's ServeMux would answer with a redirect to the cleaned path, which an HTTP client follows
// with the same method — possibly onto a different, destructive endpoint.
func (h *plexAPI) canonicalDeletes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; r.Method == http.MethodDelete && strings.HasPrefix(p, "/library/") && path.Clean(p) != p {
			h.w.mu.Lock()
			h.w.violate(ServerPlex, r, RulePlexMalformedDelete, "non-canonical DELETE path (empty or dot segment, or trailing slash)")
			h.w.mu.Unlock()
			plexError(w, http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth enforces X-Plex-Token (header or query) on everything except /identity.
func (h *plexAPI) auth(next http.Handler) http.Handler {
	token := []byte(h.w.plex.token) // immutable after build
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity" {
			got := r.Header.Get("X-Plex-Token")
			if got == "" {
				got = r.URL.Query().Get("X-Plex-Token")
			}
			if subtle.ConstantTimeCompare([]byte(got), token) != 1 {
				plexError(w, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// plexError writes Plex's bare HTML error page (error bodies are not JSON, docs/research §2.1).
func plexError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(code)
	text := http.StatusText(code)
	_, _ = fmt.Fprintf(w, "<html><head><title>%s</title></head><body><h1>%d %s</h1></body></html>", text, code, text)
}

// plexOK writes the empty 200 text/html body of Plex action endpoints.
func plexOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
}

// acceptJSON enforces Accept: application/json on JSON endpoints (real Plex answers XML).
func (h *plexAPI) acceptJSON(w http.ResponseWriter, r *http.Request) bool {
	if h.e.lenient || strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		return true
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotAcceptable)
	_, _ = io.WriteString(w, "fakemedia: this fake Plex only speaks JSON; send Accept: application/json (a real server would answer XML)\n")
	return false
}

func writeMediaContainer(w http.ResponseWriter, mc map[string]any) {
	b, err := json.Marshal(map[string]any{"MediaContainer": mc})
	if err != nil {
		plexError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (h *plexAPI) identity(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	p := h.w.plex
	writeMediaContainer(w, map[string]any{
		"size": 0, "apiVersion": "1.1.1", "claimed": true,
		"machineIdentifier": p.machineID, "version": p.version,
	})
}

func (h *plexAPI) root(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	p := h.w.plex
	mc := map[string]any{
		"size":                          5,
		"allowCameraUpload":             false,
		"allowChannelAccess":            true,
		"allowSharing":                  true,
		"allowSync":                     true,
		"allowTuners":                   false,
		"backgroundProcessing":          true,
		"certificate":                   true,
		"companionProxy":                true,
		"countryCode":                   "usa",
		"diagnostics":                   "logs,databases,streaminglogs",
		"eventStream":                   true,
		"friendlyName":                  p.friendlyName,
		"hubSearch":                     true,
		"itemClusters":                  true,
		"livetv":                        7,
		"machineIdentifier":             p.machineID,
		"mediaProviders":                true,
		"multiuser":                     true,
		"musicAnalysis":                 2,
		"myPlex":                        true,
		"myPlexMappingState":            "mapped",
		"myPlexSigninState":             "ok",
		"myPlexSubscription":            true,
		"myPlexUsername":                p.owner,
		"offlineTranscode":              1,
		"ownerFeatures":                 "hdr_transcoding,hevc_encoding,webhooks,sync,trailers",
		"platform":                      "Linux",
		"platformVersion":               "6.1.0-fake",
		"pluginHost":                    true,
		"pushNotifications":             false,
		"readOnlyLibraries":             false,
		"streamingBrainABRVersion":      3,
		"streamingBrainVersion":         2,
		"sync":                          true,
		"transcoderActiveVideoSessions": 0,
		"transcoderAudio":               true,
		"transcoderLyrics":              true,
		"transcoderPhoto":               true,
		"transcoderSubtitles":           true,
		"transcoderVideo":               true,
		"transcoderVideoBitrates":       "64,96,208,320,720,1500,2000,3000,4000,8000,10000,12000,20000",
		"transcoderVideoQualities":      "0,1,2,3,4,5,6,7,8,9,10,11,12",
		"transcoderVideoResolutions":    "128,128,160,240,320,480,768,720,720,1080,1080,1080,1080",
		"updatedAt":                     h.w.start.Unix(),
		"updater":                       true,
		"version":                       p.version,
		"voiceSearch":                   true,
		"Directory": []any{
			map[string]any{"count": 1, "key": "activities", "title": "activities"},
			map[string]any{"count": 1, "key": "library", "title": "library"},
			map[string]any{"count": 1, "key": "playlists", "title": "playlists"},
			map[string]any{"count": 1, "key": "search", "title": "search"},
			map[string]any{"count": 1, "key": "status", "title": "status"},
		},
	}
	// PMS omits the attribute when the setting is off (docs/research/plex-api.md §4.2).
	if p.allowDeletion {
		mc["allowMediaDeletion"] = true
	}
	writeMediaContainer(w, mc)
}

func (h *plexAPI) prefs(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	p := h.w.plex
	settings := []any{
		map[string]any{
			"id": "allowMediaDeletion", "label": "Allow media deletion",
			"summary": "The owner of the server will be allowed to delete media files from disk.",
			"type":    "bool", "default": false, "value": p.allowDeletion, "hidden": false, "advanced": true, "group": "library",
		},
		map[string]any{
			"id": "autoEmptyTrash", "label": "Empty trash automatically after every scan",
			"summary": "", "type": "bool", "default": false, "value": p.autoEmptyTrash, "hidden": false, "advanced": false, "group": "library",
		},
		map[string]any{
			"id": "FriendlyName", "label": "Friendly name", "summary": "", "type": "text", "default": "",
			"value": p.friendlyName, "hidden": false, "advanced": false, "group": "general",
		},
	}
	id := r.URL.Query().Get("id")
	if id == "" && strings.HasSuffix(r.URL.Path, "/get") {
		plexError(w, http.StatusBadRequest)
		return
	}
	if id != "" {
		var sel []any
		for _, s := range settings {
			if strings.EqualFold(s.(map[string]any)["id"].(string), id) {
				sel = append(sel, s)
			}
		}
		if len(sel) == 0 {
			plexError(w, http.StatusNotFound)
			return
		}
		settings = sel
	}
	writeMediaContainer(w, map[string]any{"size": len(settings), "Setting": settings})
}

// sectionAgent returns a section's agent. The legacy disc-image scanners keep the modern agent in
// the fake (items keep plex:// guids); which agent/guid combination real PMS uses with a legacy
// scanner is not modelled.
func sectionAgent(typ string) string {
	if typ == LibraryShow {
		return "tv.plex.agents.series"
	}
	return "tv.plex.agents.movie"
}

func (h *plexAPI) sections(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	var dirs []any
	locID := 0
	for _, s := range h.w.plex.sections {
		agent, scanner := sectionAgent(s.typ), s.scanner
		var locs []any
		for _, d := range s.dirs {
			locID++
			locs = append(locs, map[string]any{"id": locID, "path": h.w.plex.remote(d)})
		}
		res := "movie"
		if s.typ == LibraryShow {
			res = "show"
		}
		dirs = append(dirs, map[string]any{
			"allowSync": true, "art": "/:/resources/" + res + "-fanart.jpg",
			"composite": fmt.Sprintf("/library/sections/%s/composite/%d", s.key, s.scannedAt),
			"filters":   true, "refreshing": s.refreshing, "thumb": "/:/resources/" + res + ".png",
			"key": s.key, "type": s.typ, "title": s.title, "agent": agent, "scanner": scanner,
			"language": "en-US", "uuid": s.uuid, "updatedAt": s.scannedAt, "createdAt": s.createdAt,
			"scannedAt": s.scannedAt, "content": true, "directory": true, "contentChangedAt": s.contentChangedAt,
			"hidden": false, "Location": locs,
		})
	}
	mc := map[string]any{"size": len(dirs), "allowSync": false, "title1": "Plex Library"}
	if len(dirs) > 0 {
		mc["Directory"] = dirs
	}
	writeMediaContainer(w, mc)
}

// containerRange reads X-Plex-Container-Start/Size from headers or the query string.
func containerRange(r *http.Request) (start, size int, err error) {
	get := func(name string) string {
		if v := r.Header.Get(name); v != "" {
			return v
		}
		return r.URL.Query().Get(name)
	}
	size = -1
	if s := get("X-Plex-Container-Start"); s != "" {
		if start, err = strconv.Atoi(s); err != nil || start < 0 {
			return 0, 0, fmt.Errorf("invalid X-Plex-Container-Start %q", s)
		}
	}
	if s := get("X-Plex-Container-Size"); s != "" {
		if size, err = strconv.Atoi(s); err != nil || size < 0 {
			return 0, 0, fmt.Errorf("invalid X-Plex-Container-Size %q", s)
		}
	}
	return start, size, nil
}

func (h *plexAPI) sectionContainer(s *section) map[string]any {
	res := "movie"
	if s.typ == LibraryShow {
		res = "show"
	}
	secID, _ := strconv.Atoi(s.key)
	return map[string]any{
		"allowSync": true, "art": "/:/resources/" + res + "-fanart.jpg",
		"identifier": "com.plexapp.plugins.library", "librarySectionID": secID,
		"librarySectionTitle": s.title, "librarySectionUUID": s.uuid,
		"mediaTagPrefix": "/system/bundle/media/flags/", "mediaTagVersion": h.w.start.Unix(),
		"thumb": "/:/resources/" + res + ".png", "title1": s.title,
	}
}

// sortItems orders listings like Plex's default title sort (episodes by show/season/episode).
func sortItems(items []*item) {
	key := func(it *item) string {
		switch it.typ {
		case "episode":
			return fmt.Sprintf("%s\x00%05d\x00%05d", strings.ToLower(it.parent.parent.title), it.parent.index, it.index)
		case "season":
			return fmt.Sprintf("%s\x00%05d", strings.ToLower(it.parent.title), it.index)
		}
		t := strings.ToLower(it.title)
		for _, a := range []string{"the ", "a ", "an "} {
			if strings.HasPrefix(t, a) {
				t = t[len(a):]
				break
			}
		}
		return fmt.Sprintf("%s\x00%04d", t, it.year)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ki, kj := key(items[i]), key(items[j])
		if ki != kj {
			return ki < kj
		}
		return items[i].rk < items[j].rk
	})
}

func (h *plexAPI) sectionAll(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	start, size, err := containerRange(r)
	if err != nil {
		plexError(w, http.StatusBadRequest)
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	p := h.w.plex
	s := p.sectionOf(r.PathValue("id"))
	if s == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	want, title2, viewGroup := "", "", ""
	switch t := q.Get("type"); {
	case s.typ == LibraryMovie && (t == "" || t == "1"):
		want, title2, viewGroup = "movie", "All Movies", "movie"
	case s.typ == LibraryShow && (t == "" || t == "2"):
		want, title2, viewGroup = "show", "All Shows", "show"
	case s.typ == LibraryShow && t == "3":
		want, title2, viewGroup = "season", "All Seasons", "season"
	case s.typ == LibraryShow && t == "4":
		want, title2, viewGroup = "episode", "All Episodes", "episode"
	}
	var items []*item
	for _, it := range p.order {
		if it.sec == s && it.typ == want {
			if q.Get("duplicate") == "1" && len(it.media) < 2 {
				continue
			}
			items = append(items, it)
		}
	}
	sortItems(items)
	total := len(items)
	if p.pageLimit > 0 && (size < 0 || size > p.pageLimit) {
		size = p.pageLimit
	}
	lo := min(start, total)
	hi := total
	if size >= 0 && size < total-lo { // compare, never add: size may be as large as MaxInt
		hi = lo + size
	}
	page := items[lo:hi]
	opts := renderOpts{guids: q.Get("includeGuids") == "1"}
	mc := h.sectionContainer(s)
	mc["size"] = len(page)
	mc["totalSize"] = total
	mc["offset"] = start // echoes the requested start, even past the end
	mc["title2"] = title2
	mc["viewGroup"] = viewGroup
	if len(page) > 0 {
		md := make([]any, 0, len(page))
		for _, it := range page {
			md = append(md, h.itemJSON(it, opts))
		}
		mc["Metadata"] = md
	}
	w.Header().Set("X-Plex-Container-Start", strconv.Itoa(start))
	w.Header().Set("X-Plex-Container-Total-Size", strconv.Itoa(total))
	writeMediaContainer(w, mc)
}

func (h *plexAPI) metadata(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	var found []*item
	for _, id := range strings.Split(r.PathValue("ids"), ",") {
		if it := h.w.plex.items[strings.TrimSpace(id)]; it != nil {
			found = append(found, it)
		}
	}
	if len(found) == 0 {
		plexError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	opts := renderOpts{detail: true, guids: q.Get("includeGuids") == "1", checkFiles: q.Get("checkFiles") == "1"}
	mc := h.sectionContainer(found[0].sec)
	// The official detail examples serialise the container size as a string.
	mc["size"] = strconv.Itoa(len(found))
	md := make([]any, 0, len(found))
	for _, it := range found {
		md = append(md, h.itemJSON(it, opts))
	}
	mc["Metadata"] = md
	writeMediaContainer(w, mc)
}

func (h *plexAPI) listChildren(w http.ResponseWriter, r *http.Request, leaves bool) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	it := h.w.plex.items[r.PathValue("rk")]
	if it == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	var kids []*item
	switch {
	case leaves && it.typ == "show":
		for _, s := range it.children {
			kids = append(kids, s.children...)
		}
	case leaves && it.typ == "season", !leaves:
		kids = append(kids, it.children...)
	}
	sortItems(kids)
	mc := h.sectionContainer(it.sec)
	mc["size"] = len(kids)
	mc["key"] = it.rk
	mc["parentTitle"] = it.title
	if len(kids) > 0 {
		md := make([]any, 0, len(kids))
		for _, k := range kids {
			md = append(md, h.itemJSON(k, renderOpts{guids: r.URL.Query().Get("includeGuids") == "1"}))
		}
		mc["Metadata"] = md
	}
	writeMediaContainer(w, mc)
}

func (h *plexAPI) children(w http.ResponseWriter, r *http.Request)  { h.listChildren(w, r, false) }
func (h *plexAPI) allLeaves(w http.ResponseWriter, r *http.Request) { h.listChildren(w, r, true) }

func (h *plexAPI) sessions(w http.ResponseWriter, r *http.Request) {
	if !h.acceptJSON(w, r) {
		return
	}
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	p := h.w.plex
	var md []any
	for i, rk := range p.playing {
		it := p.items[rk]
		if it == nil || (it.typ != "movie" && it.typ != "episode") {
			continue
		}
		j := h.itemJSON(it, renderOpts{})
		for _, m := range it.media { // the session plays one (non-optimized) version
			if !m.v.Optimized {
				j["Media"] = []any{h.mediaJSON(m, renderOpts{})}
				break
			}
		}
		n := strconv.Itoa(i + 1)
		j["sessionKey"] = n
		j["viewOffset"] = 600000 * (i + 1)
		j["User"] = map[string]any{"id": "1", "title": p.owner, "thumb": "https://plex.tv/users/fake/avatar"}
		j["Player"] = map[string]any{
			"address": "192.168.1.50", "machineIdentifier": "fake-player-" + n, "platform": "Chrome",
			"product": "Plex Web", "state": "playing", "title": "Chrome", "local": true, "relayed": false, "secure": true,
		}
		j["Session"] = map[string]any{"id": "fake-session-" + n, "bandwidth": 20000, "location": "lan"}
		md = append(md, j)
	}
	mc := map[string]any{"size": len(md)}
	if len(md) > 0 {
		mc["Metadata"] = md
	}
	writeMediaContainer(w, mc)
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

func (h *plexAPI) deleteMedia(w http.ResponseWriter, r *http.Request) {
	ids, mid := r.PathValue("ids"), r.PathValue("mediaId")
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if r.URL.Query().Has("proxy") {
		h.w.violate(ServerPlex, r, RulePlexProxyParam, "the proxy parameter's scope is undocumented; omit it")
	}
	if !reRatingKey.MatchString(ids) {
		h.w.violate(ServerPlex, r, RulePlexIDList, fmt.Sprintf("rating key %q must match ^[0-9A-Za-z]+$", ids))
		plexError(w, http.StatusBadRequest)
		return
	}
	mediaID, err := strconv.ParseInt(mid, 10, 64)
	if err != nil || mediaID <= 0 {
		h.w.violate(ServerPlex, r, RulePlexMalformedDelete, fmt.Sprintf("media id %q must be a positive integer", mid))
		plexError(w, http.StatusBadRequest)
		return
	}
	p := h.w.plex
	it := p.items[ids]
	if it == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	idx := -1
	for i, m := range it.media {
		if m.id == mediaID {
			idx = i
		}
	}
	if idx < 0 {
		plexError(w, http.StatusNotFound)
		return
	}
	target := it.media[idx]
	rels := make([]string, 0, len(target.parts))
	for _, pt := range target.parts {
		rels = append(rels, pt.rel)
	}
	g := h.w.newRemovalGuard(rels...)
	h.w.recordClipDeletes(ServerPlex, r, rels)
	// The request itself is checked even when PMS refuses it below: asking to delete an optimized
	// version or a playing item is a Dupearr bug whatever the server settings.
	h.checkMediaDelete(r, it, target, g)
	if !p.allowDeletion {
		plexError(w, http.StatusBadRequest) // "Media item could not be deleted"
		return
	}
	if err := h.w.removeMediaFiles(target); err != nil {
		// PMS cannot delete a file (permissions); earlier parts of a stacked media may be gone.
		h.w.reportLosses(ServerPlex, r, g, nil, false)
		plexError(w, http.StatusBadRequest)
		return
	}
	it.media = append(it.media[:idx], it.media[idx+1:]...)
	it.updatedAt = h.w.now().Unix()
	if len(it.media) == 0 {
		p.removeItem(it) // UNVERIFIED in real PMS; the fake drops the empty item
	}
	// Safety invariants after the fact: a delete that destroyed files must leave the item (and
	// every other item that shared those files) with a playable version.
	if g.existed && !target.isOptimized() && !h.w.hasAvailableCopy(it) {
		h.w.violate(ServerPlex, r, RulePlexDeleteLastVersion, fmt.Sprintf("%s (rating key %s) has no available non-optimized version left", describeItem(it), it.rk))
	}
	h.w.reportLosses(ServerPlex, r, g, it, false)
	plexOK(w)
}

// checkMediaDelete records the violations a Plex media delete reveals before it runs: optimized
// versions are never touched, a version sharing its file with another version of the item must
// never be deleted (the one real file would go), playing items are deferred, and files of a
// full-disc backup are never deleted through Plex.
func (h *plexAPI) checkMediaDelete(r *http.Request, it *item, target *media, g *removalGuard) {
	if target.isOptimized() {
		h.w.violate(ServerPlex, r, RulePlexDeleteOptimized, fmt.Sprintf("media %d of %s is a Plex Optimized Version", target.id, describeItem(it)))
	}
	for _, m := range it.media {
		if m != target && m.hasFile(g.rels) {
			h.w.violate(ServerPlex, r, RulePlexDeleteSameFile, fmt.Sprintf("media %d of %s uses the same file as media %d", target.id, describeItem(it), m.id))
			break
		}
	}
	h.w.reportPlaying(ServerPlex, r, g)
	h.w.reportKeepTagged(ServerPlex, r, g)
	h.w.reportDiscRemoval(ServerPlex, r, g)
}

// removeMediaFiles deletes the files of a media from disk (already-missing files are fine).
func (w *world) removeMediaFiles(m *media) error {
	for _, pt := range m.parts {
		if err := removeIfExists(w.local(pt.rel)); err != nil {
			return err
		}
	}
	return nil
}

func (h *plexAPI) deleteItem(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexItemDelete, "whole-item delete removes every version; Dupearr deletes single media only")
	p := h.w.plex
	if !p.allowDeletion {
		plexError(w, http.StatusBadRequest)
		return
	}
	it := p.items[r.PathValue("ids")]
	if it == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	var all []*item
	var walk func(*item)
	walk = func(x *item) {
		all = append(all, x)
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(it)
	for _, x := range all {
		for _, m := range x.media {
			if err := h.w.removeMediaFiles(m); err != nil {
				plexError(w, http.StatusBadRequest)
				return
			}
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		p.removeItem(all[i])
	}
	plexOK(w)
}

// malformedDelete catches DELETEs under /library/metadata/ that match no documented shape (e.g. an
// empty media id: /library/metadata/123/media/). Which PMS handler such a URL reaches is
// UNVERIFIED, so Dupearr must never emit one (docs/research/plex-api.md §9.2).
func (h *plexAPI) malformedDelete(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexMalformedDelete, "DELETE path matches no documented endpoint (empty or extra segments)")
	plexError(w, http.StatusNotFound)
}

func (h *plexAPI) deleteElement(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if strings.EqualFold(r.PathValue("element"), "media") {
		// /library/metadata/{rk}/media = a version delete with the media id missing; PMS treats it
		// like /library/metadata/{rk}/media/ (UNVERIFIED handler), never a success.
		h.w.violate(ServerPlex, r, RulePlexMalformedDelete, "version delete without a media id")
		plexError(w, http.StatusNotFound)
		return
	}
	h.w.violate(ServerPlex, r, RulePlexElementDelete, "DELETE /library/metadata/{ids}/{element} is not used by Dupearr")
	plexOK(w)
}

func (h *plexAPI) mergeSplit(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexMergeSplit, "merge/split changes which versions belong to an item")
	plexOK(w)
}

func (h *plexAPI) bulkEdit(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexBulkEdit, "PUT /library/sections/{id}/all edits every matching item")
	plexOK(w)
}

func (h *plexAPI) deleteSection(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexSectionDelete, "deleting a library section")
	plexOK(w)
}

// checkTrash puts media whose files all vanished into the trash (Plex keeps them listed as
// unavailable), takes restored ones out, and — with autoEmptyTrash — removes trashed media. Media
// inside a section location that is missing or empty (an unmounted share) are left alone: PMS
// treats such a location as unavailable rather than emptied (docs/research/plex-api.md §10.3).
func (h *plexAPI) checkTrash(items []*item, scope string) (changed bool) {
	p := h.w.plex
	offline := map[string]bool{} // section location → unavailable (cached per call)
	locationOffline := func(it *item, m *media) bool {
		if it.sec == nil {
			return false
		}
		for _, pt := range m.parts {
			for _, d := range it.sec.dirs {
				if !under(pt.rel, d) {
					continue
				}
				off, ok := offline[d]
				if !ok {
					off = h.w.locationUnavailable(d)
					offline[d] = off
				}
				if off {
					return true
				}
			}
		}
		return false
	}
	for _, it := range items {
		var keep []*media
		for _, m := range it.media {
			inScope := scope == ""
			missing := 0
			for _, pt := range m.parts {
				if scope != "" && under(pt.rel, scope) {
					inScope = true
				}
				if !h.w.fileExists(pt.rel) {
					missing++
				}
			}
			if inScope && !locationOffline(it, m) {
				trashed := missing == len(m.parts)
				changed = changed || trashed != m.trashed
				m.trashed = trashed
			}
			if !(m.trashed && p.autoEmptyTrash) {
				keep = append(keep, m)
			}
		}
		if len(keep) != len(it.media) {
			it.media = keep
			it.updatedAt = h.w.now().Unix()
			changed = true
			if len(keep) == 0 {
				p.removeItem(it)
			}
		}
	}
	return changed
}

func (h *plexAPI) refreshItem(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	var found []*item
	for _, id := range strings.Split(r.PathValue("ids"), ",") {
		if it := h.w.plex.items[id]; it != nil {
			found = append(found, it)
		}
	}
	if len(found) == 0 {
		plexError(w, http.StatusNotFound)
		return
	}
	changed := h.checkTrash(found, "")
	// A refresh also picks up the item's files that are back on disk (a show/season refresh
	// covers its episodes, including ones that left the library with their last file).
	var leaves []*item
	for _, it := range h.w.plex.all {
		for _, f := range found {
			if isDescendant(it, f) {
				leaves = append(leaves, it)
				break
			}
		}
	}
	if h.w.redetect(leaves, "") {
		changed = true
	}
	if changed {
		// The library's content changed (UNVERIFIED for real Plex, research Q5: the fake assumes
		// an item refresh that changes media counts as a content change of its library).
		now := h.w.now().Unix()
		for _, f := range found {
			if f.sec != nil {
				f.sec.contentChangedAt = max(now, f.sec.contentChangedAt+1)
			}
		}
	}
	plexOK(w)
}

func (h *plexAPI) sectionRefresh(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	p := h.w.plex
	s := p.sectionOf(r.PathValue("id"))
	if s == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	if r.URL.Query().Has("force") {
		h.w.violate(ServerPlex, r, RulePlexRefreshForce, "force=1 re-downloads metadata for the whole section; scan with ?path= only")
	}
	scope := ""
	if dir := r.URL.Query().Get("path"); dir != "" {
		rel, ok := p.relOf(dir)
		if !ok {
			h.w.violate(ServerPlex, r, RulePlexScanOutsideSection, fmt.Sprintf("path %q is not a Plex path inside %s (missing path mapping?)", dir, p.mediaRoot))
			plexError(w, http.StatusBadRequest)
			return
		}
		inside := false
		for _, d := range s.dirs {
			inside = inside || under(rel, d)
		}
		if !inside {
			// PMS scans nothing (UNVERIFIED status); the violation surfaces the wrong section/path.
			h.w.violate(ServerPlex, r, RulePlexScanOutsideSection, fmt.Sprintf("path %q is outside the locations of section %s", dir, s.key))
		}
		scope = rel
	}
	var items []*item
	for _, it := range p.order {
		if it.sec == s && len(it.media) > 0 {
			items = append(items, it)
		}
	}
	changed := h.checkTrash(items, scope)
	// New files: declared versions whose files are back on disk (inside the scanned folder).
	var known []*item
	for _, it := range p.all {
		if it.sec == s {
			known = append(known, it)
		}
	}
	if h.w.redetect(known, scope) {
		changed = true
	}
	now := h.w.now().Unix()
	s.scannedAt = max(now, s.scannedAt+1) // every scan is visible, even within one second
	if changed {
		s.contentChangedAt = max(now, s.contentChangedAt+1)
	}
	plexOK(w)
}

func (h *plexAPI) cancelRefresh(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	if h.w.plex.sectionOf(r.PathValue("id")) == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	plexOK(w)
}

func (h *plexAPI) emptyTrash(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.violate(ServerPlex, r, RulePlexEmptyTrash, "Dupearr never empties the Plex trash (docs/DECISIONS.md D6)")
	p := h.w.plex
	s := p.sectionOf(r.PathValue("id"))
	if s == nil {
		plexError(w, http.StatusNotFound)
		return
	}
	for _, it := range append([]*item(nil), p.order...) {
		if it.sec != s || len(it.media) == 0 {
			continue
		}
		var keep []*media
		for _, m := range it.media {
			if !m.trashed {
				keep = append(keep, m)
			}
		}
		if len(keep) != len(it.media) {
			it.media = keep
			if len(keep) == 0 {
				p.removeItem(it)
			}
		}
	}
	plexOK(w)
}

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

func (h *plexAPI) photo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u := q.Get("url")
	if u == "" {
		plexError(w, http.StatusBadRequest)
		return
	}
	writePNG(w, u, atoiDefault(q.Get("width"), 240), atoiDefault(q.Get("height"), 360))
}

func (h *plexAPI) thumb(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	_, ok := h.w.plex.items[r.PathValue("rk")]
	h.w.mu.Unlock()
	if !ok {
		plexError(w, http.StatusNotFound)
		return
	}
	writePNG(w, r.URL.Path, 240, 360)
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// writePNG writes a small solid-colour poster placeholder (size clamped to 1..600).
func writePNG(w http.ResponseWriter, seed string, width, height int) {
	width, height = min(max(width, 1), 600), min(max(height, 1), 600)
	c := hashHex(6, seed)
	rgb, _ := strconv.ParseUint(c, 16, 32)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fill := color.RGBA{R: uint8(rgb >> 16), G: uint8(rgb >> 8), B: uint8(rgb), A: 255}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		plexError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

type renderOpts struct {
	detail     bool // detail endpoint: streams, grandparentGuid, string-typed ids
	guids      bool // includeGuids=1
	checkFiles bool // checkFiles=1: Part.exists / Part.accessible
}

func (h *plexAPI) itemJSON(it *item, o renderOpts) map[string]any {
	j := map[string]any{
		"ratingKey": it.rk,
		"key":       "/library/metadata/" + it.rk,
		"guid":      it.guid,
		"type":      it.typ,
		"title":     it.title,
		"summary":   "",
		"addedAt":   it.addedAt,
		"updatedAt": it.updatedAt,
		"thumb":     fmt.Sprintf("/library/metadata/%s/thumb/%d", it.rk, it.updatedAt),
	}
	if it.sec != nil && o.detail {
		secID, _ := strconv.Atoi(it.sec.key)
		j["librarySectionID"] = secID
		j["librarySectionTitle"] = it.sec.title
		j["librarySectionKey"] = "/library/sections/" + it.sec.key
	}
	if o.guids && len(it.guids) > 0 && it.typ != "season" {
		g := make([]any, 0, len(it.guids))
		for _, id := range it.guids {
			g = append(g, map[string]any{"id": id})
		}
		j["Guid"] = g
	}
	switch it.typ {
	case "movie":
		j["year"] = it.year
		j["titleSort"] = it.title
		j["art"] = fmt.Sprintf("/library/metadata/%s/art/%d", it.rk, it.updatedAt)
		j["originallyAvailableAt"] = fmt.Sprintf("%04d-06-01", max(it.year, 1900))
		if d := itemDuration(it); d > 0 {
			j["duration"] = d
		}
		if it.edition != "" {
			j["editionTitle"] = it.edition
		}
		j["Media"] = h.mediaList(it, o)
	case "episode":
		season := it.parent
		show := season.parent
		j["parentRatingKey"] = season.rk
		j["grandparentRatingKey"] = show.rk
		j["parentKey"] = "/library/metadata/" + season.rk
		j["grandparentKey"] = "/library/metadata/" + show.rk
		j["parentTitle"] = season.title
		j["grandparentTitle"] = show.title
		j["parentGuid"] = season.guid
		if o.detail {
			// Presence in listings is UNVERIFIED (docs/research/plex-api.md §7.4): only detail has it.
			j["grandparentGuid"] = show.guid
		}
		j["index"] = it.index
		j["parentIndex"] = season.index
		j["year"] = it.year
		j["parentThumb"] = fmt.Sprintf("/library/metadata/%s/thumb/%d", season.rk, season.updatedAt)
		j["grandparentThumb"] = fmt.Sprintf("/library/metadata/%s/thumb/%d", show.rk, show.updatedAt)
		j["grandparentArt"] = fmt.Sprintf("/library/metadata/%s/art/%d", show.rk, show.updatedAt)
		j["originallyAvailableAt"] = fmt.Sprintf("%04d-01-%02d", max(it.year, 1900), min(max(it.index, 1), 28))
		if d := itemDuration(it); d > 0 {
			j["duration"] = d
		}
		j["Media"] = h.mediaList(it, o)
	case "show":
		j["key"] = "/library/metadata/" + it.rk + "/children"
		j["year"] = it.year
		j["art"] = fmt.Sprintf("/library/metadata/%s/art/%d", it.rk, it.updatedAt)
		leaves := 0
		for _, s := range it.children {
			leaves += len(s.children)
		}
		j["childCount"] = len(it.children)
		j["leafCount"] = leaves
		j["viewedLeafCount"] = 0
		if o.detail && it.folder != "" {
			j["Location"] = []any{map[string]any{"path": h.w.plex.remote(it.folder)}}
		}
	case "season":
		show := it.parent
		j["key"] = "/library/metadata/" + it.rk + "/children"
		j["parentRatingKey"] = show.rk
		j["parentKey"] = "/library/metadata/" + show.rk
		j["parentTitle"] = show.title
		j["parentGuid"] = show.guid
		j["index"] = it.index
		j["leafCount"] = len(it.children)
		j["viewedLeafCount"] = 0
		j["parentThumb"] = fmt.Sprintf("/library/metadata/%s/thumb/%d", show.rk, show.updatedAt)
	}
	return j
}

// itemDuration is the longest analyzed version's duration (0 when nothing is analyzed).
func itemDuration(it *item) int64 {
	var d int64
	for _, m := range it.media {
		if !m.v.Unanalyzed {
			d = max(d, m.durationMs)
		}
	}
	return d
}

func (h *plexAPI) mediaList(it *item, o renderOpts) []any {
	out := make([]any, 0, len(it.media))
	for _, m := range it.media {
		out = append(out, h.mediaJSON(m, o))
	}
	return out
}

func containerOf(v *Version, rel string) string {
	if v.Container != "" {
		return v.Container
	}
	switch ext := strings.TrimPrefix(strings.ToLower(path.Ext(rel)), "."); ext {
	case "ts", "m2ts":
		return "mpegts"
	case "":
		return "mkv"
	default:
		return ext
	}
}

func plexVideoResolution(width, height int) string {
	switch {
	case width >= 3200:
		return "4k"
	case width >= 1700:
		return "1080"
	case width >= 1100:
		return "720"
	case height >= 560:
		return "576"
	case height >= 400:
		return "480"
	default:
		return "sd"
	}
}

func frameRateLabel(fps float64) string {
	switch {
	case fps <= 0:
		return ""
	case fps < 24.5:
		return "24p"
	case fps < 25.5:
		return "PAL"
	case fps < 29.99:
		return "NTSC"
	case fps < 30.5:
		return "30p"
	case fps < 50.5:
		return "50p"
	default:
		return "60p"
	}
}

func (h *plexAPI) mediaJSON(m *media, o renderOpts) map[string]any {
	v := &m.v
	container := containerOf(v, m.parts[0].rel)
	j := map[string]any{"id": m.id, "container": container}
	// An unanalyzed file has no stream information yet: PMS omits duration, bitrate, dimensions
	// and codecs (Dupearr decodes the absent width as 0 and flags the version "unanalyzed").
	if !v.Unanalyzed {
		vid := v.Video
		j["duration"] = m.durationMs
		j["bitrate"] = m.bitrateKbps()
		j["width"] = vid.Width
		j["height"] = vid.Height
		if vid.Height > 0 {
			j["aspectRatio"] = math.Round(float64(vid.Width)/float64(vid.Height)*100) / 100
		}
		j["videoCodec"] = vid.Codec
		if vid.Profile != "" {
			j["videoProfile"] = vid.Profile
		}
		j["videoResolution"] = plexVideoResolution(vid.Width, vid.Height)
		if fr := frameRateLabel(vid.FrameRate); fr != "" {
			j["videoFrameRate"] = fr
		}
		if a := primaryAudio(v); a != nil {
			j["audioCodec"] = a.Codec
			j["audioChannels"] = a.Channels
			if a.Profile != "" {
				j["audioProfile"] = a.Profile
			}
		}
		j["has64bitOffsets"] = false
		j["optimizedForStreaming"] = container == "mp4"
	}
	if v.Optimized {
		target := v.OptimizedTarget
		if target == "" {
			target = "Optimized for Mobile"
		}
		j["proxyType"] = 42
		j["target"] = target
		j["title"] = target
	}
	parts := make([]any, 0, len(m.parts))
	for _, pt := range m.parts {
		parts = append(parts, h.partJSON(m, pt, container, o))
	}
	j["Part"] = parts
	return j
}

func primaryAudio(v *Version) *Audio {
	for i := range v.Audio {
		if v.Audio[i].Default {
			return &v.Audio[i]
		}
	}
	if len(v.Audio) > 0 {
		return &v.Audio[0]
	}
	return nil
}

func (h *plexAPI) partJSON(m *media, pt *part, container string, o renderOpts) map[string]any {
	var id any = pt.id
	if o.detail {
		id = strconv.FormatInt(pt.id, 10) // string-typed ids as in the official detail examples
	}
	ext := strings.TrimPrefix(path.Ext(pt.rel), ".")
	if ext == "" {
		ext = container
	}
	j := map[string]any{
		"id":        id,
		"key":       fmt.Sprintf("/library/parts/%d/%d/file.%s", pt.id, m.addedAt, ext),
		"file":      h.w.plex.remote(pt.rel),
		"size":      pt.size,
		"container": container,
	}
	if !m.v.Unanalyzed {
		j["duration"] = pt.durationMs
		if m.v.Video.Profile != "" {
			j["videoProfile"] = m.v.Video.Profile
		}
	}
	if o.checkFiles {
		exists, accessible := h.w.fileState(pt.rel)
		j["exists"] = exists
		j["accessible"] = accessible
	}
	if o.detail && len(pt.streamIDs) > 0 {
		j["Stream"] = h.streamsJSON(m, pt)
	}
	return j
}

func (h *plexAPI) streamsJSON(m *media, pt *part) []any {
	v := &m.v
	ids := pt.streamIDs
	out := make([]any, 0, len(ids))
	out = append(out, videoStreamJSON(ids[0], v.Video, m.bitrateKbps()))
	for i, a := range v.Audio {
		out = append(out, audioStreamJSON(ids[1+i], i+1, a))
	}
	for i, s := range v.Subtitles {
		out = append(out, subtitleStreamJSON(ids[1+len(v.Audio)+i], 1+len(v.Audio)+i, s))
	}
	return out
}

func roundUp16(n int) int { return (n + 15) / 16 * 16 }

func videoStreamJSON(id int64, v Video, mediaBitrate int) map[string]any {
	bitrate := v.BitrateKbps
	if bitrate == 0 {
		bitrate = int(float64(mediaBitrate) * 0.85)
	}
	dt := videoDisplayTitle(v)
	j := map[string]any{
		"id": id, "streamType": 1, "default": true, "codec": v.Codec, "index": 0, "bitrate": bitrate,
		"width": v.Width, "height": v.Height, "codedWidth": roundUp16(v.Width), "codedHeight": roundUp16(v.Height),
		"frameRate": v.FrameRate, "scanType": "progressive", "chromaSubsampling": "4:2:0", "chromaLocation": "left",
		"refFrames": 1, "displayTitle": dt, "extendedDisplayTitle": dt,
	}
	if v.BitDepth > 0 {
		j["bitDepth"] = v.BitDepth
	}
	if v.Profile != "" {
		j["profile"] = v.Profile
	}
	switch v.Codec {
	case "hevc":
		j["level"] = 150
		if v.Width < 3200 {
			j["level"] = 120
		}
	case "h264":
		j["level"] = 41
	}
	if v.ColorPrimaries != "" {
		j["colorPrimaries"] = v.ColorPrimaries
		j["colorRange"] = "tv"
	}
	if v.ColorSpace != "" {
		j["colorSpace"] = v.ColorSpace
	}
	if v.ColorTrc != "" {
		j["colorTrc"] = v.ColorTrc
	}
	if v.DOVIProfile > 0 {
		j["DOVIPresent"] = true
		j["DOVIProfile"] = v.DOVIProfile
		j["DOVILevel"] = v.DOVILevel
		j["DOVIVersion"] = "1.0"
		j["DOVIBLPresent"] = true
		j["DOVIELPresent"] = v.DOVIProfile == 7
		j["DOVIRPUPresent"] = true
		j["DOVIBLCompatID"] = v.DOVIBLCompatID
	}
	return j
}

func resolutionLabel(width, height int) string {
	switch {
	case width >= 3200:
		return "4K"
	case width >= 1700:
		return "1080p"
	case width >= 1100:
		return "720p"
	case height >= 560:
		return "576p"
	case height > 0:
		return "480p"
	default:
		return "SD"
	}
}

func videoCodecLabel(codec, profile string) string {
	switch codec {
	case "hevc":
		if strings.EqualFold(profile, "main 10") {
			return "HEVC Main 10"
		}
		return "HEVC"
	case "h264":
		return "H.264"
	case "av1":
		return "AV1"
	case "vc1":
		return "VC-1"
	case "mpeg2video":
		return "MPEG-2"
	case "mpeg4":
		return "MPEG-4"
	}
	return strings.ToUpper(codec)
}

// videoDisplayTitle composes Plex-style titles such as "4K DoVi/HDR10 (HEVC Main 10)".
func videoDisplayTitle(v Video) string {
	hdr := ""
	hdr10 := "HDR10"
	if v.HDR10Plus {
		hdr10 = "HDR10+"
	}
	switch {
	case v.DOVIProfile > 0 && v.ColorTrc == "arib-std-b67":
		hdr = "DoVi/HLG"
	case v.DOVIProfile > 0 && (v.DOVIBLCompatID == 1 || v.DOVIBLCompatID == 6 || v.ColorTrc == "smpte2084"):
		hdr = "DoVi/" + hdr10
	case v.DOVIProfile > 0:
		hdr = "DoVi"
	case v.ColorTrc == "smpte2084":
		hdr = hdr10
	case v.ColorTrc == "arib-std-b67":
		hdr = "HLG"
	}
	label := resolutionLabel(v.Width, v.Height)
	if hdr != "" {
		label += " " + hdr
	}
	return fmt.Sprintf("%s (%s)", label, videoCodecLabel(v.Codec, v.Profile))
}

func channelLayout(ch int) (layout, label string) {
	switch ch {
	case 8:
		return "7.1(side)", "7.1"
	case 7:
		return "6.1", "6.1"
	case 6:
		return "5.1(side)", "5.1"
	case 2:
		return "stereo", "Stereo"
	case 1:
		return "mono", "Mono"
	}
	return strconv.Itoa(ch) + " channels", strconv.Itoa(ch) + "ch"
}

func audioCodecLabel(a Audio, extended bool) string {
	var s string
	switch a.Codec {
	case "truehd":
		s = "TRUEHD"
		if extended {
			s = "TrueHD"
		}
	case "dca", "dts":
		s = "DTS"
		if p := strings.ToLower(a.Profile); strings.Contains(p, "ma") {
			s = "DTS-HD MA"
		} else if strings.Contains(p, "hra") {
			s = "DTS-HD HRA"
		} else if strings.Contains(p, "x") {
			s = "DTS:X"
		}
	default:
		s = strings.ToUpper(a.Codec)
	}
	if extended && a.Atmos {
		s += " Atmos"
	}
	return s
}

func audioStreamJSON(id int64, index int, a Audio) map[string]any {
	lang := lookupLanguage(a.LanguageCode)
	layout, label := channelLayout(a.Channels)
	j := map[string]any{
		"id": id, "streamType": 2, "codec": a.Codec, "index": index, "channels": a.Channels,
		"language": lang.Name, "languageCode": strings.ToLower(a.LanguageCode),
		"audioChannelLayout": layout, "samplingRate": 48000,
		"displayTitle":         fmt.Sprintf("%s (%s %s)", lang.Name, audioCodecLabel(a, false), label),
		"extendedDisplayTitle": fmt.Sprintf("%s (%s %s)", lang.Name, audioCodecLabel(a, true), label),
	}
	if lang.Tag != "" {
		j["languageTag"] = lang.Tag
	}
	if a.Default {
		j["default"] = true
		j["selected"] = true
	}
	if a.BitrateKbps > 0 {
		j["bitrate"] = a.BitrateKbps
	}
	if a.Profile != "" {
		j["profile"] = a.Profile
	}
	if a.Title != "" {
		j["title"] = a.Title
	}
	switch a.Codec {
	case "truehd", "dca", "flac", "pcm":
		j["bitDepth"] = 24
	}
	return j
}

func subtitleStreamJSON(id int64, index int, s Subtitle) map[string]any {
	lang := lookupLanguage(s.LanguageCode)
	codec := s.Codec
	if codec == "" {
		codec = "srt"
	}
	dt := fmt.Sprintf("%s (%s)", lang.Name, strings.ToUpper(codec))
	if s.Forced {
		dt = fmt.Sprintf("%s Forced (%s)", lang.Name, strings.ToUpper(codec))
	}
	j := map[string]any{
		"id": id, "streamType": 3, "codec": codec, "language": lang.Name,
		"languageCode": strings.ToLower(s.LanguageCode), "displayTitle": dt, "extendedDisplayTitle": dt,
	}
	if lang.Tag != "" {
		j["languageTag"] = lang.Tag
	}
	if s.Forced {
		j["forced"] = true
	}
	if s.External {
		j["key"] = fmt.Sprintf("/library/streams/%d", id)
		j["format"] = codec
	} else {
		j["index"] = index
	}
	return j
}
