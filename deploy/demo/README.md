# Dupearr demo (fake data)

Try Dupearr in a minute without pointing it at anything real. The demo runs Dupearr next to
**fake** Plex, Radarr, Radarr 4K and Sonarr servers that serve a made-up library, configures
Dupearr for you and runs a first scan, so the Duplicates page is full when you open it.

- **Nothing touches your media.** The "library" is a tree of empty placeholder files (sparse files:
  they report film-sized sizes but take almost no disk space) inside a Docker volume. No folder of
  your computer is mounted, and the fake servers are only reachable inside the demo's own Docker
  network.
- **No login, local only.** Dupearr runs with authentication *None* and listens on `127.0.0.1`
  only. Do not publish its port to your network. (Its health check warns that authentication is
  disabled, as it should.)
- **Throwaway.** `docker compose down -v` removes all of it.

## Start

You need Docker with Compose v2 (`docker compose`) on x86-64 or arm64, and about 80 MB for the two
images. Run it in a new, empty folder: `curl -O` writes `docker-compose.yml` into the current folder
and would replace a compose file that is already there.

```bash
mkdir dupearr-demo && cd dupearr-demo
curl -fsSLO https://raw.githubusercontent.com/sl0wz3r/dupearr/main/deploy/demo/docker-compose.yml && docker compose up -d
```

Then open **<http://localhost:3873>**. The set-up takes about half a minute after the containers
start; `docker compose logs -f seed` shows it and ends with "Dupearr demo is ready".

Port 3873 already in use, for example by your own Dupearr? Pick another one:
`DEMO_PORT=3874 docker compose up -d`.

## What is inside

| Service | Image | What it does |
|---|---|---|
| `dupearr` | Dupearr | The real app, named "Dupearr Demo", on `127.0.0.1:3873` (`DEMO_PORT`) |
| `demo-media` | demo-media | Fake Plex (libraries *Movies*, *Movies 4K*, *TV Shows*), Radarr, Radarr 4K and Sonarr |
| `seed` | demo-media | One-shot: adds the fakes to Dupearr through its API with identity path mappings (`/data/media → /data/media`), puts *Movies* and *Movies 4K* in one scope group, sets the minimum age to 0 h and a recycle bin (`/data/dupearr-recycle`), keeps dry run on, and runs the first scan |
| `init` | demo-media | One-shot: gives the shared `/data` volume to uid/gid 1000, the user the fakes and Dupearr (`PUID`/`PGID`) run as, so Dupearr can move the fake files |

Two volumes: `media` (the fake library and Dupearr's recycle bin, `/data` in every container) and
`config` (Dupearr's `/config`). The fake servers use fixed, public test credentials, which is why
none of their ports is published. The `seed` service runs again on every `docker compose up`: it
only adds what is missing, keeps your changes and rescans.

## Things to try

1. **Duplicates.** Ten groups: five *pending* (for example *Blade Runner 2049*, where a 4K Dolby
   Vision remux tracked by Radarr beats an untracked 1080p WEB-DL, or *The Matrix*, whose Plex
   Optimized Version is never counted as a duplicate), four *to review* (*The Thing*: two films Plex
   merged by mistake; *Inception*: a file Plex never analyzed; *Interstellar*: two hard links to one
   file; *Arrival*: a sample) and one *protected* (*Dune*: its 4K and 1080p copies belong to two
   Radarr instances). Open a group to compare the copies and see why each one is kept or removed.
2. **Approve in dry run.** Approve *Blade Runner 2049*. Dry run is on, so nothing is deleted:
   *Activity → History* records "Would delete via plex …".
3. **Turn dry run off.** In *Settings → Media Management*, switch **Dry run** off and put the
   deletion methods in the order \*arr → filesystem → Plex. Approve *The Matrix*: its untracked
   720p copy goes to Dupearr's recycle bin instead of being deleted through (fake) Plex.
4. **Restore.** In *Activity → History → Actions*, **Restore** puts the file back where it was.
   The group comes back as *ignored*, so it is not removed again until you un-ignore it.
5. **Profiles.** In *Settings → Profiles*, make "Keep One Per Resolution" the default and rescan to
   see the decisions change.
6. **Start over.** `docker compose restart demo-media && docker compose up -d` resets the fake
   library and rescans (the seed runs again); Dupearr keeps its settings and history.
   `docker compose down -v && docker compose up -d` resets everything.

## Stop and clean up

```bash
docker compose stop            # pause the demo (docker compose start resumes it)
docker compose down -v         # remove it: containers, network and both volumes
```

`docker compose down -v --rmi all` also deletes the two images; leave `--rmi all` out if you run
your own Dupearr from the same `:latest` image.

## Settings

Set them in the environment or in a `.env` file next to `docker-compose.yml`.

| Variable | Default | |
|---|---|---|
| `DEMO_PORT` | `3873` | Port on `127.0.0.1` for the web UI |
| `DEMO_DUPEARR_IMAGE` | `ghcr.io/sl0wz3r/dupearr:latest` | Dupearr image, e.g. a pinned `…/dupearr:X.Y.Z` |
| `DEMO_MEDIA_IMAGE` | `ghcr.io/sl0wz3r/dupearr-demo-media:latest` | The fake servers and the seed (same tags as Dupearr) |
| `DEMO_TZ` | `Etc/UTC` | Dupearr's time zone |

Newer images: `docker compose pull && docker compose up -d`.

## For contributors

`make demo-local` builds both images from your checkout, runs this compose file with them on
`127.0.0.1:38731` and checks the result with [`test.sh`](test.sh) (the seed, the ten groups, dry
run, recycle bin, restore and a second seed), then removes the demo and the images;
`DEMO_KEEP=true` leaves it running. The demo-media image is built from
[`docker/fakemedia.Dockerfile`](../../docker/fakemedia.Dockerfile): the fake servers are
[`tools/fakemedia`](../../tools/fakemedia) and the set-up is [`seed.sh`](seed.sh). The release
workflow tests the demo and publishes the demo-media image with every release, next to the Dupearr
image. To work on Dupearr itself against the same fakes, use the [test environment](../test-env/README.md)
(`make test-env`), which builds from source.
