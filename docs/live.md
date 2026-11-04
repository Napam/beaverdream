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

## The admin stream

The admin panel uses the same idea on a second stream, `GET /admin/live`. It
subscribes to the same hub and patches `#admin-live`, `#admin-version`,
`#admin-versions`, `#admin-memory`, `#admin-history` and `#admin-feeds` as
topics arrive. The status card moves with every round, and history and memory
move when a run ends, including a run that kept the page or failed.

Admin auth is a password, not a session. `GET /admin` without the token returns
only the password form; the form POSTs the token and gets the panel. The token
then lives in the panel's hidden form fields and in the stream's
`X-Admin-Token` request header. It is never accepted from the query string and
never stored in a cookie, because the board runs model-written JavaScript on the
same origin. A reload asks again.

Actions do not redirect either. Their forms submit through Datastar with
`contentType: 'form'`, and the handler answers with the whole `#admin-body`
patched in place, flash included. A plain form post without Datastar renders
the same page with a 200, so the panel still works with scripting off and in
tests. The stream mount sits outside `#admin-body`, so a patch never opens a
second stream.

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
- `cmd/serve/handlers/admin_live_test.go` covers the panel stream, its
  version, history, state and feeds patches, and an action that patches
  `#admin-body` instead of redirecting. The password gate is in
  `handlers_test.go`.
