# Admin panel

The admin panel is code-owned. The model never writes it. It lists a world's
current version, its CSS and HTML, and the version history. It can restore an
old version, undo the last change, heal the board, or reset it. It shows the
recent webmaster turns with their prompts, tool calls and results. It also
sends an instruction straight to the model, and has a data page for journal
feeds.

The app also has pages for accounts, invites and the world registry. Only an
identity with the `ADMIN` role can create or delete worlds and create invites.

## Where it runs

The admin lives on the app port, `APP_PORT` (default `6930`), not a separate
admin port. The play port never serves an admin page. That keeps the model's
scripts off the origin that holds the app session.

- `/admin`: the overview, accounts, invites and the world list.
- `/admin/w/{slug}/...`: one world's turns, site tools and data page.
  `/admin/w/{slug}/turns`, `/admin/w/{slug}/site` and
  `/admin/w/{slug}/journal` are the main ones.

There is no `ADMIN_PORT`, no separate admin hostname, and no board redirect to
an admin port.

## Password

- The admin is an identity with the `ADMIN` role. Its username is `admin`, and
  it signs in with that username and the bootstrap password.
- `ADMIN_PASSWORD` is the bootstrap. On every start the server writes an
  argon2id hash of it into the admin identity's `password` login, clears any
  disabled state and grants `ADMIN` if it is missing.
- To change the password, change `ADMIN_PASSWORD` and restart. That revokes the
  admin's existing sessions. The UI does not change the bootstrap login.
- If `ADMIN_PASSWORD` is empty, the server generates one and logs it as a
  warning. A restart makes a new password. A chosen password must be at least
  8 characters.

## Sessions

`GET /admin` without an app session shows the login form. Signing in sets an
app session cookie:

- `HttpOnly`, `SameSite=Lax`, host-only.
- Lasts `APP_SESSION_TTL`, 7 days by default.
- Stored as a hash in the registry, so revoking it takes effect on the next
  request.
- `Secure` when the app is served over TLS.

App and play cookies have different names, so the play handoff cannot overwrite
an app session on localhost.

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

Admin responses send `Cache-Control: no-store` and
`Referrer-Policy: no-referrer`, and they refuse framing.
