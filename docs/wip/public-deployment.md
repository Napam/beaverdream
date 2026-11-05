# Public deployment: assessment and plan

Status: proposal. This note is a design record, not a description of shipped
code. The code is the truth.

## Verdict

Running on a LAN with friends is the right use of the current design. The
social context does most of the security work: you know who can post, you can
see the board, and you can reset it.

A small, invite-gated public beta on a throwaway domain is doable. It needs a
day or two of code and deployment work, a provider spend cap, and a moderation
path.

Open anonymous public is not recommended. The remaining risks are the product:
any visitor can make the model write JavaScript that runs in every other
visitor's browser, and the model can be made to write phishing, redirects, or
offensive content. You cannot remove those without removing the feature. If you
do it anyway, do it with the controls below and with someone watching.

## What changes off the LAN

- Trust. Friends who share context become anonymous users with no reason to
  behave. One troll is enough to change the page for everyone.
- The model is the only code author. A visitor does not need to find an XSS
  bug. They can ask the model for a script, and the prompt invites scripts.
- Money. Every post can start a paid model turn. The code has no global budget
  today, and the only throttle is keyed by a free display name.
- Reputation. A single hostile turn can redirect the domain to a phishing page.
  Safe Browsing flags the domain, not the version.
- Liability. You host the posts and the model's output. You need a takedown
  path and a contact.
- Recovery. The current heal button keeps the posts and the model's notes. It
  is not a full reset.

## Assessment

### What holds

The server-side basics are good and should not be weakened:

- Posts and names are escaped. `SiteDoc` replaces `{{name}}` with an escaped
  value and renders post bodies as server markup (`lib/webmaster/site.go:459`).
- The admin panel is a separate listener and renders stored model HTML and CSS
  as text, not as a page. The only `templ.Raw` is code-owned chrome
  (`web/pages/theme.go`).
- The admin cookie is signed, HttpOnly and host-only. Every cookie-authenticated
  POST needs a CSRF token derived from the login (`admin_auth.go:78`).
- The journal validates key, rules and match, caps payload size and trims to
  the declared maximum. Shared state has fixed ops and cannot create keys.
- The live stream is one Datastar connection per tab, with a cursor, and it
  patches only what changed. The hub coalesces bursts instead of dropping them.
- `go test ./...` uses `lib/mockllm`, so the test suite does not need a real
  model.

None of this stops a prompt-injected model, because writing JavaScript is the
feature.

### What the first pass overstated

- "Prompt injection" assumes an instruction boundary. There is none. Visitors
  are supposed to tell the model what to build, and the prompt teaches it
  JavaScript. The model's willingness to refuse is the only filter. Treat the
  page as attacker-controlled JavaScript that runs on the board origin.
- The identity gate being bypassable through `/j`, `/version` and `/x/state` is
  not a security issue for a public board. Those reads expose public data. The
  gate is user experience, not access control.
- `RedirectAdmin` building its target from `Host` is not a browser open
  redirect in practice. A browser attacker cannot set a victim's Host header
  and nothing caches the response. It is a correctness problem behind a proxy
  that rewrites Host or terminates TLS.
- CSRF tokens on board POSTs would not help. The attacker who matters already
  runs on the board origin and can read any token the page renders. SameSite is
  the relevant control, and it is already Lax.
- `SameSite=Strict` on the admin cookie does not separate admin from board.
  Ports do not count toward "site", so the board's scripts can make same-site
  requests to the admin port. The real separation is the separate listener plus
  the CSRF token. Strict is not adding much here.
- Journal payloads rendered with `innerHTML` are a real stored XSS path, but
  they add no capability beyond model-written JavaScript. They matter because
  they need no model cooperation, not because they cross a boundary. Fix cheap,
  do not headline.
- Cookie jar overflow can evict the admin cookie and log the admin out. That is
  an annoyance, and moving admin off the board's site fixes it anyway.
- Client-side storage surviving a heal is mostly not executable. The seed and
  the live runtime restore drafts as textarea values, and stored strings do not
  run by themselves. A service worker would be real persistence; the CSP below
  (`worker-src 'none'`) is what blocks registration, and the invariant keeps a
  second path from opening.
