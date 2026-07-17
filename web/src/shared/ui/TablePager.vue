<script setup lang="ts">
import { computed } from 'vue'

import AppButton from './AppButton.vue'
import AppSelect from './AppSelect.vue'

const props = withDefaults(
  defineProps<{
    pageCount: number
    /** Rows matching the current search across every page — the "of" number. */
    total: number
    /** 1-based index of the first row on this page; 0 when nothing matches. */
    rangeStart: number
    rangeEnd: number
    label?: string
    pageSizeOptions?: number[]
  }>(),
  { label: 'items', pageSizeOptions: () => [10, 25, 50] },
)

const page = defineModel<number>('page', { required: true })
const pageSize = defineModel<number>('pageSize', { required: true })

/**
 * Up to five page buttons centred on the current page. One button per page
 * would overflow the row as soon as a list outgrows a screenful.
 */
const windowPages = computed(() => {
  const span = Math.min(5, props.pageCount)
  const centred = page.value - Math.floor(span / 2)
  const first = Math.min(Math.max(1, centred), props.pageCount - span + 1)
  return Array.from({ length: span }, (_, index) => first + index)
})
</script>

<template>
  <div class="flex flex-wrap items-center justify-between gap-3">
    <nav v-if="pageCount > 1" class="flex items-center gap-1" aria-label="Pagination">
      <AppButton
        size="sm"
        variant="ghost"
        icon="chevrons-left"
        aria-label="First page"
        :disabled="page <= 1"
        @click="page = 1"
      />
      <AppButton
        size="sm"
        variant="ghost"
        icon="chevron-left"
        aria-label="Previous page"
        :disabled="page <= 1"
        @click="page -= 1"
      />
      <AppButton
        v-for="value in windowPages"
        :key="value"
        size="sm"
        :variant="value === page ? 'primary' : 'ghost'"
        :aria-label="`Page ${value}`"
        :aria-current="value === page ? 'page' : undefined"
        class="min-w-8 tabular-nums"
        @click="page = value"
      >
        {{ value }}
      </AppButton>
      <AppButton
        size="sm"
        variant="ghost"
        icon="chevron-right"
        aria-label="Next page"
        :disabled="page >= pageCount"
        @click="page += 1"
      />
      <AppButton
        size="sm"
        variant="ghost"
        icon="chevrons-right"
        aria-label="Last page"
        :disabled="page >= pageCount"
        @click="page = pageCount"
      />
    </nav>
    <div class="ml-auto flex items-center gap-3">
      <span class="text-[13px] whitespace-nowrap text-ink-muted tabular-nums">
        Shown {{ rangeStart }}–{{ rangeEnd }} of {{ total }} {{ label }}
      </span>
      <label class="flex items-center gap-2 text-[13px] whitespace-nowrap text-ink-muted">
        Rows
        <div class="w-[4.75rem]">
          <AppSelect v-model="pageSize" aria-label="Rows per page">
            <option v-for="option in pageSizeOptions" :key="option" :value="option">{{ option }}</option>
          </AppSelect>
        </div>
      </label>
    </div>
  </div>
</template>
