<script setup lang="ts">
const model = defineModel<string | number | undefined>()

defineOptions({ inheritAttrs: false })
</script>

<template>
  <input
    v-bind="$attrs"
    v-model="model"
    class="h-10 w-full rounded-lg border border-outline-strong bg-canvas/60 px-3 text-sm text-ink transition-colors placeholder:text-ink-muted hover:border-outline-strong focus:border-accent-500 focus:outline-none disabled:opacity-50"
  />
</template>
