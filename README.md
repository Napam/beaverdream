# beaverdream

<img src="web/assets/beaver.svg" width="240" alt="a beaver dreaming">

Nobody designed this message board. It got that way one post at a time.

beaverdream hosts worlds. Each world is a message board whose site a model
rewrites after every post: colors, layout, scripts, even the post form it hands
you. Everyone in a world sees the same page. Someone asks for a dark terminal
look, and the next page is dark for all of them. Only the admin panel can put an
old version back.

People join with an invite and sign in with a username and password. The admin
creates worlds and can delete them. Each world is its own SQLite file, like a
Minecraft save.

Model-written scripts run in every player's browser, so keep it on a network
you trust. A public deployment needs separate registrable domains for the app
and the boards.

## Play with friends on your LAN

Run beaverdream on one machine and give friends its address. They open it in a
browser, sign in, and press play. They install nothing. Posts made at the same
time get folded into one rewrite, so you are all arguing over the same page.
Details in [docs/lan-play.md](docs/lan-play.md).

## Quick start

You need [mise](https://mise.jdx.dev). Activate mise in your shell so its Go,
bun and Task are on your `PATH`. `mise.toml` pins the versions.

```sh
task init           # install tools, js deps and git hooks
task dev.mock-llm   # terminal 1: a fake model on port 6701, no key needed
task dev            # terminal 2: the app on 6930, boards on 6932
```

Open http://localhost:6930. The log prints the generated admin password. Sign
in as `admin`, open or create the first world (name it `heroes`), and mint an
invite for yourself or a friend. Press play, then post something. Try `matrix`, `pink` or
`brutalist`. The fake model picks its theme from keywords in the post, so the
page changes without a real model. No `.env` file is needed for this. To change
a setting, copy the template with `cp .env.example .env` and edit the copy. If
your `.env` sets a real model, `task dev` uses that instead.

## Run with Docker

`task build.docker.run` builds the image and runs it with `data/` mounted, so
the registry and every world survive restarts. The details are in
[docs/docker.md](docs/docker.md).

## Use a real model

Any OpenAI-compatible endpoint works, as long as it supports tool calls. Create
your `.env` with `cp .env.example .env`, set `OPENAI_BASE_URL`, `OPENAI_MODEL`
and `OPENAI_API_KEY` in it (uncomment the lines), then run `task dev`. Each post
can trigger several paid model calls. Set a spend cap with your provider first
and tune the daily token budgets in `.env`. More in
[docs/real-models.md](docs/real-models.md).

## How it works

- Each post goes to the model with the current page and a log of the room. The
  model answers with tool calls that edit the page, not with a chat reply.
- Every change is a new version. Old looks stay in the history, and the admin
  panel can restore any of them.
- The page is the model's own HTML, CSS and JavaScript, and it runs in every
  player's browser. The server escapes post text and places the post list.
- Accounts, invites, sessions and daily token budgets live in one registry
  file. Each world's pages, posts and turns live in its own file.

## Docs

- [docs/README.md](docs/README.md): index of the docs and work-in-progress notes.
- [Worlds and invites](docs/worlds.md): accounts, invites, worlds and budgets.
- [LAN play](docs/lan-play.md): share a board with friends on your network.
- [Configuration](docs/configuration.md): every environment variable, and how to
  run the binary.
- [Admin panel](docs/admin.md): the bootstrap password, sessions and the admin
  pages.
- [Real models](docs/real-models.md): OpenAI-compatible endpoints and what they
  cost.
- [Docker](docs/docker.md): the production image.
- [Development](docs/development.md): tasks, tests and the code layout.

## License

MIT. Copyright (c) 2026 Naphat Amundsen. See [LICENSE](LICENSE).

`lib/hashfs` is adapted from [benbjohnson/hashfs](https://github.com/benbjohnson/hashfs),
also MIT. Live page updates run on [Datastar](https://github.com/starfederation/datastar-go).

The project is built on [backwego-template](https://github.com/Napam/backwego-template).
