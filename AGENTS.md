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
- Test behavior, not wording. Never assert that a skill body, the system
  prompt or a hint contains a phrase, a header or a name: prose changes all
  the time, and such a test only proves the text was typed. Run the code
  instead: skill cores in node, prompt builders on real data (the room log,
  the journal lines, section offsets), and the system prompt staying one
  stable string.

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
  (the loop, prompts, tool schemas), `stream.go` (one streamed round and its
  drafts). `lib/webmaster/skills` holds the skills and their registry;
  `lib/webmaster/internal/jsrun` runs JavaScript from tests in node or bun.
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
- Compaction is one-way. When the whole prompt reaches the configured budget
  (200k estimated tokens by default, four runes per token) or a 10k-turn
  backstop, the server replaces the uncompacted history with one summary
  message and starts a fresh log after that point. The
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
  A page that already moves something with keys gets a short hint after the
  new post, so it never shifts the cached prefix. The hint names the game
  skills; the model decides which to load.
  The system prompt is one static string and includes `BaseCSS` and a sample
  post rendered by `RenderPosts`.
- Out-of-band changes are turns, not silent edits. Heal, undo and restore
  each record an event turn in the same transaction as the version they carry.
  The webmaster's notes survive heal and failed turns; reset deletes them with
  the room.
- Keeping the page is a real turn. `done` with no edits writes no version and
  records `status = 'kept'`; no `done` at all records `silent`. Do not reject a
  no-op: the plain-board rule lives in the prompt, not in code.
- A version is two model fields: `css` and `html` (everything inside body).
  Enforce only what keeps the site working as an ever-mutating board: the
  `{{posts}}` placeholder, the form contract in the prompt, the size caps and
  the code-owned shell. Do not add regions, required elements or layout rules.
  `BaseCSS` styles only code-owned markup (posts, the name link) and the color
  variables; the seed's layout lives in the seed's own stylesheet.
- The html must keep the literal `{{posts}}` placeholder exactly once; the
  server substitutes the escaped post list. `cleanHTML` rejects html without
  it or with a second copy, for `set_site` and `edit` alike, because a second
  list would repeat the post ids and the stream patches only the first.
  `SiteDoc` still appends the posts if a stored page lacks it. Never let posts
  be stored inside model-controlled HTML.
- Posts stay server markup; the model places them, it does not render them.
  Each `article.post` carries escaped `data-id`, `data-author` and
  `data-when` for page scripts, and the prompt tells the model to position
  the articles inside `#bd-posts` (it has `display:contents`), never move
  them out or rebuild them, and never put visitor text through `innerHTML`.
  Do not add a parallel post data API: it would be a second source of truth
  and its copies would repeat the `#pN` ids.
- The model changes the page with `edit` (replace one exact, unique piece of
  the stylesheet or the html, or append when `old` is empty) and `set_site`
  (whole fields, for a restyle or a different kind of page). Output tokens
  are the latency, so the prompt steers small changes to `edit`. An edit
  result goes through the same `cleanCSS` / `cleanHTML` as a whole write, and a rejected edit leaves the
  field alone. When a response calls
  `done` next to a rejected call and rounds remain, the loop defers that
  `done` so the model can fix the call; its reason and notes stay as the
  fallback. `set_site` cleans every field before it assigns any, and a write
  that changes nothing is "ok (no change)". Matching for `old` tries exact
  text, then loose whitespace between words, then loose whitespace around
  `{}:;,>`, and stops at the first step that finds anything.
- `PostsSwallowedBy` reads the html before `{{posts}}` as the browser would
  and names an unclosed comment, raw-text element or tag that hides `{{posts}}`. When this
  turn's edits caused it and rounds remain, `done` is deferred once with the
  reason; calling `done` again keeps the page. It never rewrites markup.
- Every completion sends `tool_choice: required`; `done` is the only way to
  end a turn with words. Every round streams, with `include_usage`, or the
  stream reports no tokens. The loop still acts only on the whole reply; the
  chunks exist so the admin can watch. The summarizer does not stream.
- A round whose tool arguments are not JSON, or carry the provider's own tool
  markup, is a broken sample, not an edit to fix. It is resampled without
  being appended to the conversation, because replaying the markup puts it
  back in the prompt and the model copies it; five such rounds end the turn.
  Valid JSON of the wrong shape is an ordinary rejection. A provider that
  returns `reasoning_content` gets it back with every assistant tool call,
  since its thinking mode requires it; without it the model loses the tool
  format.
