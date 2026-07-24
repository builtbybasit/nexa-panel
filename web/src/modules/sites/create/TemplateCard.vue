<script setup lang="ts">
import { AppIcon, StatusPill } from '@/shared/ui'

import type { SiteTemplate } from './templates'

defineProps<{
  template: SiteTemplate
  selected?: boolean
}>()

const emit = defineEmits<{ select: [] }>()
</script>

<template>
  <button
    type="button"
    :disabled="!template.available"
    class="group relative flex flex-col rounded-2xl border bg-surface/80 p-4 text-left transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400"
    :class="[
      selected ? 'border-accent-400/60' : 'border-outline',
      template.available
        ? 'cursor-pointer hover:border-accent-400/40 hover:bg-white/[0.03]'
        : 'cursor-not-allowed opacity-60',
    ]"
    :aria-label="template.available ? `Create a ${template.name} site` : `${template.name} — coming soon`"
    @click="template.available && emit('select')"
  >
    <!-- Abstract browser illustration; the badge names the stack. -->
    <span class="relative block overflow-hidden rounded-xl border border-outline bg-canvas/60" aria-hidden="true">
      <span class="flex items-center gap-1.5 border-b border-outline bg-white/[0.02] px-2.5 py-2">
        <span class="size-1.5 rounded-full bg-rose-400/50" />
        <span class="size-1.5 rounded-full bg-amber-400/50" />
        <span class="size-1.5 rounded-full bg-emerald-400/50" />
        <span class="ml-1 h-1.5 flex-1 rounded-full bg-white/[0.05]" />
      </span>
      <span class="block space-y-1.5 p-3 pb-4">
        <span class="block h-1.5 w-2/3 rounded-full bg-white/[0.07]" />
        <span class="block h-1.5 w-full rounded-full bg-white/[0.04]" />
        <span class="block h-1.5 w-4/5 rounded-full bg-white/[0.04]" />
      </span>
      <span
        class="absolute right-2.5 bottom-2 grid size-9 place-items-center rounded-full border border-outline-strong bg-surface text-accent-300 shadow transition-colors group-hover:border-accent-400/40 group-hover:text-accent-200"
      >
        <AppIcon :name="template.icon" :size="17" />
      </span>
    </span>

    <span class="mt-3 flex items-center gap-2">
      <span class="text-[14px] font-semibold text-ink">{{ template.name }}</span>
      <StatusPill v-if="!template.available" label="Coming soon" tone="neutral" :pulse="false" />
      <StatusPill v-else-if="selected" label="Selected" tone="accent" :pulse="false" />
    </span>
    <span class="mt-1 text-[12px] leading-relaxed text-ink-muted">{{ template.tagline }}</span>
  </button>
</template>
