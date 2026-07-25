#!/usr/bin/env bash
# Remove the Nexa Panel control plane from an Ubuntu node.
#
# The safe default removes only program-owned runtime files and leaves hosted
# sites, databases, backups, encryption keys, and the control database intact.
# Destructive data removal is a separate, explicitly confirmed operation.
set -euo pipefail

export SYSTEMD_PAGER=''
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

DRY_RUN=0
PURGE_DATA=0
ASSUME_YES=0
SSHD_CONFIG_CHANGED=0
PHP_CONFIG_CHANGED=0
CRON_CONFIG_CHANGED=0
SUDOERS_CONFIG_CHANGED=0

usage() {
  cat <<'EOF'
Usage: uninstall.sh [--dry-run] [--purge-data --yes]

  --dry-run      Print the complete plan without changing the node. Does not
                 require root and is safe to run before a maintenance window.
  --purge-data   Also delete panel state, sites, backups, credentials, and
                 managed site accounts. This cannot be undone.
  --yes          Confirm destructive --purge-data execution. A purge dry-run
                 never requires confirmation.
  -h, --help     Show this help.

Without --purge-data, hosted sites, database servers and data, TLS material,
panel state, backups, and encryption/release credentials are retained.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --purge-data) PURGE_DATA=1 ;;
    --yes) ASSUME_YES=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [[ "$PURGE_DATA" -eq 1 && "$DRY_RUN" -eq 0 && "$ASSUME_YES" -eq 0 ]]; then
  echo "error: --purge-data permanently deletes sites, panel state, keys, and backups; re-run with --yes after taking an independent backup" >&2
  exit 2
fi
if [[ "$DRY_RUN" -eq 0 && "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "error: uninstallation must run as root (try sudo); use --dry-run to inspect the plan" >&2
  exit 1
fi

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

run() {
  say "RUN $*"
  [[ "$DRY_RUN" -eq 1 ]] || "$@"
}

remove_file() {
  local path="$1"
  say "REMOVE $path"
  [[ "$DRY_RUN" -eq 1 ]] || rm -f -- "$path"
}

remove_tree() {
  local path="$1"
  case "$path" in
    /etc/nexa-panel|/var/lib/nexa-panel|/var/lib/nexa-panel-update|/var/log/nexa-panel|/srv/nexa|/var/lib/postgresql/nexa-backups|/etc/systemd/system/nexa-api.service.d|/usr/lib/nexa-panel|/etc/nginx/nexa-includes|/run/nexa-panel)
      ;;
    *)
      echo "error: refusing an unexpected recursive removal target: $path" >&2
      exit 1
      ;;
  esac
  say "REMOVE $path"
  [[ "$DRY_RUN" -eq 1 ]] || rm -rf --one-file-system -- "$path"
}

disable_unit() {
  local unit="$1" load_state unit_file_state
  say "DISABLE $unit"
  if [[ "$DRY_RUN" -eq 0 ]] && command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    load_state="$(systemctl show --property=LoadState --value "$unit" 2>/dev/null || true)"
    [[ -n "$load_state" && "$load_state" != "not-found" ]] || return 0
    systemctl stop "$unit" || die "could not stop $unit; no program files were removed"
    if systemctl is-active --quiet "$unit"; then
      die "$unit is still active after systemd reported a successful stop"
    fi
    unit_file_state="$(systemctl show --property=UnitFileState --value "$unit" 2>/dev/null || true)"
    case "$unit_file_state" in
      enabled|enabled-runtime|linked|linked-runtime|alias)
        systemctl disable "$unit" || die "could not disable $unit"
        ;;
      static|indirect|generated|disabled|masked|masked-runtime|transient|"")
        ;;
      *)
        die "refusing to ignore unexpected enablement state '$unit_file_state' for $unit"
        ;;
    esac
  fi
}

