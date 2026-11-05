# Stalled and broken rounds: diagnosis and plan

A turn sometimes sits for minutes between the model's reasoning and its tool
call, or burns a round on a call that is not JSON. This note records what two
real turns showed, what we know, what we only suspect, and the plan.

## Status

Implemented: items 1 to 7 and 9, and item 8 with a softer number. Not done:
item 0, which needs the live gateway. Where the build differs from the plan:

- Limits are higher than the plan's. Idle limits are 3 min before the first
  event and while thinking, 2 min after a tool call's name (the observed
  silences were about 5 min). The turn wall clock is 1 hour, not 30 minutes,
  and compaction's `summarizeTimeout` is 5 minutes, not 2.
- The watchdog also covers the wait for response headers, from the moment the
  request is sent, so it needs no header hook to arm. A hang before any header
  is cut at the first-event limit.
- A stalled round is retried up to three times in a row, then the turn ends as
  an error. Resamples keep their cap of five.
- A stream with no finish reason is dropped only when it has no calls or a call
  with invalid arguments. A complete call with a missing finish chunk is kept.
- Stop turn is stored as `status=error` with the text "stopped by admin", not a
  new status, so the room log, compaction and the retry button keep working
  (a stopped turn can be retried).
- The admin card shows the time of the last chunk, not a live "quiet for"
  counter. A browser ticker was left out.
- Not done: the timeline strip, the turn-table "longest silence" column and
  timing of the server's own steps between rounds (item 6).

## The turns

Dev config for both: `MUTATION_ROUNDS=16384` for the deepseek provider
(`Taskfile.yml:146`), reasoning effort `low` (`Taskfile.yml:145`, sent at
`lib/webmaster/mutate.go:198`), temperature 0.8 (`mutate.go:197`).

### Turn A: a small tweak, prompt about 31k tokens

| Round | Time | First token | Prompt / cached | Out | Result |
|---|---|---|---|---|---|
| 1 | 39.9s | 4.2s | 31k / 6.1k | 3.2k | resampled: tool markup in the arguments |
| 2 | 5m17s | 1.5s | 34k / 0 | 1.0k | resampled: one `edit` call with 0 chars of arguments, no finish reason |
| 3 | 18.6s | 2.4s | 31k / 31k | 1.6k | eight small `edit` calls, all ok |
| 4 | 5.5s | 5.3s | 32k / 31k | 210 | `done` |

Wall time was about 6.5 minutes. Round 2 was 5 of them.

### Turn #37: replace one game job with another, prompt 41k tokens

Round 1: 6m41s, first token 4.7s, 41k prompt, 0 cached, 9.6k out, 27k chars of
reasoning, one `edit` call with 0 chars of arguments, no finish reason. Resampled.
The admin's estimate of the prompt was 27k tokens (109k chars at 4 chars per
token); the provider counted 41k, so this prompt measured about 2.66 chars per
token. Round 2 was still running when this was written.

## What happened

### Round 1 of turn A: the tool format collapsed

The model batched about ten edits into one response. Its DSML tool markup
(`<｜DSML｜ invoke name="edit">`, `<parameter ...>`) leaked into the `arguments`
string, then turned into JSON-like key/values partway through. The edit values
are HTML and JS full of `<` and `</`, which is the kind of text that confuses an
XML-ish tool parser.

Causes in our code:

- The prompt says to make every edit, then call `done` in the same response, and
  not to use one response per tool (`mutate.go:713-718`). That invites a batch of
  ten or more calls.
- Temperature 0.8 may make a strict format less reliable. DeepSeek's thinking mode
  documents that it ignores temperature, so this is a weak cause.

The loop did the right thing: it dropped the round and resampled (the rule in
`AGENTS.md` about never replaying provider markup). The cost was 40 seconds and
3.2k output tokens.

### Turn A round 2 and turn #37 round 1: one recurring failure

Both rounds have the same shape: normal generation, the `edit` name arrives, then
about 300 seconds of silence, then a clean close with usage and no finish reason.
The arguments never arrive.

The timing shows it. At the 90 to 100 tokens per second measured in turn A:

- Turn #37: 9.6k tokens take about 100s, plus 4.7s to the first token. The round
  took 401s, so about 290s are missing.
- Turn A round 2: 1.0k tokens take about 11s, plus 1.5s to the first token. The
  round took 317s, so about 305s are missing.

An average of 24 tokens per second for turn #37 is not a real rate. It includes
the silence.

The token counts agree. Turn A round 2 made 1.0k tokens from 3.1k chars of
reasoning, about 3.1 chars per token. At that ratio the 27k chars of reasoning in
turn #37 are about 8.7k tokens, which leaves about 0.9k of the 9.6k, inside the
tokenizer's noise. An earlier idea that the provider swallowed a written call is
not supported.

