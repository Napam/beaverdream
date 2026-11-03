# Working on beaverdream

beaverdream is a message board whose site is rewritten by a model on each post.
Go + templ + chi + sqlite (sqlc, goose), with a small TS frontend. It is a toy:
prefer simple, readable code over production hardening.

## Ground rules

- Use `tmp/` for scratch files and test output. It is gitignored.
- The schema lives in one rewritten migration, `000001_init.sql`, not an
  additive 000002. Goose will not re-run an applied version, so after a schema
  change delete `data/sqlite.db*` and let the server recreate it. Never edit an
  applied migration on a board you want to keep.
- Read `Taskfile.yml` before running commands; it is the source of truth.
- After changes run the matching check:
  - go code: `task check.go`
  - web code: `task check.web`
  - both: `task check`
  - auto-fix: `task fix` (or `task fix.go` / `task fix.web`)
- `go test ./...` must pass. Tests use `lib/mockllm`, never a real model.

## Shape of the app

- `turns` is the room: one ordered row for every post, every webmaster turn and
  every server event (heal, undo, restore, clear). `id` is the conversation
  order. The `posts` view exposes only `kind = 'post'`; the board reads posts
  through it, so model text (said, notes, changes) cannot reach the page by
  accident. `site_versions` is the render, append-only; the current site is
  `MAX(id)` and `turn_id` names the turn that produced it. `room_summaries`
  holds the compacted history: the newest row is one message standing in for
  everything through `through_turn_id`. `site_state` holds the model-declared
  shared values, and its schema travels on the version row. `user_overrides`
  holds the per-visitor versions of the page, keyed by `version_id` and name.
- `lib/webmaster` owns the model contract: `site.go` (types, base CSS, rendering),
  `override.go` (per-visitor pages), `room.go` (the turn type, the room log and
  the change summary), `tools.go` (tool execution and validation), `mutate.go`
  (the loop, prompts, tool schemas).
- `cmd/serve/handlers` owns HTTP: `pages.go` (board, post, version),
  `overrides.go` (visitor pages, version writes), `admin.go` (auth, inject,
  restore), `handlers.go` (mutation wrapper, room log, cap, config).
- GET requests never call the model. Only a post or an admin instruction
  mutates the site.

## Rules that must not break

- The webmaster turn is reserved before its prompt is built, with
  `status = 'running'`. That id is what separates what the turn saw from posts
  that arrived while it ran: those get higher ids and appear after it in the
  next log. When the turn finishes, the version (if any) and the finished turn
  are written in one transaction. A crash leaves a running turn; `New` marks it
  as an error so it is never read back.
- The room log is words and field names only: `#id author: body`, one line per
  webmaster turn with its `said` and `changes`, and one line per server event.
  Never put markup, notes, tool calls or a render in it. The current page is
  already in the prompt, and replaying historical tool calls would mutate the
  world again. The trigger posts are skipped in the log because NEW POST shows
  them whole.
- Compaction is one-way. When the uncompacted history reaches 128k estimated
  tokens (four runes per token) or a 10k-turn backstop, the server replaces it
  with one summary message and starts a fresh log after that point. The
  thresholds are `Config.CompactAfterTokens` and `Config.CompactAfterTurns`;
  tests set them low. The summary chains from the previous one, so old detail
  fades by design. Never summarize a running or failed turn, never put markup
  in the summary, and never let a failed summary block the mutation: the same
  turns are retried next time.
- Out-of-band changes are turns, not silent edits. Heal, undo, restore and
  clear each record an event turn in the same transaction as the version they
  carry. The webmaster's notes survive heal and failed turns; reset deletes
  them with the room.
- Keeping the page is a real turn. `done` with no edits writes no version and
  records `status = 'kept'`; no `done` at all records `silent`. Do not reject a
  no-op: the plain-board rule lives in the prompt, not in code.
- The board region must keep the literal `{{posts}}` placeholder; the server
  substitutes the escaped post list. `SiteDoc` appends the posts if the
  placeholder is missing. Never let posts be stored inside model-controlled
  HTML.
- There is no sandbox. `webmaster.SiteDoc` is the document: the model's HTML,
  CSS and JavaScript run as the page. Do not reintroduce an iframe or strip
  scripts; sabotage is the point. Post bodies stay escaped.
