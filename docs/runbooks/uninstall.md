# Uninstall and purge

Inspect the exact plan first:

```bash
sudo nexa-uninstall --dry-run
```

The default operation stops and removes the panel control plane, agent, admin
tool units, generated backup timers, packaged units, and panel ingress. It
retains hosted sites, database servers/data, panel state, backups, encryption
keys, release credentials, TLS material, and site-serving Nginx/PHP
configuration:

```bash
sudo nexa-uninstall
```

Take and independently verify backups before destructive purge. Purge requires
both the destructive flag and non-interactive confirmation:

```bash
sudo nexa-uninstall --dry-run --purge-data
sudo nexa-uninstall --purge-data --yes
```

Purge removes Nexa-owned state, hosted site trees, panel backups and managed
site accounts. It intentionally does not remove Ubuntu packages or external
database clusters because those may be shared. Review the final plan and remove
unneeded packages separately with the distribution package manager.

After either mode, validate `nginx -t`, inspect `systemctl --failed`, and confirm
that the expected customer sites/databases are either retained or absent.
