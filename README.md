# mistwarp-api

OSL backend for the MistWarp community platform. It provides Rotur validator auth, project metadata, Cloudflare R2 project storage, and native pull requests between MistWarp forks. Rotur Git can be connected as an optional external Git provider.

## Run

Copy `.env.example` to `.env`, fill in the values, then run:

```
osl run main.osl
```

The server loads `.env` automatically. Real environment variables override `.env`.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| PORT | 5627 | Listen port |
| HISTORY_MIGRATION_WORKERS | 4 | Concurrent background workers used to backfill missing project histories (clamped to 1-8) |
| APP_URL | https://api.mistwarp.org | Public URL of this API |
| ROTUR_APP_KEY | mistwarp | The old validator key, for editors that predate validators keyed to the Rotur App |
| COMMERCE_SERVICE_KEY | | Key registered for `mistwarp` in Rotur's `COMMERCE_SERVICE_KEYS`. Only bounty awards and the groups integration still use it |
| ROTUR_CLIENT_ID | | The MistWarp Rotur App's client ID (`app_1938b6a87799f862`). With the secret, mistwarp-api calls the apps API as MistWarp |
| ROTUR_CLIENT_SECRET | | One of the MistWarp Rotur App's secrets, made with **New secret** on rotur.dev/me/developer |
| MISTWARP_ROTUR_USER | | Rotur account that gets MistWarp's share of sales. Without it, the MistWarp Rotur App's owner gets it |
| ROTUR_WEBHOOK_SECRET | | Signing secret (`whsec_…`) of the MistWarp Rotur App's webhook. Without it `POST /v1/rotur/webhook` answers 503 |
| R2_ENDPOINT | | https://accountid.r2.cloudflarestorage.com |
| R2_BUCKET | mistwarp | R2 bucket name |
| R2_ACCESS_KEY_ID | | R2 access key |
| R2_SECRET_ACCESS_KEY | | R2 secret key |
| R2_PUBLIC_BASE | | Public custom domain for the bucket |
| GITEA_URL | https://git.rotur.dev | Optional Rotur Git instance |
| GITEA_ADMIN_TOKEN | | Optional Rotur Git integration token |
| EDITOR_ORIGIN | https://mistwarp.org | Public editor base URL used in links |
| ADMIN_USERS | mist | Comma separated admin usernames |
| GITHUB_ORG | MistWarp | GitHub org whose pull requests appear on the roadmap's Changes tab |
| GITHUB_TOKEN | | Optional GitHub token; only raises the search rate limit, the feed works without one |
| REALTIME_URL | wss://api.mistwarp.org/v1/connect | Public multiplayer WebSocket endpoint |

The multiplayer WebSocket runs inside this API process at `/v1/connect`. It
uses the same listener, domain, and deployment as the HTTP API.

## Bans

A MistWarp ban is also a ban from the MistWarp Rotur App (`PUT /v2/apps/<app>/bans/<user>`), so Rotur enforces it too: the person's MistWarp tokens stop working and Rotur won't let them sign in to MistWarp. Rotur shows them the ban's reason, and never who made it. Lifting a ban lifts both.

Every five minutes MistWarp reads the app's bans. Bans made or lifted on rotur.dev/me/developer reach MistWarp's own list, and any ban or unban Rotur couldn't be told about yet is sent again. A ban lifted in MistWarp is never brought back from Rotur while that's pending. Rotur won't ban the app's owner or managers, classroom students, or accounts it doesn't know, so those bans stay MistWarp-only. Bans whose reason only names a report are sent with a general reason instead.

## Plans

Perks follow the person's Rotur plan. mistwarp-api asks the apps API (`GET /v2/apps/<client id>/users/<id>/plan`), which answers whatever their privacy settings. Rotur's public profile hides the plan from anyone who can't see the balance, which covers every account without a date of birth, so it's only read for people the apps API doesn't know. Answers are kept for 5 minutes, and during an outage the last known plan stays.

## Safety signals

