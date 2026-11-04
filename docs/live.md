# The live page

Status: built. This note is the design record; the code is the truth.

## What it is

The board used to poll `/version` and reload the page when a stamp moved. Now
every open page holds one Datastar stream and the server pushes changes into
it. Commands write; queries read; nothing on the page polls.

## The command side

Every write path publishes a topic after it commits:

- `POST /posts`, `POST /x/state/{key}`, `POST /j/{key}`
- restore, undo, heal, inject and reset
- the worker, when a queued post starts and when a run ends

A topic is a bit in a mask: posts, site, busy, state, journal, turns. Turns is
a finished webmaster run, version or not; only the admin panel reads it. A
topic carries no
data, so a stream cannot apply an event twice or out of order; it only learns
that something changed and reads the current state. A subscriber keeps the
mask of what changed since it last looked, so a burst coalesces into one sync.
The hub never blocks on a slow stream and never drops a topic.

Commands answer 204 for a Datastar request. Datastar follows a redirect and
would patch the board into the document, so the plain-form 303 is only for
requests without the `Datastar-Request` header. A stream from a browser with
no name answers 204, which tells Datastar to stop retrying.

## The query side

`GET /live` is one stream per tab. It reads a cursor from the page's `_bd`
signal:

```
{"_bd": {"version": 4, "post": 9, "journal": 12}, "shared": {...}}
```

The page renders the cursor into the mount, so the first connect and every
reconnect start from what the page already shows. The stream keeps the same
cursor in memory and patches it back after each push. A version of 0 adopts
the current version instead of reloading, or a page that sent no cursor would
reload forever.

On a sync the stream reads only the topics that changed, in this order:

- site: a stylesheet-only change patches `<style id="bd-css">` and calls
  `bdLive.css(id)`; anything else calls `bdLive.reload(id)`,
- posts: new rows prepended into `#bd-posts`, or all posts when the page is a
  batch behind or the ids rewound,
- state: the `$shared` signal plus `bdLive.state(...)`,
- journal: rows after the cursor through `bd._rows([...])`, or `bd._resync()`
  after a rewind or a full 500-row batch,
- busy: the spinner element, from `webmaster.BusyIndicator`.

The cursor is patched last, so a stream that dies mid-sync leaves the page
asking for the same data again. A markup reload leaves the version cursor old
until the page actually navigates, so a stream that dies during the typing wait
asks for the reload again. For posts, `bdLive.posts` drops an id the board
already shows, because a reconnect can arrive after the prepend but before the
cursor patch. A failed read is logged and skipped; the next change to that part
retries it. Only a failed write ends the stream, and the page reconnects with
its cursor.

Known gap: a reset rewinds every id. Open streams hear the reset and resync,
but a tab whose stream was down through it reconnects with old cursors. If new
ids have climbed back to exactly its cursor, it keeps stale posts or markup
until the next change or a reload. A board epoch in the cursor would close
this; for a toy it is not worth the extra signal.

## The page

Datastar is a module script, so it starts after the model's markup has parsed.
The mount is the first element in the body:

```html
<div id="bd-live" hidden
     data-signals='{"_bd":{...},"shared":{...}}'
     data-init="@get('/live', {retry: 'always', retryMaxCount: Infinity,
                retryInterval: 1234,
                filterSignals: {include: /^_bd\./, exclude: /^$/}})"></div>
```

`filterSignals` is what sends the cursor: Datastar's default excludes
underscore signals, and the mount would otherwise send `shared` on every
reconnect. `retry: 'always'` reconnects after a clean close, which is what a
deploy is. The retry interval is jittered at render time so every tab does not
come back at the same instant.

The code-owned live script owns the parts Datastar does not:

- `bdLive.reload` waits while the visitor is typing, saves the draft and the
  reading position, then replaces the page with `/?v=N`.
- `bdLive.css` moves `data-version` and dispatches `beaverdream:update`.
- `bdLive.posts` moves `data-post`, drops duplicate post ids and dispatches
  `beaverdream:post`; posts are already in the DOM.
- `bdLive.state` updates the `bd-state` tag and dispatches
  `beaverdream:state`.
- `beaverdream:stale`, from the journal runtime, reloads the page.

## The admin pages and their streams

The admin panel is pages that stack in one column: `/admin` (overview:
instruction box, charts, compaction budget, memory), `/admin/turns` (a strip
of recent turns over an inspector that follows the newest turn),
`/admin/turns/{id}` (the same strip with one turn held), and `/admin/site`
(current version, versions, journal and state, reset).

The admin lives on its own port, `ADMIN_PORT` (default: the port after
`PORT`, skipping the live reload proxy). The board's port redirects every
`/admin` path there, keeping the host, so `http://192.168.1.5:6930/admin`
becomes `http://192.168.1.5:6932/admin` for everyone on the network. The admin
listener mounts the panel and the assets it loads and nothing else: no board
route, so model-written JavaScript never runs on the origin that holds the
login cookie.

