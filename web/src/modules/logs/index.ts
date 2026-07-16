import type { FeatureModule } from '../types'

export const logsModule: FeatureModule = {
  id: 'logs',
  name: 'Logs',
  description: 'Inspect, filter, and live-tail the log files under each managed site root.',
  navigation: { label: 'Logs', to: '/logs', icon: 'file-text', group: 'Web hosting', order: 24 },
  routes: [
    {
      path: '/logs',
      name: 'logs',
      component: () => import('./views/LogsView.vue'),
      meta: { moduleId: 'logs' },
    },
  ],
}
