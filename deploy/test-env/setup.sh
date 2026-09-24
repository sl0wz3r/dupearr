#!/bin/sh
# Pre-configures the local test environment (deploy/test-env/docker-compose.yml) through Dupearr's
# API: the fake Plex server, Radarr, Radarr 4K and Sonarr, identity path mappings, a shared scope
# group for "Movies" + "Movies 4K", test-friendly settings, and a first duplicate scan.
# Idempotent: connections that already exist are left alone. Needs curl and python3.
set -eu

cd "$(dirname "$0")"
COMPOSE="docker compose -f docker-compose.yml"
URL="${DUPEARR_URL:-http://localhost:3873}"

# The fake servers' fixed test credentials (tools/fakemedia, scenario "default").
PLEX_TOKEN="fAkEpLeXtOkEn0000001"
RADARR_KEY="fa4e0000000000000000000000007878"
RADARR4K_KEY="fa4e0000000000000000000000007879"
SONARR_KEY="fa4e0000000000000000000000008989"

say() { printf '\033[1;35m==>\033[0m %s\n' "$*"; }

say "Waiting for Dupearr at $URL"
i=0
until curl -fsS "$URL/ping" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 90 ]; then echo "Dupearr did not answer /ping — check: $COMPOSE logs dupearr" >&2; exit 1; fi
  sleep 1
done

APIKEY=$($COMPOSE exec -T dupearr sed -n 's:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p' /config/config.xml | tr -d '\r\n')
[ -n "$APIKEY" ] || { echo "could not read the API key from /config/config.xml" >&2; exit 1; }

api() { # api METHOD PATH [JSON]
  if [ $# -ge 3 ]; then
    curl -fsS -X "$1" -H "X-Api-Key: $APIKEY" -H "Content-Type: application/json" "$URL/api/v1$2" -d "$3"
  else
    curl -fsS -X "$1" -H "X-Api-Key: $APIKEY" "$URL/api/v1$2"
  fi
}
json_len() { python3 -c 'import sys,json; print(len(json.load(sys.stdin)))'; }

if [ "$(api GET /mediaserver | json_len)" = "0" ]; then
  say "Adding the fake Plex server"
  api POST /mediaserver '{"name":"Fake Plex","kind":"plex","url":"http://fakemedia:32400","token":"'"$PLEX_TOKEN"'","enabled":true,"verifyTls":false}' >/dev/null
else
  say "Plex server already configured"
fi
SERVER_ID=$(api GET /mediaserver | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])')

if [ "$(api GET /arr | json_len)" = "0" ]; then
  say "Adding Radarr, Radarr 4K and Sonarr"
  api POST /arr '{"name":"Radarr","kind":"radarr","url":"http://fakemedia:7878","apiKey":"'"$RADARR_KEY"'","enabled":true,"verifyTls":false,"tags":[]}' >/dev/null
  api POST /arr '{"name":"Radarr 4K","kind":"radarr","url":"http://fakemedia:7879","apiKey":"'"$RADARR4K_KEY"'","enabled":true,"verifyTls":false,"tags":[]}' >/dev/null
  api POST /arr '{"name":"Sonarr","kind":"sonarr","url":"http://fakemedia:8989","apiKey":"'"$SONARR_KEY"'","enabled":true,"verifyTls":false,"tags":[]}' >/dev/null
else
  say "*arr instances already configured"
fi

if [ "$(api GET /pathmapping | json_len)" = "0" ]; then
  say "Adding identity path mappings (/data/media → /data/media)"
  api POST /pathmapping '{"sourceType":"server","sourceId":'"$SERVER_ID"',"remotePath":"/data/media","localPath":"/data/media"}' >/dev/null
  for id in $(api GET /arr | python3 -c 'import sys,json; print(" ".join(str(a["id"]) for a in json.load(sys.stdin)))'); do
    api POST /pathmapping '{"sourceType":"arr","sourceId":'"$id"',"remotePath":"/data/media","localPath":"/data/media"}' >/dev/null
  done
else
  say "Path mappings already configured"
fi

say "Putting \"Movies\" and \"Movies 4K\" in the same scope group (cross-library matching)"
for id in $(api GET /library | python3 -c 'import sys,json; print(" ".join(str(l["id"]) for l in json.load(sys.stdin) if l["title"] in ("Movies","Movies 4K")))'); do
  api PUT "/library/$id" '{"enabled":true,"scopeGroup":"movies"}' >/dev/null
done

say "Test-friendly settings: dry run ON, minimum age 0 h (the fake files are brand new), recycle bin /data/dupearr-recycle"
api PUT /config/settings '{"dryRun":true,"minAgeHours":0,"recycleBinPath":"/data/dupearr-recycle"}' >/dev/null

say "Running a duplicate scan"
CMD_ID=$(api POST /command '{"name":"DuplicateScan"}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
i=0
while :; do
  STATE=$(api GET "/command/$CMD_ID" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["status"] + "|" + d.get("message",""))')
  case "$STATE" in completed*|failed*|aborted*) break ;; esac
  i=$((i + 1)); [ "$i" -gt 120 ] && break
  sleep 1
done
say "Scan: ${STATE#*|}"

cat <<EOF

  Dupearr test environment is ready:  $URL
  First visit: create your login (first-run setup), then open Duplicates.

  Dry run is ON — approving only records what would be deleted. Turn it off in
  Settings → Media Management to try real removals against the fake library.
  Reset the fake library:   make test-env-reset
  Stop everything:          make test-env-down
EOF
