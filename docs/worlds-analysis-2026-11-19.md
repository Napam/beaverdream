# Where the model fails at building worlds

## Method

Snapshot taken 2026-11-19 from the live deployment: 17 world files under `worlds/` and the
registry, opened read-only with `python3` and `sqlite3.connect("file:...?mode=ro")`. The window
is 2026-11-08T19:38Z (heroes' first turn) to 2026-11-19T17:11Z (gta6's last changeset): 1,752
turns, 448 changesets (440 model turns, 8 admin hand edits in beaver-tower-defense), 2,222
rounds, 4,463 tool calls, 461 versions, 18,302 stored journal rows and 2,979,603 characters of
page (2,980,097 bytes).

What could not be measured: live server logs (only the stats stored on each round survive), any
per-frame or per-visitor trace, and journal payloads that were trimmed away. Three worlds
(fappybird, cuccos-revenge-squad, game-of-thrones-visual-novel) are still on world migration 1,
so 22 changesets there have no `turn_triggers` row.

Two data caveats. The brief's totals were wrong and the files win: 1,752 turns not 1,492, 448
changesets not ~500, 6 errors not 9, 27 kept not 25. The error column also counts two admin
stops as failures, so 6 errors is 4 restarts and 2 stops, not 6 model failures. The `attempts`
field on a round counts HTTP attempts, not loop rounds; all 2,222 rounds are attempt 1, and the
one stalled round (vim 47 round 1) was not retried: the turn went on with a fresh round 2 and
finished `changed`.

Deployed values differ from HEAD in four places that matter here: `COMPACT_AT_PROMPT_TOKENS`
was 375,000 (the page cap it implies is 1,000,000 bytes), the live room log budget was 1,500,000
runes rather than HEAD's 800,000, the live journal key limit was 6 rather than HEAD's 32, and the
notes cap was 1,000 for most of the window. Rejection quotes below are the deployed text, not
HEAD's.

## Worlds at a glance

```worlds-table
heroes|84|4|6|949497|2519|6
beaver-tower-defense|41|0|4|210834|2072|6
minecraft|21|1|2|89574|1747|6
dj-board|10|0|1|168570|1600|3
gta6|75|1|2|298967|1500|5
bark-souls|53|0|1|229620|1497|7
burger-hut|29|0|0|114983|1407|3
cuccos-revenge-squad|5|0|0|65840|1069|4
catwarriors|9|0|1|66407|987|5
counter-strike-1337|39|0|2|284184|983|5
vim|25|0|3|124793|819|4
real-life-mariokart|13|0|0|107447|600|2
exitebike|2|0|0|27825|407|2
game-of-thrones-visual-novel|12|0|1|103061|403|2
fappybird|5|0|0|15794|400|1
temple-os|9|0|4|41442|251|2
daylies|16|0|0|80765|41|2
```

- Page weight is concentrated. heroes 949,497 bytes, gta6 298,967 and counter-strike-1337
  284,184 hold 1,532,648 of the snapshot's 2,979,603 page bytes (51%).
- Data weight is concentrated the same way. heroes 2,519 rows, beaver-tower-defense 2,072,
  minecraft 1,747, dj-board 1,600 and gta6 1,500 hold 9,438 of 18,302 rows (52%).
- Page size and data size diverge. vim keeps 159,416 bytes of journal against a 124,793 byte
  page, so its data is bigger than its program. exitebike keeps 407 rows for a 27,825 byte game.
- Eleven worlds went six days or more without a changeset by the snapshot: beaver-tower-defense
  10.2 days, minecraft 8.2, and nine between 6.1 and 7.0 (fappybird, cuccos, catwarriors,
  game-of-thrones, real-life-mariokart, vim, temple-os, exitebike, daylies).
- Nothing was compacted. The journal is at its row cap in 42 of the 66 declared keys.

## Failure taxonomy

Ranked by what each one costs in wasted tokens, lost data and turns that end with the page
unchanged.

**1. Budget in the wrong unit.** Measured: `room_summaries` is empty in all 17 worlds. The model
was answering one post. heroes turn 322 began with a 1,036,077 byte prompt (33,620 of system and
1,002,457 of user) and its first round was charged 414,425 prompt tokens. A quarter of those bytes
is 259,019, under the 375,000 budget, and the check at `cmd/serve/handlers/handlers.go:1026`
divides an even smaller figure, the room log plus the page, by 4. Three heroes turns (322, 326,
330) are over the charged budget. The harness accepted them, charged them, and left no summary
behind. Why it matters: the guard under-counts the charge by 1.26 to 1.61 across the window, 1.60
on turn 322, and every feature that trusts it inherits the error.

