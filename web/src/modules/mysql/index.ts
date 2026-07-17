import type { FeatureModule } from '../types'

export const mysqlModule: FeatureModule = {
  id: 'mysql-databases',
  name: 'MySQL & MariaDB',
  description: 'Native MySQL-family databases, accounts, scoped grants, backup, and restore.',
  navigation: { label: 'MySQL / MariaDB', to: '/mysql', icon: 'server', group: 'Databases', order: 21 },
  routes: [
    {
      path: '/mysql',
      name: 'mysql',
      component: () => import('./views/MySQLView.vue'),
      meta: { moduleId: 'mysql-databases' },
    },
  ],
}
