<script setup lang="ts">
import { computed } from 'vue'

import { AppIcon } from '@/shared/ui'

import { crumbsOf, parentOf } from '../lib'

const props = defineProps<{ path: string }>()
const emit = defineEmits<{ navigate: [path: string] }>()

const crumbs = computed(() => crumbsOf(props.path))
</script>

<template>
  <div class="flex items-center gap-1 border-b border-outline px-2 py-1.5">
    <button
      class="inline-flex h-7 w-7 items-center justify-center rounded-lg text-ink-secondary transition-colors hover:bg-white/[0.06] hover:text-ink disabled:cursor-not-allowed disabled:opacity-30"
      :disabled="path === '.'"
      aria-label="Up one directory"
      title="Up one directory"
      @click="emit('navigate', parentOf(path))"
    >
      <AppIcon name="corner-left-up" :size="15" />
    </button>
    <nav class="flex min-w-0 flex-1 items-center gap-0.5 font-mono text-[13px]" aria-label="Path">
      <button
        class="rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
        :class="path === '.' ? 'text-ink' : 'text-ink-secondary'"
        @click="emit('navigate', '.')"
      >
        /
      </button>
      <template v-for="(crumb, index) in crumbs" :key="crumb.path">
        <AppIcon v-if="index > 0" name="chevron-right" :size="12" class="shrink-0 text-ink-muted" />
        <button
          class="truncate rounded px-1.5 py-0.5 transition-colors hover:bg-white/[0.05] hover:text-ink"
          :class="index === crumbs.length - 1 ? 'text-ink' : 'text-ink-secondary'"
          @click="emit('navigate', crumb.path)"
        >
          {{ crumb.name }}
        </button>
      </template>
    </nav>
  </div>
</template>