**2. Page grows, never shrinks.** Measured: 3,186 `edit` calls produced about 1.9 MB of
replacement text and 607 KB of deleted text; only 51 edits were pure deletions and 16 pure
appends. 13 of 17 worlds never end a rewrite smaller than they started it. The model adds code to
the module it already has. Inline script is 2,731,608 of 2,980,097 page bytes (91.7%), and heroes
carries one 866,754 byte module. heroes also loads three.js 0.160.0 from `https://unpkg.com`,
the only external URL in any page, instead of the vendored import map the shell provides. Its page
went from 489,046 bytes on the first model write to 949,497, and 312,345 of that arrived in 7
versions with no turn. Neither those writes nor the restore path met the size check: heroes v89
is 933,496 bytes, and v14 is 537,246 against the 524,288 cap then in force. Why it matters: at
93% of the prompt, the page is the thing the model reads most and can restructure least.

**3. Journal used as a bus.** Measured: 1,796,322 rows inserted against 18,302 kept, so
1,778,020 rows were deleted (a reset or a key clear also deletes, so this is rows ever inserted,
not rows trimmed). Four feeds send a whole entity array every frame: counter-strike `bots` 400
rows of 208 bytes, cuccos `flock` 400 of 387, catwarriors `demons` 240 of 140, gta6 `peds` 300
of 164. Together 1,340 rows and 320 KB hold 4 rows of live state, and the cuccos reducer
walks back to the newest row and ignores the other 399. burger-hut `world` keeps 929 of its
1,000 rows from rules 5 to 8 that the current reducer cannot read. The harness trimmed the
oldest row and pushed every row to every open page, with no way for the page to replace its own
last row.

**4. Schema that never lands.** Measured: 4 rejections in 4 turns across 2 worlds. gta6 declared
7 journal keys three times (turns 81, 97, 186) against the then-live limit of 6, and dj-board
turn 10 used the 2 letter key `dj`. gta6 81 and 97 still ended `changed` and shipped without the
schema they asked for; gta6 278 called `done` after its last edit was rejected. Fifteen declared
keys have no rows or no trade: beaver-tower-defense 10 of 12 recipes, gta6 3 recipes plus
`corpses` (max 1,000, 0 rows), game-of-thrones `yard` (0 rows). gta6's own notes call the set
"FULL" while `corpses` is empty. The harness rejected the call, forgot it at the round boundary
(`lib/webmaster/changeset.go:304`), and accepted the next `done`. The model then believes a key
exists that the page cannot write.

**5. State baked in the page.** Measured: 7 of 17 worlds recorded no visitor state op at all
(heroes, minecraft, game-of-thrones, temple-os, exitebike, daylies, dj-board), and two of them
(heroes, dj-board) declare keys they never exercise. Lists are the only store in others: gta6
`table` is 12 of 12 entries and 830 bytes of card moves, real-life-mariokart `laps` is 29
entries and 1,902 bytes, heroes `chat` is 4 entries after 8 days. Content is baked as literals:
heroes 96,589 bytes (a 91,564 byte sound bank), dj-board 28,885, game-of-thrones 4,084 of
dialogue. daylies keeps mode progress in `bd.save('desk')` and has 41 journal rows and 12 scores.
Whatever is baked is re-sent every turn and no store holds it.

**6. Fields cut without a word.** Measured: 69 of 448 turns store notes at exactly 1,000
characters (counter-strike 18 of 39, minecraft 8 of 21, gta6 11 of 75), and 7 more reach 1,993
in beaver-tower-defense, so the cap moved inside the window. Every one of those calls returned
`ok`. Four more fields are cut the same way: `say` (`lib/webmaster/tools.go:279`), `reason`
(`:304`), `reply` (`:308`) and `notes` (`:311`). The model's only memory that survives
compaction loses its tail silently, and it cannot learn the limit.

**7. Crashes read as keeps.** Measured: 4 restart errors, all heroes (turns 6, 13, 14, 139), two
of them storing system and user only and no round. The room line for a failed turn reads
`"(no words)" [no visible change]` (`lib/webmaster/room.go:116`); the error text is admin only.
heroes 14 stored 5 rounds and 7 messages and still wrote no version. The next turn cannot tell a
crash from a deliberate keep, so it either repeats the work or skips it.

