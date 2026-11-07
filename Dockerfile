# syntax=docker/dockerfile:1

# Go matches go.mod. bun and task match mise.toml. Neither comes from an
# installer script: bun is copied from its image, task is built from the module.
FROM golang:1.27.3-trixie AS builder

COPY --from=oven/bun:1.4.4 /usr/local/bin/bun /usr/local/bin/bun
RUN ln -s /usr/local/bin/bun /usr/local/bin/bunx && \
    GOBIN=/usr/local/bin go install github.com/go-task/task/v3/cmd/task@v3.54.0

WORKDIR /app

# This is to make build caching better
COPY web/package.json web/bun.lock web/
RUN --mount=type=cache,target=/root/.bun/install/cache \
    cd web && bun install --frozen-lockfile
COPY go.mod go.sum ./
RUN go mod download
# Force build cache of go tools
RUN --mount=type=cache,target=/root/.cache/go-build \
    go tool templ version

# .dockerignore keeps .env and local data out of this layer
COPY . .
# The registry and every world file live under /app/data.
RUN mkdir -p /app/data/worlds

# Same order as `task build.go`: templ, tailwind, the js bundle, then go build.
RUN --mount=type=cache,target=/root/.cache/go-build \
    ENV=prod CGO_ENABLED=0 task build.go && \
    chmod +x /app/bin/app

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /app/bin/app /app/bin/app
# Owned by the app user so a fresh named volume starts writable.
COPY --from=builder --chown=65532:65532 /app/data /app/data

WORKDIR /app
USER 65532:65532

# HOST=0.0.0.0 so published ports reach the host. Both listeners are
# published by `task build.docker.run`. The registry and world files sit under
# /app/data so one volume keeps the whole instance.
ENV HOST=0.0.0.0 APP_PORT=6930 PLAY_PORT=6932 \
    REGISTRY_PATH=/app/data/registry.db WORLDS_DIR=/app/data/worlds

EXPOSE 6930 6932

ENTRYPOINT ["/app/bin/app"]
