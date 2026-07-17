<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import AppButton from './AppButton.vue'
import AppDialog from './AppDialog.vue'
import AppInput from './AppInput.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    confirmLabel: string
    tone?: 'danger' | 'accent'
    typeToConfirm?: string
    busy?: boolean
  }>(),
  { tone: 'danger' },
)

const emit = defineEmits<{ confirm: []; close: [] }>()

const typed = ref('')

const confirmable = computed(() => !props.typeToConfirm || typed.value === props.typeToConfirm)

watch(
  () => props.open,
  (open) => {
    if (open) typed.value = ''
  },
)

function confirm() {
  if (!confirmable.value || props.busy) return
  emit('confirm')
}
</script>

<template>
  <AppDialog :open="open" :title="title" size="sm" @close="emit('close')">
    <div class="space-y-4">
      <div class="text-[13px] leading-relaxed text-ink-secondary">
        <slot />
      </div>
      <label v-if="typeToConfirm" class="block">
        <span class="mb-1.5 block text-[13px] text-ink-secondary">
          Type
          <span class="rounded bg-white/[0.06] px-1.5 py-0.5 font-mono text-xs font-semibold text-ink">{{
            typeToConfirm
          }}</span>
          to confirm
        </span>
        <AppInput
          v-model="typed"
          class="font-mono"
          autocomplete="off"
          spellcheck="false"
          :placeholder="typeToConfirm"
          @keydown.enter="confirm"
        />
      </label>
    </div>
    <template #footer>
      <AppButton :disabled="busy" @click="emit('close')">Cancel</AppButton>
      <AppButton
        :variant="tone === 'accent' ? 'primary' : 'danger'"
        :loading="busy"
        :disabled="!confirmable"
        @click="confirm"
      >
        {{ confirmLabel }}
      </AppButton>
    </template>
  </AppDialog>
</template>
