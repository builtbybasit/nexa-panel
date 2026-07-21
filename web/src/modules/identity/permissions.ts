export type SessionRole = 'admin' | 'operator' | 'developer' | 'viewer'

export type Permission =
  | 'system.read'
  | 'jobs.read'
  | 'audit.read'
  | 'runtimes.read'
  | 'sites.read'
  | 'sites.write'
  | 'domains.read'
  | 'domains.write'
  | 'certificates.read'
  | 'certificates.write'
  | 'databases.read'
  | 'databases.write'
  | 'operations.apply'
  | 'files.read'
  | 'files.write'
  | 'logs.read'
  | 'schedules.read'
  | 'schedules.write'
  | 'applications.read'
  | 'applications.write'
  | 'backups.read'
  | 'backups.write'
  | 'services.read'
  | 'services.write'
  | 'users.manage'

const readOnlyPermissions = [
  'system.read',
  'jobs.read',
  'runtimes.read',
  'sites.read',
  'domains.read',
  'certificates.read',
  'databases.read',
  'files.read',
  'logs.read',
  'schedules.read',
  'applications.read',
  'backups.read',
  'services.read',
] as const satisfies readonly Permission[]

const developerPermissions = [
  'system.read',
  'jobs.read',
  'runtimes.read',
  'sites.read',
  'files.read',
  'files.write',
  'logs.read',
  'schedules.read',
  'schedules.write',
  'applications.read',
  'backups.read',
] as const satisfies readonly Permission[]

const operatorPermissions = [
  ...readOnlyPermissions,
  'sites.write',
  'domains.write',
  'certificates.write',
  'databases.write',
  'files.write',
  'schedules.write',
  'applications.write',
  'backups.write',
  'services.write',
] as const satisfies readonly Permission[]

const adminPermissions = [...operatorPermissions, 'audit.read', 'operations.apply', 'users.manage'] as const satisfies readonly Permission[]

const permissionsByRole: Readonly<Record<SessionRole, ReadonlySet<Permission>>> = {
  viewer: new Set(readOnlyPermissions),
  developer: new Set(developerPermissions),
  operator: new Set(operatorPermissions),
  admin: new Set(adminPermissions),
}

function isSessionRole(role: string): role is SessionRole {
  return Object.hasOwn(permissionsByRole, role)
}

export function hasPermission(role: string | undefined, permission: Permission): boolean {
  return role !== undefined && isSessionRole(role) && permissionsByRole[role].has(permission)
}
