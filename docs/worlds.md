# Worlds, accounts and invites

One beaverdream instance holds many worlds. Each world starts as a plain page
with a post form, and a model rewrites its site. The app port owns accounts
and the lobby; the play port serves the worlds.

## First start

1. Start the server with `task dev` or `task build.docker.run`.
2. Open the app port, `http://localhost:6930`.
3. The log prints the generated admin password unless `ADMIN_PASSWORD` is set.
   Sign in as `admin` with that password.
4. Open the first world, named `heroes`. If the lobby is empty, create it. The
   name is a slug: lowercase letters, digits and dashes work best.
5. Create an invite on the admin invite page. Copy the link and send it to the
   people you want on the instance.

Only an admin creates worlds and invites. To keep the same admin password
across restarts, set `ADMIN_PASSWORD` in `.env`.

## Accounts

- Redeeming an invite creates an account. The person picks a username and a
  password.
- Usernames are the login name. Display names are separate, not unique, and can
  be changed without touching the login.
- There is no email and no forgotten-password flow yet. An admin action is the
  way back.
- The admin can play like anyone else. Admin turns never count toward a token
  budget.

## Invites

- An invite has a maximum number of uses and an optional label. The registry
  also supports an expiry, but the admin page does not set one yet.
- The link is shown once, when it is created. The server stores only a hash of
  the code.
- Opening the link shows a form. Only the form's POST uses a use, so a link
  preview or a prefetch cannot spend one.
- Expired, revoked, unknown and used-up codes all show the same failure.
- Revoking an invite stops future redemptions. It does not disable accounts
  that already used it. Revoke the identity instead.

## Worlds

- Each world is one SQLite file under `WORLDS_DIR`, named by its random id.
  The slug is only a label in the registry, so renaming or reusing a slug never
  touches a path.
- The play URL is `/worlds/{slug}`. The app shows an enter button that hands the
  browser to the play port with a one-time code.
- Deleting a world closes its database, cancels its turn, removes its file and
  then removes the registry row. A crash halfway is finished on the next start.
- Reset in the admin panel clears one world file. It keeps the registry, so
  accounts, invites and other worlds survive. Reset deletes the world's posts,
  turns, notes, shared state, holdings and journal rows.
- Back up with `VACUUM INTO` or the SQLite backup API. Copying a live `.db`
  file can miss recent writes that still sit in the WAL.

## Budgets

Two daily budgets are charged in model tokens, per UTC day:

- `DAILY_TOKENS_IDENTITY`, one identity across every world.
- `DAILY_TOKENS_GLOBAL`, every identity on the instance.

The defaults are 1,000,000 and 5,000,000. `0` means unlimited. A post over
budget is refused with a `429` and is not stored, so nobody sees a post that
will never get an answer. A batch written by several identities splits its
tokens evenly among them; the global budget takes the whole amount. A turn
already running finishes.

## Public deployments

Public play needs care:

- For public play, put the app and the play page on subdomains of one
  registrable domain over HTTPS. Set `APP_URL` and `PLAY_URL` to those origins.
  Ports on one host are not an origin boundary; that is accepted for LAN-only
  play.
- Keep the app behind HTTPS. Page scripts run with the play session and must
  never reach the app origin's HTML.
- Keep `ADMIN_PASSWORD` set and strong. The bootstrap password is the way back
  if the admin identity is disabled.
- Set both daily budgets to values you can afford. There are no rate limits,
  email, or audit log in this version.
- Back up the registry and every world file on a schedule.
