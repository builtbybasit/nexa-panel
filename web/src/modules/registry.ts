import { auditModule } from './audit'
import { applicationsModule } from './applications'
import { backupsModule } from './backups'
import { certificatesModule } from './certificates'
import { databasesModule } from './databases'
import { domainsModule } from './domains'
import { filesModule } from './files'
import { firewallModule } from './firewall'
import { identityModule } from './identity'
import { jobsModule } from './jobs'
import { logsModule } from './logs'
import { mysqlModule } from './mysql'
import { overviewModule } from './overview'
import { phpModule } from './php'
import { schedulesModule } from './schedules'
import { servicesModule } from './services'
import { sftpModule } from './sftp'
import { sitesModule } from './sites'
import { systemModule } from './system'
import type { FeatureModule } from './types'

export const featureModules: FeatureModule[] = [overviewModule, sitesModule, domainsModule, certificatesModule, filesModule, logsModule, schedulesModule, backupsModule, databasesModule, mysqlModule, applicationsModule, phpModule, servicesModule, sftpModule, firewallModule, jobsModule, identityModule, auditModule, systemModule].sort(
  (left, right) => (left.navigation?.order ?? 999) - (right.navigation?.order ?? 999),
)
