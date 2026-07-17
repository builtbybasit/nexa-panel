# Disposable Ubuntu 24.04 test node with Nexa Panel installed the same way the
# .deb would lay it out: /usr/bin/nexa, the packaged systemd units, sysusers,
# and tmpfiles configs, plus Nginx, PHP-FPM 8.3, and cron so the site,
# runtime-discovery, and scheduled-task flows work end to end. Not a
# production deployment image — the container runs systemd and needs
# privileges, and the API is rebound to 0.0.0.0 so the published port works.
#
# Build:  docker build -t nexa-node .
# Run:    docker run -d --name nexa-node --privileged --cgroupns=host \
#           -v /sys/fs/cgroup:/sys/fs/cgroup:rw -p 8080:8080 nexa-node
# Panel:  http://localhost:8080 (embedded production UI)

FROM oven/bun:1.3 AS web
WORKDIR /src
COPY web/package.json web/bun.lock web/
RUN cd web && bun install --frozen-lockfile
COPY web web
# vite.config.ts writes the production bundle to ../internal/platform/webui/dist.
# Run vite directly: `bun run build` starts with vue-tsc, a Node program the
# bun image cannot execute; typechecking belongs to `make check`, not this image.
RUN cd web && bunx --bun vite build

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY --from=web /src/internal/platform/webui/dist internal/platform/webui/dist
ARG TARGETARCH
# -ldflags="-s -w" strips the symbol table and DWARF debug info — unnecessary for
# a deployed binary and worth several MB off the embedded-UI build.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -tags embed -trimpath -ldflags="-s -w" -o /out/nexa ./cmd/nexa

FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus \
    nginx php8.3-fpm php8.3-cli \
    cron curl ca-certificates util-linux \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/nexa /usr/bin/nexa
COPY packaging/systemd/nexa-agent.service packaging/systemd/nexa-api.service /usr/lib/systemd/system/
COPY packaging/sysusers/nexa-panel.conf /usr/lib/sysusers.d/nexa-panel.conf
COPY packaging/tmpfiles/nexa-panel.conf /usr/lib/tmpfiles.d/nexa-panel.conf

# The packaged unit binds 127.0.0.1, which a published container port cannot
# reach; rebind to 0.0.0.0 inside the container only.
RUN mkdir -p /etc/systemd/system/nexa-api.service.d && \
    printf '[Service]\nExecStart=\nExecStart=/usr/bin/nexa api --address 0.0.0.0:8080 --state /var/lib/nexa-panel/control.db --master-key /var/lib/nexa-panel/master.key\n' \
      > /etc/systemd/system/nexa-api.service.d/container.conf && \
    systemctl enable nexa-agent nexa-api nginx php8.3-fpm cron

# nexa-agent runs under ProtectSystem=strict and bind-mounts every ReadWritePaths
# entry to make it writable; systemd aborts namespace setup (status=226/NAMESPACE)
# if any entry is missing. The packaged list assumes a full host with PostgreSQL
# and certbot installed, whose packages create /etc/postgresql, /var/lib/postgresql,
# /run/postgresql, /etc/letsencrypt, /var/lib/letsencrypt, /var/log/{postgresql,letsencrypt}.
# This test image installs none of them, so the agent crash-looped every 3s and,
# via nexa-api's Requires=nexa-agent, dragged the API down with it — dropping the
# UI's asset and /api/v1/jobs requests with ERR_EMPTY_RESPONSE. Scope the writable
# paths to what this image actually provides (no PostgreSQL/certbot flows here).
RUN mkdir -p /etc/systemd/system/nexa-agent.service.d && \
    printf '[Service]\nReadWritePaths=\nReadWritePaths=/etc/nexa-panel /etc/containers/systemd /etc/cron.d /etc/nginx /etc/php /srv/nexa /var/lib/nexa-panel /run/nexa-panel /run/php\n' \
      > /etc/systemd/system/nexa-agent.service.d/container.conf

EXPOSE 8080
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
