import type { FeatureModule } from '../types'

export const auditModule: FeatureModule = {
  id: 'audit',
  name: 'Audit Log',
  description: 'Append-only identity and operation history.',
  navigation: { label: 'Audit log', to: '/audit', icon: 'file-text', group: 'Operations', order: 43, roles: ['admin'] },
  routes: [
    {
      path: '/audit',
      component: () => import('./views/AuditView.vue'),
      meta: { moduleId: 'audit' },
    },
  ],
}
