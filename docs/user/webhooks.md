# Webhooks

Scheduled scans (every 6 hours by default) find every duplicate eventually. Webhooks make Dupearr
look at a title **as soon as** Radarr, Sonarr or Plex adds or changes a file, by queueing a
*targeted scan* of just that movie, series or item. Targeted scans never remove anything on their
own: they update the affected groups (marking one *resolved* when the duplicate is gone), and
removals still need approval (or auto mode; a targeted scan does not count towards its stable-scan
rule).

Keep the scheduled scan enabled even with webhooks: the \*arrs silently drop webhooks for a while
after a few failed deliveries (for example while Dupearr restarts).

All webhook URLs authenticate with Dupearr's **webhook token** in the `apikey` query parameter,
because neither Plex nor older \*arr versions can send custom headers. Copy the ready-made URLs from
*Settings → Connections → Media Servers / Applications → Webhook* (enter the address the other
application uses to reach Dupearr; the copy button includes the token):

| Source | URL |
|---|---|
| Radarr | `http://<dupearr>:3873/api/v1/webhook/radarr?apikey=<webhook token>` |
| Sonarr | `http://<dupearr>:3873/api/v1/webhook/sonarr?apikey=<webhook token>` |
| Plex | `http://<dupearr>:3873/api/v1/webhook/plex?apikey=<webhook token>` |

**Never put the API key into a webhook URL.** Webhook URLs are stored in Radarr and Sonarr (their
API, UI and backups show them unmasked) and in your plex.tv account. The webhook token can do
nothing but queue these scans; the API key controls all of Dupearr (turning dry run off, approving
removals). URLs with the API key still work for now, but Dupearr logs a warning and shows a health
warning (*WebhookApiKeyCheck*): replace them, then **regenerate the API key** (*Settings → General*).
**Regenerate webhook token** (under the URLs) if a webhook URL leaked; then update the webhooks.

`<dupearr>` must be reachable **from the sending app**: the container name on a shared Docker
network (`http://dupearr:3873`), otherwise the host's LAN IP. Add your URL base if you set one
(`http://host:3873/dupearr/api/v1/…`).

Webhooks answer `200` immediately with `{"queued": true|false}` and do the work in the background.
Test events are accepted and ignored. Webhook scans are bounded so that a leaked webhook token
cannot flood Dupearr or your Plex server: at most 8 wait in the queue and at most 30 are queued at
once (one more every 20 seconds). A webhook beyond that answers `{"queued": false, "reason": …}`
and the next scheduled scan picks the change up; a large import batch is simply covered by it. A wrong or missing token gets `401`; a Sonarr payload sent to
the Radarr URL (or the other way round) gets `400`, so **Test** in the \*arr fails when the URL is
mixed up.

## Radarr

*Radarr → Settings → Connect → + → Webhook*

| Field | Value |
|---|---|
| Name | `Dupearr` |
| Notification triggers | **On File Import**, **On File Upgrade**, **On Rename**, **On Movie File Delete** (older Radarr versions call the first two *On Import* / *On Upgrade*). Other triggers are accepted but ignored. |
| Tags | empty (= all movies) |
| URL | `http://dupearr:3873/api/v1/webhook/radarr?apikey=<webhook token>` |
| Method | `POST` |
| Username / Password | empty |

Click **Test** (Dupearr answers `200`), then **Save**. Add the webhook to **every** Radarr
instance (including 4K).

*On File Upgrade* matters: without it Radarr sends nothing when an upgrade replaces a file, which
is exactly when a stray older copy tends to stay behind in another library.

## Sonarr

*Sonarr → Settings → Connect → + → Webhook*

| Field | Value |
|---|---|
| Name | `Dupearr` |
| Notification triggers | **On File Import**, **On File Upgrade**, **On Rename**, **On Episode File Delete** (older versions: *On Import* / *On Upgrade*). *On Import Complete* is fine to enable as well. |
| Tags | empty (= all series) |
| URL | `http://dupearr:3873/api/v1/webhook/sonarr?apikey=<webhook token>` |
| Method | `POST` |

**Test**, **Save**, and repeat for every Sonarr instance.

Dupearr's own removals through the \*arr also produce *File Delete* webhooks; that is expected and
harmless (it just re-checks the title).

## Plex

Plex webhooks require an active **Plex Pass** on the server owner's account and are configured per
account, not per server.

1. Plex Web → your account menu → **Account Settings → Webhooks**
   (<https://app.plex.tv/desktop/#!/settings/webhooks>).
2. **Add Webhook** → `http://<dupearr>:3873/api/v1/webhook/plex?apikey=<webhook token>` → **Save Changes**.

Plex sends many events (play, pause, rate, …). Dupearr acts on **`library.new`** (new item added
to a library) and ignores the rest. The request comes from the Plex **server**, so the URL must be
reachable from the machine running Plex Media Server.

## Checking that it works

- *System → Tasks / Events* and *Activity → History* show targeted scans with trigger *webhook*.
- In the \*arr: *System → Events* shows failed webhook deliveries; *Settings → Connect* marks a
  connection that is temporarily disabled after failures.
- `curl -X POST "http://<dupearr>:3873/api/v1/webhook/radarr?apikey=<webhook token>" -H 'Content-Type: application/json' -d '{"eventType":"Test"}'`
  returns `{"queued":false}` when the URL and token are right, `401` when the token is wrong.
