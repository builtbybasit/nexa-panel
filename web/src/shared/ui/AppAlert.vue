<script setup lang="ts">
import AppIcon from './AppIcon.vue'

withDefaults(defineProps<{ tone?: 'info' | 'success' | 'warning' | 'danger'; title?: string }>(), {
  tone: 'info',
})

const toneClasses = {
  info: 'border-sky-400/25 bg-sky-400/[0.07] text-sky-200',
  success: 'border-emerald-400/25 bg-emerald-400/[0.07] text-emerald-200',
  warning: 'border-amber-400/25 bg-amber-400/[0.07] text-amber-200',
  danger: 'border-rose-400/25 bg-rose-400/[0.07] text-rose-200',
}

const toneIcons = { info: 'info', success: 'check', warning: 'alert-triangle', danger: 'alert-triangle' }
</script>

<template>
  <div class="flex items-start gap-3 rounded-xl border px-4 py-3 text-[13px] leading-relaxed" :class="toneClasses[tone]" role="alert">
    <AppIcon :name="toneIcons[tone]" :size="16" class="mt-0.5 shrink-0" />
    <div class="min-w-0">
      <p v-if="title" class="font-semibold">{{ title }}</p>
      <slot />
    </div>
  </div>
</template>
