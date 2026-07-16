import type { FeatureModule } from '../types'

export const identityModule: FeatureModule = {
  id: 'identity',
  name: 'Users',
  description: 'Panel accounts, roles, MFA state, and per-site developer access.',
  navigation: { label: 'Users', to: '/users', icon: 'users', group: 'Operations', order: 44, roles: ['admin'] },
  routes: [
    {
      path: '/users',
      name: 'users',
      component: () => import('./views/UsersView.vue'),
      meta: { moduleId: 'identity' },
    },
  ],
}
