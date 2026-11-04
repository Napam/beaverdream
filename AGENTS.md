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
  every server event (heal, undo, restore). `id` is the conversation
  order. The `posts` view exposes only `kind = 'post'`; the board reads posts
  through it, so model text (said, notes, changes) cannot reach the page by
  accident. `site_versions` is the render, append-only; the current site is
  `MAX(id)` and `turn_id` names the turn that produced it. `room_summaries`
  holds the compacted history: the newest row is one message standing in for
  everything through `through_turn_id`. `site_state` holds the model-declared
  shared values, and its schema travels on the version row. `journal` holds the
  model-declared append-only feeds, and their schema travels there too.
- `lib/webmaster` owns the model contract: `site.go` (types, base CSS, rendering),
  `room.go` (the turn type, the room log and
  the change summary), `tools.go` (tool execution and validation), `mutate.go`
  (the loop, prompts, tool schemas).
- `cmd/serve/handlers` owns HTTP: `live.go` (the stream and its projection),
  `hub.go` (topics and subscribers), `pages.go` (board, post, version),
  `versions.go` (version writes, carry forward), `admin.go` (auth, pages,
  inject, restore), `admin_live.go` (the admin streams), `state.go` (shared
  state), `journal.go` (feeds), `handlers.go` (mutation wrapper, room log,
  cap, config).
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
  turns are retried next time. Compaction runs after the finish transaction,
  still under `h.mu`, on turns before the finished one and before any queued
  post; the finished turn stays in the next log. The log query reads at least
  `CompactAfterTurns` turns, so its start never slides before compaction.
- The user prompt is ordered for the prompt cache: compacted history, room
  log, then the notes, the page and the new post, which change every turn.
  The system prompt is one static string and includes `BaseCSS` and a sample
  post rendered by `RenderPosts`.
- Out-of-band changes are turns, not silent edits. Heal, undo and restore
  each record an event turn in the same transaction as the version they carry.
  The webmaster's notes survive heal and failed turns; reset deletes them with
  the room.
- Keeping the page is a real turn. `done` with no edits writes no version and
  records `status = 'kept'`; no `done` at all records `silent`. Do not reject a
  no-op: the plain-board rule lives in the prompt, not in code.
- The board region must keep the literal `{{posts}}` placeholder; the server
  substitutes the escaped post list. `cleanRegion` rejects a board without it,
  for `set_site` and `edit` alike. `SiteDoc` still appends the
  posts if a stored board lacks it. Never let posts be stored inside
  model-controlled HTML.
- The model changes the page with `edit` (replace one exact, unique piece of
  the stylesheet or a region, or append when `old` is empty) and `set_site`
  (whole fields, for a real restyle). Output tokens are the latency, so the
  prompt steers small changes to `edit`. An edit result goes through the same
  `cleanCSS` / `cleanRegion` as a whole write, and a rejected edit leaves the
  field alone. When a response calls
  `done` next to a rejected call and rounds remain, the loop defers that
  `done` so the model can fix the call; its reason and notes stay as the
  fallback. `set_site` cleans every field before it assigns any, and a write
  that changes nothing is "ok (no change)". Matching for `old` tries exact
  text, then loose whitespace between words, then loose whitespace around
  `{}:;,>`, and stops at the first step that finds anything.
- `PostsSwallowedBy` reads header and board as the browser would and names an
  unclosed comment, raw-text element or tag that hides `{{posts}}`. When this
  turn's edits caused it and rounds remain, `done` is deferred once with the
  reason; calling `done` again keeps the page. It never rewrites markup.
- Every completion sends `tool_choice: required`; `done` is the only way to
  end a turn with words.
- There is no sandbox. `webmaster.SiteDoc` is the document: the model's HTML,
  CSS and JavaScript run as the page. Do not reintroduce an iframe or strip
  scripts; sabotage is the point. Post bodies stay escaped.
