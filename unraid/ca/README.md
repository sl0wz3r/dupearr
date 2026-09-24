# unraid/ca: the Community Applications template, from one settings file

`unraid/dupearr.xml` (the one public Unraid template) and the repository-root `ca_profile.xml` are
**generated** from the sources here. The public owner, repository, registry and support link are
set in exactly one place, [`publish.env`](publish.env); every URL in both files is derived from it.

| File | Committed | Purpose |
|---|---|---|
| [`publish.env`](publish.env) | yes | **The one settings file**: `PUBLIC_OWNER`, `PUBLIC_REPO`, `PUBLIC_HOST`, `REGISTRY`, `SUPPORT_URL`, `FORUM_URL`, `VERSION`, `RELEASE_DATE`, layout-B options |
| [`dupearr.xml.tmpl`](dupearr.xml.tmpl) | yes | Source of `unraid/dupearr.xml`: Overview, Requires, Changes, Config entries |
| [`ca_profile.xml.tmpl`](ca_profile.xml.tmpl) | yes | Source of `ca_profile.xml` (maintainer profile, required at the repository root) |
| [`render.sh`](render.sh) | yes | POSIX sh renderer: parses (never sources) the env files, derives the URLs, fills the placeholders, refuses to write anything with a placeholder left |
| [`validate-template.sh`](validate-template.sh) | yes | Offline check of the Community Applications template rules listed below (needs `xmllint`) |
| [`preflight.sh`](preflight.sh) | yes | Online, read-only, anonymous check of the live public repository, raw URLs, icon and image |
| [`private.env.example`](private.env.example) | yes | Example overlay for a LAN/private-registry variant |
| `private.env` | **no** (git-ignored) | Your own LAN/private-registry values (copied from `private.env.example`) |
| `out/` | **no** (git-ignored) | Rendered scratch files, including `dupearr-private.xml` |

## Commands

```sh
make ca-vars          # print every derived value (URLs to paste into the CA form)
make ca-template      # render + validate -> unraid/dupearr.xml
make ca-profile       # render + validate -> ./ca_profile.xml (repository root)
make ca-validate      # offline: committed files are current and pass every rule (+ repository scan)
make ca-preflight     # online, read-only: repo public/active/licensed, raw URLs, icon, image amd64/arm64
make ca-private       # LAN variant (publish.env + private.env) -> unraid/ca/out/dupearr-private.xml
```

`ca-template` and `ca-profile` render into `out/`, validate there, and only then copy the result
into place, so a failing rule never replaces a good file. Override the settings file with
`make ca-template CA_ENV=path/to/other.env` (for example to try a different owner).

## Placeholders

In a `.tmpl` file, `{{KEY}}` is replaced by the XML-escaped value of `KEY`; `{{?KEY}}` does the
same but drops the whole line when the value is empty (used for `<Forum>`); a line starting with
`{{#` is a comment for maintainers and is removed. Unknown keys, malformed values (owner,
repository, version, date, lower-case image, URLs) and anything left looking like `{{…}}` fail the
render.

| Key | Default (derived) | Used for |
|---|---|---|
| `PUBLIC_REPO_URL` | `https://github.com/OWNER/REPO` | `<Project>`, profile `<WebPage>` |
| `PUBLIC_RAW_BASE` | `https://raw.githubusercontent.com/OWNER/REPO/BRANCH` | base of `ICON_URL` |
| `TEMPLATE_URL` | `https://raw.githubusercontent.com/OWNER/TEMPLATES_REPO/BRANCH/TEMPLATE_PATH` | `<TemplateURL>` |
| `ICON_URL` | `PUBLIC_RAW_BASE/unraid/icon.png` | `<Icon>`, profile `<Icon>` |
| `README_URL` | `PUBLIC_REPO_URL/blob/BRANCH/unraid/README.md` | `<ReadMe>` |
| `IMAGE` | `ghcr.io/<lower-case owner>/dupearr:latest` (`docker.io`: `owner/dupearr:latest`) | `<Repository>` |
| `REGISTRY_URL` | `https://github.com/OWNER/REPO/pkgs/container/dupearr` (Docker Hub: `https://hub.docker.com/r/owner/dupearr`) | `<Registry>` |
| `SUPPORT_URL` | `PUBLIC_REPO_URL/issues` until you set the forum thread | `<Support>`, profile text |
| `FORUM_URL` | empty (line dropped) | profile `<Forum>` |
| `VERSION`, `RELEASE_DATE` | from `publish.env` | `<Date>`; the newest `<Changes>` entry (written in the `.tmpl`) must name them, like the newest `CHANGELOG.md` release (`deploy/unraid_changes_test.go`) |
| `REQUIRES_EXTRA` | empty | appended to `<Requires>` (LAN variant only) |
| `ENV_NAME` | the env file names | header comment |

## Rules the template keeps (checked by `validate-template.sh`)

Each `<Config>` on one line; `&amp;` for `&`; no `<…>` text; no `[` or `]` in `Overview` or
`Changes` (CA turns them into tags and strips them, which also breaks Markdown links); Overview
states the deletion risk and the dry-run/approval defaults; `Support`/`Project`, `TemplateURL` =
raw URL of this file, https `Icon` PNG with transparency, valid `Category` tokens, `WebUI` on the
container port 3873 that exists as a Port entry, `Privileged` false, `Beta` true while 0.x, no
`MaxVer`, `ExtraParams` flags only, `:latest` image on a public registry, no private addresses,
and **no TZ entry**: Unraid injects the server's `TZ` into every container, and an empty template
`TZ` would come after it and reset the container to UTC.

Changing the owner or the repository after the CA listing can get the whole repository
blacklisted: decide `PUBLIC_OWNER`/`PUBLIC_REPO` before the repository goes public.
Submission: [Publishing to Community Applications](../README.md#publishing-to-community-applications)
in `unraid/README.md`.
