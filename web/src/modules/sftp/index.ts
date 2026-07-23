import type { FeatureModule } from '../types'

// SFTP is a per-site capability reached from the site detail "Site managing"
// grid, so it owns a route but no top-level navigation entry.
export const sftpModule: FeatureModule = {
  id: 'sftp',
  name: 'SFTP',
  description: 'Optional SSH-jailed SFTP access per site',
  routes: [
    {
      path: '/sftp',
      name: 'sftp',
      component: () => import('./views/SftpAccessView.vue'),
      meta: { moduleId: 'sftp', permission: 'deploy.read' },
    },
  ],
}
