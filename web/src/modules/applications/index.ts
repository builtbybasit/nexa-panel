import type { FeatureModule } from '../types'

export const applicationsModule: FeatureModule = {
  id: 'applications',
  name: 'Applications',
  description: 'Install PHP, PostgreSQL, Node.js, Composer, and database web clients',
  navigation: { label: 'Applications', to: '/applications', icon: 'download', group: 'Server', order: 33, permission: 'applications.read' },
  routes: [
    {
      path: '/applications',
      name: 'applications',
      component: () => import('./views/ApplicationsView.vue'),
      meta: { moduleId: 'applications' },
    },
  ],
}
