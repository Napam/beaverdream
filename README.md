# beaverdream

<img src="web/assets/beaver.svg" width="240" alt="a beaver dreaming">

Nobody designed this message board. It got that way one post at a time.

Visitors post, and a model rewrites the whole site after each one: colors,
layout, scripts, even the post form it hands you. Everyone sees the same page.
Someone asks for a dark terminal look, and the next page is dark for all of
you. Only the admin panel can put an old version back.

Model-written scripts run in every visitor's browser, so keep it on a network
you trust.

## Play with friends on your LAN

Run beaverdream on one machine and give friends its LAN address. They open it in
a browser and install nothing. Post at the same time and your posts get folded
into one rewrite, so you are all arguing over the same page. Details in
[docs/lan-play.md](docs/lan-play.md).

## Quick start

You need [mise](https://mise.jdx.dev). Activate mise in your shell so its Go,
bun and Task are on your `PATH`. `mise.toml` pins the versions.

```sh
task init           # install tools, js deps and git hooks
task dev.mock-llm   # terminal 1: a fake model on port 6701, no key needed
task dev            # terminal 2: the server on port 6930
```

Open http://localhost:6930 and post something. Try `matrix`, `pink` or `brutalist`.
The fake model picks its theme from keywords in the post, so the page changes
without a real model. If your `.env` sets a real model, `task dev` uses that
instead.

## Run with Docker

`task build.docker.run` builds the image and runs it with a named volume for the
database, so the data survives restarts. The details are in
[docs/docker.md](docs/docker.md).

## Use a real model

Any OpenAI-compatible endpoint works, as long as it supports tool calls. Put
your endpoint and key in `.env`, then run `task dev`. Each post can trigger
several paid model calls, so set a spend cap with your provider first. More in
[docs/real-models.md](docs/real-models.md).

## How it works

- Each post goes to the model with the current page and a log of the room. The
  model answers with tool calls that edit the page, not with a chat reply.
- Every change is a new version. Old looks stay in the history, and the admin
  panel can restore any of them.
- The page is the model's own HTML, CSS and JavaScript, and it runs in every
  visitor's browser. The server escapes post text and places the post list.

## Docs

- [docs/README.md](docs/README.md): index of the docs and work-in-progress notes.
- [LAN play](docs/lan-play.md): share a board with friends on your network.
- [Configuration](docs/configuration.md): every environment variable, and how to
  run the binary.
- [Admin panel](docs/admin.md): the password, the login cookie and the admin
  port.
- [Real models](docs/real-models.md): OpenAI-compatible endpoints and what they
  cost.
- [Docker](docs/docker.md): the production image.
- [Development](docs/development.md): tasks, tests and the code layout.

## License

MIT. Copyright (c) 2026 Naphat Amundsen. See [LICENSE](LICENSE).

`lib/hashfs` is adapted from [benbjohnson/hashfs](https://github.com/benbjohnson/hashfs),
also MIT. Live page updates run on [Datastar](https://github.com/starfederation/datastar-go).

The project is built on [backwego-template](https://github.com/Napam/backwego-template).
