# The journal

Status: built on the `journal` branch. This note is the design record; the
code is the truth.

## What it is

A message board where the model writes small client-side programs over a
shared, public, append-only event log. The server keeps the log and knows
nothing about what any program means. A visitor asks for a game and the board
gains one.

The invariant: the backend stays fixed. No runtime Go, no model-written
handlers, no model-owned tables, no SQL from the model. The server grows one
channel, not a feature per request.

## What was built

- `journal` table: `id, key, visitor, payload, match, rules, created_at`, one
  index on `(key, id)`.
- `site_versions.journal_schema`: the model declares keys with
  `set_journal_schema(name, max, rules)`.
- `POST /j/{key}` appends one JSON payload. The server stamps the browser's
  opaque visitor id, the match number and the rules number from the schema.
  The body carries the rules number the page's code was written for; a
  different one is a 409, so a tab running a replaced reducer cannot write
  under rules it does not follow.
  The id in a row is derived from an HttpOnly cookie, never the cookie itself,
  so a published id cannot be copied into another browser to take a seat.
  Match may stay or advance by one; a visitor cannot jump or rewind. Writes are
  serialized by `stateMu`, allowed in bursts by a token bucket, trimmed to the
  declared `max`, and count as board activity so a heal does not wipe a game.
- `GET /j/{key}?since=N` returns rows after a cursor plus the newest match and
  the declared rules number.
- `bd.journal(key)` client runtime: `start(reduce, render)`, `append`,
  `newMatch`, a cursor, an 800ms poll, and `bd.visitor`. It is code-owned, so
  the model writes about 40 lines of rules instead of a whole client.
  - The rules number comes from the page config and never changes in the tab.
    When a read or an append reports other rules, the feed stops and sends
    `beaverdream:stale`; the live script reloads the page. A new match only
    reloads the feed.
  - A failed read leaves the board as drawn. A poll stays busy until the load
    it started settles.
  - `append` resolves with the row, or with null when the server refused it
    for a reason that is not the page's bug: a closed match reloads the feed,
    409 reloads the page, 429 and 401 drop the write. A payload the server
    rejects is reported. Only a payload that cannot be serialized throws,
    before any request is sent.
- `POST /x/err` turns a thrown script into one `alert` turn per message per
  stretch. The beacon carries the page's version, and an error from a page
  that is no longer current is dropped. It sends no line number: that counts
  lines of the served document, which the model never sees. The room log
  labels it as visitor-reported. A beacon never triggers a turn.
- The features guard: a `done` whose page no longer calls `bd.journal("key")`
  for a declared key is deferred once, so a restyle cannot orphan a game. The
  bare key in prose does not count.
- The rules floor: `set_journal_schema` reads the highest rules number stored
  under every key, declared or not. A bump, a new key, or a key declared
  again after it was dropped skips past it, so a restore cannot replay an
  older reducer's rows. Keeping the declared number keeps the old rows.
- The admin panel shows every declared key with its row count, match, rules
  and newest three rows, plus the current shared state values, so a stuck feed
  can be read without sqlite. It is read-only.
- The prompt shows each key's match, rules, row count and the newest three
  rows of the current match under the current rules, with id and visitor.
- Heal clears the shared state and the journal in the same transaction as the
  seed version it writes.
- A page with a live feed sets `data-bd-live`; the live script stops reloading
  on new posts and dispatches `beaverdream:post` instead.

## Where it stops

Public, deterministic, turn-based things. Not secrets, not real-time action,
not authority people care about. The log is public, so hidden moves need
commit-reveal the prompt has to hand the model. The reducer is the referee:
illegal rows are ignored by every client, not rejected by the server.

## Evidence in the tree

- `lib/mockllm/game_test.go` runs the shipped tic tac toe applet in node and
  checks a win and an out-of-turn move.
- `cmd/serve/handlers/journal_test.go` covers append and read, unknown keys,
  match boundaries, trimming, visitor assignment, the alert dedupe, and a
  whole `mockgame` post that ships a working feed.
- `lib/webmaster/journal_test.go` covers the schema, the guard, the payload
  limits and the `done` deferral.

## Known gaps

- One journal key cannot be created by a visitor, so a rematch is a boundary
  row inside a key that grows. `max` trims the oldest rows, which is fine for a
  match but would cut a long chat.
- The prompt shows the newest three rows of a match, not a game state.
- A model that changes `reduce` without bumping `rules` will replay old rows
  through new code. The prompt says to bump it, and the tool rejects a lower
  number, but forgetting to raise it is still possible.
- The error beacon has one global cap of five messages per ten minutes, so a
  vandal can spend it and hide real errors for that stretch. The version
  check only drops errors from replaced pages; a vandal can still claim the
  current one.
- `bd-state` is a snapshot from page load. A live page does not reload on
  posts, so the prompt tells the model to refetch `/x/state` on
  `beaverdream:post`.
