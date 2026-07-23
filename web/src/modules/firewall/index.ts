import type { FeatureModule } from '../types'

export const firewallModule: FeatureModule = {
  id: 'firewall',
  name: 'Firewall',
  description: 'Manage the UFW firewall: open and close ports and enable or disable the firewall',
  navigation: { label: 'Firewall', to: '/firewall', icon: 'shield', group: 'Server', order: 31, permission: 'firewall.read' },
  routes: [
    {
      path: '/firewall',
      name: 'firewall',
      component: () => import('./views/FirewallView.vue'),
      meta: { moduleId: 'firewall' },
    },
  ],
}
