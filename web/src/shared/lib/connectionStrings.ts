/**
 * Builds the connection strings a desktop client (pgAdmin, Beekeeper Studio,
 * DBeaver, TablePlus, psql, mysql) needs to reach a database on this node.
 *
 * Nexa leaves both engines bound to loopback, so the strings come in two
 * flavours: `direct`, which only works from the node itself or once the port is
 * deliberately exposed, and `tunnel`, which routes through SSH and is what a
 * laptop actually uses. The SSH hop also makes MySQL's `user@localhost` grants
 * match, because the connection arrives on 127.0.0.1.
 */

export type EngineKind = 'postgres' | 'mysql' | 'mariadb'

export interface ConnectionTarget {
  engine: EngineKind
  /** Host the client dials — the node's address, or 127.0.0.1 over a tunnel. */
  host: string
  port: number
  database: string
  username: string
}

export interface TunnelTarget {
  /** SSH login on the node, e.g. `root`. */
  sshUser: string
  /** Address the SSH client dials — normally the same host as the panel. */
  sshHost: string
  /** Port opened on the laptop and forwarded to the engine. */
  localPort: number
}

/** Loopback address the engine listens on, from the node's point of view. */
const LOOPBACK = '127.0.0.1'

/** Default laptop-side ports, offset so they never collide with a local install. */
export const DEFAULT_LOCAL_PORTS: Record<EngineKind, number> = {
  postgres: 15432,
  mysql: 13306,
  mariadb: 13306,
}

/** Percent-encodes a URI component that may contain `@`, `/` or spaces. */
function encode(value: string): string {
  return encodeURIComponent(value)
}

function scheme(engine: EngineKind): string {
  return engine === 'postgres' ? 'postgresql' : 'mysql'
}

/** `postgresql://user@host:5432/db` — password deliberately omitted. */
export function connectionUri(target: ConnectionTarget): string {
  return `${scheme(target.engine)}://${encode(target.username)}@${target.host}:${target.port}/${encode(target.database)}`
}

/** JDBC URL for clients that ask for one (DBeaver, IntelliJ, Metabase). */
export function jdbcUrl(target: ConnectionTarget): string {
  const driver = target.engine === 'postgres' ? 'postgresql' : target.engine
  return `jdbc:${driver}://${target.host}:${target.port}/${encode(target.database)}?user=${encode(target.username)}`
}

/** The engine's own CLI, prompting for the password rather than embedding it. */
export function cliCommand(target: ConnectionTarget): string {
  if (target.engine === 'postgres') {
    return `psql --host=${target.host} --port=${target.port} --username=${target.username} --dbname=${target.database}`
  }
  return `mysql --host=${target.host} --port=${target.port} --user=${target.username} --password ${target.database}`
}

/**
 * Foreground SSH tunnel: hold it open in a terminal, then point the client at
 * 127.0.0.1 on the local port. `-N` keeps it from opening a shell.
 */
export function sshTunnelCommand(target: ConnectionTarget, tunnel: TunnelTarget): string {
  return `ssh -N -L ${tunnel.localPort}:${LOOPBACK}:${target.port} ${tunnel.sshUser}@${tunnel.sshHost}`
}

/** The same target as seen from the laptop once the tunnel above is up. */
export function tunneledTarget(target: ConnectionTarget, tunnel: TunnelTarget): ConnectionTarget {
  return { ...target, host: LOOPBACK, port: tunnel.localPort }
}