- The only fixed markup is the document shell, the live script, the `bd-state`
  JSON tag, and the small `set name` link. The server also replaces `{{name}}`
  in the model's regions with the escaped cookie name. The post form is model
  markup: the system prompt gives it the exact method, action and field names.
  Do not reintroduce a code-owned composer; a broken form is a valid outcome.
- Visitor overrides are model-owned and belong to the version row. Only a name
  on the current version gets one, matched case-insensitively after
  `webmaster.CleanIdentity`, and later mutations inherit the map until the model
  clears it. A later `set_user_override` merges, so fields the model omits stay.
  CSS is appended to the site stylesheet; regions replace. Undo, restore and
  heal copy the overrides of the version they carry; reset drops them with the
  versions. Admin can clear one by name. Do not let overrides bypass `cleanCSS`
  or `cleanRegion`.
- Shared state is model-declared and server-applied. Only keys in the current
  version's `state_schema` are writable, values pass through
  `CleanStateValue`, and the ops are fixed (counter, list, choice). Writes are
  serialized and cooled down per visitor. Do not let visitors create keys or
  store raw HTML. Reset and heal clear the state; restores keep the values and
  take the old schema.
- The seed page reads like a plain light message board. Do not mention
  rewriting, AI, or the gimmick in its header, form or footer; the first post is
  the reveal. Keep the seed in `000001_init.sql` and the form example in the
  system prompt in sync.
- Identity is one HttpOnly cookie, `beaverdream_name`, set by `/identity`. A
  first visit gets the code-owned name page, and `GET /identity` opens the same
  form prefilled so a visitor can set their display name again. Posts use the
  cookie value as the author. Names are not unique and there are no accounts.
  The server ignores any author field in model markup.
- Admin is always token-protected. The token travels in the query string or a
  hidden form field, never a cookie: model scripts are same-origin and must not
  inherit admin access. When `ADMIN_TOKEN` is empty the server generates one
  and logs the URL.
- Mutations from posts run in the background through one worker that coalesces
  a burst into one rewrite; admin mutations stay synchronous so the flash
  message works. `h.mu` serializes both, the worker takes it before draining
  pending, and the timeout starts after the lock. Reset therefore cannot run a
  batch of posts it just deleted. Tests wait for the version or run to land.
- A model error must leave the current version alone. `Changed` is forced false
  and no version row is written, but the turn is still recorded with its error.
- Restore copies an old version forward as a new row. History stays immutable.
- A quiet board heals back to the seed by copying it forward as a new row and
  recording a heal turn. It keeps posts and the webmaster's notes, clears shared
  state and visitor overrides, takes `h.mu`, and never calls the model. The
  admin panel can heal immediately.
- Reset is the one destructive action: it keeps the oldest version, deletes
  every turn (posts, webmaster turns, events, notes) with the compacted
  summaries, later versions (with their overrides) and shared state, and
  rewinds the id sequences. It takes `h.mu`, so it cannot race a mutation.

## Frontend

The board page is assembled in Go (`lib/webmaster/site.go`), not templ. The
admin page is templ with tailwind classes. `web/lib` and
`static/bundle.js` are leftover scaffold; no page loads them, so do not add
board behavior there. `web/pages/*.templ` is the place for code-owned UI.

## Local model setup

- Mock model: `task dev.mock-llm` on `localhost:6701`, model id `mock-model`.
  Keywords pick themes: `matrix`, `pink`/`cute`, `brutalist`/`concrete`;
  `curse`/`only me` adds a per-visitor override; `mocknoop` and `mockfail`
  test the no-op and error paths.
- Real model: override `OPENAI_BASE_URL` and `OPENAI_MODEL`. A model that
  supports OpenAI-style tool calls is required. Put the key in `OPENAI_API_KEY`,
  usually via `.env` (gitignored, `.env.example` is the template). Real
  environment variables win over `.env`. OpenCode Go also gets a generated
  `x-opencode-session` header; pin it with `OPENAI_SESSION_ID`. Reasoning
  models take `OPENAI_REASONING_EFFORT`; the dev task uses
  `deepseek-v4.1-flash` with `none`.
- The dev server defaults are in `Taskfile.yml`; `PORT=6930`, live reload proxy
  `6931`.