The MistWarp Rotur App declares that people can talk and can spend credits. Before a comment is posted, mistwarp-api asks Rotur's message signal about the owner of the project or profile and the author of the comment it replies to. Comments are public, like replies to Rotur posts, so only a block or a ban (`blocked`, `banned`) stops one. When someone's message settings don't take messages from the author (`settings`, `unavailable`), the comment is still posted but doesn't notify them, and the same goes for replies, mentions and other notifications that carry someone's words. Before a purchase or donation starts, it asks the purchase signal. A "no" is shown to the person in Rotur's own words.

Rotur only answers about people who have used MistWarp through Sign in with Rotur, or made their account on MistWarp. For anyone else, or if Rotur can't be reached, MistWarp carries on as it did before. Rotur counts credits towards a parent's monthly limit when it moves them, so the purchase signal only asks.

## Payments

Buying a project or a game product, donating with a comment and refunding a game product are Rotur payment requests (`POST /v2/apps/<app>/payment-requests`). mistwarp-api never holds a permission to spend anyone's credits.

1. The editor asks for an intent. mistwarp-api asks Rotur for the payment, with a purchase key (`mwbuy_`, `mwgame_`, `mwdonate_` or `mwrefund_`) as its `reference`, and answers with the key and Rotur's `approveUrl`.
2. The person approves it on rotur.dev with their password. Rotur moves the credits, shared between everyone the sale pays: the creator, collaborators, the remixed project's creator and MistWarp's fee.
3. Rotur's `payment.completed` webhook marks the key paid and delivers projects and game products. The editor's confirm call does the same if it gets there first, asking Rotur whether the request was paid. Whichever comes second finds it done.

