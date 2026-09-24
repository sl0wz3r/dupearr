#!/bin/sh
# Integration tests for the container image: docker/entrypoint.sh (PUID/PGID/UMASK/TZ, /config
# ownership, privilege drop, argument handling), the `dupearr` exec wrapper, signal delivery and
# the image configuration.
#
#   sh docker/test-image.sh [IMAGE]      (default: dupearr:latest)
#   make docker-test                     (builds the image for this machine first)
#   make docker-test-amd64               (builds IMAGE:VERSION-amd64, runs it as linux/amd64)
#   PLATFORM=linux/amd64 sh docker/test-image.sh IMAGE
#
# PLATFORM (optional) is the platform the image runs as (docker run/create --platform, through
# DOCKER_DEFAULT_PLATFORM). Left empty, it becomes the image's own platform whenever that differs
# from the Docker daemon's, e.g. a linux/amd64 image on Apple Silicon (Rosetta) or on an arm64
# server (QEMU): docker then prints no "platform does not match" warning into the output the tests
# compare, and the tests allow for the emulator's own traces (see config_entries). An image of the
# daemon's platform runs without --platform, as before (older daemons need no flag).
#
# /app/dupearr is replaced (docker cp) by a stub that prints its uid/gid/umask/arguments, so the
# entrypoint is tested in the real image (alpine, busybox, su-exec, tini) independently of the
# application. A few tests run the real binary: version/healthcheck, and a real server (becomes
# healthy, serves the web UI, restarts in place, stops cleanly on SIGTERM, owns its files).
# Only named volumes, docker cp and docker exec are used, so the script works when the Docker
# daemon is remote (CI jobs that talk to the host's daemon).
# Everything it creates is labelled and removed on exit. Exit status: 0 when all tests pass.
#
# shellcheck disable=SC2016 # single-quoted scripts are expanded inside the container on purpose
set -eu

IMAGE=${1:-dupearr:latest}
PLATFORM=${PLATFORM:-}
LABEL="dupearr.test=$$-$(date +%s)"
TMP=$(mktemp -d)
STUB=$TMP/dupearr
PASSED=0
FAILED=0

cleanup() {
	for _c in $(docker ps -aq --filter "label=$LABEL"); do
		docker rm -f "$_c" >/dev/null 2>&1 || true
	done
	for _v in $(docker volume ls -q --filter "label=$LABEL"); do
		docker volume rm -f "$_v" >/dev/null 2>&1 || true
	done
	rm -rf "$TMP"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

cat >"$STUB" <<'EOF'
#!/bin/sh
# Test stand-in for /app/dupearr: reports how it was started.
printf 'STUB uid=%s gid=%s groups=%s umask=%s home=%s args=%s\n' \
	"$(id -u)" "$(id -g)" "$(id -G)" "$(umask)" "${HOME:-}" "$*"
case " $* " in
*" --nobrowser "*)
	# Server mode: write into /config like the real server. With STUB_STAY, stay up, but only as
	# the container's main process (child of tini, PID 1), never when started by docker exec.
	: >/config/stub-created
	if [ -n "${STUB_STAY:-}" ] && [ "$PPID" = 1 ]; then
		exec sleep 600
	fi
	;;
esac
EOF
chmod 0755 "$STUB"

pass() {
	PASSED=$((PASSED + 1))
	printf 'ok    %s\n' "$1"
}

fail() {
	FAILED=$((FAILED + 1))
	printf 'FAIL  %s\n' "$1"
	if [ -n "${2:-}" ]; then
		printf '%s\n' "$2" | sed 's/^/      | /'
	fi
}

# has TEXT NEEDLE: whether TEXT contains NEEDLE.
has() {
	case $1 in
	*"$2"*) return 0 ;;
	esac
	return 1
}

# expect NAME WANT_RC NEEDLE...: checks $RC and that $OUT contains every NEEDLE; a NEEDLE that
# starts with "!" must not occur.
expect() {
	_name=$1
	_want=$2
	shift 2
	_bad=""
	if [ "$RC" != "$_want" ]; then
		_bad="exit code $RC, want $_want"
	fi
	for _n in "$@"; do
		case $_n in
		!*) if has "$OUT" "${_n#!}"; then _bad="$_bad${_bad:+; }unexpected '${_n#!}'"; fi ;;
		*) if ! has "$OUT" "$_n"; then _bad="$_bad${_bad:+; }missing '$_n'"; fi ;;
		esac
	done
	if [ -z "$_bad" ]; then
		pass "$_name"
	else
		fail "$_name: $_bad" "$OUT"
	fi
}

# expect_equal NAME WANT: $OUT must equal WANT exactly.
expect_equal() {
	if [ "$OUT" = "$2" ]; then
		pass "$1"
	else
		fail "$1: output differs" "$(printf 'got:\n%s\nwant:\n%s' "$OUT" "$2")"
	fi
}

