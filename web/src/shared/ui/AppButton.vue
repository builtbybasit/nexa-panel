<script setup lang="ts">
import AppIcon from './AppIcon.vue'

withDefaults(
  defineProps<{
    variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
    size?: 'sm' | 'md'
    type?: 'button' | 'submit'
    icon?: string
    loading?: boolean
    disabled?: boolean
  }>(),
  { variant: 'secondary', size: 'md', type: 'button' },
)

const variantClasses = {
  primary:
    'bg-accent-500 text-accent-950 font-semibold hover:bg-accent-400 shadow-[0_8px_24px_-8px] shadow-accent-500/50',
  secondary: 'border border-outline-strong bg-white/[0.03] text-ink hover:bg-white/[0.07] hover:border-outline-strong',
  ghost: 'text-ink-secondary hover:text-ink hover:bg-white/[0.05]',
  danger: 'border border-rose-500/40 bg-rose-500/10 text-rose-300 hover:bg-rose-500/20',
}

const sizeClasses = {
  sm: 'h-8 px-3 text-[13px] gap-1.5',
  md: 'h-10 px-4 text-sm gap-2',
}
</script>

<template>
  <button
    :type="type"
    :disabled="disabled || loading"
    class="inline-flex items-center justify-center rounded-lg font-medium transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-50"
    :class="[variantClasses[variant], sizeClasses[size]]"
  >
    <span
      v-if="loading"
      class="size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
      aria-hidden="true"
    />
    <AppIcon v-else-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" />
    <slot />
  </button>
</template>
