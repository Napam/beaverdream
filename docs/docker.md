# Docker

Run the app and its worlds in a container so friends on your LAN can join.

## Build and run

    task build.docker.run

This builds `beaverdream:latest` and starts it in the foreground. Stop it with
Ctrl+C. The container is removed, but the instance stays. Its registry is
`data/registry.db` and its world files are under `data/worlds/` in this repo,
the same files a local run uses, so the next run keeps every account and world.
The container runs as your user, so the files in `data/` are yours.

To build without running:

    task build.docker

## Open the app

Find your LAN address. On macOS:

    ipconfig getifaddr en0

Then friends open:

    http://<host-LAN-IP>:6930

The app is on 6930. The worlds are on 6932, reached by pressing enter; friends do
not type that address. The admin panel is on the app port under `/admin`.

## Admin password

If `ADMIN_PASSWORD` is not set, the server makes a random password on each start.
It is written to the container log as a warning:

    docker logs beaverdream 2>&1 | hl

`docker logs` shows the raw JSON lines, one event per line. Pipe them through
`hl` to read them.

To pick your own password, set `ADMIN_PASSWORD` in `.env` and start again. The
task reads `.env` and passes the value into the container. The image does not
contain `.env`. The admin username is `admin`.

## Model settings

The task passes these from your `.env` (or your shell) into the container:
`OPENAI_BASE_URL`, `OPENAI_MODEL`, `OPENAI_API_KEY`, `OPENAI_REASONING_EFFORT`,
`OPENCODE_SESSION_ID`, `TURN_MAX_ROUNDS`, `TURN_TIMEOUT`, `ADMIN_PASSWORD`,
`APP_SESSION_TTL`, `PLAY_SESSION_TTL`, `HANDOFF_TTL`, `DAILY_TOKENS_IDENTITY`,
`DAILY_TOKENS_GLOBAL`, `MODEL_CONCURRENCY`, `WORLD_IDLE_TIMEOUT`, `APP_URL` and
`PLAY_URL`.

With no `OPENAI_BASE_URL` set, the container uses the mock model on your host:
`http://host.docker.internal:6701/v1`. Start it first in another terminal with
`task dev.mock-llm`. If `.env` sets `OPENAI_BASE_URL` to `localhost`, the
container cannot reach it. Use `host.docker.internal` or the real API URL.

## Ports and data

The run publishes ports 6930 and 6932. Stop `task dev` first if it runs, or the
container fails with "address already in use". `task dev` and the container also
share `data/`, so run one at a time.

The image sets:

- `HOST=0.0.0.0`
- `APP_PORT=6930`
- `PLAY_PORT=6932`
- `REGISTRY_PATH=/app/data/registry.db`
- `WORLDS_DIR=/app/data/worlds`

Mount one volume at `/app/data` to keep them. Do not copy a live `.db` file as a
backup; use `VACUUM INTO` or the SQLite backup API.

## Run without the task

    docker run -d --name beaverdream \
      --user "$(id -u):$(id -g)" \
      -e ADMIN_PASSWORD=change-me \
      -e OPENAI_BASE_URL=http://host.docker.internal:6701/v1 \
      -e DAILY_TOKENS_IDENTITY=1000000 \
      -e DAILY_TOKENS_GLOBAL=5000000 \
      -p 6930:6930 -p 6932:6932 \
      -v "$PWD/data:/app/data" \
      beaverdream:latest

For a public deployment behind a proxy, set `APP_URL` and `PLAY_URL` to the two
registrable domains. Do not expose the play port as a path on the app domain.
