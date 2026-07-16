import type { FeatureModule } from '../types'

export const adminToolsModule: FeatureModule = {
  id: 'admin-tools',
  name: 'Admin Tools',
  description: 'Podman-isolated phpMyAdmin and pgAdmin services.',
  navigation: { label: 'Admin tools', to: '/admin-tools', icon: 'wrench', group: 'Databases', order: 32 },
  routes: [
    {
      path: '/admin-tools',
      name: 'admin-tools',
      component: () => import('./views/AdminToolsView.vue'),
      meta: { moduleId: 'admin-tools' },
    },
  ],
}
