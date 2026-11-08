# Play on your LAN

Run beaverdream on one machine. Friends on the same network open its address in
a browser, sign in, and play. They install nothing.

## 1. Find the machine's LAN IP

- macOS: `ipconfig getifaddr en0`. On a Mac, `en0` is usually Wi-Fi. If it prints
  nothing, try `en1`.
- Linux: `hostname -I`. Use the address that starts with `192.168.`, `10.` or
  `172.`.

## 2. Start the server

```sh
task dev
```

`task dev` sets `HOST=0.0.0.0`, so the server accepts connections from other
devices. If you run the binary instead, set `HOST=0.0.0.0` yourself. The binary
listens on `localhost` by default. See [configuration.md](configuration.md).

The app runs on `APP_PORT` (default `6930`) and the worlds on `PLAY_PORT`
(default `6932`). Friends only need the app address. Pressing enter sends them to
the play port with a one-time handoff and sets a play cookie there.

## 3. Create an account for each friend

Open `http://<IP>:6930`, sign in as the admin, create a world, and mint an
invite. Send the link. The friend picks a username and a password, then presses
enter. See [worlds.md](worlds.md).

## 4. Share the address

Friends open `http://<IP>:6930` in a browser. Use the IP from step 1.

The IP can change when your router hands out a new lease. If friends get no
answer, check it again.

## The admin is on the same address

The admin pages are on the app port too, under `/admin`. Anyone on your network
who reaches the app can see the login form. Keep the password to yourself. To
keep it the same across restarts, set `ADMIN_PASSWORD` in `.env`. See
[admin.md](admin.md).

## Firewall

The first time the server runs, macOS may ask whether to allow incoming
connections. Allow them, or other devices will get no answer. On Linux with
`ufw`, allow both ports:

```sh
sudo ufw allow 6930/tcp
sudo ufw allow 6932/tcp
```

The app redirects to `PLAY_PORT` on the same host when `PLAY_URL` is unset, so
both ports must be reachable. If you set `PLAY_URL` to another host or a proxy,
open only the app port.

## Router

Friends must be on the same network as the server. Guest Wi-Fi and some routers
isolate clients from each other, which blocks this. Use the main network.

Don't set up a port forward from the internet to these ports for a casual
session. The worlds run model-written scripts for everyone who opens them, and
the app holds accounts and invites. A public deployment needs separate
registrable domains for the app and the worlds; see
[worlds.md](worlds.md#public-deployments).

## A world is hostile by design

A player's post can make the model rewrite the page, including the scripts that
run in everyone's browser. That is the game. Play with people you trust, on a
network you trust.
