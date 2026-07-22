import { describe, expect, it } from 'vitest'

import {
  cliCommand,
  connectionUri,
  jdbcUrl,
  sshTunnelCommand,
  tunneledTarget,
  type ConnectionTarget,
} from './connectionStrings'

const postgres: ConnectionTarget = {
  engine: 'postgres',
  host: 'node.example.com',
  port: 5432,
  database: 'app_db',
  username: 'app_owner',
}

const mysql: ConnectionTarget = { ...postgres, engine: 'mysql', port: 3306 }

describe('connection strings', () => {
  it('builds a password-free URI per wire protocol', () => {
    expect(connectionUri(postgres)).toBe('postgresql://app_owner@node.example.com:5432/app_db')
    expect(connectionUri(mysql)).toBe('mysql://app_owner@node.example.com:3306/app_db')
    expect(connectionUri({ ...mysql, engine: 'mariadb' })).toBe('mysql://app_owner@node.example.com:3306/app_db')
  })

  it('percent-encodes names that would break the URI', () => {
    expect(connectionUri({ ...postgres, username: 'a@b', database: 'my db' })).toBe(
      'postgresql://a%40b@node.example.com:5432/my%20db',
    )
  })

  it('names the JDBC driver MariaDB clients expect', () => {
    expect(jdbcUrl(postgres)).toBe('jdbc:postgresql://node.example.com:5432/app_db?user=app_owner')
    expect(jdbcUrl({ ...mysql, engine: 'mariadb' })).toBe('jdbc:mariadb://node.example.com:3306/app_db?user=app_owner')
  })

  it('uses each engine own CLI', () => {
    expect(cliCommand(postgres)).toContain('psql --host=node.example.com --port=5432')
    expect(cliCommand(mysql)).toContain('mysql --host=node.example.com --port=3306')
  })

  it('forwards the local port to loopback on the node', () => {
    const tunnel = { sshUser: 'root', sshHost: 'node.example.com', localPort: 15432 }
    expect(sshTunnelCommand(postgres, tunnel)).toBe('ssh -N -L 15432:127.0.0.1:5432 root@node.example.com')
    expect(connectionUri(tunneledTarget(postgres, tunnel))).toBe('postgresql://app_owner@127.0.0.1:15432/app_db')
  })
})
