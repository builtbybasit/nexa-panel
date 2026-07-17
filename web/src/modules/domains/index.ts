import type { FeatureModule } from '../types'

export const domainsModule: FeatureModule = {
  id: 'domains',
  name: 'Domains',
  description: 'Hostnames, DNS preflight, aliases, subdomains, and redirects.',
  navigation: { label: 'Domains', to: '/domains', icon: 'globe', group: 'Web hosting', order: 11 },
  routes: [
    {
      path: '/domains',
      name: 'domains',
      component: () => import('./views/DomainsView.vue'),
      meta: { moduleId: 'domains' },
    },
  ],
}
