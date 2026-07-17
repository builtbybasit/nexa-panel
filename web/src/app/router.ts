import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { featureModules } from '@/modules/registry'

const routes: RouteRecordRaw[] = featureModules.flatMap((feature) => feature.routes)

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    ...routes,
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('./views/NotFoundView.vue'),
    },
  ],
})

router.afterEach((to) => {
  const feature = featureModules.find((candidate) => candidate.id === to.meta.moduleId)
  document.title = feature ? `${feature.name} · Nexa Panel` : 'Nexa Panel'
})
