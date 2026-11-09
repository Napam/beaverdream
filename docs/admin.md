# Admin panel

The admin panel is code-owned. The model never writes it. It lists a world's
current version, its CSS and HTML, and the version history. It can restore an
old version, undo the last change, heal the site, or reset it. It shows the
recent changesets with their prompts, tool calls and results. It also
has a **talk to Naps** box that sends an instruction straight to the model, and
a data page for shared state, visitor counters and journal feeds.

The app also has pages for accounts, invites and the world registry. Only an
identity with the `ADMIN` role can create or delete worlds and create invites.
An admin can make another account an admin, or take the role back, from the
accounts page.

## Where it runs

The admin lives on the app port, `APP_PORT` (default `6930`), not a separate
admin port. The play port never serves an admin page. That keeps the model's
scripts off the origin that holds the app session.

- `/admin`: the world list, the create form, and a delete button per world.
- `/admin/invites`: invite creation and the current invites.
- `/admin/accounts`: the accounts, with make admin, remove admin, reset password
  and disable buttons. The system admin is marked and has no destructive or
  credential controls.
- `/admin/model`: the registered skills and the tools the webmaster can call,
  read from the same definitions the model requests use. Skill bodies open in
  place; tool parameter schemas are shown as JSON.
- `/admin/worlds/{slug}/...`: one world's changesets, site tools and data page.
  `/admin/worlds/{slug}/changesets`, `/admin/worlds/{slug}/site` and
  `/admin/worlds/{slug}/journal` are the main ones. The site tab holds the
  current page, its versions, a rename form, and a danger zone with reset and
  delete for that world. A rename changes the URL only: the id, file, history,
  state and play sessions stay, but old links stop working, and an open tab
  keeps the old address until the visitor goes to the new one.
- The header links back to the back office (`/admin`) and to the world's page
  (`open world ↗`).
  On a world page, the world name in the breadcrumb is a switcher when there
  is more than one world.
- The user pages are the warm surface: the lobby, the account, sign-in and the
  invite form. An admin gets a **back office** pill there that opens `/admin`.
  The back office keeps the neutral gray chrome, and its header links back to
  the lobby.

There is no `ADMIN_PORT`, no separate admin hostname, and no play redirect to
an admin port.

## Password

- The admin is an identity with the `ADMIN` role. Its username is `admin`, and
  it signs in with that username and the bootstrap password. Its identity id is
  all zeroes (`00000000000000000000000000000000`), so it is recognizable and
  protected: no account, including itself, can disable, demote or reset it.
- Other admins are ordinary identities with the `ADMIN` role. The system
  admin can make and remove them from `/admin/accounts`.
- `ADMIN_PASSWORD` is the bootstrap. On every start the server writes an
  argon2id hash of it into the admin identity's `password` login, clears any
  disabled state, grants `ADMIN` if it is missing, and moves an older
  random-id admin account onto the zero id. The move rewrites the registry's
  references. Post authors, visitor counters and journal rows already stored
  under the old id in world files stay with that old id.
- To change the password, change `ADMIN_PASSWORD` and restart. That revokes the
  admin's existing sessions. The UI does not change the system admin login.
- If `ADMIN_PASSWORD` is empty, the server generates one and logs it as a
  warning. A restart makes a new password. A chosen password must be at least
  8 characters.

## Password resets

An admin can issue a one-time reset link for an enabled account that has a
password login. The link is valid for one hour and shown once. Issuing another
link for the same login makes the older one stop working. The app does not send
it; copy the link and give it to the person.

The link opens `/reset/{code}`, where the person chooses a new password.
Redeeming it changes that login's password, revokes every app, play and API
session, deletes outstanding handoffs, and closes that identity's open streams.
The app does not sign the person in afterwards. A used, expired, unknown or
ineligible link shows the same refusal page.

The system admin is never resettable. Change `ADMIN_PASSWORD` and restart
instead.

## Sessions

`GET /admin` without an app session shows the login form. Signing in sets an
app session cookie:

- `HttpOnly`, `SameSite=Lax`, host-only.
- Lasts `APP_SESSION_TTL`, 7 days by default.
- Stored as a hash in the registry, so revoking it takes effect on the next
  request.
- `Secure`, and named `__Host-beaverdream_app`, in secure cookie mode: both
  `APP_URL` and `PLAY_URL` are `https` origins behind a proxy, or the binary
  serves TLS from `TLS_CERT_FILE` and `TLS_KEY_FILE`. LAN mode keeps the plain
  name and no `Secure`.

App and play cookies have different names, so the play handoff cannot overwrite
an app session on localhost.

A browser whose app cookie already authenticates gets a page saying it is
already signed in, with a sign-out form, on `POST /login`, `POST /invite/{code}`
and `GET /invite/{code}`. More than one app cookie is refused with the login
page, because the server cannot tell which one is real. Entering a world with a
handoff code for a different identity than the browser's current play session
is refused with `400` and the minted session is revoked; more than one play
cookie is refused the same way.

Log out with the panel's logout button, or log out everywhere from the account
page. Either path revokes sessions in the registry.

## CSRF

Every state-changing app POST that relies on the cookie needs a `csrf` field.
The server derives the value from the session with an HMAC. The panel renders
it into every form, and the live stream renders it into fragments that carry
forms.

A request authenticated with an API token skips the check, because a bearer
token is not ambient. Play handoff is still a POST with CSRF that mints a
one-time code.

## API tokens

Create a token from the account page. It is shown once, stored as a hash, and
revocable in one action. Send it only as:

    Authorization: Bearer <token>

Never as a query parameter or a form field. The token has `api` scope, works on
the app port, and is refused on the play port. The old `X-Admin-Token` header
and the password-as-token are gone.

Admin responses send `Cache-Control: no-store`, `Referrer-Policy:
no-referrer` and `Cross-Origin-Opener-Policy: same-origin`, and they refuse
framing.
