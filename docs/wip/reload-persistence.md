# Keeping the world across reloads: assessment and plan

Any html or script change makes every open page do a full reload
(`bdLive.reload`, `lib/webmaster/site.go:350-361`; `swapKind`,
`cmd/serve/handlers/pages.go:169-179`). A stylesheet-only change and a new post
do not. The reload is a real navigation, `location.replace('/?v=N')`, so the new
page starts with empty script memory. The goal is that a multiplayer page, such
as a 3D world where visitors walk around, puts each visitor back where they
were after a reload. Reducing reloads and patching html in place are out of
scope.

The claims about the code before the change were checked by reading it. Steps
1 to 4 are built and covered by the harness and Go tests under Tests.

## What survived a reload before the change

- **Draft text and scroll position.** The live script saves them in
  `sessionStorage` inside `maybeReload` before it navigates (`site.go:290-312`,
  `site.go:359`). A manual browser reload saves nothing. The navigation drops
  any URL hash.
- **Journal rows.** A reloaded page ran `read(key)` with `since=0` when a feed
  started. The server returned the oldest `maxJournalRead` = 1000 rows in
  ascending id order (`cmd/serve/handlers/journal.go`, `db/queries/journal.sql`).
  Step 3 changed the first load to take the newest rows and return them oldest
  first. The client filters by match and rules and keeps the newest `max`
  (`lib/webmaster/journal.go`). `bd.visitor` is a hash of a 365-day cookie
  (`handlers/journal.go`), so it is stable, and a page can find its own newest
  row.
- **Shared state.** Counter, list and choice only. Heal clears it. It cannot
  hold positions.

Lost on every reload: script variables, the scene, the camera, a world seed
generated at random, Datastar signals, and anything else that lives only in JS
memory.

The system prompt forbids `localStorage`, `sessionStorage` and cookies
(`lib/webmaster/changeset.go`). The journal is the intended store.

## What was really at risk before the change

**The primary cause was the prompt's own example.** The journal example in the
system prompt (`changeset.go`) was the pattern models copied for motion. In it:

- `me`, the visitor's own position, was a plain page variable. Nothing set it
  from the journal. `render` filled only `pos`, the other players.
- `setInterval(function () { moves.append({ x: me.x, y: me.y }) }, 60)` sent
  `me` from the first tick, with no condition.

After a reload, `me` held whatever start the page's script declared. The first
tick ran about 60 ms in, which could be before `start()`'s read landed (an async
`fetch`). That append made the default spawn the visitor's newest row. The
reducer keeps each visitor's newest row, so every page, this one included,
showed the visitor at spawn. The page wrote over the very row that would have
restored it.

The rows were read, and the reducer folded them. The loss was that the example
never fed the visitor's own row back into `me`, and its timer overwrote that
row. A page that followed the example lost the position on every reload,
whether or not the row survived the trim.

The prompt did say that a markup change reloads every tab and that signals
reset on reload (`changeset.go`). It did not say that script state starts over,
or that the page must restore the player from the journal. Step 1 added both.

Secondary risks, once the restore works:

- **Trim.** A visitor's last row is lost when others write `max` newer rows
  after it. Cases: a page that sends only on movement while others keep moving,
  a hidden tab whose timers slow down, a small `max` on a busy key. At 16
  updates a second from 5 players, 200 rows covers about 2.5 seconds. Inferred,
  not measured.
- **Mode lost.** Anything the visitor chose that is not in their row is gone:
  inside or outside the world, which room, which character. It belongs in the
  same row as the position.
- **A random world seed** changes the terrain on every reload.
- **The reload lands mid-stride.** `maybeReload` waited only while `input`
  events fired (`site.go:278,350`). Walking with keys on a canvas fired none.
  Step 2 fixes it.
- **A rules bump discards old rows** by design. A rewritten reducer starts a
  clean world.
- **The first paint is an empty world.** The page loads, its script starts a
  fetch, the rows arrive, then the world pops in.
- **An edge case in `read`.** Append and trim run under `stateMu` as two
  statements (`handlers/journal.go:178-212`); the read takes no lock and is
  ascending with `LIMIT 1000` (`JournalRead`). On a key with
  `max: 1000` it can land between an append and its trim, see 1001 rows, and
  return the oldest 1000, missing the newest. Narrow. Step 3 fixes it, and a Go
  test covers the new order.

## Decisions

- The visitor's newest row in the motion key is the authoritative position.
  No second position store, no URL hash, no browser storage. The server never
  reads the payload.
- A page restores the visitor from that row once, on the first render, and
  does not send until it has.
- Anything the visitor must get back after a reload (mode, room, character)
  goes in the same payload as the position.
- Keep the rules-bump behavior. Split fast and slow keys to limit the damage:
  a bump on `moves` leaves a `world` key alone.
- No before-reload flush event. The newest row is at most about 60 ms old, and
  a fetch started during unload is unreliable (`append` has no `keepalive`).
- The world seed and layout are a constant in the page's script. The model sees
  the page every turn, so it carries across reloads and changesets without
  storage.
