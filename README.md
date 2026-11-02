# beaverdream

beaverdream is a single shared chat room whose only output is the room itself.
Posts are user turns. Each post hands the conversation to a model, and the
model answers by rewriting the page's CSS and markup instead of writing a
reply. The current page is the latest answer; every earlier answer stays in the
version history.

There are no accounts and no per-user threads. One post changes the page for
everyone, and if someone talks the model into making the board hostile, it can.
That is the game.

It is a toy. It runs on localhost and calls a local model.

## How it works

- `posts` holds the messages. `site_versions` holds full snapshots of the
  design. The current site is the newest version row; each mutation appends a
  new one. Nothing is overwritten.
- A version has a stylesheet, a note, and three HTML regions: `header`, `board`,
  and `footer`. The board contains the literal placeholder `{{posts}}`, which
  the server replaces with the code-owned post list. If the model drops the
  placeholder, the posts are appended instead, so a post can never disappear.
- `GET /` never calls the model. It serves the newest version directly as the
  page: no iframe, no sandbox. The model's markup, CSS and scripts are the
  document. The only fixed markup is the note, a small post form, and links to
  `/plain` and `/admin`, appended after the model's footer. The model can style
  or delete even those. That is the point.
- Post bodies are still escaped. The model is the conduit for sabotage, not raw
  HTML injection through the form.
- A new post returns immediately. The mutation runs in the background, and open
  pages poll `/version` and reload when it changes.
- A mutation runs up to `MUTATION_ROUNDS` model rounds with four tools
  (`set_css`, `set_region`, `set_note`, `done`). The result is stored as a new
  version plus a `mutation_runs` row holding the whole conversation.

**Do not expose this to the open internet.** Model-generated scripts run in
every visitor's browser with the board's origin. That is the game. The admin
panel is token-protected and hand-written, but the board itself is hostile by
design.

## Quickstart

Requires [mise](https://mise.jdx.dev) and [bun](https://bun.sh) (installed by
mise).

```sh
task init          # install tools, js deps, git hooks
task dev.mock-llm  # terminal 1: deterministic fake model on :6701
task dev           # terminal 2: server on :6930, live reload proxy on :6931
```

To use OpenCode Go instead of the mock, put the key in `data/openai.key` and
run `task dev.muse`. It points at Muse Spark with minimal reasoning.

Open `http://localhost:6931`. The mock model picks a theme from keywords in
your post: `matrix`, `pink`/`cute`, `brutalist`/`concrete`, or
`chaos`/`sabotage`/`prank` (which writes a script, so you can watch the page
sabotage itself). Anything else gets a neutral theme. `mocknoop` asks it to
leave the site alone; `mockfail` makes it fail, which is useful for testing
error handling.

Production binary: `task build.go` writes `bin/app` with assets and migrations
embedded.

## Using a real model

Point the server at any OpenAI-compatible chat completions endpoint. OpenCode
Go with Muse Spark:

```sh
HOST=localhost PORT=6930 \
DB_PATH=./tmp/real.db DB_MIGRATE_ON_START=true \
OPENAI_BASE_URL=https://opencode.ai/inference/go/openai/v1 \
OPENAI_MODEL=muse-spark-1.3-contributor \
OPENAI_API_KEY_FILE=./data/openai.key \
OPENAI_REASONING_EFFORT=minimal \
MUTATION_ROUNDS=5 \
./bin/app
```

A local llama.cpp server works the same way, just with its own base URL and
model id. `task dev` reads the same variables; override them on the command
line or edit the `env` block in `Taskfile.yml`. The defaults match
`task dev.mock-llm`.

`OPENAI_API_KEY` takes precedence over `OPENAI_API_KEY_FILE`. The server sends a
`User-Agent` and, for `opencode.ai` URLs, a stable `x-opencode-session` header.
Set `OPENAI_SESSION_ID` to pin that session. `OPENAI_REASONING_EFFORT` accepts
`minimal`, `low`, `medium`, `high` or `xhigh` and is omitted when empty.

The model must support tool calls. If it answers in prose without calling a
tool, the site does not change. Muse Spark trains on request data, so the
workspace must allow that in the OpenCode Console privacy settings.

## Admin

`/admin` is code-owned, never model-generated. It shows:

- the current version and its CSS/regions,
- a form that sends an instruction straight to the model (optionally also
  storing it as a post),
- the version list, with a restore button that copies an old version forward as
  a new row,
- every mutation run with its full conversation: system and user prompts,
  assistant reasoning (when the server returns it), tool calls, tool results,
  errors, and duration.

Admin is always token-protected. Set `ADMIN_TOKEN` yourself, or let the server
generate a random one at startup and log the full admin URL. There is no
cookie on purpose: the model's page runs same-origin scripts, and a cookie
would hand those scripts the panel. Every admin request carries the token in
the query string, and the forms carry it as a hidden field.

`/plain` is the escape hatch: a code-owned list of posts with no model output at
all.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `HOST` / `PORT` | `localhost` / `6930` | HTTP listen address |
| `DB_PATH` | `./data/sqlite.db` | sqlite file |
| `DB_MIGRATE_ON_START` | `false` | run goose migrations at startup |
| `OPENAI_BASE_URL` | `http://localhost:6700` | chat completions base URL |
| `OPENAI_MODEL` | `mock-model` | model id |
| `OPENAI_API_KEY` | empty | provider key; wins over the file |
| `OPENAI_API_KEY_FILE` | empty | file to read the key from |
| `OPENAI_SESSION_ID` | generated | `x-opencode-session` value for `opencode.ai` |
| `OPENAI_REASONING_EFFORT` | empty | `minimal`, `low`, `medium`, `high`, `xhigh` |
| `ADMIN_TOKEN` | generated | required for `/admin`; generated and logged when empty |
| `MAX_MUTATIONS_PER_DAY` | `500` | cost cap, `0` disables |
| `MUTATION_ROUNDS` | `3` | tool rounds per mutation |

## Development

- `task check` regenerates templ/sqlc and runs go build, golangci-lint, templ
  fmt, prettier, eslint, tsc, and a bundle build.
- `task fix` auto-fixes formatting and lint issues.
- `go test ./...` runs the unit and handler tests against `lib/mockllm`, so no
  model or network is needed.
- `task gen.db` and `task gen.templ` regenerate code after changing queries or
  templates.

## Project structure

```
cmd/serve            server, routes, sqlite setup
cmd/mockllm          fake OpenAI-compatible server
cmd/serve/handlers   board pages, post creation, admin, restore
lib/webmaster        mutation loop, tools, validation, site rendering
lib/mockllm          deterministic model used by tests and task dev.mock-llm
db/migrations        goose schema
db/queries           sqlc queries
lib/webmaster/site   assembles the model-owned page and the fixed controls
web/pages            templ pages for the plain view and admin
web/assets           committed static assets
```

The scaffold still carries the template's tailwind, Lit components, Dockerfile
and live-reload setup. They are convenient and harmless, not requirements.