More evidence:

- Turn A round 2 was the same request as round 1 (a resample appends nothing) but
  reported 34k prompt tokens instead of 31k, and 0 cached. The same text with a
  different token count points at a different tokenizer, so likely a different
  backend behind the gateway. A cache is per backend, which explains the 0. Turn
  #37 also had 0 cached on the whole prompt, where turn A's first round cached
  6.1k.
- `stream.Err()` was nil, or `mutate.go:205` would have failed the turn. A usage
  chunk arrived, so something closed the stream cleanly. The missing time was about 290s and
  305s, which may be a gateway limit, but two samples of unequal length do not
  show a fixed one.
- openai-go retries only before a 2xx response, and the first token came within
  5s, so retries do not explain it. A 429 would be an error, not a stall.

The likely story: that backend's tool parser buffered the arguments of a call it
never closed.

This is an inference. The 90 to 100 tokens per second comes from a different
turn, and speed varies. The stored stats do not contain the chunk `model`, the
`system_fingerprint`, the headers, the time of the last chunk, the finish reason or
`reasoning_tokens`, so it cannot be confirmed from the admin panel today. One check
needs nothing new: the phase the card showed during the silence. `RunPhase` returns
"writing edit" as soon as a call name exists (`web/pages/admin_view.go:243`). If
the round sat on "writing edit" with frozen reasoning for about 5 minutes, the
silence is confirmed.

### Gaps in our code

- `complete` treats a stream that ends with no `finish_reason` as a normal end
  (`stream.go:120`, `return out, stream.Err()`). openai-go ends a stream with no
  error on a clean EOF, and `[DONE]` is optional, so a cleanly closed stream
  looks like a finished round.
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
- The admin phase reads "writing edit" for a silent stream once the call name has
  arrived, and "thinking" before that (`web/pages/admin_view.go:237`). It never
  shows that the stream has been quiet.
- `stream.go:97` ignores a chunk that adds no text, so a silent stream and a
  stream of empty chunks look the same and neither is recorded.
- `stream.go` reads only choice index 0 (`:63`), ignores the deprecated
  `delta.FunctionCall`, and ignores every delta field but `reasoning_content`
  (`:127`). A gateway that put arguments anywhere else would look like this
  failure. Unlikely, but the recording item counts them.
- With no `done`, the last assistant `Content` becomes `res.Reason`
  (`mutate.go:368-372`), which becomes `said` and reaches the room log. A
  resampled round's `Content` may carry DSML markup. Unverified: whether
  resampled rounds are in `res.Messages` at that point.
- Compaction holds `h.mu` for up to `compactMaxChunks` = 8 non-streaming calls at
  `summarizeTimeout` = 10 minutes each (`handlers.go:37-40`), which is 80 minutes
  with no watchdog.
- The post queue keeps the newest 10 posts (`handlers.go:935`). Posts dropped
  while a turn stalls are not logged.
- The admin prompt size estimate uses 4 chars per token and showed 27k for a
  prompt the provider counted as 41k. HTML and JS tokenize denser than prose.

### Caching

The prompt layout is not the cause: the system prompt is static, the tool
schemas are deterministic, and the user prompt orders history, the log, then the
parts that change (`mutate.go:1061-1102`). Turn A's first round cached 6.1k, roughly
the system prompt and tools, so the previous turn's prefix had expired or sat on
another backend. `x-opencode-session` is set once per process (`main.go:83`).
Whether the gateway pins an upstream with it is unknown.

### Reasoning effort

Effort is sent as the top-level `reasoning_effort` (`mutate.go:198`) and the dev
task sets `low` for deepseek. Whether the gateway passes it through is unknown.
27k chars of reasoning for turn #37 is a lot for `low`, but the request was a large
change and the reasoning likely includes drafted code.

## Output arithmetic

Rates from turn A: round 1 wrote 3.2k tokens in about 36s and round 3 wrote 1.6k
in about 16s, so roughly 90 to 100 tokens per second.

- Chars per token: 2.66 measured on turn #37's prompt (109k chars, 41k tokens).
- Limits: html 192,000 bytes and css 64,000 bytes (`tools.go:16`, `:15`), 256KB
  in all. At 2.66 bytes per token that is about 96k tokens, about 17 minutes.
- Today's page: 60 to 80KB, so a whole rewrite is about 22 to 30k tokens, 4 to 5
  minutes.

So a 16k output cap, a 180s round wall and a 4 minute turn wall all fail on
today's page, never mind the maximum. Stalls are caught by idleness, not by wall
time. The wall clocks are only a backstop and must be above a legitimate whole
rewrite plus a few rounds.

