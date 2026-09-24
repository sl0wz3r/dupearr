#!/bin/sh
# Dupearr container entrypoint (runs under tini as PID 1).
#
# Started as root (Unraid, docker compose, docker run: the normal case):
#   1. validate PUID / PGID / UMASK / TZ and apply the umask
#   2. create or reuse a group with PGID and a user with PUID (named "dupearr" when created)
#   3. give /config (the directory itself, NOT recursively) and the files Dupearr owns inside it
#      to PUID:PGID. Nothing else is ever chowned: never /data, never media, never other files
#      you keep in /config. Symlinks are not followed and other filesystems are not crossed.
#   4. server start only: lock /config/.dupearr.lock, so a second container on the same /config
#      refuses to start (lock_config)
#   5. drop privileges with su-exec and exec Dupearr (it replaces this shell, so tini signals it
#      directly and SIGTERM reaches the app)
#
# Started as a non-root user (docker run --user, Kubernetes runAsUser, TrueNAS apps 568:568):
#   steps 2-3 are skipped (they need root) and Dupearr runs as the current user. PUID/PGID are
#   ignored; make /config writable for that user yourself. The server still takes the lock (4).
#
# Arguments:
#   (none)                   /app/dupearr --data /config --nobrowser
#   -flags ...               /app/dupearr --data /config --nobrowser -flags ...
#   version|healthcheck|reset-auth|help [flags]
#                            /app/dupearr <subcommand> --data /config [flags]   (as PUID:PGID)
#   /app/dupearr|dupearr ... that exact Dupearr command line                    (as PUID:PGID)
#   anything else            executed as-is without dropping privileges (debugging, e.g. "sh")
set -eu

APP=/app/dupearr
CONFIG_DIR=/config
MEDIA_DIR=/data
USER_NAME=dupearr
LOCK_FILE=$CONFIG_DIR/.dupearr.lock

log() { printf '[entrypoint] %s\n' "$*"; }
warn() { printf '[entrypoint] WARNING: %s\n' "$*" >&2; }
die() {
	printf '[entrypoint] ERROR: %s\n' "$*" >&2
	exit 1
}

# normalize_id NAME VALUE: prints VALUE as a plain decimal id (leading zeros dropped) or exits
# with a clear message when it is not a non-negative integer.
normalize_id() {
	case $2 in
	'' | *[!0-9]*) die "$1 must be a numeric id (0-2147483647), got '$2'" ;;
	esac
	[ "${#2}" -le 10 ] || die "$1 is out of range: '$2'"
	_id=$(printf '%s' "$2" | sed 's/^0*//')
	[ -n "$_id" ] || _id=0
	[ "$_id" -le 2147483647 ] || die "$1 is out of range: '$2'"
	printf '%s' "$_id"
}

# check_umask: 1-4 octal digits (e.g. 002, 022, 0027).
check_umask() {
	case $UMASK in
	[0-7] | [0-7][0-7] | [0-7][0-7][0-7] | [0-7][0-7][0-7][0-7]) ;;
	*) die "UMASK must be 1-4 octal digits such as 002 or 022, got '$UMASK'" ;;
	esac
	# The owner digit is the third one from the right; anything but 0 removes permissions from
	# Dupearr's own files (it could no longer read its database or logs). String handling only:
	# printf %d would read "022" as octal.
	_mask=000$UMASK
	_mask=${_mask#"${_mask%???}"}
	case $_mask in
	0??) ;;
	*) warn "UMASK=$UMASK removes permissions from the file owner; Dupearr may be unable to read its own files. Use 002 or 022." ;;
	esac
}