- A standing-still visitor stays in the window by sending on the timer, or by
  writing a slow presence key at about 1 Hz. No server change for that.
- A visitor's builds in a slow key erode at `maxJournalRows` (1000). Accept it.
- Full reload stays the update path. See Skipped for why Datastar does not
  replace it.

## Plan

### 1. Fix the example and the prompt (primary, no runtime code) (done)

The motion half of the example in `journalPrompt` (`changeset.go`) now has this
shape:

```js
var me = { x: 50, y: 50 }        // fixed spawn, never random
var pos = {}, ready = false

moves.start(function (rows) {
  // the referee: newest in-bounds row per visitor
}, function (p) {
  pos = p
  if (!ready) {                  // first render: put the visitor back
    var mine = p[bd.visitor]
    if (mine) { me.x = mine.x; me.y = mine.y }
    ready = true
  }
})

setInterval(function () {
  if (ready) moves.append({ x: me.x, y: me.y })
}, 60)
```

The first render runs after the first read lands, even with no rows, so `ready`
flips on a successful load. A failed first read leaves `ready` false, so the
page sends nothing instead of writing the spawn over the visitor's row. The
runtime retries a failed first read once, after a second. If that retry also
fails, `ready` stays false until a pushed row or a resync reads again.

These lines were added to the journal section:

- Any change to your html or scripts reloads every open page, and your script
  starts over. Rebuild the world and the visitor from journal rows.
- Spawn the viewer at their own newest row (`row.visitor === bd.visitor`), and
  fall back to a fixed spawn point. Do not send before the first render.
- Put everything the viewer must get back after a reload in the same payload
  as the position: whether they are inside the world, which room.
- Keep the world seed and layout as a constant in the page, never a random
  value.
- For a visitor who stands still, keep sending on the timer, or write a slow
  presence key, so the newest row stays in the window.

### 2. Reload at a pause (done)

Treat `keydown` like `input` in the typing wait (`site.go:278-279`). Held keys
auto-repeat, so the reload lands at a real pause in walking. The 15 s cap still
applies. Do not add `pointermove`: mouse-look is constant on a 3D page, so the
cap would always bind, and the old page would keep running old code meanwhile.

### 3. Fix the `read` edge case (done)

The first load reads newest-first with the limit, then reverses, so a read
between append and trim drops the oldest row, not the newest.

### 4. Start the read earlier (done, lower priority, does not fix the reset)

At runtime init, every declared key is read and `start()` consumes the promise.
It shortens the empty first frame. On a busy key whose script starts late, a
pushed row usually discards the early read. It does nothing for the reset,
which step 1 fixes. A resync before `start()` marks those early reads stale, so
`start()` reads again.

### 5. Embed the rows in the document (lowest priority, only if 4 is not enough)

This also does not fix the reset. It removes the one round trip that step 4
still pays: under 1 ms on a LAN, roughly 30 to 100 ms on a VPS. Build it only if
the empty-world flash still matters on a VPS after step 4.

**Server.**

- `journalState` runs `JournalTail(key, match, rules)` on every `GET /` with
  `Limit: 3` (`handlers/journal.go:432-436`). Raise the limit to the key's `max`
  for the render and reverse the result.
- Marshal each key's rows into a `rows` map on the `bd-journal` config script
  tag with `json.Marshal`, like `bd-state`. Skip a key that would push the total
  past 64 KB; it fetches as today. Never put rows in an attribute.
- Read the cursor before the rows. `MaxJournalID` is read at `pages.go:59`,
  after `siteWithShared` at `pages.go:53`. Move it first, so a row written
  between the reads is in the snapshot and in the stream, and `seen` drops the
  duplicate. Do not use the embedded rows' max id as the cursor; the cursor is
  global across keys.
- Embed only keys in the current schema. The payload stays a `RawMessage`.

**Runtime.**

- The first `load()` takes `conf.rows[key]` if present, deletes it, sets
  `loaded`, calls `add` and queues a draw. Otherwise it fetches.
- Only `start()` consumes the embed. Rematch, a closed match and `_resync` still
  fetch.
- Buffer pushed rows for embedded keys. `receive` returns early before `start`
  (`journal.go:377`) and `_rows` skips unopened keys (`journal.go:532`). With an
  embed, the stream can move the journal cursor past rows the snapshot lacks
  before a slow page script calls `start()`. A `<script type="module">` that
  imports three.js runs after Datastar's module (`site.go:509`), so this is the
  likely case for a 3D page.

