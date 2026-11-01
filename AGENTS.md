# AGENTS.md

mistwarp-api is the OSL backend behind https://api.mistwarp.org/v1, for the MistWarp community site and editor (scratch-gui). It needs OSL 3.0. Community data is in SQLite (`data/community.db`), other state is JSON under `data/`, and project files are in Cloudflare R2.

## Where code goes

- `main.osl` only composes the app: it imports every domain, runs startup and migrations, registers routes and starts the server. Don't put features in it.
- `src/<domain>/` holds everything else (config, core, http, auth, storage, projects, history, git, games, realtime, community, admin, classroom, rotur). The [Backend Overview](https://mistwarp.org/docs/backend/overview/) describes what each owns.
- Each file has an `export {...}` block after its imports, listing what other directories may use; anything not listed is private. Keep exports minimal.
- A domain registers its routes in its own `routes.osl` (`registerXRoutes(api)`). Global caches live next to the code that uses them. Tests sit beside their source as `<name>.test.osl`.

## The /v1 contract

The editor and site depend on every path, method, middleware order, body limit and response shape. Don't change them unless the task is to. `python3 scripts/route_dump.py .` prints every route with its middleware, so diff its output before and after any routing change. `python3 scripts/e2e_smoke.py <old binary> <new binary>` runs an offline end-to-end pass against two builds and diffs their responses.

## OSL traps

- String `.len` counts characters. Use `.toBytes().len` for anything measured in bytes: stored sizes, quotas, body caps, R2 objects.
- Arrays are shared on assignment and when passed to a function. `.assert(array)`, `?? []` and function returns copy, so after changing a copied array, write it back (`obj.field = list`).
- `result`, `set`, `error`, `option`, `xml`, `thread`, `unknown` and `channel` are reserved type names, so don't use them as variable names.
- The runtime throws where older versions returned defaults. Read request values as `(c.query("x") ?? "")`, parse bodies with `requestJSON(c)`, discard unused calls with `void`, and end fallible reads with `.unwrapOr(<default>)`.

## Checks

`osl test`, `osl lsp check main.osl`, `osl compile main.osl -o main`, and `python3 scripts/check_conventions.py`. The last one rejects code comments and em dashes in the lines you add. CI (`.github/workflows/test.yml`) runs all of them plus the smoke test, with OSL pinned to a commit.

## Working here

- The default branch is `main`. Open PRs with `gh pr create -R MistWarp/api --base main`.
- Never deploy, restart or SSH to the production server unless explicitly asked.
- New behaviour comes with tests. A refactor keeps the route dump and the smoke diff identical.
- Never add `Co-Authored-By` or any AI or tool attribution to commits or PRs.
- READMEs are overviews. Documentation goes in the Backend section of MistWarp/docs (https://mistwarp.org/docs/backend/overview/).
