<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { featureModules } from '@/modules/registry'
import { AppIcon } from '@/shared/ui'

import BrandMark from './BrandMark.vue'

const emit = defineEmits<{ navigate: [] }>()

const identity = useIdentityStore()

interface NavigationGroup {
  label: string
  items: { id: string; label: string; to: string; icon: string }[]
}

const groups = computed<NavigationGroup[]>(() => {
  const role = identity.user?.role
  const grouped: NavigationGroup[] = []
  for (const feature of featureModules) {
    if (!feature.navigation) continue
    const { group, label, to, icon, roles } = feature.navigation
    // Cosmetic gating only; the server rejects unauthorized requests regardless.
    if (roles && (!role || !roles.includes(role))) continue
    const existing = grouped.find((entry) => entry.label === group)
    const item = { id: feature.id, label, to, icon }
    if (existing) existing.items.push(item)
    else grouped.push({ label: group, items: [item] })
  }
  return grouped
})
</script>

<template>
  <div class="flex h-full flex-col overflow-y-auto px-4 py-5">
    <RouterLink to="/" aria-label="Nexa Panel overview" class="mb-6 block rounded-xl px-2 py-1" @click="emit('navigate')">
      <BrandMark />
    </RouterLink>

    <nav class="flex-1 space-y-6" aria-label="Primary navigation">
      <div v-for="group in groups" :key="group.label">
        <p class="mb-1.5 px-3 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase">{{ group.label }}</p>
        <RouterLink
          v-for="item in group.items"
          :key="item.id"
          :to="item.to"
          class="group mb-0.5 flex min-h-10 items-center gap-3 rounded-lg px-3 text-[13.5px] font-medium text-ink-secondary transition-colors hover:bg-white/[0.04] hover:text-ink [&.router-link-exact-active]:bg-accent-400/[0.08] [&.router-link-exact-active]:text-ink [&.router-link-exact-active]:shadow-[inset_2px_0_0] [&.router-link-exact-active]:shadow-accent-400"
          @click="emit('navigate')"
        >
          <AppIcon
            :name="item.icon"
            :size="16"
            class="shrink-0 text-ink-muted transition-colors group-hover:text-ink-secondary group-[.router-link-exact-active]:text-accent-300"
          />
          {{ item.label }}
        </RouterLink>
      </div>
    </nav>

    <div class="mt-6 flex items-center gap-2.5 border-t border-outline px-3 pt-4 text-xs">
      <span class="relative flex size-2" aria-hidden="true">
        <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
        <span class="relative inline-flex size-2 rounded-full bg-emerald-400" />
      </span>
      <span>
        <strong class="block font-semibold text-ink">Local node</strong>
        <small class="block text-ink-muted">Single-server control plane</small>
      </span>
    </div>
  </div>
</template>
