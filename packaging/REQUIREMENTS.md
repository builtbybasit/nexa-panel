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
  its server, client, dump utility, Unix socket, and systemd service.

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
