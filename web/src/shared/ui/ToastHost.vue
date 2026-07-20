<script setup lang="ts">
import { RouterLink } from 'vue-router'

import { pauseToastTimers, resumeToastTimers, useToasts } from '@/shared/composables/useToasts'

import AppIcon from './AppIcon.vue'

const { toasts, dismiss } = useToasts()

/**
 * Dismissing the hovered toast removes it before mouseleave can fire, which
 * would leave the pause latched; resume the remaining timers explicitly.
 */
function dismissAndResume(id: number) {
  dismiss(id)
  resumeToastTimers()
}

/* Tone treatments mirror AppAlert; the tint sits on a solid raised base so
 * toasts stay readable over any page content. */
const toneShell = {
  info: 'border-sky-400/25',
  success: 'border-emerald-400/25',
  warning: 'border-amber-400/25',
  danger: 'border-rose-400/25',
}

const toneBody = {
  info: 'bg-sky-400/[0.07] text-sky-200',
  success: 'bg-emerald-400/[0.07] text-emerald-200',
  warning: 'bg-amber-400/[0.07] text-amber-200',
  danger: 'bg-rose-400/[0.07] text-rose-200',
}

const toneIcons = { info: 'info', success: 'check', warning: 'alert-triangle', danger: 'alert-triangle' }
</script>

<template>
  <div
    class="pointer-events-none fixed right-4 bottom-4 z-[1000] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2"
    aria-live="polite"
  >
    <TransitionGroup
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-y-2 opacity-0"
      enter-to-class="translate-y-0 opacity-100"
      leave-active-class="transition duration-150 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
      move-class="transition duration-200"
    >
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="pointer-events-auto overflow-hidden rounded-xl border bg-raised shadow-[0_16px_40px_-16px_rgba(0,0,0,0.7)]"
        :class="toneShell[toast.tone]"
        @mouseenter="pauseToastTimers"
        @mouseleave="resumeToastTimers"
        @focusin="pauseToastTimers"
        @focusout="resumeToastTimers"
      >
        <div class="flex items-start gap-3 px-4 py-3 text-[13px] leading-relaxed" :class="toneBody[toast.tone]">
          <AppIcon :name="toneIcons[toast.tone]" :size="16" class="mt-0.5 shrink-0" />
          <div class="min-w-0 flex-1">
            <p class="font-semibold">{{ toast.title }}</p>
            <p v-if="toast.body" class="mt-0.5 opacity-80">{{ toast.body }}</p>
            <RouterLink
              v-if="toast.to"
              :to="toast.to"
              class="mt-1 inline-block font-semibold underline underline-offset-2 hover:opacity-80"
              @click="dismissAndResume(toast.id)"
            >
              {{ toast.toLabel ?? 'View' }}
            </RouterLink>
          </div>
          <button
            type="button"
            class="-mt-0.5 -mr-1.5 grid size-6 shrink-0 place-items-center rounded-md opacity-70 transition-opacity hover:opacity-100"
            aria-label="Dismiss notification"
            @click="dismissAndResume(toast.id)"
          >
            <AppIcon name="x" :size="14" />
          </button>
        </div>
      </div>
    </TransitionGroup>
  </div>
</template>
