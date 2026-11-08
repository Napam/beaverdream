# Configuration

Every setting is an environment variable. Server settings come from
`lib/env/env.go`; the model limits under [Limits](#limits) are read at startup
by `lib/webmaster`. `.env.example` lists every one with its default. The
Taskfile sets a few more for `task` runs.

## Where values come from

- The server reads only the process environment. A binary you start by hand
  does not read `.env`.
- `task` loads `.env` for every task. Values in the Taskfile's `env:` block
  override `.env`. Shell variables override both.
- `.env` is gitignored. `.env.example` is the template: `cp .env.example .env`
  makes your copy. Every line in the template is optional.

## Running the binary

```sh
task build.go                   # writes ./bin/app with assets and migrations embedded
set -a && . ./.env && set +a    # export the keys in .env into this shell
./bin/app
```

The binary listens on `localhost` unless `HOST` is set. For LAN play, run
`HOST=0.0.0.0 ./bin/app`. The registry and each world file are created and
migrated automatically on first open.

## Ports

- `HOST` (default `localhost`): the address both listeners bind to. `task dev`
  sets `0.0.0.0`, so other devices can connect.
- `APP_PORT` (default `6930`): the management plane. Login, account, API
  tokens, invites, the lobby and every admin page.
- `PLAY_PORT` (default `6932`): the worlds. Each world serves under
  `/worlds/{slug}`, including its live stream, posts, state and journal. It
  never serves login, invites or admin.
- `TLS_CERT_FILE`, `TLS_KEY_FILE` (default unset): serve HTTPS on both
  listeners. Set both or neither. Browsers use HTTP/2 only over TLS, so every
  open tab shares one connection. Without them the server speaks HTTP/1.1 and
  accepts h2c from a proxy that terminates TLS.

Cookies ignore ports. On `localhost` the app and play listeners share a host,
so a page script can send a request to the app port with the app cookie
attached. The app's CSRF token and its refusal to be framed are what keep that
from changing state. Strong isolation needs two hosts, which for a public
deployment means subdomains of one registrable domain over HTTPS, such as
`example.com` and `play.example.com`. Test that shape with both URLs set before
you go public.

## Public URLs

- `APP_URL` (default empty): public origin of the app, such as
  `https://example.com`. Trailing slashes are trimmed.
- `PLAY_URL` (default empty): public origin of the worlds, such as
  `https://play.example.com`. Trailing slashes are trimmed.

Leave them empty on a LAN: the server builds the play link from the request
host and `PLAY_PORT`. Set both to `https://` origins behind a proxy, or set
`TLS_CERT_FILE` and `TLS_KEY_FILE`. Either turns on secure cookie mode: both
cookies are named `__Host-beaverdream_app` and `__Host-beaverdream_play`, and
the server reads only the prefixed name. One `https` URL and one `http` or
empty is refused at startup, so set both. A LAN keeps the plain cookie names
and no `Secure`.

## Storage

- `REGISTRY_PATH` (default `./data/registry.db`): the registry SQLite file. It
  holds identities, logins, sessions, invites, roles, usage and the `worlds`
  table.
- `WORLDS_DIR` (default `./data/worlds`): one file per world, named by the
  world's immutable random id: `<WORLDS_DIR>/<id>.db`. The slug never reaches
  the file path.
- Migrations run when the registry opens and each time a world opens. A world
  that sat idle catches up the next time it opens. There is no
  `DB_MIGRATE_ON_START` switch and no single `DB_PATH`.
- `task db.migrate.up` (also `task db.migrate`) migrates the registry only.
  World files migrate on open.
  It reads `REGISTRY_PATH` from `.env` or the shell, and
  `REGISTRY_MIGRATIONS_DIR` (default `./db/registry/migrations`) if the
  migration folder moves.
- `task db.migrate.down` rolls the registry back to the auth migration. In the
  current split that drops `000002_data.sql`, so invites, worlds, handoffs and
  usage go while accounts, logins and sessions stay. `task db.migrate.up`
  reapplies it empty. It deletes the `worlds` rows too, so the retained files
  under `WORLDS_DIR` become orphaned: the app no longer lists or opens them.
  Delete `WORLDS_DIR` by hand for a full wipe. Stop the app first, and point
  `REGISTRY_PATH` at the file you mean.

Use `VACUUM INTO` or the SQLite backup API to back up the registry and world
files. Never copy a live `.db` file: the WAL may hold recent writes.

## Accounts, sessions and worlds

- `ADMIN_PASSWORD` (default empty): the bootstrap admin password. On every
  start the server writes an argon2id hash into the admin identity, clears its
  disabled state, grants `ADMIN` if missing, and revokes the admin's sessions
  when the password changed. Empty generates a random password and logs it.
  Change the variable and restart; the UI does not change this login.
- `APP_SESSION_TTL` (default `168h`): how long an app session lasts.
- `PLAY_SESSION_TTL` (default `0`): how long a play session lasts. `0` means no
  expiry.
- `HANDOFF_TTL` (default `1m`): lifetime of the one-time code that moves a
  browser from the app port to a world.
- `MODEL_CONCURRENCY` (default `1`): model turns running at once across every
  loaded world.
- `WORLD_IDLE_TIMEOUT` (default `15m`): close a world with no subscribers, no
  queued posts and no running turn. `0` never unloads one.

## Daily token budgets

- `DAILY_TOKENS_IDENTITY` (default `1000000`): tokens one identity may spend
  in a UTC day.
- `DAILY_TOKENS_GLOBAL` (default `5000000`): tokens all identities may spend
  in a UTC day.

`0` means unlimited for either. A post over budget gets `429` with reason
`budget` and is not stored. A turn already running finishes, so the day can
overshoot by one turn per running world. Admin turns never count and are never
refused. A single turn can reach about 200k prompt tokens before compaction, so
a per-identity limit below that allows about one turn a day. Tune from real
usage.

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

## Turn budget

- `TURN_MAX_ROUNDS` (default `25`): tool rounds one turn may use. Zero or a
  negative value falls back to 25.
- `TURN_TIMEOUT` (default `1h`): backstop for one whole turn.

## Limits

These depend on the model and provider, so they are env vars. Each is read once
at startup. Every other cap is a constant in `lib/webmaster/constants.go` or
`cmd/serve/handlers`, because changing it changes the game or the code, not the
deployment.

- `LLM_MAX_OUTPUT_TOKENS` (default `262144`): one completion. It bounds a whole
  `set_site`; a larger page grows through edits.
- `COMPACT_AT_PROMPT_TOKENS` (default `200000`): the prompt budget, transcript
  plus page, in estimated tokens at four runes each. Past it the history is
  compacted. The room log backstop is four times this, in runes.
- `MAX_PAGE_BYTES` (default `524288`): css and html together, in bytes. Keep
  `MAX_PAGE_BYTES/4` below `COMPACT_AT_PROMPT_TOKENS`, or a full page fills the
  budget and compaction stops. The server logs a warning at startup if it does.
- `LLM_IDLE_TIMEOUT` (default: 3m before the first event and while thinking, 2m
  once a tool call starts): how long a stream may go quiet before the round is
  retried. Setting it uses one value for every phase. Zero turns the check off.

A full 512 KiB page is about 131k tokens at the four-runes-per-token estimate.
Check that the model's context holds `COMPACT_AT_PROMPT_TOKENS` plus the system
prompt and `LLM_MAX_OUTPUT_TOKENS`.

## Admin and API tokens

- The admin is an identity with the `ADMIN` role, not a separate password
  cookie. Its username is `admin`. Log in on the app port. Admin pages are
  `/admin` and, per world, `/admin/worlds/{slug}/...`.
- API tokens are created in the account page. Send one only as
  `Authorization: Bearer <token>` on the app port. It is refused on the play
  port. The old `X-Admin-Token` header and password-as-token are gone.
- Every state-changing app POST that uses a cookie needs the `csrf` form field.
  Bearer tokens skip it. See [admin.md](admin.md).

## Dev only

These come from the Taskfile. The server does not need them.

- `LIVE_RELOAD_PROXY_HOST` (`localhost`), `LIVE_RELOAD_PROXY_PORT` (`6931`) and
  `LIVE_RELOAD_PROXY_BIND` (`0.0.0.0`): the templ live reload proxy. It targets
  the app port. The server only logs the address, and the Taskfile reads the
  bind address.

## Removed

The old single-world settings are gone, with no aliases: `PORT`, `ADMIN_PORT`,
`ADMIN_URL`, `DB_PATH`, `DB_MIGRATE_ON_START`, `AUTH`, `DAILY_TOKENS_MEMBER`
and `X-Admin-Token`. The visitor and name cookies are replaced by identities.