Donations are delivered when their comment is posted, and refunds when the product is revoked. Paid keys waiting for that are kept for 30 days, and unpaid ones for an hour (Rotur's requests expire after 15 minutes).

## Rotur webhooks

Set the MistWarp Rotur App's webhook to `https://api.mistwarp.org/v1/rotur/webhook` on rotur.dev/me/developer, with privacy requests on, and put its signing secret in `ROTUR_WEBHOOK_SECRET`.

- `user.deleted`, `user.left` and `user.banned` erase everything MistWarp holds about that person, the same as deleting their data from Settings.
- A `privacy.request` for `erasure` does the same. One for `access` sends them a MistWarp notification pointing to the download in Settings.
- `payment.completed` finishes the purchase it names (see Payments).

Deliveries are checked against `Rotur-Signature` and refused if their timestamp is more than five minutes out. Each is saved to `data/rotur-webhooks.json`, answered, then handled in the background, and a retried delivery is only handled once. Anything not yet handled when the server stops is handled when it starts.

Rotur only sends these for people who have used MistWarp through Sign in with Rotur, or whose account was made on MistWarp. The five-minute check of `/accounts/deleted_check` stays for everyone else.

## Reports

Every report is also filed in the MistWarp Rotur App's report queue (`POST /v2/apps/<app>/reports`), naming the reporter when Rotur knows them as a MistWarp user. Reports use Rotur's categories. One in a priority category (`csea`, `threat_to_life`, `self_harm`, `terrorism`) goes to Rotur's safety team as soon as it's filed. MistWarp's own queue and actions are unchanged. Dismissing a report closes it on Rotur as dismissed, and any other action closes it as resolved, except reports Rotur is still reviewing.

Who a report is about, and the snapshot sent with it, are worked out by mistwarp-api from the content when the report is made, never taken from the reporter: a project's owner and its title, description, instructions and notes; a comment's author and its text; a profile's owner and bio. Moderation actions on a report (ban, warn) use the same person. A report whose subject can't be found stays MistWarp-only, because Rotur needs someone to name.

A report Rotur couldn't be reached for stays pending and is filed by the five-minute reconcile loop. One closed in MistWarp before Rotur had it is closed on Rotur as soon as it's filed.

## Badges

MistWarp gives badges on people's Rotur profiles as the MistWarp Rotur App. Which badges exist, and what earns them, is data. Define the badges on rotur.dev/me/developer, then map MistWarp's events to them in `data/rotur-badges.json`:

```json
{"events": {
  "project_shared": [{"badge": "creator", "delta": 1}],
  "love_received": [{"badge": "loved", "delta": 1}],
  "remixed": [{"badge": "remixed", "delta": 1}],
  "comment_posted": [{"badge": "commenter", "delta": 1}],
  "streak": [{"badge": "streak", "progress": "value"}]
}}
```

| Event | When |
| --- | --- |
| `project_shared` | Someone shares a project for the first time |
| `love_received` | Someone else loves their project, counted once per person per project |
| `remixed` | Someone else shares a remix of their project |
| `comment_posted` | They post a comment anywhere |
| `streak` | They save a project on a new day. The value is how many days in a row they have done so |

`delta` adds to their progress. `"progress": "value"` sets it to the event's value. A rule with neither is skipped. Updates are sent in the background, and Rotur only gives badges to people who have used MistWarp through Sign in with Rotur. Without the file, nothing is sent. The file is read on every event, so changing it needs no restart.

## Development feed

`/v1/development/pulls` lists MistWarp's own open and recently merged pull
requests, and `/v1/roadmap` resolves the ones linked to each entry.

GitHub's search quota is 10 requests a minute for an unauthenticated caller and
is counted per IP, so the feed is built to stay well inside it:

- One search covers the whole org, so a refresh costs two requests rather than
  one per repository.
- A refresh happens at most once every five minutes. The snapshot is stored, so
  a restart reuses it and costs nothing — a crash loop cannot burn the budget.
- Every response's `X-RateLimit-*` headers are recorded, and a refresh is
  refused when the remaining quota would not cover it, keeping a small reserve
  for anything else on the same address.
- A 403 or 429 backs off until the window reopens, honouring `Retry-After`.

Search responses carry no `ETag` and are sent `no-cache`, so a conditional
request cannot earn a free 304; not spending the request is the only lever.

`GITHUB_TOKEN` raises the ceiling but is not required.

## Deployment layout

The site and editor are one scratch-gui build (community pages live in
`scratch-gui/src/community`), served on the frontend domain:

| Path | Serves |
| --- | --- |
| / | community app (webpack `community` entry -> index.html) |
| /editor | scratch-gui editor |
| /embed.html | scratch-gui embed player (project pages iframe this) |
| /project/*, /explore, /users/*, /settings | community app (client routing) |

This API runs at `api.mistwarp.org`, with `mwapi.mistium.com` retained as a
backwards-compatible hostname. The frontend calls it at
`https://api.mistwarp.org/v1` directly. In dev, leave the API base unset and
webpack-dev-server proxies `/v1` to `http://localhost:5627`. Auth is
Bearer-token based (the session token returned by `/v1/auth`), so a
cross-domain API works without shared cookies. CORS echoes the request Origin
with credentials, so any frontend origin is accepted.

`/v1` is the canonical route group. Every route is also registered under
`/api` for backwards compatibility, including uploads and their larger body
limits. New clients and generated URLs must use `/v1`.

R2 bucket only needs public GET through `R2_PUBLIC_BASE`, and the public domain must expose the bucket's `assets/` prefix and allow CORS GET requests from the MistWarp frontend. Project metadata keeps the API `/blobs/assets` base. That endpoint serves a local asset when its file exists under `data/blobs/assets/`; otherwise it redirects to `R2_PUBLIC_BASE`. All writes go through this server. With no R2 configured, the server falls back to local disk (`data/blobs/`, served at `/blobs`) so it runs locally with zero setup.

## Upload pipeline

The editor POSTs a sparse sb3 to `POST /v1/projects/:id/upload` (multipart, fields `project` and optional `thumbnail`). The server extracts at most 256 MiB of `project.json`, validates it incrementally, requires asset filenames to match their content, and limits every asset to 10 MiB and all assets to 50 MiB. The JSON is accepted only when its gzip representation is at most 20 MiB.

### Commit inspection

The read-only commit endpoints inspect the stored `.mwp` on the server. Clients do not need to download the complete workspace to render history or browse an old revision.

- `GET /v1/projects/:id/commits/:sha` returns commit metadata, its first parent, and changed-file records. Each record has `path`, `status`, `oldOid`, `newOid`, `oldSize`, and `newSize`. With `?inline=1`, small text changes also include `oldData` and `newData`, encoded as base64 and capped at 1 MiB per file and 8 MiB per response. Root commits report every file as added. `legacy: true` means the change includes an old `project.sb3`; clients that need the expanded Fractch diff should use their legacy conversion fallback.
- `GET /v1/projects/:id/commits/:sha/tree` returns `commit` and a flat `files` array. Each file has `path`, `oid`, `mode`, `size`, and `binary`.
- `GET /v1/projects/:id/commits/:sha/file?path=<path>` returns one file record and `content`, encoded as base64 by JSON.
- `GET /v1/projects/:id/commits/:sha/co-authors` returns the Rotur users credited on that commit. The older `/collaborators` path remains an alias.
- `PATCH /v1/projects/:id/commits/:sha` accepts `{coAuthors: string[]}` with at most 25 existing Rotur usernames. `{collaborators: string[]}` remains an input alias. It replaces the commit's co-author metadata without rewriting Git objects or changing any SHA. Responses expose canonical `coAuthors` records as `{username, userId}` and a compatibility `collaborators` alias. Only the project owner, a maintainer, or an administrator may patch commit credits.

These routes use the same see-inside policy as workspace downloads. The server caches materialized workspace layers and computed JSON by the ordered content-addressed layer keys. Public, free projects may be cached by the CDN for one hour; private, unlisted, and paid responses use `private, no-store`. Stable ETags let browsers revalidate without reparsing Git objects. The local inspection cache keeps at most 256 files or 2 GiB and removes entries older than 24 hours.

Before extraction, the API rejects archives with unsafe paths, duplicate entries, symlinks, unsupported compression methods, or more entries and expanded bytes than the endpoint allows. MistWarp history archives are capped at 128 MiB compressed and 20,000 entries. Their expanded-byte ceiling matches the account's maximum project size, including its tier-specific asset allowance. The API reads each history entry through that ceiling before storing it, so forged ZIP metadata cannot bypass the limit.

- `assets/<md5ext>`: content addressed, shared across all projects and remixes, uploaded once ever
- `projects/<id>/project.json`: the gzip-encoded playable snapshot
- `projects/<id>/thumb.png`

Project JSON, history archives, and assets are staged on local disk before the upload request returns. The API serves those files locally until the project has been inactive for seven days. Every edit restarts that window, including saves with unchanged content. Shared files wait until every project referencing them has been inactive for seven days. The background loop checks every five minutes, uploads eligible files to R2, and removes each local copy only after a successful upload. Failed uploads stay local for retry. Existing local copies recorded in the R2 storage index are also removed after inactivity without uploading them again. With R2 disabled, files stay local. Git carries every save. `data/assets-index.json` tracks known assets so duplicates are never re-uploaded.

The first editor save uploads a compact MWP archive containing the Git repository without a duplicate worktree. Later saves compare the local HEAD with the server HEAD and send only new Git objects plus updated refs. The API stores those archives as content-addressed layers, so remixes share their parent's history instead of copying it. It compacts a chain after eight layers. If the user saves without making a commit, the editor sends a full archive with the worktree so uncommitted changes are not lost.

For a delta whose `baseHead` still matches the stored head, the API removes loose Git objects already present in the materialized base before storing the new layer. It retains the delta manifest and refs, verifies the combined archive, and stitches only the new commits and graph nodes ahead of `baseHead` onto the inherited metadata. A final locked head comparison rejects concurrent history changes before project metadata is saved.

## Storage limits

Each plan has a total storage limit: 500 MB on Free, 2 GB on Lite, 10 GB on Plus and 50 GB on Pro. A user's usage is the sum of every project they own, trashed projects included. Each project counts its assets, its compressed project JSON and its history archive, even when an asset is shared with other projects. Saving is checked against the owner's limit before anything is stored. A save may make a project bigger only while it fits in the free space, so a user over the limit can still save a project at its current size or smaller. Assets uploaded ahead of a save count as pending until a saved project references them, or for 7 days. Deleting a project frees its space once it is removed from the trash. Backpack items count toward the same limit, and deleting one frees its space straight away. `GET /v1/me/quota` returns `used`, `pending`, `limit`, `remaining`, `backpack` (the bytes held in the backpack) and the five largest projects. On first start after this change, the server measures the history size of existing projects from local copies and the R2 inventory.

## Backpack

The editor backpack syncs to the signed-in account. Items are scripts, sprites, costumes and sounds, stored as a body file plus an optional PNG or JPEG thumbnail under `backpack/<random id>/` in R2 (or `data/blobs/` locally), with the per-user list in `data/backpack/`. Blob URLs are unguessable and served sandboxed with `nosniff`; only the owner can list items.

| Route | Purpose |
| --- | --- |
| `GET /v1/me/backpack?type=&offset=&limit=` | Newest first, up to 100 per page, with `total`, `count` and `bytes` |
| `POST /v1/me/backpack` | Multipart `type`, `mime`, `name`, `body`, and optional `thumbnail` with `thumbnailMime`; checked against the storage limit |
| `PATCH /v1/me/backpack/:id` | Rename with `{name}` |
| `DELETE /v1/me/backpack/:id` | Delete one item |
| `POST /v1/me/backpack/delete` | Delete several with `{ids}`; returns `deleted` and `freedBytes` |

A body may be up to 20 MB and a backpack holds up to 2000 items. Account deletion, deleted Rotur accounts and data export all include the backpack.

## Auth flow

1. Client holds a Rotur token from Sign in with Rotur, with the `validators:generate` scope.
2. Client calls `POST https://api.rotur.dev/v2/validators` with `{"key": "<ROTUR_CLIENT_ID>"}` and the token in the `Authorization` header, never the URL. MistWarp's own token can always make validators for its app, without `validators:generate`. Older editors send `ROTUR_APP_KEY` instead. It must be the same Rotur instance the server validates against.
3. Client calls `POST /v1/auth?v=<validator>`; the API validates it against `https://api.rotur.dev/validate`, with the app ID first and then the old key, and returns a 7 day session token (also set as the auth_token cookie). Bearer header and cookie are both accepted. For the app ID, Rotur also refuses anyone the app has banned, or who otherwise can't use MistWarp, with a message for them.

Only MistWarp's own sign-in token can make app-ID validators (rotur/api#85), so the desktop app, which still uses the old sign-in, and older tokens send the old key. Rotur doesn't check the old key against MistWarp's Rotur App, so for those sign-ins Rotur's suspension, adults-only, parental approval and age settings aren't enforced, and a rotur.dev ban only applies once the five-minute ban sync has brought it in. Old-key validators made by another Rotur App's sign-in are refused. The gap closes once the desktop app uses Sign in with Rotur through a loopback redirect, which needs Rotur to accept any port on `http://127.0.0.1`.

A refusal from Rotur is answered with 403 and its `code`, `error`, and for an app ban its `reason` and `until`.


## Milestone notifications

MistWarp sends one-time notifications at 5, 25, 50, 100, 500, and 1,000 likes or followers. Likes are counted separately for each project, comment, roadmap post, space, news post, profile post, and theme. The `like_milestones` collection stores progress independently of the inbox, so clearing notifications, unliking, and re-liking do not reset it.

Project and community reactions check milestones when saved. Profile-post, theme, and follower checks read existing public APIs and keep their state in MistWarp. The client requests a check after a like or follow; opening the inbox also checks the current account's followers. These sampled counts can detect a threshold crossed between visits, but cannot detect a count that rose and fell entirely between checks. Existing counts establish a baseline without sending every earlier milestone. No Rotur or WarpTheme server changes are required.

## Project source access

Paid projects and projects with See inside disabled expose commits, branches, files, and workspace archives only to their owner. Buying a project grants playback, not source-history access. Pull-request inspection follows the same restriction for both projects. The project response exposes `canViewSource` for clients.

Deploy the API and client changes together. Purge previously cached project history and workspace responses from the CDN when deploying this policy; new responses use `private, no-store`. This policy controls MistWarp's inspection endpoints. Playback still sends the data required to run a Scratch project to the browser.
