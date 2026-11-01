# mistwarp-api

The OSL backend for the MistWarp community platform. It signs people in with Rotur, stores projects and their history in Cloudflare R2, and serves everything the community site and editor need: comments, spaces, notifications, MistWarp Classroom, game data and pull requests between MistWarp forks.

It runs at `https://api.mistwarp.org/v1`. The editor and community site are [scratch-gui](https://github.com/MistWarp/scratch-gui).

## Getting started

You need OSL 3.0.

```sh
cp .env.example .env
osl run main.osl
osl test
```

The code is split by domain under `src/`, and `main.osl` composes them.

## Documentation

The [Backend section of the MistWarp docs](https://mistwarp.org/docs/backend/overview) covers the source layout, configuration, sign-in, safety and moderation, payments, storage, and community features.
