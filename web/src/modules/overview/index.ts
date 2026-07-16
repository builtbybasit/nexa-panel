import type { FeatureModule } from '../types'

export const overviewModule: FeatureModule = {
  id: 'overview',
  name: 'Overview',
  description: 'Operational summary for this Nexa node.',
  navigation: { label: 'Overview', to: '/', icon: 'grid', group: 'General', order: 10 },
  routes: [
    {
      path: '/',
      name: 'overview',
      component: () => import('./views/OverviewView.vue'),
      meta: { moduleId: 'overview' },
    },
  ],
}
