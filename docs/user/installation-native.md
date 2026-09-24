# Installing Dupearr natively

Dupearr is a single static binary with the web UI built in: no runtime, no dependencies. Release
archives exist for Linux (amd64, arm64, armv7), macOS (Intel, Apple Silicon) and Windows (amd64):
`dupearr_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) plus `checksums.txt`, on the project's
releases page.

- [Common to all platforms](#common-to-all-platforms)
- [Linux (systemd)](#linux-systemd)
- [macOS](#macos)
- [Windows](#windows)
- [Build from source](#build-from-source)

## Common to all platforms

```
dupearr [--data DIR] [--nobrowser]      run the server (web UI on http://localhost:3873/)
dupearr version                         print version information
dupearr healthcheck [--data DIR]        exit 0 when the local server answers /ping
dupearr reset-auth [--data DIR]         forgot the password: reset to a fresh login setup
```

The data directory holds `config.xml`, `dupearr.db`, `logs/` and `Backups/`. Without `--data`
it is:

| OS | Default data directory |
|---|---|
| Linux | `~/.config/Dupearr` (for the user running it) |
| macOS | `~/Library/Application Support/Dupearr` |
| Windows | `%AppData%\Dupearr` |

Use a local disk for it (SQLite). Run maintenance commands such as `reset-auth` as the same user
as the server, with the same `--data`, so file ownership stays consistent.

Verify a download: `sha256sum -c checksums.txt --ignore-missing` (macOS:
`shasum -a 256 -c checksums.txt --ignore-missing`).

## Linux (systemd)

The Linux archives contain [`dupearr.service`](../../deploy/systemd/dupearr.service), a unit
following the Servarr conventions (binary in `/opt/Dupearr`, data in `/var/lib/dupearr`, user
`dupearr` in group `media`, `UMask=0002`). Step-by-step install, permissions and updates:
[`deploy/systemd/README.md`](../../deploy/systemd/README.md).

Short version:

```sh
sudo groupadd -f media && sudo useradd --system --no-create-home --shell /usr/sbin/nologin --gid media dupearr
sudo install -D -m 0755 dupearr /opt/Dupearr/dupearr
sudo install -d -m 0750 -o dupearr -g media /var/lib/dupearr
sudo install -m 0644 dupearr.service /etc/systemd/system/dupearr.service
sudo systemctl daemon-reload && sudo systemctl enable --now dupearr
```

The `dupearr` user needs write access to your media for the filesystem deletion method (put the
media in the `media` group, group-writable), and read (list) access to every folder on the way to
it: Dupearr opens folders one by one so that a symlink swapped in cannot redirect a removal.

The unit is **sandboxed** (`ProtectSystem=strict`, `ProtectHome=true`, no capabilities, a system
call filter, `StateDirectory=dupearr`): Dupearr can only write to `/var/lib/dupearr`. For the
filesystem deletion method, the recycle bin and restore, allow your media folders with a drop-in
(`sudo systemctl edit dupearr`):

```ini
[Service]
ReadWritePaths=/srv/media
# media under /home also needs:
# ProtectHome=read-only
```

Without it removals fail with "read-only file system" and nothing is changed. If you move `--data`,
add it to `ReadWritePaths` too. Details: [`deploy/systemd/README.md`](../../deploy/systemd/README.md#sandboxing).

## macOS

```sh
tar -xzf dupearr_<version>_darwin_arm64.tar.gz      # darwin_amd64 on Intel Macs
sudo install -m 0755 dupearr_<version>_darwin_arm64/dupearr /usr/local/bin/dupearr
xattr -d com.apple.quarantine /usr/local/bin/dupearr 2>/dev/null || true   # unsigned binary
dupearr
```

To start it at login, save this as `~/Library/LaunchAgents/io.dupearr.plist` and run
`launchctl load ~/Library/LaunchAgents/io.dupearr.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.dupearr</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/dupearr</string>
    <string>--nobrowser</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict>
</plist>
```

Media on a NAS share must be mounted before Dupearr needs it (only for the filesystem method);
macOS may also ask to allow access to network volumes or removable disks.

## Windows

1. Unzip `dupearr_<version>_windows_amd64.zip`, for example to `C:\Program Files\Dupearr\`.
2. Run `dupearr.exe` and open <http://localhost:3873/>. Allow it through the Windows firewall if
   other machines should reach it.

To start it automatically, create a *Task Scheduler* task "At log on" (or "At startup" with
"Run whether user is logged on or not") that runs:

```
"C:\Program Files\Dupearr\dupearr.exe" --nobrowser --data "C:\ProgramData\Dupearr"
```

Dupearr does not register itself as a Windows service; if you prefer a service, wrap it with a
service manager such as NSSM or WinSW using the same command line. Services and "run whether
logged on or not" tasks **cannot see mapped drive letters** (`Z:\`); use UNC paths
(`\\nas\media\…`) in path mappings, and run the task/service as a user that can access the share.

Plex on the same Windows machine reports paths like `D:\Media\Movies\…`, which Dupearr can open
directly; for the filesystem method and the recycle bin add a Plex mapping from the folder to
itself (`D:\Media` → `D:\Media`). If Plex runs elsewhere, map its paths (for example Plex
`/data/media` → `\\nas\data\media`).

On Windows the final move into and out of the recycle bin is done by path (Windows has no
`renameat`); Dupearr still opens and checks every folder first, but a local user who can replace a
media folder with a junction at exactly that moment could redirect it. Only give other accounts
write access to your media folders if you trust them, or leave `filesystem` out of the deletion
methods on shared Windows machines.

## Build from source

Requires Go 1.27+, Node.js 24 (20.19+ or 22.13+ also work) and make:

```sh
git clone https://github.com/sl0wz3r/dupearr.git && cd dupearr
make            # web UI + ./bin/dupearr for this machine
make release    # all release archives into dist/ (after `make web`)
```

Other targets (FreeBSD, Windows arm64, …) build with
`CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -trimpath -tags timetzdata ./cmd/dupearr`
after `make web`; the SQLite driver is pure Go.
