import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { featureModules } from '@/modules/registry'

const routes: RouteRecordRaw[] = featureModules.flatMap((feature) => feature.routes)

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    ...routes,
    {
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
})
