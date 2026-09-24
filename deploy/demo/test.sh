#!/bin/sh
# Smoke test of the one-command demo (deploy/demo/docker-compose.yml) with the images you name. It
# starts the demo as its own Compose project on 127.0.0.1:DEMO_PORT, waits for the seed service and
# checks through Dupearr's API (with the API key read from the config volume) that:
#   - the seed configured Dupearr: fake Plex, 3 *arrs, identity path mappings, the Movies/Movies 4K
#     scope group, dry run on, minimum age 0 h, a recycle bin;
#   - the scan found the default scenario's 10 groups (5 pending, 4 to review, 1 protected);
#   - only Dupearr is published, on 127.0.0.1, without login for local hosts only (auth None);
#   - the README's walk-through works: a dry-run approval records "would delete", a real removal
#     moves the file to the recycle bin (the shared /data volume is writable for PUID/PGID) and
#     Restore brings it back;
#   - running the seed again changes nothing it should keep (idempotent, settings left alone).
# Then it removes the project with its volumes (DEMO_KEEP=true leaves it running). Used by
# `make demo-local` and by the release workflow before the demo image is pushed.
#
#   DEMO_DUPEARR_IMAGE=dupearr:dev DEMO_MEDIA_IMAGE=dupearr-demo-media:dev sh deploy/demo/test.sh
#
# Environment: DEMO_DUPEARR_IMAGE and DEMO_MEDIA_IMAGE (required; local tags are used as they are,
# missing ones are pulled), DEMO_PORT (default 38731), DEMO_PROJECT (Compose project, default
# dupearr-demo-test; must not exist yet), DEMO_KEEP (true: leave the demo running).
# Needs Docker with Compose v2, curl and jq.
set -eu

here=$(cd "$(dirname "$0")" && pwd -P)
: "${DEMO_DUPEARR_IMAGE:?set DEMO_DUPEARR_IMAGE to the Dupearr image to test}"
: "${DEMO_MEDIA_IMAGE:?set DEMO_MEDIA_IMAGE to the demo-media image to test}"
DEMO_PORT=${DEMO_PORT:-38731}
project=${DEMO_PROJECT:-dupearr-demo-test}
keep=${DEMO_KEEP:-false}
export DEMO_DUPEARR_IMAGE DEMO_MEDIA_IMAGE DEMO_PORT

for tool in docker curl jq; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "test.sh: $tool is required" >&2
		exit 2
	}
done
case $DEMO_PORT in
'' | *[!0-9]*)
	echo "test.sh: DEMO_PORT must be a port number" >&2
	exit 2
	;;
esac

compose() { docker compose -f "$here/docker-compose.yml" -p "$project" "$@"; }
say() { printf '\n==> %s\n' "$*"; }
failures=0
ok() { printf '  ok    %s\n' "$*"; }
bad() {
	printf '  FAIL  %s\n' "$*"
	failures=$((failures + 1))
}
# check DESCRIPTION GOT WANT
check() {
	if [ "$2" = "$3" ]; then ok "$1: $2"; else bad "$1: got '$2', want '$3'"; fi
}

# down -v deletes volumes: never touch a project this script did not start.
if [ -n "$(docker ps -a -q --filter "label=com.docker.compose.project=$project")" ] ||
	[ -n "$(docker volume ls -q --filter "label=com.docker.compose.project=$project")" ]; then
	echo "test.sh: Compose project '$project' already exists; remove it or set DEMO_PROJECT" >&2
	exit 2
fi

