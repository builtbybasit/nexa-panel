<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import AppInput from './AppInput.vue'

withDefaults(
  defineProps<{
    search: string
    count?: number
    countLabel?: string
    placeholder?: string
  }>(),
  { placeholder: 'Search', countLabel: 'items' },
)

const emit = defineEmits<{ 'update:search': [value: string] }>()
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <div class="relative min-w-0 flex-1 basis-52 sm:max-w-xs">
      <AppIcon
        name="search"
        :size="15"
        class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-ink-muted"
      />
      <AppInput
        :model-value="search"
        class="pl-9"
        :placeholder="placeholder"
        aria-label="Search"
        autocomplete="off"
        @update:model-value="emit('update:search', String($event ?? ''))"
      />
    </div>
    <slot name="filters" />
    <div v-if="count !== undefined || $slots.actions" class="ml-auto flex items-center gap-2">
      <span v-if="count !== undefined" class="text-[13px] whitespace-nowrap text-ink-muted tabular-nums">
        {{ count }} {{ countLabel }}
      </span>
      <slot name="actions" />
    </div>
  </div>
</template>
