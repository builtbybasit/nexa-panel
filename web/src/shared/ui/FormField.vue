<script setup lang="ts">
import { computed, provide, useId } from 'vue'

import { formFieldContextKey } from './formFieldContext'

const props = defineProps<{
  label: string
  hint?: string
  error?: string
}>()

const id = useId()
const labelId = `${id}-label`
const messageId = `${id}-message`
const descriptionId = computed(() => (props.error || props.hint ? messageId : undefined))
provide(formFieldContextKey, { labelId, descriptionId })
</script>

<template>
  <div class="block">
    <span :id="labelId" class="mb-1.5 block text-[13px] font-medium text-ink-secondary">{{ label }}</span>
    <slot />
    <span v-if="error" :id="messageId" role="alert" class="mt-1.5 block text-xs leading-relaxed text-rose-300">{{ error }}</span>
    <span v-else-if="hint" :id="messageId" class="mt-1.5 block text-xs leading-relaxed text-ink-muted">{{ hint }}</span>
  </div>
</template>
