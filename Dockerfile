# Disposable Ubuntu 24.04 test node that installs Nexa Panel by running the real
# installer, scripts/install.sh — the same script a real host runs. This image
# deliberately does NOT reproduce the install steps: a Dockerfile that lays the
# node out by hand drifts from the installer silently, and the drift surfaces as
# a bug on someone's server rather than here. Everything below the installer is
# either container-specific (systemd in a container, a published port) or test
# seed data, and each carries the reason it cannot live in the installer.
#
# Not a production deployment image — the container runs systemd and needs
# privileges, and Nginx is rebound to 0.0.0.0 so the published port works.
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

# The real installer. It configures the ondrej/php and PGDG repositories, so the
# Applications catalog can enumerate every PHP and PostgreSQL version from the
# moment the node boots — with Ubuntu's archive alone it can only ever offer the
# single PHP that noble ships. --no-start because systemd is not running in a
# build layer; the units are enabled and start on first boot. No binary is passed:
# compose bind-mounts dist/nexa-linux-${ARCH} onto /usr/bin/nexa at runtime, so
# iterating on the binary needs a restart, not a rebuild.
#
# The apt lists are deliberately NOT cleaned afterwards: the catalog reads them
# with apt-cache and never refreshes them itself, so an image that cleaned them
# would boot showing a truncated catalog.
COPY packaging/ /tmp/nexa-install/packaging/
COPY scripts/install.sh /tmp/nexa-install/scripts/install.sh
RUN /tmp/nexa-install/scripts/install.sh --no-start && rm -rf /tmp/nexa-install

# Test seed data, not part of a node install: the panel installs PHP versions on
# demand, but the site-rendering and runtime-discovery flows this node exercises
# need one present from the start.
RUN apt-get install -y --no-install-recommends php8.3-fpm php8.3-cli && \
    systemctl enable php8.3-fpm

# The installer deliberately exposes only a loopback Nginx bootstrap listener
# when no hostname is supplied. A published container port needs an all-interface
# listener, but the API remains confined to its Unix socket exactly as it is on a
# real host. This keeps the disposable node on the production ingress path.
RUN sed -i 's/listen 127\.0\.0\.1:8080;/listen 0.0.0.0:8080;/' \
      /etc/nginx/sites-available/nexa-panel.conf

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

# Docker's ubuntu image ships /usr/sbin/policy-rc.d as `exit 101`, which makes
# every package's postinst skip starting its service. That is right for a build
# layer, where there is no systemd to start anything — so it stays in force for
# the apt-get above — but wrong for this node at runtime: systemd is PID 1 here,
# and the Applications page installs *services*. Left in place, installing
# MariaDB reports success while leaving the server stopped, so the MySQL page
# still finds no engine through its socket — precisely the end-to-end break this
# node exists to catch. Removing it after the build-time installs gives runtime
# installs the same behaviour as a real host.
RUN rm -f /usr/sbin/policy-rc.d

EXPOSE 8080
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
