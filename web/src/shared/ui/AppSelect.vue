<script setup lang="ts">
const model = defineModel<string | number>()

defineOptions({ inheritAttrs: false })
</script>

<template>
  <div class="relative">
    <select
      v-bind="$attrs"
      v-model="model"
      class="h-10 w-full appearance-none rounded-lg border border-outline-strong bg-canvas/60 px-3 pr-9 text-sm text-ink transition-colors focus:border-accent-500 focus:outline-none disabled:opacity-50 [&>option]:bg-raised"
    >
      <slot />
    </select>
    <svg
      class="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-ink-muted"
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <polyline points="6 9 12 15 18 9" />
    </svg>
  </div>
</template>
