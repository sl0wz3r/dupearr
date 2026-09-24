# Getting help with Dupearr

Dupearr is a small project in beta. Please ask in the right place: the answers are public, so they
help the next person too.

| You want to… | Go to |
|---|---|
| Ask how to set something up, or whether a result is expected | [Discussions → Q&A](https://github.com/sl0wz3r/dupearr/discussions/categories/q-a) |
| Suggest an idea or talk about the [roadmap](ROADMAP.md) | [Discussions → Ideas](https://github.com/sl0wz3r/dupearr/discussions/categories/ideas) |
| Report a bug: something is wrong or broken | [New issue → Bug report](https://github.com/sl0wz3r/dupearr/issues/new/choose) |
| Request a specific, well-defined feature | [New issue → Feature request](https://github.com/sl0wz3r/dupearr/issues/new/choose) |
| Report a security vulnerability | **Privately**, as described in [SECURITY.md](SECURITY.md). Never in a public issue or discussion. |
| Get help with Unraid | The Unraid forum support thread. Its link will be added here once the Community Applications listing is live; until then, use Discussions → Q&A. |

## If Dupearr removed something it should not have

1. Turn **dry run** back on (*Settings → Media Management*). Dupearr checks dry run again right
   before every queued removal, so from then on everything is only simulated.
2. Open *Activity → History*. Every removal shows the method that was used and whether it was
   **Permanent** or went to a **Recycle bin**. You can restore anything in Dupearr's recycle bin
   from *Activity*. Radarr and Sonarr keep their own recycling bin (their *Settings → Media
   Management*).
3. Open a [bug report](https://github.com/sl0wz3r/dupearr/issues/new/choose) and answer **Yes**
   to "Did Dupearr remove, move or change a file it should not have?". These reports get the
   `safety` label and are handled first.

## Before you ask

- Look at *System → Status*. The health checks catch most configuration problems and say how to
  fix them.
- Read the user guides: [troubleshooting](docs/user/troubleshooting.md), [FAQ](docs/user/faq.md),
  [safety](docs/user/safety.md) and [configuration](docs/user/configuration.md). Path mappings
  cause most problems.
- Search the existing [issues](https://github.com/sl0wz3r/dupearr/issues?q=is%3Aissue) and
  [discussions](https://github.com/sl0wz3r/dupearr/discussions).

## What to include

- **Version:** *System → Status*, or `dupearr version`. Include the image tag or digest if you pin
  one.
- **Install method:** Unraid (Community Applications or a manual template), docker compose,
  docker run, or a native binary (which OS).
- **Dry run:** on or off, and your order of deletion methods (*Settings → Media Management*).
- **Your other apps:** the versions of Plex, Radarr and Sonarr, and how many instances of each.
- **Path layout:** the container path mappings of Plex, the \*arrs and Dupearr, plus the path
  mappings set up in Dupearr.
- **For a question about a decision:** the group's detail: the "decided by" line and any review
  flags. A screenshot is fine.
- **Logs:** see below.

### Sharing logs safely

1. Set the log level to `debug` (*Settings → General*) and reproduce the problem.
2. Get the log from *System → Log Files*, from `logs/dupearr.txt` in the data directory, or with
   `docker logs dupearr`. Only share the part around the problem.
3. **Check it before you post it.** Dupearr redacts API keys, tokens and passwords in its log. It
   does **not** hide file paths, host names or IP addresses, and paths show your folder layout and
   your titles. Replace anything you do not want to make public, but keep the structure:
   `/data/media/movies/…` versus `/movies/…` is often the answer. If the log has a first-run
   `setupCode=` line, remove it.

**Never post** your Plex token, your \*arr API keys, Dupearr's API key or webhook token (it is in
every webhook URL, as `?apikey=`), notification secrets (webhook URLs, bot tokens) or passwords.
Never attach `config.xml`, `dupearr.db` or a backup zip: they hold every secret. If something
leaks, replace it. In Dupearr, use **Regenerate** for the API key (*Settings → General*) and for
the webhook token. For a Plex token or an \*arr key, replace it in the app that issued it, then
update Dupearr.
