# Installing Dupearr on Unraid

Dupearr runs as a Docker container managed by Unraid's Docker tab, exactly like Radarr or Sonarr.
This guide is for users; maintainers building and publishing the image should read
[`unraid/README.md`](../../unraid/README.md).

- [Before you start](#before-you-start)
- [1. Install from Community Applications](#1-install-from-community-applications)
- [2. Add the template](#2-add-the-template)
- [3. Fill in the container settings](#3-fill-in-the-container-settings)
- [4. First run](#4-first-run)
- [Updating](#updating)
- [Uninstalling](#uninstalling)

## Before you start

- Check the [requirements](requirements.md) (CPU, kernel, Docker engine, Unraid versions, memory).
- Unraid 6.12 or newer with Docker enabled.
- Plex Media Server (any install: container, VM or another machine). Radarr/Sonarr are optional
  but strongly recommended: deleting through them keeps them in sync and prevents re-downloads.
- Know where your media lives. The [TRaSH Guides Unraid layout](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Unraid/)
  is the reference: one share `data` with `media/`, `torrents/` and `usenet/` inside, mounted as
  `/data` in Radarr/Sonarr and `/data/media` in Plex.

## 1. Install from Community Applications

Dupearr's image is public on the GitHub Container Registry (`ghcr.io/sl0wz3r/dupearr:latest`, built for x86-64 and
arm64), so Unraid needs no registry setup and its update check works.

The simplest way is the **Apps** tab (Community Applications): search for **Dupearr**, click
**Install**, then continue with [3. Fill in the container settings](#3-fill-in-the-container-settings).
To install the template by hand instead (for example to test a template change), use
[2. Add the template](#2-add-the-template). (Alternative without any registry: load an image
archive with `docker load`, see *Install without a registry* in `unraid/README.md`.)

## 2. Add the template

```sh
mkdir -p /boot/config/plugins/dockerMan/templates-user
wget -O /boot/config/plugins/dockerMan/templates-user/my-dupearr.xml \
  https://raw.githubusercontent.com/sl0wz3r/dupearr/main/unraid/dupearr.xml
```

(No terminal? Copy [`unraid/dupearr.xml`](../../unraid/dupearr.xml) to the `flash` share as
`config/plugins/dockerMan/templates-user/my-dupearr.xml`.)

Now go to **Docker → Add Container** and choose **dupearr** under **[ User templates ]** in the
*Template* dropdown.

Prefer Unraid to fill in your default appdata location (like a Community Applications install)?
See [Method B in unraid/README.md](../../unraid/README.md#method-b-behave-like-a-community-applications-install).

## 3. Fill in the container settings

| Field | Default | What to put |
|---|---|---|
| **WebUI Port** | `3873` | Any free host port. |
| **Appdata** (`/config`) | `/mnt/user/appdata/dupearr` | Where Dupearr keeps `config.xml`, its database, logs and backups. Keep it on your cache/pool (ideally an exclusive `appdata` share). **Never** a network share: SQLite needs a local disk. |
| **Media** (`/data`) | `/mnt/user/data` | The same host folder your Plex/Radarr/Sonarr containers map (see below). Only required for the filesystem deletion method, Dupearr's recycle bin and hardlink detection. |
| **PUID / PGID** (advanced) | `99` / `100` | `nobody:users`, the owner of Unraid shares. Keep them unless you changed your media ownership. |
| **UMASK** (advanced) | `022` | Keep. Use `002` if other apps must be able to modify files Dupearr creates in shared folders (its own config, database, backups and logs are always private). |
| **Trusted proxies** (advanced) | *(empty)* | Only behind a reverse proxy (SWAG, Nginx Proxy Manager): the proxy container's IP on your custom Docker network (give it a fixed IP; not the network's gateway). Required for *External* authentication. |
| **Allowed hosts** (advanced) | *(empty)* | Optional: the host name(s) you reach Dupearr by through the proxy, e.g. `dupearr.example.com`. |
| **Extra Parameters** (Advanced View) | `--security-opt=no-new-privileges:true --cap-drop=ALL --cap-add=…` | Keep them: the container runs with the least privileges it needs. A container created from an older template keeps its old Extra Parameters; copy them from [unraid/README.md](../../unraid/README.md). |

Unraid passes your server time zone (`TZ`) to every container automatically.

Unraid kills containers that take longer than **Settings → Docker → Docker Stop Timeout** (10
seconds by default) to stop, for example when you stop the array. Consider raising it to about 30
seconds, so that a removal in progress and the database can finish cleanly.

### Choosing the `/data` mapping

Dupearr compares file paths reported by Plex and the \*arrs, and (optionally) touches files
itself. It is easiest when **every app sees the same path**:

| Your setup | Plex container | Radarr/Sonarr | Dupearr `/data` | Path mappings needed |
|---|---|---|---|---|
| TRaSH (recommended) | `/mnt/user/data/media` → `/data/media` | `/mnt/user/data` → `/data` | `/mnt/user/data` → `/data` | Plex `/data/media` → `/data/media` (same path on both sides; only for filesystem removals, the recycle bin and hardlink detection) |
| linuxserver defaults | `/mnt/user/Movies` → `/movies`, `/mnt/user/TV` → `/tv` | `/mnt/user/Movies` → `/movies` | `/mnt/user` → `/data` | Plex `/movies` → `/data/Movies`, `/tv` → `/data/TV` (same for the \*arrs) |
| Deleting only through Radarr/Sonarr/Plex | any | any | leave empty | none; remove `filesystem` from *Deletion Methods* |

Mappings are added in Dupearr under *Settings → Media Management → Path Mappings*; details and
more examples in [configuration → path mappings](configuration.md#path-mappings).

If the host folder in the *Media* field does not exist, Unraid creates it (and with it a new user
share), so double-check the spelling, or clear the field if you do not need it. Also note that
Unraid does not auto-start a container whose mapped folder is missing at boot (for example an
Unassigned Devices disk that is not mounted yet).

### Permissions

Dupearr runs as `PUID:PGID` (99:100) and can only delete what that user may delete. Shares
created by Unraid are `nobody:users`, so this normally just works. If Radarr/Sonarr or a
downloader created files with other owners, fix them the TRaSH way:

```sh
chown -R nobody:users /mnt/user/data
chmod -R a=,a+rX,u+w,g+w /mnt/user/data
```

(or use *Tools → Docker Safe New Perms*). Dupearr itself never changes ownership of your media; it
only fixes the ownership of its own files in `/config` on start.

Your appdata folder holds the Plex owner token, the \*arr API keys and every notification secret.
At start Dupearr makes it `rwxr-x---` (no group write, nothing for others) and its secret files
owner-only, even after *New Permissions* loosened them; do not export `appdata` over SMB/NFS and do
not mount Dupearr's appdata folder into other containers (most Unraid containers run as the same
uid 99).

Click **Apply**. Unraid pulls the image and starts the container.

## 4. First run

Open the WebUI (container icon → **WebUI**, or `http://<server-ip>:3873/`) and follow the
[first-run steps in the README](../../README.md#first-run). Creating the login asks for the
**setup code** from Dupearr's log: container icon → **Logs**, line *First-run setup is pending …*
(or `docker logs dupearr` in the terminal). Then: create a login, add Plex, enable
libraries, add Radarr/Sonarr, add path mappings if needed, review the profile, scan, review and
approve. **Dry run is on by default**, so the first approvals are simulations; check
*Activity → History* before you turn it off.

Plex running on the same server? Use the server's LAN address (for example
`http://192.168.0.10:32400`) rather than a `plex.direct` URL or the container name, unless both
containers share a custom Docker network.

Reaching Dupearr from outside your home? Use a VPN (WireGuard in Unraid) or a reverse proxy with
HTTPS, and never forward port 3873 on your router. See the
[deployment hardening guide](../SECURITY.md#deployment-hardening-guide).

## Updating

When a new release is out, the **Docker** tab shows *update ready* for Dupearr (the template
follows the `latest` tag): click it, or **Check for Updates** first. An image pinned by digest
never shows updates; change its tag and digest to update.
Your settings and database are kept in `/config`; a backup is taken on schedule and you can make
one manually under *System → Backup* before updating.

## Uninstalling

Stop and remove the container on the Docker tab. Remove `/mnt/user/appdata/dupearr` if you do not
want to keep its database and backups, and `/boot/config/plugins/dockerMan/templates-user/my-dupearr.xml`
to drop the template. If you configured a Dupearr recycle bin, empty or delete that folder too.

See also: [troubleshooting](troubleshooting.md), [safety](safety.md).
