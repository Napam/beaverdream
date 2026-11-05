# Stalled and broken rounds: diagnosis and plan

A turn sometimes sits for minutes between the model's reasoning and its tool
call, or burns a round on a call that is not JSON. This note records what one
real turn showed, what we know, what we only suspect, and the plan.

## The turn

Prompt about 31k tokens. Dev config: `MUTATION_ROUNDS=16384` for the deepseek
provider (`Taskfile.yml:146`), temperature 0.8 (`lib/webmaster/mutate.go:197`).

| Round | Time | First token | Prompt / cached | Out | Result |
|---|---|---|---|---|---|
| 1 | 39.9s | 4.2s | 31k / 6.1k | 3.2k | resampled: tool markup in the arguments |
| 2 | 5m17s | 1.5s | 34k / 0 | 1.0k | resampled: one `edit` call with 0 chars of arguments, no finish reason |
| 3 | 18.6s | 2.4s | 31k / 31k | 1.6k | eight small `edit` calls, all ok |
| 4 | 5.5s | 5.3s | 32k / 31k | 210 | `done` |

Wall time was about 6.5 minutes for a small tweak. Round 2 was 5 of them.

## What happened

### Round 1: the tool format collapsed

The model batched about ten edits into one response. Its DSML tool markup
(`<｜DSML｜ invoke name="edit">`, `<parameter ...>`) leaked into the `arguments`
string, then turned into JSON-like key/values partway through. The edit values
are HTML and JS full of `<` and `</`, which is the kind of text that confuses an
XML-ish tool parser.

Causes in our code:

- The prompt says to make every edit, then call `done` in the same response, and
  not to use one response per tool (`mutate.go:713-718`). That invites a batch of
  ten or more calls.
- Temperature 0.8 makes a strict format less reliable.

The loop did the right thing: it dropped the round and resampled (the rule in
`AGENTS.md` about never replaying provider markup). The cost was 40 seconds and
3.2k output tokens.

### Round 2: a stall, most likely upstream

Evidence:

- Same messages as round 1 (a resample appends nothing), but 34k prompt tokens
  instead of 31k, and 0 cached. Same text with a different token count points at
  a different tokenizer, so likely a different backend behind the gateway. A
  cache is per backend, which explains the 0. Round 3 returned to 31k and 31k
  cached.
- The tool name arrived, the arguments stayed empty, no finish reason.
- `stream.Err()` was nil, or `mutate.go:205` would have failed the turn. A usage
  chunk arrived, so something closed the stream cleanly. About 300s looks like a
  gateway or upstream limit.
- 1.0k completion tokens matches 3.1k reasoning characters. Five minutes of
  generation would be far more.
- openai-go retries only before a 2xx response, and the first token came at
  1.5s, so retries do not explain it. A 429 would be an error, not a stall.

The likely story: that backend's tool parser buffered the arguments of a call it
never closed.

This is an inference. The stored stats do not contain the chunk `model`, the
`system_fingerprint`, the headers, the time of the last chunk or the finish
reason, so it cannot be confirmed from the admin panel today.

### Gaps in our code

- `complete` treats a stream that ends with no `finish_reason` as a normal end
  (`stream.go:120`, `return out, stream.Err()`). openai-go ends a stream with no
  error on a clean EOF, and `[DONE]` is optional, so a cleanly closed empty
  stream looks like a finished round.
- Nothing limits one round below `mutationTimeout` of 2 hours (`handlers.go:43`).
  It is applied at `:948` for the worker and at `mutationContext` (`:1365`) for
  admin turns.
- `maxOutputTokens` is 131072 (`mutate.go:20`). That is deliberate headroom for a
  whole `set_site` (see the arithmetic below), so no output cap fires before a
  wall clock would.
