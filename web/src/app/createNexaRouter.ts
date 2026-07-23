import { createRouter, type RouterHistory, type RouteRecordRaw } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { featureModules } from '@/modules/registry'
import { requestDiscardUnsavedChanges } from '@/shared/navigation/unsavedChanges'

const routes: RouteRecordRaw[] = featureModules.flatMap((feature) =>
  feature.routes.map((route) => ({
    ...route,
    meta: {
      ...route.meta,
      permission: route.meta?.permission ?? feature.navigation?.permission,
    },
  })),
)

export function createNexaRouter(history: RouterHistory) {
  const router = createRouter({
    history,
    routes: [
      ...routes,
      {
        path: '/forbidden',
        name: 'forbidden',
        component: () => import('./views/ForbiddenView.vue'),
      },
      {
        path: '/:pathMatch(.*)*',
        name: 'not-found',
        component: () => import('./views/NotFoundView.vue'),
      },
    ],
  })

  router.beforeEach(async (to, from) => {
    const identity = useIdentityStore()
    if (!identity.initialized) await identity.initialize()
    if (identity.authenticated && to.meta.permission && !identity.can(to.meta.permission)) {
      return { name: 'forbidden', query: { from: to.fullPath }, replace: true }
    }
    const dropsMountedDraft = from.name !== undefined && (to.path !== from.path || to.query.site !== from.query.site)
    if (dropsMountedDraft && !requestDiscardUnsavedChanges()) return false
    return true
  })

  router.afterEach((to) => {
    const feature = featureModules.find((candidate) => candidate.id === to.meta.moduleId)
    document.title = feature ? `${feature.name} · Nexa Panel` : 'Nexa Panel'
  })

  return router
}
