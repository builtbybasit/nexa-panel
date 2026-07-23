# Managed Node Requirements

Nexa Panel currently supports Ubuntu 24.04 LTS on AMD64 and ARM64. A production
node must boot with systemd and the installer must run as root. The supported
installation path is `scripts/install.sh`; containers that run systemd in
privileged mode are acceptance environments, not production deployments.

## Installed foundation

The installer configures and keeps these host capabilities available:

- Nginx with Ubuntu's `sites-available` and `sites-enabled` layout;
- systemd, D-Bus, cron, standard account tools, `runuser`, and core utilities;
- Certbot and its Nginx plugin for public panel TLS;
- `logrotate`, which reads the per-site stanzas the sites operator writes to
  `/etc/logrotate.d/` when a site enables log rotation;
- Podman with Quadlet and `fuse-overlayfs` for isolated pgAdmin
  (Podman 4.5 or newer is required by the generated definition);
- `rclone` for local, S3-compatible, SFTP, and Google Drive backup accounts;
- `postgresql-common` and `libjson-perl` for JSON cluster discovery;
- `git`, `unzip`, `rsync`, `acl`, and `sudo` for deployment tooling: cloning and
  updating a site's repository, unpacking downloaded archives, copying release
  directories, granting the site account access to shared paths, and invoking
  the single root-owned reload wrapper a deployer-mode site is allowed to run;
- CA certificates, cURL, GnuPG, and Ubuntu repository-management tools.

The installer also enables the Ondrej PHP PPA and the PostgreSQL PGDG
repository, then leaves the apt index populated because the Applications module
uses that index as its live catalog.

## Installed on demand

Nexa Panel installs selected application packages from the configured
repositories rather than requiring every supported version up front:

- each selected `php<version>-fpm` service and its matching `php-fpm<version>`
  configuration validator;
- native phpMyAdmin with PHP 8.3 FPM and its MySQL, mbstring, ZIP, GD, and cURL
  extensions when the database web client is deployed;
- PostgreSQL server and client packages for the selected major version,
  including `pg_lsclusters`, `pg_createcluster`, `pg_ctlcluster`, `psql`,
  `createdb`, `dropdb`, `pg_dump`, and `pg_restore`;
- one native MySQL or MariaDB series offered by the Applications catalog, with
  its server, client, dump utility, Unix socket, and systemd service;
- Composer and the `php<version>-cli` matching a deployer-mode site's PHP
  branch, installed by the deployment prepare job. Composer is not a foundation
  package because Ubuntu's archive copy depends on the distro `php-cli`, which
  the Applications catalog would then report as an installed PHP branch.

Database backup and restore operations require the client major version to
match the managed server. Site backup creation additionally uses the host
`tar`; safe extraction is implemented inside Nexa Panel.

## Network access

- Package installation needs outbound HTTPS access to Ubuntu, the configured
  PHP/PostgreSQL repository, and the selected MySQL or MariaDB repository.
- Deploying phpMyAdmin needs Ubuntu package-repository access; deploying pgAdmin
  needs outbound access to its configured OCI image registry.
- Remote backups need outbound access to their configured rclone backend.
- Public TLS needs a DNS record pointing at the node and inbound TCP port 80
  for the initial ACME HTTP-01 challenge. Nginx serves HTTPS after Certbot has
  installed the certificate.

## Filesystem and service contract

The packaged tmpfiles rules create the shared runtime socket directory, private
control-plane state and logs, managed site roots, ACME webroot, generated task
definitions, and administration-tool data directories with explicit ownership
and modes.

Before the privileged agent starts, its systemd `ExecStartPre` runs
`nexa agent-token` to create or validate the root-owned credential at
`/etc/nexa-panel/agent.token`. The API reads that `0640 root:nexa` file directly,
which keeps it unavailable to unrelated users and lets credential rotation take
effect without restarting the API. Nginx reaches the API
through `/run/nexa-panel/api.sock`; the API reaches the root agent through the
separate authenticated `/run/nexa-panel/agent.sock`.

The API service runs as the unprivileged `nexa` account. The root agent runs
with systemd hardening and an explicit writable-path boundary covering the host
trees managed by package, service, site, database, certificate, schedule, and
backup operations.

The master key is `/etc/nexa-panel/master.key`, apart from the control-plane
state in `/var/lib/nexa-panel`, so that a stolen state backup does not also hand
over the key that opens it. `/etc/nexa-panel` stays root-owned `0711`: a
privileged `ExecStartPre` on `nexa-api.service` mints the key, or moves a
pre-split key out of the state directory, before the service drops to `nexa`,
which then only reads it. The unit therefore needs no write access to `/etc`.

## Resource envelope

A node needs at least 2 GiB of memory; the panel reports anything smaller as an
unsupported capacity profile. Both long-running units are capped so neither can
exhaust that floor: the control plane throttles at 256M and is stopped at 512M,
and the agent — whose cgroup also contains `apt`, `dpkg`, and `podman` — at 1G
and 1536M. Both cap tasks and open files, and both enter a failed state after
ten restarts in five minutes instead of restarting forever.
