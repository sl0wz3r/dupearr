#!/bin/sh
# Seeds the Dupearr demo (deploy/demo/docker-compose.yml) through Dupearr's API, like
# deploy/test-env/setup.sh does for the local test environment: the fake Plex server, Radarr,
# Radarr 4K, Sonarr and Tautulli of the demo-media service (tools/fakemedia, scenario "default"), identity
# path mappings, one scope group for "Movies" + "Movies 4K", demo settings (dry run on, minimum
# age 0 h, a recycle bin) and a first duplicate scan.
#
# It runs as the demo's one-shot "seed" service, from the demo-media image (docker/fakemedia.Dockerfile
# installs it as /usr/local/bin/dupearr-demo-seed, next to curl and jq). It reads Dupearr's API key
# from /config/config.xml (Dupearr's config volume, mounted read-only, as PUID so the 0600 file is
# readable) and talks to Dupearr over the demo's Docker network only.
#
# Idempotent: every `docker compose up` runs it again. Connections, mappings and scope groups that
# exist are left alone, and the settings are only written while no recycle bin is configured, so a
# restarted demo keeps what you changed. The scan always runs again: restarting demo-media resets
# the fake library to the scenario.
#
# Environment (defaults fit the compose file): DUPEARR_URL, CONFIG_XML, MEDIA_HOST, DEMO_PORT.
set -eu

