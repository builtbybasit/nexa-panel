#!/usr/bin/env bash
set -euo pipefail

root="$(mktemp -d)"
suffix="$$"
network="nexa-admin-tools-${suffix}"
mysql="nexa-admin-mysql-${suffix}"
postgres="nexa-admin-postgres-${suffix}"
phpmyadmin="nexa-admin-phpmyadmin-${suffix}"
pgadmin="nexa-admin-pgadmin-${suffix}"

cleanup() {
  docker rm -f "$phpmyadmin" "$pgadmin" "$mysql" "$postgres" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$root"
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run -d --name "$mysql" --network "$network" -e MYSQL_ROOT_PASSWORD=root mysql:8.4 --port=3306 >/dev/null
docker run -d --name "$postgres" --network "$network" -e POSTGRES_PASSWORD=postgres postgres:18 >/dev/null

for _ in $(seq 1 90); do
  mysql_port="$(docker exec "$mysql" mysql --user=root --password=root --batch --skip-column-names --execute 'SELECT @@port;' 2>/dev/null || true)"
  postgres_ready="$(docker exec "$postgres" pg_isready -U postgres 2>/dev/null || true)"
  if [[ "$mysql_port" == "3306" && "$postgres_ready" == *"accepting connections"* ]]; then break; fi
  sleep 1
done
[[ "$mysql_port" == "3306" && "$postgres_ready" == *"accepting connections"* ]]

docker exec -i "$mysql" mysql --user=root --password=root <<'SQL'
CREATE DATABASE app_db;
CREATE USER 'app_user'@'%' IDENTIFIED BY 'database-secret';
GRANT ALL PRIVILEGES ON app_db.* TO 'app_user'@'%';
SQL

mkdir -p "$root/php/sessions" "$root/pg/data"
chmod 0777 "$root/php/sessions" "$root/pg/data"
printf '%s\n' "<?php" "\$cfg['Servers'][1]['auth_type'] = 'signon';" "\$cfg['Servers'][1]['SignonSession'] = 'SignonSession';" "\$cfg['Servers'][1]['SignonURL'] = '/';" > "$root/php/config.user.inc.php"
printf '%s\n' "<?php" "\$cfg['blowfish_secret'] = '0123456789abcdef0123456789abcdef';" > "$root/php/config.secret.inc.php"
printf '%s' 'PMA_single_signon_user|s:8:"app_user";PMA_single_signon_password|s:15:"database-secret";PMA_single_signon_host|s:'"${#mysql}"':"'"$mysql"'";PMA_single_signon_port|s:4:"3306";PMA_single_signon_only_db|s:6:"app_db";' > "$root/php/sessions/sess_acceptanceSession1"
chmod 0644 "$root/php/config.user.inc.php" "$root/php/config.secret.inc.php" "$root/php/sessions/sess_acceptanceSession1"

docker run -d --name "$phpmyadmin" --network "$network" --read-only --cap-drop ALL --memory 128m --pids-limit 128 -p 127.0.0.1:19080:80 --tmpfs /tmp:rw,noexec,nosuid,size=32m --tmpfs /var/run/apache2:rw,noexec,nosuid,size=4m --tmpfs /var/lock/apache2:rw,noexec,nosuid,size=4m --tmpfs /var/log/apache2:rw,noexec,nosuid,size=16m -e PMA_HOST="$mysql" -v "$root/php/config.user.inc.php:/etc/phpmyadmin/config.user.inc.php:ro" -v "$root/php/config.secret.inc.php:/etc/phpmyadmin/config.secret.inc.php:ro" -v "$root/php/sessions:/sessions" phpmyadmin:5.2.3 >/dev/null

for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:19080/ >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsSL -c "$root/php/cookies" -b 'SignonSession=acceptanceSession1' http://127.0.0.1:19080/ > "$root/php/page.html"
grep -q 'phpMyAdmin' "$root/php/page.html"
if grep -q 'name="pma_username"' "$root/php/page.html"; then
  echo 'phpMyAdmin signon returned the login form' >&2
  grep -E -m 3 'error|Error|pma_username|login' "$root/php/page.html" >&2 || true
  docker logs "$phpmyadmin" >&2 || true
  exit 1
fi

printf '%s\n' "AUTHENTICATION_SOURCES = ['webserver']" "WEBSERVER_AUTO_CREATE_USER = True" "WEBSERVER_REMOTE_USER = 'HTTP_X_FORWARDED_USER'" "MASTER_PASSWORD_REQUIRED = False" > "$root/pg/config_local.py"
printf '%s\n' "LOG_FILE = '/dev/null'" > "$root/pg/config_distro.py"
printf '%s\n' 'bootstrap-password' > "$root/pg/bootstrap-password"
printf '%s\n' "$postgres:5432:app_db:postgres:postgres" > "$root/pg/pgpass"
printf '%s\n' '{"Servers":{"1":{"Name":"app_db","Group":"Nexa Panel","Host":"'"$postgres"'","Port":5432,"MaintenanceDB":"postgres","Username":"postgres","SSLMode":"prefer","Shared":true}}}' > "$root/pg/servers.json"
chmod 0644 "$root/pg/config_local.py" "$root/pg/config_distro.py" "$root/pg/bootstrap-password" "$root/pg/pgpass" "$root/pg/servers.json"

docker run -d --name "$pgadmin" --network "$network" --read-only --cap-drop ALL --memory 256m --pids-limit 192 -p 127.0.0.1:19081:5050 --tmpfs /tmp:rw,noexec,nosuid,size=32m -e PGADMIN_LISTEN_PORT=5050 -e PGADMIN_DISABLE_POSTFIX=1 -e PGADMIN_CUSTOM_CONFIG_DISTRO_FILE=/nexa-config/config_distro.py -e PGADMIN_REPLACE_SERVERS_ON_STARTUP=True -e PGPASS_FILE=/nexa-config/pgpass -e PGADMIN_DEFAULT_EMAIL=bootstrap@nexa.example.com -e PGADMIN_DEFAULT_PASSWORD_FILE=/nexa-config/bootstrap-password -v "$root/pg/data:/var/lib/pgadmin" -v "$root/pg/config_local.py:/pgadmin4/config_local.py:ro" -v "$root/pg/config_distro.py:/nexa-config/config_distro.py:ro" -v "$root/pg/servers.json:/pgadmin4/servers.json:ro" -v "$root/pg/pgpass:/nexa-config/pgpass:ro" -v "$root/pg/bootstrap-password:/nexa-config/bootstrap-password:ro" dpage/pgadmin4:9.16 >/dev/null

for _ in $(seq 1 90); do
  if curl -fsS -H 'X-Forwarded-User: admin@nexa.example.com' http://127.0.0.1:19081/ >/dev/null 2>&1; then break; fi
  sleep 1
done
if ! curl -fsS -H 'X-Forwarded-User: admin@nexa.example.com' http://127.0.0.1:19081/ >/dev/null 2>&1; then
  echo 'pgAdmin did not become ready' >&2
  docker logs "$pgadmin" >&2 || true
  exit 1
fi
curl -fsSL -c "$root/pg/cookies" -H 'X-Forwarded-User: admin@nexa.example.com' http://127.0.0.1:19081/ > "$root/pg/page.html"
grep -qi 'pgAdmin' "$root/pg/page.html"
if grep -qi 'name="email"' "$root/pg/page.html"; then
  echo 'pgAdmin webserver authentication returned the login form' >&2
  exit 1
fi
docker exec "$pgadmin" python3 -c "import sqlite3; db=sqlite3.connect('/var/lib/pgadmin/pgadmin4.db'); assert db.execute(\"select count(*) from server where name='app_db' and username='postgres'\").fetchone()[0] == 1"
docker exec "$pgadmin" sh -c "for file in \$(find /var/lib/pgadmin -name .pgpass -type f); do grep -q ':app_db:postgres:' \"\$file\" && exit 0; done; exit 1"

echo 'phpMyAdmin signon and pgAdmin webserver authentication acceptance passed'
