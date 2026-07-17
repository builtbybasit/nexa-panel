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
  selected?: boolean
}>()

const emit = defineEmits<{ select: [] }>()

function onClick() {
  if (props.clickable) emit('select')
}
</script>

<template>
  <!--
    The row stays a <div> even when clickable: an inner button stretched over
    the row (via its ::before overlay) carries the click/keyboard behavior so
    the action buttons in the #actions slot are never nested inside a button.
  -->
  <div
    :aria-current="selected ? 'true' : undefined"
    class="relative flex w-full items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition-colors"
    :class="[
      clickable ? 'hover:border-outline hover:bg-white/[0.03]' : '',
      selected ? 'border-accent-400/40 bg-raised ring-1 ring-accent-400/25' : 'border-transparent',
    ]"
  >
    <span
      class="grid size-9 shrink-0 place-items-center rounded-lg border border-outline bg-white/[0.03] text-[13px] font-bold text-accent-300"
    >
      <AppIcon v-if="icon" :name="icon" :size="16" />
      <template v-else>{{ avatar }}</template>
    </span>
    <component
      :is="clickable ? 'button' : 'span'"
      :type="clickable ? 'button' : undefined"
      class="min-w-0 flex-1 text-left"
      :class="
        clickable
          ? 'cursor-pointer outline-none before:absolute before:inset-0 before:rounded-xl focus-visible:before:ring-2 focus-visible:before:ring-accent-400/60'
          : ''
      "
      @click="onClick"
    >
      <strong class="block truncate text-sm font-semibold text-ink">{{ title }}</strong>
      <small v-if="subtitle" class="block truncate text-xs text-ink-muted">{{ subtitle }}</small>
    </component>
    <!-- meta/actions/status sit above the overlay so tooltips and buttons keep working -->
    <span v-if="$slots.meta" class="relative flex shrink-0 items-center gap-1.5">
      <slot name="meta" />
    </span>
    <span v-if="$slots.actions" class="relative flex shrink-0 items-center gap-1.5" @click.stop>
      <slot name="actions" />
    </span>
    <span v-if="$slots.status" class="relative flex shrink-0 items-center gap-1.5">
      <slot name="status" />
    </span>
  </div>
</template>
