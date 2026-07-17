import type { FeatureModule } from '../types'

export const identityModule: FeatureModule = {
  id: 'identity',
  name: 'Users',
  description: 'Panel accounts, roles, MFA state, and per-site developer access.',
  navigation: { label: 'Users', to: '/users', icon: 'users', group: 'Administration', order: 40, roles: ['admin'] },
  routes: [
    {
      path: '/users',
      name: 'users',
      component: () => import('./views/UsersView.vue'),
      meta: { moduleId: 'identity' },
    },
    {
      // Per-account security (MFA); available to every signed-in role, so it is
      // reached from the top-bar account menu rather than the admin sidebar.
      path: '/account/security',
      name: 'account-security',
      component: () => import('./views/AccountSecurityView.vue'),
      meta: { moduleId: 'identity' },
    },
  ],
}
