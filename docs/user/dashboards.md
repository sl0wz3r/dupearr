# Dashboards

Dupearr's duplicate counts can go on a dashboard such as [Homepage](https://gethomepage.dev),
through Homepage's generic `customapi` widget. Native Homepage and Homarr widgets are on the
[roadmap](../../ROADMAP.md#dashboard-widgets-homepage-homarr).

> [!WARNING]
> The widget needs Dupearr's **API key**, and the API key is an admin credential: whoever has it
> can approve removals and change every setting, like you in the web UI. Keep the dashboard that
> holds it private (never reachable from the internet without its own login), and keep the key
> out of files you share. See [Keeping the key private](#keeping-the-key-private).

## The statistics endpoint

`GET /api/v1/duplicate/stats`, authenticated with the `X-Api-Key` header, returns (from the
[demo](../../deploy/demo/)):

```json
{
  "total": 10,
  "byStatus": { "pending": 5, "review": 4, "protected": 1, "queued": 0, "deferred": 0,
                "failed": 0, "ignored": 0, "resolved": 0 },
  "reclaimableBytes": 86342893565,
  "reclaimedBytes": 0,
  "lastScan": { "id": 1, "status": "completed", "trigger": "manual",
                "startedAt": "2026-09-24T17:54:00.96Z", "finishedAt": "2026-09-24T17:54:00.98Z",
                "stats": { "groupsFound": 10, "pendingGroups": 5, "reviewGroups": 4 } }
}
```

(`lastScan.stats` has more counters than shown here.)

| Field | Meaning |
|---|---|
| `total` | Duplicate groups Dupearr knows, in any status |
| `byStatus.<status>` | Groups per status: `pending` (waiting for your approval), `review` (needs a look first), `queued`, `deferred`, `protected`, `failed`, `ignored`, `resolved` |
| `reclaimableBytes` | Size of the copies marked for removal in *pending*, *review* and *queued* groups |
| `reclaimedBytes` | Size of the files Dupearr actually removed (dry-run removals do not count) |
| `lastScan` | The last scan (`status`, `startedAt`, `finishedAt`, `trigger`, `stats`), or `null` before the first one |

Full reference: [docs/API.md](../API.md#duplicates).

## Homepage

Add a service to Homepage's `services.yaml`:

```yaml
- Media:
    - Dupearr:
        icon: https://raw.githubusercontent.com/sl0wz3r/dupearr/main/unraid/icon.png
        href: http://your-server:3873 # where your browser opens Dupearr
        description: Duplicate remover
        widget:
          type: customapi
          url: http://dupearr:3873/api/v1/duplicate/stats
          refreshInterval: 60000 # ms; the numbers only change with scans and removals
          headers:
            X-Api-Key: "{{HOMEPAGE_VAR_DUPEARR_API_KEY}}"
          mappings:
            - field: byStatus.pending
              label: Pending
              format: number
            - field: byStatus.review
              label: To review
              format: number
            - field: reclaimableBytes
              label: Reclaimable
              format: bytes
            - field: lastScan.finishedAt
              label: Last scan
              format: relativeDate
```

and give Homepage the key as an environment variable (in its `docker-compose.yml` or its Unraid
template), not in `services.yaml` itself:

```yaml
    environment:
      HOMEPAGE_VAR_DUPEARR_API_KEY: "the key from Dupearr: Settings → General → API Key"
```

Homepage replaces `{{HOMEPAGE_VAR_DUPEARR_API_KEY}}` with that variable. `HOMEPAGE_FILE_…`
variables read the value from a file instead, for Docker secrets.

- **`url`** is the address *Homepage's container* reaches Dupearr at: `http://dupearr:3873` when
  both containers share a Docker network, otherwise the server's IP and port (on Unraid, usually
  `http://<server IP>:3873`). Add the URL base if you set one (`…/dupearr/api/v1/duplicate/stats`).
- **Other fields:** any number from the table works, for example `byStatus.queued`, or
  `reclaimedBytes` with `format: bytes` for what Dupearr has freed so far.
- The header works with every authentication method (*Forms*, *External*, *None*) and whatever
  host name Homepage uses: a valid API key is accepted before those checks.

Tested with Homepage 2.4.0 and Dupearr 0.1.1 (the demo): the widget shows *5 Pending,
4 To review, 86.3 GB Reclaimable* and the time of the last scan.

## Keeping the key private

- **Homepage requests the stats from its own server** (its widget proxy), so the key is not sent
  to the browsers that open the dashboard. It is in Homepage's environment, and in anything that
  copies it: compose files, backups, screenshots of the container settings.
- **Dupearr has no read-only key yet.** The API key can do everything the web UI can.
- **If it leaks,** *Regenerate* it in *Settings → General* (your password is required) and update
  Homepage's variable. Changing your Dupearr password also replaces the key, unless you tick
  *Keep the current API key*.
- **Never put the key into a URL** (`?apikey=` is refused anyway): URLs end up in proxy logs and
  browser history. Use the header.

## Troubleshooting

| What you see | Cause |
|---|---|
| The fields stay at `-` | Homepage cannot reach the `url`. Check it from Homepage's container, for example `docker exec homepage wget -qO- --header "X-Api-Key: <key>" <url>` |
| An API error (HTTP 401) | Wrong key, or the key changed (regenerated, or replaced by a password change) |
| `Last scan` is empty | No scan has run yet (`lastScan` is `null`) |

To try it without touching your real Dupearr, start the [demo](../../deploy/demo/) and point
`url` at it. It runs without login; its API key is in its config file:
`docker compose exec dupearr sed -n 's:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p' /config/config.xml`.
