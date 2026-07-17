<script setup lang="ts">
import { ref, watch } from 'vue'

import AppButton from './AppButton.vue'
import AppCard from './AppCard.vue'
import FactList, { type Fact } from './FactList.vue'

const props = defineProps<{
  credential: string
  /** Names the account this credential belongs to; also used as the download filename. */
  accountLabel?: string
  facts?: Fact[]
}>()
const emit = defineEmits<{ clear: [] }>()

const copied = ref(false)
const copyFailed = ref(false)
const secretEl = ref<HTMLElement>()

watch(
  () => props.credential,
  () => {
    copied.value = false
    copyFailed.value = false
  },
)

async function copy() {
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(props.credential)
    copied.value = true
  } catch {
    copied.value = false
    copyFailed.value = true
    selectSecret()
  }
}

function selectSecret() {
  const node = secretEl.value
  if (!node) return
  const range = document.createRange()
  range.selectNodeContents(node)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
}

function download() {
  const blob = new Blob([props.credential], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = props.accountLabel ? `${props.accountLabel.replace(/[^\w.-]+/g, '-')}.txt` : 'credential.txt'
  link.click()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <AppCard eyebrow="One-time credential" title="Copy this password now" class="border-amber-400/30">
    <template #actions>
      <AppButton size="sm" :icon="copied ? 'check' : 'copy'" @click="copy()">
        {{ copied ? 'Copied' : 'Copy' }}
      </AppButton>
      <AppButton size="sm" icon="download" @click="download()">Download .txt</AppButton>
      <AppButton size="sm" variant="ghost" icon="x" @click="emit('clear')">Clear</AppButton>
    </template>
    <p class="mb-3 text-[13px] text-amber-200">
      It cannot be revealed again. Store it in your password manager before leaving this page.
    </p>
    <p v-if="accountLabel" class="mb-2 text-[13px] text-ink-secondary">
      For <span class="font-medium text-ink">{{ accountLabel }}</span>
    </p>
    <code
      ref="secretEl"
      class="block rounded-lg border border-outline bg-canvas/70 px-3 py-2.5 font-mono text-[13px] break-all text-accent-200"
    >
      {{ credential }}
    </code>
    <p v-if="copyFailed" role="alert" class="mt-2 text-xs text-rose-300">
      Copy failed. The password above is selected — press Ctrl+C or Cmd+C to copy it.
    </p>
    <FactList v-if="facts && facts.length" :facts="facts" class="mt-4" />
  </AppCard>
</template>
