<script setup lang="ts">
import { ref } from 'vue'

import AppButton from './AppButton.vue'
import AppCard from './AppCard.vue'

defineProps<{ credential: string }>()
const emit = defineEmits<{ clear: [] }>()

const copied = ref(false)

async function copy(credential: string) {
  try {
    await navigator.clipboard.writeText(credential)
    copied.value = true
  } catch {
    copied.value = false
  }
}
</script>

<template>
  <AppCard eyebrow="One-time credential" title="Copy this password now" class="border-amber-400/30">
    <template #actions>
      <AppButton size="sm" :icon="copied ? 'check' : 'copy'" @click="copy(credential)">
        {{ copied ? 'Copied' : 'Copy' }}
      </AppButton>
      <AppButton size="sm" variant="ghost" icon="x" @click="emit('clear')">Clear</AppButton>
    </template>
    <p class="mb-3 text-[13px] text-amber-200">
      It cannot be revealed again. Store it in your password manager before leaving this page.
    </p>
    <code class="block rounded-lg border border-outline bg-canvas/70 px-3 py-2.5 font-mono text-[13px] break-all text-accent-200">
      {{ credential }}
    </code>
  </AppCard>
</template>