remove_generated_file() {
  local path="$1" expected="$2" first_line link_target
  [[ "$path" =~ $expected ]] || die "refusing unexpected generated configuration path: $path"
  if [[ -L "$path" ]]; then
    link_target="$(readlink "$path")"
    case "$path:$link_target" in
      /etc/nginx/sites-enabled/nexa-*.conf:/etc/nginx/sites-available/nexa-*.conf|/etc/nginx/sites-enabled/nexa-*.conf:../sites-available/nexa-*.conf)
        ;;
      *) die "refusing unrecognized generated symlink $path -> $link_target" ;;
    esac
  elif [[ -f "$path" ]]; then
    IFS= read -r first_line < "$path" || true
    [[ "$first_line" == '# Managed by Nexa Panel.'* || "$first_line" == '; Managed by Nexa Panel.'* ]] ||
      die "refusing to delete $path because it lacks the Nexa Panel ownership header"
  else
    die "refusing generated configuration path with an unsupported file type: $path"
  fi
  remove_file "$path"
}

if [[ "$DRY_RUN" -eq 0 ]]; then
  install -d -m 0755 /run/lock
  exec 9>/run/lock/nexa-panel-lifecycle.lock
  if ! flock -n 9; then
    echo "error: another Nexa Panel install, update, or uninstall is running" >&2
    exit 1
  fi
fi

say "Nexa Panel uninstall plan"
say "MODE $([[ "$PURGE_DATA" -eq 1 ]] && echo purge-data || echo retain-data)"

for unit in \
  nexa-pgadmin-idle-stop.timer \
  nexa-pgadmin-idle-stop.service \
  nexa-pgadmin.service \
  nexa-phpmyadmin.service \
  nexa-panel-system-backup.timer \
  nexa-panel-system-backup.service \
  nexa-update-recovery.service \
  nexa-api.service \
  nexa-agent.service
do
  disable_unit "$unit"
done

# Generated backup timers invoke /usr/bin/nexa and must not be left firing after
# the binary is removed. Their durable plans remain in the retained control DB.
for path in /etc/systemd/system/nexa-backup-*.timer /etc/systemd/system/nexa-backup-*.service; do
  [[ -e "$path" ]] || continue
  disable_unit "$(basename "$path")"
  remove_file "$path"
done

[[ -e /etc/phpmyadmin/conf.d/nexa-panel.php || -L /etc/phpmyadmin/conf.d/nexa-panel.php ]] && PHP_CONFIG_CHANGED=1
for path in \
  /usr/lib/systemd/system/nexa-agent.service \
  /usr/lib/systemd/system/nexa-api.service \
  /usr/lib/systemd/system/nexa-panel-system-backup.service \
  /usr/lib/systemd/system/nexa-panel-system-backup.timer \
  /usr/lib/systemd/system/nexa-update-recovery.service \
  /etc/systemd/system/nexa-pgadmin-idle-stop.service \
  /etc/systemd/system/nexa-pgadmin-idle-stop.timer \
  /etc/systemd/system/nexa-phpmyadmin.service \
  /etc/containers/systemd/nexa-pgadmin.container \
  /usr/lib/sysusers.d/nexa-panel.conf \
  /usr/lib/tmpfiles.d/nexa-panel.conf \
  /etc/nginx/sites-enabled/nexa-panel.conf \
  /etc/nginx/sites-available/nexa-panel.conf \
  /etc/nginx/snippets/nexa-panel-proxy.conf \
  /etc/nginx/conf.d/zzz-nexa-sites-enabled.conf \
  /etc/nginx/sites-enabled/nexa-phpmyadmin.conf \
  /etc/nginx/sites-available/nexa-phpmyadmin.conf \
  /etc/phpmyadmin/conf.d/nexa-panel.php \
  /usr/bin/nexa \
  /usr/bin/nexa.prev \
  /usr/bin/nexa.previous
do
  remove_file "$path"
done

# The native phpMyAdmin integration may have been installed against any
# supported PHP branch. Remove only the exact panel-owned service drop-in, not
# the surrounding operator/package directory.
for path in /etc/systemd/system/php*-fpm.service.d/nexa-phpmyadmin.conf; do
  [[ -e "$path" || -L "$path" ]] || continue
  remove_generated_file "$path" '^/etc/systemd/system/php[0-9]+\.[0-9]+-fpm\.service\.d/nexa-phpmyadmin\.conf$'
  PHP_CONFIG_CHANGED=1
done