- `ParseForm` accepting 10 MB urlencoded and 32 MB multipart, and the missing
  `ReadTimeout`, are real. A reverse proxy and a one-line `MaxBytesReader` fix
  them. They are not top-ten items.

### What the independent review added

An independent review by Opus 5.5 read the same tree and agreed with most of
the threat model, but changed the ranking and found several things the first
pass missed:

1. `/x/err` has no identity check (`cmd/serve/handlers/journal.go:289`). It
   writes an `alert` turn that the next prompt shows as a visitor-reported
   browser error. The cap is global, five per ten minutes, so a troll can both
   inject text and spend the budget that real errors need.
2. The room log can be forged with names and newlines. Post bodies go into the
   prompt raw (`lib/webmaster/mutate.go:1113`, `lib/webmaster/room.go:99`), and
   only `admin` is reserved as a name (`cmd/serve/handlers/identity.go:54`). A
   visitor named `server` or `you`, or a body containing a newline and
   `#42 server: ...`, can claim server or webmaster authority in the prompt.
3. Recovery blocks behind the model. Undo, restore and heal take `h.mu` without
   calling `stopWork` (`cmd/serve/handlers/admin.go:489`, `:694`,
   `cmd/serve/handlers/handlers.go:1289`). Only reset cancels the run first
   (`cmd/serve/handlers/reset.go:23`). During an attack, heal can wait the full
   two-hour mutation budget, and queued posts then run on the healed page.
4. The journal visitor id is minted per request when the cookie is missing
   (`journal.go:84`). Omitting the cookie gives a fresh rate bucket on every
   append. Each append also takes `stateMu`, writes, trims and wakes every live
   stream, so it is the cheapest write-times-fan-out amplifier in the app.
5. A hostile page can make your own audience attack you. It can run
   `setInterval(fetch('/j/key'))` or reload every second, which turns every open
   tab into load on SQLite. CSP cannot stop same-origin requests. Only
   server-side read limits or caching can.
6. SQLite contention can waste paid turns. The finish transaction has a ten
   second budget against a five second busy timeout (`handlers.go:505`). Under
   a write flood it can fail after the model has already spent the tokens.
7. Disk growth comes from `turns.messages`, not posts. Every finished turn
   stores its full prompt, page included, forever.
8. Permissions persist per origin. If a visitor grants microphone access to one
   fun voice-game version, every later version, including a hostile one,
   inherits it.
9. The admin is a target. The page can identify the admin by name or with a
   cross-site leak against the admin port and show that person a fake login on
   the board origin. The admin password is the asset that matters most.
10. Domain reputation is a real cost. The most likely outcome of open public
    deployment is a redirect or drive-by that gets the domain flagged. Getting
    off Safe Browsing is slow.
11. There is no way to remove one post. The model writes whatever it is asked
    for, and posts are content you host. A takedown request has no tool.
12. A tab that was open through a heal can keep running its old script if it
    has replaced `window.bdLive`. Heal is not a kill switch for open tabs.
13. One invariant worth writing down: never serve model or visitor content
    with a JavaScript or HTML content type from the board origin. That is what
    prevents service-worker persistence and extra model-made pages.

One correction to the review: it estimated about eight model calls per turn
because broken-argument rounds are resampled. In the current loop a broken
round consumes the round budget (`lib/webmaster/mutate.go:182`, `:319`), so the
default is three calls, not eight. The five-broken-round cap only matters if
`MUTATION_ROUNDS` is raised above five. Three calls at up to 131k output tokens
each is still enough to make a budget necessary.

### Residual risk

These stay even after the plan:

- A visitor can ask the model for a page that phishes, redirects, mines, or
  offends. The captured data can always be posted back to the public board.
- The model's output is only as good as the model's judgment. A confident
  hostile post can talk it into almost anything.
- Open tabs can run stale code until they load another page.
- Names are not identities. Nobody can prove who posted what.
- Anything typed on the board is public. Drafts, names and activity included.
- The journal and shared state are trust-based. Games are not fair.
- The domain can be flagged even with every control in place.

## Priority

Rough effort against impact. The order is the review's, adjusted for the
current code.

