import type { RouteRecordRaw } from 'vue-router'

import type { Permission } from './identity/permissions'

export interface FeatureModule {
  id: string
  name: string
  description: string
  routes: RouteRecordRaw[]
  navigation?: {
    label: string
    to: string
    /** Icon name resolved by the shared AppIcon component. */
    icon: string
    /** Sidebar section heading; items with the same group render together. */
    group: string
    order: number
    /** Required capability for this navigation destination. */
    permission: Permission
  }
}