**8. Blind edit anchors.** Measured: 16 rejections in 16 turns across 10 worlds: 9 `old text not
found` (8 html, 1 stylesheet) and 7 `matches N places`. Stale: burger-hut 14, 29; counter-strike
43; cuccos 7; daylies 47; exitebike 6; game-of-thrones 41; gta6 278; vim 92. Ambiguous: bark-souls
34, 66; burger-hut 50; counter-strike 20; gta6 190, 229; heroes 14. One anchor was
`requestAnimationFrame(frame)`, 28 bytes. The model copies from a page it edited one round
earlier, and the harness offers one string match, with no line or region.

**9. Cap deadlock on shrink.** Measured: 14 rejections in 6 heroes turns (96, 115, 123, 127, 131
at the 533,333 cap, 59 at 524,288). The check runs on the result (`lib/webmaster/tools.go:357`),
so a shrinking edit is refused like a growing one: turn 96 deleted 777 and 1,994 bytes in
separate rounds and was still told to remove at least 8,018, and succeeded only after deleting
20,718 bytes in one edit. Turn 127 spent 15 rounds and 5 rejections on one 559 to 1,930 byte
edit.

**10. Stop discards the whole round.** Measured: 2 errors, minecraft 48 after 110,110 ms and
gta6 224 after 29,835 ms; both stored system and user only, and neither wrote a version. Nothing
partial lands, so a stop throws away the longest single stretch of model time here.

## Harness levers