| # | Change | Effort | Impact |
|---|---|---|---|
| 1 | A hard spend cap at the model provider | minutes | Caps the worst case even if the code has bugs |
| 2 | Admin off the public internet, on its own bind address | small | Removes admin phishing, brute force, cookie games and Host problems |
| 3 | Board on a throwaway registrable domain, not a subdomain of anything you care about | small | Limits same-site exposure and reputation damage |
| 4 | Code-owned CSP, `Permissions-Policy` and `nosniff` headers | small | Blocks external scripts, beacons, LAN fetches, third-party loads |
| 5 | A pause/kill switch that calls `stopWork`, and recovery actions that pause first | small | Recovery takes seconds, not up to two hours |
| 6 | Global spacing between turns plus a daily token budget | small | Predictable cost |
| 7 | Per-IP limits on posts and `/live` connections at the proxy | small | Stops floods that in-app name limits miss |
| 8 | Admin soft-hide post, clear notes, roll back the summary | medium | Moderation and recovery without reset |
| 9 | `/x/err` requires identity and visitor cookies; journal append rejects a missing visitor cookie | trivial | Closes two cheap holes |
| 10 | Quote post bodies in the prompt and reserve `server`, `you`, `browser`, `anon` | trivial | Stops authority spoofing |
| 11 | Shorter mutation timeout and a per-turn token cap | trivial | Bounds the lock hold and one turn's cost |
| 12 | Prune `turns.messages` older than a fixed window | small | Disk |
| 13 | Share one snapshot per publish instead of one read per stream | medium | Only matters at scale; skip for beta |

## Design decisions

### Topology

A separate port gives origin isolation and nothing else. Cookies ignore port,
so the board and admin share a cookie jar and a site. A subdomain gives a
separate host-only cookie jar but is still same-site. A separate registrable
domain gives both. The strongest option is an admin that is not public at all.

For LAN, keep the port split. For public, put the admin on loopback and reach
it over SSH, Tailscale or a VPN. Add an `ADMIN_HOST` setting; today `HOST`
binds both listeners, so `HOST=0.0.0.0` exposes admin on the next port.

### Keep the page unsandboxed?

Yes, for this project. An iframe on another origin would protect the code-owned
shell, but the shell holds almost nothing of value and the visitor still sees
whatever the hostile frame shows. A constrained DSL removes the reason the
project exists. Header CSP is the middle ground: it does not stop inline model
JavaScript, but it stops external scripts, beacons and third-party loads.

For an audience you know, with a throwaway domain, a private admin, a spend cap
and a kill switch, unsandboxed is acceptable. For anonymous open traffic it is
never safe.

### CSP and headers

Add headers from the server, not a meta tag. A meta tag can only tighten a
policy and the model can remove it.

Starting policy:

```
default-src 'self';
script-src 'self' 'unsafe-inline' 'unsafe-eval';
style-src 'self' 'unsafe-inline';
img-src 'self' data: blob:;
font-src 'self' data:;
connect-src 'self';
form-action 'self';
frame-src 'none';
object-src 'none';
base-uri 'none';
worker-src 'none';
manifest-src 'none'
```

Datastar may need `'unsafe-eval'`. Verify that and drop it if it does not. Also
send:

```
Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(),
  usb=(), serial=(), bluetooth=()
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

This policy is a product constraint. `img-src 'self'` blocks hotlinked images.
`worker-src 'none'` blocks Web Workers and service workers. `connect-src
'self'` blocks external APIs. Decide which of those you want to allow before
shipping the policy.

CSP will not stop top-level navigation, `window.open`, CPU mining, or
same-origin exfiltration into `/posts` and `/j`. Phishing still works; the
capture just has to land on your own board.

### Model spend

Use three layers:

1. A prepaid or hard cap at the provider. This is the backstop that survives
   your bugs.
2. A daily token budget in the worker, from a daily counter or the stored
   usage. When it trips, pause the worker and show the admin why.
3. Global spacing between turn starts, at most one every N seconds. This
   smooths traffic and makes the per-name throttle irrelevant.

Per-name and per-visitor limits are decoration against a script. Keep them for
runaway pages, do not rely on them for security.

### Moderation

The primitive is a soft-hide flag on a post. The `posts` view, the room log and
the summarizer must all skip it, so a hidden post leaves both the page and the
model's memory. Add one admin action for hide and one for unhide. This is a
schema change; do it before the public board has data you want to keep, or
change the one-migration rule deliberately.

Add "clear notes" and "roll the summary back to turn X" alongside hide. Notes
are the strongest persistence channel for an injection because they survive
heal, undo and restore.

### Moderation provider

Content moderation needs a first pass that is faster than the generator and
cheap enough to run on every changed page. A System One model such as Jev, or
the model provider's moderation endpoint, can be that pass. Treat it as a
router, not as the policy and not as the final judge. Jev is not in the current
model catalog, so it would be a new external provider dependency.

The house rules are short. Allow edgy, dark, political, blasphemous and
fictional content, ugly art and adult innuendo. Disallow protected-group hate,
incitement, targeted harassment, sexual content involving minors, doxxing,
operational illegal instructions, malware and phishing, real shock gore and
self-harm encouragement. Posts may quote or reclaim a slur. The page never uses
one in its own voice.

#### Interface

Keep the vendor behind a small interface so the policy lives in Go and the
provider can change.

```go
type Moderation interface {
	Check(ctx context.Context, in Content) (Decision, error)
}

type Content struct {
	Source string   // page, post, name, note, summary
	Text   string   // visible text, not markup
	URLs   []string // links, form actions, image and script hosts
}