- A round whose stream goes quiet is dropped and sent again, never acted on.
  `IdleLimits` (3 min before the first event and while thinking, 2 min once a
  tool call's name has arrived) reset on every chunk that adds text, reasoning
  or call bytes, so a slow stream that keeps moving is never cut. A stream that
  closes with no finish reason and no usable call is treated the same way. A
  retry sends the identical request after a short pause, keeps nothing of the
  dropped round (its reasoning must not go back to the provider), and counts
  toward a streak of three, which ends the turn as an error with the page
  untouched. Stalled and resampled rounds do not use up `maxRounds`. A call cut
  off by `finish_reason: length` is rejected with a message, not resampled.
  Never put raw stream text in `Message.Content` or `Message.Reasoning`: the
  first goes back to the provider and the second can become the turn's reason.
  It lives in `RoundStats.Trace`, which only the admin reads.
- Stop turn cancels the running turn with the cause `errAdminStop`. It names
  the turn id, so a late click cannot stop the next turn, and it never takes
  `h.mu`. The turn is stored as an error labeled "stopped by admin".
- There is no sandbox. `webmaster.SiteDoc` is the document: the model's HTML,
  CSS and JavaScript run as the page. Do not reintroduce an iframe or strip
  scripts; sabotage is the point. Post bodies stay escaped.
- The only fixed markup is the document shell, the import map that gives
  `three` and `three/addons` to three.js 0.187 (vendored in `web/vendor`, so
  pages need no CDN),
  Datastar and the live stream mount, the live script, the `bd-posts`
  container, the `bd-state` JSON tag, the small `set name` link, and the busy
  spinner. The spinner is
  inline-styled and patched by the stream while a rewrite is queued or
  running, so a model stylesheet cannot move or hide it. The server also
  replaces `{{name}}`
  in the model's html with the escaped cookie name, and puts the same name
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
  shows, so a reconnect cannot double a prepend. `bdLive.posts` emits
  `beaverdream:post` with `mode` (`prepend` or `replace`) and the patched
  `ids`, so a script that places posts knows which ones to place. A
  stylesheet-only change
  patches `<style id="bd-css">` and calls `bdLive.css`; anything else calls
  `bdLive.reload`, which waits until the visitor stops typing or pressing keys
  (a 1.5 s quiet window, capped at 15 s from the first wait), then saves the
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
  stored rows carry, declared key or not. A feed reads its rows once, and
  retries a failed first read once. The runtime starts that read at load and
  `start()` uses it, then the stream pushes every new row after that, so nothing
  polls. The feed keeps the same newest `max` rows the server trims to, so a
  reloaded tab and a long-open tab fold the same history, and it folds a burst
  once per frame. A hidden tab renders on a timer, because it parks animation
  frames. The client runtime is code-owned: it owns the append, the fold, the
  row window, the match and rules handling and the error reports. The model
  writes `reduce`, `render`, its send cadence and when to offer the next match,
  so those are the parts a bad page can get wrong. The motion example restores
  the visitor from their own newest row on the first render and sends nothing
  before that, because a reload restarts the page script at the spawn point. The
  log is public, so the reducer, not the server, rejects illegal rows.
- The admin data page (`/admin/journal`, labelled "data") shows the shared
  state first, then lists every key that has rows, declared or not, with stats
  from one aggregate query, and browses one key's rows newest first. The site
  tab holds only versions and the reset; the overview shows the compacted
  memory and the latest notes. The selection (`feed`, `match`, `visitor`,
  `before`) is in the URL, and the page's stream renders the same selection, so
  a patch never resets what the admin picked. The stream waits a second after
  each patch, because every append publishes and the stats scan every key.
  Payloads are shown as text only.
- Clearing a key (`POST /admin/journal/{key}/clear`) deletes its rows and
  records a `journal` event turn in one transaction, under `stateMu` like an
  append, never `mu`, and it never calls the model. The key falls back to match
  1; rules belong to the schema and do not change, and the id sequence is left
  alone so cursors stay monotonic. A clear bumps `journalClears`, and each
  stream that has seen fewer sends `bd._resync()`, because a clear can leave
  the newest row id where it was. The client reads the feed again on a pushed
  row from an older match it has not read, which covers a stream that was down.
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
- Admin is always password-protected, on its own port. The pages are
  `/admin`, `/admin/turns`, `/admin/turns/{id}` and `/admin/site`, served only
  on `ADMIN_PORT` (default: the port after `PORT`, skipping the live reload
  proxy). The board's port redirects every admin path there, keeping the host
  or to `ADMIN_URL` when it is set, so a LAN address works too. The admin
  listener mounts the panel and its assets only and serves no board route.
  That split is what allows the login cookie: model-written JavaScript never
  runs on the admin origin, and the
  browser refuses to hand the board a cross-origin response. The cookie is
  `beaverdream_admin`, a signed expiry, HttpOnly, SameSite=Strict, host-only,
  lasting 30 days; a new `ADMIN_PASSWORD` invalidates it. Without it a page
  renders the password form. A correct password sets the cookie and answers
  with a 303 back to the same path, so the next request carries the cookie,
  the page gets its CSRF token, and a reload cannot repost the password.
  Cookies are scoped by host, not by port, so the board port does receive the
  cookie; HttpOnly keeps it out of the model's scripts, and the CSRF token
  keeps a forged action from working.
- Every cookie-authenticated POST needs the `csrf` field, which is
  HMAC(ADMIN_PASSWORD, "admin-csrf:" + the signed expiry in the cookie), compared
  in constant time. It is derived, not stored, and it dies with the login. The
  panel renders it into every action form, and the live stream renders it into
  the fragments that carry forms (undo, heal, restore), because that is where
  those panels come from after load. Only the admin origin can read it. A
  request authenticated by `X-Admin-Token` skips the check: that token is
  itself a secret only a script has. Login needs no CSRF (one shared identity,
  and the password is still required). Never add an `Origin` or `Sec-Fetch-*`
  check: `Referrer-Policy: no-referrer` makes browsers report `Origin: null`,
  and plain HTTP on a LAN address sends no fetch metadata at all.
- Every cookie with the admin's name is tried and any that verifies is used.
  A page on the board port can plant one ahead of the real login; skipping bad
  ones keeps that from locking the admin out. Nobody can read or overwrite the
  real cookie from the board. `X-Admin-Token` still works in a header or form
  field for scripts. Never a query parameter, and never the token in a page or
  a URL. Admin pages and actions answer Datastar with `#admin-body` patched in
  place (a plain request renders the whole page), so no redirect needs a
  secret. Each page opens its own stream for its own panels. A turn's
  conversation is only stored when it finishes, so the turns page shows the
  running turn from memory: the prompt and every finished round in full as
  `Mutator.OnMessage` hears them, and the round being written as
  `#admin-draft`, which `OnDraft` fills chunk by chunk. A draft publishes
  `topicDraft` at most every 100 ms and patches only `#admin-draft` and the
  status card, never the whole inspector; the card names the phase (waiting,
  thinking, replying, writing a call, running its tools). Board streams do
  not subscribe to it. The stored turn replaces the live view when it lands,
  and a held turn that was running gets that one last patch: its mount
  carries `live=1`, so the patch also lands when the turn finished before the
  stream connected. `runState` rides on the public
  `/version` JSON, so its messages and draft never marshal. When
  `ADMIN_PASSWORD` is empty the server generates one and logs the password.
- Mutations from posts run in the background through one worker that coalesces
  a burst into one rewrite; admin mutations stay synchronous so the flash
  message works. `h.mu` serializes both, the worker takes it before draining
  pending, and the timeout starts after the lock. Reset therefore cannot run a
  batch of posts it just deleted. Tests wait for the version or run to land.
- Skills are how-tos the model reads with `load_skill(name)`, registered in
  `skills/skills.go`. A body is only ever a tool result: never in the room
  log, the page or the system prompt. A turn loads each skill once; a second
  load points back at the first result. A skill load is not an edit and does
  not end the turn, so the system prompt has the model send its loads in one
  response of their own, then edit. The skills are `controls`, `feel`, `space`,
  `entities`, `paths`, `random`, `canvas` and `characters`; each teaches
  general technique, never one board's game. Each holds one script block
  that its `*_test.go` runs; change both together.
- A model error must leave the current version alone. `Changed` is forced false
  and no version row is written, but the turn is still recorded with its error.
- Restore copies an old version forward as a new row. History stays immutable.
- A retry of an errored turn is a new turn, never an edit of the failed row. An
  admin instruction is retried from its stored `body`; a post-triggered turn
  retries the posts between it and the webmaster turn before it, trimmed to
  the newest `maxBatchPosts` like the worker, which is the batch that turn ran.
  A retry of a retry has no posts of its own, so it walks back to the turn it
  copied. A crash leaves no stored prompt, so the posts are read back from
  `turns` rather than parsed out of the messages.
- Heal puts the seed page back by copying it forward as a new row and
  recording a heal turn. It keeps posts and the webmaster's notes, clears shared
  state and the journal in the same transaction as that version, takes `h.mu`,
  and never calls the model. Only the admin panel heals: a quiet board keeps
  its page however long it sits.
- Reset is the one destructive action: it keeps the oldest version, deletes
  every turn (posts, webmaster turns, events, notes) with the compacted
  summaries, later versions and shared state, and
  rewinds the id sequences. It cancels the turn in flight and drops the queue
  before it takes `h.mu`, so it never waits on a model call and cannot race a
  mutation. Posts that arrive during the reset stay queued and run after it.

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
  paths. `mockskill` loads the controls skill in its first round.
  `mockedit` appends one CSS rule, `mockpatch` replaces the default
  theme's accent, and `mockbadedit` sends an edit that misses, so the `done`
  next to it is deferred. Each sends the edit and `done` in one response.
  A streamed request gets the same reply cut into small chunks with a short
  line of reasoning first (`mockllm.Streamed`; test fakes that write a whole
  completion wrap themselves in it). `task dev.mock-llm` pauses 15 ms per
  chunk so a turn is slow enough to watch; tests use no pause.
  The mock reports rough usage, four request bytes per prompt token with the
  system message counted as cached, so the admin charts have numbers.
- Each model round logs `Mutation round finished` with its time, time to the
  first token, prompt,
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
  `x-opencode-session` header; pin it with `OPENCODE_SESSION_ID`. Reasoning
  models take `OPENAI_REASONING_EFFORT`.
- The dev server defaults are in `Taskfile.yml`; `PORT=6930`, live reload proxy
  `6931`.
