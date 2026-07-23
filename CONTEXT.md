# Nexa Panel Context

This glossary defines the language used by the control plane, privileged agent,
and feature modules of Nexa Panel.

## Managed Web Hosting

**Site**:
A managed web workload with one immutable slug, one Unix owner, one root directory, and an attached runtime.
_Avoid_: Website, vhost, app

**Primary Domain**:
The globally unique hostname used as a site's canonical routing identity. It is attached to a site but is not the site's identifier.
_Avoid_: Site name, URL

**Domain**:
A normalized, globally unique hostname routed to a site as a primary domain, alias, subdomain, or redirect.
_Avoid_: URL, site

**Alias**:
An additional hostname that serves the same Site without changing the browser location.
_Avoid_: Redirect, parked site

**Redirect**:
A hostname that permanently sends requests to a canonical target hostname.
_Avoid_: Alias, rewrite

**Certificate**:
Observed TLS material issued for a Site's active non-redirect Domains, with lifecycle and expiry state but no exposed private key.
_Avoid_: SSL domain, HTTPS switch

**Runtime**:
An installed and policy-allowed PHP-FPM version with a discovered set of capabilities.
_Avoid_: PHP binary, interpreter

**Runtime Pool**:
The dedicated PHP-FPM worker pool and Unix socket that execute one site's PHP requests as that site's Unix owner.
_Avoid_: Shared pool, PHP service

**Unix Owner**:
The operating-system identity that exclusively owns a site's files and runs its Runtime Pool.
_Avoid_: Panel user, administrator

**Desired State**:
The persisted configuration that the control plane has approved for a managed resource.
_Avoid_: Current state, configuration file

**Observed State**:
The resource state reported from the node after inspection or post-change verification.
_Avoid_: Desired state, cached state

## Managed Databases

**PostgreSQL Instance**:
An independently managed PostgreSQL cluster with one major version and its own connection endpoint, storage, logs, and lifecycle state.
_Avoid_: PostgreSQL server, database, node

**Managed Database**:
A logical database belonging to exactly one PostgreSQL Instance and owned by one Database Role.
_Avoid_: Schema, instance, control database

**Database Role**:
A non-superuser login identity whose ownership and grants are limited to managed databases in one PostgreSQL Instance.
_Avoid_: Database user, administrator, Unix user

**Logical Backup**:
A portable dump of one Managed Database that can become a Restore Point after integrity verification.
_Avoid_: Snapshot, filesystem copy, export file

**MySQL-family Engine**:
The single native MySQL or MariaDB installation selected for a managed node, with an engine identity that cannot be changed by reusing its data directory.
_Avoid_: MySQL-compatible mode, drop-in replacement, database

**Database Account**:
A MySQL-family login identity defined by both account name and allowed connection host, with privileges scoped to managed databases.
_Avoid_: Database Role, Unix user, administrator

**Admin Tool**:
An isolated browser application that administers one database engine only through Nexa-authorized sessions.
_Avoid_: Database panel, public phpMyAdmin, public pgAdmin

**Admin Tool Launch**:
A single-use, short-lived authorization exchanged server-side for an Admin Tool session without exposing database credentials to the browser.
_Avoid_: Auto login, password link, permanent session

## Operations

**Plan**:
A short-lived, agent-issued description of an exact change derived from current Observed State and a requested Desired State.
_Avoid_: Preview, dry run

**Job**:
A durable execution record for approved work, including progress, outcome, and recovery information.
_Avoid_: Task, process

**Restore Point**:
A backup snapshot whose manifest and checksums can be used to reconstruct its captured resources.
_Avoid_: Backup job, archive

## Site Tools

**File Broker**:
The typed, site-scoped file interface that resolves every path beneath one site's root and preserves the site's ownership on writes.
_Avoid_: Filesystem browser, shell, FTP

**Write Zone**:
The public, private, tmp, and backups directories where the File Broker permits mutations; everything else under the site root is read-only.
_Avoid_: Whole site root, arbitrary directory

**Upload Session**:
A staged, chunked transfer into a site's tmp directory that becomes the target file only on an explicit, size-verified commit.
_Avoid_: Direct write, form upload

**Log Stream**:
A bounded, offset-resumable live tail of one discovered site log delivered over server-sent events with rotation detection.
_Avoid_: Unbounded tail, log file handle

**Scheduled Task**:
A user-defined cron-expression command that always executes as its site's Unix Owner through a Nexa-owned wrapper script.
_Avoid_: Root cron job, crontab entry, Job

**Wrapper Script**:
The root-owned, site-group-executable script that enforces a Scheduled Task's timeout, overlap skipping, bounded output, and run recording.
_Avoid_: User script, inline command

**Task Run**:
One recorded execution of a Scheduled Task with start time, duration, exit status, trigger, and bounded output.
_Avoid_: Job, log line

**Site Grant**:
An administrator-managed assignment that limits a developer account to specific sites; ungranted sites stay invisible.
_Avoid_: Share, permission flag, role

## Deployments

**SSH Access**:
Interactive shell login to a site delivered through the site's own Unix Owner, an sshd drop-in, and a set of operator-installed authorized keys; mutually exclusive with that site's SFTP jail.
_Avoid_: SFTP access, shell account, root login

**Deploy Key**:
The site-scoped keypair generated on the node so the site's Unix Owner can read one Git repository; only the public half and its fingerprint ever reach the control plane.
_Avoid_: Access token, credential, private key

**Deployer Mode**:
The site setting under which an external deploy tool owns the served tree, so the panel renders the virtual host against the Current Release and never manages a file beneath it.
_Avoid_: Git mode, deployment method, standard mode

**Release**:
One immutable directory of application files placed under a site's release tree by a deploy tool and owned by that site's Unix Owner; the panel observes releases but never writes one.
_Avoid_: Build, version, artifact

**Current Release**:
The symlink the deploy tool swaps to publish a Release, and the only path the virtual host and Runtime Pool of a deployer-mode site resolve through.
_Avoid_: Live directory, latest release, document root

**Shared Path**:
The per-site directory of state that outlives every Release — uploads, caches, and the shared environment file — linked into each Release by the deploy tool.
_Avoid_: Persistent volume, storage, site root

**Prepared Node**:
A node observed to carry every tool a deploy needs, recorded by a preparation run that installs what is missing and reports, without changing, whether the firewall still admits SSH.
_Avoid_: Provisioned server, healthy node, prerequisite check
