# beaverdream

beaverdream is a single shared chat room whose only output is the room itself.
Posts are user turns. Each post hands the conversation to a model, and the
model answers by rewriting the page's CSS and markup instead of writing a
reply. The current page is the latest answer; every earlier answer stays in the
version history.

There are no accounts and no per-user threads. A post usually changes the page
for everyone, though the model can also give one visitor a private version. If
someone talks it into making the board hostile, it can. That is the game.

It is a toy. It runs on localhost and calls a local model.

## How it works

- `posts` holds the messages. `site_versions` holds full snapshots of the
  design. The current site is the newest version row; each mutation appends a
  new one. Nothing is overwritten.
- A version has a stylesheet and three HTML regions: `header`, `board`, and
  `footer`. The board contains the literal placeholder `{{posts}}`, which the
  server replaces with the code-owned post list. If the model drops the
  placeholder, the posts are appended instead, so a post can never disappear.
- A version can also carry overrides for individual visitor names. The model
  can restyle the whole page for one person while everyone else keeps the
  normal board. Overrides belong to the version, so undo, restore and heal move
  them with the rest of the page.
- `GET /` never calls the model. It serves the newest version directly as the
  page: no iframe, no sandbox. The model's markup, CSS and scripts are the
  document. The only code-owned markup is the document shell, the live-update
  script, the shared-state tag and the small name link. The model writes the
  post form itself, so it can move it, restyle it, or break it. The system
  prompt gives it the exact method, action and field names. If it breaks the
  form, posting stops working until an admin injects a fix.
- The first visit asks for a display name. It goes into an HttpOnly cookie and
  becomes the author of that visitor's posts. There are no accounts and names
  are not unique. The name page is code-owned and plain; the model never sees
  it. Visit `/identity`, or the **set name** link at the bottom of the board, to
  set it again.
- Post bodies are still escaped. The model is the conduit for sabotage, not raw
  HTML injection through the form.
- The model can also declare a tiny shared state: counters, lists and choices,
  through `set_state_schema`. The page reads it from the `bd-state` JSON tag or
  `GET /x/state`, and visitors change it with `POST /x/state/{key}`. It is the
  model's backend without giving it arbitrary Go. Restores keep the values;
  reset and heal clear them.
- A new post returns immediately. A single worker coalesces posts that arrive
  during a rewrite, so a burst becomes one mutation. Open pages poll
  `/version`: a stylesheet-only change is swapped in place, and a markup change
  reloads after saving the draft and reading position. An override that changes
  only one visitor's markup reloads that visitor and leaves everyone else alone.
- After 20 quiet minutes the board copies the seed forward, keeping the posts
  and clearing shared state and visitor overrides. The next visitor gets the
  plain board and the reveal again. The admin panel can trigger the same heal
  immediately.
- A mutation runs up to `MUTATION_ROUNDS` model rounds. The model edits the
  stylesheet, the regions, shared state and per-visitor overrides, then calls
  `done`. The result is stored as a new version plus a turn in the room, with
  the whole conversation.
- The board is one conversation with one webmaster. Each turn reads back the
  room log: who said what, and what the webmaster said and changed, oldest
  first. It also carries a small private note to itself across turns. Keeping
  the page is a real answer, so a post that does not warrant a change can end
  as a kept turn with no new version.
- When the conversation reaches 128k estimated tokens (four runes per token)
  or a 10k-turn backstop, the server compacts it into one summary message and
  starts a fresh log after it. The webmaster reads the summary first, then the
  recent turns. Summaries survive heal; reset clears them with the room.

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

To use OpenCode Go instead of the mock, copy `.env.example` to `.env`, put the
key in `OPENAI_API_KEY`, and run `task dev.deepseek`. It points at DeepSeek
V4.1 Flash with reasoning turned off.

Open `http://localhost:6931`. The dev task binds the app and the live reload
proxy to all interfaces, so another device on the same network can use
`http://<your-ip>:6931`. The admin panel is exposed too, still behind its
token.

The mock model picks a theme from keywords in your post: `matrix`,
`pink`/`cute`, `brutalist`/`concrete`, or
`chaos`/`sabotage`/`prank` (which writes a script, so you can watch the page
sabotage itself). `curse` or `only me` gives the poster a private page. Anything
else gets a neutral theme. `mocknoop` asks it to leave the site alone;
`mockfail` makes it fail, which is useful for testing error handling.

Production binary: `task build.go` writes `bin/app` with assets and migrations
embedded.

## Using a real model

Point the server at any OpenAI-compatible chat completions endpoint. Copy the
example file, fill in the key, and run the binary:

```sh
cp .env.example .env
$EDITOR .env
./bin/app
```

`.env` is loaded at startup. Variables already set in the shell win, so you can
still override any of them on the command line:

```sh
PORT=6940 DB_PATH=./tmp/real.db DB_MIGRATE_ON_START=true ./bin/app
```

A local llama.cpp server works the same way, just with its own base URL and
model id. `task dev` reads the same variables; override them on the command
line or edit the `env` block in `Taskfile.yml`. The defaults match
`task dev.mock-llm`.

The server sends a `User-Agent` and, for `opencode.ai` URLs, a stable
`x-opencode-session` header. Set `OPENAI_SESSION_ID` to pin that session.
`OPENAI_REASONING_EFFORT` accepts `none`, `minimal`, `low`, `medium`, `high`,
`xhigh` or `max`, and is omitted when empty. Providers that do not support the
parameter reject the request; leave it empty for those.

The model must support tool calls. If it answers in prose without calling a
tool, the site does not change. DeepSeek V4.1 Flash on OpenCode Go is a
reasoning model, so keep the effort at `none` unless you want the extra tokens
and latency.

## Admin

`/admin` is code-owned, never model-generated. It shows:

- the current version and its CSS/regions,
- a form that sends an instruction straight to the model (optionally also
  storing it as a post),
- the visitor overrides on the current version, with a clear button that gives
  one person the normal page back,
- the version list, with a restore button that copies an old version forward as
  a new row,
- a reset button that deletes every post, turn and later version, and clears
  the room, keeping only the original version,
- the newest webmaster turns with their full conversation: system and user
  prompts, assistant reasoning (when the server returns it), tool calls, tool
  results, errors, and duration. Older turns keep a summary so the page stays
  small.

Admin is always token-protected. Set `ADMIN_TOKEN` yourself, or let the server
generate a random one at startup and log the full admin URL. There is no
cookie on purpose: the model's page runs same-origin scripts, and a cookie
would hand those scripts the panel. Every admin request carries the token in
the query string, and the forms carry it as a hidden field.

## Configuration

`.env` in the working directory is loaded at startup, and real environment
variables win over it. `.env` is gitignored; `.env.example` is the template.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HOST` / `PORT` | `localhost` / `6930` | HTTP listen address |
| `DB_PATH` | `./data/sqlite.db` | sqlite file |
| `DB_MIGRATE_ON_START` | `false` | run goose migrations at startup |
| `OPENAI_BASE_URL` | `http://localhost:6700` | chat completions base URL |
| `OPENAI_MODEL` | `mock-model` | model id |
| `OPENAI_API_KEY` | empty | provider key, usually from `.env` |
| `OPENAI_SESSION_ID` | generated | `x-opencode-session` value for `opencode.ai` |
| `OPENAI_REASONING_EFFORT` | empty | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |
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
web/pages            templ page for admin
web/assets           committed static assets
```

The scaffold still carries the template's tailwind, Lit components, Dockerfile
and live-reload setup. They are convenient and harmless, not requirements.
