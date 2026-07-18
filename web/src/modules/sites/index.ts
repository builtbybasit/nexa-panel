import type { FeatureModule } from '../types'

export const sitesModule: FeatureModule = {
  id: 'sites',
  name: 'Sites',
  description: 'Managed PHP sites, routing identity, and runtime configuration.',
  navigation: { label: 'Sites', to: '/sites', icon: 'layers', group: 'Web hosting', order: 10 },
  routes: [
    {
      path: '/sites',
      name: 'sites',
      component: () => import('./views/SitesView.vue'),
      meta: { moduleId: 'sites' },
    },
    {
      path: '/sites/new',
      name: 'site-create',
      component: () => import('./views/SiteCreateView.vue'),
      meta: { moduleId: 'sites' },
    },
    {
      path: '/sites/:siteId',
      name: 'site-detail',
      component: () => import('./views/SiteDetailView.vue'),
      meta: { moduleId: 'sites' },
    },
  ],
}
