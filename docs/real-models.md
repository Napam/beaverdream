# Real models

beaverdream calls any OpenAI-compatible chat completions endpoint. The model
must support tool calls. A model that replies in prose without calling a tool
leaves the page unchanged.

## Setup

1. Put these in `.env`:

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

`MUTATION_ROUNDS` caps the tool rounds one turn may use. The default is `3`. A
turn ends when the model calls `done`, or when it runs out of rounds.

## Cost

Every model round is a paid request. A post can take several rounds, up to
`MUTATION_ROUNDS`. Compaction makes one more call when the room log gets long.
Friends can post all evening, so set a spend cap with your provider before you
start. Check the provider's dashboard while you play.

## Try one post first

Post once before you invite anyone. The terminal logs each model round with its
time, token counts and tool calls. The page should change. If it does not, the
model may not be calling tools. Then check the spend on the provider's dashboard.
The admin turns page shows each turn in full. See [admin.md](admin.md).
