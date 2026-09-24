# Security policy

Dupearr holds your Plex **owner** token, your Radarr/Sonarr API keys and your notification
secrets, and it can delete media files. Security reports are welcome and taken seriously.

## Supported versions

Dupearr is pre-1.0. Security fixes are made on `main` and shipped in the next release; there are no
back-ported fixes for older versions.

| Version | Supported |
|---|---|
| Latest release (and `main`) | Yes |
| Any older release | No — update to the latest release |

After 1.0, the latest minor release will receive security fixes.

## Reporting a vulnerability

**Please do not open a public issue, pull request or discussion with the details of a
vulnerability.**

Report it privately through GitHub: **[Report a vulnerability](https://github.com/sl0wz3r/dupearr/security/advisories/new)**
(the repository's *Security* tab → *Report a vulnerability*). Only you and the maintainer
(**sl0wz3r**) can see the report. If that form is not available to you, open an issue
titled **"Security contact request"** that contains **no details** (no affected feature, no proof
of concept); the maintainer will contact you to arrange a private channel.

Please include:

- the Dupearr version (*System → Status* or `dupearr version`) and how you run it (Docker, Unraid,
  native), including the authentication method and whether a reverse proxy is involved;
- what an attacker needs (network position, credentials, a malicious upstream, …) and what they
  gain;
- steps to reproduce or a proof of concept, and any logs — **with tokens, API keys and passwords
  removed** (Dupearr redacts most secrets at `debug` level, but check before sending).

What to expect:

- an acknowledgement within **7 days**;
- an assessment (accepted, needs more information, or not a vulnerability, with reasons) within
  **14 days**;
- a fix or a documented mitigation as soon as possible, targeting **30 days** for high-severity
  issues and **90 days** for the rest; you will be told if that slips;
- credit in the release notes and in [docs/SECURITY.md](docs/SECURITY.md), unless you prefer not
  to be named.

Please give us a reasonable time to release a fix before disclosing publicly, and coordinate the
disclosure date with the owner.

## Scope

In scope: the Dupearr server (`cmd/`, `internal/`), the web UI (`web/`), the container image and
its entrypoint (`Dockerfile`, `docker/`), the deployment files (`deploy/`, `unraid/`) and the CI /
release workflows.

Especially interesting:

- authentication or authorization bypasses (including through reverse-proxy headers, DNS
  rebinding or cross-site requests);
- any way to make Dupearr delete, move or overwrite a file it should not (outside the reviewed
  removals, outside library folders, through symlinks, crafted backups or hostile upstream data);
- disclosure of stored secrets (Plex token, *arr keys, notification secrets, API key, password
  hash) to anyone but an authenticated admin;
- server-side request forgery that turns admin features into a pivot into your network;
- code execution, container escape or privilege escalation (entrypoint, file ownership).

Out of scope (see the accepted risks in [docs/SECURITY.md](docs/SECURITY.md#residual-and-accepted-risks)):

- attacks that require an already authenticated admin, unless they cross a boundary listed above;
- setups that deliberately disable protections (authentication `None`, a port forwarded to the
  internet without a reverse proxy, `DisabledForLocalAddresses` on an untrusted network);
- denial of service by flooding the network or the host;
- vulnerabilities in Plex, Radarr, Sonarr or other third-party software (report those upstream).

## Safe harbour

We will not pursue anyone who reports in good faith, tests only against their **own**
installation, avoids deleting or accessing other people's data, and gives us the chance to fix the
issue before disclosure.

## Hardening your installation

See the [deployment hardening guide](docs/SECURITY.md#deployment-hardening-guide): authentication
modes, reverse-proxy setup, file permissions, network isolation, backups and updates.
