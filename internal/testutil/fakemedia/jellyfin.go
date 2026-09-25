package fakemedia

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The fake Jellyfin 12.1 HTTP API (docs/research/jellyfin-emby.md §3, Appendix A). It serves the
// read requests Dupearr's client may send and the change notification, with Jellyfin's semantics
// (jellyfinstate.go). The destructive endpoints are implemented FAITHFULLY — a movie's delete
// removes its whole folder when it is not in a mixed folder, prefix sidecars otherwise, only part 1
// of a stack; a bulk delete stops half-way — so a regression that calls one destroys files of the
// test tree, and each records a Violation. Routes match case-insensitively like ASP.NET.

// Jellyfin violation rules.
const (
	// RuleJellyfinItemDelete: DELETE /Items/{id} (removes a movie's whole folder or prefix sidecars).
	RuleJellyfinItemDelete = "jellyfin_item_delete"
	// RuleJellyfinBulkDelete: DELETE /Items?ids= (deletes some ids, then fails).
	RuleJellyfinBulkDelete = "jellyfin_bulk_delete"
	// RuleJellyfinMergeVersions: POST /Videos/MergeVersions.
	RuleJellyfinMergeVersions = "jellyfin_merge_versions"
	// RuleJellyfinUnlinkVersions: DELETE /Videos/{id}/AlternateSources.
	RuleJellyfinUnlinkVersions = "jellyfin_unlink_versions"
	// RuleJellyfinTokenInURL: the credential was sent in the URL (ApiKey=/api_key=).
	RuleJellyfinTokenInURL = "jellyfin_token_in_url"
	// RuleJellyfinLegacyAuth: a legacy credential (X-Emby-Token, X-MediaBrowser-Token,
	// X-Emby-Authorization, the "Emby" scheme, api_key) — off by default in Jellyfin (401).
	RuleJellyfinLegacyAuth = "jellyfin_legacy_auth"
	// RuleJellyfinUnexpectedRequest: any request outside Dupearr's allowlist (research §5.3.3).
	RuleJellyfinUnexpectedRequest = "jellyfin_unexpected_request"
)

// jfSidecarExt are the extensions Jellyfin deletes next to a file in a mixed folder when their
// name starts with the file's name (BaseItem.cs:57-76).
var jfSidecarExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".tbn": true, ".gif": true, ".svg": true,
	".nfo": true, ".xml": true, ".srt": true, ".vtt": true, ".sub": true, ".sup": true, ".idx": true,
	".txt": true, ".edl": true, ".bif": true, ".smi": true, ".ttml": true, ".lrc": true, ".elrc": true,
}

type jfAPI struct {
	e *Env
	w *world
}

// jellyfinHandler serves the fake Jellyfin.
func (e *Env) jellyfinHandler() http.Handler {
	h := &jfAPI{e: e, w: e.w}
	return http.HandlerFunc(h.serve)
}

// jfCaller is who a request authenticated as.
type jfCaller int

const (
	jfNobody jfCaller = iota
	jfAdmin           // the API key
	jfUser            // the non-administrator user
)

func jfJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jfError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

// queryGet reads a query parameter case-insensitively (ASP.NET binds them so).
func queryGet(q url.Values, key string) (string, bool) {
	for k, vs := range q {
		if strings.EqualFold(k, key) && len(vs) > 0 {
			return vs[0], true
		}
	}
	return "", false
}

