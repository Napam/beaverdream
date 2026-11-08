# Real models

beaverdream calls any OpenAI-compatible chat completions endpoint. The model
must support tool calls. A model that replies in prose without calling a tool
leaves the page unchanged.

## Setup

1. Create `.env` from the template if you do not have one: `cp .env.example .env`.
   Then set these in it, uncommenting the lines:

   ```sh
   OPENAI_BASE_URL=https://your-endpoint.example.com/v1
   OPENAI_MODEL=your-model-name
   OPENAI_API_KEY=your-key
   ```

2. Run `task dev`.

Leave `OPENAI_API_KEY` empty for a server that needs no key. See
[configuration.md](configuration.md) for every variable.

## Reasoning effort

`OPENAI_REASONING_EFFORT` sets how hard the model thinks. The accepted values
are `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`. Leave it
empty if the model rejects the field. Lower effort means fewer tokens and less
waiting, but the quality may drop.

## Rounds per turn

`TURN_MAX_ROUNDS` caps the tool rounds one turn may use. The default is `25`. A
turn ends when the model calls `done`, or when it runs out of rounds.

## Cost

Every model round is a paid request. A post can take several rounds, up to
`TURN_MAX_ROUNDS`. Compaction adds up to eight more calls when the room log
gets long, one per chunk of up to 1,000 turns.
Friends can post all evening, so set a spend cap with your provider before you
start. Check the provider's dashboard while you play.

The server also enforces two daily token budgets, per identity and global, in
`DAILY_TOKENS_IDENTITY` and `DAILY_TOKENS_GLOBAL`. They are a safety net, not a
billing guarantee: a turn already running finishes, and admin turns are never
charged. See [configuration.md](configuration.md#daily-token-budgets).

## Try one post first

Post once before you invite anyone. The terminal logs each model round with its
time, token counts and tool calls. The page should change. If it does not, the
model may not be calling tools. Then check the spend on the provider's dashboard.
The admin changesets page shows each turn in full. See [admin.md](admin.md).
