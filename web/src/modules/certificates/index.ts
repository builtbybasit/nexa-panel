import type { FeatureModule } from '../types'

export const certificatesModule: FeatureModule = {
  id: 'certificates',
  name: 'TLS Certificates',
  description: "Let's Encrypt issuance, renewal, revocation, and expiry.",
  navigation: { label: 'TLS certificates', to: '/certificates', icon: 'shield', group: 'Web hosting', order: 12, permission: 'certificates.read' },
  routes: [
    {
      path: '/certificates',
      name: 'certificates',
      component: () => import('./views/CertificatesView.vue'),
      meta: { moduleId: 'certificates' },
    },
  ],
}