## Plan

Order: 0 to 4 first (no tuning needed, and 1 supplies the data the rest depends
on), then 5, then 6 to 9. Choose idle limits from item 1's data.

### 0. Two checks that need no code

- Reasoning effort: send "What is 2+2?" through the gateway at `low` and at `high`
  and compare `prompt_tokens`. A third-party write-up reports that `high` adds
  about 79 prompt tokens and `low` adds none, so equal counts would mean `low` is
  being dropped. Unconfirmed against DeepSeek's own docs.
- Upstream pinning: send two requests with the same `x-opencode-session` and
  compare `model`, `system_fingerprint` and cached tokens. If the gateway does not
  pin, the options are a direct provider endpoint or accepting cold prefixes after
  a stall. Both stalls had a cold cache.

### 1. Record what the next stall needs

Per round, stored on `RoundStats` in its own field and logged in `Mutation round
finished`:

- time to response headers, HTTP status, and attempts (count them with
  `option.WithMiddleware`; make the client's retry count explicit)
- the chunk `model` and `system_fingerprint`, the chunk id, the request id and any
  upstream header the gateway adds
- `reasoning_tokens` from usage (`completion_tokens_details`). Completion minus
  reasoning is what the model wrote outside its thinking; a value near zero means
  nothing was written after the call name
- event counts by kind: reasoning, content, tool name, tool args, usage only,
  empty; plus counts of choices with index other than 0, `function_call` deltas and
  unknown delta field names
- the time of the first and last event, the time the tool name arrived, the longest
  gap and the phase it fell in, and whether a finish reason arrived
- the first 2 and last 20 raw SSE `data:` lines, capped at about 8KB
- prompt tokens divided by prompt runes, to show the real ratio and calibrate the
  admin estimate

Raw text never goes into `Message.Content` or `Message.Reasoning`. `Reasoning` is
sent back to the provider as `reasoning_content` (`mutate.go:254`), and `Content`
feeds the fallback reason (`mutate.go:368-372`). `turns.messages` is read only by
the admin (`admin.go:349`), and the `Draft` in `runState` is `json:"-"`
(`handlers.go:84`), so a separate field stays off the board and `/version`.

Do not use `option.WithRequestTimeout`: it would kill long healthy streams.

### 2. Truncation handling

`finish_reason: length` with tool calls is truncation, not a broken sample. Do not
resample it. Reject the calls with a message telling the model to use `edit` or
smaller writes, and count it as a normal round. Do this before touching any output
cap.

### 3. Cap calls per response in the prompt

Change the lines at `mutate.go:713-718`: at most about six calls per response, and
for more, use `set_site` for that field. Do not add an `edits` array tool. It is
one huge argument, which is the same failure, bigger. This targets turn A round 1
directly and is one paragraph. It is less likely to matter for turn #37, which was
one large change, not a batch.

### 4. Keep resampled markup out of the fallback reason

When no `done` arrives, skip resampled rounds when picking the last assistant
`Content` for `res.Reason` (`mutate.go:368-372`). Confirm first whether resampled
rounds are in `res.Messages` there; if not, this item is a test only.

### 5. Idle watchdog per round, with its own retry budget, and the no-finish retry

A stream that ends with no `finish_reason` and no error is a broken stream. It and
a stall share the discard-and-resend path and the retry budget, so they are one
item.

- A missing finish reason must not swallow the no-choices path (`mutate.go:212`)
  and must not throw away a valid round whose only fault is a missing finish
  chunk. Audit test fakes for a missing finish reason first; the mock sends one.
- Each round gets `context.WithCancelCause`, derived from the turn's context. The
  watchdog is a `time.AfterFunc` that cancels with `errStalled`. A check inside the
  read loop would never run, because `NewStreaming` blocks until headers and
  `Next()` blocks on the body.
- Arm the timer after the response headers arrive, using the middleware from item
  1. Before that, openai-go's retry backoff and up to 60s of `Retry-After` would
  count as a stall.
- Reset it on events that grew the draft, not on empty chunks. A stream of only
  keepalives then trips the limit. This changes the "empty keepalive" mock case:
  it should stall, not pass.
- Phases the client can actually tell apart:

  | Phase | Idle limit (start, tune from item 1) |
  |---|---|
  | After headers, before the first event | 30s |
  | While reasoning or content is arriving | 60s |
  | After a tool call name has arrived | 30s |

  The tool limit starts when the name arrives, not at the first argument byte. In
  both observed rounds the name came and no argument byte ever did. Reasoning
  ending cannot be observed until a non-reasoning chunk arrives, so "quiet after
  reasoning" is part of the reasoning limit.
- No tokens-per-second floor. The timer resets only on growth, so a slow but real
  stream is not a stall.
- No per-round wall clock. See the arithmetic. An optional ceiling on reasoning
  with no call, around 120k chars (about 40k tokens, 7 minutes), would catch a
  runaway. Do not set it near the sizes seen: 27k chars was probably legitimate.
- Tell a stall from a real cancel with `context.Cause(roundCtx) == errStalled &&
  ctx.Err() == nil`. Today any stream error fails the turn (`mutate.go:205`).
- On a stall or a missing finish reason: discard the round, wait 1 to 2s with
  jitter, and resend the identical request. A resend may land on a healthy backend
  and keep the cache. No nudge: it changes the message list and gains nothing.
  Partial reasoning is dropped, because it cannot be sent back without its tool
  call.
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
- Turn wall clock: from 2h to 30 minutes, counted after `h.mu`, at both `:948` and
  `mutationContext` (`:1365`). A whole legal page is about 17 minutes of
  generation, so 20 minutes is too tight. Admin turns share it.
- Stored stats for a stalled round: finish reason `stalled`, the phase, the idle
  time, the time of the last event, and usage marked unknown instead of 0.

### 6. Show it in the admin panel

- Running turn card: the phase, time in phase, and time since the last event, so a
  quiet stream reads "writing edit, quiet for 38s". Send the last-event timestamp
  once and let the browser count, so there are no server ticks.
- Finished rounds: a thin timeline of request to first chunk, reasoning, the
  longest gap, tool-call writing and tools; the finish reason; the backend
  `model` and fingerprint; a `stalled` or `resampled` flag with its cause.
- Turn table: longest silence next to first-token time.
- Time the server's own steps between rounds (prompt build, tool execution and
  validation, the finish transaction, compaction), so "the model was slow" and
  "we were slow" separate.
