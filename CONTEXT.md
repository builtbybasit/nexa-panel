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
