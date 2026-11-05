# Play on your LAN

Run beaverdream on one machine. Friends on the same network open its address in
a browser. They install nothing.

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

## 3. Share the address

Friends open `http://<IP>:6930` in a browser. Use the IP from step 1.

The IP can change when your router hands out a new lease. If friends get no
answer, check it again.

## The admin is on the LAN too

The admin panel listens on port 6932 under `task dev`, on the same address. Anyone
on your network can reach its login form. Keep the password to yourself. To keep
it the same across restarts, set `ADMIN_PASSWORD` in `.env`. See
[admin.md](admin.md).

## Firewall

The first time the server runs, macOS may ask whether to allow incoming
connections. Allow them, or other devices will get no answer. On Linux with
`ufw`, run `sudo ufw allow 6930/tcp`. Port 6932 needs a rule only if you want the
admin panel from another device.

## Router

Friends must be on the same network as the server. Guest Wi-Fi and some routers
isolate clients from each other, which blocks this. Use the main network.

Don't set up a port forward from the internet to port 6930. The board runs
model-written scripts for everyone who opens it, so it should stay on your LAN.

## The board is hostile by design

A visitor's post can make the model rewrite the page, including the scripts that
run in everyone's browser. That is the game. Play with people you trust, on a
network you trust.