- A resample does `continue` inside `for round < m.maxRounds` (`mutate.go:182`,
  `:325`), so it uses up a work round. With the default `MUTATION_ROUNDS=3`,
  `maxBrokenArgumentRounds = 5` (`mutate.go:25`) can never be reached, and two
  bad samples leave one real round. In dev the 16384 budget hides this and makes
  the cap of 5 the only backstop.
- A stalled round is only counted as a resample if its arguments happen to be
  empty or invalid. A stall with no tool call at all is a different path.
- A `set_site` cut off by `finish_reason: length` has truncated JSON arguments, so
  `argumentsRejected` (`tools.go:89`) counts it as a broken sample and the round
  is resampled (`mutate.go:265`, `:319`). Any new cap makes this common, and the
  retry would truncate the same way.
- A stopped turn is recorded as `status=error` with `chat completion: context
  canceled` (`handlers.go:465`, `:498`). There is no stopped status.
- The admin phase stays "thinking" while the stream is silent, because the phase
  comes from what has arrived (`web/pages/admin_view.go:237`).
- `stream.go:97` ignores a chunk that adds no text, so a silent stream and a
  stream of empty chunks look the same and neither is recorded.
- Compaction holds `h.mu` for up to `compactMaxChunks` = 8 non-streaming calls at
  `summarizeTimeout` = 10 minutes each (`handlers.go:37-40`), which is 80 minutes
  with no watchdog.
- The post queue keeps the newest 10 posts (`handlers.go:935`). Posts dropped
  while a turn stalls are not logged.

### Caching

The prompt layout is not the cause: the system prompt is static, the tool
schemas are deterministic, and the user prompt orders history, the log, then the
parts that change (`mutate.go:1061-1102`). Round 1's 6.1k cached is roughly the
system prompt and tools, so the previous turn's prefix had expired or sat on
another backend. `x-opencode-session` is set once per process (`main.go:83`).
Whether the gateway pins an upstream with it is unknown.

## Output arithmetic

Rates from the turn's own rounds: round 1 wrote 3.2k tokens in about 36s and round
3 wrote 1.6k in about 16s, so roughly 90 to 100 tokens per second.

- Limits: html 192,000 bytes and css 64,000 bytes (`tools.go:16`, `:15`), 256KB
  in all. At about 3.3 bytes per token that is 75 to 85k tokens, 14 to 16 minutes.
- Today's page: a 31k-token prompt suggests 60 to 80KB of page. A whole rewrite is
  about 20 to 25k tokens, 3.5 to 4.5 minutes.

So a 16k output cap, a 180s round wall and a 4 minute turn wall all fail on
today's page, never mind the maximum. Stalls are caught by idleness, not by wall
time. The wall clocks are only a backstop and must be above a legitimate whole
rewrite.

## Plan

Order: 1, 2, 3 first (small, no tuning needed), then 4 to 6, then 7 to 9. Collect
the data from item 3 before choosing idle limits.

### 1. Stream-end handling

- A stream that ends with no `finish_reason` and no error is a broken stream. It
  enters the retry path. It must not swallow the no-choices path (`mutate.go:212`)
  and must not throw away a valid round whose only fault is a missing finish
  chunk. Audit test fakes for a missing finish reason first; the mock sends one.
- `finish_reason: length` with tool calls is truncation, not a broken sample. Do
  not resample it. Reject the calls with a message telling the model to use `edit`
  or smaller writes, and count it as a normal round. Do this before touching any
  output cap.

### 2. Cap calls per response in the prompt

Change the lines at `mutate.go:713-718`: at most about six calls per response, and
for more, use `set_site` for that field. Do not add an `edits` array tool. It is
one huge argument, which is the same failure, bigger. This targets round 1
directly and is one paragraph.

### 3. Record what the next stall needs

Per round, stored on the message stats and logged in `Mutation round finished`:

- time to response headers, HTTP status, and attempts (count them with
  `option.WithMiddleware`; make the client's retry count explicit)
- the chunk `model` and `system_fingerprint`, the chunk id, the request id and any
  upstream header the gateway adds
- event counts by kind: reasoning, content, tool name, tool args, usage only,
  empty
- the longest gap between events and the phase it fell in
- when the last event arrived, and whether there was a finish reason

Do not use `option.WithRequestTimeout`: it would kill long healthy streams.

### 4. Idle watchdog per round, with its own retry budget

- Each round gets `context.WithCancelCause`, derived from the turn's context. The
  watchdog is a `time.AfterFunc` that cancels with `errStalled`. A check inside the
  read loop would never run, because `NewStreaming` blocks until headers and
  `Next()` blocks on the body.
- Arm the timer after the response headers arrive, using the middleware from item
  3. Before that, openai-go's retry backoff and up to 60s of `Retry-After` would
  count as a stall.
- Reset it on events that grew the draft, not on empty chunks. A stream of only
  keepalives then trips the limit. This changes the "empty keepalive" mock case:
  it should stall, not pass.
- Phases the client can actually tell apart:

  | Phase | Idle limit (start, tune from item 3) |
  |---|---|
  | After headers, before the first event | 30s |
  | While reasoning or content is arriving | 60s |
  | While tool arguments are arriving | 30s |

  Reasoning ending cannot be observed until a non-reasoning chunk arrives, so
  "quiet after reasoning" is part of the reasoning limit, not its own phase. The
  60s reasoning limit is the one that would have caught round 2.
- No per-round wall clock. See the arithmetic.
- Tell a stall from a real cancel with `context.Cause(roundCtx) == errStalled &&
  ctx.Err() == nil`. Today any stream error fails the turn (`mutate.go:205`).
- On a stall: discard the round, wait 1 to 2s with jitter, and resend the
  identical request. A resend may land on a healthy backend and keep the cache. No
  nudge: it changes the message list and gains nothing. Partial reasoning is
  dropped, because it cannot be sent back without its tool call.
- Split the budgets. `maxRounds` counts real work rounds. A separate retry budget
  of about 2 covers resamples and stalls. The loop's `round` drives `OnRound`,
  `Draft.Round`, `RoundStats.Round` and the fallback call ids (`stream.go:117`),
  so keep a separate attempt number for those.
- What a spent budget does:
  - A spent stall budget ends the turn as an error and leaves the page alone: a
    stall is infrastructure.
  - A spent resample budget keeps today's behavior, a `silent` turn that names the
    broken arguments. `TestMutateStopsWhenBrokenArgumentsKeepComing`
    (`mutate_test.go:1004`) expects `err == nil`, five rounds and a "not JSON"
    reason, and AGENTS.md says "five such rounds end the turn". If the budget
    changes from five, update both.
- Turn wall clock: from 2h to about 20 minutes, counted after `h.mu`, at both
  `:948` and `mutationContext` (`:1365`). That is above a legitimate whole rewrite
  plus a few rounds. Admin turns share it.
- Stored stats for a stalled round: finish reason `stalled`, the phase, the idle
  time, the time of the last event, and usage marked unknown instead of 0.

### 5. Show it in the admin panel

- Running turn card: the phase, time in phase, and time since the last event. Send
  the last-event timestamp once and let the browser count, so there are no server
  ticks.
- Finished rounds: a thin timeline of request to first chunk, reasoning, the
  longest gap, tool-call writing and tools; the finish reason; the backend
  `model` and fingerprint; a `stalled` or `resampled` flag with its cause.
- Turn table: longest silence next to first-token time.
- Time the server's own steps between rounds (prompt build, tool execution and
  validation, the finish transaction, compaction), so "the model was slow" and
  "we were slow" separate.

### 6. Stop turn button

- One admin action on the running-turn card. Change `cancelRun` from a
  `context.CancelFunc` to a `CancelCauseFunc`, at both places the context is made
  (`:948`, `:1365`), so the stop carries `errAdminStop`.