| Lever | Today | Where | Taxonomy |
|---|---|---|---|
| `CompactAfterTokens` | 375,000 deployed (200,000 default) | `lib/webmaster/constants.go:40` | 1 |
| `maxPageLen` | 1,000,000 bytes deployed, derived as budget x 4 x 2/3 | `lib/webmaster/constants.go:50` | 1, 2, 9 |
| `roomLogBudget` | 1,500,000 runes deployed (800,000 at HEAD's default) | `lib/webmaster/constants.go:59` | 1 |
| `maxJournalKeys` | 32 (6 in production) | `lib/webmaster/journal.go:17` | 4 |
| `maxJournalRows` / `defaultJournalMax` | 1,000 / 300 | `lib/webmaster/journal.go:19` | 3 |
| `maxJournalPayload` | 2,048 bytes | `lib/webmaster/journal.go:24` | 3 |
| `maxStateKeys` | 64 | `lib/webmaster/constants.go:79` | 5 |
| `maxStateList` / `defaultListMax` | 100 / 20 entries | `lib/webmaster/constants.go:83` | 5 |
| `maxNotesLen` | 2,000 (1,000 deployed) | `lib/webmaster/constants.go:100` | 6 |
| `maxReplyLen` / `maxSayLen` / `maxReasonLen` | 1,500 / 300 / 200 | `lib/webmaster/constants.go:88` | 6 |
| `maxRounds` | 25 | `lib/webmaster/changeset.go:52` | 8, 10 |
| `maxOutputTokens` | 250,000 | `lib/webmaster/constants.go:18` | 2 |
| `IdleLimits` | 3 min first, 3 min thinking, 2 min call | `lib/webmaster/trace.go:37` | 7 |
| `maxSaveLen` | 8,192 chars per name | `lib/webmaster/journal.go:28` | 5 |
| `maxBatchPosts` | 10 | `cmd/serve/handlers/handlers.go:1282` | 5 |

Cheap to raise: nothing that costs prompt bytes. The limits worth touching are the two that
decide what the budget and the page cap mean, and both are computed from a bytes-per-token
guess rather than a measurement. Two levers the data argues against: `maxRounds`, because the
longest turn used 21 of 25 and no turn exhausted the budget, and `maxOutputTokens`, because 0 of
2,222 rounds ended with `finish_reason: length`. Raising `maxJournalRows` would make failure 3
worse by adding rows every page must fold.

## Proposals

Ranked by severity first, then by observed failures removed per unit of effort. S is an hour, M a day, L more.

**1. Let a shrinking write through the cap.** S, one branch.
Evidence: 14 cap rejections in 6 heroes turns; turn 96 shrank twice and was still refused.
Change: in `lib/webmaster/tools.go:357`, reject only when the result grows past the cap; keep
`checkPageSize` (`lib/webmaster/tools.go:629`) for whole writes.

```
ok (shrunk 1994 bytes to 1001994; still 1994 over the 1000000 cap)
rejected: the page is 1002400 bytes and this edit adds 2400; shrink it or set_site a smaller page
```

Cost and risk: one size comparison; a page can end a turn over the cap while it shrinks.
Worked when: cap rejections fall from 14 in 3,186 `edit` calls to 0.

**2. Infer the edit target.** S, one lookup.
Evidence: 9 `unknown target ""` rejections in 4 turns (bark-souls 90, gta6 242, heroes 309, vim
10); heroes 309 sent all 5 edits without the field.
Change: at `lib/webmaster/tools.go:332`, before rejecting, look for `old` in each field and apply
it when exactly one field contains it.

```
ok (target html: old text matched once in CURRENT HTML)
rejected: target is css or html, and old text was found in neither
```

Cost and risk: one extra match on a path that already matches twice; clean and the cap still run.
Worked when: rejections starting `unknown target` fall from 9 to 0.

**3. Carry a rejection across rounds.** S, one counter.
Evidence: 3 turns ended without fixing a rejection (gta6 81, 97, 278); 41 of 44 were fixed in the
same turn.
Change: keep the rejection flag for the turn instead of clearing it at
`lib/webmaster/changeset.go:304`, and let the deferral at `lib/webmaster/changeset.go:334` read
it once.

```
not finished: round 4's edit was rejected and never fixed. Fix it, or call done again to keep the page as it is.
```

Cost and risk: one field and one condition; a second `done` still keeps the page.
Worked when: turns ending with an unrepaired rejection fall from 3 to 0.

**4. Say what was cut.** S, four return strings.
Evidence: 69 of 448 turns store notes at exactly 1,000 characters and 7 more reach 1,993; none
was told.
Change: return the stored length when `truncateRunes` fired, at `lib/webmaster/tools.go:279`,
`:304`, `:308` and `:311`.

```
ok (notes cut to 2000 of 2530 chars)
ok (reply cut to 1500 of 2110 chars)
```

Cost and risk: four strings; nothing else moves.
Worked when: notes written at exactly the cap fall, and no turn sends a 2,500 character note
twice.

**5. Put the failure in the room line.** S, a column, a field and a branch.
Evidence: 4 restart errors (heroes 6, 13, 14, 139), 2 with no stored round, all reading
`"(no words)"`.
Change: at `lib/webmaster/room.go:118`, use the stored error text as `said` on an error turn,
clipped to the reason length.

```
"the turn failed: interrupted by a restart" [no visible change]
```

Cost and risk: a room-log column, a `Turn` field, its mapping and the branch; players see that a
turn failed, which the unchanged page shows anyway.
Worked when: error turns reading `"(no words)"` fall from 4 to 0.

**6. Name the notes budget where notes are read.** S, one line.
Evidence: 374 of 448 turns rewrote notes; counter-strike paid 1,000 output characters on 18 of
39 turns; 69 hit the cap.
Change: after the notes block at `lib/webmaster/changeset.go:1517`, print the length and the
keep rule.

```
Notes: 978 of 2000 chars. Keep them. Do not restate constants the page already holds.
```

Cost and risk: one line per prompt after the cached prefix, so no cache shift.
Worked when: notes carried unchanged rise above 74 of 448 and notes at the cap fall.

**7. Read back the stored schema.** M, one formatted result.
Evidence: 15 declared keys with no rows or no trade (beaver-tower-defense 10 of 12 recipes, gta6
3 recipes plus `corpses`, game-of-thrones `yard`).
Change: return the cleaned schema instead of `ok` at `lib/webmaster/tools.go:203` and `:247`.

```
ok: 6 keys, corpses max 1000, moves max 400
```

Cost and risk: one string per schema write (65 journal and 22 state writes in the window).
Worked when: keys left with 0 rows after a schema write fall below 3.

**8. Replace this visitor's last row.** M, one option and one delete.
Evidence: 1,796,322 rows inserted, 18,302 kept; 1,340 rows across 4 snapshot feeds for 4 rows of
live state; 42 of the 66 keys at their cap.
Change: accept a `replace` option in the append runtime at `lib/webmaster/journal.go:469` and,
under the same `stateMu` at `cmd/serve/handlers/journal.go:152`, delete that visitor's earlier
rows on that key, so ids stay monotonic.

```
feed.append(payload, {replace: true})  // keeps your newest row on this key, drops your older ones
```

Cost and risk: one runtime flag and one delete beside the trim; a feed that needs history loses
it.
Worked when: rows inserted per row kept falls from 98 to 1 toward 10 to 1 on the four snapshot
feeds.

**9. Measure the budget in charged tokens.** M, one ratio.
Evidence: heroes turn 322: 1,036,077 prompt bytes, server estimate 259,019, charged 414,425
(2.50 bytes per token); 0 summaries in 17 worlds.
Change: replace both divide-by-4 estimates, the page guard at
`cmd/serve/handlers/handlers.go:987` and the budget check at `:1026`, with the world's own
measured bytes per token (kept beside the budget at `lib/webmaster/constants.go:40`), floored at
2.0.

```
budget check: 1036077 B / 2.50 B per token = 414431 estimated tokens, over the 375000 budget
```

Cost and risk: one ratio per world kept from `RoundStats.PromptTokens`; compaction then fires on
heroes every turn while the page alone is about 380,000 tokens, so it cannot reach the budget.
Worked when: the first `room_summaries` row appears, and the logged estimate lands within 10% of
the charged count on the same turn.

**10. Rebase the page cap on measured tokens.** L, every big page must shrink.
Evidence: heroes 949,497 bytes is 95% of the 1,000,000 cap and about 380,000 charged tokens, 1%
over the whole 375,000 budget; the page is 93% of the prompt at turn 330.
Change: derive the cap at `lib/webmaster/constants.go:50` from the budget, the measured bytes
per token and the 2/3 page share, and print both units at
`lib/webmaster/changeset.go:1005`.

```
The two fields share one size cap: css plus html may be at most 625000 bytes
(about 250000 tokens, two thirds of the prompt budget).
```

Cost and risk: heroes must lose about 34% of its page in one write; no turn in the snapshot
failed on context, so this buys headroom rather than a fix.
Worked when: page_max per world is under the cap and charged prompt tokens under budget on the
same turn.

**11. Checkpoint a feed above a rules change.** M, one row per key.
Evidence: burger-hut `world` 929 of 1,000 rows belong to retired rules; minecraft `blocks`
269 of 562 and `drops` 6 of 572 at rules 1; every new page folds the window on load.
Change: let a page write one folded-state row per key and rules number, and read it before the
history, at `lib/webmaster/journal.go:469` and the feed start at `lib/webmaster/journal.go:507`.

```
feed.checkpoint(state)  // one row the next page reads before folding history
```

Cost and risk: a second row shape in every feed and a rule for when it is stale. The data does
not justify this yet: 3 feeds show abandoned rules and only vim `doc` needs history.
Worked when: rows folded on first load per key fall.

**12. Server save per identity.** L, new store.
Evidence: 7 worlds with 0 visitor state ops; daylies keeps progress in `bd.save('desk')` with 41
journal rows and 12 scores in its life; heroes `chat` has 4 entries after 8 days.
Change: put a server-backed store behind `bd.save` at `lib/webmaster/journal.go:626`, keyed by
identity and world id.

```
bd.save('run', value)  // kept on the server for this identity, so it survives a new browser
```

Cost and risk: a table, a write path, a cap and a reset rule for data no page has asked for. Not
justified yet: 1 to 6 writers per feed and no case of one visitor on two devices.
Worked when: saves written per world, and a save survives a reset.

## Evidence index

- `$S/findings/00-report-spec.md`: structure and check contract.
- `$S/findings/01-outcomes.md`: turn outcomes, error taxonomy, 44 rejections, round shape,
  prompt growth per world.
- `$S/findings/01-outcomes-turns.tsv`: all 448 changesets with derived columns.
- `$S/findings/01-outcomes-rounds.tsv`: all 2,222 rounds with prompt, cache, finish and gap.
- `$S/findings/01-outcomes-rejections.txt`: every rejection with its full arguments.
- `$S/findings/01-outcomes-errors.txt`: the 6 error turns and their round shape.
- `$S/findings/02-data.md`: page growth, journal churn and shape, state, notes, budget.
- `$S/findings/02-data-versions.txt`: every version, its size, delta, turn and change kind.
- `$S/findings/02-data-pages.txt`: page composition per world (scripts, literals, markers).
- `$S/findings/02-data-journal.txt`: every journal key with rows, cap and payload fields.
- `$S/findings/02-data-churn.txt`: churn, state, notes, prompt budget and post counts.
- `$S/findings/03-harness-map.md`: lever table, prompt map, tool text, feedback audit, edit sites.
- `$S/inventory.tsv` and `$S/worlds.tsv`: the per-world numbers in the table above.

## Open questions

- Should the page cap be cut to the token budget now, when heroes must lose 34% of its page and
  no turn has failed on context yet?
- If compaction is measured in charged tokens, what should it do for a world whose page alone is
  over the budget, where summarising the room frees almost nothing?
- Should the model see its own truncated fields and rejected schema next turn, or is naming the
  cut in the tool result enough?
- Is a journal checkpoint worth its second row shape for 3 feeds with retired rules, or would a
  per-visitor row cap on those keys do the same work?
- Should the room line show a failed turn to players, or only to the admin, given that the page
  already shows nothing changed?
