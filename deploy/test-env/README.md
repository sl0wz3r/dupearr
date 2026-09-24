# Local test environment (Docker)

A throwaway Dupearr you can click around in, wired to **fake** Plex, Radarr, Radarr 4K, Sonarr and Tautulli
servers (`tools/fakemedia`). The fake library is a tree of sparse dummy files inside a Docker
volume — nothing touches real media, and the files take no real disk space.

```bash
make test-env        # build images, start, pre-configure, run a first scan
```

Then open <http://localhost:3873> and create your login (first-run setup).

## What you get

| Service | Where | Notes |
|---|---|---|
| Dupearr | http://localhost:3873 | image `dupearr:test-env`, PUID/PGID 1000, `/config` + `/data` volumes |
| Fake Plex | http://127.0.0.1:42400 (token `fAkEpLeXtOkEn0000001`) | libraries Movies, Movies 4K, TV Shows |
| Fake Radarr | http://127.0.0.1:47878 (key `fa4e…7878`) | no recycle bin (deletes are "permanent") |
| Fake Radarr 4K | http://127.0.0.1:47879 (key `fa4e…7879`) | recycle bin `/data/recycle/radarr4k` |
| Fake Sonarr | http://127.0.0.1:48989 (key `fa4e…8989`) | recycle bin `/data/recycle/sonarr` |
| Fake Tautulli | http://127.0.0.1:48181 (key `fa4e…8181`, header `X-Api-Key`) | plays of a few titles, for the *Played* / *Last played* criteria |

`setup.sh` configured Dupearr for you: the Plex server, the three *arr instances, Tautulli, identity path
mappings (`/data/media → /data/media`), a shared scope group for Movies + Movies 4K, **dry run ON**,
minimum age **0 h** (the fake files are brand new) and a recycle bin at `/data/dupearr-recycle`.

The default scenario contains a copy of every interesting case: 4K DV remux vs 1080p WEB-DL, Plex
Optimized Versions, stacked cd1/cd2, a multi-episode file, a Sonarr-tracked loser, two films Plex
merged by mistake, an unanalyzed file, a sample, hardlinks, editions / 3D / language variants (not
duplicates) and a 4K + 1080p pair managed by two Radarr instances (protected).

## Things to try

- **Duplicates** → open a group → compare copies, override keep/remove, **Approve** (dry run just
  records "would delete via …" under Activity → History).
- Settings → Media Management → turn **Dry run** off, set deletion methods to
  *arr → filesystem → Plex to see files go to the recycle bin, then **Restore** one from
  Activity → History → Actions.
- Settings → Profiles: switch the default profile (e.g. "Keep One Per Resolution") and watch
  decisions change.

## Housekeeping

```bash
make test-env-logs   # follow logs
make test-env-down   # stop (keeps volumes)
make test-env-reset  # wipe Dupearr's config + the fake library and start over
```

Restarting only the fake (`docker compose -f deploy/test-env/docker-compose.yml restart fakemedia`)
resets the fake library to the scenario while keeping Dupearr's settings and history.
To skip the login page while testing, uncomment `DUPEARR__AUTH__METHOD: "None"` in
`docker-compose.yml` and run `make test-env` again.