say "REMOVE /etc/systemd/system/nexa-api.service.d"
[[ "$DRY_RUN" -eq 1 ]] || remove_tree /etc/systemd/system/nexa-api.service.d
say "REMOVE /run/nexa-panel"
[[ "$DRY_RUN" -eq 1 ]] || remove_tree /run/nexa-panel

# Repair installations made before the API and privileged-agent socket groups
# were split. Failure is harmless when the membership or account is absent.
say "REMOVE_GROUP_MEMBERSHIP www-data nexa"
if [[ "$DRY_RUN" -eq 0 ]] && command -v gpasswd >/dev/null 2>&1; then
  gpasswd -d www-data nexa >/dev/null 2>&1 || true
fi

UFW_RULES_FILE="/var/lib/nexa-panel/install/ufw.rules"
if [[ -f "$UFW_RULES_FILE" ]]; then
  while IFS= read -r rule; do
    [[ "$rule" =~ ^[0-9]{1,5}/(tcp|udp)$ ]] || continue
    say "REMOVE_UFW_RULE $rule"
    if [[ "$DRY_RUN" -eq 0 ]] && command -v ufw >/dev/null 2>&1; then
      if ufw status 2>/dev/null | grep -Eq "^[[:space:]]*${rule//\//\\/}[[:space:]]+ALLOW([[:space:]]|$)"; then
        ufw --force delete allow "$rule" >/dev/null || die "could not remove panel-owned UFW rule $rule"
      fi
    fi
  done < "$UFW_RULES_FILE"
fi

