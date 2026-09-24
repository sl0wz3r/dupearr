# Dupearr on Unraid

This folder holds everything Unraid needs:

| File | Purpose |
|---|---|
| [`dupearr.xml`](dupearr.xml) | The Docker template (dockerMan / Community Applications format, `version="2"`): the **one** public template, the file Community Applications (CA) lists. **Generated** by `make ca-template` from [`ca/dupearr.xml.tmpl`](ca/dupearr.xml.tmpl) and [`ca/publish.env`](ca/publish.env); edit those, never this file. |
| [`icon.png`](icon.png) | 512×512 RGBA icon referenced by the template's `<Icon>` |
| [`icon.svg`](icon.svg) | Vector version of the same icon |
| [`ca/`](ca/README.md) | Template sources, the one settings file with the public owner/repository/registry, and the render, validate and preflight scripts |

`dupearr.xml` pulls the public image `ghcr.io/sl0wz3r/dupearr:latest` from the GitHub Container Registry
([section 1](#1-the-image)).

- **From Community Applications** (once listed): **Apps** tab → search **Dupearr** → **Install**,
  review the settings ([section 3](#3-settings-to-review)), **Apply**, then [first start](#4-first-start).
- **By hand** (before the listing, or to test a template): [section 2](#2-install-the-template).

For the full walk-through (first run, Plex, Radarr/Sonarr, path mappings) see
[docs/user/installation-unraid.md](../docs/user/installation-unraid.md).

---

## 1. The image

The template pulls `ghcr.io/sl0wz3r/dupearr:latest` from the GitHub Container Registry. The image is built for
`linux/amd64` (every Unraid server) and `linux/arm64`, and anyone can pull it: no login and no
Docker setting is needed, and Unraid's update check follows the `latest` tag.

Every release also has its own tags (`X.Y.Z`, `X.Y`), and its release notes list the image
digest (`sha256:…`). To run exactly one reviewed image, put it in **Repository** with its digest:
`ghcr.io/sl0wz3r/dupearr:X.Y.Z@sha256:<digest>`. Docker then checks every byte it pulls against that
digest. Unraid shows no updates for a pinned image; to update, change the tag and the digest. The
build provenance of a release can be checked with
`gh attestation verify oci://ghcr.io/sl0wz3r/dupearr:X.Y.Z --owner sl0wz3r`.

## 2. Install the template


### Method A: user template (recommended while testing)

Copy the template onto the flash drive as `my-dupearr.xml` (the `my-` prefix and lower-case name
match what dockerMan writes itself, so your first **Apply** updates the same file):

```sh
mkdir -p /boot/config/plugins/dockerMan/templates-user
wget -O /boot/config/plugins/dockerMan/templates-user/my-dupearr.xml \
  https://raw.githubusercontent.com/sl0wz3r/dupearr/main/unraid/dupearr.xml
```

The Unraid server also downloads the `<Icon>` from the repository, so while the repository it
names is private or unreachable the Docker tab shows a question-mark icon (cosmetic).

Then in the web UI:

1. **Docker → Add Container**.
2. **Template:** pick **dupearr** under **[ User templates ]**.
3. Review the settings (see [section 3](#3-settings-to-review)) and click **Apply**.

With a user template the `/config` host path is exactly what the template says:
`/mnt/user/appdata/dupearr`.

### Method B: behave like a Community Applications install

- **Default template:** put the file in any sub-folder of
  `/boot/config/plugins/dockerMan/templates/` (not `limetech/`, which Unraid wipes):

  ```sh
  mkdir -p /boot/config/plugins/dockerMan/templates/dupearr
  cp dupearr.xml /boot/config/plugins/dockerMan/templates/dupearr/dupearr.xml
  ```

  It then shows under **[ Default templates ]** in the Template dropdown. For default templates
  Unraid **auto-fills** the `/config` host path from **Settings → Docker → Default appdata storage
  location** plus the container name, e.g. `/mnt/cache/appdata/dupearr` instead of the template's
  `/mnt/user/appdata/dupearr`. That is the same thing a real CA install does. The name is
  lower-case (`dupearr`) on purpose so the folder matches the template default.
- **CA "Private Apps":** Community Applications also lists any template found under
  `/boot/config/plugins/community.applications/private/<folder>/*.xml`:

  ```sh
  mkdir -p /boot/config/plugins/community.applications/private/dupearr
  cp dupearr.xml /boot/config/plugins/community.applications/private/dupearr/dupearr.xml
  ```

  Refresh the **Apps** tab and look under **Private Apps**. Installing from there also applies
  the `/config` auto-fill.

## 3. Settings to review

| Setting | Default | Notes |
|---|---|---|
| WebUI Port | `3873` | Container port 3873. Change only the host side if 3873 is taken. |
| Appdata (`/config`) | `/mnt/user/appdata/dupearr` | `config.xml`, the SQLite database, logs and backups. Keep it on a pool/cache (an exclusive share is ideal); never on NFS/SMB. |
| Media (`/data`) | `/mnt/user/data` | Use the **same host path your Plex and Radarr/Sonarr containers use** so that every app sees the same file paths (TRaSH layout). Needed only for the *filesystem* deletion method, Dupearr's recycle bin and hardlink detection, which also need a Plex path mapping in Dupearr (with identical paths, map the folder to itself: `/data/media` → `/data/media`); \*arr and Plex deletions work without it. Unraid creates a missing folder (and with it a new share), so change or clear it if you do not have `/mnt/user/data`. |
| PUID / PGID | `99` / `100` | `nobody:users`, the owner of Unraid shares. The user must be allowed to delete your media. |
| UMASK | `022` | Permissions for files Dupearr creates. `002` keeps them group-writable. Dupearr's own config, database, backups and logs stay private (`0600`) either way. |
| Trusted proxies (advanced) | *(empty)* | `DUPEARR__AUTH__TRUSTEDPROXIES`. Only behind a reverse proxy (SWAG, Nginx Proxy Manager): the proxy container's IP on your custom Docker network (give it a fixed IP; not the gateway). Dupearr then believes the client address the proxy reports; required for *External* authentication. |
| Allowed hosts (advanced) | *(empty)* | `DUPEARR__AUTH__ALLOWEDHOSTS`. Optional: the host name(s) Dupearr is reached by through the proxy. |
| Extra Parameters (Advanced View) | `--security-opt=no-new-privileges:true --cap-drop=ALL --cap-add=CHOWN --cap-add=DAC_OVERRIDE --cap-add=KILL --cap-add=SETGID --cap-add=SETUID` | Least privilege. The entrypoint starts as root only to give Dupearr's files in `/config` to PUID:PGID (CHOWN, DAC_OVERRIDE), switch to that user (SETUID, SETGID) and pass signals on to the app (KILL). Dupearr itself runs without capabilities, and nothing in the container can gain privileges later. Keep these flags. A container installed from an older template keeps its saved Extra Parameters, so paste them in yourself. |

Also worth checking once: **Settings → Docker → Docker Stop Timeout** (default 10 seconds, for all
containers). Unraid kills a container that has not stopped by then, for example when you stop the
array. Around 30 seconds gives Dupearr time to finish a removal in progress and close its database
cleanly.

Unraid adds `TZ` (your server's time zone) automatically; there is intentionally no TZ setting in
the template, because an empty `TZ` variable would override it.

If your media is **not** under one `/data` tree (for example Plex uses `/movies` and `/tv`),
leave the `/data` mapping pointed at the parent share and add **Path Mappings** in Dupearr
(Settings → Media Management). See [configuration](../docs/user/configuration.md#path-mappings).

## 4. First start

1. Click the container icon → **WebUI** (or browse to `http://<server>:3873/`).
2. Create your login, add Plex, enable libraries, add Radarr/Sonarr, check the decision profile,
   run a scan and review the results. **Dry run is on by default**: nothing is deleted until you
   turn it off in Settings → Media Management *and* approve a group.
3. Logs: container icon → **Logs**, or `/mnt/user/appdata/dupearr/logs/dupearr.txt`.

Maintenance commands run through the bundled `dupearr` wrapper, which drops to PUID:PGID so the
files it writes in `/config` keep the right owner:

```sh
docker exec -it dupearr dupearr version
docker exec -it dupearr dupearr reset-auth && docker restart dupearr   # forgot your password
```

One appdata folder serves one Dupearr server. A second container started on the same `/config`
host path (for example a copy of the template) stops at once with *"Another Dupearr server is
already running on /config"* instead of processing the same removal queue a second time; give
each container its own appdata folder.

## 5. Building and pushing the image

The image is built from the repository root (`Dockerfile`) for `linux/amd64` and `linux/arm64`.

**From CI:** pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`. It runs the tests,
builds and tests the image, pushes `ghcr.io/sl0wz3r/dupearr:X.Y.Z`, `:vX.Y.Z`, `:X.Y` and `:latest`
with the workflow's own token (a pre-release tag such as `v0.2.0-rc.1` pushes only its own tags),
attests the image and the binaries, and publishes a GitHub release whose notes list the image
digest. Nothing else publishes anything: branch pushes and pull requests only run
`.github/workflows/ci.yml`, which never pushes.

**From a workstation** (maintainers): `docker login ghcr.io -u <github-user>` with a personal
access token that has only the `write:packages` scope, then `make docker-push` (a release checkout
pushes `:X.Y.Z` and `:latest`; a dev build needs `DOCKER_LATEST=true` to move `:latest`). The
push prints the image digest.

`make docker` builds a local image for your own machine only (`dupearr:<version>`), handy for
testing without a registry.

## Install without a registry (docker load)

Use this method when the Unraid server cannot reach the registry, or to try a build that was
never pushed.
You build the image yourself and copy it to the server as a file, so nobody on the network can
swap it on the way. Server prerequisites: [requirements](../docs/user/requirements.md).

### 1. Build the image archive

You need a checkout of the repository and Docker with buildx. The build machine can be x86-64 or
arm64, such as Apple Silicon. On arm64 only the last build stage runs under emulation; the Go
binary and the web UI are compiled natively.

```sh
make docker-save          # linux/amd64, for any x86-64 Unraid server
make docker-test-amd64    # optional: the container self-test on that image (emulated on arm64)
```

`make docker-save` produces:

- the image **`dupearr:<version>-amd64`** in your local Docker. `docker load` recreates this tag
  on the server;
- **`dist/dupearr_<version>_linux-amd64.tar.gz`**: the `docker save` output, gzipped, about
  12 MB;
- **`dist/dupearr_<version>_linux-amd64.tar.gz.sha256`**: its checksum.

`<version>` comes from `git describe`: `0.2.0` on a release tag, `0.1.0-dev` on an untagged
checkout. The examples below use `0.2.0`. You can override `IMAGE=` (the image name), `VERSION=`,
`DOCKER_SAVE_PLATFORM=linux/arm64` (for an arm64 server) and `DOCKER_SAVE_FILE=`. `make release`
and `make clean` empty `dist/`, so copy the archive out first. The target only ever creates the
`-amd64` tag. It never touches `dupearr:<version>` or `dupearr:latest`, so it does not replace
the image your own machine runs.

### 2. Load it on the server

1. Copy both files to the server. Use `scp` (SSH must be enabled: *Settings → Management
   Access*) or any share:

   ```sh
   scp dist/dupearr_0.2.0_linux-amd64.tar.gz dist/dupearr_0.2.0_linux-amd64.tar.gz.sha256 root@tower:/tmp/
   ```

   `/tmp` lives in RAM and is emptied at every reboot, which is fine for a file you load right
   away. Do not put it on the flash drive (`/boot`).
2. In the server's terminal, check the file, load it and look at the result:

   ```sh
   cd /tmp
   sha256sum -c dupearr_0.2.0_linux-amd64.tar.gz.sha256      # dupearr_0.2.0_linux-amd64.tar.gz: OK
   docker load -i dupearr_0.2.0_linux-amd64.tar.gz           # Loaded image: dupearr:0.2.0-amd64
   docker image inspect --format '{{.Os}}/{{.Architecture}} {{.RepoTags}}' dupearr:0.2.0-amd64
   # linux/amd64 [dupearr:0.2.0-amd64]
   rm dupearr_0.2.0_linux-amd64.tar.gz dupearr_0.2.0_linux-amd64.tar.gz.sha256
   ```

   The checksum only shows that the file arrived intact. It is only as trustworthy as the way the
   `.sha256` file reached you; here both come from your own build. `docker load` reads the
   gzipped archive directly and needs Docker Engine 20.10 or newer, which every Unraid 6.12 and
   7.x release has.
3. Install the template as in [section 2](#2-install-the-template), but before **Apply** set
   **Repository** to the loaded tag, exactly: `dupearr:0.2.0-amd64`. Review the other settings as
   in [section 3](#3-settings-to-review). For a container you already have: Docker tab → container
   icon → **Edit** → **Repository** → **Apply**.

### How Unraid treats a loaded image

The following comes from reading dockerMan's source ([unraid/webgui](https://github.com/unraid/webgui):
`CreateDocker.php`, `DockerClient.php`). The code is the same in Unraid 6.12.15, 7.0, 7.2, 7.3 and
`master` (checked 2026-09-23). It has not been tried on a live server.

| Action | What dockerMan does | With a loaded image |
|---|---|---|
| **Apply** (Add Container, Edit) | Pulls only when no local image matches **Repository**, then replaces the container. | Nothing is pulled; the container is created from the loaded image. |
| Array start, reboot, autostart | Starts the existing container (`docker start` never pulls). | Works, also offline. The image stays in the Docker vDisk/directory across reboots. |
| Update check (Docker tab) | Compares the registry digest recorded for a pulled image with the registry. | Shows **not available**. A loaded image has no registry digest; this is expected. |
| **force update** / **apply update** | Always pulls first. When the pull fails, it skips the container. | The pull fails and the container keeps running unchanged. A name without a registry host means Docker Hub (`docker.io/library/dupearr`), which only holds Docker's official images. So this never updates anything; use the steps below. |

Pitfalls, from the same code:

- **Type the complete `name:tag`.** The "is it already local?" check is a substring match on the
  *first* tag of each local image. `dupearr` or `dupearr:0.2.0` would also "match"
  `dupearr:0.2.0-amd64`. dockerMan then skips the pull and removes the old container, and
  `docker run` asks for an image that does not exist: Docker tries Docker Hub, fails, and no
  container is left. Apply again with the correct Repository to recreate it. The template and
  `/config` are kept, so nothing is lost.
- **Keep exactly one tag on the loaded image.** Do not also tag it `dupearr:latest`: only each
  image's first tag is compared, so dockerMan may look at the other tag, try to pull, fail and
  stop. The old container stays in that case.

### Updating and rolling back

1. Build and copy the new archive, then `docker load` it. It loads as a new tag, for example
   `dupearr:0.3.0-amd64`.
2. Take a backup (*System → Backup* in Dupearr), then **Edit** the container, set
   **Repository** to the new tag and **Apply**. `/config` is kept.
3. Remove the old image once you no longer need it: `docker rmi dupearr:0.2.0-amd64`. dockerMan
   only removes the previous image itself on its update path, not after an Apply.

To roll back, set **Repository** to the old tag again, as long as that image is still loaded. If
the newer version upgraded the database, the older one refuses to start ("database schema version
… is newer than this build of Dupearr supports"). In that case either stay on the newer tag, or
copy the pre-update backup zip out of `Backups/`, give the old tag an empty appdata folder and
upload that backup in *System → Backup*. Loading an
archive whose tag already exists moves the tag to the new image, and the old one stays as an
untagged `<none>` image (`docker image prune` removes it). Versioned tags keep rollbacks and
"which build is running?" simple.

Back to the registry: set **Repository** to `ghcr.io/sl0wz3r/dupearr:latest` (or a pinned
`ghcr.io/sl0wz3r/dupearr:<version>@sha256:<digest>`, see [section 1](#1-the-image)) and **Apply**.

## Publishing to Community Applications

CA only accepts templates from a **public GitHub repository** with an OSI-approved license and a
`ca_profile.xml` at its root, and the image must be pullable by anyone (ghcr.io or Docker Hub).
Everything public is set in **one** file, [`ca/publish.env`](ca/publish.env) (owner, repository,
registry, support link, version); the template and the profile are rendered from it:

```sh
$EDITOR unraid/ca/publish.env      # PUBLIC_OWNER, PUBLIC_REPO, ... (decide before going public: never rename later)
make ca-template ca-profile        # render + validate unraid/dupearr.xml and ./ca_profile.xml
make ca-validate                   # offline: both files current and passing every CA rule
make ca-preflight                  # online, read-only: public repo, license, raw URLs, icon, image amd64/arm64
```

The image must be public on ghcr.io (a new package is private until its visibility is changed on
the package page), and Community Applications (<https://ca.unraid.net/submit/new>) needs the
public GitHub repository URL. Template changes pushed to the default branch reach CA at its next
build; containers that are already installed keep their saved template, so announce new settings
in the release notes.

When you replace the icon later, change its URL (set `ICON_URL` in `ca/publish.env`, for example
the default URL with `?v=2` appended, then `make ca-template ca-profile`): Unraid caches icons per
URL.
