<script setup lang="ts">
import AppIcon from './AppIcon.vue'

const props = defineProps<{
  title: string
  subtitle?: string
  /** Single character or short text used as the leading avatar. */
  avatar?: string
  /** Icon name used instead of the avatar text. */
  icon?: string
  clickable?: boolean
}>()

const emit = defineEmits<{ select: [] }>()

function onClick() {
  if (props.clickable) emit('select')
}
</script>

<template>
  <component
    :is="clickable ? 'button' : 'div'"
    :type="clickable ? 'button' : undefined"
    class="flex w-full items-center gap-3 rounded-xl border border-transparent px-3 py-2.5 text-left transition-colors"
    :class="clickable ? 'cursor-pointer hover:border-outline hover:bg-white/[0.03]' : ''"
    @click="onClick"
  >
    <span
      class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-[13px] font-bold text-accent-300"
    >
      <AppIcon v-if="icon" :name="icon" :size="16" />
      <template v-else>{{ avatar }}</template>
    </span>
    <span class="min-w-0 flex-1">
      <strong class="block truncate text-sm font-semibold text-ink">{{ title }}</strong>
      <small v-if="subtitle" class="block truncate text-xs text-ink-muted">{{ subtitle }}</small>
    </span>
    <slot name="meta" />
    <span v-if="$slots.actions" class="flex shrink-0 items-center gap-1.5" @click.stop>
      <slot name="actions" />
    </span>
    <slot name="status" />
  </component>
</template>
