import type { FeatureModule } from '../types'

export const auditModule: FeatureModule = {
  id: 'audit',
  name: 'Audit Log',
  description: 'Append-only identity and operation history.',
  navigation: { label: 'Audit log', to: '/audit', icon: 'shield', group: 'Administration', order: 41, permission: 'audit.read' },
  routes: [
    {
      path: '/audit',
      component: () => import('./views/AuditView.vue'),
      meta: { moduleId: 'audit' },
    },
  ],
}