# check_tz: an unknown zone makes Go silently fall back to UTC; say so.
check_tz() {
	if [ -z "${TZ:-}" ]; then
		unset TZ
		return 0
	fi
	_zone=${TZ#:}
	case $_zone in
	/*) _file=$_zone ;;
	*) _file=/usr/share/zoneinfo/$_zone ;;
	esac
	case $_zone in
	*..*) warn "TZ='$TZ' is not a valid time zone name; times will be shown in UTC." ;;
	*) [ -f "$_file" ] || warn "Unknown time zone TZ='$TZ' (expected e.g. Europe/Amsterdam or America/New_York); times will be shown in UTC." ;;
	esac
}

# setup_identity: make sure PGID has a group entry and PUID a user entry, so logs, `id` and `ps`
# show names and the process gets a sane HOME. Dupearr itself only needs the numeric ids
# (su-exec accepts them without passwd entries), so every failure here is non-fatal.
setup_identity() {
	# A "dupearr" user/group left over from an earlier start with other ids is removed first
	# (the container filesystem survives restarts, PUID/PGID may have changed in between).
	_entry=$(getent passwd "$USER_NAME" || true)
	if [ -n "$_entry" ]; then
		_uid=$(printf '%s' "$_entry" | cut -d: -f3)
		_gid=$(printf '%s' "$_entry" | cut -d: -f4)
		if [ "$_uid" != "$PUID" ] || [ "$_gid" != "$PGID" ]; then
			deluser "$USER_NAME" >/dev/null 2>&1 || return 1
		fi
	fi
	_entry=$(getent group "$USER_NAME" || true)
	if [ -n "$_entry" ] && [ "$(printf '%s' "$_entry" | cut -d: -f3)" != "$PGID" ]; then
		delgroup "$USER_NAME" >/dev/null 2>&1 || return 1
	fi

	# Group: reuse whatever already has PGID (e.g. 100 = "users" in alpine), else create one.
	GROUP_NAME=$(getent group "$PGID" | cut -d: -f1 || true)
	if [ -z "$GROUP_NAME" ]; then
		addgroup -g "$PGID" "$USER_NAME" >/dev/null 2>&1 || return 1
		GROUP_NAME=$USER_NAME
	fi

	# User: reuse whatever already has PUID (e.g. 0 = root), else create one in that group.
	RUN_USER=$(getent passwd "$PUID" | cut -d: -f1 || true)
	if [ -z "$RUN_USER" ]; then
		adduser -D -H -h "$CONFIG_DIR" -s /sbin/nologin -G "$GROUP_NAME" -u "$PUID" "$USER_NAME" \
			>/dev/null 2>&1 || return 1
		RUN_USER=$USER_NAME
	fi
	return 0
}

# chown_tree PATH: give PATH (and, for a directory, everything below it on the same filesystem)
# to PUID:PGID. Only entries with the wrong owner are touched, so restarts stay fast. Symlinks are
# changed themselves (-h), never followed, so a link cannot redirect the chown to media. Files with
# more than one name (hard links) are left alone: Dupearr never hard-links its own files, and a
# link placed in /config could otherwise hand over a root-owned file from elsewhere on that disk.
#
# Root only acts on entries that belong to another user. find checks an entry and chown resolves
# its path again later, so whatever can write below /config (another container on the same appdata
# folder, as PUID under another group) could swap a directory on that path for a symlink in
# between; it can only create entries owned by PUID, though, and those root never selects. An
# entry of PUID with another group only needs its group changed, which PUID may do itself: that
# pass runs without privileges (su-exec), so a swapped path cannot lend it root's rights.
chown_tree() {
	_linked=$(find "$1" -xdev ! -type d -links +1 ! -user "$PUID" -print 2>/dev/null | head -n 5)
	if [ -n "$_linked" ]; then
		warn "Not changing ownership of hard-linked files (more than one name) under $1: $(printf '%s' "$_linked" | tr '\n' ' ')"
	fi
	_rc=0
	find "$1" -xdev \( -type d -o -links 1 \) ! -user "$PUID" -exec chown -h "$PUID:$PGID" {} + || _rc=1
	su-exec "$PUID:$PGID" find "$1" -xdev -user "$PUID" ! -group "$PGID" -exec chgrp -h "$PGID" {} + || _rc=1
	return "$_rc"
}

# fix_config_ownership: /config itself (non-recursive) plus the files Dupearr creates there.
fix_config_ownership() {
	_ok=0
	if [ "$(stat -c '%u:%g' "$CONFIG_DIR")" != "$PUID:$PGID" ]; then
		chown "$PUID:$PGID" "$CONFIG_DIR" || _ok=1
	fi
	# config.xml (+ its atomic-write temp), dupearr.db (+ -wal/-shm/-journal), logs/, Backups/,
	# .restore/ (staged restore) and the restore's temporaries (.restore-tmp-*, .restore-upload-*,
	# .restore-cfgcheck-*, .restore.old-*: swept at start-up, which fails for another owner), and
	# the <name>.bak-<time> copies a restore keeps of the replaced config.xml and database.
	for _path in "$CONFIG_DIR"/config.xml "$CONFIG_DIR"/.config.xml.*.tmp "$CONFIG_DIR"/config.xml.bak-* \
		"$CONFIG_DIR"/*.db "$CONFIG_DIR"/*.db-* "$CONFIG_DIR"/*.db.bak-* \
		"$CONFIG_DIR"/logs "$CONFIG_DIR"/Backups "$CONFIG_DIR"/.restore "$CONFIG_DIR"/.restore[-.]*; do
		if [ -L "$_path" ]; then
			warn "Not changing ownership through symlink $_path; make sure its target is writable for $PUID:$PGID."
			continue
		fi
		# Unmatched globs stay literal and do not exist.
		[ -e "$_path" ] || continue
		chown_tree "$_path" || _ok=1
	done
	return "$_ok"
}

# PROBE_SCRIPT (run by sh with the directory as $1): creates and removes a probe file, i.e. whether
# the current user can really create files there. Permission bits alone mislead: Docker Desktop
# (macOS/Windows) bind mounts report root:root 0755 and ignore chown, yet accept writes from any
# uid; ACLs and root-squashed NFS can also disagree with `test -w` either way.
# shellcheck disable=SC2016 # $1 is expanded by the inner shell on purpose
PROBE_SCRIPT='test -d "$1" && _f=$(mktemp -p "$1" .dupearr-write-test.XXXXXX 2>/dev/null) && rm -f "$_f"'

# can_create DIR: whether the current user can create files in DIR (write probe).
can_create() {
	sh -c "$PROBE_SCRIPT" sh "$1"
}

# can_write USER_SPEC DIR: whether USER_SPEC (uid:gid) can create files in DIR (write probe).
# Only used for /config, which belongs to Dupearr.
can_write() {
	su-exec "$1" sh -c "$PROBE_SCRIPT" sh "$2"
}

# may_write USER_SPEC DIR: whether DIR looks writable for USER_SPEC by its permission bits. For
# /data, which is never written here (not even a probe file); only used for an informational note.
may_write() {
	# shellcheck disable=SC2016 # $1 is expanded by the inner shell on purpose
	su-exec "$1" sh -c 'test -d "$1" && test -w "$1" && test -x "$1"' sh "$2"
}

# as_app_user CMD...: runs CMD as the user Dupearr runs as (PUID:PGID when started as root).
as_app_user() {
	if [ "$(id -u)" = 0 ]; then
		su-exec "$PUID:$PGID" "$@"
	else
		"$@"
	fi
}

# lock_config: takes an exclusive lock on /config/.dupearr.lock for the server's whole lifetime and
# refuses to start when another server holds it (a second container on the same appdata folder,
# e.g. a copied Unraid template): two servers on one database would each run the scheduler and
# process the removal queue, bypassing the per-run limits. The lock lives on descriptor 9, which
# su-exec and Dupearr inherit (also across its in-place restarts); it is released when the server
# exits, whatever the reason. Filesystems without lock support only get a warning.
#
# /config belongs to PUID, so anything that can write it as PUID (another container on the same
# appdata folder) can swap the lock file for a symlink at any moment, also between a check and the
# next command. Root therefore never creates, writes or chowns anything through that path: the file
# is created as PUID:PGID, opened read-only (flock needs no write access, and a read-only open
# creates nothing), used only when the descriptor is the regular file that is at the path, and a
# legacy file is chowned through that descriptor (/proc/self/fd/9), never by name.
lock_config() {
	if [ ! -e "$LOCK_FILE" ] && [ ! -L "$LOCK_FILE" ]; then
		# shellcheck disable=SC2016 # $1 is expanded by the inner shell on purpose
		as_app_user sh -c ': >>"$1"' sh "$LOCK_FILE" 2>/dev/null || true
	fi
	if [ -L "$LOCK_FILE" ]; then
		warn "$LOCK_FILE is a symlink, which is not used; starting without protection against a second server on $CONFIG_DIR."
		return 0
	fi
	# `command`: a failed open returns instead of ending the shell. The braces keep 2>/dev/null
	# temporary (on exec itself it would silence the entrypoint's and Dupearr's stderr for good).
	if ! { command exec 9<"$LOCK_FILE"; } 2>/dev/null; then
		warn "Could not open $LOCK_FILE; starting without protection against a second server on $CONFIG_DIR."
		return 0
	fi
	# What was opened (the magic link follows the descriptor, not the name) must be the regular
	# file that is at the path now (lstat): same device and inode.
	_opened=$(stat -L -c '%d:%i %h %u:%g %F' /proc/self/fd/9 2>/dev/null || true)
	_named=$(stat -c '%d:%i %F' "$LOCK_FILE" 2>/dev/null || true)
	case "$_opened" in
	*' regular'*) ;;
	*) _opened=invalid ;;
	esac
	case "$_named" in
	*' regular'*) ;;
	*) _named=unknown ;;
	esac
	if [ "${_opened%% *}" != "${_named%% *}" ]; then
		exec 9<&-
		warn "$LOCK_FILE was replaced while it was opened, which is not used; starting without protection against a second server on $CONFIG_DIR."
		return 0
	fi
	if _err=$(flock -n 9 2>&1); then
		# A lock file left by an earlier start as root or with other ids. Only a file with a single
		# name: a hard link would hand over whatever else that name belongs to.
		_rest=${_opened#* }
		_links=${_rest%% *}
		_rest=${_rest#* }
		_owner=${_rest%% *}
		if [ "$(id -u)" = 0 ] && [ "$_links" = 1 ] && [ "$_owner" != "$PUID:$PGID" ]; then
			chown "$PUID:$PGID" /proc/self/fd/9 2>/dev/null || true
		fi
		return 0
	fi
	if [ -z "$_err" ]; then
		die "Another Dupearr server is already running on $CONFIG_DIR (another container using the same appdata folder?). Stop it first: two servers on one database would both process the removal queue."
	fi
	warn "Could not lock $LOCK_FILE ($_err); starting without protection against a second server on $CONFIG_DIR."
}

# ----------------------------------------------------------------------------------------------
# Build the command line.
# ----------------------------------------------------------------------------------------------
# SERVER=1 when this starts the long-running server (the start-up notes only matter then).
DROP_PRIVILEGES=1
SERVER=0
case "${1:-}" in
'')
	set -- "$APP" --data "$CONFIG_DIR" --nobrowser
	SERVER=1
	;;
-*)
	set -- "$APP" --data "$CONFIG_DIR" --nobrowser "$@"
	SERVER=1
	;;
version | healthcheck | reset-auth | help)
	_sub=$1
	shift
	set -- "$APP" "$_sub" --data "$CONFIG_DIR" "$@"
	;;
"$APP" | dupearr)
	shift
	case "${1:-}" in
	version | healthcheck | reset-auth | help) ;;
	*) SERVER=1 ;;
	esac
	set -- "$APP" "$@"
	;;
*)
	DROP_PRIVILEGES=0
	;;
esac

if [ "$DROP_PRIVILEGES" = 0 ]; then
	exec "$@"
fi

UMASK=${UMASK:-002}
check_umask
umask "$UMASK"
check_tz

# ----------------------------------------------------------------------------------------------
# Non-root start: nothing to set up, and nothing we could change anyway.
# ----------------------------------------------------------------------------------------------
if [ "$(id -u)" != 0 ]; then
	log "Running as non-root $(id -u):$(id -g) (umask $UMASK); PUID/PGID are ignored."
	if ! can_create "$CONFIG_DIR"; then
		die "$CONFIG_DIR is not writable for $(id -u):$(id -g). Fix the ownership of the host folder mapped to $CONFIG_DIR."
	fi
	if [ "$SERVER" = 1 ]; then
		lock_config
	fi
	exec "$@"
fi

# ----------------------------------------------------------------------------------------------
# Root start: identity, /config ownership, privilege drop.
# ----------------------------------------------------------------------------------------------
PUID=$(normalize_id PUID "${PUID:-1000}")
PGID=$(normalize_id PGID "${PGID:-1000}")
if [ "$PUID" = 0 ]; then
	warn "PUID=0: Dupearr will run as root. This works but is not recommended; use the id that owns your media (Unraid: 99, most Linux hosts: 1000)."
fi

GROUP_NAME=$PGID
RUN_USER=$PUID
if ! setup_identity; then
	# e.g. a read-only root filesystem (/etc/passwd cannot be changed); su-exec takes numeric ids.
	warn "Could not create a user/group entry for $PUID:$PGID; continuing with numeric ids."
fi
# setup_identity may have stopped half-way with an empty name: show the numeric id instead.
[ -n "$RUN_USER" ] || RUN_USER=$PUID
[ -n "$GROUP_NAME" ] || GROUP_NAME=$PGID

mkdir -p "$CONFIG_DIR"
if ! fix_config_ownership; then
	warn "Could not change the ownership of everything in $CONFIG_DIR to $PUID:$PGID (read-only or network filesystem?)."
fi
if ! can_write "$PUID:$PGID" "$CONFIG_DIR"; then
	die "$CONFIG_DIR is not writable for $PUID:$PGID. Check the host folder mapped to $CONFIG_DIR (it must be a local disk, not NFS/SMB) and your PUID/PGID."
fi

if [ "$SERVER" = 1 ]; then
	# Also gives a lock file left by an earlier start to PUID:PGID (through its descriptor).
	lock_config
	# Informational only: /data is optional and never modified here.
	if [ ! -d "$MEDIA_DIR" ]; then
		log "$MEDIA_DIR is not mounted: the filesystem deletion method and hardlink detection are unavailable (the Radarr/Sonarr and Plex methods still work)."
	elif ! may_write "$PUID:$PGID" "$MEDIA_DIR"; then
		log "$MEDIA_DIR does not look writable for $PUID:$PGID (permission bits): the filesystem deletion method and a recycle bin under $MEDIA_DIR may fail (the Radarr/Sonarr and Plex methods still work)."
	fi
	log "Starting Dupearr as $RUN_USER:$GROUP_NAME ($PUID:$PGID), umask $UMASK, TZ=${TZ:-UTC}"
fi
exec su-exec "$PUID:$PGID" "$@"
