<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { useIdentityStore } from '@/modules/identity/store'
import { featureModules } from '@/modules/registry'
import { AppIcon } from '@/shared/ui'

const emit = defineEmits<{ toggleSidebar: [] }>()

const route = useRoute()
const identity = useIdentityStore()

const activeModule = computed(() => featureModules.find((feature) => feature.id === route.meta.moduleId))
const initials = computed(() => identity.user?.username.slice(0, 2).toUpperCase() ?? 'NA')
</script>

<template>
  <header class="sticky top-0 z-30 flex items-center gap-3 border-b border-outline bg-canvas/85 px-4 py-3 backdrop-blur-xl sm:px-8">
    <button
      type="button"
      class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline text-ink-secondary hover:text-ink lg:hidden"
      aria-label="Toggle navigation"
      @click="emit('toggleSidebar')"
    >
      <AppIcon name="menu" :size="17" />
    </button>

    <div class="min-w-0 flex-1">
      <h1 class="truncate text-[15px] font-semibold tracking-tight text-ink">
        {{ activeModule?.name ?? 'Nexa Panel' }}
      </h1>
      <p class="hidden truncate text-xs text-ink-muted sm:block">
        {{ activeModule?.description ?? 'Modern server management platform' }}
      </p>
    </div>

    <div class="flex shrink-0 items-center gap-3">
      <span class="hidden rounded-full border border-outline-strong px-2.5 py-1 text-[11px] font-semibold text-ink-secondary sm:inline">
        Development
      </span>
      <div class="flex items-center gap-2.5">
        <span class="grid size-9 place-items-center rounded-full bg-gradient-to-br from-accent-300 to-accent-600 text-xs font-bold text-accent-950">
          {{ initials }}
        </span>
        <span class="hidden min-w-0 sm:block">
          <strong class="block max-w-32 truncate text-[13px] font-semibold text-ink">{{ identity.user?.username }}</strong>
          <small class="block text-[11px] text-ink-muted capitalize">{{ identity.user?.role }}</small>
        </span>
      </div>
      <button
        type="button"
        class="grid size-9 place-items-center rounded-lg border border-outline text-ink-secondary transition-colors hover:border-rose-400/40 hover:text-rose-300"
        aria-label="Sign out"
        title="Sign out"
        @click="identity.logout()"
      >
        <AppIcon name="log-out" :size="16" />
      </button>
    </div>
  </header>
</template>
