import type { FeatureModule } from '../types'

export const nodeOperationsModule: FeatureModule = {
  id: 'nodeoperations',
  name: 'Node Operations',
  description: 'Reviewed and reversible privileged node operations.',
  navigation: { label: 'Node operations', to: '/operations', icon: 'zap', group: 'Server', order: 31, permission: 'operations.plan' },
  routes: [
    {
      path: '/operations',
      component: () => import('./views/OperationsView.vue'),
      meta: { moduleId: 'nodeoperations' },
    },
  ],
}