- The only fixed markup is the document shell, Datastar and the live stream
  mount, the live script, the `bd-posts` container, the `bd-state` JSON tag,
  the small `set name` link, and the busy spinner. The spinner is
  inline-styled and patched by the stream while a rewrite is queued or
  running, so a model stylesheet cannot move or hide it. The server also
  replaces `{{name}}`
  in the model's regions with the escaped cookie name, and puts the same name
  on `<html data-name="...">` so model scripts can personalize in the browser.
  The post form is model markup: the system prompt gives it the exact method,
  action and field names. Do not reintroduce a code-owned composer; a broken
  form is a valid outcome.
- The read side is one Datastar stream per open page, `GET /live`. Commands
  (`POST /posts`, `/x/state/{key}`, `/j/{key}`, every admin action, the
  worker) write the database and then publish a topic. Topics are a bitmask:
  posts, site, busy, state, journal, and turns for the admin panel. Every
  stream re-reads the parts its page is missing and pushes them; a subscriber
  coalesces a burst into one sync, so the hub never blocks and never drops.
  The page holds its cursor as the `_bd` signal (`version`, `post`, `journal`)
  and the stream patches it as it pushes, so a reconnect resumes from what the
  page already shows and gets nothing twice.
- New posts are patched into `#bd-posts`: a prepend, or an inner replace when
  the page is more than one batch behind or a reset rewound the ids. A new
  post never reloads the page. The client drops a post id the board already
  shows, so a reconnect cannot double a prepend. A stylesheet-only change
  patches `<style id="bd-css">` and calls `bdLive.css`; anything else calls
  `bdLive.reload`, which waits for the visitor to stop typing and saves the
  draft and reading position first. Shared state arrives as the `$shared`
  signal, journal rows through `bd._rows`, the spinner through `#bd-busy`, and
  the cursor is patched last. A markup reload leaves the version cursor old
  until the page navigates, so a stream that dies during the typing wait asks
  for the reload again.
- A cursor version of 0 adopts the current version instead of reloading, or a
  page that connected without one would reload forever. A stream without a
  name answers 204, which tells Datastar to stop retrying. Datastar commands
  answer 204 and never a redirect, because Datastar follows a redirect and
  would patch the board into the document. A plain form post still gets 303.
  `/version` stays as a plain JSON read for tools and tests; nothing polls it.
- Everyone sees the same page. Personalization happens in the browser: the
  server fills `{{name}}` and `<html data-name>` from the cookie, and the
  prompt tells the model to branch on `document.documentElement.dataset.name`
  in a script. There is no per-visitor server state.
- Shared state is model-declared and server-applied. Only keys in the current
  version's `state_schema` are writable, values pass through
  `CleanStateValue`, and the ops are fixed (counter, list, choice). Writes are
  serialized and cooled down per visitor. Do not let visitors create keys or
  store raw HTML. Reset and heal clear the state; restores keep the values and
  take the old schema.
- The journal is the model's one flexible store and the server never reads a
  payload. Rows are append-only, grouped by a model-declared key, and carry the
  browser's opaque visitor id plus the match and rules numbers. Only keys in
  the current version's `journal_schema` are writable; visitors cannot create
  keys. Every declared key must be opened by a `bd.journal("key")` call in the
  page (the bare word in prose does not count), and a `done` whose page drops
  one is deferred, because its rows would be unreachable. A rules number
  belongs to the reducer that wrote rows under it: the page holds the rules
  its code was written for and sends them with every append, the server
  refuses a mismatch with 409, and a mismatch anywhere reloads the page, never
  just the feed. A rules bump in `set_journal_schema` skips past every number
  stored rows carry, declared key or not. A feed loads its rows once and the
  stream pushes every new row after that, so nothing polls. The client runtime
  is code-owned; the model writes `reduce` and `render` only.
  The log is public, so the reducer, not the server, rejects illegal rows.
- A browser error is a turn, not a console line. `/x/err` records one `alert`
  turn per message per stretch, labelled as visitor-reported, and never
  triggers a turn, or a script that throws again would loop forever. The
  beacon carries the page's version; an error from a page that is no longer
  current is dropped. Refusals the page did not cause (closed match, 409, 429,
  401) resolve `append` with null and never reach the beacon.
- The seed page reads like a plain light message board. Do not mention
  rewriting, AI, or the gimmick in its header, form or footer; the first post is
  the reveal. Keep the seed in `000001_init.sql` and the form example in the
  system prompt in sync.
