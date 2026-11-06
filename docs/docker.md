# Docker

Run the board in a container so friends on your LAN can open it.

## Build and run

    task build.docker.run

This builds `beaverdream:latest` and starts it in the foreground. Stop it with
Ctrl+C. The container is removed, but the board stays. Its database is
`data/sqlite.db` in this repo, the same file a local run uses, so the next run
keeps the board. The container runs as your user, so the files in `data/` are
yours.

To build without running:

    task build.docker

## Open the board

Find your LAN address. On macOS:

    ipconfig getifaddr en0

Then friends open:

    http://<host-LAN-IP>:6930

The admin runs on its own port:

    http://<host-LAN-IP>:6932/admin

The board port redirects `/admin` to the admin port, so `http://<host-LAN-IP>:6930/admin`
also works.

## Admin password

If `ADMIN_PASSWORD` is not set, the server makes a random password on each start.
It is written to the container log:

    docker logs beaverdream | grep admin_password

`docker logs` shows the raw JSON lines, one event per line. Pipe them through
`hl` to read them: `docker logs beaverdream 2>&1 | hl`.

To pick your own password, set `ADMIN_PASSWORD` in `.env` and start again. The
task reads `.env` and passes the value into the container. The image does not
contain `.env`.

## Model settings

The task passes these from your `.env` (or your shell) into the container:
`OPENAI_BASE_URL`, `OPENAI_MODEL`, `OPENAI_API_KEY`, `OPENAI_REASONING_EFFORT`,
`OPENCODE_SESSION_ID`, `TURN_MAX_ROUNDS`, and `ADMIN_PASSWORD`.

With no `OPENAI_BASE_URL` set, the container uses the mock model on your host:
`http://host.docker.internal:6701/v1`. Start it first in another terminal with
`task dev.mock-llm`. If `.env` sets `OPENAI_BASE_URL` to `localhost`, the
container cannot reach it. Use `host.docker.internal` or the real API URL.

## Ports already in use

The run publishes ports 6930 and 6932. Stop `task dev` first if it runs, or the
container fails with "address already in use". `task dev` and the container also
share `data/sqlite.db`, so run one at a time.

## Run without the task

    docker run -d --name beaverdream \
      --user "$(id -u):$(id -g)" \
      -e ADMIN_PASSWORD=change-me \
      -e OPENAI_BASE_URL=http://host.docker.internal:6701/v1 \
      -p 6930:6930 -p 6932:6932 \
      -v "$PWD/data:/app/data" \
      beaverdream:latest
