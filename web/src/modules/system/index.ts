import type { FeatureModule } from '../types'

export const systemModule: FeatureModule = {
  id: 'system',
  name: 'Updates',
  description: 'Check for and install Nexa Panel updates.',
  navigation: { label: 'Updates', to: '/system/updates', icon: 'refresh-cw', group: 'Administration', order: 42, permission: 'system.read' },
  routes: [
    {
      path: '/system/updates',
      name: 'system-updates',
      component: () => import('./views/SystemView.vue'),
      meta: { moduleId: 'system' },
    },
  ],
}
