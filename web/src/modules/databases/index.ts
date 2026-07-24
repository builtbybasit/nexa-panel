import type { FeatureModule } from '../types'

export const databasesModule: FeatureModule = {
  id: 'databases',
  name: 'Databases',
  description: 'MySQL, MariaDB, and PostgreSQL servers, databases, users, grants, backup, and restore.',
  navigation: { label: 'Databases', to: '/databases', icon: 'database', group: 'Databases', order: 20, permission: 'databases.read' },
  routes: [
    {
      path: '/databases',
      name: 'databases',
      component: () => import('./views/DatabasesView.vue'),
      meta: { moduleId: 'databases' },
    },
    {
      path: '/databases/new',
      name: 'database-create',
      component: () => import('./views/DatabaseCreateView.vue'),
      meta: { moduleId: 'databases' },
    },
    {
      path: '/databases/users/new',
      name: 'database-user-create',
      component: () => import('./views/UserCreateView.vue'),
      meta: { moduleId: 'databases' },
    },
    {
      path: '/databases/users/:userId/password',
      name: 'database-user-password',
      component: () => import('./views/UserPasswordView.vue'),
      meta: { moduleId: 'databases' },
    },
    {
      path: '/databases/servers/new',
      name: 'database-server-create',
      component: () => import('./views/ServerCreateView.vue'),
      meta: { moduleId: 'databases' },
    },
    {
      path: '/databases/:databaseId',
      name: 'database-detail',
      component: () => import('./views/DatabaseDetailView.vue'),
      meta: { moduleId: 'databases' },
    },
  ],
}
