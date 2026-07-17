import type { FeatureModule } from '../types'

export const adminToolsModule: FeatureModule = {
  id: 'admin-tools',
  name: 'Admin Tools',
  description: 'phpMyAdmin & pgAdmin',
  navigation: { label: 'DB web clients', to: '/admin-tools', icon: 'terminal', group: 'Databases', order: 22 },
  routes: [
    {
      path: '/admin-tools',
      name: 'admin-tools',
      component: () => import('./views/AdminToolsView.vue'),
      meta: { moduleId: 'admin-tools' },
    },
  ],
}
