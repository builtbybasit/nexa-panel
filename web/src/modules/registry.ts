import { auditModule } from './audit'
import { adminToolsModule } from './admintools'
import { certificatesModule } from './certificates'
import { databasesModule } from './databases'
import { domainsModule } from './domains'
import { filesModule } from './files'
import { identityModule } from './identity'
import { jobsModule } from './jobs'
import { logsModule } from './logs'
import { mysqlModule } from './mysql'
import { nodeOperationsModule } from './nodeoperations'
import { overviewModule } from './overview'
import { schedulesModule } from './schedules'
import { sitesModule } from './sites'
import { systemModule } from './system'
import type { FeatureModule } from './types'

export const featureModules: FeatureModule[] = [overviewModule, sitesModule, domainsModule, certificatesModule, databasesModule, mysqlModule, filesModule, logsModule, schedulesModule, adminToolsModule, systemModule, nodeOperationsModule, jobsModule, auditModule, identityModule].sort(
  (left, right) => (left.navigation?.order ?? 999) - (right.navigation?.order ?? 999),
)
