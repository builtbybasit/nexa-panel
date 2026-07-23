import type { FeatureModule } from '../types'

export const sitesModule: FeatureModule = {
  id: 'sites',
  name: 'Sites',
  description: 'Managed PHP sites, routing identity, and runtime configuration.',
  navigation: { label: 'Sites', to: '/sites', icon: 'layers', group: 'Web hosting', order: 10, permission: 'sites.read' },
  routes: [
    {
      path: '/sites',
      name: 'sites',
      component: () => import('./views/SitesView.vue'),
      meta: { moduleId: 'sites', permission: 'sites.read' },
    },
    {
      path: '/sites/new',
      name: 'site-create',
      component: () => import('./views/SiteCreateView.vue'),
      meta: { moduleId: 'sites', permission: 'sites.write' },
    },
    {
      path: '/sites/:siteId',
      name: 'site-detail',
      component: () => import('./views/SiteDetailView.vue'),
      meta: { moduleId: 'sites', permission: 'sites.read' },
    },
    {
      path: '/sites/:siteId/settings',
      name: 'site-settings',
      component: () => import('./views/SiteSettingsView.vue'),
      meta: { moduleId: 'sites', permission: 'sites.write' },
    },
    // Deployment is a per-site page, so it lives on the site's route tree and
    // earns no sidebar entry of its own.
    {
      path: '/sites/:siteId/deployment',
      name: 'site-deployment',
      component: () => import('@/modules/deploy/views/SiteDeploymentView.vue'),
      meta: { moduleId: 'sites', permission: 'deploy.read' },
    },
  ],
}
