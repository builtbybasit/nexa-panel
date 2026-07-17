<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { featureModules } from '@/modules/registry'
import { getSystemOverview } from '@/modules/system/api'
import { formatTime } from '@/shared/formatters'
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

// Same key as the top bar memory gauge so both share one 15s poll.
const overviewQuery = useQuery({
  queryKey: ['system', 'overview'],
  queryFn: getSystemOverview,
  refetchInterval: 15_000,
  retry: 1,
})

const node = computed(() => {
  if (overviewQuery.isError.value) {
    const lastChecked = overviewQuery.dataUpdatedAt.value
    return {
      dot: 'bg-rose-400',
      ping: false,
      label: 'Node unreachable',
      detail: 'The control plane is not responding',
      title: lastChecked ? `Last successful check: ${formatTime(new Date(lastChecked).toISOString())}` : 'No successful check yet',
    }
  }
  const overview = overviewQuery.data.value
  if (!overview) {
    return { dot: 'bg-ink-muted', ping: false, label: 'Local node', detail: 'Checking node health…', title: undefined }
  }
  if (overview.warnings.length > 0) {
    return {
      dot: 'bg-amber-400',
      ping: true,
      label: 'Local node',
      detail: `${overview.warnings.length} ${overview.warnings.length === 1 ? 'warning' : 'warnings'} — see System`,
      title: overview.warnings.join('\n'),
    }
  }
  return { dot: 'bg-emerald-400', ping: true, label: 'Local node', detail: 'Single-server control plane', title: undefined }
})
</script>

<template>
  <div class="flex h-full flex-col overflow-y-auto px-4 py-5">
    <RouterLink to="/" aria-label="Nexa Panel overview" class="mb-6 block rounded-xl px-2 py-1" @click="emit('navigate')">
      <BrandMark />
    </RouterLink>

    <nav class="flex-1 space-y-6" aria-label="Primary navigation">
      <div v-for="group in groups" :key="group.label">
        <!-- 'General' holds the ungrouped Overview entry above the first header. -->
        <p v-if="group.label !== 'General'" class="mb-1.5 px-3 text-[10px] font-bold tracking-[0.14em] text-ink-muted uppercase">
          {{ group.label }}
        </p>
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

    <div class="mt-6 flex items-center gap-2.5 border-t border-outline px-3 pt-4 text-xs" :title="node.title" :class="node.title ? 'cursor-help' : ''">
      <span class="relative flex size-2" aria-hidden="true">
        <span v-if="node.ping" class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" :class="node.dot" />
        <span class="relative inline-flex size-2 rounded-full" :class="node.dot" />
      </span>
      <span>
        <strong class="block font-semibold text-ink">{{ node.label }}</strong>
        <small class="block text-ink-muted">{{ node.detail }}</small>
      </span>
    </div>
  </div>
</template>
