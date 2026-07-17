import type { FeatureModule } from '../types'

export const systemModule: FeatureModule = {
  id: 'system',
  name: 'System',
  description: 'Node capacity and container runtime capabilities.',
  navigation: { label: 'System', to: '/system', icon: 'cpu', group: 'Server', order: 30 },
  routes: [
    {
      path: '/system',
      name: 'system',
      component: () => import('./views/SystemView.vue'),
      meta: { moduleId: 'system' },
    },
  ],
}