# volume: creates a labelled named volume and prints its name.
volume() {
	docker volume create --label "$LABEL"
}

# run DOCKER_CREATE_ARGS...: creates a container (arguments include the image and command), swaps
# in the stub binary, runs it attached and sets OUT (stdout + stderr) and RC.
run() {
	_cid=$(docker create --label "$LABEL" "$@")
	docker cp "$STUB" "$_cid:/app/dupearr" >/dev/null
	if OUT=$(docker start -a "$_cid" 2>&1); then RC=0; else RC=$?; fi
	docker rm -f "$_cid" >/dev/null 2>&1 || true
}

# as_root CONFIG_VOLUME DATA_VOLUME SCRIPT: runs SCRIPT as root with the volumes at /config and
# /data (fixtures and inspection); sets OUT and RC.
as_root() {
	if OUT=$(docker run --rm --label "$LABEL" -v "$1:/config" -v "$2:/data" \
		--entrypoint /bin/sh "$IMAGE" -c "$3" 2>&1); then RC=0; else RC=$?; fi
}

# os_arch PLATFORM: the "os/arch" part of a platform, without the variant (linux/arm64/v8 ->
# linux/arm64). It decides whether an image runs natively on the daemon.
os_arch() {
	printf '%s\n' "$1" | cut -d/ -f1-2
}

# config_entries CONFIG_VOLUME DATA_VOLUME: sets OUT to the entries of /config (`ls -A`, one per
# line). Under emulation the emulator's own cache is left out: Rosetta keeps its translations in
# $HOME/.cache/rosetta, and $HOME is /config for PUID, so a /config/.cache that holds nothing but
# "rosetta" is not Dupearr's. Natively every entry counts.
config_entries() {
	if [ "$EMULATED" = 1 ]; then
		as_root "$1" "$2" 'ls -A /config | { if [ "$(ls -A /config/.cache 2>/dev/null)" = rosetta ]; then grep -vxF .cache; else cat; fi; }; true'
	else
		as_root "$1" "$2" 'ls -A /config'
	fi
}

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
	echo "image $IMAGE not found (build it first: make docker)" >&2
	exit 2
fi

# Platform: see PLATFORM in the header. DOCKER_DEFAULT_PLATFORM makes every docker run/create below
# pass --platform, so a foreign-architecture image starts without the CLI's mismatch warning.
HOST_PLATFORM=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}' 2>/dev/null || true)
IMAGE_PLATFORM=$(docker image inspect --format '{{.Os}}/{{.Architecture}}{{with .Variant}}/{{.}}{{end}}' "$IMAGE" 2>/dev/null || true)
if [ -n "$PLATFORM" ] && [ -n "$IMAGE_PLATFORM" ] && [ "$(os_arch "$PLATFORM")" != "$(os_arch "$IMAGE_PLATFORM")" ]; then
	# docker run --platform would look for (and try to pull) another image of that name.
	echo "PLATFORM=$PLATFORM, but $IMAGE is a $IMAGE_PLATFORM image" >&2
	exit 2
fi
if [ -z "$PLATFORM" ] && [ -n "$HOST_PLATFORM" ] && [ -n "$IMAGE_PLATFORM" ] &&
	[ "$(os_arch "$IMAGE_PLATFORM")" != "$HOST_PLATFORM" ]; then
	PLATFORM=$IMAGE_PLATFORM
fi
EMULATED=0
if [ -n "$PLATFORM" ]; then
	DOCKER_DEFAULT_PLATFORM=$PLATFORM
	export DOCKER_DEFAULT_PLATFORM
	if [ "$(os_arch "$PLATFORM")" != "$HOST_PLATFORM" ]; then
		EMULATED=1
	fi
fi
if [ "$EMULATED" = 1 ]; then
	echo "Testing $IMAGE as $PLATFORM (emulated on ${HOST_PLATFORM:-an unknown daemon platform})"
else
	echo "Testing $IMAGE${PLATFORM:+ as $PLATFORM}"
fi

# --------------------------------------------------------------------------------------------
# Image configuration
# --------------------------------------------------------------------------------------------
if OUT=$(docker image inspect --format '{{json .Config}}' "$IMAGE" 2>&1); then RC=0; else RC=$?; fi
expect "image config" 0 \
	'"Entrypoint":["/sbin/tini","--","/entrypoint.sh"]' \
	'"3873/tcp":{}' \
	'"/config":{}' \
	'"Test":["CMD","/app/dupearr","healthcheck","--data","/config"]' \
	'"StartInterval":5000000000' \
	'"StopSignal":"SIGTERM"' \
	'"org.opencontainers.image.licenses":"GPL-3.0-or-later"' \
	'"DUPEARR_DOCKER=1"' \
	'!"Cmd":["/bin/sh"]'

