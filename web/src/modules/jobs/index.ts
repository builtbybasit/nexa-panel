import type { FeatureModule } from '../types'

export const jobsModule: FeatureModule = {
  id: 'jobs',
  name: 'Jobs',
  description: 'Durable background operations and progress history.',
  navigation: { label: 'Jobs', to: '/jobs', icon: 'history', group: 'Operations', order: 40 },
  routes: [
    {
      path: '/jobs',
      component: () => import('./views/JobsView.vue'),
      meta: { moduleId: 'jobs' },
    },
  ],
}
