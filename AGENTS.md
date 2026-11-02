# Working on beaverdream

beaverdream is a message board whose site is rewritten by a model on each post.
Go + templ + chi + sqlite (sqlc, goose), with a small TS frontend. It is a toy:
prefer simple, readable code over production hardening.

## Ground rules

- Use `tmp/` for scratch files and test output. It is gitignored.
- Read `Taskfile.yml` before running commands; it is the source of truth.
- After changes run the matching check:
  - go code: `task check.go`
  - web code: `task check.web`
  - both: `task check`
  - auto-fix: `task fix` (or `task fix.go` / `task fix.web`)
- `go test ./...` must pass. Tests use `lib/mockllm`, never a real model.

## Shape of the app

- `posts` is the truth. `site_versions` is the render, append-only; the current
  site is `MAX(id)`. `mutation_runs` stores the full conversation per mutation
  so `/admin` can replay it.
- `lib/webmaster` owns the model contract: `site.go` (types, base CSS, rendering),
  `tools.go` (tool execution and validation), `mutate.go` (the loop, prompts,
  tool schemas).
- `cmd/serve/handlers` owns HTTP: `pages.go` (board, post, version),
  `admin.go` (auth, inject, restore), `handlers.go` (mutation wrapper, cap,
  config).
- GET requests never call the model. Only a post or an admin instruction
  mutates the site.

## Rules that must not break

- The board region must keep the literal `{{posts}}` placeholder; the server
  substitutes the escaped post list. `SiteDoc` appends the posts if the
  placeholder is missing. Never let posts be stored inside model-controlled
  HTML.
- There is no sandbox. `webmaster.SiteDoc` is the document: the model's HTML,
  CSS and JavaScript run as the page. Do not reintroduce an iframe or strip
  scripts; sabotage is the point. Post bodies stay escaped.
- The only fixed markup is the note and the document shell. The post form is
  model markup: the system prompt gives it the exact method, action and field
  names. Do not reintroduce a code-owned composer; a broken form is a valid
  outcome.
- Admin is always token-protected. The token travels in the query string or a
  hidden form field, never a cookie: model scripts are same-origin and must not
  inherit admin access. When `ADMIN_TOKEN` is empty the server generates one
  and logs the URL.
- Mutations from posts run in the background; admin mutations stay
  synchronous so the flash message works. `h.mu` serializes both, and tests
  wait for the version or run to land.
- A model error must leave the current version alone. `Changed` is forced false
  and no version row is written, but the failed run is still recorded.
- Restore copies an old version forward as a new row. History stays immutable.
- Reset is the one destructive action: it keeps the oldest version, deletes all
  posts, later versions and mutation runs, and rewinds the id sequences. It
  takes `h.mu`, so it cannot race a mutation.

## Frontend

The board page is assembled in Go (`lib/webmaster/site.go`), not templ. The
admin page is templ with tailwind classes. `web/lib` and
`static/bundle.js` are leftover scaffold; no page loads them, so do not add
board behavior there. `web/pages/*.templ` is the place for code-owned UI.

## Local model setup

- Mock model: `task dev.mock-llm` on `localhost:6701`, model id `mock-model`.
  Keywords pick themes: `matrix`, `pink`/`cute`, `brutalist`/`concrete`;
  `mocknoop` and `mockfail` test the no-op and error paths.
- Real model: override `OPENAI_BASE_URL` and `OPENAI_MODEL`. A model that
  supports OpenAI-style tool calls is required. Put the key in `OPENAI_API_KEY`,
  usually via `.env` (gitignored, `.env.example` is the template). Real
  environment variables win over `.env`. OpenCode Go also gets a generated
  `x-opencode-session` header; pin it with `OPENAI_SESSION_ID`. Reasoning
  models can take `OPENAI_REASONING_EFFORT` (Muse Spark: `minimal`).
- The dev server defaults are in `Taskfile.yml`; `PORT=6930`, live reload proxy
  `6931`.
