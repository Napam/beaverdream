# Docs

Start with the [README](../README.md). These pages go deeper.

## Guides

- [Worlds and invites](worlds.md): accounts, invites, worlds and daily budgets.
- [LAN play](lan-play.md): share a running board with friends on your network.
- [Configuration](configuration.md): every environment variable and its default,
  plus how to run the binary.
- [Admin panel](admin.md): the bootstrap password, sessions and the admin pages.
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
- [reload-persistence.md](wip/reload-persistence.md): assessment and plan for keeping
  a visitor's place and the page state across reloads.
- [worlds-and-invites.md](wip/worlds-and-invites.md): invite-only access with daily
  budgets, then many worlds on one instance. Not built.
