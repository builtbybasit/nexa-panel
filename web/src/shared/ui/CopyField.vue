<script setup lang="ts">
import { ref, watch } from 'vue'

import AppButton from './AppButton.vue'

const props = defineProps<{
  label: string
  value: string
  hint?: string
}>()

const copied = ref(false)
const copyFailed = ref(false)
const valueEl = ref<HTMLElement>()
let resetHandle: ReturnType<typeof setTimeout> | undefined

watch(
  () => props.value,
  () => {
    copied.value = false
    copyFailed.value = false
  },
)

async function copy() {
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(props.value)
    copied.value = true
    clearTimeout(resetHandle)
    resetHandle = setTimeout(() => (copied.value = false), 2000)
  } catch {
    // Clipboard access is denied over plain HTTP and in some embedded views;
    // selecting the text lets the keyboard shortcut finish the job.
    copied.value = false
    copyFailed.value = true
    selectValue()
  }
}

function selectValue() {
  const node = valueEl.value
  if (!node) return
  const range = document.createRange()
  range.selectNodeContents(node)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
}
</script>

<template>
  <div class="min-w-0">
    <p class="text-[11px] font-bold tracking-[0.1em] text-ink-muted uppercase">{{ label }}</p>
    <div class="mt-1 flex items-start gap-2">
      <code
        ref="valueEl"
        class="min-w-0 flex-1 rounded-lg border border-outline bg-canvas/70 px-3 py-2 font-mono text-xs break-all text-accent-200"
      >
        {{ value }}
      </code>
      <AppButton
        size="sm"
        variant="ghost"
        :icon="copied ? 'check' : 'copy'"
        :aria-label="`Copy ${label}`"
        @click="copy()"
      >
        {{ copied ? 'Copied' : 'Copy' }}
      </AppButton>
    </div>
    <p v-if="copyFailed" role="alert" class="mt-1 text-xs text-rose-300">
      Copy failed. The value is selected — press Ctrl+C or Cmd+C.
    </p>
    <p v-else-if="hint" class="mt-1 text-xs leading-relaxed text-ink-muted">{{ hint }}</p>
  </div>
</template>
