# Dupearr as a systemd service

[`dupearr.service`](dupearr.service) follows the Servarr (Sonarr/Radarr) wiki conventions:

| Convention | Value |
|---|---|
| Binary | `/opt/Dupearr/dupearr` |
| Data directory (`config.xml`, database, logs, backups) | `/var/lib/dupearr` (`--data=/var/lib/dupearr`) |
| Service user / group | `dupearr` / `media` (the group your Plex and \*arr users share) |
| UMask | `0002` (files stay writable for the `media` group; `config.xml`, the database and backups are always `0600`) |
| Sandbox | read-only system, no capabilities, private `/tmp` and `/dev`, system call filter (see [Sandboxing](#sandboxing)) |
| Web UI | `http://<host>:3873/` |

## Install

Release archives are named `dupearr_<version>_<os>_<arch>.tar.gz` and contain the binary (web UI
embedded), this unit file, `LICENSE`, `README.md` and `CHANGELOG.md`.

```sh
# 1. Service user in the shared media group (create the group if you do not have one yet)
sudo groupadd -f media
sudo useradd --system --no-create-home --shell /usr/sbin/nologin --gid media dupearr

# 2. Binary
VERSION=0.1.0
ARCH=amd64          # amd64, arm64 or armv7
curl -fLO "https://github.com/sl0wz3r/dupearr/releases/download/v${VERSION}/dupearr_${VERSION}_linux_${ARCH}.tar.gz"
tar -xzf "dupearr_${VERSION}_linux_${ARCH}.tar.gz"
sudo mkdir -p /opt/Dupearr
sudo install -m 0755 "dupearr_${VERSION}_linux_${ARCH}/dupearr" /opt/Dupearr/dupearr

# 3. Data directory
sudo install -d -m 0750 -o dupearr -g media /var/lib/dupearr

# 4. Service
sudo install -m 0644 "dupearr_${VERSION}_linux_${ARCH}/dupearr.service" /etc/systemd/system/dupearr.service
sudo systemctl -q daemon-reload && sudo systemctl enable --now -q dupearr
systemctl status dupearr
```

Then open `http://<host>:3873/` and follow the first-run steps in the main [README](../../README.md#first-run).

## Media permissions

Dupearr deletes files only through the method you allow (Radarr/Sonarr API, Plex API or the
filesystem). The **filesystem** method, the Dupearr recycle bin and hardlink detection need the
`dupearr` user to read and write your media folders. With the shared-group setup from the Servarr
wiki / TRaSH Guides:

```sh
sudo chgrp -R media /srv/data
sudo chmod -R g+rwX /srv/data
sudo find /srv/data -type d -exec chmod g+s {} +   # new files inherit the media group
```

(`/srv/data` stands for your media root.) Plex and Radarr/Sonarr usually run as other users; path
mappings translate their paths if they see the files under different prefixes (for example a
Windows Plex server). The filesystem method and the recycle bin only act inside folders covered by
a Plex path mapping, so add one even when Plex sees the same paths (`/srv/data/media` →
`/srv/data/media`). See [configuration](../../docs/user/configuration.md#path-mappings).

The unit's sandbox also makes every folder outside `/var/lib/dupearr` read-only for Dupearr, so
the filesystem method, the recycle bin and restores from it need your media folders listed in a
drop-in:

```sh
sudo systemctl edit dupearr
```

```ini
[Service]
# One line per media root (the folders your Plex path mappings point to).
ReadWritePaths=/srv/data/media
# Only for media under /home: read-only is enough to read it, "no" to delete there.
#ProtectHome=read-only
```

Then `sudo systemctl restart dupearr`. Without the drop-in those deletions fail with "read-only
file system" and nothing is changed; Radarr/Sonarr and Plex deletions do not need it.

## Sandboxing

`dupearr.service` enables systemd's sandboxing by default: the file system is read-only except
`/var/lib/dupearr` (`StateDirectory=`, created for the service user if missing) and the folders
you add with `ReadWritePaths=`, `/home` is hidden, `/tmp` and `/dev` are private, the service has
no capabilities and cannot gain any, may only open IPv4/IPv6/Unix sockets, and runs under a
system call filter. `systemd-analyze security dupearr` rates it about 1.4 ("OK"; 9.0 without the
sandbox). It keeps a compromised process away from the rest of the machine, including Plex's
and the \*arr apps' own configuration and secrets.

If you move the data directory (`--data=`), add it to `ReadWritePaths=` in the drop-in. To find
what the sandbox blocks, look for `EPERM`/"operation not permitted" or "read-only file system"
in `journalctl -u dupearr`.

## Update

```sh
sudo systemctl stop dupearr
sudo install -m 0755 dupearr_<new>_linux_<arch>/dupearr /opt/Dupearr/dupearr
sudo systemctl start dupearr
```

A backup is taken on schedule (System → Backup); take a manual one before upgrading.

## Useful commands

```sh
journalctl -u dupearr -f                                   # live log (also /var/lib/dupearr/logs/dupearr.txt)
sudo -u dupearr /opt/Dupearr/dupearr version
sudo -u dupearr /opt/Dupearr/dupearr reset-auth --data=/var/lib/dupearr && sudo systemctl restart dupearr
```

Run maintenance commands as the `dupearr` user (as above), never as root: files written by root
in `/var/lib/dupearr` would no longer be writable by the service.

## Environment overrides

Add `Environment=` lines (or `sudo systemctl edit dupearr`) to override `config.xml` values without
editing the file, for example:

```ini
[Service]
Environment=DUPEARR__SERVER__PORT=3873
Environment=DUPEARR__LOG__LEVEL=debug
```

The full list is in [docs/user/configuration.md](../../docs/user/configuration.md#environment-variables).