- The prompt size estimate: show the measured chars per token from reported usage,
  or label the estimate as a floor.

### 7. Stop turn button

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

### 8. Bound compaction

Compaction holds `h.mu` for up to 8 calls at 10 minutes each. Lower
`summarizeTimeout` to about 2 minutes. A failed summary already retries next time
and never blocks the mutation (AGENTS.md).

### 9. Log dropped posts

When the queue trims to the newest 10 (`handlers.go:935`), log how many were
dropped and their ids. A long stall makes this likelier.

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
  seen running away, and then fix truncation handling first (item 2).
- Lowering the temperature. DeepSeek's thinking mode documents that it ignores it.
- A per-round wall clock. It fails on a legitimate whole rewrite.
- A tokens-per-second floor. The idle timer already covers a dead stream, and a
  slow real stream is not a failure.
- DSML repair. A strict repair that rewrites a complete, valid call as clean JSON
  would not break the rule against replaying markup, but turn A round 1 collapsed
  into JSON-like text halfway through, and a wrong split puts mangled HTML on the
  page. Revisit only after items 1 to 7, and accept a call only on a strict,
  complete parse.

## Test plan

- `lib/mockllm`: a stream that goes quiet after the tool name arrives; one that
  goes quiet during reasoning; one that sends tool arguments as a single large
  chunk; one that ends with no finish reason; one that sends empty keepalive chunks
  and nothing else.
- The watchdog fires on each phase's limit, with a fake clock; a healthy slow
  stream with steady chunks does not trip it.
- A stalled round is not appended to the conversation, is resent once, and is
  recorded as `stalled`. A second stall spends the retry budget and ends the turn
  with the page unchanged.
- A resample and a stall do not consume `maxRounds`; real rounds still stop at it.
- A real cancel (turn context) is not mistaken for a stall.
- A `length` finish with calls is rejected with a message, not resampled.
- A stream that ends with no finish reason is retried.
- The recorded stats carry `reasoning_tokens`, the tool-name time and the SSE tail,
  and none of the raw text appears in `Content`, `Reasoning`, the next prompt or
  the room log.
- A turn that ends with no `done` after a resampled round does not carry that
  round's markup into `said`.
- `RunPhase` shows the quiet time for a stream that has gone silent after a call
  name.
- Stop turn during the model call, during a tool run and during the finish
  transaction leaves no `running` row and records the stopped status. A stop sent
  with a stale turn id does nothing.
- `task check.go` and `go test ./...` pass.

## Open questions

- Idle limits: the numbers above are guesses. Tune them from item 1's data before
  enabling the watchdog; a thinking model may legitimately go quiet for a while
  on a big prompt.
- Whether the gateway honors `low` reasoning effort (item 0).
- Whether the retry budget should count a stall and a resample together, or
  separately.
- Whether to keep `MUTATION_ROUNDS=16384` for deepseek in dev once the retry
  budget exists, or set it to the prod default so dev matches prod.
