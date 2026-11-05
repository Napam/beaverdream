# Keeping the world across reloads: assessment and plan

Any html or script change makes every open page do a full reload
(`bdLive.reload`, `lib/webmaster/site.go`, `cmd/serve/handlers/live.go`). A
stylesheet-only change and a new post do not. A page that keeps its world in
script memory, such as a 3D scene where visitors walk around, starts over each
time. The goal is to make reloads cheap to survive. Reducing reloads and
patching html in place are out of scope.

Two assessments fed this note, and Opus 5.5 reviewed the plan against the code.
The claims below were checked against the code unless marked otherwise.

## What survives a reload today

- **Draft text and scroll position.** The live script saves them in
  `sessionStorage` inside `maybeReload` before it navigates (`site.go`,
  `saveState`). A manual browser reload saves nothing.
- **Journal rows.** A reloaded page runs `read(key)` with `since=0`. The server
  returns the key's rows in ascending id order, up to `maxJournalRead` = 1000
  (`cmd/serve/handlers/journal.go`, `db/queries/journal.sql`). The client
  filters by match and rules (`lib/webmaster/journal.go`, `add`) and keeps the
  newest `max`. `bd.visitor` is a hash of a 365-day cookie, so it is stable, and
  a page can find its own newest row.
- **Shared state.** Counter, list and choice only. Heal clears it. It cannot
  hold positions.

Lost on every reload: script variables, the scene, the camera, a world seed
generated at random, and anything else that lives only in JS memory.

The system prompt forbids the model from using `localStorage`,
`sessionStorage` and cookies (`mutate.go`, `systemPromptTail`). The journal is
the intended store.

## What is really at risk

A player's last position is lost only when other players write `max` newer rows
after it. The prompt's example sends on an unconditional 60 ms `setInterval`,
and a mutation reloads every tab at once, so everyone's sender stops together
and the gap rarely trims anyone. Cases that do lose a position:

- A page that sends only when the player moves, while others keep moving.
- A hidden tab, whose timers slow down.
- A small `max` on a busy motion key. At 16 updates a second from 5 players,
  200 rows covers about 2.5 seconds.

Other real gaps:

- **The model rebuilds from defaults.** Nothing tells it that the page is
  rebuilt from scratch on a markup change, so it often draws the world from its
  constants. This is probably most of the visible reset.
- **A random world seed is not persisted.** Terrain changes on every reload.
- **The reload lands mid-stride.** `maybeReload` waits only while `input`
  events fire (`site.go`, the `input` listener). Walking with keys on a canvas
  fires none.
- **A rules bump discards old rows** by design. That keeps old moves from
  replaying through new code. A rewritten reducer starts a clean world.
- **The first paint is an empty world.** The page loads, its script starts a
  fetch, the rows arrive, then the world pops in.
- **An edge case in `read`.** It is ascending with `LIMIT 1000` and takes no
  lock (`handlers/journal.go`). On a key with `max: 1000` it can land between an
  append and its trim, when 1001 rows exist, and return the oldest 1000, missing
  the newest. Narrow, but real.

## Decisions

- Keep the journal as the shared truth for positions. No second position store.
- Keep the rules-bump behavior. Split fast and slow keys to limit the damage: a
  bump on `moves` leaves a `world` key alone.
- No before-reload flush event. The newest row is at most about 60 ms old, and
  a fetch started during unload is unreliable (`append` has no `keepalive`).
- The world seed and layout are a constant in the page's script. The model sees
  the page every turn, so it carries across reloads and mutations without
  storage. Shared state cannot hold it.
- A standing-still player stays in the window by sending on a timer, or by
  writing a slow presence key at about 1 Hz. No server change for that.
- A player's builds in a slow key erode at `maxJournalRows` (1000). A durable
  store would need last-writer-wins by a payload field, which breaks the rule
  that the server never reads a payload. Accept the erosion.

## Plan

### 1. Prompt guidance (no code)

The prompt already says to raise `rules` rarely (`journalPrompt`), to treat
`at` as presence, and to send on a timer in its example. Add only what is new,
in the journal section of the system prompt (`mutate.go`):

- Any change to your html or scripts reloads every open page, so a page is
  rebuilt from scratch often.
