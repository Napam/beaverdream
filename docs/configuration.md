# Configuration

Every setting is an environment variable. The defaults below come from
`lib/env/env.go`. The Taskfile sets a few more for `task` runs.

## Where values come from

- The server reads only the process environment. A binary you start by hand does
  not read `.env`.
- `task` loads `.env` for every task. Values in the Taskfile's `env:` block
  override `.env`. Shell variables override both.
- `.env` is gitignored. `.env.example` is the template: `cp .env.example .env`
  makes your copy. Every line in the template is optional.

## Running the binary

```sh
task build.go                   # writes ./bin/app with assets and migrations embedded
set -a && . ./.env && set +a    # export the keys in .env into this shell
DB_MIGRATE_ON_START=true ./bin/app
```

- Set `DB_MIGRATE_ON_START=true` so the tables exist. Without it a binary run
  finds no tables. `.env.example` sets it, but your `.env` may not.
- The binary listens on `localhost` unless `HOST` is set. For LAN play, run
  `HOST=0.0.0.0 ./bin/app`.
- `task db.migrate` also creates the tables. It uses `DB_PATH` from `.env` or
  the shell, and `./data/sqlite.db` when that is unset.

## Server

- `HOST` (default `localhost`): the address both listeners bind to. `task dev`
  sets `0.0.0.0`, so other devices can connect.
- `PORT` (default `6930`): the board's port.
- `ADMIN_PORT` (default: `PORT + 1`, skipping `LIVE_RELOAD_PROXY_PORT`): the port
  the admin panel runs on. It must differ from `PORT`. It is `6932` under
  `task dev` and `6931` for a bare binary.
- `TLS_CERT_FILE`, `TLS_KEY_FILE` (default unset): serve HTTPS. Set both or
  neither. Browsers use HTTP/2 only over TLS, so every open tab shares one
  connection. Without them the server speaks HTTP/1.1 and accepts h2c from a
  proxy that terminates TLS.

## Database

- `DB_PATH` (default `./data/sqlite.db`): the SQLite file. A relative path starts
  from the working directory.
- `DB_MIGRATE_ON_START` (default `false`, `true` in the Taskfile and
  `.env.example`): run the goose migrations at startup. Any value Go's
  `strconv.ParseBool` accepts works, such as `1`.

## Model

- `OPENAI_BASE_URL` (default `http://localhost:6701`): base URL of an
  OpenAI-compatible chat completions API. The default points at the mock.
- `OPENAI_MODEL` (default `mock-model`): the model id sent with each request.
- `OPENAI_API_KEY` (default empty): sent as the bearer key. Empty sends no
  `Authorization` header. Surrounding spaces are trimmed. Logs show only whether
  it is set.
- `OPENCODE_SESSION_ID` (default unset): pins the session id sent as a request
  header, for gateways that route by session. Leave it unset unless your gateway
  asks for one. When unset, the server makes a random id per start, but only for
  base URLs on one specific gateway.
- `OPENAI_REASONING_EFFORT` (default empty, left out of the request): reasoning
  effort for models that support it. The SDK accepts `none`, `minimal`, `low`,
  `medium`, `high`, `xhigh` and `max`. Leave it empty for a model that rejects
  the field.

## Mutation budget

- `MUTATION_ROUNDS` (default `3`): tool rounds one turn may use. Zero or a
  negative value falls back to 3.

## Admin

- `ADMIN_PASSWORD` (default: random, generated and logged at each start): the admin
  password. It also signs the login cookie. See [admin.md](admin.md).
- `ADMIN_URL` (default unset): the public URL of the admin panel, such as
  `https://admin.example.com`. When set, the board's `/admin` redirect goes there
  instead of to `ADMIN_PORT` on the request's host. A trailing `/` is trimmed.

## Dev only

These come from the Taskfile. The server does not need them.

- `LIVE_RELOAD_PROXY_HOST` (`localhost`) and `LIVE_RELOAD_PROXY_PORT` (`6931`):
  the templ live reload proxy. The server logs the address, and the port feeds
  the admin port default.
- `LIVE_RELOAD_PROXY_BIND` (`0.0.0.0`): the address the proxy binds. Only the
  Taskfile reads it.
