<script setup lang="ts">
import { AppIcon, StatusPill } from '@/shared/ui'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/shared/ui/collapsible'

type PillTone = 'success' | 'warning' | 'danger' | 'info' | 'accent' | 'neutral'

/**
 * One expandable row of the wizard's configuration list: an icon, a title, a
 * one-line summary of the current choice, and a status pill — details stay
 * hidden until the row is opened. An optional `control` slot (e.g. a Switch)
 * sits outside the trigger so toggling it never collapses the row.
 */
withDefaults(
  defineProps<{
    icon: string
    title: string
    /** Current-state summary, always visible — the row's one-line answer. */
    summary?: string
    pillLabel?: string
    pillTone?: PillTone
    /** Renders the header without a body: nothing to expand. */
    static?: boolean
  }>(),
  { pillTone: 'neutral' },
)

const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <Collapsible v-model:open="open" :disabled="static">
    <div class="rounded-2xl border bg-surface/80 transition-colors" :class="open ? 'border-outline-strong' : 'border-outline'">
      <div class="flex items-center gap-3 p-4">
        <CollapsibleTrigger as-child>
          <button
            type="button"
            class="group flex min-w-0 flex-1 items-center gap-3 rounded-xl text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-400"
            :class="static ? 'cursor-default' : ''"
          >
            <span class="grid size-10 shrink-0 place-items-center rounded-xl border border-outline bg-white/[0.03] text-accent-300">
              <AppIcon :name="icon" :size="18" />
            </span>
            <span class="min-w-0 flex-1">
              <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span class="text-[13px] font-semibold text-ink">{{ title }}</span>
                <StatusPill v-if="pillLabel" :label="pillLabel" :tone="pillTone" :pulse="false" />
              </span>
              <span v-if="summary" class="mt-0.5 block truncate text-[12px] text-ink-muted">{{ summary }}</span>
            </span>
            <AppIcon
              v-if="!static"
              name="chevron-down"
              :size="16"
              class="shrink-0 text-ink-muted transition-transform group-data-[state=open]:rotate-180"
            />
          </button>
        </CollapsibleTrigger>
        <div v-if="$slots.control" class="shrink-0" @click.stop>
          <slot name="control" />
        </div>
      </div>
      <CollapsibleContent v-if="!static">
        <div class="border-t border-outline px-4 py-4 sm:px-5">
          <slot />
        </div>
      </CollapsibleContent>
    </div>
  </Collapsible>
</template>
