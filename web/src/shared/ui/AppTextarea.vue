<script setup lang="ts">
const model = defineModel<string>()

defineProps<{ invalid?: boolean }>()

defineOptions({ inheritAttrs: false })
</script>

<template>
  <textarea
    v-bind="$attrs"
    v-model="model"
    :aria-invalid="invalid || undefined"
    class="w-full rounded-lg border bg-canvas/60 px-3 py-2 font-mono text-[13px] text-ink transition-colors placeholder:text-ink-muted focus:outline-none disabled:opacity-50"
    :class="invalid ? 'border-rose-500/60 focus:border-rose-400' : 'border-outline-strong focus:border-accent-500'"
  />
</template>