// authenticate reads the caller's credential. Legacy forms are refused (off by default) and
// recorded; the ApiKey query parameter is accepted like Jellyfin does, and recorded.
func (h *jfAPI) authenticate(r *http.Request) (jfCaller, bool) {
	s := h.w.jellyfin
	legacy := r.Header.Get("X-Emby-Token") != "" || r.Header.Get("X-MediaBrowser-Token") != "" ||
		r.Header.Get("X-Emby-Authorization") != "" ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "emby ")
	if _, ok := queryGet(r.URL.Query(), "api_key"); ok {
		legacy = true
	}
	if legacy {
		h.w.violate(ServerJellyfin, r, RuleJellyfinLegacyAuth, "a legacy credential form (disabled in Jellyfin by default)")
		return jfNobody, false
	}
	token := ""
	if v, ok := queryGet(r.URL.Query(), "ApiKey"); ok {
		h.w.violate(ServerJellyfin, r, RuleJellyfinTokenInURL, "the credential was sent in the URL")
		token = v
	}
	if a := strings.TrimSpace(r.Header.Get("Authorization")); token == "" && a != "" {
		if !strings.HasPrefix(strings.ToLower(a), "mediabrowser ") {
			return jfNobody, false
		}
		for _, p := range strings.Split(a[len("mediabrowser "):], ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "Token") {
				token = strings.Trim(strings.TrimSpace(v), `"`)
			}
		}
	}
	switch {
	case token == "":
		return jfNobody, false
	case token == s.apiKey:
		return jfAdmin, true
	case token == s.userToken:
		return jfUser, true
	}
	return jfNobody, false
}