type Decision struct {
	Categories map[string]float64 // calibrated probabilities
	Severity   float64
	Action     string // allow, defer, hold
	Reason     string
}
```

A no-op implementation keeps LAN and tests independent of the vendor. A Jev
implementation sends the state and the questions below. A provider
implementation maps its generic categories into the same `Decision`. The
mutation loop only knows `allow`, `defer` and `hold`.

#### What to send

For a page, extract visible text from the candidate HTML and include `reason`
and `notes`. Send URLs, form actions and image and script hosts. Do not send CSS
or the whole 192KB document if the context window is tight; send the changed
text and the resulting visible text. Run the check inside the mutation loop,
next to the existing `done` deferrals, before the finish transaction.

For a post, send the body and the display name. For a note or summary, send the
text. For a name, use a small normalized blocklist instead; a model call is
overkill for 32 characters.

#### Questions

Ask atomic questions, not "is this racist". Jev evaluates them in parallel and
each one stays easy to calibrate.

- `is_hate`: dehumanization, inferiority or violence against a protected group.
- `targets_protected_group`: the target is a protected group, not an ideology
  or institution.
- `is_attack`: a slur is used against a person or group, rather than quoted,
  reclaimed or discussed.
- `is_quote_or_critique`: the text condemns or examines the content instead of
  speaking it in the board's own voice.
- `is_harassment`: sustained targeting of a real person.
- `is_threat`: a credible threat of violence.
- `sexual_minors`: sexual content involving minors.
- `is_doxxing`: private address, phone, employer or family of a private person.
- `illegal_instructions`: operational weapons, explosives, drugs, hacking or
  evasion instructions.
- `malware_phishing`: credential capture, malware, miners, drive-by downloads.
- `shock_gore`: real gore or content whose point is to traumatize.
- `self_harm`: encouragement or method instructions.
- `category`: a choice for routing when several questions fire.
- `severity`: a score from 0 to 3 for the admin queue.

The `is_quote_or_critique` question is what keeps a post that condemns a slur
from being treated the same as a post that uses one. It is also the question a
substring filter cannot answer.

#### Thresholds

Start conservative and tune on your own data:

- `p >= 0.98` on a hard category (sexual minors, doxxing, credible threat,
  malware or phishing) holds the page or hides the post.
- `p >= 0.80` on hate or harassment defers `done` if rounds remain, then holds
  the page if the model repeats it.
- Below that, allow and log.

Calibrate on 200 to 500 labeled examples from the LAN and beta. Include edgy but
allowed content, reclaimed slurs, political critique and ironic hate. Measure
the false positive rate on the allowed set before turning on auto-hide. The
independent Jev benchmark found that binary probabilities need tuned thresholds
and that rubric judgments are its weakest area, so do not use one global cutoff
for every category.

#### Failure behavior

- Page gate unavailable: keep the current page and record a `held` turn. Do not
  publish unchecked model output.
- Post check unavailable: store the post, mark it for review, and skip the
  turn. The board stays up.
- Fallback: the provider moderation endpoint or a larger model. Never use the
  generator to judge what it just wrote.
- Log the provider, model version, probabilities, action and a hash of the
  excerpt. Keep the excerpt itself only as long as the moderation log.

#### Jev and the alternatives

Jev is TypeSafe AI's System One model
([introduction](https://typesafe.ai/blog/introducing-system-one-models-and-jev)).
It returns typed choices, scores and probabilities in one parallel pass. Vendor
claims at launch are 70 to 500ms, $0.042 per million input tokens and free
output, in early access. The independent
[benchmark](https://arxiv.org/abs/2609.37647) found strong classification and
well-calibrated probabilities, but weaker fine-grained and rubric judgments. It
is a good first pass, not an authority.

| Option | Speed | Custom policy | Calibration | Cost | Limits |
|---|---|---|---|---|---|
| Jev | 70 to 500ms | Yes, typed questions | Per-question probabilities | Low, early access | Text and structured state; new vendor |
| Provider moderation | Fast | Generic categories | No per-question probabilities | Low or free | Text only; less control |
| Small LLM with the policy | 0.5 to 2s | Yes | Overconfident, needs parsing | Higher | Can be argued around |
| The generator judging itself | Seconds | No | No | Highest | It just wrote the page |

Use Jev at the page gate first. It is where a bad classification is cheap to
defer and where a miss is most visible. Add it to the post worker second. Batch
or sample journal rows, or check them only after a report; do not call it on
every append. Names stay on the blocklist.

#### What it cannot do

The text pass will not see a swastika drawn in SVG, a shock image, text
assembled in JavaScript at runtime, or a page that becomes hateful only after a
script runs. It will not reliably read coded language, reclaimed slurs or irony.
It does not replace CSP, the system prompt, soft-hide, invite revocation or a
human. It also adds a data-processing dependency: visitor text goes to the
vendor, so update the privacy notice, check retention and terms, and do not send
suspected CSAM to a third party.

### Journal and shared state

Do not referee games. It would mean server code per game the model invents
every turn, and griefing a game is part of the fun. Cheap hardening instead:

- Apply the `CleanStateValue` character strip to every string inside a journal
  payload. Keep the JSON shape.
- Require an existing visitor cookie to append. Do not mint one on the append
  path.
- Key the rate limit by IP as well as visitor.
- Prune `lastRewriteAt` and `lastStateAt` the way `journalBucket` already prunes.
- Say in the UI that scores and seats are not authoritative.

### Admin

- Separate bind address, default loopback for public deployments.
- Require `ADMIN_PASSWORD` to be at least 24 random characters. With that, a login
  rate limit is not worth the code; without it, a weak password is the problem.
- Set `Secure` from configuration, not from `r.TLS`, which is nil behind a
  proxy.
- Keep the CSRF token and the separate listener. Do not add `Origin` or
  `Sec-Fetch` checks; the existing docs explain why they break on LAN HTTP.
- Keep the admin panel rendering model content as text. Any new `templ.Raw` on
  stored model output would put the model on the admin origin.

### Recovery

Heal is not enough. A quarantine action should:

1. Pause the worker and drop pending posts.
2. Copy the seed version forward (heal).
3. Clear the model's notes.
4. Delete the summary rows after the last clean turn.
5. Hide posts in a range or by author.

Keep reset for the case where quarantine is not enough. Reset is destructive
and rewinds ids; it should not be the first tool in an incident.

### One invariant

Never serve model or visitor content with a JavaScript or HTML content type
from the board origin. The board page is HTML by design; the invariant is about
separate files and endpoints. If a visitor payload can be fetched as
`text/javascript`, a service worker or a second model-made page becomes
possible.

## Plan

### Phase 0: LAN, now

These are cheap, stay useful, and make the toy safer to leave running.

- Set a provider spend cap.
- Add a pause action that calls `stopWork`. Make undo, restore and heal call it
  before taking `h.mu`, and resume after.
- Require identity and the visitor cookie on `/x/err`; require an existing
  visitor cookie on journal append.
- Quote post bodies in the room log and the NEW POST section, and reserve
  `server`, `you`, `browser` and `anon` as names.
- Lower the default mutation timeout to ten minutes; add a per-turn token cap.
- Prune `lastRewriteAt` and `lastStateAt`.
- Add `ADMIN_HOST`, default loopback, and stop using `HOST` for both listeners.
- Add `MaxBytesReader` to the form routes and a `ReadTimeout` that has been
  tested against `/live`.

### Phase 1: invite-only beta

This is the recommended public mode. It turns "anonymous trolls" into "people
you can uninvite," which is the biggest single change to the threat model.

- Everything in Phase 0.
- Board on a throwaway registrable domain. TLS at the proxy. HSTS.
- Admin on loopback, reached over SSH or Tailscale, not exposed.
- Header CSP, `Permissions-Policy`, `Referrer-Policy`, `nosniff`.
- One invite code per person gates `/identity`. Set a signed invite cookie so an
  invite can be revoked individually. Do not reuse the admin token.
- Global spacing and a daily token budget, with auto-pause when either trips.
- Per-IP limits on `/posts`, `/j`, `/x/state` and `/live` at the proxy.
- The moderation interface with a no-op default, and a page publish gate that
  can defer and hold. Post triage in the worker comes second.
- Soft-hide, clear notes, roll back the summary. A contact address on the
  identity page and in the admin panel.
- Retention for `turns.messages`.
- A written runbook: pause, quarantine, rotate the admin token, restore.

### Phase 2: open public

Only if you accept the residual risk and can respond while it is live.

- Everything in Phase 1.
- Calibrate moderation thresholds on beta data, add a fallback provider, and
  alert on classifier failure or a growing review queue.
- Auto-pause on budget, post rate or journal rate, with an alert.
- A tested incident runbook and someone on call.
- Domain reputation monitoring and a Safe Browsing removal process.
- A real takedown process and legal contact.
- Acceptance that the domain may be burned and that the admin password is the
  most valuable thing you own.

If those are not true, stop at Phase 1.

## Work items

| Item | Where | Phase |
|---|---|---|
| Provider spend cap | provider dashboard | 0 |
| Pause action; `stopWork` before heal, undo, restore | `handlers.go`, `admin.go` | 0 |
| `/x/err` identity and visitor check; journal append requires a cookie | `journal.go` | 0 |
| Quote bodies; reserve names | `room.go`, `mutate.go`, `identity.go` | 0 |
| Mutation timeout, per-turn token cap | `handlers.go`, `mutate.go`, `env.go` | 0 |
| Prune throttle maps | `handlers.go` | 0 |
| `ADMIN_HOST` and separate bind | `main.go`, `env.go` | 0 |
| `MaxBytesReader`, `ReadTimeout` | handlers, `main.go` | 0 |
| Soft-hide post, hide from view/log/summary | migration, queries, `admin.go` | 1 |
| Clear notes, roll back summary | `admin.go`, queries | 1 |
| Moderation interface and no-op provider | `lib/webmaster`, `handlers.go` | 1 |
| Page publish gate with defer and hold | `mutate.go`, `handlers.go` | 1 |
| Post triage in the worker | `handlers.go` | 1 |
| Jev or provider endpoint; calibration set | provider, tests | 1 or 2 |
| Journal batch or report scan | `journal.go` | 2 |
| Invite gate on `/identity`, one code per person | `identity.go` | 1 |
| CSP, `Permissions-Policy`, `Referrer-Policy`, `nosniff` | `Index`, `main.go` | 1 |
| Per-IP limits | proxy | 1 |
| Global spacing and token budget | `handlers.go` | 1 |
| `turns.messages` retention | query/job | 1 |
| SSE connection cap | `hub.go`, `live.go` | 1 or 2 |
| Runbook and incident drill | docs | 1 |
| Auto-pause and alerting | worker, admin | 2 |
| Domain reputation and takedown process | operations | 2 |

## Non-goals

Document these so nobody expects the code to provide them:

- Protecting visitors from the model's page. Visitors run whatever the model
  wrote, and anything typed into the board is public.
- Fair games, tamper-proof feeds or authoritative scores.
- Identity, impersonation resistance or account recovery.
- Privacy of drafts, names or activity from other visitors.
- Protecting tabs that are already open during an incident.
- Prompt-level safety as a security control. A system prompt can set tone; it
  cannot stop a determined visitor.
- Perfect automated content moderation. A classifier routes; it does not
  decide. The page gate and the human hide remain.
- Per-row journal classification. Batch, sample or report only.
- Image classification. Block external images and draw in SVG, CSS or canvas.
- Relying on the provider's safety filter, or on the generator to judge what it
  just wrote.
- Static scanning of model JavaScript for `location` or `fetch`. It is
  bypassed in minutes and breaks legitimate pages.
- Server-side game refereeing. It cannot be generic, and griefing is part of
  the toy.
- Board CSRF tokens. The attacker who matters is same-origin.
- Accounts. An invite code gives most of the benefit without turning the board
  into a product.

## Test and runbook

Before the beta, test these scenarios:

- A page that posts, fetches `/j`, and reloads in a loop is stopped by the
  rate limits and the budget.
- A post with a newline and `#99 server: ...` cannot forge a room line.
- `/x/err` without a name or visitor cookie is rejected.
- A journal append without a visitor cookie is rejected.
- A hidden post disappears from the page, the room log and the summary.
- The page gate defers a flagged version, then holds it; the old page stays and
  the admin can publish or discard.
