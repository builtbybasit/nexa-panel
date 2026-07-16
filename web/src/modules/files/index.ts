import type { FeatureModule } from '../types'

export const filesModule: FeatureModule = {
  id: 'files',
  name: 'Files',
  description: 'Browse, edit, upload, and organize files under each managed site root.',
  navigation: { label: 'Files', to: '/files', icon: 'folder', group: 'Web hosting', order: 23 },
  routes: [
    {
      path: '/files',
      name: 'files',
      component: () => import('./views/FilesView.vue'),
      meta: { moduleId: 'files' },
    },
  ],
}
