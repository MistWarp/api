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
| APP_URL | https://mwapi.mistium.com | Public URL of this API |
| ROTUR_APP_KEY | mistwarp | Rotur validator app key |
| COMMERCE_SERVICE_KEY | | Key registered for `mistwarp` in Rotur's `COMMERCE_SERVICE_KEYS` |
| R2_ENDPOINT | | https://accountid.r2.cloudflarestorage.com |
| R2_BUCKET | mistwarp | R2 bucket name |
| R2_ACCESS_KEY_ID | | R2 access key |
| R2_SECRET_ACCESS_KEY | | R2 secret key |
| R2_PUBLIC_BASE | | Public custom domain for the bucket |
| GITEA_URL | https://git.rotur.dev | Optional Rotur Git instance |
| GITEA_ADMIN_TOKEN | | Optional Rotur Git integration token |
| EDITOR_ORIGIN | https://warp.mistium.com | Editor origin for CORS |
| ADMIN_USERS | mist | Comma separated admin usernames |
| REALTIME_URL | wss://mwapi.mistium.com/v1/connect | Public multiplayer WebSocket endpoint |

The multiplayer WebSocket runs inside this API process at `/v1/connect`. It
uses the same listener, domain, and deployment as the HTTP API.

## Deployment layout

The site and editor are one scratch-gui build (community pages live in
`scratch-gui/src/community`), served on the frontend domain:

| Path | Serves |
| --- | --- |
| / | community app (webpack `community` entry -> index.html) |
| /editor | scratch-gui editor |
| /embed.html | scratch-gui embed player (project pages iframe this) |
| /project/*, /explore, /users/*, /settings | community app (client routing) |

This API runs on a SEPARATE domain, `mwapi.mistium.com`. The frontend calls it
at `https://mwapi.mistium.com/api` directly. In dev, leave the API base unset and
webpack-dev-server proxies `/api` to `http://localhost:5627`. Auth is
Bearer-token based (the session token returned by `/api/auth`), so a
cross-domain API works without shared cookies. CORS echoes the request Origin
with credentials, so any frontend origin is accepted.

R2 bucket only needs public GET through `R2_PUBLIC_BASE`, and the public domain must expose the bucket's `assets/` prefix and allow CORS GET requests from the MistWarp frontend. Project metadata keeps the API `/blobs/assets` base. That endpoint serves a local asset when its file exists under `data/blobs/assets/`; otherwise it redirects to `R2_PUBLIC_BASE`. All writes go through this server. With no R2 configured, the server falls back to local disk (`data/blobs/`, served at `/blobs`) so it runs locally with zero setup.

## Upload pipeline

The editor POSTs a sparse sb3 to `POST /api/projects/:id/upload` (multipart, fields `project` and optional `thumbnail`). The server extracts at most 256 MiB of `project.json`, validates it incrementally, requires asset filenames to match their content, and limits every asset to 10 MiB and all assets to 50 MiB. The JSON is accepted only when its gzip representation is at most 20 MiB.

### Commit inspection

The read-only commit endpoints inspect the stored `.mwp` on the server. Clients do not need to download the complete workspace to render history or browse an old revision.

- `GET /api/projects/:id/commits/:sha` returns commit metadata, its first parent, and changed-file records. Each record has `path`, `status`, `oldOid`, `newOid`, `oldSize`, and `newSize`. Small text changes also include `oldData` and `newData` as base64, capped at 1 MiB per file and 8 MiB per response, so clients can render ordinary diffs without a request per blob. Root commits report every file as added. `legacy: true` means the change includes an old `project.sb3`; clients that need the expanded Fractch diff should use their legacy conversion fallback.
- `GET /api/projects/:id/commits/:sha/tree` returns `commit` and a flat `files` array. Each file has `path`, `oid`, `mode`, `size`, and `binary`.
- `GET /api/projects/:id/commits/:sha/file?path=<path>` returns one file record and `content`, encoded as base64 by JSON.
- `GET /api/projects/:id/commits/:sha/co-authors` returns the Rotur users credited on that commit. The older `/collaborators` path remains an alias.
- `PATCH /api/projects/:id/commits/:sha` accepts `{coAuthors: string[]}` with at most 25 existing Rotur usernames. `{collaborators: string[]}` remains an input alias. It replaces the commit's co-author metadata without rewriting Git objects or changing any SHA. Responses expose canonical `coAuthors` records as `{username, userId}` and a compatibility `collaborators` alias. Only the project owner, a maintainer, or an administrator may patch commit credits.

These routes use the same see-inside policy as workspace downloads. The server caches materialized workspace layers and computed JSON by the ordered content-addressed layer keys. Public, free projects may be cached by the CDN for one hour; private, unlisted, and paid responses use `private, no-store`. Stable ETags let browsers revalidate without reparsing Git objects. The local inspection cache keeps at most 256 files or 2 GiB and removes entries older than 24 hours.

Before extraction, the API rejects archives with unsafe paths, duplicate entries, symlinks, unsupported compression methods, or more entries and expanded bytes than the endpoint allows. MistWarp history archives are capped at 128 MiB compressed and 20,000 entries. Their expanded-byte ceiling matches the account's maximum project size, including its tier-specific asset allowance. The API reads each history entry through that ceiling before storing it, so forged ZIP metadata cannot bypass the limit. Upload attempts count toward the weekly byte budget even when archive validation fails.

- `assets/<md5ext>`: content addressed, shared across all projects and remixes, uploaded once ever
- `projects/<id>/project.json`: the gzip-encoded playable snapshot
- `projects/<id>/thumb.png`

Project JSON is staged on local disk before the upload request returns. The API serves that staged copy immediately, then the background flush loop writes at most one R2 snapshot per project every 24 hours. Git carries every save. `data/assets-index.json` tracks known assets so duplicates are never re-uploaded.

The first editor save uploads a compact MWP archive containing the Git repository without a duplicate worktree. Later saves compare the local HEAD with the server HEAD and send only new Git objects plus updated refs. The API stores those archives as content-addressed layers, so remixes share their parent's history instead of copying it. It compacts a chain after eight layers. If the user saves without making a commit, the editor sends a full archive with the worktree so uncommitted changes are not lost.

For a delta whose `baseHead` still matches the stored head, the API removes loose Git objects already present in the materialized base before storing the new layer. It retains the delta manifest and refs, verifies the combined archive, and stitches only the new commits and graph nodes ahead of `baseHead` onto the inherited metadata. A final locked head comparison rejects concurrent history changes before project metadata is saved.

## Auth flow

1. Client holds a rotur token (rotur-sdk login).
2. Client fetches `https://api.rotur.dev/generate_validator?key=<ROTUR_APP_KEY>&auth=<token>` (must be the same rotur instance the server validates against).
3. Client calls `POST /api/auth?v=<validator>`; the API validates it against `https://api.rotur.dev/validate` and returns a 7 day session token (also set as the auth_token cookie). Bearer header and cookie are both accepted.
