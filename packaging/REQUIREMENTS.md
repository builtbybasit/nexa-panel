# Managed Node Requirements

Nexa Panel's API is unprivileged. The root agent requires these host packages
for Milestones 2 and 3:

- Nginx with Debian/Ubuntu `sites-available` and `sites-enabled` layout;
- each operator-selected `php<version>-fpm` service and matching
  `php-fpm<version>` validation binary;
- Certbot with the webroot authenticator;
- systemd and the standard `useradd` account-management tools.
- `postgresql-common`, `libjson-perl`, plus any managed PostgreSQL 16, 17, or 18 server and
  client packages (`pg_lsclusters`, `pg_createcluster`, and version-matched
  `psql`, `pg_dump`, and `pg_restore` binaries).

The packaged tmpfiles configuration creates `/srv/nexa/sites` and the public
`/srv/nexa/acme/.well-known/acme-challenge` webroot. The agent systemd unit has
write access only to the managed Nginx/PHP/Let's Encrypt, site, account, runtime,
and Nexa paths required by these operations.
