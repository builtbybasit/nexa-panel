import type { FeatureModule } from '../types'

export const schedulesModule: FeatureModule = {
  id: 'schedules',
  name: 'Scheduled tasks',
  description: 'Plan, apply, and monitor per-site cron tasks with reviewed host artifacts.',
  navigation: { label: 'Scheduled tasks', to: '/schedules', icon: 'clock', group: 'Web hosting', order: 15 },
  routes: [
    {
      path: '/schedules',
      name: 'schedules',
      component: () => import('./views/SchedulesView.vue'),
      meta: { moduleId: 'schedules' },
    },
  ],
}