# No USER in the image: the entrypoint must start as root to apply PUID/PGID (Unraid, compose).
if OUT=$(docker image inspect --format '{{.Config.User}}' "$IMAGE" 2>&1); then RC=0; else RC=$?; fi
expect_equal "image has no USER" ""

cfg=$(volume)
data=$(volume)
as_root "$cfg" "$data" 'for f in /app/LICENSE /sbin/tini /sbin/su-exec /usr/share/zoneinfo/Europe/Amsterdam; do [ -f "$f" ] || echo "missing $f"; done; [ -x /usr/local/bin/dupearr ] || echo "wrapper not executable"; [ -x /entrypoint.sh ] || echo "entrypoint not executable"; echo done'
expect_equal "image files" "done"

# The real binary through entrypoint -> su-exec (the only test that does not use the stub).
if OUT=$(docker run --rm --label "$LABEL" "$IMAGE" version 2>&1); then RC=0; else RC=$?; fi
expect "real binary: version" 0 "Dupearr "
if OUT=$(docker run --rm --label "$LABEL" "$IMAGE" healthcheck 2>&1); then RC=0; else RC=$?; fi
expect "real binary: healthcheck without a server is unhealthy" 1 "healthcheck:"
# Read-only root filesystem (Kubernetes readOnlyRootFilesystem): no passwd/group entries can be
# added, the start continues with numeric ids. docker cp cannot write the stub into a read-only
# container, so this runs the real binary with a bad flag (it exits right after the entrypoint).
cfg=$(volume)
if OUT=$(docker run --rm --label "$LABEL" --read-only -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" --no-such-flag 2>&1); then RC=0; else RC=$?; fi
expect "read-only root filesystem: numeric ids" 2 \
	"Could not create a user/group entry for 99:100" \
	"Starting Dupearr as 99:users (99:100)" \
	"flag provided but not defined: -no-such-flag"

# --------------------------------------------------------------------------------------------
# Defaults and identities
# --------------------------------------------------------------------------------------------
cfg=$(volume)
run -v "$cfg:/config" "$IMAGE"
expect "defaults: 1000:1000, umask 002, server args" 0 \
	"STUB uid=1000 gid=1000 groups=1000 umask=0002 home=/config args=--data /config --nobrowser" \
	"Starting Dupearr as dupearr:dupearr (1000:1000), umask 002, TZ=Etc/UTC" \
	"/data is not mounted"

cfg=$(volume)
run -e PUID=99 -e PGID=100 -e UMASK=022 -e TZ=America/Chicago -v "$cfg:/config" "$IMAGE"
expect "Unraid ids: reuse group users" 0 \
	"STUB uid=99 gid=100 groups=100 umask=0022" \
	"Starting Dupearr as dupearr:users (99:100), umask 022, TZ=America/Chicago"

run -e PUID=00099 -e PGID=0100 -v "$cfg:/config" "$IMAGE" version
expect "leading zeros are normalised" 0 "STUB uid=99 gid=100 " "args=version --data /config"

nobody_cfg=$(volume)
run -e PUID=65534 -e PGID=65534 -v "$nobody_cfg:/config" "$IMAGE" --nobrowser
expect "existing user is reused (nobody)" 0 "STUB uid=65534 gid=65534" "Starting Dupearr as nobody:nobody"

run -e PUID=0 -e PGID=0 -v "$cfg:/config" "$IMAGE" version
expect "PUID=0 warns and runs as root" 0 "WARNING: PUID=0" "STUB uid=0 gid=0"

run -e PUID= -e PGID= -e UMASK= -v "$cfg:/config" "$IMAGE" version
expect "empty variables mean defaults (Unraid passes empty values)" 0 "STUB uid=1000 gid=1000 " "umask=0002"

# PUID/PGID changing between starts of the same container (the container filesystem persists).
run -v "$cfg:/config" "$IMAGE" sh -c 'PUID=99 PGID=100 /entrypoint.sh version >/dev/null 2>&1 && PUID=1234 PGID=4321 /entrypoint.sh version && getent passwd dupearr && getent group dupearr && getent group users'
expect "changed ids recreate the user and group" 0 \
	"STUB uid=1234 gid=4321 " "dupearr:x:1234:4321:" "dupearr:x:4321:" "users:x:100:"

# --------------------------------------------------------------------------------------------
# Validation
# --------------------------------------------------------------------------------------------
run -e PUID=abc -v "$cfg:/config" "$IMAGE"
expect "PUID=abc is rejected" 1 "PUID must be a numeric id" "!STUB"
run -e PGID=-5 -v "$cfg:/config" "$IMAGE"
expect "PGID=-5 is rejected" 1 "PGID must be a numeric id" "!STUB"
run -e PUID=99999999999 -v "$cfg:/config" "$IMAGE"
expect "PUID out of range is rejected" 1 "PUID is out of range" "!STUB"
run -e UMASK=abc -v "$cfg:/config" "$IMAGE"
expect "UMASK=abc is rejected" 1 "UMASK must be 1-4 octal digits" "!STUB"
run -e UMASK=0800 -v "$cfg:/config" "$IMAGE"
expect "UMASK=0800 is rejected" 1 "UMASK must be 1-4 octal digits" "!STUB"
run -e UMASK=0277 -v "$cfg:/config" "$IMAGE" version
expect "UMASK removing owner bits warns" 0 "removes permissions from the file owner" "umask=0277"
run -e TZ=Mars/Olympus_Mons -v "$cfg:/config" "$IMAGE" version
expect "unknown TZ warns" 0 "Unknown time zone TZ='Mars/Olympus_Mons'" "STUB uid=1000"
run -e TZ=../../etc/passwd -v "$cfg:/config" "$IMAGE" version
expect "TZ with .. warns" 0 "is not a valid time zone name" "STUB uid=1000"

# --------------------------------------------------------------------------------------------
# Arguments
# --------------------------------------------------------------------------------------------
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" --extra-flag
expect "flags are appended to the server command" 0 "STUB uid=99 " "args=--data /config --nobrowser --extra-flag"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" reset-auth
expect "subcommand runs as PUID with --data" 0 "STUB uid=99 gid=100 " "args=reset-auth --data /config"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" /app/dupearr healthcheck --data /config
expect "explicit binary runs as PUID" 0 "STUB uid=99 gid=100 " "args=healthcheck --data /config"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" sh -c 'echo "root=$(id -u)"'
expect "other commands run as-is (debugging)" 0 "root=0" "!STUB" "!Starting Dupearr"

# --------------------------------------------------------------------------------------------
# /config ownership: only Dupearr's own files, never /data, never through symlinks
# --------------------------------------------------------------------------------------------
cfg=$(volume)
data=$(volume)
as_root "$cfg" "$data" 'set -e
mkdir -p /config/logs /config/Backups/manual /config/.restore /config/sub /data/media \
	/config/.restore-tmp-x1 /config/.restore.old-x2 /config/.restorex
touch /config/config.xml /config/dupearr.db /config/dupearr.db-wal /config/logs/dupearr.txt \
	/config/Backups/manual/b.zip /config/.restore/config.xml /config/keep-me.txt /config/sub/other.txt \
	/config/config.xml.bak-20260101T000000Z /config/dupearr.db.bak-20260101T000000Z-wal \
	/config/.config.xml.123.tmp /config/.restore-tmp-x1/dupearr.db /config/.restore.old-x2/config.xml \
	/config/.restore-upload-x3.zip /config/.restorex/notes.txt /data/media/movie.mkv
chown 1234:1234 /config/keep-me.txt /config/sub /config/sub/other.txt /config/.restorex /config/.restorex/notes.txt
chown -R 1234:1234 /data/media
: >/config/Backups/manual/group-only.zip
chown 99:1234 /config/Backups/manual/group-only.zip
ln -s /data/media/movie.mkv /config/logs/filelink
ln -s /data/media /config/logs/dirlink
: >/config/root-secret
chmod 0600 /config/root-secret
ln /config/root-secret /config/logs/hardlink'
expect "ownership fixture" 0
run -e PUID=99 -e PGID=100 -v "$cfg:/config" -v "$data:/data" "$IMAGE"
expect "server start with /data read-only for PUID" 0 "STUB uid=99 gid=100 " "/data does not look writable for 99:100" \
	"Not changing ownership of hard-linked files (more than one name) under /config/logs: /config/logs/hardlink"
as_root "$cfg" "$data" 'stat -c "%u:%g %n" /config /config/config.xml /config/dupearr.db /config/dupearr.db-wal \
	/config/logs /config/logs/dupearr.txt /config/logs/filelink /config/logs/dirlink /config/Backups \
	/config/Backups/manual/b.zip /config/Backups/manual/group-only.zip /config/.restore/config.xml /config/config.xml.bak-20260101T000000Z \
	/config/dupearr.db.bak-20260101T000000Z-wal /config/.config.xml.123.tmp /config/.restore-tmp-x1/dupearr.db \
	/config/.restore.old-x2/config.xml /config/.restore-upload-x3.zip /config/stub-created /config/keep-me.txt \
	/config/sub /config/sub/other.txt /config/.restorex/notes.txt /config/root-secret /data /data/media /data/media/movie.mkv'
expect_equal "only Dupearr's files are chowned; /data, link targets and hard-linked files untouched" "99:100 /config
99:100 /config/config.xml
99:100 /config/dupearr.db
99:100 /config/dupearr.db-wal
99:100 /config/logs
99:100 /config/logs/dupearr.txt
99:100 /config/logs/filelink
99:100 /config/logs/dirlink
99:100 /config/Backups
99:100 /config/Backups/manual/b.zip
99:100 /config/Backups/manual/group-only.zip
99:100 /config/.restore/config.xml
99:100 /config/config.xml.bak-20260101T000000Z
99:100 /config/dupearr.db.bak-20260101T000000Z-wal
99:100 /config/.config.xml.123.tmp
99:100 /config/.restore-tmp-x1/dupearr.db
99:100 /config/.restore.old-x2/config.xml
99:100 /config/.restore-upload-x3.zip
99:100 /config/stub-created
1234:1234 /config/keep-me.txt
1234:1234 /config/sub
1234:1234 /config/sub/other.txt
1234:1234 /config/.restorex/notes.txt
0:0 /config/root-secret
0:0 /data
1234:1234 /data/media
1234:1234 /data/media/movie.mkv"

cfg=$(volume)
data=$(volume)
as_root "$cfg" "$data" 'set -e
mkdir -p /data/media
touch /data/media/movie.mkv
chown -R 1234:1234 /data/media
ln -s /data/media /config/Backups
ln -s /data/media/movie.mkv /config/config.xml
ln -s /data/media/movie.mkv /config/.dupearr.lock'
expect "symlink fixture" 0
run -e PUID=99 -e PGID=100 -v "$cfg:/config" -v "$data:/data" "$IMAGE"
expect "top-level symlinks are not followed" 0 \
	"Not changing ownership through symlink /config/Backups" \
	"Not changing ownership through symlink /config/config.xml" \
	"/config/.dupearr.lock is a symlink, which is not used" "STUB uid=99 gid=100 "
as_root "$cfg" "$data" 'stat -c "%u:%g %n" /data/media /data/media/movie.mkv'
expect_equal "symlink targets keep their owner" "1234:1234 /data/media
1234:1234 /data/media/movie.mkv"

# --------------------------------------------------------------------------------------------
# The lock file vs. anything else that can write /config as PUID (another container on the same
# appdata folder): /config/.dupearr.lock is swapped between a regular file and symlinks (to a
# root-owned file and to a missing file in root-owned /data) while the entrypoint starts over and
# over as root. Root must never create a file or change an owner through such a link.
# --------------------------------------------------------------------------------------------
cfg=$(volume)
data=$(volume)
as_root "$cfg" "$data" 'set -e; : >/data/victim; chmod 0600 /data/victim; chown 0:0 /data /data/victim; chmod 0755 /data'
expect "lock race fixture" 0
run -v "$cfg:/config" -v "$data:/data" "$IMAGE" sh -c '
PUID=99 PGID=100 /entrypoint.sh version >/dev/null 2>&1
su-exec 99:100 sh -c "cd /config && while :; do : >.r; mv -f .r .dupearr.lock; ln -sfn /data/created-by-root .l; mv -f .l .dupearr.lock; : >.r; mv -f .r .dupearr.lock; ln -sfn /data/victim .l; mv -f .l .dupearr.lock; done" &
i=0
while [ "$i" -lt 150 ]; do
	PUID=99 PGID=100 /entrypoint.sh >/dev/null 2>&1 || true
	i=$((i + 1))
done
kill "$!"
echo "runs=$i"
stat -c "victim %u:%g %a" /data/victim
ls /data'
expect "a lock file swapped for symlinks: root creates and chowns nothing through them" 0 \
	"runs=150" "victim 0:0 600" "!created-by-root"

# A lock file left by an earlier start (as root, or with other ids) is handed to PUID:PGID; one with
# a second name (hard link) is not, since that would hand over whatever else the name belongs to.
cfg=$(volume)
as_root "$cfg" "$data" 'set -e; : >/config/.dupearr.lock; chown 0:0 /config/.dupearr.lock'
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE"
expect "server start with a root-owned lock file" 0 "STUB uid=99 gid=100 " "!WARNING"
as_root "$cfg" "$data" 'stat -c "%u:%g %n" /config/.dupearr.lock; rm /config/.dupearr.lock; : >/config/root-file; chown 0:0 /config/root-file; ln /config/root-file /config/.dupearr.lock'
expect_equal "a legacy lock file is given to PUID:PGID" "99:100 /config/.dupearr.lock"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE"
expect "server start with a hard-linked lock file" 0 "STUB uid=99 gid=100 "
as_root "$cfg" "$data" 'stat -c "%u:%g %n" /config/root-file'
expect_equal "a hard-linked lock file keeps its owner" "0:0 /config/root-file"

# The recursive chown vs. the same neighbour writing as PUID under another group (so its entries
# have the "wrong" owner): a directory below /config/logs is swapped for a symlink into root-owned
# /data while the entrypoint starts over and over. find checks the path, chown resolves it again
# later; root must never follow such a swap (it only acts on entries of other users).
cfg=$(volume)
data=$(volume)
as_root "$cfg" "$data" 'set -e; mkdir -p /data/victimdir /config/logs; : >/data/victimdir/x; chmod 0600 /data/victimdir/x
chown 0:0 /data /data/victimdir /data/victimdir/x; chmod 0755 /data /data/victimdir; chown -R 99:100 /config; chmod 0700 /config/logs'
expect "chown race fixture" 0
run -v "$cfg:/config" -v "$data:/data" "$IMAGE" sh -c '
su-exec 99:1234 sh -c "cd /config/logs && mkdir -p d.real && : >d.real/x && while :; do chgrp 1234 d.real d.real/x 2>/dev/null; mv d.real d 2>/dev/null; mv d d.real 2>/dev/null; ln -s /data/victimdir d 2>/dev/null; rm -f d; done" &
i=0
while [ "$i" -lt 150 ]; do
	PUID=99 PGID=100 /entrypoint.sh version >/dev/null 2>&1 || true
	i=$((i + 1))
	[ "$(stat -c %u:%g /data/victimdir/x)" = 0:0 ] || break
done
kill "$!"
echo "runs=$i"
stat -c "victim %u:%g %a" /data/victimdir/x'
expect "a directory in /config swapped for a symlink: root changes no owner through it" 0 \
	"runs=150" "victim 0:0 600"

# The /config write check is a real probe file (permission bits lie on Docker Desktop bind mounts):
# it must refuse a read-only /config and leave nothing behind in a writable one.
cfg=$(volume)
run -e PUID=99 -e PGID=100 -v "$cfg:/config:ro" "$IMAGE"
expect "read-only /config is refused" 1 "/config is not writable for 99:100" "!STUB"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" version
expect "writable /config passes the write probe" 0 "STUB uid=99 gid=100 "
config_entries "$cfg" "$data"
expect_equal "the write probe leaves no file in /config" ""

# --------------------------------------------------------------------------------------------
# Non-root start (docker run --user, Kubernetes, TrueNAS 568:568)
# --------------------------------------------------------------------------------------------
cfg=$(volume)
run --user 568:568 -v "$cfg:/config" "$IMAGE"
expect "--user with an unwritable /config fails clearly" 1 "/config is not writable for 568:568" "!STUB"
as_root "$cfg" "$data" 'chown 568:568 /config'
run --user 568:568 -e PUID=99 -v "$cfg:/config" "$IMAGE"
expect "--user with a writable /config runs as that user" 0 \
	"Running as non-root 568:568" "STUB uid=568 gid=568 " "args=--data /config --nobrowser"

# --------------------------------------------------------------------------------------------
# Real server: HEALTHCHECK, embedded web UI, in-place restart under tini, graceful SIGTERM
# (requests run inside the container, so no host port is needed and remote daemons work too)
# --------------------------------------------------------------------------------------------
# get CID PATH [WGET_ARGS...]: GET (or, with --post-data, POST) http://127.0.0.1:3873PATH inside the
# container with the instance's API key; prints the body.
get() {
	_c=$1
	_p=$2
	shift 2
	docker exec "$_c" wget -qO- -T 5 --header "X-Api-Key: ${KEY:-}" "$@" "http://127.0.0.1:3873$_p"
}
# start_time CID: the server's startTime from /api/v1/system/status ("" when it does not answer).
start_time() {
	get "$1" /api/v1/system/status 2>/dev/null | sed -n 's/.*"startTime":"\([^"]*\)".*/\1/p'
}

cfg=$(volume)
cid=$(docker run -d --label "$LABEL" -e PUID=99 -e PGID=100 -e DUPEARR__AUTH__METHOD=None -v "$cfg:/config" "$IMAGE")
# --start-interval=5s makes this a few seconds; Docker Engine < 25 probes every 30s.
i=0
while [ "$(docker inspect -f '{{.State.Health.Status}}' "$cid")" != healthy ] && [ "$i" -lt 60 ]; do
	i=$((i + 1))
	sleep 1
done
OUT="health=$(docker inspect -f '{{.State.Health.Status}}' "$cid") after ${i}s"
RC=0
expect "real server: the container becomes healthy" 0 "health=healthy"

if OUT=$(get "$cid" / 2>&1); then RC=0; else RC=$?; fi
expect "real server: the web UI is served" 0 "<title>Dupearr</title>" 'window.Dupearr={urlBase:""}' "/assets/index-"
asset=$(printf '%s' "$OUT" | sed -n 's/.*src="\.\(\/assets\/index-[^"]*\.js\)".*/\1/p' | head -n 1)
if OUT=$(get "$cid" "${asset:-/assets/missing.js}" 2>&1 | head -c 200); then RC=0; else RC=$?; fi
expect "real server: the UI's script is embedded ($asset)" 0 "!404"

KEY=$(docker exec "$cid" sed -n 's:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p' /config/config.xml || true)
before=$(start_time "$cid")
pid_before=$(docker exec "$cid" sh -c 'for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = dupearr ] && echo "${p#/proc/}"; done; true' 2>/dev/null || true)
if OUT=$(get "$cid" /api/v1/system/restart --post-data '' 2>&1); then RC=0; else RC=$?; fi
expect "real server: POST /system/restart is accepted" 0
i=0
after=""
while [ "$i" -lt 40 ]; do
	after=$(start_time "$cid")
	if [ -n "$after" ] && [ "$after" != "$before" ]; then
		break
	fi
	i=$((i + 1))
	sleep 0.5
done
OUT=$(docker exec "$cid" sh -c 'for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = dupearr ] && echo "pid=${p#/proc/} owner=$(stat -c %u:%g "$p") ppid=$(cut -d" " -f4 "$p/stat")"; done; true' 2>&1 || true)
OUT="$OUT running=$(docker inspect -f '{{.State.Running}} restarts={{.RestartCount}}' "$cid") startTime: $before -> $after"
RC=0
if [ -n "$before" ] && [ -n "$after" ] && [ "$after" != "$before" ]; then
	expect "real server: restarts in place (same PID under tini, still PUID:PGID)" 0 \
		"pid=$pid_before owner=99:100 ppid=1" "running=true restarts=0"
else
	fail "real server: restarts in place: the server did not come back" "$OUT"
fi

start=$(date +%s)
docker stop -t 20 "$cid" >/dev/null
elapsed=$(($(date +%s) - start))
OUT="exit=$(docker inspect -f '{{.State.ExitCode}}' "$cid") elapsed=${elapsed}s"
RC=0
if [ "$elapsed" -lt 10 ]; then
	expect "real server: SIGTERM stops it cleanly (exit 0) within 10s" 0 "exit=0"
else
	fail "real server: SIGTERM stops it cleanly within 10s: docker stop waited for the timeout" "$OUT"
fi
as_root "$cfg" "$data" 'ls -A /config | sort | tr "\n" " "; stat -c "%u:%g" /config/config.xml /config/dupearr.db /config/logs/dupearr.txt | sort -u'
expect "real server: config.xml, database and log belong to PUID:PGID, WAL checkpointed" 0 \
	".dupearr.lock " "config.xml " "dupearr.db " "logs " "99:100" "!dupearr.db-wal" "!0:0"

# --------------------------------------------------------------------------------------------
# Hardened run options, as in deploy/docker-compose.yml and unraid/dupearr.xml: no capabilities
# beyond what the entrypoint needs (chown /config, su-exec, tini's signal forwarding) and no
# privilege gain. The real server must still start, fix /config ownership (also inside a 0700
# directory of PUID's), become healthy (the HEALTHCHECK runs as root) and stop on SIGTERM.
# --------------------------------------------------------------------------------------------
HARDENED="--security-opt=no-new-privileges:true --cap-drop=ALL --cap-add=CHOWN --cap-add=DAC_OVERRIDE --cap-add=KILL --cap-add=SETGID --cap-add=SETUID"
cfg=$(volume)
as_root "$cfg" "$data" 'set -e; mkdir -p /config/Backups/scheduled; : >/config/Backups/scheduled/old.zip; chown -R 99:100 /config/Backups; chown 0:0 /config/Backups/scheduled/old.zip; chmod 0700 /config/Backups /config/Backups/scheduled; chmod 0600 /config/Backups/scheduled/old.zip'
# shellcheck disable=SC2086 # word splitting of $HARDENED is intended
cid=$(docker run -d --label "$LABEL" $HARDENED -e PUID=99 -e PGID=100 -e DUPEARR__AUTH__METHOD=None -v "$cfg:/config" "$IMAGE")
i=0
while [ "$(docker inspect -f '{{.State.Health.Status}}' "$cid")" != healthy ] && [ "$i" -lt 60 ]; do
	i=$((i + 1))
	sleep 1
done
OUT="health=$(docker inspect -f '{{.State.Health.Status}}' "$cid") after ${i}s"
RC=0
expect "hardened: the real server becomes healthy" 0 "health=healthy"
if OUT=$(docker exec "$cid" sh -c 'grep -E "^(CapBnd|NoNewPrivs)" /proc/1/status; for p in /proc/[0-9]*; do [ "$(cat "$p/comm" 2>/dev/null)" = dupearr ] && grep -E "^(Uid|CapEff|CapPrm)" "$p/status"; done; true' 2>&1); then RC=0; else RC=$?; fi
expect "hardened: bounding set, no-new-privileges, app without capabilities" 0 \
	"CapBnd:	00000000000000e3" "NoNewPrivs:	1" "Uid:	99	99	99	99" "CapEff:	0000000000000000" "CapPrm:	0000000000000000"
if OUT=$(get "$cid" / 2>&1); then RC=0; else RC=$?; fi
expect "hardened: the web UI is served" 0 "<title>Dupearr</title>"
start=$(date +%s)
docker stop -t 20 "$cid" >/dev/null
elapsed=$(($(date +%s) - start))
OUT="exit=$(docker inspect -f '{{.State.ExitCode}}' "$cid") elapsed=${elapsed}s"
RC=0
if [ "$elapsed" -lt 10 ]; then
	expect "hardened: SIGTERM stops it cleanly (exit 0) within 10s" 0 "exit=0"
else
	fail "hardened: SIGTERM stops it cleanly within 10s: docker stop waited for the timeout (tini needs KILL)" "$OUT"
fi
as_root "$cfg" "$data" 'stat -c "%u:%g" /config/config.xml /config/dupearr.db /config/Backups/scheduled/old.zip /config/.dupearr.lock | sort -u'
expect_equal "hardened: /config files belong to PUID:PGID" "99:100"

# --------------------------------------------------------------------------------------------
# Running container: exec wrapper, process owner, SIGTERM delivery
# --------------------------------------------------------------------------------------------
cfg=$(volume)
cid=$(docker create --label "$LABEL" -e PUID=99 -e PGID=100 -e STUB_STAY=1 -v "$cfg:/config" "$IMAGE")
docker cp "$STUB" "$cid:/app/dupearr" >/dev/null
docker start "$cid" >/dev/null
i=0
until docker exec "$cid" test -f /config/stub-created 2>/dev/null; do
	i=$((i + 1))
	if [ "$i" -ge 20 ]; then
		break
	fi
	sleep 1
done

# /proc/<pid> belongs to the process's effective uid:gid.
if OUT=$(docker exec "$cid" sh -c 'for p in /proc/[0-9]*; do if [ "$(cat "$p/comm" 2>/dev/null)" = sleep ]; then stat -c "%u:%g" "$p"; fi; done' 2>&1); then RC=0; else RC=$?; fi
expect_equal "server process runs as PUID:PGID" "99:100"

if OUT=$(docker exec "$cid" dupearr version 2>&1); then RC=0; else RC=$?; fi
expect "exec wrapper: version as PUID:PGID" 0 "STUB uid=99 gid=100 " "args=--data /config version"
if OUT=$(docker exec "$cid" dupearr reset-auth --data /config 2>&1); then RC=0; else RC=$?; fi
expect "exec wrapper: reset-auth as PUID:PGID" 0 "STUB uid=99 gid=100 " "args=--data /config reset-auth --data /config"
if OUT=$(docker exec "$cid" dupearr --data /config -- healthcheck 2>&1); then RC=0; else RC=$?; fi
expect "exec wrapper: flags before the subcommand" 0 "STUB uid=99 " "args=--data /config --data /config -- healthcheck"
if OUT=$(docker exec -u 568:568 "$cid" dupearr version 2>&1); then RC=0; else RC=$?; fi
expect "exec wrapper: non-root caller runs directly" 0 "STUB uid=568 gid=568 "
for args in "" "--nobrowser" "--data /config" "--data=/config --nobrowser"; do
	# shellcheck disable=SC2086 # word splitting of $args is intended
	if OUT=$(docker exec "$cid" dupearr $args 2>&1); then RC=0; else RC=$?; fi
	expect "exec wrapper refuses a second server (args: '$args')" 2 "already runs in this container" "!STUB"
done

# A second container on the same /config (copied Unraid template, compose + docker run) must not
# start a second server on the database; one-shot commands such as reset-auth still work.
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE"
expect "second container on the same /config refuses to start a server" 1 \
	"Another Dupearr server is already running on /config" "!STUB"
run --user 99:100 -v "$cfg:/config" "$IMAGE" --nobrowser
expect "... also when started as non-root" 1 "Another Dupearr server is already running on /config" "!STUB"
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE" reset-auth
expect "subcommands run while the server holds the lock" 0 "STUB uid=99 gid=100 " "args=reset-auth --data /config"

start=$(date +%s)
docker stop -t 20 "$cid" >/dev/null
elapsed=$(($(date +%s) - start))
OUT="exit=$(docker inspect -f '{{.State.ExitCode}}' "$cid") elapsed=${elapsed}s"
RC=0
if [ "$elapsed" -lt 10 ]; then
	expect "SIGTERM reaches the app through tini, sh and su-exec" 0 "exit=143"
else
	fail "SIGTERM reaches the app through tini, sh and su-exec: docker stop waited for the timeout" "$OUT"
fi

# The lock goes away with the server process.
run -e PUID=99 -e PGID=100 -v "$cfg:/config" "$IMAGE"
expect "a server starts again once the first one stopped" 0 "STUB uid=99 gid=100 " "args=--data /config --nobrowser"
as_root "$cfg" "$data" 'stat -c "%u:%g %n" /config/.dupearr.lock'
expect_equal "the lock file belongs to PUID:PGID" "99:100 /config/.dupearr.lock"

echo
echo "$PASSED passed, $FAILED failed"
[ "$FAILED" = 0 ]
