<script lang="ts">
export interface Fact {
  label: string
  value: string
  mono?: boolean
  /** When set, the value renders as a link to this route. */
  to?: string
}
</script>

<script setup lang="ts">
import { RouterLink } from 'vue-router'

defineProps<{ facts: Fact[] }>()
</script>

<template>
  <dl class="grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-4">
    <div v-for="fact in facts" :key="fact.label" class="min-w-0">
      <dt class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">{{ fact.label }}</dt>
      <dd
        class="mt-1 text-[13px] break-all text-ink"
        :class="fact.mono ? 'font-mono text-xs text-accent-200' : ''"
      >
        <RouterLink
          v-if="fact.to"
          :to="fact.to"
          class="text-accent-300 underline-offset-2 transition-colors hover:text-accent-200 hover:underline"
        >
          {{ fact.value }}
        </RouterLink>
        <template v-else>{{ fact.value }}</template>
      </dd>
    </div>
  </dl>
</template>