- Build the scene from journal rows on load, and spawn each visitor at their own
  newest row.
- Keep the world seed and layout as a constant in the page, never a random value.
- For a player who stands still, keep sending on the timer, or write a slow
  presence key, so the newest row stays in the window.

### 2. Embed the journal rows in the document

When the server renders the board, put the current rows of every declared key in
the `bd-journal` config in `<head>`, so a reloaded page folds them on first
paint. Today the page waits for a fetch.

**Cost.** Embedding adds no bytes: the `/j` fetch already sends the same rows,
and more, because it ships every match and rules, which the client filters.
Board responses have no compression today. The embed removes one request and the
`/j` handler time per key and per tab. It adds no database work. `GET /` already
reads a tail per key (`journalState`, `handlers/journal.go`), and after a
mutation every reloaded tab already runs the same reads through `/j`. It saves
one round trip: under 1 ms on a LAN, roughly 30 to 100 ms on a VPS. The visible
gain is the empty-world flash. A typical motion key (300 rows) is about 39 KB.
The worst case without a cap is about 4.3 MB (6 keys, 1000 rows, 600-byte
payloads), which would delay the body's first paint, so the embed is capped.

**Server.**

- `journalState` already runs `JournalTail(key, match, rules)` on every `GET /`
  with `Limit: 3`. Raise the limit to the key's `max` for the render and reverse
  the result. Rows of one match and rules form a suffix in id order, so this is
  the same set the client keeps after its add-then-trim.
- Marshal each key's rows with `json.Marshal` into a `rows` map on the
  `bd-journal` config. Keep a running total and skip a key that would pass
  64 KB, so its feed fetches as today. A skipped key needs no other handling.
- Read the cursor before the rows. `MaxJournalID` is read at `pages.go:59`,
  after `siteWithState` (`pages.go:53`). Move it first. A row written between
  the reads is then in the snapshot and in the stream, and the client's `seen`
  set drops the duplicate. If the rows were read first, a row written between
  the two reads would be in neither, and be lost with no error. No transaction
  is needed. A helper exists (`handlers/versions.go`) and adds nothing here.
- Do not use the embedded rows' max id as the cursor. The cursor is global
  across keys.
- Embed only keys in the current schema. Use a script tag with `json.Marshal`,
  like `bd-state`. It escapes `<`, `>`, `&`, U+2028 and U+2029. Rows are
  untrusted public data, so do not put them in an attribute.
- The payload stays a `RawMessage`. The server never reads it.

**Runtime.**

- The first `load()` takes `conf.rows[key]` if present, deletes it, sets
  `loaded`, calls `add` and queues a draw. Otherwise it fetches.
- Only `start()` consumes the embed. Rematch, a closed match and `_resync` go
  through `load()` and `read()` and must still fetch, so a stale embed is never
  reused.
- Buffer pushed rows for embedded keys. `receive` returns early while a feed is
  not started (`journal.go`, `receive`), and `_rows` skips keys no script has
  opened. Today that is safe, because `start()` fetches after the stream has
  pushed. With an embed, the stream can advance the `_bd` journal cursor past
  rows the snapshot lacks before `start()` runs, and those rows are then lost. A
  3D page is the likely case: a `<script type="module">` that imports three.js
  runs after Datastar's module, which comes earlier in the document. So `_rows`
  holds rows for a key that has an embed and no started feed, and `start()`
  applies them after the embed.

**Fallback.** If the embed proves too fiddly, start the fetch earlier instead.
At runtime init, fire `read()` for every declared key and let `start()` consume
the promise. It is about 10 lines of JS, has no cursor risk, and still costs one
round trip, but that trip starts as the response begins.

**Side fix.** Fix the `read` edge case above by reading newest-first and
reversing, as the embed does.

**Docs to update.** AGENTS.md journal rules: "A feed loads its rows once and the
stream pushes every new row after that", "the same newest `max` rows the server
trims to", and the embed itself. The runtime comment on journal loading in
`lib/webmaster/journal.go` near line 213.

### 3. Reload at a pause

