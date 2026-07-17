<script setup lang="ts">
import { useQuery } from '@tanstack/vue-query'
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { getSystemOverview } from '@/modules/system/api'
import {
  AppIcon,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  ProgressBar,
} from '@/shared/ui'

import JobsIndicator from './JobsIndicator.vue'

defineProps<{ sidebarOpen: boolean }>()
const emit = defineEmits<{ toggleSidebar: []; openPalette: [] }>()

const identity = useIdentityStore()
const initials = computed(() => identity.user?.username.slice(0, 2).toUpperCase() ?? 'NA')

const menuButton = ref<HTMLButtonElement>()
defineExpose({ focusMenuButton: () => menuButton.value?.focus() })

const shortcutHint = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘K' : 'Ctrl K'
const environment = import.meta.env.MODE === 'production' ? '' : import.meta.env.MODE

// Same key as the sidebar node dot so both share one 15s poll.
const overviewQuery = useQuery({
  queryKey: ['system', 'overview'],
  queryFn: getSystemOverview,
  refetchInterval: 15_000,
  retry: 1,
})

const memory = computed(() => {
  const snapshot = overviewQuery.data.value?.memory
  return snapshot?.supported ? snapshot : undefined
})
const memoryTone = computed<'accent' | 'warning' | 'danger'>(() => {
  const used = memory.value?.usedPercent ?? 0
  if (used >= 90) return 'danger'
  if (used >= 75) return 'warning'
  return 'accent'
})

function signOut() {
  void identity.logout()
}
</script>

<template>
  <header class="sticky top-0 z-30 flex items-center gap-3 border-b border-outline bg-canvas/85 px-4 py-3 backdrop-blur-xl sm:px-8">
    <button
      ref="menuButton"
      type="button"
      class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline text-ink-secondary hover:text-ink lg:hidden"
      aria-label="Toggle navigation"
      :aria-expanded="sidebarOpen"
      aria-controls="app-sidebar"
      @click="emit('toggleSidebar')"
    >
      <AppIcon name="menu" :size="17" />
    </button>

    <button
      type="button"
      class="flex h-9 shrink-0 items-center gap-2 rounded-lg border border-outline px-2.5 text-[13px] text-ink-muted transition-colors hover:border-outline-strong hover:text-ink-secondary sm:min-w-52"
      aria-label="Search"
      @click="emit('openPalette')"
    >
      <AppIcon name="search" :size="15" />
      <span class="hidden sm:inline">Search</span>
      <kbd class="ml-auto hidden rounded border border-outline px-1.5 py-0.5 text-[10px] font-semibold lg:inline" aria-hidden="true">
        {{ shortcutHint }}
      </kbd>
    </button>

    <div class="flex-1" />

    <div class="flex shrink-0 items-center gap-3">
      <RouterLink
        v-if="memory"
        to="/system"
        class="hidden items-center gap-2 rounded-lg border border-outline px-2.5 py-2 transition-colors hover:border-outline-strong md:flex"
        :title="`Memory: ${Math.round(memory.usedPercent)}% used — open System`"
      >
        <span class="text-[11px] font-semibold text-ink-muted">Mem</span>
        <span class="w-16"><ProgressBar :value="memory.usedPercent" :tone="memoryTone" /></span>
        <span class="text-[11px] text-ink-secondary tabular-nums">{{ Math.round(memory.usedPercent) }}%</span>
      </RouterLink>

      <JobsIndicator />

      <span
        v-if="environment"
        class="hidden rounded-full border border-outline-strong px-2.5 py-1 text-[11px] font-semibold text-ink-secondary capitalize sm:inline"
      >
        {{ environment }}
      </span>

      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <button
            type="button"
            class="grid size-9 place-items-center rounded-full bg-gradient-to-br from-accent-300 to-accent-600 text-xs font-bold text-accent-950"
            aria-label="Account menu"
          >
            {{ initials }}
          </button>
        </DropdownMenuTrigger>

        <DropdownMenuContent align="end" class="w-56">
          <div class="flex items-center gap-2.5 px-3 py-2">
            <AppIcon name="user" :size="14" class="shrink-0 text-ink-muted" />
            <span class="min-w-0">
              <strong class="block truncate text-[13px] font-semibold text-ink">{{ identity.user?.username }}</strong>
              <small class="block text-[11px] text-ink-muted capitalize">{{ identity.user?.role }}</small>
            </span>
          </div>
          <DropdownMenuSeparator />
          <DropdownMenuItem v-if="identity.user?.role === 'admin'" as-child>
            <RouterLink to="/audit">
              <AppIcon name="shield" :size="14" class="text-ink-muted" />
              Audit log
            </RouterLink>
          </DropdownMenuItem>
          <DropdownMenuItem
            class="text-rose-300 data-[highlighted]:bg-rose-500/10 data-[highlighted]:text-rose-300"
            @select="signOut"
          >
            <AppIcon name="log-out" :size="14" class="text-ink-muted" />
            Sign out
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  </header>
</template>
