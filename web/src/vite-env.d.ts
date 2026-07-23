/// <reference types="vite/client" />

import type { Permission } from './modules/identity/permissions'

declare module 'vue-router' {
  interface RouteMeta {
    moduleId?: string
    permission?: Permission
  }
}

export {}