Treat `keydown` like `input` in the typing wait (`site.go`, `maybeReload`).
Held keys auto-repeat, so the reload lands at a real pause in walking. The 15 s
cap still applies. This moves the reload and does not remove it. Do not add
`pointermove`: mouse-look is constant on a 3D page, so the 15 s cap would always
bind, and the old page would keep running old code in the meantime.

## Deferred

- **`keep_latest: N`**, to keep the latest row per visitor through the trim. It
  needs more than one SQL clause, which is why it is deferred:
  - The server trim is per key across all matches and rules
    (`db/queries/journal.sql`). A closed match's last row would stay pinned and
    never show. Scope the kept rows to the appended row's match and the key's
    rules: `AND id NOT IN (SELECT MAX(id) ... WHERE key = ?1 AND match = ?3 AND
    rules = ?4 GROUP BY visitor ORDER BY MAX(id) DESC LIMIT ?5)`.
  - The client `trim()` (`lib/webmaster/journal.go`) keeps only the newest `max`
    rows, so a load would drop the kept rows at once. Mirror the rule there.
  - The read must fetch `max + N` newest rows. The AGENTS.md invariant becomes
    "the same rows the server keeps".
  - Cap N. Visitor ids are free (clear the cookie), so an uncapped rule grows
    without limit. Storage is bounded by `max + N` per key.

  Build it only if step 1's timer and a slow presence key prove not enough.
- **`bd.keep`** (a code-owned `sessionStorage` snapshot for tab-local data). Cut
  as YAGNI. A version stamp would drop every snapshot, because each markup
  mutation changes the version and that mutation is when the reload happens. If
  it is ever needed, stamp it with the rules number only, and save from
  `saveState`.

## Skipped

- A before-reload flush event (see Decisions).
- A softer rules-bump default.
- WebGL rebuild cost. A rebuild and a brief flash are fine for a toy. Giving the
  shell `background: var(--bg)` would cut the white flash.
- `<link rel=preload>` for `/j`: fragile with `no-store`.
- ETag on `/j`: the cursor moves about 20 times a second.
- A cache keyed by version and cursor: the cursor changes with every motion row.

## Risks

- **Silent drops.** Reading rows before the cursor, or letting pushed rows hit a
  feed that has not started, loses rows with no error. The two tests below are
  required.
- **Late first paint.** An embed without the size cap delays the body.
- **Hot path.** The embed raises the tail `Limit` on every `GET /`. It is cheap
  but it is a change on the hot path.
- **Teleporting.** A page can append a fake position from the console, as it can
  today. The reducer needs a speed check.
- **Ghosts.** Rows of players who left stay in the window, so the reducer fades
  them out by `at`.

## Tests

There is a harness that runs the real client runtime in node
(`lib/webmaster/journal_runtime_test.go`, scenarios with a scripted server and
pushed rows) and one that drives the reload wait (`live_test.go`). Use them. Do
not leave these as manual checks.

For the embed:

- **Gap (Go).** A goroutine appends while many `GET /` run. For each page, open
  `/live` with its cursor. The snapshot plus the pushed rows must hold every
  appended id for the current match. Overlap is allowed.
- **Late `start` (harness).** Rows are pushed for an embedded key before
  `start()` runs. After `start()`, the feed holds the snapshot plus those rows,
  with no duplicates. This fails first without the buffering.
- **Filter (Go).** Rows across matches 1 and 2 and rules 1 and 2: the snapshot
  holds only the current match and rules, in ascending order.
- **Trim (Go).** `max` 3, append 5: the snapshot holds the last 3.
- **Escaping (Go).** A payload `"</script><!--"` survives in the tag and
  round-trips.
- **Cap (Go).** Too many rows: the key is absent from `rows` and `/j` still
  serves it.
- **Stale embed (harness).** The snapshot is used once. A rematch, a closed
  match and `_resync` all fetch.

For the reload wait (harness): a `keydown` within 1.5 s holds the reload, and
the 15 s cap still releases it.

## Order

1. Prompt guidance.
2. The embed, with the cursor move, the buffering fix and the `read` fix.
3. The `keydown` reload wait.

## Open questions

- Real `max` and send cadence for a walking world, to check the 64 KB cap
  against a real 3D page before settling the number.
- Whether the gain on a LAN is measurable at all. Opus puts it near zero. The
  case for the embed is the VPS and the first paint.
