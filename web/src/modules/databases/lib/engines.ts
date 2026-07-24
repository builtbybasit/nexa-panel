import type { EngineKind as ConnectionEngine } from '@/shared/lib/connectionStrings'

import type { ToolKind } from '@/modules/admintools/api'

import type { DatabaseEngine, DatabaseServer, DatabaseUser } from '../api'

/**
 * Per-engine display and capability config — the only place the frontend
 * spells out how engines differ. Adding an engine to the panel means adding a
 * backend adapter and one entry here.
 */
export interface EngineConfig {
  /** Human name of the family ("MySQL / MariaDB"). Servers report a concrete kind. */
  label: string
  /** Web client that opens this engine's databases, if any. */
  tool: ToolKind
  toolLabel: string
  /** Whether the panel can provision additional servers of this engine. */
  provisionable: boolean
  /** Whether users carry a client host scope (MySQL's `user@host`). */
  userHostScopes: boolean
}

export const ENGINES: Record<DatabaseEngine, EngineConfig> = {
  mysql: {
    label: 'MySQL / MariaDB',
    tool: 'phpmyadmin',
    toolLabel: 'phpMyAdmin',
    provisionable: false,
    userHostScopes: true,
  },
  postgresql: {
    label: 'PostgreSQL',
    tool: 'pgadmin',
    toolLabel: 'pgAdmin',
    provisionable: true,
    userHostScopes: false,
  },
}

const KIND_LABELS: Record<DatabaseServer['kind'], string> = {
  mysql: 'MySQL',
  mariadb: 'MariaDB',
  postgresql: 'PostgreSQL',
}

/** "MariaDB 10.11" / "PostgreSQL 18 · main" — the full server name. */
export function serverLabel(server: DatabaseServer): string {
  const base = `${KIND_LABELS[server.kind]} ${server.version}`
  return server.cluster ? `${base} · ${server.cluster}` : base
}

/** Compact form for table cells, where the full label would crowd the row. */
export function serverShort(server: DatabaseServer): string {
  return server.cluster ? `${KIND_LABELS[server.kind]} ${server.version} · ${server.cluster}` : `${KIND_LABELS[server.kind]} ${server.version}`
}

/** `app_usr@localhost` for MySQL-family users, plain `app_usr` elsewhere. */
export function userLabel(user: DatabaseUser): string {
  return user.host ? `${user.name}@${user.host}` : user.name
}

/** Engine kind used by the shared connection-string builders. */
export function connectionEngine(server: DatabaseServer): ConnectionEngine {
  return server.kind === 'postgresql' ? 'postgres' : server.kind
}