// allowlisted reports a request Dupearr's client may send (research §5.3.3).
func allowlisted(method, p string) bool {
	switch method + " " + p {
	case "GET /System/Info/Public", "GET /System/Info", "GET /System/Configuration", "GET /Library/VirtualFolders",
		"GET /Items", "GET /Sessions", "GET /ScheduledTasks", "POST /Library/Media/Updated":
		return true
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return method == http.MethodGet && len(segs) == 3 && segs[0] == "Videos" && segs[2] == "AdditionalParts"
}

func (h *jfAPI) serve(w http.ResponseWriter, r *http.Request) {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.jfApplyDue(false)
	p := r.URL.Path
	lp := strings.ToLower(strings.TrimRight(p, "/"))
	if !allowlisted(r.Method, p) {
		h.w.violate(ServerJellyfin, r, RuleJellyfinUnexpectedRequest, "outside Dupearr's allowlist")
	}
	if r.Method == http.MethodGet && lp == "/system/info/public" {
		jfJSON(w, http.StatusOK, h.systemInfo(true))
		return
	}
	caller, ok := h.authenticate(r)
	if !ok {
		jfError(w, http.StatusUnauthorized, "")
		return
	}
	segs := strings.Split(strings.TrimPrefix(lp, "/"), "/")
	switch {
	case r.Method == http.MethodGet && lp == "/system/info":
		jfJSON(w, http.StatusOK, h.systemInfo(false))
	case r.Method == http.MethodGet && lp == "/system/configuration":
		jfJSON(w, http.StatusOK, h.configuration())
	case r.Method == http.MethodGet && lp == "/library/virtualfolders":
		if caller != jfAdmin {
			jfError(w, http.StatusForbidden, "")
			return
		}
		jfJSON(w, http.StatusOK, h.virtualFolders())
	case r.Method == http.MethodGet && lp == "/items":
		h.items(w, r)
	case r.Method == http.MethodGet && len(segs) == 2 && segs[0] == "items":
		jfError(w, http.StatusBadRequest, "Error processing request.") // needs a user (research §3.2)
	case r.Method == http.MethodGet && len(segs) == 3 && segs[0] == "videos" && segs[2] == "additionalparts":
		h.additionalParts(w, segs[1])
	case r.Method == http.MethodGet && lp == "/sessions":
		h.sessions(w, caller)
	case r.Method == http.MethodGet && lp == "/scheduledtasks":
		h.scheduledTasks(w)
	case r.Method == http.MethodPost && lp == "/library/media/updated":
		h.mediaUpdated(w, r)
	case r.Method == http.MethodDelete && len(segs) == 2 && segs[0] == "items":
		if !h.deleteItem(r, segs[1]) {
			h.w.violate(ServerJellyfin, r, RuleJellyfinItemDelete, "DELETE /Items/{id} of an unknown id")
			jfError(w, http.StatusNotFound, "")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && lp == "/items":
		h.w.violate(ServerJellyfin, r, RuleJellyfinBulkDelete, "DELETE /Items?ids= deletes some ids, then fails")
		ids, _ := queryGet(r.URL.Query(), "ids")
		for _, id := range strings.Split(ids, ",") {
			if !h.deleteItem(r, strings.TrimSpace(id)) {
				jfError(w, http.StatusNotFound, "")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && lp == "/videos/mergeversions":
		h.w.violate(ServerJellyfin, r, RuleJellyfinMergeVersions, "merging versions changes the library")
		ids, _ := queryGet(r.URL.Query(), "ids")
		h.merge(strings.Split(ids, ","))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && len(segs) == 3 && segs[0] == "videos" && segs[2] == "alternatesources":
		h.w.violate(ServerJellyfin, r, RuleJellyfinUnlinkVersions, "unlinking versions changes the library")
		h.unlink(segs[1])
		w.WriteHeader(http.StatusNoContent)
	default:
		jfError(w, http.StatusNotFound, "")
	}
}

func (h *jfAPI) systemInfo(public bool) map[string]any {
	s := h.w.jellyfin
	out := map[string]any{
		"LocalAddress": "http://127.0.0.1:8096", "ServerName": s.name, "Version": s.version,
		"ProductName": "Jellyfin Server", "OperatingSystem": "", "Id": s.serverID, "StartupWizardCompleted": true,
	}
	if !public {
		out["OperatingSystemDisplayName"] = "Linux"
		out["HasPendingRestart"] = false
		out["IsShuttingDown"] = false
	}
	return out
}

func (h *jfAPI) configuration() map[string]any {
	s := h.w.jellyfin
	subst := make([]map[string]string, 0, len(s.subst))
	for _, p := range s.subst {
		subst = append(subst, map[string]string{"From": p.From, "To": p.To})
	}
	return map[string]any{
		"LogFileRetentionDays": 3, "EnableMetrics": false, "ServerName": s.name,
		"PathSubstitutions": subst, "LibraryMonitorDelay": int(s.monitorDelay / time.Second),
		"EnableCaseSensitiveItemIds": true, "CachePath": "/cache", "MetadataPath": "/config/metadata",
	}
}

func (h *jfAPI) virtualFolders() []map[string]any {
	s := h.w.jellyfin
	out := make([]map[string]any, 0, len(s.libs))
	for _, l := range s.libs {
		locs := make([]string, 0, len(l.dirs))
		for _, d := range l.dirs {
			locs = append(locs, s.remote(d))
		}
		f := map[string]any{"Name": l.name, "Locations": locs, "ItemId": l.id, "RefreshStatus": "Idle"}
		if l.collection != "" {
			f["CollectionType"] = l.collection
		}
		out = append(out, f)
	}
	return out
}

// ---------------------------------------------------------------------------
// Items
// ---------------------------------------------------------------------------

func (h *jfAPI) items(w http.ResponseWriter, r *http.Request) {
	s := h.w.jellyfin
	q := r.URL.Query()
	var rows []map[string]any
	if ids, ok := queryGet(q, "Ids"); ok {
		for _, id := range strings.Split(ids, ",") {
			id = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
			for _, it := range s.items {
				if it.id() == id && !it.hidden {
					rows = append(rows, h.row(it))
				}
			}
			for _, sr := range s.series {
				if sr.id == id {
					rows = append(rows, map[string]any{"Id": sr.id, "Name": sr.name, "Type": "Series",
						"Path": s.shown(sr.folder), "ProviderIds": sr.ids, "ProductionYear": sr.year, "LocationType": "FileSystem"})
				}
			}
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		jfJSON(w, http.StatusOK, map[string]any{"Items": rows, "TotalRecordCount": len(rows), "StartIndex": 0})
		return
	}
	var lib *jfLibrary
	if pid, ok := queryGet(q, "ParentId"); ok {
		for _, l := range s.libs {
			if l.id == strings.ToLower(pid) {
				lib = l
			}
		}
		if lib == nil {
			jfError(w, http.StatusNotFound, "")
			return
		}
	}
	types, _ := queryGet(q, "IncludeItemTypes")
	var all []*jfItem
	for _, t := range strings.Split(types, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		for _, typ := range []string{"Movie", "Episode"} {
			if strings.EqualFold(t, typ) {
				all = append(all, s.libraryItems(lib, typ)...)
			}
		}
	}
	if types == "" {
		all = s.libraryItems(lib, "")
	}
	start, _ := strconv.Atoi(firstOr(q, "StartIndex", "0"))
	limit, err := strconv.Atoi(firstOr(q, "Limit", "0"))
	if err != nil || limit <= 0 {
		limit = len(all)
	}
	if s.pageLimit > 0 && limit > s.pageLimit {
		limit = s.pageLimit
	}
	total := len(all)
	if start > 0 {
		total += s.totalDrift
	}
	start = min(max(start, 0), len(all))
	end := min(start+limit, len(all))
	rows = make([]map[string]any, 0, end-start)
	for _, it := range all[start:end] {
		rows = append(rows, h.row(it))
	}
	jfJSON(w, http.StatusOK, map[string]any{"Items": rows, "TotalRecordCount": total, "StartIndex": start})
}

func firstOr(q url.Values, key, def string) string {
	if v, ok := queryGet(q, key); ok {
		return v
	}
	return def
}

// row renders one item (a BaseItemDto with MediaSources).
func (h *jfAPI) row(it *jfItem) map[string]any {
	s := h.w.jellyfin
	primary := it.versions[0]
	row := map[string]any{
		"Name": it.name, "ServerId": s.serverID, "Id": it.id(), "Type": it.typ,
		"Path": s.shown(primary.parts[0].rel), "ProviderIds": it.ids, "LocationType": "FileSystem",
		"VideoType": "VideoFile", "DateCreated": it.added.UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"ParentId": it.lib.id, "IsFolder": false, "MediaType": "Video",
	}
	if it.year > 0 {
		row["ProductionYear"] = it.year
	}
	if len(primary.parts) > 1 {
		row["PartCount"] = len(primary.parts)
	}
	if it.typ == "Episode" {
		row["IndexNumber"] = it.episode
		if it.season >= 0 {
			row["ParentIndexNumber"] = it.season
		}
		if primary.epEnd > it.episode {
			row["IndexNumberEnd"] = primary.epEnd
		}
		if it.series != nil {
			row["SeriesId"], row["SeriesName"] = it.series.id, it.series.name
		}
	}
	var sources []map[string]any
	for _, v := range it.versions {
		sources = append(sources, h.source(v, "Default"))
	}
	for _, o := range it.linked {
		sources = append(sources, h.source(o.versions[0], "Grouping"))
	}
	if len(sources) != 1 {
		row["MediaSourceCount"] = len(sources)
	}
	row["MediaSources"] = sources
	if d := h.durationMs(primary.parts[0].rel); d > 0 && !primary.strm {
		row["RunTimeTicks"] = d * 10_000
	}
	return row
}

// durationMs returns the declared duration of the version whose first file is rel (a stack's
// whole running time; Mins(100) for undeclared files).
func (h *jfAPI) durationMs(rel string) int64 {
	if v := h.w.fileSpecs[rel]; v != nil {
		if v.DurationMs > 0 {
			return v.DurationMs
		}
		return Mins(120)
	}
	return Mins(100)
}

// source renders one media source.
func (h *jfAPI) source(v *jfVersion, typ string) map[string]any {
	s := h.w.jellyfin
	p := v.parts[0]
	src := map[string]any{
		"Protocol": "File", "Id": v.id, "Path": s.shown(p.rel), "Type": typ, "Size": p.size, "Name": v.name,
		"IsRemote": false, "VideoType": "VideoFile", "SupportsDirectPlay": true,
	}
	if v.strm {
		src["Container"] = "strm"
		src["MediaStreams"] = []any{}
		return src
	}
	src["Container"] = strings.TrimPrefix(strings.ToLower(path.Ext(p.rel)), ".")
	d := h.durationMs(p.rel)
	src["RunTimeTicks"] = d * 10_000
	if d > 0 {
		src["Bitrate"] = p.size * 8 * 1000 / d
	}
	src["MediaStreams"] = h.streams(p.rel)
	return src
}

// streams renders the MediaStreams of a file from the scenario's declaration (a plain 1080p H.264
// stream for undeclared files).
func (h *jfAPI) streams(rel string) []map[string]any {
	spec := h.w.fileSpecs[rel]
	if spec == nil {
		return []map[string]any{
			{"Type": "Video", "Codec": "h264", "Profile": "High", "Width": 1920, "Height": 1080, "BitDepth": 8,
				"IsDefault": true, "VideoRangeType": "SDR", "DisplayTitle": "1080p H264 SDR", "Index": 0},
			{"Type": "Audio", "Codec": "aac", "Channels": 2, "Language": "eng", "IsDefault": true, "Index": 1, "DisplayTitle": "English - AAC - Stereo"},
		}
	}
	if spec.Unanalyzed {
		return []map[string]any{}
	}
	vd := spec.Video
	rangeType := "SDR"
	switch {
	case vd.DOVIProfile > 0 && (vd.DOVIBLCompatID == 1 || vd.DOVIBLCompatID == 6):
		rangeType = "DOVIWithHDR10"
	case vd.DOVIProfile > 0:
		rangeType = "DOVI"
	case vd.HDR10Plus:
		rangeType = "HDR10Plus"
	case vd.ColorTrc == "smpte2084":
		rangeType = "HDR10"
	case vd.ColorTrc == "arib-std-b67":
		rangeType = "HLG"
	}
	video := map[string]any{
		"Type": "Video", "Codec": vd.Codec, "Profile": vd.Profile, "Width": vd.Width, "Height": vd.Height,
		"BitDepth": vd.BitDepth, "IsDefault": true, "Index": 0, "ColorTransfer": vd.ColorTrc,
		"ColorPrimaries": vd.ColorPrimaries, "VideoRangeType": rangeType,
		"DisplayTitle": fmt.Sprintf("%dp %s %s", vd.Height, strings.ToUpper(vd.Codec), rangeType),
	}
	if vd.FrameRate > 0 {
		video["RealFrameRate"] = vd.FrameRate
	}
	if vd.BitrateKbps > 0 {
		video["BitRate"] = vd.BitrateKbps * 1000
	}
	if vd.DOVIProfile > 0 {
		video["DvProfile"], video["DvBlSignalCompatibilityId"] = vd.DOVIProfile, vd.DOVIBLCompatID
	}
	if vd.HDR10Plus {
		video["Hdr10PlusPresentFlag"] = true
	}
	out := []map[string]any{video}
	for i, a := range spec.Audio {
		codec, profile := a.Codec, a.Profile
		if codec == "dca" {
			codec = "dts"
			if profile == "ma" {
				profile = "DTS-HD MA"
			}
		}
		out = append(out, map[string]any{"Type": "Audio", "Codec": codec, "Profile": profile, "Channels": a.Channels,
			"Language": a.LanguageCode, "Title": a.Title, "IsDefault": a.Default || i == 0, "Index": i + 1,
			"DisplayTitle": strings.TrimSpace(a.Title)})
	}
	for i, sub := range spec.Subtitles {
		out = append(out, map[string]any{"Type": "Subtitle", "Codec": sub.Codec, "Language": sub.LanguageCode,
			"IsForced": sub.Forced, "IsExternal": sub.External, "Index": len(spec.Audio) + 1 + i})
	}
	return out
}

func (h *jfAPI) additionalParts(w http.ResponseWriter, id string) {
	s := h.w.jellyfin
	it, v := s.findVersion(strings.ToLower(id))
	if it == nil {
		if pit, _, _ := s.findPart(strings.ToLower(id)); pit == nil {
			jfError(w, http.StatusNotFound, "")
			return
		}
		jfJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	items := make([]map[string]any, 0, len(v.parts))
	for _, p := range v.parts[1:] {
		src := map[string]any{"Protocol": "File", "Id": p.id, "Path": s.shown(p.rel), "Type": "Default",
			"Container": strings.TrimPrefix(strings.ToLower(path.Ext(p.rel)), "."), "IsRemote": false}
		if !s.omitSize {
			src["Size"] = p.size
		}
		items = append(items, map[string]any{"Id": p.id, "Name": strings.TrimSuffix(path.Base(p.rel), path.Ext(p.rel)),
			"Type": it.typ, "Path": s.shown(p.rel), "LocationType": "FileSystem", "MediaSources": []any{src}})
	}
	jfJSON(w, http.StatusOK, map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
}

func (h *jfAPI) sessions(w http.ResponseWriter, caller jfCaller) {
	s := h.w.jellyfin
	out := []map[string]any{}
	for i, se := range s.sessions {
		if caller == jfUser && !se.User {
			continue // a user sees only its own sessions (research S14)
		}
		row := map[string]any{"Id": fmt.Sprintf("session%02d", i), "Client": "Jellyfin Web", "DeviceName": "Browser",
			"PlayState": map[string]any{"IsPaused": se.Paused, "MediaSourceId": se.MediaSourceID, "CanSeek": true}}
		if se.ItemID != "" {
			npi := map[string]any{"Id": se.ItemID, "Type": "Movie"}
			if se.PrimaryVersionID != "" {
				npi["PrimaryVersionId"] = se.PrimaryVersionID
			}
			row["NowPlayingItem"] = npi
		}
		out = append(out, row)
	}
	jfJSON(w, http.StatusOK, out)
}

func (h *jfAPI) scheduledTasks(w http.ResponseWriter) {
	s := h.w.jellyfin
	task := map[string]any{"Name": "Scan Media Library", "Key": "RefreshLibrary", "State": "Idle", "Category": "Library", "Id": "6330ee8fb4a957f33981f89aa78b030f"}
	if !s.lastScan.IsZero() {
		task["LastExecutionResult"] = map[string]any{
			"StartTimeUtc": s.lastScan.Add(-time.Minute).UTC().Format(time.RFC3339Nano),
			"EndTimeUtc":   s.lastScan.UTC().Format(time.RFC3339Nano), "Status": "Completed", "Key": "RefreshLibrary",
		}
	}
	jfJSON(w, http.StatusOK, []any{task, map[string]any{"Name": "Clean Cache Directory", "Key": "DeleteCacheFiles", "State": "Idle"}})
}

// mediaUpdated records a notification and schedules the library monitor: the folders of the named
// paths are read again after LibraryMonitorDelay (on the Env clock).
func (h *jfAPI) mediaUpdated(w http.ResponseWriter, r *http.Request) {
	s := h.w.jellyfin
	var body struct {
		Updates []struct{ Path, UpdateType string }
	}
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &body); err != nil || len(body.Updates) == 0 {
		jfError(w, http.StatusBadRequest, "invalid body")
		return
	}
	n := JellyfinNotification{Time: h.w.now()}
	var folders []string
	for _, u := range body.Updates {
		n.Paths = append(n.Paths, u.Path)
		n.UpdateType = append(n.UpdateType, u.UpdateType)
		if rel, ok := s.relOf(u.Path); ok {
			folders = append(folders, path.Dir(rel))
		}
	}
	s.notified = append(s.notified, n)
	if len(folders) > 0 {
		s.pending = append(s.pending, jfPending{folders: folders, due: h.w.now().Add(s.monitorDelay)})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Destructive endpoints (faithful; each also records a violation)
// ---------------------------------------------------------------------------

// deletePaths returns what Jellyfin deletes for a version (GetDeletePaths, research §3.3): the
// containing folder (recursively) for a movie that is not in a mixed folder; otherwise the file
// plus the sidecars of its folder whose name starts with the file's name. Other stack parts are
// separate items and are not deleted.
func (h *jfAPI) deletePaths(it *jfItem, rel string) (dirs, files []string) {
	if it.typ == "Movie" && !it.mixed {
		return []string{it.folder}, nil
	}
	files = []string{rel}
	dir := path.Dir(rel)
	stem := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	entries, _ := os.ReadDir(h.w.local(dir))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == path.Base(rel) {
			continue
		}
		if jfSidecarExt[strings.ToLower(path.Ext(name))] && strings.HasPrefix(strings.TrimSuffix(name, path.Ext(name)), stem) {
			files = append(files, path.Join(dir, name))
		}
	}
	return nil, files
}

// deleteItem deletes an item (a row, an alternate or a stack part) like Jellyfin: from disk, and
// from the listing (an alternate leaves its item; a primary is replaced by its first alternate).
func (h *jfAPI) deleteItem(r *http.Request, id string) bool {
	s := h.w.jellyfin
	id = strings.ToLower(id)
	it, v := s.findVersion(id)
	rel := ""
	if it != nil {
		rel = v.parts[0].rel
	} else if pit, pv, pi := s.findPart(id); pit != nil {
		it, rel = pit, pv.parts[pi].rel
	} else {
		return false
	}
	dirs, files := h.deletePaths(it, rel)
	for _, d := range dirs {
		_ = os.RemoveAll(h.w.local(d))
	}
	for _, f := range files {
		_ = os.Remove(h.w.local(f))
	}
	h.w.violate(ServerJellyfin, r, RuleJellyfinItemDelete, fmt.Sprintf("deleted %s: folders %v, files %v", it.describe(), dirs, files))
	if v != nil {
		kept := it.versions[:0:0]
		for _, o := range it.versions {
			if o != v {
				kept = append(kept, o)
			}
		}
		it.versions = kept
		if len(kept) == 0 {
			out := s.items[:0:0]
			for _, o := range s.items {
				if o != it {
					out = append(out, o)
				}
			}
			s.items = out
		}
	}
	return true
}

// merge links items like POST /Videos/MergeVersions (the first id is the primary); items already
// merged join their existing title.
func (h *jfAPI) merge(ids []string) {
	s := h.w.jellyfin
	var rels []string
	for _, id := range ids {
		if it, _ := s.findVersion(strings.ToLower(strings.TrimSpace(id))); it != nil && !slices.Contains(rels, it.versions[0].parts[0].rel) {
			rels = append(rels, it.versions[0].parts[0].rel)
		}
	}
	if len(rels) < 2 {
		return
	}
	kept := s.merges[:0:0]
	for _, m := range s.merges {
		if slices.ContainsFunc(m, func(r string) bool { return slices.Contains(rels, r) }) {
			for _, r := range m {
				if !slices.Contains(rels, r) {
					rels = append(rels, r)
				}
			}
			continue
		}
		kept = append(kept, m)
	}
	s.merges = append(kept, rels)
	h.w.jfApplyMerges()
}

// unlink removes an item from its merged title like DELETE /Videos/{id}/AlternateSources (no file
// deleted).
func (h *jfAPI) unlink(id string) {
	s := h.w.jellyfin
	it, _ := s.findVersion(strings.ToLower(id))
	if it == nil {
		return
	}
	rel := it.versions[0].parts[0].rel
	kept := s.merges[:0:0]
	for _, m := range s.merges {
		m = slices.DeleteFunc(slices.Clone(m), func(r string) bool { return r == rel })
		if len(m) >= 2 {
			kept = append(kept, m)
		}
	}
	s.merges = kept
	h.w.jfApplyMerges()
}

// ---------------------------------------------------------------------------
// Controls and lookups
// ---------------------------------------------------------------------------

// JellyfinScan runs a library scan: the whole tree is read again (ghost entries disappear, new
// files appear), the .ignore cache is cleared and pending notifications are dropped (the scan
// covers them). /ScheduledTasks reports it as the last completed "Scan Media Library".
func (e *Env) JellyfinScan() {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	s := e.w.jellyfin
	if s == nil {
		return
	}
	s.ignoreCache = map[string]bool{}
	s.pending = nil
	e.w.jfResolveAll()
	s.lastScan = e.w.now()
}

// JellyfinApplyPending applies every pending notification now, as if LibraryMonitorDelay passed.
func (e *Env) JellyfinApplyPending() {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	if e.w.jellyfin != nil {
		e.w.jfApplyDue(true)
	}
}

// JellyfinPending reports whether notifications wait for the monitor delay.
func (e *Env) JellyfinPending() bool {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return e.w.jellyfin != nil && len(e.w.jellyfin.pending) > 0
}

// SetJellyfinSessions replaces the sessions GET /Sessions reports.
func (e *Env) SetJellyfinSessions(sessions ...JellyfinSession) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.sessions = append([]JellyfinSession(nil), sessions...)
}

// SetJellyfinServerID changes the server id Jellyfin answers with (another server at the URL).
func (e *Env) SetJellyfinServerID(id string) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.serverID = strings.ToLower(id)
}

// SetJellyfinVersion changes the version Jellyfin reports.
func (e *Env) SetJellyfinVersion(v string) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.version = v
}

// SetJellyfinPathSubstitutions replaces the path substitutions /System/Configuration reports.
func (e *Env) SetJellyfinPathSubstitutions(subst ...JellyfinPathSubstitution) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.subst = append([]JellyfinPathSubstitution(nil), subst...)
}

// AddJellyfinLibrary adds a library (a user adding one in Jellyfin's dashboard); its folders are
// read at once, like the scan Jellyfin runs for a new library.
func (e *Env) AddJellyfinLibrary(l JellyfinLibrary) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	s := e.w.jellyfin
	s.libs = append(s.libs, &jfLibrary{id: jfID(jfTypeCollection, l.Name), name: l.Name, collection: l.CollectionType, dirs: append([]string(nil), l.Dirs...)})
	e.w.jfResolveAll()
}

// SetJellyfinTotalDrift adds n to TotalRecordCount on every listing page after the first (a
// library that changes while it is listed).
func (e *Env) SetJellyfinTotalDrift(n int) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.totalDrift = n
}

// SetJellyfinOmitPartSize makes AdditionalParts answers carry no Size (UNVERIFIED field).
func (e *Env) SetJellyfinOmitPartSize(on bool) {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.jellyfin.omitSize = on
}

// JellyfinNotifications returns the POST /Library/Media/Updated requests received.
func (e *Env) JellyfinNotifications() []JellyfinNotification {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	return append([]JellyfinNotification(nil), e.w.jellyfin.notified...)
}

// JellyfinMediaPath returns the path the Jellyfin server sees for a media-root relative path.
func (e *Env) JellyfinMediaPath(rel string) string { return e.w.jellyfin.remote(rel) }

// JellyfinSourceID returns the id of the version whose first file is rel as listed now ("" when
// none; files never listed get the id Jellyfin would derive for a movie).
func (e *Env) JellyfinSourceID(rel string) string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	for _, it := range e.w.jellyfin.items {
		for _, v := range it.versions {
			if v.parts[0].rel == rel {
				return v.id
			}
		}
	}
	return ""
}

