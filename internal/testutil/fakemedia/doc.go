// Package fakemedia is a realistic, in-memory fake of a Plex Media Server plus Radarr / Sonarr
// (API v3) instances and a Tautulli (API v2), backed by a real directory tree of sparse dummy video
// files. It exists for
// integration and end-to-end tests, the local demo and web-UI development — it is NOT used by the
// Dupearr binary.
//
// # Fidelity
//
// The fake emulates exactly the endpoints and wire formats Dupearr uses, as documented (and
// verified against primary sources) in docs/research/plex-api.md and docs/research/arr-api.md:
//
//   - Plex: GET /identity (no auth), GET / (allowMediaDeletion only when enabled — absent means
//     disabled), /:/prefs and /:/prefs/get?id=, /library/sections (Directory.scanner: "Plex Movie" /
//     "Plex TV Series", or the legacy disc-image scanner), /library/sections/{id}/all (type=1|2|3|4,
//     includeGuids, duplicate, X-Plex-Container-Start/Size paging with X-Plex-Container-* response
//     headers and offset/size/totalSize), /library/metadata/{ids} (checkFiles → Part.exists /
//     Part.accessible from the real files, full Stream arrays incl. Dolby Vision fields, colorTrc,
//     displayTitle/extendedDisplayTitle; numbers-as-strings where Plex's own examples do that),
//     show/season metadata, /children, /allLeaves, DELETE /library/metadata/{rk}/media/{mediaId}
//     (only with allowMediaDeletion; deletes the backing files; HTML 400 otherwise), PUT
//     /library/metadata/{rk}/refresh and GET|POST /library/sections/{id}/refresh?path= (files
//     that vanished are put in the Plex "trash": the media stays listed with exists=false; a
//     scenario file whose media is gone but that is back on disk — e.g. restored from a recycle
//     bin — is re-detected as a new media with new ids; the item keeps its addedAt, only the
//     part key carries the new timestamp),
//     /status/sessions and /photo/:/transcode (tiny PNG). Token auth via the X-Plex-Token header or
//     query parameter, 401 HTML otherwise; JSON only when Accept: application/json is sent.
//   - Radarr / Sonarr (/api/v3, X-Api-Key): system/status, movie (embedded movieFile),
//     moviefile?movieId=, DELETE moviefile/{id} (deletes the file or moves it to the instance's
//     recycle bin; 404 unknown; 409 when the movie's root folder is missing or empty), command
//     (RescanMovie/RescanSeries adopt the LARGEST untracked video file, like the real import
//     engine), movie/editor, exclusions, series, episode?seriesId=, episodefile?seriesId=,
//     DELETE episodefile/{id}, episode/monitor, importlistexclusion, config/mediamanagement,
//     queue (paged), tag and rootfolder. Optional URL base (307 when omitted) and a "starting up"
//     mode (503).
//   - Tautulli (/api/v2, docs/research/watch-history.md): get_tautulli_info, get_server_info (the
//     fake Plex's machine identifier), get_users, get_library (an unknown section gets Tautulli's
//     "Local" defaults) and get_history with Tautulli's semantics — comma-separated rating_key
//     lists, guid prefix matching, section_id, grouping and live activity ON unless turned off (a
//     live row has a null row_id), start/length paging (default 25) with recordsFiltered, and
//     HTML-escaped strings. The plays come from [Movie].Plays (a play can lie under a retired
//     rating key: Plex re-created the item); libraries and users can keep no history. The key is
//     read from ?apikey= (a violation, RuleTautulliKeyInURL) or, like 2.18.0+, the X-Api-Key header;
//     [Env.SetTautulliMode] emulates an older Tautulli, another Plex server, an error result, a
//     short page, no rating-key list support, or an outage. The "watch" scenario ([Watch]) pairs
//     played and unplayed copies across Movies and Movies 4K.
//
// # Filesystem layout
//
// Every fake server "sees" the TRaSH-style Docker layout under [RemoteRoot] ("/data"): media lives
// under /data/media/{movies,movies4k,tv}/… and *arr recycle bins under /data/recycle/…. The same
// tree exists locally under [Env.Dir]; configure Dupearr with the path mappings returned by
// [Env.PathMappings] (/data/media → <Env.MediaRoot>). Files are created with os.Truncate, so they
// report realistic sizes (tens of GB) while using almost no disk space on filesystems with sparse
// file support (APFS, ext4, XFS, btrfs, ZFS). Hard links are real (os.Link).
//
// # Full-disc backups
//
// [Movie].Discs / [Show].Discs place full-disc backups ([Disc]) in movie and season folders, and the
// "discs" scenario ([Discs]) covers the layouts of docs/research/disc-structures.md: a UHD BDMV
// with 300 clips next to a remux MKV, a 1080p BDMV (MakeMKV), a DVD VIDEO_TS, an ISO, a
// "Disc 1"/"Disc 2" set with a "Bonus Disc", a disc-only folder, a clip Radarr tracks after a
// manual import, a damaged disc, a standalone .m2ts (not a disc) and a TV season disc. The trees
// are complete and valid — index.bdmv, MovieObject.bdmv, PLAYLIST/*.mpls (a multi-clip main
// feature 00800.mpls, warning/trailer playlists and a looping menu playlist longer than the
// feature), CLIPINF/*.clpi, BACKUP/, CERTIFICATE/, AACS/, VIDEO_TS IFO/BUP/VOB, ISO 9660/UDF
// descriptors — written by encoders of this package that are independent of Dupearr's reader
// (internal/disc), with sparse stream files. Plex's default scanners skip discs (a folder with an
// MKV next to BDMV/ is one version; a disc-only folder has no item); [Options].DiscImageScanner
// (or Library.Scanner) emulates the legacy "Plex Movie Scanner with Disc Image Support": each disc
// becomes one version with one Part per BDMV/STREAM clip. Radarr/Sonarr rescans never adopt disc
// clips (no year in the name); Disc.Tracked emulates a manual in-place import of the main clip.
// [Env.DiscFixtures] returns the ground truth (main playlist, duration, clips, sizes, owned
// entries) and [Env.AssertDiscsIntact] checks every disc is complete or gone as a whole.
//
// # Loose clip sets
//
// [Movie].ClipSets place flattened disc backups ([ClipSet]) in movie folders: the numbered STREAM
// clips of a Blu-ray (00174.m2ts, 00004.1.m2ts …) or the VOBs of a DVD lying loose, without BDMV/
// or VIDEO_TS/. Plex's default scanner lists EVERY loose clip as its own version (container "ts")
// of the movie item, which is how one backup turns into a hundred "copies". Clips can be left out
// of Plex (Clip.Unlisted), loose navigation files written next to them (ClipSet.Playlist,
// ClipSet.NavFiles) and one clip tracked by Radarr (Clip.Tracked; a rescan adopts a loose clip
// because the folder carries the year). The "looseclips" scenario ([LooseClips]) mirrors a real
// library stored this way: 60–190 clips per folder, a film split into short clips, a film spanning
// two clips, a folder with no film clip, a set of two clips and one of a single clip, clips next to
// an MKV, a flat DVD, a clip Radarr tracks and a full-length .ts as the control.
// [Env.ClipSetFixtures] returns the ground truth, [Env.ClipDeletes] every delete request that
// targeted a clip-named file (stale entries included), and [Env.DiscProblems] reports a set left
// incomplete.
//
// # Safety net for tests
//
// Besides the request log ([Env.Requests]; it keeps request headers, credentials included, for
// assertions) the fake records [Violation]s: calls Dupearr must never make (whole-item or library
// deletes, malformed or non-canonical delete paths, emptyTrash, merge/split, the proxy parameter,
// section refreshes with force or outside the section, *arr bulk deletes, rescans without an item
// id, the apikey query parameter of an *arr or Tautulli, …) and deletes that break the safety invariants of
// docs/ARCHITECTURE.md §6: deleting a Plex Optimized Version, a version sharing its file with
// another version, the last available version of an item, a multi-episode file another episode
// still needs, a file of an item that is playing, a file tracked by an *arr item carrying the
// keep tag, a file of a full-disc backup or a loose disc clip through Plex or an *arr (see the
// Rule* constants and [Env.SetKeepTag]). The fake still performs these requests like the real
// server would; end-to-end tests should finish with [Env.AssertNoViolations] (and
// [Env.AssertDiscsIntact] when the scenario has discs or loose clip sets).
// [Env.InjectFault] makes matching requests fail (any status code and body, e.g. a 503 or an HTML
// error page) to exercise error handling.
//
// Radarr instances honour their configured version where the wire format changed: before
// 5.22.4.9896 the movieFile embedded in GET /movie carries a misleading "customFormatScore": 0, and
// before 5.3.3.8535 GET /moviefile binds a single movieId and returns only the first file.
//
// Controls such as [Env.SetAllowDeletion], [Env.SetPlaying], [Env.AddQueueItem],
// [Env.SetStartingUp], [Env.SetRecycleBin], [Env.Unmount] and [Env.CreateFile] change the world
// while it runs; every method is safe for concurrent use.
//
// # Several Plex servers
//
// One [Env] serves one Plex server. A second server runs as a second Env: on its own tree (a
// server on another host, e.g. [MirrorServer]: the same relative paths and sizes, other files), or
// over the first Env's tree with [Options].ShareMedia ([SharedServer]: two servers on one share).
// A shared Env never resets or removes the tree, creates only files it declares that are missing
// (an existing file must have the declared size) and serves a Plex server only. A file deleted
// through one server stays listed by the other until that server refreshes, while its checkFiles
// reports exists=false — as a real second server behaves between its scans. [PlexServer].MediaRoot
// makes a server see the media root at another path (e.g. /srv; the *arrs keep /data/media), and
// [Env.PathMappings] follows it. /library/sections reports scannedAt (bumped by every section
// refresh), contentChangedAt (bumped only when a refresh trashed or found media) and refreshing;
// [Env.SetSectionState] sets them and [Env.SetMachineIdentifier] makes the server answer as
// another one. Outages use [Env.InjectFault] with Server [ServerPlex]. Two assertions check the
// multi-server safety rules at the end of a test: [Env.AssertEveryItemHasAFile]
// ([RuleDeleteOtherServerLastCopy]: an item that had a file has none now) and [TitlesWithoutFile]
// ([RuleDeleteLastCopyAnywhere]: a title has no file on any server).
//
// # Jellyfin
//
// [Scenario].Jellyfin (see [Scenario.WithJellyfin] and [JellyfinAppendixA], the sample tree of
// docs/research/jellyfin-emby.md Appendix A) adds a fake Jellyfin 12.1 over the same tree
// ([Env].Jellyfin, [ServerJellyfin]). Unlike the fake Plex it does not list declared items: it
// resolves them from the files on disk with a port of Jellyfin's rules (version folders, stacks,
// mixed folders, episode versions, .strm files, .ignore files; jellyfinstate.go), with ids derived
// from type and path like Jellyfin's. It serves the requests Dupearr's allowlist permits
// (/System/Info[/Public], /System/Configuration, /Library/VirtualFolders, /Items with its filters,
// /Videos/{id}/AdditionalParts, /Sessions, /ScheduledTasks, POST /Library/Media/Updated),
// authenticates the Authorization header's MediaBrowser Token (the API key acts as an
// administrator, [Env].JellyfinUserToken as a plain user), and keeps listing a removed file
// ("ghost") until a change notification and the library monitor's delay, or a library scan
// ([Env.JellyfinScan]). Its destructive endpoints (item and bulk
// delete, merge/unlink versions) behave like Jellyfin's — a delete removes the whole folder — and
// each records a Violation (RuleJellyfin*), as does a credential in the URL, a legacy header or any
// request outside the allowlist. Controls: [Env.SetJellyfinSessions], [Env.SetJellyfinServerID],
// [Env.SetJellyfinVersion], [Env.SetJellyfinPathSubstitutions], [Env.JellyfinNotifications], and
// the id lookups [Env.JellyfinSourceID], [Env.JellyfinRowID] and [Env.JellyfinPartID].
//
// # Usage
//
//	env := fakemedia.Start(t, fakemedia.Default())
//	resp := get(env.Plex.URL+"/library/sections", env.PlexToken)
//	…
//	env.AssertNoViolations(t)
//
// Custom scenarios start from [Base] (standard server, libraries and *arr instances without
// content) and append [Movie] / [Show] values; the video/audio presets ([DolbyVisionUHD], [FHD],
// [TrueHDAtmos], …) keep them short. The CLI in tools/fakemedia serves a scenario for manual
// testing and UI development.
package fakemedia
