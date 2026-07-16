<script setup lang="ts">
const model = defineModel<string>()

defineOptions({ inheritAttrs: false })
</script>

<template>
  <textarea
    v-bind="$attrs"
    v-model="model"
    class="w-full rounded-lg border border-outline-strong bg-canvas/60 px-3 py-2 font-mono text-[13px] text-ink transition-colors placeholder:text-ink-muted focus:border-accent-500 focus:outline-none disabled:opacity-50"
  />
</template>
