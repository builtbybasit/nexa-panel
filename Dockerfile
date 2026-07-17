# Disposable Ubuntu 24.04 test node with Nexa Panel installed the same way the
# .deb would lay it out: /usr/bin/nexa, the packaged systemd units, sysusers,
# and tmpfiles configs, plus Nginx, PHP-FPM 8.3, and cron so the site,
# runtime-discovery, and scheduled-task flows work end to end. Not a
# production deployment image — the container runs systemd and needs
# privileges, and the API is rebound to 0.0.0.0 so the published port works.
#
# Testing image only: it does not build anything. Build the binary first with
# scripts/build-linux-release.sh, which runs `bun run build` for the embedded
# web UI and compiles the Go binary with -tags embed, then bind-mount the
# resulting dist/nexa-linux-${ARCH} into the container as /usr/bin/nexa —
# it is not COPYed into the image, so re-testing a binary change only needs
# a container restart, not a rebuild.
#
# Build:  ./scripts/build-linux-release.sh amd64
#         docker build -t nexa-node .
# Run:    docker run -d --name nexa-node --privileged --cgroupns=host \
#           -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
#           -v $(pwd)/dist/nexa-linux-amd64:/usr/bin/nexa:ro \
#           -p 8080:8080 nexa-node

FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus \
    nginx php8.3-fpm php8.3-cli \
    cron curl ca-certificates util-linux \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY packaging/systemd/nexa-agent.service packaging/systemd/nexa-api.service /usr/lib/systemd/system/
COPY packaging/sysusers/nexa-panel.conf /usr/lib/sysusers.d/nexa-panel.conf
COPY packaging/tmpfiles/nexa-panel.conf /usr/lib/tmpfiles.d/nexa-panel.conf

# The packaged unit binds 127.0.0.1, which a published container port cannot
# reach; rebind to 0.0.0.0 inside the container only.
RUN mkdir -p /etc/systemd/system/nexa-api.service.d && \
    printf '[Service]\nExecStart=\nExecStart=/usr/bin/nexa api --address 0.0.0.0:8080 --state /var/lib/nexa-panel/control.db --master-key /var/lib/nexa-panel/master.key\n' \
      > /etc/systemd/system/nexa-api.service.d/container.conf && \
    systemctl enable nexa-agent nexa-api nginx php8.3-fpm cron

# The packaged agent runs under ProtectSystem=strict with a scoped ReadWritePaths
# list. Two problems for this test image:
#   1) The packaged list assumes PostgreSQL and certbot are installed (creating
#      /etc/postgresql, /var/lib/postgresql, /run/postgresql, /etc/letsencrypt,
#      /var/lib/letsencrypt, /var/log/{postgresql,letsencrypt}); missing entries
#      abort namespace setup (226/NAMESPACE), crash-looping the agent and, via
#      nexa-api's Requires=nexa-agent, the API too (ERR_EMPTY_RESPONSE).
#   2) The Applications page installs OS packages with apt, which must write the
#      dpkg database and files across /usr, /var, and /etc — impossible under
#      ProtectSystem=strict (apt reports "Not using locking for read only lock
#      file /var/lib/dpkg/lock" and installs nothing).
# For this DISPOSABLE test node only, drop the filesystem sandbox so package
# installs work end to end. Production package-install support is a deliberate
# hardening decision (a package-management operation with a curated writable set),
# NOT this blanket relaxation — do not copy this drop-in into the packaged unit.
# ProtectHome=off matters too: add-apt-repository (for the ondrej/php PPA) spawns
# gpg to store the signing key, which needs a writable $HOME (/root/.gnupg);
# with ProtectHome=true /root is hidden and gpg throws a Python traceback.
RUN mkdir -p /etc/systemd/system/nexa-agent.service.d && \
    printf '[Service]\nProtectSystem=off\nProtectHome=off\nReadWritePaths=\nNoNewPrivileges=no\nPrivateTmp=no\n' \
      > /etc/systemd/system/nexa-agent.service.d/container.conf

EXPOSE 8080
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