DUPEARR_URL=${DUPEARR_URL:-http://dupearr:3873}
CONFIG_XML=${CONFIG_XML:-/config/config.xml}
MEDIA_HOST=${MEDIA_HOST:-demo-media}
DEMO_PORT=${DEMO_PORT:-3873}

# The fake servers' fixed test credentials (tools/fakemedia). Public values: the fakes are only
# reachable inside the demo's Docker network and serve nothing but empty placeholder files.
PLEX_TOKEN="fAkEpLeXtOkEn0000001"
RADARR_KEY="fa4e0000000000000000000000007878"
RADARR4K_KEY="fa4e0000000000000000000000007879"
SONARR_KEY="fa4e0000000000000000000000008989"
TAUTULLI_KEY="fa4e0000000000000000000000008181"

# Same layout as the fakes: the media lives under /data/media for Plex, the *arrs and Dupearr.
MEDIA_PATH=/data/media
RECYCLE_BIN=/data/dupearr-recycle
SCOPE_GROUP=movies

say() { printf '==> %s\n' "$*"; }
die() {
	printf 'seed: %s\n' "$*" >&2
	exit 1
}

for tool in curl jq sed; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is missing (run this in the demo-media image)"
done

# --- wait for Dupearr and its API key -----------------------------------------------------------
say "Waiting for Dupearr at $DUPEARR_URL"
i=0
until curl -fs -o /dev/null --max-time 5 "$DUPEARR_URL/ping"; do
	i=$((i + 1))
	[ "$i" -le 180 ] || die "Dupearr did not answer $DUPEARR_URL/ping within 3 minutes (docker compose logs dupearr)"
	sleep 1
done

APIKEY=""
i=0
while :; do
	if [ -r "$CONFIG_XML" ]; then
		APIKEY=$(sed -n 's:.*<ApiKey>\([0-9A-Za-z]*\)</ApiKey>.*:\1:p' "$CONFIG_XML" | head -n 1)
	fi
	[ -z "$APIKEY" ] || break
	i=$((i + 1))
	[ "$i" -le 30 ] || die "no API key in $CONFIG_XML (is Dupearr's config volume mounted, and does this container run as its PUID?)"
	sleep 1
done

# api METHOD PATH [JSON]: one API call; prints the response body, fails (with the body) on an error.
api() {
	if [ $# -ge 3 ]; then
		curl -sS --fail-with-body --max-time 60 -X "$1" -H "X-Api-Key: $APIKEY" \
			-H "Content-Type: application/json" --data "$3" "$DUPEARR_URL/api/v1$2"
	else
		curl -sS --fail-with-body --max-time 60 -X "$1" -H "X-Api-Key: $APIKEY" "$DUPEARR_URL/api/v1$2"
	fi
}

# --- connections ----------------------------------------------------------------------------------
PLEX_URL="http://$MEDIA_HOST:32400"
if api GET /mediaserver | jq -e --arg url "$PLEX_URL" 'any(.[]; .url == $url)' >/dev/null; then
	say "Fake Plex: already configured"
else
	say "Adding the fake Plex server ($PLEX_URL)"
	api POST /mediaserver "$(jq -nc --arg url "$PLEX_URL" --arg token "$PLEX_TOKEN" \
		'{name: "Fake Plex", kind: "plex", url: $url, token: $token, enabled: true, verifyTls: false}')" >/dev/null
fi
SERVER_ID=$(api GET /mediaserver | jq -r --arg url "$PLEX_URL" 'first(.[] | select(.url == $url) | .id)')

add_arr() { # add_arr NAME KIND PORT KEY
	url="http://$MEDIA_HOST:$3"
	if api GET /arr | jq -e --arg url "$url" 'any(.[]; .url == $url)' >/dev/null; then
		say "$1: already configured"
	else
		say "Adding $1 ($url)"
		api POST /arr "$(jq -nc --arg name "$1" --arg kind "$2" --arg url "$url" --arg key "$4" \
			'{name: $name, kind: $kind, url: $url, apiKey: $key, enabled: true, verifyTls: false, tags: []}')" >/dev/null
	fi
}
add_arr "Radarr" radarr 7878 "$RADARR_KEY"
add_arr "Radarr 4K" radarr 7879 "$RADARR4K_KEY"
add_arr "Sonarr" sonarr 8989 "$SONARR_KEY"

# Tautulli: the play history of the fake Plex server (profiles may rank by it: Played, Last played).
TAUTULLI_URL="http://$MEDIA_HOST:8181"
if api GET /tautulli | jq -e --arg url "$TAUTULLI_URL" 'any(.[]; .url == $url)' >/dev/null; then
	say "Tautulli: already configured"
else
	say "Adding Tautulli ($TAUTULLI_URL)"
	api POST /tautulli "$(jq -nc --arg url "$TAUTULLI_URL" --arg key "$TAUTULLI_KEY" --argjson sid "$SERVER_ID" \
		'{name: "Tautulli", serverId: $sid, url: $url, apiKey: $key, enabled: true, verifyTls: false}')" >/dev/null
fi

# --- identity path mappings (every app sees the same /data/media) --------------------------------
map_identity() { # map_identity SOURCE_TYPE SOURCE_ID
	if api GET /pathmapping | jq -e --arg type "$1" --argjson id "$2" --arg path "$MEDIA_PATH" \
		'any(.[]; .sourceType == $type and .sourceId == $id and .remotePath == $path)' >/dev/null; then
		return 0
	fi
	api POST /pathmapping "$(jq -nc --arg type "$1" --argjson id "$2" --arg path "$MEDIA_PATH" \
		'{sourceType: $type, sourceId: $id, remotePath: $path, localPath: $path}')" >/dev/null
}
say "Path mappings: $MEDIA_PATH -> $MEDIA_PATH for Plex and each *arr"
map_identity server "$SERVER_ID"
for id in $(api GET /arr | jq -r --arg prefix "http://$MEDIA_HOST:" '.[] | select(.url | startswith($prefix)) | .id'); do
	map_identity arr "$id"
done

# --- scope group: match "Movies" against "Movies 4K" ----------------------------------------------
# The libraries are synced when the server is added; wait briefly in case that is still running.
i=0
until [ "$(api GET /library | jq --argjson sid "$SERVER_ID" \
	'[.[] | select(.serverId == $sid and (.title == "Movies" or .title == "Movies 4K"))] | length')" -ge 2 ]; do
	i=$((i + 1))
	[ "$i" -le 30 ] || die "the fake Plex server's libraries \"Movies\" and \"Movies 4K\" did not show up"
	sleep 1
done
for id in $(api GET /library | jq -r --argjson sid "$SERVER_ID" \
	'.[] | select(.serverId == $sid and (.title == "Movies" or .title == "Movies 4K") and (.scopeGroup // "") == "") | .id'); do
	say "Library $id: scope group \"$SCOPE_GROUP\" (cross-library matching of Movies and Movies 4K)"
	api PUT "/library/$id" "$(jq -nc --arg group "$SCOPE_GROUP" '{enabled: true, scopeGroup: $group}')" >/dev/null
done

# --- demo settings (first run only) ---------------------------------------------------------------
if [ "$(api GET /config/settings | jq -r '.recycleBinPath // ""')" = "" ]; then
	say "Settings: dry run ON, minimum age 0 h (the fake files are brand new), recycle bin $RECYCLE_BIN"
	api PUT /config/settings "$(jq -nc --arg bin "$RECYCLE_BIN" \
		'{dryRun: true, minAgeHours: 0, recycleBinPath: $bin}')" >/dev/null
else
	say "Settings: already configured (left as they are)"
fi

# --- duplicate scan -------------------------------------------------------------------------------
say "Running a duplicate scan"
CMD_ID=$(api POST /command '{"name":"DuplicateScan"}' | jq -r '.id')
i=0
while :; do
	STATE=$(api GET "/command/$CMD_ID" | jq -r '.status + "|" + (.message // "")')
	case "$STATE" in completed\|* | failed\|* | aborted\|*) break ;; esac
	i=$((i + 1))
	[ "$i" -le 300 ] || die "the scan did not finish within 5 minutes (docker compose logs dupearr)"
	sleep 1
done
case "$STATE" in
completed\|*) ;;
*) die "the scan ended with status ${STATE%%|*}: ${STATE#*|}" ;;
esac
SUMMARY=$(api GET /duplicate/stats | jq -r \
	'"\(.total) duplicate groups (" + ([.byStatus | to_entries[] | select(.value > 0) | "\(.value) \(.key)"] | join(", ")) + ")"')
say "Scan: ${STATE#*|}"
say "$SUMMARY"

if [ "$(api GET /config/settings | jq -r '.dryRun')" = true ]; then
	DRY_RUN="Dry run is ON: approving a group only records what would be deleted."
else
	DRY_RUN="Dry run is OFF: approved removals really delete (fake) files."
fi
cat <<EOF

  Dupearr demo is ready: http://localhost:$DEMO_PORT  (no login: it only listens on 127.0.0.1)

  Everything here is FAKE: a made-up library of empty placeholder files in a Docker volume,
  served by fake Plex/Radarr/Sonarr servers. Nothing touches your own media or servers.
  $DRY_RUN
  Remove the demo and its volumes:  docker compose down -v
EOF