DEFAULT_TARGET_FILE="/var/lib/nexa-panel/install/nginx-default.target"
if [[ -f "$DEFAULT_TARGET_FILE" && ! -e /etc/nginx/sites-enabled/default ]]; then
  default_target="$(head -n 1 "$DEFAULT_TARGET_FILE")"
  case "$default_target" in
    /etc/nginx/sites-available/*|../sites-available/*)
      say "RESTORE /etc/nginx/sites-enabled/default -> $default_target"
      if [[ "$DRY_RUN" -eq 0 ]]; then
        ln -s -- "$default_target" /etc/nginx/sites-enabled/default
      fi
      ;;
    *)
      say "SKIP unsafe recorded Nginx default target"
      ;;
  esac
fi

if [[ "$PURGE_DATA" -eq 0 ]]; then
  for path in /var/lib/nexa-panel /var/lib/nexa-panel-update /srv/nexa/sites /var/lib/postgresql /etc/nexa-panel /var/log/nexa-panel; do
    say "RETAIN $path"
  done
  say "RETAIN hosted-site Nginx/PHP configuration and database packages"
  # Said separately because it is the one retained file that decides what a later
  # reinstall does: the panel vhost is removed above, and without the record a
  # reinstall would have nothing left to learn the hostname and TLS mode from.
  say "RETAIN /etc/nexa-panel/publishing.json (a reinstall republishes on the same hostname and TLS mode)"
else
  # Enumerate accounts from panel-owned site roots and verify the exact account
  # contract before deleting anything. Prefix matching every `nexa_*` account
  # could destroy an unrelated operator-created user.
  MANAGED_SITE_ACCOUNTS=()
  if command -v getent >/dev/null 2>&1; then
    for site_root in /srv/nexa/sites/*; do
      [[ -d "$site_root" && ! -L "$site_root" ]] || continue
      slug="$(basename "$site_root")"
      [[ "$slug" =~ ^[a-z][a-z0-9-]{1,31}$ ]] || continue
      account="nexa_${slug//-/_}"
      account_record="$(getent passwd "$account" 2>/dev/null || true)"
      [[ -n "$account_record" ]] || continue
      IFS=: read -r found_account _ found_uid _ _ found_home found_shell <<< "$account_record"
      [[ "$found_account" == "$account" && "$found_uid" != "0" && "$found_home" == "$site_root" ]] ||
        die "account $account does not match the managed site identity for $site_root"
      case "$found_shell" in
        /usr/sbin/nologin|/bin/bash) ;;
        *) die "account $account has unexpected shell $found_shell; refusing to delete it" ;;
      esac
      MANAGED_SITE_ACCOUNTS+=("$account")
    done
  fi

  for account in "${MANAGED_SITE_ACCOUNTS[@]}"; do
    say "REMOVE_MANAGED_SITE_ACCOUNT $account"
    if [[ "$DRY_RUN" -eq 0 ]]; then
      # Terminate anything the account still owns before deleting it. On-demand
      # PHP-FPM workers and in-flight scheduled tasks run as the site user, and
      # userdel refuses while a live PID exists. The site's FPM pool, Nginx route,
      # and cron entries are all removed below, so these processes are transient;
      # an on-demand worker can spawn at any instant, so stop them first rather
      # than racing the deletion. TERM, settle, then KILL as a last resort.
      if command -v pkill >/dev/null 2>&1; then
        pkill -TERM -u "$account" 2>/dev/null || true
        for _ in 1 2 3 4 5 6 7 8 9 10; do
          pgrep -u "$account" >/dev/null 2>&1 || break
          sleep 0.5
        done
        if pgrep -u "$account" >/dev/null 2>&1; then
          pkill -KILL -u "$account" 2>/dev/null || true
          sleep 0.5
        fi
      fi
      userdel "$account" || die "could not delete managed site account $account (it may still own a running process)"
      if getent passwd "$account" >/dev/null 2>&1; then
        die "managed site account $account still exists after userdel"
      fi
      if getent group "$account" >/dev/null 2>&1; then
        groupdel "$account" || die "could not delete managed site group $account"
      fi
    fi
  done

  # Remove generated host configuration by narrow, independently checked path
  # families. Package and operator-owned neighbours are never glob-deleted.
  # These globs also match the panel's own vhosts (nexa-panel.conf and the
  # phpMyAdmin gateway), which are removed by exact path above and — unlike
  # hosted-site vhosts — carry no ownership header. In a real run they are
  # already gone; in a dry-run nothing is removed, so without this skip the glob
  # re-encounters them and the header check aborts the purge dry-run before it
  # can print the rest of the plan.
  for path in /etc/nginx/sites-available/nexa-*.conf; do
    [[ -e "$path" || -L "$path" ]] || continue
    case "$(basename "$path")" in nexa-panel.conf|nexa-phpmyadmin.conf) continue ;; esac
    remove_generated_file "$path" '^/etc/nginx/sites-available/nexa-[a-z0-9-]+\.conf$'
  done
  for path in /etc/nginx/sites-enabled/nexa-*.conf; do
    [[ -e "$path" || -L "$path" ]] || continue
    case "$(basename "$path")" in nexa-panel.conf|nexa-phpmyadmin.conf) continue ;; esac
    remove_generated_file "$path" '^/etc/nginx/sites-enabled/nexa-[a-z0-9-]+\.conf$'
  done
  for path in /etc/nginx/conf.d/nexa-*-ratelimit.conf; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/nginx/conf\.d/nexa-[a-z0-9-]+-ratelimit\.conf$'
  done
  for path in /etc/php/*/fpm/pool.d/nexa-*.conf; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/php/[0-9]+\.[0-9]+/fpm/pool\.d/nexa-[a-z0-9-]+\.conf$'
    PHP_CONFIG_CHANGED=1
  done
  for path in /etc/logrotate.d/nexa-*; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/logrotate\.d/nexa-[a-z0-9-]+$'
  done
  for path in /etc/cron.d/nexa-task-*; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/cron\.d/nexa-task-[A-Za-z0-9_-]+$'
    CRON_CONFIG_CHANGED=1
  done
  for path in /etc/ssh/sshd_config.d/nexa-site-*.conf /etc/ssh/sshd_config.d/nexa-access-*.conf; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/ssh/sshd_config\.d/nexa-(site|access)-[a-z0-9-]+\.conf$'
    SSHD_CONFIG_CHANGED=1
  done
  for path in /etc/sudoers.d/nexa-deploy-*; do
    [[ -e "$path" || -L "$path" ]] || continue
    remove_generated_file "$path" '^/etc/sudoers\.d/nexa-deploy-[a-z0-9-]+$'
    SUDOERS_CONFIG_CHANGED=1
  done
  say "REMOVE /etc/nginx/nexa-includes"
  [[ "$DRY_RUN" -eq 1 ]] || remove_tree /etc/nginx/nexa-includes

  # Print the leaf explicitly so a dry-run makes the irreversible site deletion
  # impossible to miss, then remove only the audited parent roots.
  say "REMOVE /srv/nexa/sites"
  remove_tree /srv/nexa
  remove_tree /var/lib/nexa-panel
  remove_tree /var/lib/nexa-panel-update
  remove_tree /var/lib/postgresql/nexa-backups
  remove_tree /etc/nexa-panel
  remove_tree /var/log/nexa-panel

  say "REMOVE_SERVICE_ACCOUNT nexa"
  if [[ "$DRY_RUN" -eq 0 ]]; then
    if getent passwd nexa >/dev/null 2>&1; then
      userdel nexa || die "could not delete the nexa service account"
    fi
    if getent group nexa >/dev/null 2>&1; then
      groupdel nexa || die "could not delete the nexa service group"
    fi
  fi