// JellyfinRowID returns the id of the row that lists a version whose first file is rel ("" when
// none).
func (e *Env) JellyfinRowID(rel string) string {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	for _, it := range e.w.jellyfin.items {
		for _, v := range it.versions {
			if v.parts[0].rel == rel && !it.hidden {
				return it.id()
			}
		}
	}
	return ""
}

// JellyfinPartID returns the item id of a stack part file (parts 2 and later).
func (e *Env) JellyfinPartID(rel string) string {
	return jfID(jfTypeVideo, e.w.jellyfin.remote(rel))
}

// JellyfinListed reports whether any listed version (ghosts included) has the file rel.
func (e *Env) JellyfinListed(rel string) bool {
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	for _, it := range e.w.jellyfin.items {
		for _, v := range it.versions {
			for _, p := range v.parts {
				if p.rel == rel {
					return true
				}
			}
		}
	}
	return false
}

// JellyfinLibraryID returns the ItemId of the library named name ("" when none).
func (e *Env) JellyfinLibraryID(name string) string {
	for _, l := range e.w.jellyfin.libs {
		if l.name == name {
			return l.id
		}
	}
	return ""
}

// WriteFile writes a small file (a .strm, a sidecar, a .ignore) at a media-root relative path.
func (e *Env) WriteFile(rel, content string) error {
	if !validRel(rel) {
		return fmt.Errorf("fakemedia: invalid relative path %q", rel)
	}
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	lp := e.w.local(rel)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return err
	}
	return os.WriteFile(lp, []byte(content), 0o644)
}
