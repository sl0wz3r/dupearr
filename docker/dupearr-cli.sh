#!/bin/sh
# /usr/local/bin/dupearr inside the image: runs the Dupearr binary for maintenance commands, e.g.
#
#   docker exec dupearr dupearr version
#   docker exec -it dupearr dupearr reset-auth   (the running server applies it and restarts)
#
# `docker exec` starts processes as root. Files Dupearr writes as root (config.xml, the database
# and its -wal/-shm files) would then be unwritable for the server, which runs as PUID:PGID. So
# when invoked as root this wrapper drops to PUID:PGID first, exactly like the entrypoint does.
# The data directory is /config unless the command line gives another --data.
#
# It never starts a server. The container already runs one, and a second process on the same
# /config would open the same database, mark the running server's commands as failed and start
# its own scheduler (queue processing included) before it even notices that the port is taken.
set -eu

APP=/app/dupearr
CONFIG_DIR=/config

usage() {
	cat >&2 <<'EOF'
usage: dupearr <command> [--data DIR]

  version       print build information
  healthcheck   exit 0 when the running server answers /ping
  reset-auth    reset authentication to a fresh login setup (the running server
                applies it at once and restarts by itself)
  help          full help

The Dupearr server already runs in this container: restart the container
(docker restart <name>) instead of starting a second server.
EOF
}

# subcommand prints the first positional argument, like the binary's own flag parsing: --data and
# -data take the next argument as their value (unless written --data=DIR), "--" ends the flags,
# -h/-help/--help mean help. Prints nothing when there is no subcommand (= start a server).
subcommand() {
	_skip=0
	_end=0
	for _arg in "$@"; do
		if [ "$_end" = 1 ]; then
			printf '%s' "$_arg"
			return 0
		fi
		if [ "$_skip" = 1 ]; then
			_skip=0
			continue
		fi
		case $_arg in
		--) _end=1 ;;
		--data | -data) _skip=1 ;;
		-h | -help | --help)
			printf 'help'
			return 0
			;;
		-*) ;;
		*)
			printf '%s' "$_arg"
			return 0
			;;
		esac
	done
	return 0
}

if [ -z "$(subcommand "$@")" ]; then
	usage
	exit 2
fi

# --data first: a --data on the command line comes later and wins. Unknown commands are rejected
# by the binary before it touches anything.
if [ "$(id -u)" = 0 ]; then
	exec su-exec "${PUID:-1000}:${PGID:-1000}" "$APP" --data "$CONFIG_DIR" "$@"
fi
exec "$APP" --data "$CONFIG_DIR" "$@"
