import type { FeatureModule } from '../types'

export const mysqlModule: FeatureModule = {
  id: 'mysql-databases',
  name: 'MySQL & MariaDB',
  description: 'Native MySQL-family databases, accounts, scoped grants, backup, and restore.',
  navigation: { label: 'MySQL / MariaDB', to: '/mysql', icon: 'server', group: 'Databases', order: 21, permission: 'databases.read' },
  routes: [
    {
      path: '/mysql',
      name: 'mysql',
      component: () => import('./views/MySQLView.vue'),
      meta: { moduleId: 'mysql-databases' },
    },
    {
      path: '/mysql/:databaseId',
      name: 'mysql-database-detail',
      component: () => import('./views/MySQLDatabaseDetailView.vue'),
      meta: { moduleId: 'mysql-databases' },
    },
  ],
}
