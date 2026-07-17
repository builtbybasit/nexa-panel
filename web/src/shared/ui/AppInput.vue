<script setup lang="ts">
const model = defineModel<string | number | undefined>()

defineProps<{ invalid?: boolean }>()

defineOptions({ inheritAttrs: false })
</script>

<template>
  <input
    v-bind="$attrs"
    v-model="model"
    :aria-invalid="invalid || undefined"
    class="h-10 w-full rounded-lg border bg-canvas/60 px-3 text-sm text-ink transition-colors placeholder:text-ink-muted focus:outline-none disabled:opacity-50"
    :class="
      invalid
        ? 'border-rose-500/60 hover:border-rose-500/60 focus:border-rose-400'
        : 'border-outline-strong hover:border-outline-strong focus:border-accent-500'
    "
  />
</template>
