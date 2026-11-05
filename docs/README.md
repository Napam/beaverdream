# Docs

Start with the [README](../README.md). These pages go deeper.

## Guides

- [LAN play](lan-play.md): share a running board with friends on your network.
- [Configuration](configuration.md): every environment variable and its default,
  plus how to run the binary.
- [Admin panel](admin.md): the admin port, the password and the login cookie.
- [Real models](real-models.md): OpenAI-compatible endpoints, reasoning effort
  and the cost.
- [Docker](docker.md): build and run the production image.
- [Development](development.md): the `task` commands, tests, the git hook and the
  code layout.

## Work in progress

`wip/` holds design notes: how a feature was designed and what was decided, or
a plan that is not built yet. The code is the source of truth.

- [journal.md](wip/journal.md): the journal, where the model declares append-only
  feeds.
- [live.md](wip/live.md): the one live stream each open page holds.
- [public-deployment.md](wip/public-deployment.md): assessment and plan for putting
  the board on the internet. Not done.
- [reload-persistence.md](wip/reload-persistence.md): assessment and plan for keeping
  a visitor's place and the page state across reloads.
