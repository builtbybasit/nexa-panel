# Runbook: Deployments (deploy keys, the current release, and the FPM sudoers drop-in)

Three recoveries for deployer-mode sites, in the order they come up in practice:
rotating a deploy key, repairing a site whose `current` symlink no longer points
at a release, and removing the FPM reload sudoers drop-in by hand.

Throughout, `<slug>` is the site's immutable slug, its Unix Owner is
`nexa_<slug>`, and `{root}` is the site root — `/srv/nexa/sites/<slug>` on a
default install. The release tree is nested one level below it:

| Path | Owner | What it is |
| --- | --- | --- |
| `{root}` | `root:root` `0755` | Site root. Must stay root-owned — it is the per-site SFTP `ChrootDirectory`. |
| `{root}/app` | `nexa_<slug>:nexa_<slug>` `0755` | Deploy path. The deploy tool needs write here to swap `current`. |
| `{root}/app/releases/` | `nexa_<slug>:www-data` `0755` | One directory per Release. The panel never writes below this. |
| `{root}/app/shared/` | `nexa_<slug>:www-data` `0750` | Shared Path — the `.env` and anything that outlives a Release. |
| `{root}/app/current` | symlink | Current Release. Nginx and PHP-FPM resolve through it. |

## Rotating a deploy key

Rotate when the key may have been exposed, when someone with node access has
left, or when GitHub reports a key you do not recognize on the repository.

Rotation is **immediately breaking**: the new pair replaces the old one on the
node the moment you confirm it, and every deploy fails until the new public half
is registered on GitHub. Do it with a maintenance window, not mid-deploy.

1. In the panel, open **Sites → the site → Deployment → GitHub access** and
   press **Rotate key**. The site's primary domain is the type-to-confirm.
2. Copy the new public key from the card and add it as a deploy key on the
   repository (**Add it on GitHub** opens the right settings page). Read-only
   access is enough.
3. Delete the **old** deploy key on GitHub. Rotation does not — and cannot —
   remove it; until you do, the retired key is still trusted by the repository.
4. Press **Test connection** and confirm the card reports the account GitHub
   greeted and a branch count.

Notes:

- The private half is generated on the node and never reaches the control panel,
  so there is nothing to rotate in `control.db` and nothing to shred off-box.
  The pair lives at `{root}/.ssh/id_ed25519`, owned by `nexa_<slug>`, `0600`.
- `{root}/.ssh/known_hosts` is written by the panel from pinned GitHub host keys.
  Do not hand-edit it — the panel rewrites it, and a host key you added yourself
  is silently dropped on the next write.
- If the test fails with a host-key error rather than a permission error, the
  problem is `known_hosts`, not the key: re-save the deploy key to have the panel
  rewrite it.

## Recovering a site whose `current` symlink is broken

Symptoms: the site serves 502 or 404, `nginx -t` passes, and applying the site
fails on the document root check. A deploy that died between writing a release
and swapping the link, or a manually deleted release, leaves `current` dangling.

The panel refuses to guess here on purpose: `current` belongs to the deploy tool,
and the panel only ever creates it when it is **absent**. So the repair is to put
a valid link back and let the next deploy take over.

1. Look at what is actually there:

   ```
   ls -l /srv/nexa/sites/<slug>/app
   ls -1 /srv/nexa/sites/<slug>/app/releases
   readlink -f /srv/nexa/sites/<slug>/app/current
   ```

2. **Repoint the link at a release that exists.** Do it as the site's own
   account, from inside the deploy path, with a relative target — that is exactly
   what the deploy tool does, and it keeps the link's ownership right:

   ```
   runuser -u nexa_<slug> -- \
     ln -sfn releases/<release> /srv/nexa/sites/<slug>/app/current
   ```

   `ln -sfn` replaces the link atomically. Never `rm` the link first: between the
   two commands every request 404s.