started=0
cleanup() {
	status=$?
	trap - EXIT INT TERM
	if [ "$started" = 1 ]; then
		if [ "$status" != 0 ] || [ "$failures" != 0 ]; then
			say "Logs (last 40 lines per service)"
			compose logs --no-color --tail 40 || true
		fi
		if [ "$keep" = true ]; then
			say "DEMO_KEEP=true: the demo keeps running on http://127.0.0.1:$DEMO_PORT"
			echo "  remove it: docker compose -f $here/docker-compose.yml -p $project down -v"
		else
			say "Removing the demo (project $project, containers, network and volumes)"
			compose down -v --remove-orphans >/dev/null 2>&1 || true
		fi
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# wait_exit SERVICE SECONDS: waits for the service's (one-shot) container to exit; prints its code.
wait_exit() {
	cid=$(compose ps -a -q "$1")
	[ -n "$cid" ] || {
		echo "no container for service $1" >&2
		return 1
	}
	i=0
	while :; do
		state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$cid")
		case $state in exited*) break ;; esac
		i=$((i + 1))
		[ "$i" -le "$2" ] || {
			echo "$1 still running after $2 s" >&2
			return 1
		}
		sleep 1
	done
	echo "${state#exited }"
}

say "Starting the demo (project $project) on 127.0.0.1:$DEMO_PORT"
echo "  Dupearr:    $DEMO_DUPEARR_IMAGE"
echo "  demo-media: $DEMO_MEDIA_IMAGE"
started=1
compose up -d
code=$(wait_exit seed 300)
compose logs --no-color seed
check "seed exit code" "$code" 0

say "Checking the seeded demo through the API"
base="http://127.0.0.1:$DEMO_PORT"
key=$(compose exec -T dupearr sed -n 's:.*<ApiKey>\([0-9A-Za-z]*\)</ApiKey>.*:\1:p' /config/config.xml | tr -d '\r\n')
[ -n "$key" ] || {
	echo "could not read the API key from the config volume" >&2
	exit 1
}
api() { # api METHOD PATH [JSON]
	if [ $# -ge 3 ]; then
		curl -sS --fail-with-body --max-time 30 -X "$1" -H "X-Api-Key: $key" \
			-H "Content-Type: application/json" --data "$3" "$base/api/v1$2"
	else
		curl -sS --fail-with-body --max-time 30 -X "$1" -H "X-Api-Key: $key" "$base/api/v1$2"
	fi
}

check "Dupearr port" "$(compose port dupearr 3873)" "127.0.0.1:$DEMO_PORT"
check "demo-media published ports" "$(docker port "$(compose ps -q demo-media)" | wc -l | tr -d ' ')" 0
system=$(api GET /system/status)
check "instance name" "$(printf '%s' "$system" | jq -r .instanceName)" "Dupearr Demo"
check "authentication" "$(printf '%s' "$system" | jq -r .authentication)" "None"
check "no login from localhost (GET without key)" \
	"$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$base/api/v1/system/status")" 200
check "a public host name still needs a key" \
	"$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -H 'Host: dupearr.example.com' "$base/api/v1/system/status")" 401

check "media servers" "$(api GET /mediaserver | jq -c '[.[] | .url]')" '["http://demo-media:32400"]'
check "*arr instances" "$(api GET /arr | jq -c '[.[] | "\(.kind) \(.url)"] | sort')" \
	'["radarr http://demo-media:7878","radarr http://demo-media:7879","sonarr http://demo-media:8989"]'
check "path mappings (identity /data/media)" \
	"$(api GET /pathmapping | jq '[.[] | select(.remotePath == "/data/media" and .localPath == "/data/media")] | length')" 4
check "scope group movies" "$(api GET /library | jq -c '[.[] | select(.scopeGroup == "movies") | .title] | sort')" \
	'["Movies","Movies 4K"]'
check "settings" "$(api GET /config/settings | jq -c '{dryRun, minAgeHours, recycleBinPath}')" \
	'{"dryRun":true,"minAgeHours":0,"recycleBinPath":"/data/dupearr-recycle"}'
stats=$(api GET /duplicate/stats)
check "duplicate groups" "$(printf '%s' "$stats" | jq -c '{total, pending: .byStatus.pending, review: .byStatus.review, protected: .byStatus.protected}')" \
	'{"total":10,"pending":5,"review":4,"protected":1}'
