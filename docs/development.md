# Development

## Setup

Run `task init` once after you clone. It installs the tools pinned in
`mise.toml`, installs the js dependencies in `web/`, and makes `.hooks/` the git
hooks directory.

## Tasks

- `task init`: install tools, js dependencies and the git hooks.
- `task dev`: run the server with live reload and the watchers for CSS, js and
  SQL. The app is on `6930`, the worlds on `6932`, and the live reload proxy on
  `6931`. Open the proxy to reload the page after each change.
- `task dev.mock-llm`: run the fake model on `6701`. Leave it running in its own
  terminal.
- `task check`: regenerate templ and sqlc code, then run the Go build,
  golangci-lint, templ format check, prettier, eslint, the TypeScript check and
  a bundle build.
- `task fix`: auto-fix formatting and lint in Go and in `web/`.
- `task build.go`: write `bin/app` with the assets and migrations embedded.
- `task db.migrate.up` (also `task db.migrate`): run the goose migrations on
  the registry (`REGISTRY_PATH`, default `./data/registry.db`). World files
  migrate when they open.
- `task db.migrate.down`: roll the registry back to the auth migration
  (currently drops the data migration) while keeping accounts and sessions.
  World files stay; stop the app first.
- `task gen.templ` and `task gen.db`: regenerate code after you change a templ
  file or a SQL query.
- `task build.docker` and `task build.docker.run`: build the Docker image, or
  build it and run it. See [docker.md](docker.md).

## Logs

The server writes one JSON object per line to stdout, using `slog`'s JSON
handler. `task dev` pipes everything through `hl -P` (no pager), so you read
formatted logs without typing the pipe. `hl`, pinned in `mise.toml`, must be on
your PATH. Without it, `task dev` prints the raw JSON.

To read a built binary, pipe it yourself. The `2>&1` sends build errors and
stderr through the pipe too:

    ./bin/app 2>&1 | hl

`hl -l warn` shows only warnings and errors. `hl --help` lists the other flags.

## Tests

`go test ./...` runs the Go tests. They use `lib/mockllm` in place of a real
model, so they need no key and no network. Tests create a temporary registry and
temporary world files, so they never touch `data/`. Run them before you push.
There is no `task test` target.

## Git hook

`task init` links `.hooks/` as git's hooks directory. The `pre-push` hook runs
`task check`, and a failing check stops the push. The hook does not run
`go test ./...`.

## Mock model

`task dev.mock-llm` answers with canned replies. Keywords in the prompt pick
the reply:

- `matrix`, `pink` or `cute`, `brutalist` or `concrete`, and `chaos`,
  `sabotage` or `prank`: themes. The last group writes a script that sabotages
  the page.
- `mocknoop`: leaves the site alone.
- `mockfail`: answers with HTTP 500.
- `mockedit`, `mockpatch`, `mockbadedit`: a small edit, a patch to the default
  theme's accent, and an edit that misses. The last one shows the retry.
- `mockprose`: replies in words with no tool call.
- `mockgame`: ships a small game script.
- `mockwallet`: declares visitor counters and a recipe, and ships a purse page.
- `mockreply` and `mocksay`: reply in the room, with or without a line first.

The mock takes a `-delay` flag (default `15ms`) that pauses before each streamed
chunk. Run it with `go run ./cmd/mockllm -addr localhost:6701 -delay 0` to skip
the pause.

## Layout

- `cmd/serve`: the server. It holds `main.go` and both listeners.
  `cmd/serve/handlers` holds the HTTP handlers for the app, the worlds, the live
  stream and the admin pages.
- `cmd/mockllm`: the fake OpenAI-compatible model server.
- `lib/registry`: the registry database, identities, logins, sessions, invites,
  roles, usage and the world list.
- `lib/worlds`: opens, migrates, unloads and deletes world files.
- `lib/webmaster`: the model loop, its tools, validation and site rendering.
- `lib/mockllm`: the deterministic model, used by the tests and by
  `task dev.mock-llm`.
- `lib/env`: reads the environment into one struct. See
  [configuration.md](configuration.md).
- `lib/hashfs`: content-hashed static files, adapted from benbjohnson/hashfs.
- `db/`: one folder per database. `world/` holds the world migrations, the
  sqlc queries and config, and the generated Go; `registry/` holds the registry
  migrations.
- `web/`: the admin templ pages in `pages/`, the js and ts runtime, tailwind
  input, and `static/` for built assets. `web/lib` is leftover scaffold that no
  page loads.
- `docs/`: this documentation.
- `scripts/`: `dev-run.sh` rebuilds and restarts the dev server.
- `bin/`: built binaries such as `app` and `dev-app`. Git ignores them.
- `.hooks/`: the git hooks.
- `files_embed.go` and `files_noembed.go`: `go:embed` for `web/static` and
  `db/world/migrations`. The `noembed` build tag, which `dev-run.sh` uses, reads
  those files from disk instead.