- Identity is one HttpOnly cookie, `beaverdream_name`, set by `/identity`. A
  first visit gets the code-owned name page, and `GET /identity` opens the same
  form prefilled so a visitor can set their display name again. Posts use the
  cookie value as the author. Names are not unique and there are no accounts.
  The server ignores any author field in model markup.
- Admin is always password-protected. The pages are `/admin`,
  `/admin/turns`, `/admin/turns/{id}` and `/admin/site`. Without the token a
  page renders the password form, which POSTs the token back to the same path.
  The page carries the token in hidden fields and the `$_token` signal, and
  sends it as `X-Admin-Token` on its live stream and on the fetch that moves
  between pages. Never a cookie, and never a query parameter: model scripts
  are same-origin and must not inherit admin access, and the token must not
  land in URLs or history. Admin pages and actions answer Datastar with
  `#admin-body` patched in place (a plain request renders the whole page), so
  no redirect needs the token. Each page opens its own stream for its own
  panels. When `ADMIN_TOKEN` is empty the server generates one and logs the
  password.
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
  state and the journal in the same transaction as that version, takes `h.mu`,
  and never calls the model. The
  admin panel can heal immediately.
- Reset is the one destructive action: it keeps the oldest version, deletes
  every turn (posts, webmaster turns, events, notes) with the compacted
  summaries, later versions and shared state, and
  rewinds the id sequences. It takes `h.mu`, so it cannot race a mutation.

## Frontend

The board page is assembled in Go (`lib/webmaster/site.go`), not templ. The
admin page is templ with tailwind classes; `web/pages/admin_view.go` turns
stored turns into what it shows. The user message in `turns.messages`
carries `sections`, byte offsets that tile the prompt, written by the same
builder that writes the prompt, so the inspector shows each part exactly as
the model read it. Charts are plain CSS bars. `web/lib` and
`static/bundle.js` are leftover scaffold; no page loads them, so do not add
board behavior there. `web/pages/*.templ` is the place for code-owned UI.

## Local model setup

- Mock model: `task dev.mock-llm` on `localhost:6701`, model id `mock-model`.
  Keywords pick themes: `matrix`, `pink`/`cute`, `brutalist`/`concrete`;
  `mocknoop` and `mockfail` test the no-op and error
  paths. `mockedit` appends one CSS rule, `mockpatch` replaces the default
  theme's accent, and `mockbadedit` sends an edit that misses, so the `done`
  next to it is deferred. Each sends the edit and `done` in one response.
  The mock reports rough usage, four request bytes per prompt token with the
  system message counted as cached, so the admin charts have numbers.
- Each model round logs `Mutation round finished` with its time, prompt,
  cached and completion tokens, the size of the reply and the reasoning, its
  `finish_reason`, the tools it called, how many were rejected, one clipped
  `outcome` line per call, and the `done_deferred` reason when a done was
  pushed back. The same stats are stored on each assistant message in
  `turns.messages`. `Mutation finished` sums
  the turn: rounds, the done round, whether done was called at all, a done-only
  last round, rejections, truncated rounds, the cached ratio, prompt section
  sizes in runes, compaction time, and for posts the queue wait and the time
  from post to version. A turn that runs out of rounds without done logs
  `Mutation ended without done` with the round budget, the reason and the last
  round's outcomes. The reason names what actually stopped the loop: the output
  cap, a round with no tool call, an empty choice list, or the round budget.
- Real model: override `OPENAI_BASE_URL` and `OPENAI_MODEL`. A model that
  supports OpenAI-style tool calls is required. Put the key in `OPENAI_API_KEY`,
  usually via `.env` (gitignored, `.env.example` is the template, loaded by the
  Taskfile). The server reads only the process environment; shell variables win
  over Taskfile values, which win over `.env`. OpenCode Go also gets a generated
  `x-opencode-session` header; pin it with `OPENAI_SESSION_ID`. Reasoning
  models take `OPENAI_REASONING_EFFORT`; the dev task uses
  `deepseek-v4.1-flash` with `low`.
- The dev server defaults are in `Taskfile.yml`; `PORT=6930`, live reload proxy
  `6931`.