3. If `releases/` is empty — nothing has ever deployed, or someone cleaned it out
   — recreate the seeded release the panel makes at activation, then let the
   panel re-verify:

   ```
   install -d -o nexa_<slug> -g www-data -m 0755 /srv/nexa/sites/<slug>/app/releases/initial
   install -d -o nexa_<slug> -g www-data -m 0750 /srv/nexa/sites/<slug>/app/releases/initial/public
   runuser -u nexa_<slug> -- \
     ln -sfn releases/initial /srv/nexa/sites/<slug>/app/current
   ```

4. Re-apply the site from the panel (**Sites → the site → Settings → Save**, or
   any change that re-applies). The apply asserts the document root resolves
   before it validates Nginx, so a still-broken link fails the job rather than
   the site.

If `current` is a **regular directory** rather than a symlink, the panel refuses
to adopt it (`current is not a managed release link`) — something wrote into the
deploy path directly. Move it aside (`mv current current.broken`) and redo step 2
or 3; do not delete it until you have checked what is inside.

Switching the site back to standard mode is a valid escape hatch: the vhost goes
back to `{root}/public` and the release tree is left untouched on disk.

## Removing the FPM reload sudoers drop-in by hand

Deployer-mode sites get exactly two root-owned artifacts so the site account can
reload PHP-FPM after a release swap:

| Path | Mode | What it is |
| --- | --- | --- |
| `/etc/nexa-panel/generated/deploy/nexa-fpm-reload-<slug>.sh` | `0555 root:root` | Fixed, argument-less wrapper. Runs one `systemctl reload php<ver>-fpm.service`. |
| `/etc/sudoers.d/nexa-deploy-<slug>` | `0440 root:root` | One `NOPASSWD` rule for that one absolute path. |

Both are written and removed through the panel's FPM reload operator, and the
panel drives it on its own at three points:

| When | What the panel does |
| --- | --- |
| The prepare job runs for a **deployer-mode** site | Installs the pair. A standard-mode site never gets one. |
| The site **leaves** deployer mode | Removes the pair, *before* the mode column moves — a switch whose withdrawal fails is refused, so a standard-mode site never keeps a rule. |
| The site is **deleted** | Removes the pair while the row still exists, so a rule naming a dead slug cannot be inherited by the next site created with that slug. |

So a drop-in should normally only be removed by hand when one of those paths
failed against an unreachable node and left the pair behind, or when you are
withdrawing the privilege from a site that is still live and still in deployer
mode. Everything below is that manual path.

1. Confirm what the account may currently run:

   ```
   sudo -l -U nexa_<slug>
   ```

   The output must list exactly the one wrapper path. Anything else on that list
   did not come from Nexa.

2. **Validate before you edit, and never edit in place.** A malformed file under
   `/etc/sudoers.d` breaks `sudo` host-wide. `visudo` on the drop-in is safe
   because it refuses to save a file that does not parse:

   ```
   visudo -f /etc/sudoers.d/nexa-deploy-<slug>
   ```

3. To remove the privilege, delete the drop-in first and the wrapper second — in
   that order, so there is never a moment where a rule points at a path that
   something else could create:

   ```
   rm -f /etc/sudoers.d/nexa-deploy-<slug>
   rm -f /etc/nexa-panel/generated/deploy/nexa-fpm-reload-<slug>.sh
   ```

4. Verify that both the grant and the whole sudoers set are still sane:

   ```
   sudo -l -U nexa_<slug>
   visudo -c
   ```

   `visudo -c` must print `parsed OK` for every file. If it does not, fix it
   **now** — from the root shell you already have open, because a broken sudoers
   file is not something you can repair through `sudo` later.

Notes:

- The wrapper takes no arguments and its contents are fixed. If you find one that
  takes an argument, or a sudoers rule with a wildcard or `ALL` command, it is
  not a file this panel wrote — treat it as an incident.
- `systemctl reload php<ver>-fpm.service` reloads every pool on that PHP branch,
  not just this site's. That is a known limitation of a branch-wide FPM master;
  a reload is graceful, so it costs no requests.
- Re-running the prepare job (**Deployment → Server prerequisites → Prepare this
  node**) *does* recreate a drop-in you removed, if the site is in deployer mode
  — that is the supported way to restore one. To withdraw the privilege for good,
  switch the site back to standard mode rather than deleting the files, or the
  next prepare run will put them back.
