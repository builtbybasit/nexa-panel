import type { FeatureModule } from '../types'

export const databasesModule: FeatureModule = {
  id: 'databases',
  name: 'PostgreSQL',
  description: 'PostgreSQL instances, databases, roles, grants, backup, and restore.',
  navigation: { label: 'PostgreSQL', to: '/databases', icon: 'database', group: 'Databases', order: 30 },
  routes: [
    {
      path: '/databases',
      name: 'databases',
      component: () => import('./views/DatabasesView.vue'),
      meta: { moduleId: 'databases' },
    },
  ],
}