- The request carries the turn id. The handler compares it with the running turn
  under `liveMu` before cancelling, so a stop that arrives after the turn finished
  cannot cancel the next one. Do not reuse `stopWork`: it bumps `stopped` and drops
  the queue (`:1055`).
- The handler must not take `h.mu`. The worker holds it for the whole turn, so the
  button would wait for the thing it is meant to stop.
- Add a stopped status. The turn records `stopped by admin`, not a model error.
  The finish already runs on a detached context (`:505`) and the deferred
  `FailTurn` (`:415-434`) closes any unfinished turn, so no `running` row is left
  today; the work is the status and the label.
- Compaction runs on `WithoutCancel` (`:578`) and a stopped turn skips it through
  the `context.Canceled` check at `:576`. `WithCancelCause` still yields
  `Canceled`, so that keeps working; test it.
- Same CSRF path as the other admin actions; answer Datastar with `#admin-body`
  patched in place.

### 7. Bound compaction

Compaction holds `h.mu` for up to 8 calls at 10 minutes each. Lower
`summarizeTimeout` to about 2 minutes. A failed summary already retries next time
and never blocks the mutation (AGENTS.md).

### 8. Log dropped posts

When the queue trims to the newest 10 (`handlers.go:935`), log how many were
dropped and their ids. A long stall makes this likelier.

### 9. Check upstream pinning

Find out whether the gateway honors `x-opencode-session` for sticky routing. If
not, the options are a direct provider endpoint or accepting cold prefixes after a
stall. Look at `model` and `system_fingerprint` from item 3 first.

## Not doing

- A Retry round button. The watchdog does the same job, and a manual button races
  the round loop.
- Making heal, undo or restore cancel the running turn. They queue behind
  `h.mu` and apply afterward, which is the right order. Press Stop first.
- Keeping partial reasoning after a stall.
- A nudge message on resend, or switching models automatically.
- A client request timeout.
- Lowering `maxOutputTokens` or the field caps. A whole `set_site` can be 20k
  tokens or more, and the page limits allow far more. Revisit only if a round is
  seen running away, and then fix truncation handling first (item 1).
- Lowering the temperature. DeepSeek's thinking mode documents that it ignores it.
- A per-round wall clock. It fails on a legitimate whole rewrite.
- DSML repair. A strict repair that rewrites a complete, valid call as clean JSON
  would not break the rule against replaying markup, but round 1 collapsed into
  JSON-like text halfway through, and a wrong split puts mangled HTML on the
  page. Revisit only after items 1 to 6, and accept a call only on a strict,
  complete parse.

## Test plan

- `lib/mockllm`: a stream that goes quiet after reasoning; one that sends tool
  arguments as a single large chunk; one that ends with no finish reason; one that
  sends empty keepalive chunks and nothing else.
- The watchdog fires on each phase's limit, with a fake clock; a healthy slow
  stream with steady chunks does not trip it.
- A stalled round is not appended to the conversation, is resent once, and is
  recorded as `stalled`. A second stall spends the retry budget and ends the turn
  with the page unchanged.
- A resample and a stall do not consume `maxRounds`; real rounds still stop at it.
- A real cancel (turn context) is not mistaken for a stall.
- A `length` finish with calls is rejected with a message, not resampled.
- A stream that ends with no finish reason is retried.
- Stop turn during the model call, during a tool run and during the finish
  transaction leaves no `running` row and records the stopped status. A stop sent
  with a stale turn id does nothing.
- `task check.go` and `go test ./...` pass.

## Open questions

- Idle limits: the numbers above are guesses. Tune them from item 3's data before
  enabling the watchdog; a thinking model may legitimately go quiet for a while
  on a big prompt.
- Whether the retry budget should count a stall and a resample together, or
  separately.
- Whether to keep `MUTATION_ROUNDS=16384` for deepseek in dev once the retry
  budget exists, or set it to the prod default so dev matches prod.
