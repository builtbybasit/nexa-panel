import type { FeatureModule } from '../types'

export const phpModule: FeatureModule = {
  id: 'php',
  name: 'PHP',
  description: 'Manage PHP extensions and php.ini settings per installed PHP version',
  navigation: { label: 'PHP', to: '/php', icon: 'wrench', group: 'Server', order: 34, permission: 'applications.read' },
  routes: [
    {
      path: '/php',
      name: 'php',
      component: () => import('./views/PhpView.vue'),
      meta: { moduleId: 'php' },
    },
    {
      path: '/php/site',
      name: 'php-site',
      component: () => import('./views/SitePhpSettingsView.vue'),
      meta: { moduleId: 'php' },
    },
  ],
}
