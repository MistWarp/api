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
| APP_URL | https://mwapi.mistium.com | Public URL of this API |
| ROTUR_APP_KEY | mistwarp | Rotur validator app key |
| R2_ENDPOINT | | https://accountid.r2.cloudflarestorage.com |
| R2_BUCKET | mistwarp | R2 bucket name |
| R2_ACCESS_KEY_ID | | R2 access key |
| R2_SECRET_ACCESS_KEY | | R2 secret key |
| R2_PUBLIC_BASE | | Public custom domain for the bucket |
| GITEA_URL | https://git.rotur.dev | Optional Rotur Git instance |
| GITEA_ADMIN_TOKEN | | Optional Rotur Git integration token |
| EDITOR_ORIGIN | https://warp.mistium.com | Editor origin for CORS |
| ADMIN_USERS | mist | Comma separated admin usernames |

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

R2 bucket only needs public GET (through R2_PUBLIC_BASE). All writes go through this server. With no R2 configured, the server falls back to local disk (`data/blobs/`, served at `/blobs`) so it runs locally with zero setup.
## Upload pipeline

The editor POSTs a sparse sb3 to `POST /api/projects/:id/upload` (multipart, fields `project` and optional `thumbnail`). The server extracts at most 1 GiB of `project.json`, validates it incrementally, requires asset filenames to match their content, and limits every asset to 10 MiB and all assets to 50 MiB. The JSON is accepted only when its gzip representation is at most 20 MiB.

- `assets/<md5ext>`: content addressed, shared across all projects and remixes, uploaded once ever
- `projects/<id>/project.json`: the gzip-encoded playable snapshot
- `projects/<id>/thumb.png`

Project JSON is staged on local disk before the upload request returns. The API serves that staged copy immediately, then the background flush loop writes at most one R2 snapshot per project every 24 hours. Git carries every save. `data/assets-index.json` tracks which assets R2 already has so duplicates are never re-uploaded.

## Auth flow

1. Client holds a rotur token (rotur-sdk login).
2. Client fetches `https://api.rotur.dev/generate_validator?key=<ROTUR_APP_KEY>&auth=<token>` (must be the same rotur instance the server validates against).
3. Client calls `POST /api/auth?v=<validator>`; the API validates it against `https://api.rotur.dev/validate` and returns a 7 day session token (also set as the auth_token cookie). Bearer header and cookie are both accepted.
