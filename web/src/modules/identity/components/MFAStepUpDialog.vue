<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

import { registerMFAStepUpHandler } from '@/shared/api/mfaStepUp'
import { AppAlert, AppButton, AppDialog, AppInput, FormField } from '@/shared/ui'

import { useIdentityStore } from '../store'

const identity = useIdentityStore()
const open = ref(false)
const code = ref('')
const recovery = ref(false)
const error = ref('')
const busy = ref(false)

let settle: { resolve: () => void; reject: (reason: Error) => void } | undefined
let unregister: (() => void) | undefined

function prompt(): Promise<void> {
  code.value = ''
  recovery.value = false
  error.value = ''
  open.value = true
  return new Promise<void>((resolve, reject) => {
    settle = { resolve, reject }
  })
}

async function verify() {
  busy.value = true
  error.value = ''
  try {
    await identity.verify(code.value, recovery.value)
    open.value = false
    settle?.resolve()
    settle = undefined
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'Verification failed.'
  } finally {
    busy.value = false
  }
}

function cancel() {
  if (busy.value || !open.value) return
  open.value = false
  settle?.reject(new Error('MFA step-up was cancelled.'))
  settle = undefined
}

function toggleRecovery() {
  recovery.value = !recovery.value
  code.value = ''
  error.value = ''
}

onMounted(() => {
  unregister = registerMFAStepUpHandler(prompt)
})

onBeforeUnmount(() => {
  unregister?.()
  settle?.reject(new Error('MFA step-up is unavailable.'))
})
</script>

<template>
  <AppDialog
    :open="open"
    title="Verify this privileged action"
    description="Enter a fresh authenticator or recovery code to continue."
    size="sm"
    @close="cancel"
  >
    <form id="mfa-step-up-form" class="space-y-4" @submit.prevent="verify">
      <p class="text-[13px] leading-relaxed text-ink-secondary">
        Your administrator verification is older than ten minutes. Enter a fresh authenticator code to continue.
      </p>
      <FormField :label="recovery ? 'Recovery code' : 'Six-digit code'">
        <AppInput
          v-model.trim="code"
          name="step-up-code"
          class="text-center font-mono text-lg tracking-[0.3em]"
          :inputmode="recovery ? 'text' : 'numeric'"
          autocomplete="one-time-code"
          :pattern="recovery ? undefined : '[0-9]{6}'"
          :maxlength="recovery ? 19 : 6"
          required
        />
      </FormField>
      <button type="button" class="text-[13px] font-medium text-accent-300 hover:text-accent-200" @click="toggleRecovery">
        {{ recovery ? 'Use authenticator instead' : 'Use a recovery code' }}
      </button>
      <AppAlert v-if="error" tone="danger">{{ error }}</AppAlert>
    </form>

    <template #footer>
      <AppButton :disabled="busy" @click="cancel">Cancel</AppButton>
      <AppButton variant="primary" type="submit" form="mfa-step-up-form" :loading="busy" :disabled="!code">
        Verify and continue
      </AppButton>
    </template>
  </AppDialog>
</template>