check "last scan" "$(printf '%s' "$stats" | jq -c '.lastScan | {status, groupsFound: .stats.groupsFound, errors: .stats.errors}')" \
	'{"status":"completed","groupsFound":10,"errors":0}'

# group_id TITLE / approve TITLE / action_of GROUP_ID: the walk-through of deploy/demo/README.md.
group_id() { api GET '/duplicate?pageSize=100' | jq -r --arg t "$1" 'first(.records[] | select(.title == $t) | .id) // empty'; }
approve() {
	gid=$(group_id "$1")
	[ -n "$gid" ] || {
		bad "no group titled $1"
		return 0
	}
	sig=$(api GET "/duplicate/$gid" | jq -r .signature)
	api POST "/duplicate/$gid/approve" "$(jq -nc --arg s "$sig" '{signature: $s}')" >/dev/null
}
# Waits until the group's newest action is finished; prints it as JSON.
action_of() {
	i=0
	while :; do
		a=$(api GET '/action?pageSize=100&sortKey=id&sortDirection=descending' |
			jq -c --argjson g "$1" 'first(.records[] | select(.groupId == $g)) // empty')
		case $(printf '%s' "$a" | jq -r '.status // ""') in
		dry_run | succeeded | failed | skipped | cancelled)
			printf '%s\n' "$a"
			return 0
			;;
		esac
		i=$((i + 1))
		[ "$i" -le 60 ] || {
			printf '%s\n' "${a:-null}"
			return 0
		}
		sleep 1
	done
}

say "Walk-through: dry-run approval, real removal to the recycle bin, restore"
approve "Blade Runner 2049"
a=$(action_of "$(group_id "Blade Runner 2049")")
check "dry-run approval of Blade Runner 2049" "$(printf '%s' "$a" | jq -r '"\(.status) \(.dryRun)"')" "dry_run true"

api PUT /config/settings '{"dryRun":false,"deletionMethods":["arr","filesystem","plex"]}' >/dev/null
approve "The Matrix"
a=$(action_of "$(group_id "The Matrix")")
check "removal of The Matrix's 720p copy" "$(printf '%s' "$a" | jq -r '"\(.status) \(.method)"')" "succeeded filesystem"
bin_path=$(printf '%s' "$a" | jq -r '.recyclePath // ""')
case $bin_path in
/data/dupearr-recycle/*) ok "moved to the recycle bin: $bin_path" ;;
*) bad "recycle path: got '$bin_path', want one under /data/dupearr-recycle/" ;;
esac
if [ -n "$bin_path" ] && compose exec -T dupearr test -f "$bin_path"; then
	ok "the recycled file exists"
else
	bad "the recycled file is missing"
fi
action_id=$(printf '%s' "$a" | jq -r '.id')
restored=$(api POST "/action/$action_id/restore" | jq -r '.message')
case $restored in
"Restored from the recycle bin"*) ok "restore: $restored" ;;
*) bad "restore: $restored" ;;
esac
orig=$(printf '%s' "$a" | jq -r '.paths[0]')
if compose exec -T dupearr test -f "$orig"; then ok "the file is back: $orig"; else bad "the file is not back: $orig"; fi

say "Running the seed again (idempotent; keeps what the user changed)"
compose up -d
code=$(wait_exit seed 300)
check "second seed exit code" "$code" 0
check "connections after the second seed" \
	"$(api GET /mediaserver | jq length) $(api GET /arr | jq length) $(api GET /pathmapping | jq length)" "1 3 4"
check "settings after the second seed" "$(api GET /config/settings | jq -c '{dryRun, recycleBinPath}')" \
	'{"dryRun":false,"recycleBinPath":"/data/dupearr-recycle"}'

if [ "$failures" -ne 0 ]; then
	say "FAILED: $failures check(s)"
	exit 1
fi
say "Demo OK"