fi

if [[ "$DRY_RUN" -eq 0 ]]; then
  if [[ "$SSHD_CONFIG_CHANGED" -eq 1 ]] && command -v sshd >/dev/null 2>&1; then
    sshd -t || die "OpenSSH validation failed after removing panel site access drop-ins"
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet ssh.service; then
      systemctl reload ssh.service || die "could not reload OpenSSH after removing panel site access drop-ins"
    fi
  fi
  if [[ "$SUDOERS_CONFIG_CHANGED" -eq 1 ]] && command -v visudo >/dev/null 2>&1; then
    visudo -cf /etc/sudoers >/dev/null || die "sudoers validation failed after removing panel deploy grants"
  fi
  if [[ "$PHP_CONFIG_CHANGED" -eq 1 ]]; then
    for php_config_dir in /etc/php/*/fpm; do
      [[ -d "$php_config_dir" ]] || continue
      php_branch="$(basename "$(dirname "$php_config_dir")")"
      [[ "$php_branch" =~ ^[0-9]+\.[0-9]+$ ]] || continue
      php_fpm_binary="$(command -v "php-fpm${php_branch}" 2>/dev/null || true)"
      [[ -z "$php_fpm_binary" ]] || "$php_fpm_binary" -t || die "PHP-FPM $php_branch validation failed after removing panel pools"
      if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "php${php_branch}-fpm.service"; then
        systemctl reload "php${php_branch}-fpm.service" || die "could not reload PHP-FPM $php_branch"
      fi
    done
  fi
  if [[ "$CRON_CONFIG_CHANGED" -eq 1 ]] && command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet cron.service; then
    systemctl reload cron.service || die "could not reload cron after removing panel tasks"
  fi
  if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    systemctl daemon-reload || die "systemd could not reload its unit index"
    # Clear lingering failed state for every panel unit, not just the two core
    # services: a unit that was failed before uninstall (e.g. nexa-pgadmin.service)
    # otherwise survives in `systemctl --failed` as a phantom after its unit file
    # is gone, until the next reboot or manual reset-failed.
    systemctl reset-failed \
      nexa-pgadmin-idle-stop.timer nexa-pgadmin-idle-stop.service \
      nexa-pgadmin.service nexa-phpmyadmin.service \
      nexa-panel-system-backup.timer nexa-panel-system-backup.service \
      nexa-update-recovery.service nexa-api.service nexa-agent.service \
      >/dev/null 2>&1 || true
  fi
  if command -v nginx >/dev/null 2>&1; then
    nginx -t || die "Nginx validation failed after removing panel configuration"
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx.service; then
      systemctl reload nginx.service || die "could not reload Nginx after removing panel configuration"
    fi
  fi
fi

# Keep the recovery command available until every fallible stop, removal,
# validation, and reload has succeeded. These are intentionally the final two
# filesystem mutations so a partial uninstall remains retryable.
say "REMOVE /usr/lib/nexa-panel"
[[ "$DRY_RUN" -eq 1 ]] || remove_tree /usr/lib/nexa-panel
remove_file /usr/sbin/nexa-uninstall

say "Nexa Panel uninstallation complete. Ubuntu packages and external database data were not removed."
