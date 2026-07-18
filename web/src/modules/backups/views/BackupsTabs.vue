<script setup lang="ts">
import { useRoute } from 'vue-router'

// FastPanel-style sub-navigation for the Backups area. Plans is the primary
// tab; Accounts holds the storage destinations plans write to.
const route = useRoute()
const tabs = [
  { label: 'Plans', to: '/backups' },
  { label: 'Accounts', to: '/backups/accounts' },
]
</script>

<template>
  <nav class="flex items-center gap-1 border-b border-outline">
    <RouterLink
      v-for="tab in tabs"
      :key="tab.to"
      :to="tab.to"
      class="border-b-2 px-3 py-2 text-sm font-medium transition-colors"
      :class="
        route.path === tab.to
          ? 'border-accent-500 text-ink'
          : 'border-transparent text-ink-muted hover:text-ink'
      "
    >
      {{ tab.label }}
    </RouterLink>
  </nav>
</template>
