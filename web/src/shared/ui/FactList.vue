<script lang="ts">
export interface Fact {
  label: string
  value: string
  mono?: boolean
}
</script>

<script setup lang="ts">
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
        {{ fact.value }}
      </dd>
    </div>
  </dl>
</template>