**Docs to update.** AGENTS.md journal rules ("A feed reads its rows once", "the
same newest `max` rows the server trims to"), the example rule for restoring
the visitor, and the runtime comment near `journal.go:213`.

## Deferred

- **A code-owned send gate.** `append` could wait for the feed's first load, so
  a page that ignores step 1 still cannot overwrite its own row. Build it if
  model pages keep sending before the restore.
- **`keep_latest: N`**, to keep the latest row per visitor through the trim.
  The server trim is per key across all matches and rules, so the kept rows
  must be scoped to the appended row's match and the key's rules; the client
  `trim()` must mirror the rule; the read must fetch `max + N` rows; N must be
  capped because visitor ids are free. Build it only if the timer and a slow
  presence key prove not enough.
- **`bd.keep`**, a code-owned `sessionStorage` snapshot for tab-local data.
  YAGNI, and it would be a second position store.

## Skipped

- **Datastar persistence.** The vendored build
  (`web/vendor/datastar-v1.0.4.js`, identical to `web/static/datastar.js`) has
  no hits for `persist`, `localStorage`, `sessionStorage`, `replaceUrl` or
  `queryString`. Its plugins are `attr bind class computed effect indicator init
  json-signals on-intersect on-interval on-signal-patch on ref show signals
  style text`. Signals live in JS memory and a navigation drops them. `_bd`
  holds only server ids for resuming the stream (`site.go:253`). Any storage
  helper would also break the prompt's storage rule.
- **Patching the page in place instead of reloading.** `patch-elements` morphs,
  and Datastar runs new `<script>` tags it patches in. The old page's timers,
  listeners, animation loops, WebGL contexts and globals keep running in the
  same realm, so new model code would run on top of the old. Not safe without a
  teardown contract the model cannot be trusted to keep.
- A before-reload flush event (see Decisions).
- A softer rules-bump default.
- WebGL rebuild cost. Giving the shell `background: var(--bg)` would cut the
  white flash.
- `<link rel=preload>` for `/journal`, an ETag on `/journal`, and a cache keyed
  by version and cursor: the cursor moves with every motion row.

## Risks

- **A model ignores the new example.** The reset comes back on that page. The
  deferred send gate is the fallback.
- **A failed first read** is retried once, after a second. If the retry also
  fails, the page stays silent until a pushed row or a resync reads again. On an
  empty world nothing is pushed, so the visitor cannot move others' view of them
  until a reload. Acceptable for a toy.
- **Silent drops** in the embed: reading rows before the cursor, or letting
  pushed rows hit a feed that has not started.
- **Late first paint.** An embed without the size cap delays the body.
- **Teleporting.** A page can append a fake position from the console. The
  reducer needs a speed check.
- **Ghosts.** Rows of visitors who left stay in the window, so the reducer fades
  them out by `at`.

## Tests

The node harness runs the real client runtime with a scripted server and pushed
rows (`lib/webmaster/journal_runtime_test.go`), and `live_test.go` drives the
reload wait. Use them.

**The example (harness, step 1):**

- **Overwrite.** Confirmed before the fix. The example sent the spawn over the
  visitor's row whether the read landed before or after the first tick. The
  restore test is the regression check.
- **Restore.** The row for `bd.visitor` is at `{x: 20, y: 70}`, not the spawn,
  so a wrong `me` shows. Expect no append before the first render, and `me`
  and the first append at `{x: 20, y: 70}`.
- **Fallback.** No row for `bd.visitor`: `me` stays at the spawn `{x: 50, y: 50}`
  and sending starts after the first render.

A drift test in `journal_test.go` checks that the harness copy of the motion
block appears in the prompt whole.

**The runtime load path (harness):** a row or an append pushed while the read
taken at load is out waits in `held` and is folded once. The same holds for a
reload. A resync before `start()` makes `start()` read again, and a failed first
read is retried once.

For the reload wait (harness): a `keydown` within 1.5 s holds the reload, and
the 15 s cap still releases it.

For the `read` fix (Go): with `max` 1000 and 1001 rows present, the read
returns the newest 1000 in ascending order.

For the embed, if built: a gap test (appends during many `GET /`, snapshot plus
pushed rows hold every id), a late `start` test (rows pushed before `start()`
are kept, no duplicates), filter, trim, escaping (`"</script><!--"`
round-trips), cap (an oversized key is absent and `/journal` still serves it),
and a stale-embed test (rematch, closed match and `_resync` fetch).

## Order

Order items 1 to 5 are done, in this order: the overwrite test confirmed the
cause, then the example and prompt with the restore and fallback tests, the
`keydown` reload wait, the `read` fix, and the earlier read. Order item 6, the
embed (plan step 5), is not built.

1. Run the overwrite test to confirm the cause.
2. Fix the example and the prompt, with the restore and fallback tests.
3. The `keydown` reload wait.
4. The `read` fix.
5. Start the read earlier.
6. The embed, only if 5 is not enough on a VPS.

## Not verified

- That `data-persist` and `data-replace-url` exist only in Datastar Pro is from
  memory of the Datastar docs. What is verified is that the vendored 1.0.4 file
  has no persistence code.
- The module ordering race for the embed is plausible from `site.go:509` and
  untested.
- The trim numbers (rows per second, window length) are arithmetic, not
  measured on a real 3D page.

## Open questions

- Real `max` and send cadence for a walking world, to check the 64 KB embed cap
  if the embed is built.
- Whether models follow the new example reliably, or the send gate is needed.