A password, once entered, sets `beaverdream_admin`: a signed expiry, HttpOnly,
SameSite=Strict, host-only, good for 30 days. The login answers with a 303 to
the same path, so the next request has the cookie and the page can render its
CSRF token. Nothing like the token appears in the page or in a URL. A new
`ADMIN_TOKEN` invalidates every remembered login.

Cookies ignore ports, so the board's page does send this cookie to the admin
port. That is harmless for reads (the browser refuses to hand a cross-origin
response to a script) but not for writes, which is why every
cookie-authenticated POST must carry `csrf`: HMAC(ADMIN_TOKEN, "admin-csrf:" +
the cookie's signed expiry), in constant time. The panel puts it in every
action form, and the stream puts it in the fragments that contain forms. The
board cannot read the admin HTML, so it cannot learn the token. A request
authenticated by `X-Admin-Token` skips the check. Login needs none, since it
is one shared identity and still needs the password.

Do not add an `Origin` or `Sec-Fetch-*` check. `Referrer-Policy: no-referrer`
makes browsers report `Origin: null` on form posts, and plain HTTP on a LAN
address sends no fetch metadata at all, so such a check only passes on
localhost.

Any cookie with the login's name is tried and the first one that verifies is
used, so a cookie planted by the board's page cannot lock the admin out.
Signing out clears this browser only; rotating `ADMIN_TOKEN` signs out
everyone. Without cookies the form works but every visit asks again.

A page without a login returns only the password form, which POSTs back to the
same path, so a reload or a new tab lands where it was. Signing out clears
the cookie and shows the form again.

Links between pages keep a real `href`. A plain click pushes the path to
history and fetches it, and the cookie rides along; the page answers with
`#admin-body` patched in place. Back and forward fetch `location.pathname` the
same way. A modified click falls through to the browser, so a new tab works
and is already logged in.

Each page has its own stream, `GET /admin/live?page=...`, plus `&turn=N` on a
held turn. The mount sits inside `#admin-body` with an id per page and opens
the stream with `requestCancellation: 'cleanup'`, so navigating removes the
mount, closes the old stream and opens the new page's. Every stream patches
`#admin-live`, the status card, which moves with every round. The overview's
stream patches `#admin-metrics` and `#admin-memory` when a run ends, and
`#admin-budget` with posts, state and journal rows too, because the page it
measures carries them. The turns stream patches `#admin-history` when a run
ends, including a run that kept the page or failed, and `#admin-turn` only
while it follows the newest; a held turn stays as it was loaded. The site
stream patches `#admin-version` and `#admin-versions` on a new version and
`#admin-feeds` on state and journal writes.

Actions do not redirect either. Their forms submit through Datastar with
`contentType: 'form'`, and the handler answers with the page the action lives
on, its whole `#admin-body` patched in place, flash included: an instruction
answers with the overview, the repairs and reset with the site page. A plain
form post without Datastar renders the same page with a 200, so the panel
still works with scripting off and in tests.

## HTTP/2

A stream per tab is only cheap over HTTP/2. Browsers cap HTTP/1.1 at six
connections per origin, so the seventh tab would stall. The server enables
HTTP/1.1, HTTP/2 and h2c: browsers get HTTP/2 when `TLS_CERT_FILE` and
`TLS_KEY_FILE` are set, and a proxy that terminates TLS can speak h2c to the
plain port. There is no write timeout, because a stream lives as long as its
tab.

## What the model gets

Datastar is the page's front end, so the system prompt teaches it: the
attribute cheat sheet (`data-signals`, `data-on:click`, `data-show`,
`data-text`, `data-class`, `data-bind`, `data-computed`, `data-on-interval`,
`data-init`), the `@post` rule (only `/posts` and `/x/state/KEY`), the
reserved ids, `$shared`, and the three page events. The form example uses
`data-on:submit="@post('/posts', {contentType: 'form'}); el.reset()"`, so the
seed and every restyle keep an in-place composer. A plain form post still
works.

## Evidence in the tree

- `cmd/serve/handlers/live_test.go` opens real streams and checks posts
  prepended and replaced, resume from a cursor, a stylesheet swap, a markup
  reload, a missing cursor, shared state, journal rows and the 204 without a
  name.
- `cmd/serve/handlers/swap_test.go` covers `swapKind` and the hub's coalescing.
- `lib/webmaster/live_test.go` runs the live script in node or bun: reload,
  typing wait, stale reload, posts, stylesheet, state.
- `lib/webmaster/journal_runtime_test.go` runs the journal runtime in node or
  bun: pushed rows, held rows, failed reads, rules changes, matches, resync,
  refusals.
- `cmd/serve/handlers/admin_live_test.go` covers each page's stream and its
  patches, a held turn the stream leaves alone, the turn page, the remembered
  login, the origin gate, the host split, signing out, and an action that
  patches `#admin-body` instead of redirecting. The cookie and password rules
  are in `handlers_test.go`.