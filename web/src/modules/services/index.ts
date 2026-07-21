import type { FeatureModule } from '../types'

export const servicesModule: FeatureModule = {
  id: 'services',
  name: 'Services',
  description: 'List systemd services and start, stop, restart, or toggle their boot-time autorun',
  navigation: { label: 'Services', to: '/services', icon: 'server', group: 'Server', order: 30, permission: 'services.read' },
  routes: [
    {
      path: '/services',
      name: 'services',
      component: () => import('./views/ServicesView.vue'),
      meta: { moduleId: 'services' },
    },
  ],
}
