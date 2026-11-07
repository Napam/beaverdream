# Visitor state: things a visitor owns that last

Status: built. Reviewed by general and by deepseek-v4.1-flash before the build.

## Goal

Shared state grows a second scope. A key with `scope: "visitor"` is a count
per visitor: coins, fish, hats, lives, xp. Holdings survive heal and every
rewrite; only reset clears them. Recipes trade counts atomically. The model
keeps one rule: the journal is what happens, state is what lasts.

Cheating is accepted. The server keeps counts non-negative, under their caps
and trades atomic. It does not know whether a visitor won.

## What the model declares

`set_state_schema` keeps its flat list. Items gain optional fields, and a
recipe is one more entry with `op: "recipe"`.

```json
{
  "keys": [
    { "name": "visits", "op": "counter" },
    { "name": "coins", "op": "counter", "scope": "visitor", "label": "Gold", "per_hour": 300 },
    { "name": "koi", "op": "counter", "scope": "visitor", "label": "Koi" },
    { "name": "rod", "op": "counter", "scope": "visitor", "label": "Rod", "max": 5 },
    { "name": "sell_koi", "op": "recipe", "cost": { "koi": 1 }, "gain": { "coins": 150 } },
    { "name": "buy_rod", "op": "recipe", "cost": { "coins": 150 }, "gain": { "rod": 1 } }
  ]
}
```

Recipes live in the same `state_schema` map as keys, so carry, restore,
`sameSiteContent`, the room change summary and the prompt need no new column.
Keys and recipes share one namespace.

- `scope: "visitor"` is allowed only with `op: "counter"`.
- `label` is display text, cleaned like a state value. The name is the id.
- `per_hour` caps how much one visitor can gain in that key in a rolling hour,
  from `add` and from recipe gains alike. The window slides, so the budget
  refills continuously. It is a game tool (lives that refill, a daily bonus),
  not an anti-cheat. Clamped to `maxVisitorPerHour` (1,000,000, enough for an
  idle game ticking every second); omitted means that ceiling.
- `max` on a visitor counter is the most one visitor can hold (today `max`
  means list length and is ignored on counters; the tool text says both).
  Clamped to `maxVisitorHolding` (1,000,000,000,000, inside a JS safe
  integer); omitted means that ceiling.
- One op moves 1 to `maxVisitorAmount` (1,000,000).
- Counts are whole numbers. A page that earns fractions accumulates them
  locally and sends whole ones.
- A recipe names declared visitor keys only, 1 to 4 entries per side, each
  amount 1 to `maxVisitorAmount`.
- `maxStateKeys` goes from 8 to 24 and counts keys and recipes together, so a
  game with a few currencies and several upgrades fits.
- A recipe has one fixed price. Rising prices are several recipes
  (`buy_grandma_1`, `buy_grandma_2`); the prompt says so.

### Holdings outlive their key

Holdings stay under their name whatever the schema says. A later schema may
omit a visitor key (hidden, holdings kept) or declare it again (holdings come
back). The current schema always wins: if it declares `coins` as a global
counter or a recipe, the handlers treat it as that, and the old holdings stay
hidden until a schema declares `coins` as a visitor key again. Nothing is
reserved, so a new game can reuse common names. Hidden keys come from
`SELECT DISTINCT key FROM state_holdings` that the current schema does not
declare as visitor keys; no extra table.

## Tables (world migration)

```sql
-- +goose Up
CREATE TABLE state_holdings (
  visitor TEXT NOT NULL,      -- raw cookie id, never published
  key TEXT NOT NULL,
  amount INTEGER NOT NULL CHECK (amount >= 0),
  name TEXT NOT NULL,         -- display name on the latest write, for $tops
  PRIMARY KEY (visitor, key)
);

CREATE INDEX state_holdings_key ON state_holdings (key, amount DESC);

-- Successful ops only, one row per key changed. The audit log and the
-- rolling-hour window. Rows older than a day are pruned on write.
CREATE TABLE state_ops (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  op_id INTEGER NOT NULL,     -- the rows of one trade share it
  visitor TEXT NOT NULL,
  name TEXT NOT NULL,
  op TEXT NOT NULL,           -- add, spend, trade
  source TEXT NOT NULL,       -- the key, or the recipe name
  key TEXT NOT NULL,
  delta INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX state_ops_window ON state_ops (visitor, key, created_at);
```

The window is `SUM(delta) WHERE visitor = ? AND key = ? AND delta > 0 AND
created_at > ?`. Totals are `SUM(amount)` per key; nothing stores a supply.

## Ops

| Call | HTTP | Does |
|---|---|---|
| `@post('state/coins?add=5')` | 204 | add, capped |
| `@post('state/coins?spend=5')` | 204 | spend, never below 0 |
| `@post('state/buy_rod')` | 204 | recipe |
| `bd.state.add('coins', 5)` | fetch, JSON | same, resolves `{ ok, mine }` or `{ ok: false, reason }` |
| `bd.state.spend('coins', 5)` | fetch, JSON | same |
| `bd.state.trade('buy_rod')` | fetch, JSON | same |

`SetState` branches on the current schema entry:

- global key: today's path, unchanged except the cooldown.
- visitor key: needs `add` or `spend`, else 400. Never the global `+1`.
- recipe: the trade.

Each visitor op:

1. Visitor from the cookie (issued if missing); a display name is still
   required. `allowStateWrite` keys on the visitor cookie for every state
   write, global or visitor, so a looping script is throttled before it
   touches the database. The cooldown stays 250 ms per visitor and key; the
   prompt states it, so a page ticks at most every second.
