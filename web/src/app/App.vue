<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'

import AuthGate from '@/modules/identity/components/AuthGate.vue'
import { useIdentityStore } from '@/modules/identity/store'
import { lockBodyScroll, unlockBodyScroll } from '@/shared/composables/useBodyScrollLock'
import { ToastHost, TooltipProvider } from '@/shared/ui'

import CommandPalette from './components/CommandPalette.vue'
import SidebarNav from './components/SidebarNav.vue'
import TopBar from './components/TopBar.vue'

const route = useRoute()
const identity = useIdentityStore()
const sidebarOpen = ref(false)
const paletteOpen = ref(false)

const sidebar = ref<HTMLElement>()
const topBar = ref<InstanceType<typeof TopBar>>()

onMounted(() => {
  void identity.initialize()
  window.addEventListener('keydown', onGlobalKeydown)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onGlobalKeydown)
  if (sidebarOpen.value) unlockBodyScroll()
})

watch(
  () => route.fullPath,
  () => {
    sidebarOpen.value = false
    paletteOpen.value = false
  },
)

watch(sidebarOpen, async (open, wasOpen) => {
  if (open) {
    lockBodyScroll()
    await nextTick()
    sidebar.value?.focus()
  } else if (wasOpen) {
    unlockBodyScroll()
    topBar.value?.focusMenuButton()
  }
})

function onGlobalKeydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    if (identity.authenticated) paletteOpen.value = !paletteOpen.value
    return
  }
  // The palette owns Escape while it is open.
  if (event.key === 'Escape' && sidebarOpen.value && !paletteOpen.value) {
    sidebarOpen.value = false
  }
}
</script>

<template>
  <TooltipProvider :delay-duration="300">
  <main v-if="!identity.initialized" class="grid min-h-screen place-items-center">
    <div class="flex flex-col items-center gap-4">
      <span class="grid size-14 animate-pulse place-items-center rounded-2xl bg-gradient-to-br from-accent-300 to-accent-600 text-2xl font-black text-accent-950">
        N
      </span>
      <p class="text-sm text-ink-secondary">
        {{ identity.loading ? 'Starting secure control plane…' : 'Loading Nexa Panel…' }}
      </p>
    </div>
  </main>

  <AuthGate v-else-if="!identity.authenticated" />

  <div v-else class="min-h-screen lg:grid lg:grid-cols-[264px_minmax(0,1fr)]">
    <!-- Mobile scrim -->
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-40 bg-canvas/70 backdrop-blur-sm lg:hidden"
      aria-hidden="true"
      @click="sidebarOpen = false"
    />

    <aside
      id="app-sidebar"
      ref="sidebar"
      tabindex="-1"
      aria-label="Sidebar"
      class="fixed inset-y-0 left-0 z-50 w-[264px] -translate-x-full border-r border-outline bg-surface/95 backdrop-blur-xl transition-transform duration-200 outline-none lg:sticky lg:top-0 lg:h-screen lg:translate-x-0 lg:bg-surface/60"
      :class="sidebarOpen ? 'translate-x-0' : ''"
    >
      <SidebarNav @navigate="sidebarOpen = false" />
    </aside>

    <div class="flex min-h-screen min-w-0 flex-col">
      <TopBar
        ref="topBar"
        :sidebar-open="sidebarOpen"
        @toggle-sidebar="sidebarOpen = !sidebarOpen"
        @open-palette="paletteOpen = true"
      />
      <main class="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-8 sm:py-8">
        <RouterView />
      </main>
    </div>

    <CommandPalette :open="paletteOpen" @close="paletteOpen = false" />
  </div>

  <ToastHost />
  </TooltipProvider>
</template>
