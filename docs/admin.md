# Admin panel

The admin panel is code-owned. The model never writes it. It lists the current
version, its CSS and HTML, and the version history. It can restore an old
version, undo the last change, heal the board, or reset it. It shows the recent
webmaster turns with their prompts, tool calls and results. It also sends an
instruction straight to the model, and has a data page for journal feeds.

## Port

The admin listens on its own port, set by `ADMIN_PORT`. The default is the port
after `PORT`, skipping the live reload port. Under `task dev` that is `6932`.

The board's port answers `/admin` and every path under it with a 302 to the
admin port. The host stays the same, so `http://<IP>:6930/admin` leads to
`http://<IP>:6932/admin`.

The admin port serves only the panel and its assets. No board page is served
there. That split keeps the model's scripts off the origin that holds the login
cookie.

## Public use

To reach the admin from the internet, give it its own hostname at your proxy,
such as `admin.example.com`, and set `ADMIN_URL` to that URL. The board's
`/admin` redirect then goes to the public address, not to the admin port on the
board's host. Protect that hostname at the proxy with basic auth, an IP
allow-list, or a VPN, and keep the admin password set.

## Password

- Set `ADMIN_PASSWORD` in `.env` for a password that stays the same across restarts.
- Leave it empty and the server makes a random one at startup. It logs the
  password and the admin URL as a warning. A restart makes a new password, and
  that signs out every browser.
- Scripts can send the password as the `X-Admin-Token` header or as a `token`
  form field. The query string is never read, so the password stays out of URLs.

## Login cookie

`GET /admin` shows a password form. It posts the password as the `password`
field. On success the server sets the `beaverdream_admin` cookie:

- `HttpOnly`, so page scripts cannot read it.
- `SameSite=Strict`.
- Valid for 30 days. The value holds a signed expiry, so there is no session
  table.
- `Secure` when the admin is served over TLS.

Cookies ignore ports, so the board's port also receives this cookie. `HttpOnly`
keeps the model's scripts from reading it, and the CSRF token below keeps a
forged request from working.

Log out with the panel's logout button, which sends `POST /admin/logout`.

## CSRF token

Every POST that relies on the cookie must include a `csrf` field. The server
derives the value from the login's expiry and `ADMIN_PASSWORD`. The panel fills it
in. A request that authenticates with `X-Admin-Token` or the `token` field does
not need it.

Admin responses send `Cache-Control: no-store` and
`Referrer-Policy: no-referrer`, and they refuse framing.