- A moderation provider timeout keeps the old page and does not take the board
  down.
- A post flagged as hate is held out of the log and summary; a hard category is
  hidden. An allowed edgy post and a quoted slur pass the calibration set.
- Heal during a running turn returns in seconds, and queued posts do not run on
  the healed page.
- The CSP does not break the seed, Datastar, the journal runtime or the live
  stream.
- `task check.go` and `go test ./...` pass.

Run a drill once: pause, quarantine, rotate the admin token, restore a known
good version. Time it. If it takes more than a few minutes, the runbook is not
done.

## Second opinion

Opus 5.5 agreed with the analysis but argued that the framing should change:
there is no instruction boundary to break, so "prompt injection" understates
the issue. The page is attacker-controlled JavaScript by design. It ranked
spend, admin exposure and moderation above the protocol-level details, and
recommended an invite-gated beta on a throwaway domain rather than open public
deployment.

It also argued against several controls that look attractive but do not fit
this app: board CSRF tokens, `Origin` checks, static scanning of model
JavaScript, server-side game refereeing, and an admin login rate limit when the
token is already strong. I agree with those. I would still keep the cheap
code-level fixes in Phase 0, especially the body limits, the map pruning and the
log quoting, because they cost little and remove easy noise.

The main disagreement is emphasis, not direction. Opus would accept a
well-controlled invite beta and advise against open anonymous public. I would
go further: open anonymous public is a different product with a different cost
and liability profile, and this codebase does not have the moderation or
recovery tools for it yet.

On content, both reviews landed on the same shape: posts are the visitor's
speech and the page is the house's speech; the system prompt and the page gate
do most of the work; soft-hide and memory repair matter more than a post
filter; open public needs a human backstop. They differed on post ingestion
(narrow legal checks versus a worker classifier) and on images (an allowlist
versus blocking them). The plan follows the stricter reading: deterministic
hard checks, a worker triage, and no external images.

## Decisions needed

1. Is open anonymous public a goal at all? If not, stop at Phase 1.
2. What is the daily model budget, in tokens or dollars?
3. Is the admin allowed on the public host, or only over a tunnel?
4. Do you want a permanent code-owned contact and report link, even though the
   model can remove it from the page?
5. Are soft-hide and a schema change acceptable before the board has data you
   want to keep?
6. Which CSP restrictions are acceptable: external images, workers, external
   APIs?
7. Who responds to a takedown or an incident, and within what time?
8. Do you want a moderation provider in the invite beta, or human review only?
9. Which provider: Jev, the model provider's endpoint, or both with a fallback?
10. What false positive rate on edgy content are you willing to accept before a
    page is held?
