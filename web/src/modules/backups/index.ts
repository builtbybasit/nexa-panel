import type { FeatureModule } from '../types'

export const backupsModule: FeatureModule = {
  id: 'backups',
  name: 'Backups',
  description: 'Back up sites and databases to local or remote storage',
  navigation: { label: 'Backups', to: '/backups', icon: 'archive', group: 'Web hosting', order: 16, permission: 'backups.read' },
  routes: [
    {
      path: '/backups',
      name: 'backups',
      component: () => import('./views/BackupPlansView.vue'),
      meta: { moduleId: 'backups' },
    },
    {
      path: '/backups/accounts',
      name: 'backup-accounts',
      component: () => import('./views/BackupAccountsView.vue'),
      meta: { moduleId: 'backups' },
    },
    {
      path: '/backups/plans/:planId/copies',
      name: 'backup-copies',
      component: () => import('./views/BackupCopiesView.vue'),
      meta: { moduleId: 'backups' },
    },
    {
      path: '/backups/system',
      name: 'backup-system',
      component: () => import('./views/SystemBackupsView.vue'),
      meta: { moduleId: 'backups' },
    },
  ],
}
