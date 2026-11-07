# Worlds, identities and invites

Status: decided, not built. This note records the design. The code is the truth.

## Goal

Beaverdream becomes an instance that holds many worlds, like Minecraft saves. An
admin creates worlds. People join with an invite, get an identity, and play in
any world they can open. A public deployment is invite-only, with a daily token
budget per identity and a global one.

Forking, templates, per-world visibility and spectator mode are deferred. See
[Deferred](#deferred).

Breaking changes are fine. The old visitor cookie, name cookie, admin cookie,
`X-Admin-Token`, `PORT`, `ADMIN_PORT` and the `members` idea are removed with no
migration. See [Removed](#removed).

## Build order

1. Registry file, its migrations, and the identity, login, session, invite,
   usage and world tables.
2. Admin invite page, `POST /invite/{code}` and a GET that only shows a form.
3. The gate middleware on the play port.
4. Replace the visitor cookie with identity ids for posts, holdings, journal and
   cooldowns. Drop the old keys; no migration.
5. Revoke cancels that identity's open streams.
6. Usage and the budget.
7. Worlds: the lobby, admin create and delete, and `/w/{slug}` routing on the
   play port. The first world is created by the admin and is named `heroes`.

Each step ends with `task check` and `go test ./...` passing.

## Two ports, one app

Two listeners, both from the same binary:

- **`APP_PORT`, default 6930**: the management plane. Login, account, API
  tokens, invites, the world registry and the admin pages.
- **`PLAY_PORT`, default 6932**: the boards. Each world's board, its `/live`
  stream, posts, state, journal and error beacon.

`HOST` binds both. The live reload proxy keeps its own port. `Taskfile.yml`,
`docs/configuration.md`, `docs/lan-play.md` and the tests change with the rename.

Pressing play on a world sends the browser from the app port to the play port
through the handoff below.

### What the ports do and do not separate

Cookies ignore ports. On `localhost`, a cookie set by 6930 is sent to 6932 too.
So a board script on 6932 can send a request to 6930 with the app session
attached. CORS stops it from reading the reply, but the request still goes
through. The app therefore needs a CSRF token on every state-changing POST.

- The token is derived from the session with an HMAC. It is rendered into app
  forms only. A board script cannot read it across origins.
- A `GET` never changes state.
- Real isolation needs two different sites, with different registrable domains.
  On `localhost` you get the code path and the tokens, not the isolation. Test
  on two real domains before anything public.

## Identity model

The split follows apato's `identities` and `logins` (Postgres). It becomes
sqlite with text ids.

- **`identities`**: one row per person. `id`, `display_name` (not unique, set by
  the person), `invite_id` (nullable, the invite that made it), `created_at`,
  `disabled_at`.
- **`logins`**: one row per way to sign in. `id`, `identity_id`, `source`,
  `external_id` (the username), `secret_hash` (argon2id), `created_at`,
  `last_login_at`. Unique on `(source, external_id)`. `source` is `password`
  for now. The column stays so another way to sign in can be added as a row.
- **`sessions`**: one row per session token. `hash` (primary key, SHA-256 of
  the token), `identity_id`, `login_id`, `scope`, `created_at`, `last_used_at`,
  `expires_at` (nullable, null means no expiry), `revoked_at`.
- **`identity_roles`**: role rows, like apato. `ADMIN` is the only role for now.
  A partial unique index on `(identity_id, role) WHERE revoked_at IS NULL`
  allows a revoked role to be granted again.
- **`invites`**: `id`, `code_hash` (unique), `label`, `created_by` (nullable,
  null means the admin), `max_uses`, `uses`, `expires_at` (nullable),
  `revoked_at`, `created_at`.
- **`usage`**: `identity_id`, `day` (UTC), `tokens`, and the turn it belongs to.
  Keyed by identity, not by world.
- **`worlds`**: `id`, `slug` (unique), `file` (the path, named by id), `status`
  (`creating`, `ready`, `deleting`), `created_by`, `created_at`.

### Accounts from invites

Redeeming an invite creates an account. The redeemer picks a username and a
password. The server stores one `password` login with the username as
`external_id` and an argon2id hash as `secret_hash`. The identity keeps that
login, so signing in later with the username and password works from any
browser. `invite_id` on the identity records which invite made it.

Password rules and the hashing details are left to the implementer.

### Scopes

`sessions.scope` takes one of:

- `app`: the management plane. Held by a browser after login. Can change the
  account, mint API tokens, create invites and, for admins, create and delete
  worlds. Expires after 7 days.
- `play`: a board. Minted by the handoff. It can post and play, and it cannot
  change credentials. No expiry by default.
- `api`: a script token. Admin identity only for now. Sent as
  `Authorization: Bearer` or `X-Admin-Token`. It skips the CSRF check, since a
  script cannot attach an ambient cookie. Never a query parameter.

Every session check looks up the hash, then checks `revoked_at`, `expires_at`,
and that the identity is not disabled. The scope decides which routes accept
it.

## Admin

- The admin is an identity with the `ADMIN` role in `identity_roles`.
- `ADMIN_PASSWORD` is the bootstrap. On every start the server writes an argon2id
  hash of it into the admin identity's `password` login. The env var is the
  truth: to change the password, change the variable and restart. The UI does
  not offer a password change for the bootstrap login.
- If `ADMIN_PASSWORD` is empty, the server generates one and logs it, as now.
- Only admins create invites, create worlds and delete worlds.
- The admin can play boards like any identity. Its board sessions are never
  refused and never counted toward a budget.
- The API token replaces the password-as-header. It is a random value of about
  32 bytes, shown once when created, stored as a hash in `sessions` with
  `scope = 'api'`, and revocable in one update. A leaked script token is one
  revocable row, not the password. Tests mint one at startup from the bootstrap
  admin.
- No safeguards against the admin disabling itself for now. The bootstrap
  password on start is the way back.

## Login, handoff and play

1. A visitor opens the app and signs in with their username and password.
2. The app sets an `app` session cookie, HttpOnly, SameSite=Lax, host-only.
3. Pressing play sends the browser to the play port with a top-level redirect,
   carrying a one-time handoff code.
4. The play port exchanges the code for a `play` session and sets its cookie.
   The handoff code is single-use and short-lived. The implementer picks where
   it is kept. The `app` cookie never
   reaches the play port's requests, except on localhost, where the CSRF rule
   above applies.
5. Without a play session, a board request gets the gate page. `/live` answers
   204, so Datastar stops retrying.

The handoff adds one redirect the first time a browser opens a board. Later
visits use the play cookie.

## Invites

- Only an admin creates an invite, on the app port, with CSRF. Each one shows
  the link once, with a copy button.
- The code is random, 128 bits, stored as a hash. The lookup is a primary key
  lookup.
- Redemption is a `POST /invite/{code}`. The GET page only shows a form with a
  username field, a password field and a button. A link preview or a prefetch
  cannot use up a use.
- Redemption runs in one transaction:
  ```sql
  UPDATE invites SET uses = uses + 1
  WHERE code_hash = ? AND revoked_at IS NULL
    AND uses < max_uses
    AND (expires_at IS NULL OR expires_at > ?)
  ```
  It requires exactly one affected row. The first statement is a write, so the
  transaction takes the write lock at once. Then it inserts the identity, the
  password login and the first app session. A taken username is refused, and
  the use is rolled back with it.
- A browser that already holds an app session is logged out before the new one
  is set.
- Failures show one message whether the code is unknown, expired, used up or
  revoked.
- Expired invites stay in the list, marked expired.
- Revoking an invite stops future redemptions only. Revoking an identity stops
  its sessions and its streams.

## Revocation

- Revoking an identity sets `disabled_at`, revokes its sessions, and cancels its
  open `/live` streams through the hub. Otherwise a revoked player keeps
  receiving pushes until the page reloads.
- Revoking one session affects one browser only.
- Log out everywhere revokes every session of an identity.
- The check on each request reads the database, so revocation takes effect on
  the next request, with no in-memory set to keep in step.

## Budget

- Unit: tokens, from the round stats the model already reports.
- Window: a UTC day.
- Config (names proposed): `DAILY_TOKENS_GLOBAL` and `DAILY_TOKENS_MEMBER`,
  where 0 means unlimited. Defaults are set, not unlimited. Starting guess: about
  1M per identity and 5M global. A single turn can reach roughly 200k prompt
  tokens before compaction, so a per-identity limit below that allows about one
  turn a day. Tune from real usage.
- Check at post time. A post over budget gets 429 with reason `budget`, and it
  is not stored. Nobody sees a post that will never get an answer.
- A turn already running finishes, so it can overshoot a limit by one turn.
- A batch has several authors. The tokens are split evenly across the distinct
  identities in the batch. The global total takes the whole amount.
- **Usage is written after the world commit, not in the finish transaction.**
  The version lives in the world file and usage lives in the registry, and one
  transaction cannot span two sqlite files. A crash between the two loses one
  turn's charge. That is accepted.
- Admin turns count toward neither budget and are never refused.
- A budget refusal goes to the board as a 429, never to the error beacon. The
  board reads `GET /x/budget` for `{used, limit}`.

## Registry and reset

- The registry is its own sqlite file from the start, even with one world.
  It holds identities, logins, sessions, invites, roles, usage and the `worlds`
  table: one row per world with its id, slug, file path, status and creation
  time. The lobby reads that table.
- A world file holds a world's pages, posts, turns, notes, state and journal.
- No foreign keys cross files. A world can point at an identity id that is
  disabled, or missing from a restored registry. Every world read must handle
  that and show a fallback name.
- Reset only touches the world file. The registry is never reset. Its
  `AGENTS.md` rule becomes: reset clears the world file, nothing else.

## Worlds

- One sqlite file per world. Migrations run per file, and the seed in
  `000001_init.sql` stays frozen.
- Only an admin creates and deletes worlds.
- A `World` struct holds what the handler set holds today: the database, the
  hub, `mu`, the worker and the mutator. A registry loads worlds lazily and
  unloads idle ones.
- The app port lists worlds (the lobby), creates them and shows a play button.
  The play port serves `/w/{slug}/...`.
- The current database, `data/sqlite.db`, is deleted when the new layout lands.
  The owner backs up what they want first. The first world is created fresh
  and named `heroes`.
- Keys that are per world, like the browser store's `bd:save:` names, include
  the world slug. Worlds share the play origin, so a world's script can reach
  another world's endpoints. A path prefix and namespaced saves are enough for a
  toy. Stronger isolation needs subdomains per world, and that is a later
  decision.
- The shared budget and the concurrent model call limit span all worlds.

### Creating and opening a world file

- Create in three steps: insert the registry row with `status = 'creating'`,
  create the file at `<worlds dir>/<id>.db` and run the world migrations, then
  set `status = 'ready'`.
- On start, rows still `creating` are finished or deleted together with their
  files. A crash never leaves an orphan row or an orphan file.
- The file name is the random id, never the slug. The slug lives in the
  registry, so a user-chosen name never touches the path.
- Migrations run on every open, not only at creation. Each file keeps its own
  `goose_db_version`, so worlds sit at different versions until they open.
- The registry and the world migrations are separate sets, in separate folders.
- An idle world closes its database after a timeout. Reopening takes the same
  open path, so the migrations run again and do nothing when current.
- Backups use `VACUUM INTO` or the sqlite backup API. Never copy a live `.db`
  file, because the WAL may hold recent writes.

### Deleting a world

- Set `status = 'deleting'` and refuse new turns and plays for it.
- Close the world's database and cancel its turn in flight.
- Remove the file, then delete the registry row. A crash in between leaves a
  `deleting` row, which start finishes.

## Visitor id and holdings

- The visitor cookie goes. A holding or journal row keys on `identities.id`.
- The display name lives on the identity. The name cookie goes with it.
- `publicVisitor` stops hashing the id.
- A journal append needs a play session. It no longer mints a visitor id per
  request.

## Logging

- Regular application logs, as the server already writes them.
- No audit log for now. It can come later, as its own design.

## Timestamps and transactions

- Times are stored as the text format the existing migrations use
  (`strftime('%Y-%m-%dT%H:%M:%fZ')`). Bind them as text in that format. A
  `time.Time` written by the driver is a different string, and comparisons
  like `expires_at > ?` would go wrong without an error.
- Test one expiry at the exact boundary.
- The busy timeout does not cover a deferred transaction that reads, then
  writes, because SQLite can fail with `SQLITE_BUSY_SNAPSHOT` without waiting.
  Open the registry with `_txlock=immediate`, or start each write transaction
  with a write.
- Ids are 128-bit random values in hex, generated in Go. No autoincrement ids.

## Removed

No migration. These go in the first build:

- The visitor cookie and the name cookie. Holdings and journal rows of the old
  visitor ids are dropped.
- The admin cookie, its CSRF scheme and `X-Admin-Token` as the password. The
  API token replaces the header.
- `PORT` and `ADMIN_PORT`. Use `APP_PORT` and `PLAY_PORT`.
- The signed member cookie, the `members` table, usage in the finish
  transaction, and the constant-time compare for codes.
- The current database, `data/sqlite.db`, and the `main` world. The owner backs
  it up first.

## Tests

Run the real router through `httptest`, with `lib/mockllm`:

- `GET /invite/{code}` ten times leaves `uses` at 0.
- Twenty concurrent `POST`s on an invite with `max_uses = 3` give exactly three
  successes.
- An expired, revoked or exhausted invite is refused. An invite at its exact
  expiry instant is refused.
- A redemption logs out an existing app session in the same browser.
- A revoked identity is refused on its next request, and its open `/live`
  stream closes.
- A forged or unknown session cookie gets the gate page. `/live` without a
  session answers 204.
- A `POST` without a CSRF token on the app port is refused.
- A non-admin cannot create or delete a world.
- A post at the member limit and one at the global limit get 429 with reason
  `budget`, and the `turns` row count stays the same.
- An admin turn with the budget at 0 still runs and writes no usage row.
- A reset leaves the registry untouched: the old play session still works.
- Holdings follow the identity across two different play sessions.
- The bootstrap admin login changes when `ADMIN_PASSWORD` changes between two
  starts.
- An API token works on the app port and is refused on the play port.
- A username and password from an invite sign in from a new browser.
- A world left in `creating` or `deleting` is finished or removed on start.

## Configuration (proposed names)

| Name | Default | Meaning |
|---|---|---|
| `APP_PORT` | 6930 | The app: login, invites, worlds, admin |
| `PLAY_PORT` | 6932 | The boards |
| `HOST` | unchanged | Bind address for both |
| `ADMIN_PASSWORD` | empty | Bootstrap password, written on every start |
| `APP_SESSION_TTL` | 168h | App sessions, 7 days |
| `PLAY_SESSION_TTL` | 0 | Play sessions; 0 means no expiry |
| `DAILY_TOKENS_GLOBAL` | set | Per UTC day, all identities |
| `DAILY_TOKENS_MEMBER` | set | Per UTC day, one identity |

## LAN play

Friends on the LAN open one address, the app port, and a redirect sends them to
the play port. They never type a play URL. That redirect is the sign-in handoff.

## Deferred

Discussed, not agreed to build yet.

- **Forking and templates.** A fork is a new world file seeded from a snapshot
  of a source version. It would copy the page, both schemas, the rules number
  and the notes, plus an origin summary as compacted history. It would not copy
  posts, turns, journal rows, state values or holdings. Open: whether a fork
  keeps the notes, whether any identity can fork any world, and how to keep
  names out of the copied page.
- **Per-world visibility and invites.** Listed, unlisted or private worlds,
  each with its own invite links. Owner powers would go on the app port.
- **Separate sites per world.** Stronger isolation for worlds than a path prefix.
- **Forgotten passwords.** There is no email, so a reset would be an admin
  action. Not designed yet.
- **Rate limits.** None for now.
- **Audit log.** Regular application logs only for now.
- **Logins off for LAN play.** An `AUTH=off` switch was discussed. Giving every
  browser a guest identity is the likely shape, but it is not designed yet.

## Review notes

From an Opus review of this plan. Not yet folded into the sections above.

### Lifecycle

- Open existing worlds with `mode=rw`. The default `mode=rwc` creates an empty
  file, so a request after a delete could rebuild an orphan world.
- Delete and the `creating` cleanup also remove the `-wal` and `-shm` files.
- Closing a world drains its worker and closes its `/live` and admin streams
  before the database closes. Otherwise a finishing turn writes to a closed
  handle.
- Idle unload needs a definition: no subscribers, no queued posts, no running
  turn.
- Store only the world id in `worlds`, and derive the path from a `WORLDS_DIR`
  setting. Add `WORLDS_DIR` to the configuration table.
- Key `bd:save:` names on the world id, not the slug, so a reused slug does not
  load a deleted world's saves.
- Cap loaded worlds, or use a small connection pool per world.
- A post handler reads the registry for its session and budget, then writes the
  world. Never hold a world write transaction while querying the registry.

### Security

- Play cookies are `HttpOnly`. App and play cookies have different names, so the
  handoff cannot overwrite the app session on localhost.
- Every app-port response sends `frame-ancestors 'none'` (or `X-Frame-Options:
  DENY`), so a board cannot frame the app.
- "Press play" is a POST with CSRF, or a GET that only mints the code. The code
  is bound to the identity and the world, and the redirect after exchange drops
  it from the URL.
- Skip CSRF only when the bearer token authenticates. Accept the token in the
  `Authorization` header or `X-Admin-Token`, never as a form field.
- Bootstrap recovery: on start, clear `disabled_at` and grant `ADMIN` again, so
  the password on start is a real way back.
- Rotating `ADMIN_PASSWORD` revokes the admin's existing sessions. Compare by
  verifying the password, not by comparing hashes, since argon2 salts differ.
- Model pages can post with a visitor's play cookie on load, which spends that
  visitor's budget. Decide whether to accept this or refuse posts without a
  code-owned token.

### Budget and data

- `usage` gets a `world_id`, since turn ids are per file.
- Charge rules are missing for compaction, admin instruction turns and retries,
  and for an admin's share of a mixed batch.
- "One turn overshoot" really means one turn per running world, plus posts
  queued before their usage is written.
- A plain form POST that hits a budget refusal gets a raw 429, and the typed
  post is lost. Say what a plain POST gets. `GET /x/budget` also needs a world
  prefix.

### Routes and revocation

- Admin pages per world need a route scheme, such as `/admin/w/{slug}/...`, and
  a rule for which hub the admin streams subscribe to.
- Revoking an identity must reach every loaded world's hub, not just one.
- Revoking one session says whether its streams close.

### Wording

- "No autoincrement ids" applies to the registry only. World `turns.id` is the
  conversation order and needs its sequence.
- The "Removed" section and the Scopes section disagree on `X-Admin-Token`.
  Decide whether it stays, then fix both.
- `DAILY_TOKENS_MEMBER` should drop the word "member". Pick one term for
  "revoke" and "disable".
- Tests need a real play session to post, so say how a test gets one.
- Rewrite the `publicVisitor` comment in `journal.go` to match the new reason:
  one stable id across every world.

## Open

- The default budget numbers.
- The config names above are proposals.
- Forgotten-password reset, as above.
- Password rules, the hashing path, the handoff code's storage, the concurrent
  model call limit and the build plumbing are left to the implementer.