2. Declared in the current schema, else 404.
3. Amount within 1 to `maxVisitorAmount`, else 400.
4. In one transaction under `stateMu`: every cost is held, every gain stays
   under `per_hour` and `max`. Any failure rolls back the whole op; nothing is
   stored. The answer names the reason: `short` (not enough), `full` (at
   `max`), `cap` (hour budget used), `slow` (cooldown), `gone` (not declared
   now), `bad` (amount).
5. Write holdings (with the name), one `state_ops` row per key, prune old
   rows, commit, publish `topicState`.

## Runtime

`bd.state` joins `bd.journal` in the code-owned runtime.

- `add`, `spend`, `trade` return a promise of `{ ok: true, mine }` or
  `{ ok: false, reason }`, so a shop can say "not enough" apart from "come
  back later".
- No refusal reaches the error beacon, including `gone` from a tab with an
  older schema. `bd.state` does not copy the journal's "409 means stale page"
  branch.
- One chain per visitor: a call waits for the one before it, so a trade and a
  spend cannot race on the shown numbers. The server is atomic regardless.
- The response updates `$mine` at once; the stream confirms it.
- `bd.state.mine` and `bd.state.tops` hold the latest values;
  `beaverdream:state` carries `{ state, mine, tops }`.

## Stream and first render

- `$shared`, `bd-state` and `GET /worlds/{slug}/state` carry global keys only.
  `stateValues` skips visitor keys and recipes.
- `$mine`: this visitor's holdings for every declared visitor key, 0 when
  none.
- `$tops`: per declared visitor key, the top 10 as `{ name, id, amount }`,
  one query with `ROW_NUMBER`. `id` is `publicVisitor`, the same value as
  `bd.visitor`, so a page finds its own row with `r.id === bd.visitor`.
- `liveMount` seeds `$mine` and `$tops` with `_bd` and `$shared`, so markup
  bound to them renders before the stream connects. `pages.go:55` already has
  the visitor.
- `Live` reads the visitor cookie without minting one and keeps the visitor
  and the last `$mine` and `$tops` strings on `liveView`, so a push that
  changes nothing is skipped.
- `$mine` and `$tops` are not underscored, so Datastar sends them with every
  `@post`. That is small at this size.

## Lifecycle

| Event | Global keys | Visitor holdings |
|---|---|---|
| model rewrite | kept | kept |
| heal | cleared | kept, hidden by the seed schema |
| restore | kept, old schema | kept, old schema may hide keys |
| admin clear key | | that key's holdings and ops, event turn |
| reset | cleared | cleared |

Reset deletes `state_holdings` and `state_ops`, and adds `state_ops` to the
`sqlite_sequence` delete.

## Prompt

- The shared state section in `writePage` (uncached tail) lists visitor keys
  with label, total and holders, recipes, and hidden visitor keys that still
  hold anything, one line each. Global values stay as they are.
- System prompt: one paragraph after the state paragraph. Visitor keys,
  `@post` and `bd.state`, `$mine` and `$tops`, one recipe example, "the
  journal is what happens, state is what lasts", names are ids and labels are
  cosmetic, hidden names come back when declared again. Also: counts are
  whole numbers; one write per key per 250 ms; a recipe has one price, so
  rising prices are several recipes; nothing ticks on the server, so a
  timer runs only while a tab is open and two tabs tick twice. Fix the line in
  `changeset.go`: heal clears global state and keeps visitor holdings.
- The tool description names the new fields and `op: "recipe"`.

No new skill in v1. Add one if pages get it wrong.

## Admin

The data page shows visitor keys after the shared state: total, holders,
gained in the last hour, top 10, and one visitor's op log when picked. Clear
key deletes that key's holdings and ops and records a `state` event turn in
the same transaction, under `stateMu`.

## AGENTS.md

- Migrations: pre-production, each database keeps one migration file
  (`000001_init.sql`) edited in place. It freezes at the first production
  deploy; after that schema changes are new numbered migrations. Never edit an
  applied one. Reset clears every table a migration adds and its
  `sqlite_sequence` row.
- Shared state paragraph: visitor scope, recipes, permanence, heal keeps
  holdings, `$mine` and `$tops`, cooldown by visitor.

## Build order and tests

1. The world migration, sqlc queries, reset statements, AGENTS.md rule.
   - a world file opens with its data kept
   - reset leaves every non-seed table empty
2. Schema: `StateKey` gains `Scope`, `Label`, `PerHour`, `Cost`, `Gain`;
   validation and clamps; hidden keys for the prompt.
   - clamps, bad recipes, recipe naming a global key refused
   - hidden then declared again brings holdings back; a schema that reuses a
     hidden name as a global counter leaves the holdings alone
3. Handlers: scope branch, add, spend, trade, cooldown by visitor.
   - caps, max, never negative
   - a trade whose gain hits `max` after its cost passed changes nothing
   - a visitor key never gets the global `+1`; `stateValues` and
     `GET /worlds/{slug}/state` leave visitor keys out
   - a refusal loop is throttled and stores nothing
   - heal keeps holdings; restore of an old global `coins` leaves holdings
4. Stream and runtime: `$mine`, `$tops`, `liveMount` seeds, `bd.state`.
   - two streams with different cookies get different `$mine`
   - first render has `$mine`
   - the `$tops` name survives op pruning
   - in node: calls run in order, each refusal resolves its reason, `gone`
     sends no beacon, `$mine` updated from the response
5. Prompt: page section, system prompt paragraph, tool description. Tests on
   real data, not wording.
6. Admin panel and clear key.
7. Mock model keyword `mockwallet` that declares a visitor key and a recipe,
   so the dev world shows it end to end.
